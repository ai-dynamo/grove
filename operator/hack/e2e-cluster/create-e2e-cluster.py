#!/usr/bin/env python3
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

"""
create-e2e-cluster.py - Unified cluster setup for Grove E2E testing.

All actions are opt-in via flags. Supports fresh k3d cluster creation and
deploying scale infrastructure on any existing cluster.

Environment Variables:
    All cluster configuration can be overridden via E2E_* environment variables:
    - E2E_CLUSTER_NAME (default: shared-e2e-test-cluster)
    - E2E_REGISTRY_PORT (default: 5001)
    - E2E_API_PORT (default: 6560)
    - E2E_WORKER_NODES (default: 30)
    - E2E_KAI_VERSION (default: from dependencies.yaml)
    - And more (see ClusterConfig class for full list)

Examples:
    # Fresh k3d e2e cluster
    ./hack/e2e-cluster/create-e2e-cluster.py --k3d --kai --grove --topology --prepull

    # Delete k3d cluster
    ./hack/e2e-cluster/create-e2e-cluster.py --k3d --delete

    # Scale infra on existing cluster (KWOK controller auto-installed)
    ./hack/e2e-cluster/create-e2e-cluster.py --kwok-nodes 1000 --pyroscope

    # Delete all KWOK nodes
    ./hack/e2e-cluster/create-e2e-cluster.py --kwok-delete

    # Deploy Grove on existing cluster with custom registry
    ./hack/e2e-cluster/create-e2e-cluster.py --grove --registry myregistry.io/grove

For detailed usage information, run: ./hack/e2e-cluster/create-e2e-cluster.py --help
"""

import argparse
import json
import logging
import os
import re
import subprocess
import sys
import tempfile
import time
import yaml
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import docker
import sh
from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict
from rich.console import Console
from rich.panel import Panel
from rich.progress import Progress, SpinnerColumn, TextColumn, BarColumn, TaskProgressColumn
from tenacity import retry, stop_after_attempt, wait_fixed, wait_exponential, retry_if_result, RetryError

console = Console(stderr=True)
logger = logging.getLogger(__name__)


# ============================================================================
# Configuration
# ============================================================================

def load_dependencies() -> dict:
    """Load dependency versions and images from dependencies.yaml."""
    deps_file = Path(__file__).resolve().parent / "dependencies.yaml"
    with open(deps_file) as f:
        return yaml.safe_load(f)


DEPENDENCIES = load_dependencies()

# Webhook readiness check configuration
WEBHOOK_READY_MAX_RETRIES = 60
WEBHOOK_READY_POLL_INTERVAL_SECONDS = 5

# Kai queue webhook readiness configuration
KAI_QUEUE_MAX_RETRIES = 12
KAI_QUEUE_POLL_INTERVAL_SECONDS = 5

# KWOK node constants
NODE_CONDITIONS = [
    {"type": "Ready", "status": "True", "reason": "KubeletReady",
     "message": "kubelet is posting ready status"},
    {"type": "MemoryPressure", "status": "False", "reason": "KubeletHasSufficientMemory",
     "message": "kubelet has sufficient memory available"},
    {"type": "DiskPressure", "status": "False", "reason": "KubeletHasNoDiskPressure",
     "message": "kubelet has no disk pressure"},
    {"type": "PIDPressure", "status": "False", "reason": "KubeletHasSufficientPID",
     "message": "kubelet has sufficient PID available"},
    {"type": "NetworkUnavailable", "status": "False", "reason": "RouteCreated",
     "message": "RouteController created a route"},
]

# E2E node role label/taint (used in k3d cluster creation)
_E2E_NODE_ROLE_KEY = "node_role.e2e.grove.nvidia.com"

# Kai Scheduler Helm OCI path
_KAI_SCHEDULER_OCI = "oci://ghcr.io/nvidia/kai-scheduler/kai-scheduler"

# Grove image names (must match skaffold output)
_GROVE_OPERATOR_IMAGE = "grove-operator"
_GROVE_INITC_IMAGE = "grove-initc"

# Grove module path for LD_FLAGS
_GROVE_MODULE_PATH = "github.com/ai-dynamo/grove/operator/internal/version"

# KWOK node annotation/taint keys
_KWOK_ANNOTATION_KEY = "kwok.x-k8s.io/node"
_KWOK_FAKE_NODE_TAINT_KEY = "fake-node"

# Keywords that indicate Grove webhook is responsive
_WEBHOOK_READY_KEYWORDS = ["validated", "denied", "error", "invalid", "created", "podcliqueset"]

# Fields displayed during cluster creation (inclusion set avoids fragile exclusion lists)
_DISPLAY_FIELDS = frozenset({
    "cluster_name", "registry_port", "api_port", "lb_port",
    "worker_nodes", "worker_memory", "k3s_image", "kai_version",
    "skaffold_profile",
})


