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

"""k3d cluster lifecycle, image pre-pulling, and topology labels."""

from __future__ import annotations

import time
from concurrent.futures import ThreadPoolExecutor, as_completed

import docker
import sh
from rich.panel import Panel
from rich.progress import BarColumn, Progress, SpinnerColumn, TaskProgressColumn, TextColumn
from tenacity import retry, stop_after_attempt, wait_fixed

from infra_manager import console
from infra_manager.config import ClusterConfig
from infra_manager.constants import (
    CLUSTER_CREATE_RETRY_WAIT_SECONDS,
    CLUSTER_TIMEOUT,
    DEFAULT_IMAGE_PULL_MAX_WORKERS,
    parse_memory_mb,
    E2E_NODE_ROLE_KEY,
    LABEL_BLOCK,
    LABEL_CONTROL_PLANE,
    LABEL_RACK,
    LABEL_TYPE,
    LABEL_TYPE_KWOK,
    LABEL_ZONE,
    NODES_PER_BLOCK,
    NODES_PER_RACK,
    NODES_PER_ZONE,
)

# ============================================================================
# Image pre-pulling functions
# ============================================================================


def _pull_tag_push(
    docker_client: docker.DockerClient,
    image_name: str,
    registry_port: int,
    version: str,
) -> tuple[str, bool, str | None]:
    """Pull, tag, and push a single image to the local k3d registry."""
    full_image = f"{image_name}:{version}"
    registry_image = f"localhost:{registry_port}/{image_name}:{version}"
    try:
        docker_client.images.pull(full_image)
        image = docker_client.images.get(full_image)
        image.tag(registry_image)
        docker_client.images.push(registry_image, stream=False)
        return (image_name, True, None)
    except docker.errors.ImageNotFound:
        return (image_name, False, "Image not found")
    except docker.errors.APIError as e:
        return (image_name, False, f"Docker API error: {e}")
    except Exception as e:
        return (image_name, False, str(e))


def prepull_images(images: list[str], registry_port: int, version: str) -> None:
    """Pre-pull images in parallel and push them to the local k3d registry."""
    if not images:
        return
    prepull_image_groups([(images, version)], registry_port)


def _run_parallel_pulls_versioned(
    docker_client: docker.DockerClient,
    items: list[tuple[str, str]],
    registry_port: int,
) -> list[str]:
    """Pull images in parallel where each item carries its own version.

    Args:
        docker_client: Docker client instance.
        items: List of (image_name, version) tuples.
        registry_port: Local k3d registry port.

    Returns:
        List of failed image names.
    """
    failed_images: list[str] = []
    with Progress(
        SpinnerColumn(),
        TextColumn("[progress.description]{task.description}"),
        BarColumn(),
        TaskProgressColumn(),
        console=console,
    ) as progress:
        task = progress.add_task("[cyan]Pulling images...", total=len(items))
        with ThreadPoolExecutor(max_workers=DEFAULT_IMAGE_PULL_MAX_WORKERS) as executor:
            futures = {
                executor.submit(_pull_tag_push, docker_client, img, registry_port, ver): img for img, ver in items
            }
            for future in as_completed(futures):
                image_name, success, error = future.result()
                progress.advance(task)
                if success:
                    console.print(f"[green]\u2713 {image_name}[/green]")
                else:
                    console.print(f"[red]\u2717 {image_name} - {error}[/red]")
                    failed_images.append(image_name)
    return failed_images


def prepull_image_groups(
    groups: list[tuple[list[str], str]],
    registry_port: int,
) -> None:
    """Pre-pull multiple image groups in a single batch.

    Args:
        groups: List of (images, version) tuples to pull.
        registry_port: Local k3d registry port.
    """
    items: list[tuple[str, str]] = [(img, version) for images, version in groups for img in images]

    if not items:
        return

    console.print(Panel.fit("Pre-pulling images to local registry", style="bold blue"))
    console.print(f"[yellow]Pre-pulling {len(items)} images in parallel (this speeds up cluster startup)...[/yellow]")

    try:
        docker_client = docker.from_env()
    except Exception as e:
        console.print(f"[yellow]\u26a0\ufe0f  Failed to connect to Docker: {e}[/yellow]")
        console.print("[yellow]\u26a0\ufe0f  Skipping image pre-pull (cluster will pull images on-demand)[/yellow]")
        return

    try:
        failed_images = _run_parallel_pulls_versioned(docker_client, items, registry_port)
    finally:
        docker_client.close()

    if failed_images:
        console.print(f"[yellow]\u26a0\ufe0f  Failed to pre-pull {len(failed_images)} images[/yellow]")
        console.print("[yellow]   Cluster will pull these images on-demand (may be slower)[/yellow]")
    else:
        console.print(f"[green]\u2705 Successfully pre-pulled all {len(items)} images[/green]")


