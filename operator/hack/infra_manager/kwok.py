# /*
# Copyright 2026 The Grove Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
# */

"""KWOK controller installation, node manifests, and batch creation."""

from __future__ import annotations

import tempfile
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

import yaml
from rich.panel import Panel
from tenacity import RetryError, retry, retry_if_result, stop_after_attempt, wait_exponential

from infra_manager import console, logger
from infra_manager.config import KwokConfig
from infra_manager.constants import (
    DEFAULT_KWOK_VERSION,
    KWOK_ANNOTATION_KEY,
    KWOK_CONTROLLER_DEPLOYMENT,
    KWOK_FAKE_NODE_TAINT_KEY,
    KWOK_IP_OCTET_SIZE,
    KWOK_IP_PREFIX,
    KWOK_MANIFESTS,
    LABEL_BLOCK,
    LABEL_HOSTNAME,
    LABEL_RACK,
    LABEL_TYPE,
    LABEL_TYPE_KWOK,
    LABEL_ZONE,
    NODE_CONDITIONS,
    NODES_PER_BLOCK,
    NODES_PER_RACK,
    NODES_PER_ZONE,
    NS_KUBE_SYSTEM,
    dep_value,
)
from infra_manager.utils import kwok_release_url, run_kubectl


def topology_labels(node_id: int) -> dict[str, str]:
    """Compute topology labels for a KWOK node using multi-zone index arithmetic.

    Args:
        node_id: Zero-based KWOK node index.

    Returns:
        Dictionary of Kubernetes label key-value pairs.
    """
    return {
        LABEL_ZONE: f"zone-{node_id // NODES_PER_ZONE}",
        LABEL_BLOCK: f"block-{node_id // NODES_PER_BLOCK}",
        LABEL_RACK: f"rack-{node_id // NODES_PER_RACK}",
        LABEL_HOSTNAME: f"kwok-node-{node_id}",
        LABEL_TYPE: LABEL_TYPE_KWOK,
    }


def node_manifest(node_id: int, kwok_cfg: KwokConfig) -> dict:
    """Build the Kubernetes Node manifest for a KWOK node.

    Args:
        node_id: Zero-based KWOK node index.
        kwok_cfg: KWOK configuration with CPU, memory, and pod limits.

    Returns:
        Kubernetes Node resource as a dictionary ready for YAML serialization.
    """
    name = f"kwok-node-{node_id}"
    resources = {
        "cpu": kwok_cfg.kwok_node_cpu,
        "memory": kwok_cfg.kwok_node_memory,
        "pods": str(kwok_cfg.kwok_max_pods),
    }
    return {
        "apiVersion": "v1",
        "kind": "Node",
        "metadata": {
            "name": name,
            "labels": topology_labels(node_id),
            "annotations": {
                KWOK_ANNOTATION_KEY: "fake",
                "node.alpha.kubernetes.io/ttl": "0",
            },
        },
        "spec": {
            "taints": [{"effect": "NoSchedule", "key": KWOK_FAKE_NODE_TAINT_KEY, "value": "true"}]
        },
        "status": {
            "capacity": resources,
            "allocatable": resources,
            "conditions": NODE_CONDITIONS,
            "addresses": [
                {"type": "InternalIP",
                 "address": f"{KWOK_IP_PREFIX}.{node_id // KWOK_IP_OCTET_SIZE}.{node_id % KWOK_IP_OCTET_SIZE}"}
            ],
        },
    }


def install_kwok_controller(version: str, timeout: int = 120) -> None:
    """Install KWOK controller and wait for it to be available.

    Args:
        version: KWOK release version tag (e.g. ``v0.7.0``).
        timeout: Maximum seconds to wait for the controller deployment.

    Raises:
        RuntimeError: If the KWOK controller is not ready within the timeout.
    """
    console.print(Panel.fit(f"Installing KWOK controller ({version})", style="bold blue"))
    base_url = kwok_release_url(version)
    for manifest in KWOK_MANIFESTS:
        ok, _, stderr = run_kubectl(["apply", "-f", f"{base_url}/{manifest}"], timeout=60)
        if not ok:
            console.print(f"[yellow]\u26a0\ufe0f  Partial failure applying {manifest}: {stderr[:200]}[/yellow]")
            console.print("[yellow]   (This is usually safe if KWOK is already installed)[/yellow]")

    console.print("[yellow]\u2139\ufe0f  Waiting for KWOK controller to be available...[/yellow]")
    ok, _, stderr = run_kubectl([
        "wait", "--for=condition=Available",
        f"deployment/{KWOK_CONTROLLER_DEPLOYMENT}", "-n", NS_KUBE_SYSTEM,
        f"--timeout={timeout}s"
    ], timeout=timeout + 10)
    if not ok:
        raise RuntimeError(f"KWOK controller not ready after {timeout}s: {stderr[:200]}")
    console.print("[green]\u2705 KWOK controller installed and ready[/green]")