class ClusterConfig(BaseSettings):
    """
    Configuration auto-loaded from E2E_* environment variables.

    Environment variables are automatically mapped to fields using the E2E_ prefix:
    - E2E_CLUSTER_NAME → cluster_name
    - E2E_REGISTRY_PORT → registry_port (automatically converted to int)
    - E2E_API_PORT → api_port (automatically converted to int)
    - E2E_WORKER_NODES → worker_nodes (automatically converted to int)
    - E2E_KAI_VERSION → kai_version
    - E2E_REGISTRY → registry
    - E2E_KWOK_NODES → kwok_nodes
    - E2E_KWOK_BATCH_SIZE → kwok_batch_size
    - E2E_PYROSCOPE_NS → pyroscope_ns
    """

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    # Cluster configuration
    cluster_name: str = "shared-e2e-test-cluster"
    registry_port: int = Field(default=5001, ge=1, le=65535)
    api_port: int = Field(default=6560, ge=1, le=65535)
    lb_port: str = "8090:80"
    worker_nodes: int = Field(default=30, ge=1, le=100)
    worker_memory: str = Field(default="150m", pattern=r"^\d+[mMgG]?$")
    k3s_image: str = "rancher/k3s:v1.33.5-k3s1"
    kai_version: str = Field(default=DEPENDENCIES['kai_scheduler']['version'],
                             pattern=r"^v[\d.]+(-[\w.]+)?$")
    skaffold_profile: str = "topology-test"
    max_retries: int = Field(default=3, ge=1, le=10)

    # New fields (overridable via E2E_* env vars)
    registry: str | None = None
    kwok_nodes: int | None = None
    kwok_batch_size: int = Field(default=150, ge=1)
    pyroscope_ns: str = "pyroscope"

    # KWOK node resources
    kwok_node_cpu: str = "64"
    kwok_node_memory: str = "512Gi"
    kwok_max_pods: int = 110

    # Constants (not configurable via environment variables)
    cluster_timeout: str = "120s"
    nodes_per_zone: int = 28
    nodes_per_block: int = 14
    nodes_per_rack: int = 7


# ============================================================================
# Utility functions
# ============================================================================

def require_command(cmd: str) -> None:
    """Check if a command exists, raise RuntimeError if not."""
    try:
        sh.which(cmd)
    except sh.ErrorReturnCode:
        raise RuntimeError(f"Required command '{cmd}' not found. Please install it first.")


def run_cmd(cmd, *args, **kwargs) -> tuple[int, Any]:
    """Run a command, handling errors gracefully. Returns (exit_code, output).
    Pass _ok_code=[0, 1, ...] to suppress exceptions for expected exit codes.
    """
    ok_codes = kwargs.pop('_ok_code', [0])
    try:
        output = cmd(*args, **kwargs)
        return 0, output
    except sh.ErrorReturnCode as e:
        if e.exit_code in ok_codes:
            return e.exit_code, e
        raise


def run_kubectl(args: list[str], timeout: int = 30) -> tuple[bool, str, str]:
    """Run a kubectl command and return (success, stdout, stderr)."""
    try:
        result = subprocess.run(
            ["kubectl", *args],
            capture_output=True, text=True, timeout=timeout,
        )
        return result.returncode == 0, result.stdout, result.stderr
    except (subprocess.SubprocessError, OSError) as exc:
        return False, "", str(exc)


# ============================================================================
# Flag validation
# ============================================================================

def validate_flags(parser: argparse.ArgumentParser, args: argparse.Namespace) -> None:
    """Validate flag combinations. Uses parser.error() for hard failures (exits 2)."""
    if args.prepull and not args.k3d:
        parser.error("--prepull requires --k3d")

    if args.grove and not args.registry and not args.k3d:
        parser.error("--registry is required when --grove is used without --k3d")

    for flag in ("grove_profiling", "grove_pcs_syncs", "grove_pclq_syncs", "grove_pcsg_syncs"):
        if getattr(args, flag) and not args.grove:
            logger.warning("Grove tuning flag --%s ignored because --grove is not set",
                           flag.replace("_", "-"))


# ============================================================================
# Image pre-pulling functions
# ============================================================================