# ============================================================================
# Cluster operations
# ============================================================================


def _get_system_total_memory_ki() -> int | None:
    """Read MemTotal from /proc/meminfo (KiB). Returns None on non-Linux systems."""
    try:
        with open("/proc/meminfo") as f:
            for line in f:
                if line.startswith("MemTotal:"):
                    return int(line.split()[1])
    except FileNotFoundError:
        return None
    return None


def delete_cluster(cfg: ClusterConfig) -> None:
    """Delete the k3d cluster.

    Args:
        cfg: k3d cluster configuration with the cluster name.
    """
    console.print(f"[yellow]\u2139\ufe0f  Deleting k3d cluster '{cfg.name}'...[/yellow]")
    try:
        sh.k3d("cluster", "delete", cfg.name)
        console.print(f"[green]\u2705 Cluster '{cfg.name}' deleted[/green]")
    except sh.ErrorReturnCode_1:
        console.print(f"[yellow]\u26a0\ufe0f  Cluster '{cfg.name}' not found or already deleted[/yellow]")


def create_cluster(cfg: ClusterConfig) -> None:
    """Create a k3d cluster with retry logic.

    Args:
        cfg: k3d cluster configuration including retry count.

    Raises:
        RetryError: If the cluster cannot be created after all retries.
    """
    console.print(Panel.fit("Creating k3d cluster", style="bold blue"))

    # In DinD, --agents-memory is broken (DinD bind-mounts /proc/meminfo from the host,
    # so k3d sees the full host RAM and ignores the flag). Use kubelet system-reserved
    # instead: set system-reserved = total_host_memory - worker_memory_mb so each node
    # has ~worker_memory_mb capacity, matching --agents-memory behavior.
    memory_args: list[str] = []
    if cfg.dind_memory_mode:
        worker_memory_mb = parse_memory_mb(cfg.worker_memory)
        total_ki = _get_system_total_memory_ki()
        if total_ki is not None:
            system_reserved_mi = (total_ki // 1024) - worker_memory_mb
            if system_reserved_mi > 0:
                effective_allocatable_mi = worker_memory_mb - 100
                console.print(
                    f"[yellow]\u2139\ufe0f  DinD mode: detected {total_ki // 1024}Mi system memory, "
                    f"setting system-reserved={system_reserved_mi}Mi "
                    f"(effective capacity: ~{worker_memory_mb}Mi/node, "
                    f"allocatable: ~{effective_allocatable_mi}Mi/node)[/yellow]"
                )
                memory_args = [
                    "--k3s-arg",
                    f"--kubelet-arg=system-reserved=memory={system_reserved_mi}Mi@agent:*",
                ]
            else:
                console.print(
                    f"[yellow]\u26a0\ufe0f  DinD mode: system memory too low ({total_ki // 1024}Mi) "
                    f"to emulate {worker_memory_mb}Mi capacity per node, skipping system-reserved[/yellow]"
                )
    else:
        memory_args = ["--agents-memory", cfg.worker_memory]

    @retry(
        stop=stop_after_attempt(cfg.max_retries),
        wait=wait_fixed(CLUSTER_CREATE_RETRY_WAIT_SECONDS),
        reraise=True,
    )
    def _attempt() -> None:
        """Delete any existing cluster, then create a fresh one with the configured parameters."""
        try:
            sh.k3d("cluster", "delete", cfg.name)
            console.print("[yellow]   Removed existing cluster[/yellow]")
        except sh.ErrorReturnCode_1:
            console.print("[yellow]   No existing cluster found[/yellow]")

        sh.k3d(
            "cluster",
            "create",
            cfg.name,
            "--servers",
            "1",
            "--agents",
            str(cfg.worker_nodes),
            "--image",
            cfg.k3s_image,
            "--api-port",
            cfg.api_port,
            "--port",
            f"{cfg.lb_port}@loadbalancer",
            "--registry-create",
            f"registry:0.0.0.0:{cfg.registry_port}",
            "--k3s-arg",
            f"--node-taint={E2E_NODE_ROLE_KEY}=agent:NoSchedule@agent:*",
            "--k3s-node-label",
            f"{E2E_NODE_ROLE_KEY}=agent@agent:*",
            "--k3s-node-label",
            "nvidia.com/gpu.deploy.operands=false@server:*",
            "--k3s-node-label",
            "nvidia.com/gpu.deploy.operands=false@agent:*",
            *memory_args,
            "--timeout",
            CLUSTER_TIMEOUT,
            "--wait",
        )

    _attempt()
    console.print("[green]\u2705 Cluster created successfully[/green]")


def wait_for_nodes(cfg: ClusterConfig, max_restart_rounds: int = 2) -> None:
    """Wait for all nodes to be ready, restarting failed containers if needed.

    With 30+ k3d nodes, occasionally a k3s-agent process dies silently inside its
    container during startup due to resource contention. This function detects
    NotReady nodes after the initial wait, restarts their Docker containers, and
    retries — up to max_restart_rounds times.
    """
    for attempt in range(1, max_restart_rounds + 2):
        console.print(f"[yellow]\u2139\ufe0f  Waiting for all nodes to be ready (attempt {attempt})...[/yellow]")
        try:
            sh.kubectl("wait", "--for=condition=Ready", "nodes", "--all", "--timeout=5m")
            console.print("[green]\u2705 All nodes are ready[/green]")
            return
        except sh.ErrorReturnCode:
            pass  # timed out — fall through to identify and restart NotReady nodes

        not_ready_output = sh.kubectl(
            "get", "nodes",
            "--no-headers",
            "-o", "custom-columns=NAME:.metadata.name,STATUS:.status.conditions[?(@.type=='Ready')].status",
        ).strip()

        not_ready_nodes = [
            line.split()[0]
            for line in not_ready_output.splitlines()
            if len(line.split()) >= 2 and line.split()[1] != "True"
        ]

        if not not_ready_nodes:
            console.print("[green]\u2705 All nodes are ready[/green]")
            return

        if attempt > max_restart_rounds:
            raise RuntimeError(
                f"{len(not_ready_nodes)} node(s) still NotReady after {max_restart_rounds} "
                f"restart rounds: {not_ready_nodes}"
            )

        console.print(f"[yellow]\u26a0\ufe0f  {len(not_ready_nodes)} node(s) NotReady: {not_ready_nodes}[/yellow]")

        docker_client = docker.from_env()
        for node_name in not_ready_nodes:
            if not node_name.startswith(f"k3d-{cfg.name}-"):
                console.print(f"[yellow]   Skipping {node_name} (not part of cluster '{cfg.name}')[/yellow]")
                continue
            try:
                container = docker_client.containers.get(node_name)
                console.print(f"[yellow]   Restarting container {node_name}...[/yellow]")
                container.restart(timeout=30)
                console.print(f"[green]   \u2713 Restarted {node_name}[/green]")
            except docker.errors.NotFound:
                console.print(f"[red]   \u2717 Container {node_name} not found[/red]")
            except Exception as e:
                console.print(f"[red]   \u2717 Failed to restart {node_name}: {e}[/red]")

        console.print("[yellow]   Waiting 15s for restarted nodes to rejoin...[/yellow]")
        time.sleep(15)


# ============================================================================
# Topology labels
# ============================================================================


def _label_single_node(node: str, idx: int) -> None:
    """Apply topology labels to a single worker node.

    Args:
        node: Kubernetes node name.
        idx: Zero-based node index for topology calculation.
    """
    zone = idx // NODES_PER_ZONE
    block = idx // NODES_PER_BLOCK
    rack = idx // NODES_PER_RACK
    sh.kubectl(
        "label",
        "node",
        node,
        f"{LABEL_ZONE}=zone-{zone}",
        f"{LABEL_BLOCK}=block-{block}",
        f"{LABEL_RACK}=rack-{rack}",
        "--overwrite",
    )


def apply_topology_labels() -> None:
    """Apply zone, block, and rack topology labels to all worker nodes."""
    console.print(Panel.fit("Applying topology labels to worker nodes", style="bold blue"))

    nodes_output = sh.kubectl(
        "get",
        "nodes",
        "-l",
        f"!{LABEL_CONTROL_PLANE},{LABEL_TYPE}!={LABEL_TYPE_KWOK}",
        "-o",
        "jsonpath={.items[*].metadata.name}",
    ).strip()

    worker_nodes = sorted(nodes_output.split()) if nodes_output else []
    if not worker_nodes:
        console.print("[yellow]No worker nodes found, skipping topology labels[/yellow]")
        return
    max_workers = min(len(worker_nodes), 10)
    with ThreadPoolExecutor(max_workers=max_workers) as executor:
        futures = {executor.submit(_label_single_node, node, idx): node for idx, node in enumerate(worker_nodes)}
        for future in as_completed(futures):
            future.result()
    console.print(f"[green]\u2705 Applied topology labels to {len(worker_nodes)} worker nodes[/green]")