@retry(
    stop=stop_after_attempt(3),
    wait=wait_exponential(multiplier=1, min=1, max=8),
    retry=retry_if_result(lambda ok: not ok),
)
def _kubectl_apply(tmp_name: str) -> bool:
    """Apply a manifest file via kubectl with retry logic.

    Args:
        tmp_name: Path to the temporary YAML manifest file.

    Returns:
        True if the apply succeeded or the resource already exists.
    """
    ok, _, stderr = run_kubectl(["apply", "-f", tmp_name])
    return ok or "AlreadyExists" in stderr


def create_node_batch(
    node_ids: list[int], kwok_cfg: KwokConfig
) -> tuple[list[int], list[tuple[int, str]]]:
    """Create a batch of KWOK nodes with a single kubectl apply.

    Args:
        node_ids: List of zero-based KWOK node indices to create.
        kwok_cfg: KWOK configuration with CPU, memory, and pod limits.

    Returns:
        Tuple of (successful_node_ids, failed_node_id_error_pairs).
    """
    combined_yaml = "---\n".join(
        yaml.dump(node_manifest(nid, kwok_cfg), default_flow_style=False)
        for nid in node_ids
    )

    tmp = tempfile.NamedTemporaryFile(delete=False, suffix=".yaml")
    try:
        tmp.write(combined_yaml.encode())
        tmp.flush()
        tmp.close()

        try:
            success = _kubectl_apply(tmp.name)
        except RetryError:
            success = False

        if success:
            return node_ids, []
        return [], [(nid, "kubectl apply failed") for nid in node_ids]
    finally:
        Path(tmp.name).unlink(missing_ok=True)


def _wait_kwok_nodes_ready(timeout: int = 120) -> None:
    """Wait for all KWOK nodes to reach Ready condition.

    Args:
        timeout: Maximum seconds to wait for nodes to be ready.
    """
    console.print("[yellow]\u2139\ufe0f  Waiting for KWOK nodes to be ready...[/yellow]")
    ok, _, stderr = run_kubectl([
        "wait", "--for=condition=Ready", "nodes",
        "-l", f"{LABEL_TYPE}={LABEL_TYPE_KWOK}",
        f"--timeout={timeout}s",
    ], timeout=timeout + 10)
    if not ok:
        console.print(f"[yellow]\u26a0\ufe0f  Some KWOK nodes may not be ready: {stderr[:200]}[/yellow]")
    else:
        console.print("[green]\u2705 All KWOK nodes are ready[/green]")


def create_nodes(total: int, kwok_cfg: KwokConfig) -> None:
    """Create KWOK nodes in parallel batches.

    Args:
        total: Total number of KWOK nodes to create.
        kwok_cfg: KWOK configuration with batch size and node resource limits.
    """
    logger.info("Creating %d KWOK nodes (batch size=%d)...", total, kwok_cfg.kwok_batch_size)
    successes: list[int] = []
    failures: list[tuple[int, str]] = []

    batches = []
    for batch_start in range(0, total, kwok_cfg.kwok_batch_size):
        batch_end = min(batch_start + kwok_cfg.kwok_batch_size, total)
        batches.append(list(range(batch_start, batch_end)))

    max_workers = min(len(batches), 5)
    with ThreadPoolExecutor(max_workers=max_workers) as executor:
        futures = {
            executor.submit(create_node_batch, node_ids, kwok_cfg): node_ids
            for node_ids in batches
        }
        for future in as_completed(futures):
            node_ids = futures[future]
            batch_ok, batch_fail = future.result()
            successes.extend(batch_ok)
            failures.extend(batch_fail)
            logger.info("Batch %d-%d: success=%d, failed=%d",
                        node_ids[0], node_ids[-1], len(batch_ok), len(batch_fail))

    if failures:
        console.print(f"[yellow]\u26a0\ufe0f  {len(failures)} nodes failed to create[/yellow]")
        for nid, err in failures[:10]:
            logger.error("  kwok-node-%d: %s", nid, err)
    console.print(f"[green]\u2705 Created {len(successes)} KWOK nodes[/green]")
    _wait_kwok_nodes_ready()


def delete_kwok_nodes() -> None:
    """Delete all KWOK simulated nodes and uninstall the KWOK controller.

    Raises:
        RuntimeError: If node deletion fails.
    """
    console.print("[yellow]\u2139\ufe0f  Deleting all KWOK nodes...[/yellow]")
    ok, _, stderr = run_kubectl(
        ["delete", "nodes", "-l", f"{LABEL_TYPE}={LABEL_TYPE_KWOK}", "--ignore-not-found"],
        timeout=300,
    )
    if ok:
        console.print("[green]\u2705 KWOK nodes deleted[/green]")
    else:
        raise RuntimeError(f"Failed to delete KWOK nodes: {stderr[:200]}")

    console.print("[yellow]\u2139\ufe0f  Uninstalling KWOK controller...[/yellow]")
    kwok_version = dep_value("kwok_controller", "version", default=DEFAULT_KWOK_VERSION)
    base_url = kwok_release_url(kwok_version)
    for manifest in reversed(KWOK_MANIFESTS):
        run_kubectl(["delete", "-f", f"{base_url}/{manifest}", "--ignore-not-found"], timeout=120)
    console.print("[green]\u2705 KWOK controller uninstalled[/green]")