def prepull_images(images: list[str], registry_port: int, version: str) -> None:
    """Pre-pull images in parallel and push them to the local k3d registry."""
    if not images:
        return

    console.print(Panel.fit("Pre-pulling images to local registry", style="bold blue"))
    console.print(f"[yellow]Pre-pulling {len(images)} images in parallel (this speeds up cluster startup)...[/yellow]")

    try:
        docker_client = docker.from_env()
    except Exception as e:
        console.print(f"[yellow]⚠️  Failed to connect to Docker: {e}[/yellow]")
        console.print("[yellow]⚠️  Skipping image pre-pull (cluster will pull images on-demand)[/yellow]")
        return

    def pull_tag_push(image_name: str) -> tuple[str, bool, str | None]:
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

    try:
        with Progress(
            SpinnerColumn(), TextColumn("[progress.description]{task.description}"),
            BarColumn(), TaskProgressColumn(), console=console,
        ) as progress:
            task = progress.add_task("[cyan]Pulling images...", total=len(images))
            failed_images = []
            with ThreadPoolExecutor(max_workers=5) as executor:
                futures = {executor.submit(pull_tag_push, img): img for img in images}
                for future in as_completed(futures):
                    image_name, success, error = future.result()
                    progress.advance(task)
                    if success:
                        console.print(f"[green]✓ {image_name}[/green]")
                    else:
                        console.print(f"[red]✗ {image_name} - {error}[/red]")
                        failed_images.append(image_name)
    finally:
        docker_client.close()

    if failed_images:
        console.print(f"[yellow]⚠️  Failed to pre-pull {len(failed_images)} images[/yellow]")
        console.print("[yellow]   Cluster will pull these images on-demand (may be slower)[/yellow]")
    else:
        console.print(f"[green]✅ Successfully pre-pulled all {len(images)} images[/green]")


# ============================================================================
# Cluster operations
# ============================================================================

def delete_cluster(config: ClusterConfig) -> None:
    """Delete the k3d cluster."""
    console.print(f"[yellow]ℹ️  Deleting k3d cluster '{config.cluster_name}'...[/yellow]")
    exit_code, _ = run_cmd(sh.k3d, "cluster", "delete", config.cluster_name, _ok_code=[0, 1])
    if exit_code == 0:
        console.print(f"[green]✅ Cluster '{config.cluster_name}' deleted[/green]")
    else:
        console.print(f"[yellow]⚠️  Cluster '{config.cluster_name}' not found or already deleted[/yellow]")


def create_cluster(config: ClusterConfig) -> bool:
    """Create a k3d cluster with retry logic."""
    console.print(Panel.fit("Creating k3d cluster", style="bold blue"))
    console.print("[yellow]Configuration:[/yellow]")
    for key, value in config.model_dump(include=_DISPLAY_FIELDS).items():
        console.print(f"  {key:20s}: {value}")

    for attempt in range(1, config.max_retries + 1):
        console.print(f"[yellow]ℹ️  Cluster creation attempt {attempt} of {config.max_retries}...[/yellow]")
        exit_code, _ = run_cmd(sh.k3d, "cluster", "delete", config.cluster_name, _ok_code=[0, 1])
        if exit_code == 0:
            console.print("[yellow]   Removed existing cluster[/yellow]")
        else:
            console.print("[yellow]   No existing cluster found (proceeding with creation)[/yellow]")

        exit_code, _ = run_cmd(
            sh.k3d, "cluster", "create", config.cluster_name,
            "--servers", "1",
            "--agents", str(config.worker_nodes),
            "--image", config.k3s_image,
            "--api-port", config.api_port,
            "--port", f"{config.lb_port}@loadbalancer",
            "--registry-create", f"registry:0.0.0.0:{config.registry_port}",
            "--k3s-arg", f"--node-taint={_E2E_NODE_ROLE_KEY}=agent:NoSchedule@agent:*",
            "--k3s-node-label", f"{_E2E_NODE_ROLE_KEY}=agent@agent:*",
            "--k3s-node-label", "nvidia.com/gpu.deploy.operands=false@server:*",
            "--k3s-node-label", "nvidia.com/gpu.deploy.operands=false@agent:*",
            "--agents-memory", config.worker_memory,
            "--timeout", config.cluster_timeout,
            "--wait",
            _ok_code=[0, 1]
        )

        if exit_code == 0:
            console.print(f"[green]✅ Cluster created successfully on attempt {attempt}[/green]")
            return True

        if attempt < config.max_retries:
            console.print("[yellow]⚠️  Cluster creation failed, retrying in 10 seconds...[/yellow]")
            time.sleep(10)

    console.print(f"[red]❌ Cluster creation failed after {config.max_retries} attempts[/red]")
    return False


def wait_for_nodes() -> None:
    """Wait for all nodes to be ready."""
    console.print("[yellow]ℹ️  Waiting for all nodes to be ready...[/yellow]")
    sh.kubectl("wait", "--for=condition=Ready", "nodes", "--all", "--timeout=5m")
    console.print("[green]✅ All nodes are ready[/green]")


def install_kai_scheduler(config: ClusterConfig) -> None:
    """Install Kai Scheduler using Helm."""
    console.print(Panel.fit("Installing Kai Scheduler", style="bold blue"))
    console.print(f"[yellow]Version: {config.kai_version}[/yellow]")
    run_cmd(sh.helm, "uninstall", "kai-scheduler", "-n", "kai-scheduler", _ok_code=[0, 1])
    sh.helm(
        "install", "kai-scheduler",
        _KAI_SCHEDULER_OCI,
        "--version", config.kai_version,
        "--namespace", "kai-scheduler",
        "--create-namespace",
        "--set", "global.tolerations[0].key=node-role.kubernetes.io/control-plane",
        "--set", "global.tolerations[0].operator=Exists",
        "--set", "global.tolerations[0].effect=NoSchedule",
        "--set", f"global.tolerations[1].key={_E2E_NODE_ROLE_KEY}",
        "--set", "global.tolerations[1].operator=Equal",
        "--set", "global.tolerations[1].value=agent",
        "--set", "global.tolerations[1].effect=NoSchedule",
    )
    console.print("[green]✅ Kai Scheduler installed[/green]")


@retry(
    stop=stop_after_attempt(WEBHOOK_READY_MAX_RETRIES),
    wait=wait_fixed(WEBHOOK_READY_POLL_INTERVAL_SECONDS),
    reraise=True,
)
def _check_grove_webhook_ready(operator_dir: Path) -> None:
    _, result = run_cmd(
        sh.kubectl, "create", "-f", str(operator_dir / "e2e/yaml/workload1.yaml"),
        "--dry-run=server", "-n", "default", _ok_code=[0, 1],
    )
    raw = str(result) if isinstance(result, str) else str(result.stdout) + str(result.stderr)
    output = raw.lower()
    if not any(kw in output for kw in _WEBHOOK_READY_KEYWORDS):
        raise RuntimeError("Grove webhook not ready")


def _build_grove_images(config: ClusterConfig, operator_dir: Path, push_repo: str) -> dict[str, str]:
    """Run skaffold build, return {imageName: tag} map."""
    console.print(f"[yellow]ℹ️  Building images (push to {push_repo})...[/yellow]")
    build_output = json.loads(
        sh.skaffold(
            "build",
            "--default-repo", push_repo,
            "--profile", config.skaffold_profile,
            "--quiet",
            "--output={{json .}}",
            _cwd=str(operator_dir)
        )
    )
    return {build["imageName"]: build["tag"] for build in build_output.get("builds", [])}


def _deploy_grove_charts(
    config: ClusterConfig,
    operator_dir: Path,
    images: dict[str, str],
    pull_repo: str,
) -> None:
    """Run skaffold deploy with resolved image tags."""
    console.print("[yellow]Deploying with images:[/yellow]")
    for name, tag in images.items():
        console.print(f"  {name}={tag}")

    os.environ["CONTAINER_REGISTRY"] = pull_repo
    sh.skaffold(
        "deploy",
        "--profile", config.skaffold_profile,
        "--namespace", "grove-system",
        "--status-check=false",
        "--default-repo=",
        "--images", f"{_GROVE_OPERATOR_IMAGE}={images[_GROVE_OPERATOR_IMAGE]}",
        "--images", f"{_GROVE_INITC_IMAGE}={images[_GROVE_INITC_IMAGE]}",
        _cwd=str(operator_dir)
    )
    console.print("[green]✅ Grove operator deployed[/green]")


def _apply_grove_helm_overrides(operator_dir: Path, helm_set_values: list[str]) -> None:
    """Apply helm value overrides via helm upgrade --reuse-values."""
    if not helm_set_values:
        return
    console.print("[yellow]ℹ️  Applying Grove helm overrides...[/yellow]")
    set_args = [item for val in helm_set_values for item in ("--set", val)]
    chart_path = str(operator_dir / "charts")
    sh.helm(
        "upgrade", "grove-operator", chart_path,
        "-n", "grove-system",
        "--reuse-values",
        *set_args,
    )
    console.print("[green]✅ Grove helm overrides applied[/green]")


def _wait_grove_webhook(operator_dir: Path) -> None:
    """Wait for Grove webhook to become responsive."""
    console.print("[yellow]ℹ️  Waiting for Grove webhook to be ready...[/yellow]")
    try:
        _check_grove_webhook_ready(operator_dir)
        console.print("[green]✅ Grove webhook is ready[/green]")
    except (RuntimeError, RetryError):
        raise RuntimeError("Timed out waiting for Grove webhook")


def deploy_grove_operator(
    config: ClusterConfig,
    operator_dir: Path,
    registry: str | None = None,
    grove_profiling: bool = False,
    grove_pcs_syncs: int | None = None,
    grove_pclq_syncs: int | None = None,
    grove_pcsg_syncs: int | None = None,
) -> None:
    """Deploy Grove operator using Skaffold."""
    console.print(Panel.fit("Deploying Grove operator", style="bold blue"))
    run_cmd(sh.helm, "uninstall", "grove-operator", "-n", "grove-system", _ok_code=[0, 1])

    build_date = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    os.environ.update({
        "VERSION": "E2E_TESTS",
        "LD_FLAGS": (
            f"-X {_GROVE_MODULE_PATH}.gitCommit=e2e-test-commit "
            f"-X {_GROVE_MODULE_PATH}.gitTreeState=clean "
            f"-X {_GROVE_MODULE_PATH}.buildDate={build_date} "
            f"-X {_GROVE_MODULE_PATH}.gitVersion=E2E_TESTS"
        )
    })

    if registry:
        push_repo = registry
        pull_repo = registry
    else:
        push_repo = f"localhost:{config.registry_port}"
        pull_repo = f"registry:{config.registry_port}"

    raw_images = _build_grove_images(config, operator_dir, push_repo)
    images = {name: tag.replace(push_repo, pull_repo) for name, tag in raw_images.items()}

    _deploy_grove_charts(config, operator_dir, images, pull_repo)

    helm_overrides = []
    if grove_profiling:
        helm_overrides.append("config.debugging.enableProfiling=true")
    if grove_pcs_syncs is not None:
        helm_overrides.append(f"config.controllers.podCliqueSet.concurrentSyncs={grove_pcs_syncs}")
    if grove_pclq_syncs is not None:
        helm_overrides.append(f"config.controllers.podClique.concurrentSyncs={grove_pclq_syncs}")
    if grove_pcsg_syncs is not None:
        helm_overrides.append(f"config.controllers.podCliqueScalingGroup.concurrentSyncs={grove_pcsg_syncs}")

    _apply_grove_helm_overrides(operator_dir, helm_overrides)

    console.print("[yellow]ℹ️  Waiting for Grove deployment rollout...[/yellow]")
    sh.kubectl("rollout", "status", "deployment", "-n", "grove-system", "--timeout=5m")

    _wait_grove_webhook(operator_dir)


def _node_sort_key(name: str) -> tuple[str, int]:
    m = re.search(r"-(\d+)$", name)
    return re.sub(r"-\d+$", "", name), (int(m.group(1)) if m else 0)


def apply_topology_labels(config: ClusterConfig) -> None:
    """Apply topology labels to worker nodes."""
    console.print(Panel.fit("Applying topology labels to worker nodes", style="bold blue"))

    nodes_output = sh.kubectl(
        "get", "nodes",
        "-l", "!node-role.kubernetes.io/control-plane",
        "-o", "jsonpath={.items[*].metadata.name}"
    ).strip()

    worker_nodes = sorted(nodes_output.split(), key=_node_sort_key)
    for idx, node in enumerate(worker_nodes):
        zone = idx // config.nodes_per_zone
        block = idx // config.nodes_per_block
        rack = idx // config.nodes_per_rack
        sh.kubectl(
            "label", "node", node,
            f"kubernetes.io/zone=zone-{zone}",
            f"kubernetes.io/block=block-{block}",
            f"kubernetes.io/rack=rack-{rack}",
            "--overwrite"
        )
    console.print(f"[green]✅ Applied topology labels to {len(worker_nodes)} worker nodes[/green]")


# ============================================================================
# KWOK functions
# ============================================================================

def topology_labels(node_id: int, config: ClusterConfig) -> dict[str, str]:
    """Compute topology labels for a KWOK node using multi-zone index arithmetic."""
    return {
        "kubernetes.io/zone": f"zone-{node_id // config.nodes_per_zone}",
        "kubernetes.io/block": f"block-{node_id // config.nodes_per_block}",
        "kubernetes.io/rack": f"rack-{node_id // config.nodes_per_rack}",
        "kubernetes.io/hostname": f"kwok-node-{node_id}",
        "type": "kwok",
    }


def node_manifest(node_id: int, config: ClusterConfig) -> dict:
    """Build the Kubernetes Node manifest for a KWOK node."""
    name = f"kwok-node-{node_id}"
    resources = {
        "cpu": config.kwok_node_cpu,
        "memory": config.kwok_node_memory,
        "pods": str(config.kwok_max_pods),
    }
    return {
        "apiVersion": "v1",
        "kind": "Node",
        "metadata": {
            "name": name,
            "labels": topology_labels(node_id, config),
            "annotations": {
                _KWOK_ANNOTATION_KEY: "fake",
                "node.alpha.kubernetes.io/ttl": "0",
            },
        },
        "spec": {
            "taints": [{"effect": "NoSchedule", "key": _KWOK_FAKE_NODE_TAINT_KEY, "value": "true"}]
        },
        "status": {
            "capacity": resources,
            "allocatable": resources,
            "conditions": NODE_CONDITIONS,
            "addresses": [
                {"type": "InternalIP",
                 "address": f"10.0.{node_id // 256}.{node_id % 256}"}
            ],
        },
    }


def install_kwok_controller(version: str, timeout: int = 120) -> None:
    """Install KWOK controller and wait for it to be available."""
    console.print(Panel.fit(f"Installing KWOK controller ({version})", style="bold blue"))
    kwok_repo = "kubernetes-sigs/kwok"
    base_url = f"https://github.com/{kwok_repo}/releases/download/{version}"
    for manifest in ("kwok.yaml", "stage-fast.yaml"):
        ok, _, stderr = run_kubectl(["apply", "-f", f"{base_url}/{manifest}"], timeout=60)
        if not ok:
            console.print(f"[yellow]⚠️  Partial failure applying {manifest}: {stderr[:200]}[/yellow]")
            console.print("[yellow]   (This is usually safe if KWOK is already installed)[/yellow]")

    console.print("[yellow]ℹ️  Waiting for KWOK controller to be available...[/yellow]")
    ok, _, stderr = run_kubectl([
        "wait", "--for=condition=Available",
        "deployment/kwok-controller", "-n", "kube-system",
        f"--timeout={timeout}s"
    ], timeout=timeout + 10)
    if not ok:
        raise RuntimeError(f"KWOK controller not ready after {timeout}s: {stderr[:200]}")
    console.print("[green]✅ KWOK controller installed and ready[/green]")


@retry(
    stop=stop_after_attempt(3),
    wait=wait_exponential(multiplier=1, min=1, max=8),
    retry=retry_if_result(lambda ok: not ok),
)
def _kubectl_apply(tmp_name: str) -> bool:
    ok, _, stderr = run_kubectl(["apply", "-f", tmp_name])
    return ok or "AlreadyExists" in stderr


def create_node_batch(
    node_ids: list[int], config: ClusterConfig
) -> tuple[list[int], list[tuple[int, str]]]:
    """Create a batch of KWOK nodes with a single kubectl apply."""
    combined_yaml = "---\n".join(
        yaml.dump(node_manifest(nid, config), default_flow_style=False)
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


def create_nodes(total: int, config: ClusterConfig) -> None:
    """Create KWOK nodes in batches."""
    logger.info("Creating %d KWOK nodes (batch size=%d)...", total, config.kwok_batch_size)
    successes: list[int] = []
    failures: list[tuple[int, str]] = []

    for batch_start in range(0, total, config.kwok_batch_size):
        batch_end = min(batch_start + config.kwok_batch_size, total)
        node_ids = list(range(batch_start, batch_end))
        batch_ok, batch_fail = create_node_batch(node_ids, config)
        successes.extend(batch_ok)
        failures.extend(batch_fail)
        logger.info("Batch %d-%d: success=%d, failed=%d",
                    batch_start, batch_end - 1, len(batch_ok), len(batch_fail))

    if failures:
        console.print(f"[yellow]⚠️  {len(failures)} nodes failed to create[/yellow]")
        for nid, err in failures[:10]:
            logger.error("  kwok-node-%d: %s", nid, err)
    console.print(f"[green]✅ Created {len(successes)} KWOK nodes[/green]")


def delete_kwok_nodes() -> None:
    """Delete all KWOK simulated nodes and uninstall the KWOK controller."""
    console.print("[yellow]ℹ️  Deleting all KWOK nodes...[/yellow]")
    ok, _, stderr = run_kubectl(["delete", "nodes", "-l", "type=kwok", "--ignore-not-found"], timeout=300)
    if ok:
        console.print("[green]✅ KWOK nodes deleted[/green]")
    else:
        raise RuntimeError(f"Failed to delete KWOK nodes: {stderr[:200]}")

    console.print("[yellow]ℹ️  Uninstalling KWOK controller...[/yellow]")
    kwok_version = DEPENDENCIES.get("kwok_controller", {}).get("version", "v0.7.0")
    kwok_repo = "kubernetes-sigs/kwok"
    base_url = f"https://github.com/{kwok_repo}/releases/download/{kwok_version}"
    for manifest in ("stage-fast.yaml", "kwok.yaml"):
        run_kubectl(["delete", "-f", f"{base_url}/{manifest}", "--ignore-not-found"], timeout=120)
    console.print("[green]✅ KWOK controller uninstalled[/green]")


def install_pyroscope(namespace: str, values_file: Path | None = None, version: str = "") -> None:
    """Install Pyroscope via Helm."""
    console.print(Panel.fit(f"Installing Pyroscope (namespace: {namespace})", style="bold blue"))
    require_command("helm")

    sh.helm("repo", "add", "grafana", "https://grafana.github.io/helm-charts", "--force-update")
    sh.helm("repo", "update", "grafana")

    ok, _, stderr = run_kubectl(["create", "namespace", namespace])
    if not ok and "AlreadyExists" not in stderr:
        raise RuntimeError(f"Failed to create namespace {namespace}: {stderr}")

    helm_args = ["upgrade", "--install", "pyroscope", "grafana/pyroscope", "-n", namespace]
    if version:
        helm_args += ["--version", version]
    if values_file and values_file.exists():
        helm_args += ["-f", str(values_file)]
    sh.helm(*helm_args)
    console.print("[green]✅ Pyroscope installed[/green]")


# ============================================================================
# CLI
# ============================================================================

@retry(
    stop=stop_after_attempt(KAI_QUEUE_MAX_RETRIES),
    wait=wait_fixed(KAI_QUEUE_POLL_INTERVAL_SECONDS),
    reraise=True,
)
def _apply_kai_queues(queues_file: Path) -> None:
    exit_code, _ = run_cmd(sh.kubectl, "apply", "-f", str(queues_file), _ok_code=[0, 1])
    if exit_code != 0:
        raise RuntimeError("Kai queue webhook not ready")


def build_parser() -> argparse.ArgumentParser:
    """Build the argument parser with grouped flags."""
    parser = argparse.ArgumentParser(
        description="Unified cluster setup for Grove E2E testing. All actions are opt-in.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "Examples:\n"
            "  create-e2e-cluster.py --k3d --kai --grove --topology --prepull\n"
            "  create-e2e-cluster.py --k3d --delete\n"
            "  create-e2e-cluster.py --kwok-nodes 1000 --pyroscope\n"
            "  create-e2e-cluster.py --kwok-delete\n"
            "  create-e2e-cluster.py --grove --registry myregistry.io/grove\n"
        ),
    )

    k3d_group = parser.add_argument_group("k3d cluster")
    k3d_group.add_argument("--k3d", action="store_true",
                           help="Create a new k3d cluster before installing anything")
    k3d_group.add_argument("--workers", type=int, default=None,
                           help="k3d worker nodes (default: 30; overrides E2E_WORKER_NODES)")
    k3d_group.add_argument("--delete", action="store_true",
                           help="Delete the k3d cluster and exit")

    comp_group = parser.add_argument_group("components (all opt-in)")
    comp_group.add_argument("--kai", action="store_true", help="Install Kai Scheduler")
    comp_group.add_argument("--grove", action="store_true", help="Install Grove operator")
    comp_group.add_argument("--topology", action="store_true",
                            help="Apply topology labels to worker nodes")
    comp_group.add_argument("--prepull", action="store_true",
                            help="Pre-pull images into local k3d registry (only with --k3d)")
    comp_group.add_argument("--registry", type=str, default=None,
                            help="Container registry URL (default: auto-detected from k3d when --k3d is set)")

    kwok_group = parser.add_argument_group("KWOK")
    kwok_group.add_argument("--kwok-nodes", type=int, default=None,
                            help="Create N KWOK simulated nodes (installs controller automatically)")
    kwok_group.add_argument("--kwok-batch-size", type=int, default=None,
                            help="Node creation batch size (default: 150)")
    kwok_group.add_argument("--kwok-delete", action="store_true",
                            help="Delete all KWOK simulated nodes")

    pyro_group = parser.add_argument_group("Pyroscope")
    pyro_group.add_argument("--pyroscope", action="store_true",
                            help="Install Pyroscope via Helm")
    pyro_group.add_argument("--pyroscope-namespace", type=str, default=None,
                            help="Pyroscope namespace (default: pyroscope)")

    tuning_group = parser.add_argument_group(
        "Grove tuning (applies when --grove is set, as helm overrides)"
    )
    tuning_group.add_argument("--grove-profiling", action="store_true",
                              help="Enable pprof (sets config.debugging.enableProfiling=true)")
    tuning_group.add_argument("--grove-pcs-syncs", type=int, default=None,
                              help="PodCliqueSet concurrentSyncs")
    tuning_group.add_argument("--grove-pclq-syncs", type=int, default=None,
                              help="PodClique concurrentSyncs")
    tuning_group.add_argument("--grove-pcsg-syncs", type=int, default=None,
                              help="PodCliqueScalingGroup concurrentSyncs")

    return parser


def _run(
    args: argparse.Namespace,
    config: ClusterConfig,
    operator_dir: Path,
    script_dir: Path,
) -> None:
    """Orchestrate all requested actions. Raises RuntimeError on failure."""
    # --kwok-delete: delete KWOK nodes and exit
    if args.kwok_delete:
        delete_kwok_nodes()
        return

    # --k3d --delete: delete k3d cluster and exit
    if args.k3d and args.delete:
        delete_cluster(config)
        return

    # --k3d: create cluster
    if args.k3d:
        prereqs = ["k3d", "kubectl", "docker"]
        if args.kai:
            prereqs.append("helm")
        if args.grove:
            prereqs.extend(["skaffold", "jq"])
        console.print(Panel.fit("Checking prerequisites", style="bold blue"))
        for cmd in prereqs:
            require_command(cmd)
        console.print("[green]✅ All required tools are available[/green]")

        if args.grove:
            console.print(Panel.fit("Preparing Helm charts", style="bold blue"))
            prepare_charts = operator_dir / "hack/prepare-charts.sh"
            if prepare_charts.exists():
                sh.bash(str(prepare_charts))
                console.print("[green]✅ Charts prepared[/green]")

        if not create_cluster(config):
            raise RuntimeError(f"Cluster creation failed after {config.max_retries} attempts")
        wait_for_nodes()

        if args.prepull:
            prepull_images(DEPENDENCIES['kai_scheduler']['images'],
                           config.registry_port, DEPENDENCIES['kai_scheduler']['version'])
            prepull_images(DEPENDENCIES['cert_manager']['images'],
                           config.registry_port, DEPENDENCIES['cert_manager']['version'])
            busybox_images = DEPENDENCIES.get('test_images', {}).get('busybox')
            if busybox_images:
                prepull_images(busybox_images, config.registry_port, 'latest')

    # --kai: install Kai Scheduler
    if args.kai:
        install_kai_scheduler(config)

    # --grove: deploy Grove operator
    if args.grove:
        deploy_grove_operator(
            config, operator_dir,
            registry=config.registry,
            grove_profiling=args.grove_profiling,
            grove_pcs_syncs=args.grove_pcs_syncs,
            grove_pclq_syncs=args.grove_pclq_syncs,
            grove_pcsg_syncs=args.grove_pcsg_syncs,
        )

    # Wait for Kai and apply queues
    if args.kai:
        console.print("[yellow]ℹ️  Waiting for Kai Scheduler pods to be ready...[/yellow]")
        sh.kubectl("wait", "--for=condition=Ready", "pods", "--all",
                   "-n", "kai-scheduler", "--timeout=5m")
        console.print("[yellow]ℹ️  Creating default Kai queues (with retry for webhook readiness)...[/yellow]")
        try:
            _apply_kai_queues(operator_dir / "e2e/yaml/queues.yaml")
            console.print("[green]✅ Kai queues created successfully[/green]")
        except (RuntimeError, RetryError):
            raise RuntimeError("Failed to create Kai queues after retries")

    # --topology: apply topology labels to real worker nodes
    if args.topology:
        apply_topology_labels(config)

    # --k3d: export kubeconfig
    if args.k3d:
        console.print(Panel.fit("Configuring kubeconfig", style="bold blue"))
        default_kubeconfig_dir = Path.home() / ".kube"
        default_kubeconfig_dir.mkdir(parents=True, exist_ok=True)
        default_kubeconfig_path = default_kubeconfig_dir / "config"
        sh.k3d("kubeconfig", "merge", config.cluster_name, "-o", str(default_kubeconfig_path))
        default_kubeconfig_path.chmod(0o600)
        console.print(f"[green]  ✓ Merged to {default_kubeconfig_path}[/green]")

    # --kwok-nodes N: install KWOK controller and create nodes
    if args.kwok_nodes is not None:
        kwok_version = DEPENDENCIES.get("kwok_controller", {}).get("version", "v0.7.0")
        install_kwok_controller(kwok_version)
        node_count = config.kwok_nodes or 0
        if node_count > 0:
            create_nodes(node_count, config)

    # --pyroscope: install Pyroscope
    if args.pyroscope:
        values_file = script_dir / "pyroscope-values.yaml"
        pyroscope_version = DEPENDENCIES.get("pyroscope", {}).get("version", "")
        install_pyroscope(config.pyroscope_ns, values_file, version=pyroscope_version)

    if args.k3d and not args.delete:
        console.print(Panel.fit("Cluster setup complete!", style="bold green"))
        console.print("[yellow]To run E2E tests against this cluster:[/yellow]")
        console.print(f"\n  export E2E_REGISTRY_PORT={config.registry_port}")
        console.print("  make run-e2e")
        console.print("  make run-e2e TEST_PATTERN=Test_GS  # specific tests\n")
        console.print(f"[green]✅ Cluster '{config.cluster_name}' is ready for E2E testing![/green]")


def main() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%H:%M:%S",
    )

    parser = build_parser()
    args = parser.parse_args()
    validate_flags(parser, args)

    # Build ClusterConfig, applying CLI overrides on top of env-var defaults
    base_config = ClusterConfig()
    overrides = {k: v for k, v in {
        "worker_nodes": args.workers,
        "registry": args.registry,
        "kwok_nodes": args.kwok_nodes,
        "kwok_batch_size": args.kwok_batch_size,
        "pyroscope_ns": args.pyroscope_namespace,
    }.items() if v is not None}
    config = base_config.model_copy(update=overrides)

    script_dir = Path(__file__).resolve().parent
    operator_dir = script_dir.parent.parent  # hack/e2e-cluster/ → operator/

    try:
        _run(args, config, operator_dir, script_dir)
    except RuntimeError as e:
        console.print(f"[red]❌ {e}[/red]")
        sys.exit(1)


if __name__ == "__main__":
    main()
