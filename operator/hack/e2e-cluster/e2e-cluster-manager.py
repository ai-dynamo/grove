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

Default behavior creates a full e2e cluster (k3d + kai + grove + topology + prepull).
Use --skip-* flags to opt out of individual steps.
Use --delete, --kwok-nodes, --kwok-delete, --pyroscope for explicit actions.

Environment Variables:
    All cluster configuration can be overridden via E2E_* environment variables:
    - E2E_CLUSTER_NAME (default: shared-e2e-test-cluster)
    - E2E_REGISTRY_PORT (default: 5001)
    - E2E_API_PORT (default: 6560)
    - E2E_WORKER_NODES (default: 30)
    - E2E_KAI_VERSION (default: from dependencies.yaml)
    - And more (see config classes for full list)

Examples:
    # Full e2e setup (default — no flags needed!)
    ./create-e2e-cluster.py

    # Full e2e but skip image pre-pulling
    ./create-e2e-cluster.py --skip-prepull

    # Deploy only grove on existing cluster
    ./create-e2e-cluster.py --skip-cluster-creation --skip-kai --skip-topology --skip-prepull

    # Delete cluster
    ./create-e2e-cluster.py --delete

    # Scale test on existing cluster
    ./create-e2e-cluster.py --skip-cluster-creation --skip-kai --skip-grove --skip-topology --skip-prepull --kwok-nodes 1000 --pyroscope

    # Delete all KWOK nodes
    ./create-e2e-cluster.py --skip-cluster-creation --skip-kai --skip-grove --skip-topology --skip-prepull --kwok-delete

For detailed usage information, run: ./create-e2e-cluster.py --help
"""

from __future__ import annotations

import json
import logging
import os
import re
import subprocess
import sys
import tempfile
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import docker
import sh
import typer
import yaml
from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict
from rich.console import Console
from rich.panel import Panel
from rich.progress import BarColumn, Progress, SpinnerColumn, TaskProgressColumn, TextColumn
from tenacity import RetryError, retry, retry_if_result, stop_after_attempt, wait_exponential, wait_fixed

console = Console(stderr=True)
logger = logging.getLogger(__name__)

app = typer.Typer(help="Unified cluster setup for Grove E2E testing.")


# ============================================================================
# Constants
# ============================================================================

def load_dependencies() -> dict:
    """Load dependency versions and images from dependencies.yaml.

    Returns:
        Parsed YAML content as a nested dictionary.
    """
    deps_file = Path(__file__).resolve().parent / "dependencies.yaml"
    with open(deps_file) as f:
        return yaml.safe_load(f)


DEPENDENCIES = load_dependencies()

CLUSTER_TIMEOUT = "120s"
NODES_PER_ZONE = 28
NODES_PER_BLOCK = 14
NODES_PER_RACK = 7

WEBHOOK_READY_MAX_RETRIES = 60
WEBHOOK_READY_POLL_INTERVAL_SECONDS = 5

KAI_QUEUE_MAX_RETRIES = 12
KAI_QUEUE_POLL_INTERVAL_SECONDS = 5

CLUSTER_CREATE_RETRY_WAIT_SECONDS = 10

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

_E2E_NODE_ROLE_KEY = "node_role.e2e.grove.nvidia.com"
_KAI_SCHEDULER_OCI = "oci://ghcr.io/nvidia/kai-scheduler/kai-scheduler"
_GROVE_OPERATOR_IMAGE = "grove-operator"
_GROVE_INITC_IMAGE = "grove-initc"
_GROVE_MODULE_PATH = "github.com/ai-dynamo/grove/operator/internal/version"
_KWOK_ANNOTATION_KEY = "kwok.x-k8s.io/node"
_KWOK_FAKE_NODE_TAINT_KEY = "fake-node"
_WEBHOOK_READY_KEYWORDS = ["validated", "denied", "error", "invalid", "created", "podcliqueset"]

# -- Namespaces --
NS_KAI_SCHEDULER = "kai-scheduler"
NS_KUBE_SYSTEM = "kube-system"
NS_DEFAULT = "default"

# -- Helm releases --
HELM_RELEASE_KAI = "kai-scheduler"
HELM_RELEASE_GROVE = "grove-operator"
HELM_RELEASE_PYROSCOPE = "pyroscope"

# -- Helm repos --
HELM_REPO_GRAFANA = "grafana"
HELM_REPO_GRAFANA_URL = "https://grafana.github.io/helm-charts"
HELM_CHART_PYROSCOPE = "grafana/pyroscope"

# -- Topology labels --
LABEL_ZONE = "kubernetes.io/zone"
LABEL_BLOCK = "kubernetes.io/block"
LABEL_RACK = "kubernetes.io/rack"
LABEL_HOSTNAME = "kubernetes.io/hostname"
LABEL_TYPE = "type"
LABEL_TYPE_KWOK = "kwok"
LABEL_CONTROL_PLANE = "node-role.kubernetes.io/control-plane"

# -- Relative paths --
REL_WORKLOAD_YAML = "e2e/yaml/workload1.yaml"
REL_QUEUES_YAML = "e2e/yaml/queues.yaml"
REL_PREPARE_CHARTS = "hack/prepare-charts.sh"
REL_CHARTS_DIR = "charts"

# -- KWOK --
KWOK_GITHUB_REPO = "kubernetes-sigs/kwok"
KWOK_MANIFESTS = ("kwok.yaml", "stage-fast.yaml")
KWOK_CONTROLLER_DEPLOYMENT = "kwok-controller"
KWOK_IP_PREFIX = "10.0"
KWOK_IP_OCTET_SIZE = 256

# -- Helm override keys --
HELM_KEY_PROFILING = "config.debugging.enableProfiling"
HELM_KEY_PCS_SYNCS = "config.controllers.podCliqueSet.concurrentSyncs"
HELM_KEY_PCLQ_SYNCS = "config.controllers.podClique.concurrentSyncs"
HELM_KEY_PCSG_SYNCS = "config.controllers.podCliqueScalingGroup.concurrentSyncs"

# -- K3d cluster defaults --
DEFAULT_CLUSTER_NAME = "shared-e2e-test-cluster"
DEFAULT_REGISTRY_PORT = 5001
DEFAULT_API_PORT = 6560
DEFAULT_LB_PORT = "8090:80"
DEFAULT_WORKER_NODES = 30
DEFAULT_WORKER_MEMORY = "150m"
DEFAULT_K3S_IMAGE = "rancher/k3s:v1.33.5-k3s1"
DEFAULT_CLUSTER_CREATE_MAX_RETRIES = 3

# -- Component defaults --
DEFAULT_SKAFFOLD_PROFILE = "topology-test"
DEFAULT_GROVE_NAMESPACE = "grove-system"

# -- KWOK defaults --
DEFAULT_KWOK_VERSION = "v0.7.0"
DEFAULT_KWOK_BATCH_SIZE = 150
DEFAULT_KWOK_NODE_CPU = "64"
DEFAULT_KWOK_NODE_MEMORY = "512Gi"
DEFAULT_KWOK_MAX_PODS = 110
DEFAULT_PYROSCOPE_NAMESPACE = "pyroscope"

# -- E2E build metadata --
E2E_TEST_VERSION = "E2E_TESTS"
E2E_TEST_COMMIT = "e2e-test-commit"
E2E_TEST_TREE_STATE = "clean"

# -- Parallelism & limits --
DEFAULT_IMAGE_PULL_MAX_WORKERS = 5


# ============================================================================
# Configuration classes
# ============================================================================

class K3dConfig(BaseSettings):
    """k3d cluster configuration, auto-loaded from E2E_* env vars.

    Attributes:
        cluster_name: Name of the k3d cluster.
        registry_port: Port for the local container registry.
        api_port: Kubernetes API server port.
        lb_port: Load balancer port mapping (host:container).
        worker_nodes: Number of worker nodes to create.
        worker_memory: Memory limit per worker node.
        k3s_image: K3s Docker image to use.
        max_retries: Maximum cluster creation retry attempts.
    """

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    cluster_name: str = DEFAULT_CLUSTER_NAME
    registry_port: int = Field(default=DEFAULT_REGISTRY_PORT, ge=1, le=65535)
    api_port: int = Field(default=DEFAULT_API_PORT, ge=1, le=65535)
    lb_port: str = DEFAULT_LB_PORT
    worker_nodes: int = Field(default=DEFAULT_WORKER_NODES, ge=1, le=100)
    worker_memory: str = Field(default=DEFAULT_WORKER_MEMORY, pattern=r"^\d+[mMgG]?$")
    k3s_image: str = DEFAULT_K3S_IMAGE
    max_retries: int = Field(default=DEFAULT_CLUSTER_CREATE_MAX_RETRIES, ge=1, le=10)


class ComponentConfig(BaseSettings):
    """Component versions and registry, auto-loaded from E2E_* env vars.

    Attributes:
        kai_version: Kai Scheduler Helm chart version.
        skaffold_profile: Skaffold profile for Grove deployment.
        grove_namespace: Kubernetes namespace for Grove operator.
        registry: Container registry URL override, or None for k3d local.
    """

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    kai_version: str = Field(default=DEPENDENCIES['kai_scheduler']['version'],
                             pattern=r"^v[\d.]+(-[\w.]+)?$")
    skaffold_profile: str = DEFAULT_SKAFFOLD_PROFILE
    grove_namespace: str = DEFAULT_GROVE_NAMESPACE
    registry: str | None = None


class KwokConfig(BaseSettings):
    """KWOK and Pyroscope configuration, auto-loaded from E2E_* env vars.

    Attributes:
        kwok_nodes: Number of KWOK nodes to create, or None to skip.
        kwok_batch_size: Number of nodes to create per kubectl apply batch.
        kwok_node_cpu: CPU capacity to advertise per KWOK node.
        kwok_node_memory: Memory capacity to advertise per KWOK node.
        kwok_max_pods: Maximum pods per KWOK node.
        pyroscope_ns: Kubernetes namespace for Pyroscope installation.
    """

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    kwok_nodes: int | None = None
    kwok_batch_size: int = Field(default=DEFAULT_KWOK_BATCH_SIZE, ge=1)
    kwok_node_cpu: str = DEFAULT_KWOK_NODE_CPU
    kwok_node_memory: str = DEFAULT_KWOK_NODE_MEMORY
    kwok_max_pods: int = DEFAULT_KWOK_MAX_PODS
    pyroscope_ns: str = DEFAULT_PYROSCOPE_NAMESPACE


# ============================================================================
# Action flags
# ============================================================================

@dataclass(frozen=True)
class ActionFlags:
    """Single source of truth for what actions to perform.

    Attributes:
        create_k3d: Whether to create a new k3d cluster.
        delete_k3d: Whether to delete the existing k3d cluster.
        install_kai: Whether to install Kai Scheduler.
        install_grove: Whether to deploy Grove operator.
        apply_topology: Whether to apply topology labels to nodes.
        prepull_images: Whether to pre-pull container images.
        create_kwok_nodes: Whether to create KWOK simulated nodes.
        kwok_node_count: Number of KWOK nodes to create.
        delete_kwok_nodes: Whether to delete existing KWOK nodes.
        install_pyroscope: Whether to install Pyroscope.
        grove_profiling: Whether to enable pprof on Grove.
        grove_pcs_syncs: PodCliqueSet concurrent syncs override, or None.
        grove_pclq_syncs: PodClique concurrent syncs override, or None.
        grove_pcsg_syncs: PodCliqueScalingGroup concurrent syncs override, or None.
        registry: Container registry URL override, or None for k3d local.
    """

    create_k3d: bool
    delete_k3d: bool
    install_kai: bool
    install_grove: bool
    apply_topology: bool
    prepull_images: bool
    create_kwok_nodes: bool
    kwok_node_count: int
    delete_kwok_nodes: bool
    install_pyroscope: bool
    grove_profiling: bool
    grove_pcs_syncs: int | None
    grove_pclq_syncs: int | None
    grove_pcsg_syncs: int | None
    registry: str | None


# ============================================================================
# Config resolution
# ============================================================================

def validate_flags(
    skip_cluster_creation: bool,
    skip_kai: bool,
    skip_grove: bool,
    skip_prepull: bool,
    delete: bool,
    registry: str | None,
    grove_profiling: bool,
    grove_pcs_syncs: int | None,
    grove_pclq_syncs: int | None,
    grove_pcsg_syncs: int | None,
) -> None:
    """Validate flag combinations for mutual exclusivity and consistency.

    Args:
        skip_cluster_creation: Whether cluster creation is skipped.
        skip_kai: Whether Kai Scheduler installation is skipped.
        skip_grove: Whether Grove deployment is skipped.
        skip_prepull: Whether image pre-pulling is skipped.
        delete: Whether the cluster should be deleted.
        registry: Container registry URL override, or None.
        grove_profiling: Whether pprof profiling is enabled.
        grove_pcs_syncs: PodCliqueSet concurrent syncs override, or None.
        grove_pclq_syncs: PodClique concurrent syncs override, or None.
        grove_pcsg_syncs: PodCliqueScalingGroup concurrent syncs override, or None.

    Raises:
        typer.BadParameter: If prepull is enabled without cluster creation.
    """
    if not skip_prepull and skip_cluster_creation and not delete:
        raise typer.BadParameter(
            "--skip-prepull must be set when --skip-cluster-creation is used (prepull requires k3d)"
        )

    install_grove = not skip_grove and not delete
    if delete and (not skip_kai or not skip_grove):
        logger.warning("--delete is set; install flags will be ignored")

    if install_grove and not registry and skip_cluster_creation:
        logger.warning(
            "--registry not set with --skip-cluster-creation; "
            "will use k3d local registry (localhost:<port>/registry:<port>)"
        )

    grove_tuning = grove_profiling or grove_pcs_syncs or grove_pclq_syncs or grove_pcsg_syncs
    if grove_tuning and not install_grove:
        logger.warning("Grove tuning flags ignored because Grove is not being installed")


def resolve_config(
    skip_cluster_creation: bool,
    skip_kai: bool,
    skip_grove: bool,
    skip_topology: bool,
    skip_prepull: bool,
    delete: bool,
    workers: int | None,
    registry: str | None,
    kwok_nodes: int | None,
    kwok_batch_size: int | None,
    kwok_delete: bool,
    pyroscope: bool,
    pyroscope_namespace: str | None,
    grove_profiling: bool,
    grove_pcs_syncs: int | None,
    grove_pclq_syncs: int | None,
    grove_pcsg_syncs: int | None,
) -> tuple[K3dConfig, ComponentConfig, KwokConfig, ActionFlags]:
    """Merge CLI overrides, environment variables, and defaults into config objects.

    Resolution priority: CLI arguments > E2E_* environment variables > defaults.

    Args:
        skip_cluster_creation: Whether cluster creation is skipped.
        skip_kai: Whether Kai Scheduler installation is skipped.
        skip_grove: Whether Grove deployment is skipped.
        skip_topology: Whether topology label application is skipped.
        skip_prepull: Whether image pre-pulling is skipped.
        delete: Whether the cluster should be deleted.
        workers: CLI override for worker node count, or None.
        registry: Container registry URL override, or None.
        kwok_nodes: Number of KWOK nodes to create, or None.
        kwok_batch_size: KWOK node creation batch size override, or None.
        kwok_delete: Whether to delete KWOK nodes.
        pyroscope: Whether to install Pyroscope.
        pyroscope_namespace: Pyroscope namespace override, or None.
        grove_profiling: Whether pprof profiling is enabled.
        grove_pcs_syncs: PodCliqueSet concurrent syncs override, or None.
        grove_pclq_syncs: PodClique concurrent syncs override, or None.
        grove_pcsg_syncs: PodCliqueScalingGroup concurrent syncs override, or None.

    Returns:
        Tuple of (K3dConfig, ComponentConfig, KwokConfig, ActionFlags).
    """
    k3d_cfg = K3dConfig()
    comp_cfg = ComponentConfig()
    kwok_cfg = KwokConfig()

    # CLI overrides (CLI > env > default)
    if workers is not None:
        k3d_cfg = k3d_cfg.model_copy(update={"worker_nodes": workers})
    if registry is not None:
        comp_cfg = comp_cfg.model_copy(update={"registry": registry})
    if kwok_nodes is not None:
        kwok_cfg = kwok_cfg.model_copy(update={"kwok_nodes": kwok_nodes})
    if kwok_batch_size is not None:
        kwok_cfg = kwok_cfg.model_copy(update={"kwok_batch_size": kwok_batch_size})
    if pyroscope_namespace is not None:
        kwok_cfg = kwok_cfg.model_copy(update={"pyroscope_ns": pyroscope_namespace})

    resolved_kwok_count = kwok_cfg.kwok_nodes or 0

    flags = ActionFlags(
        create_k3d=not skip_cluster_creation and not delete,
        delete_k3d=delete,
        install_kai=not skip_kai and not delete,
        install_grove=not skip_grove and not delete,
        apply_topology=not skip_topology and not delete,
        prepull_images=not skip_prepull and not skip_cluster_creation and not delete,
        create_kwok_nodes=kwok_nodes is not None,
        kwok_node_count=resolved_kwok_count,
        delete_kwok_nodes=kwok_delete,
        install_pyroscope=pyroscope,
        grove_profiling=grove_profiling,
        grove_pcs_syncs=grove_pcs_syncs,
        grove_pclq_syncs=grove_pclq_syncs,
        grove_pcsg_syncs=grove_pcsg_syncs,
        registry=comp_cfg.registry,
    )

    return k3d_cfg, comp_cfg, kwok_cfg, flags


# ============================================================================
# Display
# ============================================================================

def display_config(
    flags: ActionFlags,
    k3d_cfg: K3dConfig,
    comp_cfg: ComponentConfig,
    kwok_cfg: KwokConfig,
) -> None:
    """Print only config relevant to requested actions.

    Args:
        flags: Resolved action flags controlling what to display.
        k3d_cfg: k3d cluster configuration.
        comp_cfg: Component versions and registry configuration.
        kwok_cfg: KWOK and Pyroscope configuration.
    """
    console.print(Panel.fit("Configuration", style="bold blue"))

    if flags.create_k3d or flags.delete_k3d:
        console.print("[yellow]k3d cluster:[/yellow]")
        console.print(f"  cluster_name    : {k3d_cfg.cluster_name}")
        console.print(f"  registry_port   : {k3d_cfg.registry_port}")
        console.print(f"  api_port        : {k3d_cfg.api_port}")
        console.print(f"  lb_port         : {k3d_cfg.lb_port}")
        console.print(f"  worker_nodes    : {k3d_cfg.worker_nodes}")
        console.print(f"  worker_memory   : {k3d_cfg.worker_memory}")
        console.print(f"  k3s_image       : {k3d_cfg.k3s_image}")

    if flags.install_kai:
        console.print("[yellow]Kai Scheduler:[/yellow]")
        console.print(f"  kai_version     : {comp_cfg.kai_version}")

    if flags.install_grove:
        console.print("[yellow]Grove:[/yellow]")
        console.print(f"  skaffold_profile: {comp_cfg.skaffold_profile}")
        console.print(f"  registry        : {comp_cfg.registry or '(auto from k3d)'}")

    if flags.create_kwok_nodes or flags.delete_kwok_nodes:
        console.print("[yellow]KWOK:[/yellow]")
        console.print(f"  kwok_nodes      : {flags.kwok_node_count}")
        console.print(f"  kwok_batch_size : {kwok_cfg.kwok_batch_size}")

    if flags.install_pyroscope:
        console.print("[yellow]Pyroscope:[/yellow]")
        console.print(f"  namespace       : {kwok_cfg.pyroscope_ns}")


# ============================================================================
# Utility functions
# ============================================================================

def _resolve_bool_flag(name: str, value: Any) -> bool:
    """Resolve a Typer boolean flag value with sys.argv fallback.

    Workaround: Typer has issues with boolean flags in some environments,
    passing None or strings instead of True/False.

    Args:
        name: Python parameter name (underscores), e.g. ``skip_kai``.
        value: Raw value from Typer.

    Returns:
        Resolved boolean.
    """
    flag = f"--{name.replace('_', '-')}"
    if flag in sys.argv:
        return True
    if value is None:
        return False
    if isinstance(value, str):
        return value.lower() not in ("false", "0", "", "no", "n", "none")
    return bool(value)


def dep_value(*keys: str, default: Any = None) -> Any:
    """Safely traverse the DEPENDENCIES dict by key path.

    Args:
        *keys: Sequence of dictionary keys to traverse.
        default: Value to return if any key is missing.

    Returns:
        The value at the nested key path, or *default* if not found.
    """
    node = DEPENDENCIES
    for key in keys:
        if not isinstance(node, dict):
            return default
        node = node.get(key)
        if node is None:
            return default
    return node


def _kwok_release_url(version: str) -> str:
    """Build the GitHub release base URL for a KWOK version.

    Args:
        version: KWOK release version tag (e.g. ``v0.7.0``).

    Returns:
        Full GitHub release download URL.
    """
    return f"https://github.com/{KWOK_GITHUB_REPO}/releases/download/{version}"


def _resolve_registry_repos(registry: str | None, port: int) -> tuple[str, str]:
    """Resolve push/pull registry repos.

    k3d uses separate names for push (localhost:<port>) and pull (registry:<port>)
    because the push happens from the host while the pull happens inside the cluster.

    Args:
        registry: Explicit registry URL override, or None for k3d local.
        port: k3d local registry port number.

    Returns:
        Tuple of (push_repo, pull_repo) registry URLs.
    """
    if registry:
        return registry, registry
    return f"localhost:{port}", f"registry:{port}"


def _collect_grove_helm_overrides(flags: ActionFlags) -> list[str]:
    """Build helm override strings from grove tuning flags.

    Args:
        flags: Resolved action flags containing grove tuning values.

    Returns:
        List of ``key=value`` strings for ``helm --set`` arguments.
    """
    overrides: list[tuple[bool, str, str]] = [
        (flags.grove_profiling, HELM_KEY_PROFILING, "true"),
        (flags.grove_pcs_syncs is not None, HELM_KEY_PCS_SYNCS, str(flags.grove_pcs_syncs)),
        (flags.grove_pclq_syncs is not None, HELM_KEY_PCLQ_SYNCS, str(flags.grove_pclq_syncs)),
        (flags.grove_pcsg_syncs is not None, HELM_KEY_PCSG_SYNCS, str(flags.grove_pcsg_syncs)),
    ]
    return [f"{key}={value}" for enabled, key, value in overrides if enabled]


def require_command(cmd: str) -> None:
    """Check if a command exists on the system PATH.

    Args:
        cmd: Name of the CLI command to check.

    Raises:
        RuntimeError: If the command is not found.
    """
    try:
        sh.which(cmd)
    except sh.ErrorReturnCode:
        raise RuntimeError(f"Required command '{cmd}' not found. Please install it first.")



def run_kubectl(args: list[str], timeout: int = 30) -> tuple[bool, str, str]:
    """Run a kubectl command via subprocess and return (success, stdout, stderr).

    Uses subprocess instead of sh because kubectl output parsing requires
    precise control over stdout/stderr separation that sh's combined output
    makes unreliable (e.g., checking webhook readiness keywords).

    Args:
        args: kubectl arguments (e.g. ``["get", "pods", "-n", "default"]``).
        timeout: Maximum seconds to wait for the command to complete.

    Returns:
        Tuple of (success, stdout, stderr).
    """
    try:
        result = subprocess.run(
            ["kubectl", *args],
            capture_output=True, text=True, timeout=timeout,
        )
        return result.returncode == 0, result.stdout, result.stderr
    except (subprocess.SubprocessError, OSError) as exc:
        return False, "", str(exc)


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


def _run_parallel_pulls(
    docker_client: docker.DockerClient,
    images: list[str],
    registry_port: int,
    version: str,
) -> list[str]:
    """Pull images in parallel with progress bar. Returns failed image names."""
    failed_images: list[str] = []
    with Progress(
        SpinnerColumn(), TextColumn("[progress.description]{task.description}"),
        BarColumn(), TaskProgressColumn(), console=console,
    ) as progress:
        task = progress.add_task("[cyan]Pulling images...", total=len(images))
        with ThreadPoolExecutor(max_workers=DEFAULT_IMAGE_PULL_MAX_WORKERS) as executor:
            futures = {
                executor.submit(_pull_tag_push, docker_client, img, registry_port, version): img
                for img in images
            }
            for future in as_completed(futures):
                image_name, success, error = future.result()
                progress.advance(task)
                if success:
                    console.print(f"[green]✓ {image_name}[/green]")
                else:
                    console.print(f"[red]✗ {image_name} - {error}[/red]")
                    failed_images.append(image_name)
    return failed_images


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

    try:
        failed_images = _run_parallel_pulls(docker_client, images, registry_port, version)
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

def delete_cluster(k3d_cfg: K3dConfig) -> None:
    """Delete the k3d cluster.

    Args:
        k3d_cfg: k3d cluster configuration with the cluster name.
    """
    console.print(f"[yellow]ℹ️  Deleting k3d cluster '{k3d_cfg.cluster_name}'...[/yellow]")
    try:
        sh.k3d("cluster", "delete", k3d_cfg.cluster_name)
        console.print(f"[green]✅ Cluster '{k3d_cfg.cluster_name}' deleted[/green]")
    except sh.ErrorReturnCode_1:
        console.print(f"[yellow]⚠️  Cluster '{k3d_cfg.cluster_name}' not found or already deleted[/yellow]")


def create_cluster(k3d_cfg: K3dConfig) -> None:
    """Create a k3d cluster with retry logic.

    Args:
        k3d_cfg: k3d cluster configuration including retry count.

    Raises:
        RetryError: If the cluster cannot be created after all retries.
    """
    console.print(Panel.fit("Creating k3d cluster", style="bold blue"))

    @retry(
        stop=stop_after_attempt(k3d_cfg.max_retries),
        wait=wait_fixed(CLUSTER_CREATE_RETRY_WAIT_SECONDS),
        reraise=True,
    )
    def _attempt() -> None:
        try:
            sh.k3d("cluster", "delete", k3d_cfg.cluster_name)
            console.print("[yellow]   Removed existing cluster[/yellow]")
        except sh.ErrorReturnCode_1:
            console.print("[yellow]   No existing cluster found[/yellow]")

        sh.k3d(
            "cluster", "create", k3d_cfg.cluster_name,
            "--servers", "1",
            "--agents", str(k3d_cfg.worker_nodes),
            "--image", k3d_cfg.k3s_image,
            "--api-port", k3d_cfg.api_port,
            "--port", f"{k3d_cfg.lb_port}@loadbalancer",
            "--registry-create", f"registry:0.0.0.0:{k3d_cfg.registry_port}",
            "--k3s-arg", f"--node-taint={_E2E_NODE_ROLE_KEY}=agent:NoSchedule@agent:*",
            "--k3s-node-label", f"{_E2E_NODE_ROLE_KEY}=agent@agent:*",
            "--k3s-node-label", "nvidia.com/gpu.deploy.operands=false@server:*",
            "--k3s-node-label", "nvidia.com/gpu.deploy.operands=false@agent:*",
            "--agents-memory", k3d_cfg.worker_memory,
            "--timeout", CLUSTER_TIMEOUT,
            "--wait",
        )

    _attempt()
    console.print("[green]✅ Cluster created successfully[/green]")


def wait_for_nodes() -> None:
    """Wait for all nodes to be ready."""
    console.print("[yellow]ℹ️  Waiting for all nodes to be ready...[/yellow]")
    sh.kubectl("wait", "--for=condition=Ready", "nodes", "--all", "--timeout=5m")
    console.print("[green]✅ All nodes are ready[/green]")


def install_kai_scheduler(comp_cfg: ComponentConfig) -> None:
    """Install Kai Scheduler using Helm.

    Args:
        comp_cfg: Component configuration with the Kai version.
    """
    console.print(Panel.fit("Installing Kai Scheduler", style="bold blue"))
    console.print(f"[yellow]Version: {comp_cfg.kai_version}[/yellow]")
    try:
        sh.helm("uninstall", HELM_RELEASE_KAI, "-n", NS_KAI_SCHEDULER)
        console.print("[yellow]   Removed existing Kai Scheduler release[/yellow]")
    except sh.ErrorReturnCode_1:
        console.print("[yellow]   No existing Kai Scheduler release found[/yellow]")
    sh.helm(
        "install", HELM_RELEASE_KAI,
        _KAI_SCHEDULER_OCI,
        "--version", comp_cfg.kai_version,
        "--namespace", NS_KAI_SCHEDULER,
        "--create-namespace",
        "--set", f"global.tolerations[0].key={LABEL_CONTROL_PLANE}",
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
    """Dry-run kubectl create to verify the Grove webhook is responding.

    Args:
        operator_dir: Root directory of the Grove operator source tree.

    Raises:
        RuntimeError: If the webhook response does not contain expected keywords.
    """
    try:
        result = sh.kubectl(
            "create", "-f", str(operator_dir / REL_WORKLOAD_YAML),
            "--dry-run=server", "-n", NS_DEFAULT,
        )
        output = str(result).lower()
    except sh.ErrorReturnCode as e:
        output = (str(e.stdout) + str(e.stderr)).lower()
    if not any(kw in output for kw in _WEBHOOK_READY_KEYWORDS):
        raise RuntimeError("Grove webhook not ready")


def _build_grove_images(comp_cfg: ComponentConfig, operator_dir: Path, push_repo: str) -> dict[str, str]:
    """Run skaffold build and return the built image map.

    Args:
        comp_cfg: Component configuration with the skaffold profile.
        operator_dir: Root directory of the Grove operator source tree.
        push_repo: Registry URL to push built images to.

    Returns:
        Dictionary mapping image names to their full tagged references.
    """
    console.print(f"[yellow]ℹ️  Building images (push to {push_repo})...[/yellow]")
    build_output = json.loads(
        sh.skaffold(
            "build",
            "--default-repo", push_repo,
            "--profile", comp_cfg.skaffold_profile,
            "--quiet",
            "--output={{json .}}",
            _cwd=str(operator_dir)
        )
    )
    return {build["imageName"]: build["tag"] for build in build_output.get("builds", [])}


def _deploy_grove_charts(
    comp_cfg: ComponentConfig,
    operator_dir: Path,
    images: dict[str, str],
    pull_repo: str,
) -> None:
    """Run skaffold deploy with resolved image tags.

    Args:
        comp_cfg: Component configuration with skaffold profile and namespace.
        operator_dir: Root directory of the Grove operator source tree.
        images: Dictionary mapping image names to their full tagged references.
        pull_repo: Registry URL the cluster uses to pull images.
    """
    console.print("[yellow]Deploying with images:[/yellow]")
    for name, tag in images.items():
        console.print(f"  {name}={tag}")

    os.environ["CONTAINER_REGISTRY"] = pull_repo
    sh.skaffold(
        "deploy",
        "--profile", comp_cfg.skaffold_profile,
        "--namespace", comp_cfg.grove_namespace,
        "--status-check=false",
        "--default-repo=",
        "--images", f"{_GROVE_OPERATOR_IMAGE}={images[_GROVE_OPERATOR_IMAGE]}",
        "--images", f"{_GROVE_INITC_IMAGE}={images[_GROVE_INITC_IMAGE]}",
        _cwd=str(operator_dir)
    )
    console.print("[green]✅ Grove operator deployed[/green]")


def _apply_grove_helm_overrides(operator_dir: Path, helm_set_values: list[str], grove_namespace: str) -> None:
    """Apply helm value overrides via ``helm upgrade --reuse-values``.

    Args:
        operator_dir: Root directory of the Grove operator source tree.
        helm_set_values: List of ``key=value`` strings for ``--set`` arguments.
        grove_namespace: Kubernetes namespace where Grove is installed.
    """
    if not helm_set_values:
        return
    console.print("[yellow]ℹ️  Applying Grove helm overrides...[/yellow]")
    set_args = [item for val in helm_set_values for item in ("--set", val)]
    chart_path = str(operator_dir / REL_CHARTS_DIR)
    sh.helm(
        "upgrade", HELM_RELEASE_GROVE, chart_path,
        "-n", grove_namespace,
        "--reuse-values",
        *set_args,
    )
    console.print("[green]✅ Grove helm overrides applied[/green]")


def _wait_grove_webhook(operator_dir: Path) -> None:
    """Wait for Grove webhook to become responsive.

    Args:
        operator_dir: Root directory of the Grove operator source tree.

    Raises:
        RuntimeError: If the webhook does not become ready within the timeout.
    """
    console.print("[yellow]ℹ️  Waiting for Grove webhook to be ready...[/yellow]")
    try:
        _check_grove_webhook_ready(operator_dir)
        console.print("[green]✅ Grove webhook is ready[/green]")
    except (RuntimeError, RetryError):
        raise RuntimeError("Timed out waiting for Grove webhook")


def deploy_grove_operator(
    k3d_cfg: K3dConfig,
    comp_cfg: ComponentConfig,
    operator_dir: Path,
    flags: ActionFlags,
) -> None:
    """Deploy Grove operator using Skaffold.

    Builds images, deploys charts, applies helm overrides, and waits for
    the webhook to become responsive.

    Args:
        k3d_cfg: k3d cluster configuration for registry port resolution.
        comp_cfg: Component configuration with skaffold profile and namespace.
        operator_dir: Root directory of the Grove operator source tree.
        flags: Resolved action flags containing registry and tuning overrides.
    """
    console.print(Panel.fit("Deploying Grove operator", style="bold blue"))
    try:
        sh.helm("uninstall", HELM_RELEASE_GROVE, "-n", comp_cfg.grove_namespace)
        console.print("[yellow]   Removed existing Grove operator release[/yellow]")
    except sh.ErrorReturnCode_1:
        console.print("[yellow]   No existing Grove operator release found[/yellow]")

    build_date = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    os.environ.update({
        "VERSION": E2E_TEST_VERSION,
        "LD_FLAGS": (
            f"-X {_GROVE_MODULE_PATH}.gitCommit={E2E_TEST_COMMIT} "
            f"-X {_GROVE_MODULE_PATH}.gitTreeState={E2E_TEST_TREE_STATE} "
            f"-X {_GROVE_MODULE_PATH}.buildDate={build_date} "
            f"-X {_GROVE_MODULE_PATH}.gitVersion={E2E_TEST_VERSION}"
        )
    })

    push_repo, pull_repo = _resolve_registry_repos(flags.registry, k3d_cfg.registry_port)

    raw_images = _build_grove_images(comp_cfg, operator_dir, push_repo)
    images = {name: tag.replace(push_repo, pull_repo) for name, tag in raw_images.items()}

    _deploy_grove_charts(comp_cfg, operator_dir, images, pull_repo)

    helm_overrides = _collect_grove_helm_overrides(flags)
    _apply_grove_helm_overrides(operator_dir, helm_overrides, comp_cfg.grove_namespace)

    console.print("[yellow]ℹ️  Waiting for Grove deployment rollout...[/yellow]")
    sh.kubectl("rollout", "status", "deployment", "-n", comp_cfg.grove_namespace, "--timeout=5m")

    _wait_grove_webhook(operator_dir)


def _node_sort_key(name: str) -> tuple[str, int]:
    """Sort key that strips trailing digits and returns (prefix, number).

    Args:
        name: Kubernetes node name (e.g. ``k3d-cluster-agent-12``).

    Returns:
        Tuple of (name_prefix, trailing_number) for natural sort ordering.
    """
    m = re.search(r"-(\d+)$", name)
    return re.sub(r"-\d+$", "", name), (int(m.group(1)) if m else 0)


def apply_topology_labels() -> None:
    """Apply zone, block, and rack topology labels to all worker nodes."""
    console.print(Panel.fit("Applying topology labels to worker nodes", style="bold blue"))

    nodes_output = sh.kubectl(
        "get", "nodes",
        "-l", f"!{LABEL_CONTROL_PLANE}",
        "-o", "jsonpath={.items[*].metadata.name}"
    ).strip()

    worker_nodes = sorted(nodes_output.split(), key=_node_sort_key)
    for idx, node in enumerate(worker_nodes):
        zone = idx // NODES_PER_ZONE
        block = idx // NODES_PER_BLOCK
        rack = idx // NODES_PER_RACK
        sh.kubectl(
            "label", "node", node,
            f"{LABEL_ZONE}=zone-{zone}",
            f"{LABEL_BLOCK}=block-{block}",
            f"{LABEL_RACK}=rack-{rack}",
            "--overwrite"
        )
    console.print(f"[green]✅ Applied topology labels to {len(worker_nodes)} worker nodes[/green]")


# ============================================================================
# KWOK functions
# ============================================================================

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
    base_url = _kwok_release_url(version)
    for manifest in KWOK_MANIFESTS:
        ok, _, stderr = run_kubectl(["apply", "-f", f"{base_url}/{manifest}"], timeout=60)
        if not ok:
            console.print(f"[yellow]⚠️  Partial failure applying {manifest}: {stderr[:200]}[/yellow]")
            console.print("[yellow]   (This is usually safe if KWOK is already installed)[/yellow]")

    console.print("[yellow]ℹ️  Waiting for KWOK controller to be available...[/yellow]")
    ok, _, stderr = run_kubectl([
        "wait", "--for=condition=Available",
        f"deployment/{KWOK_CONTROLLER_DEPLOYMENT}", "-n", NS_KUBE_SYSTEM,
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


def create_nodes(total: int, kwok_cfg: KwokConfig) -> None:
    """Create KWOK nodes in batches.

    Args:
        total: Total number of KWOK nodes to create.
        kwok_cfg: KWOK configuration with batch size and node resource limits.
    """
    logger.info("Creating %d KWOK nodes (batch size=%d)...", total, kwok_cfg.kwok_batch_size)
    successes: list[int] = []
    failures: list[tuple[int, str]] = []

    for batch_start in range(0, total, kwok_cfg.kwok_batch_size):
        batch_end = min(batch_start + kwok_cfg.kwok_batch_size, total)
        node_ids = list(range(batch_start, batch_end))
        batch_ok, batch_fail = create_node_batch(node_ids, kwok_cfg)
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
    """Delete all KWOK simulated nodes and uninstall the KWOK controller.

    Raises:
        RuntimeError: If node deletion fails.
    """
    console.print("[yellow]ℹ️  Deleting all KWOK nodes...[/yellow]")
    ok, _, stderr = run_kubectl(
        ["delete", "nodes", "-l", f"{LABEL_TYPE}={LABEL_TYPE_KWOK}", "--ignore-not-found"],
        timeout=300,
    )
    if ok:
        console.print("[green]✅ KWOK nodes deleted[/green]")
    else:
        raise RuntimeError(f"Failed to delete KWOK nodes: {stderr[:200]}")

    console.print("[yellow]ℹ️  Uninstalling KWOK controller...[/yellow]")
    kwok_version = dep_value("kwok_controller", "version", default=DEFAULT_KWOK_VERSION)
    base_url = _kwok_release_url(kwok_version)
    for manifest in reversed(KWOK_MANIFESTS):
        run_kubectl(["delete", "-f", f"{base_url}/{manifest}", "--ignore-not-found"], timeout=120)
    console.print("[green]✅ KWOK controller uninstalled[/green]")


def install_pyroscope(namespace: str, values_file: Path | None = None, version: str = "") -> None:
    """Install Pyroscope via Helm.

    Args:
        namespace: Kubernetes namespace for Pyroscope installation.
        values_file: Optional path to a Helm values override file.
        version: Helm chart version to install, or empty string for latest.

    Raises:
        RuntimeError: If namespace creation fails.
    """
    console.print(Panel.fit(f"Installing Pyroscope (namespace: {namespace})", style="bold blue"))
    require_command("helm")

    sh.helm("repo", "add", HELM_REPO_GRAFANA, HELM_REPO_GRAFANA_URL, "--force-update")
    sh.helm("repo", "update", HELM_REPO_GRAFANA)

    ok, _, stderr = run_kubectl(["create", "namespace", namespace])
    if not ok and "AlreadyExists" not in stderr:
        raise RuntimeError(f"Failed to create namespace {namespace}: {stderr}")

    helm_args = ["upgrade", "--install", HELM_RELEASE_PYROSCOPE, HELM_CHART_PYROSCOPE, "-n", namespace]
    if version:
        helm_args += ["--version", version]
    if values_file and values_file.exists():
        helm_args += ["-f", str(values_file)]
    sh.helm(*helm_args)
    console.print("[green]✅ Pyroscope installed[/green]")


# ============================================================================
# Kai queue helper
# ============================================================================

@retry(
    stop=stop_after_attempt(KAI_QUEUE_MAX_RETRIES),
    wait=wait_fixed(KAI_QUEUE_POLL_INTERVAL_SECONDS),
    reraise=True,
)
def _apply_kai_queues(queues_file: Path) -> None:
    """Apply Kai queue CRs with retry for webhook readiness.

    Args:
        queues_file: Path to the Kai queues YAML manifest.

    Raises:
        RuntimeError: If the Kai queue webhook is not ready.
    """
    try:
        sh.kubectl("apply", "-f", str(queues_file))
    except sh.ErrorReturnCode:
        raise RuntimeError("Kai queue webhook not ready")


# ============================================================================
# Orchestration
# ============================================================================

def _run_prerequisites(flags: ActionFlags, operator_dir: Path) -> None:
    """Check CLI tools and prepare Helm charts.

    Args:
        flags: Resolved action flags to determine which tools are needed.
        operator_dir: Root directory of the Grove operator source tree.
    """
    prereqs = ["k3d", "kubectl", "docker"]
    if flags.install_kai:
        prereqs.append("helm")
    if flags.install_grove:
        prereqs.extend(["skaffold", "jq"])
    console.print(Panel.fit("Checking prerequisites", style="bold blue"))
    for cmd in prereqs:
        require_command(cmd)
    console.print("[green]✅ All required tools are available[/green]")

    if flags.install_grove:
        console.print(Panel.fit("Preparing Helm charts", style="bold blue"))
        prepare_charts = operator_dir / REL_PREPARE_CHARTS
        if prepare_charts.exists():
            sh.bash(str(prepare_charts))
            console.print("[green]✅ Charts prepared[/green]")


def _run_cluster_creation(k3d_cfg: K3dConfig) -> None:
    """Create k3d cluster and wait for nodes.

    Args:
        k3d_cfg: k3d cluster configuration including retry count.

    Raises:
        RetryError: If the cluster cannot be created after all retries.
    """
    create_cluster(k3d_cfg)
    wait_for_nodes()


def _run_prepull(k3d_cfg: K3dConfig) -> None:
    """Pre-pull images to local registry.

    Args:
        k3d_cfg: k3d cluster configuration with the registry port.
    """
    prepull_images(
        DEPENDENCIES['kai_scheduler']['images'],
        k3d_cfg.registry_port, DEPENDENCIES['kai_scheduler']['version'],
    )
    prepull_images(
        DEPENDENCIES['cert_manager']['images'],
        k3d_cfg.registry_port, DEPENDENCIES['cert_manager']['version'],
    )
    busybox_images = dep_value('test_images', 'busybox')
    if busybox_images:
        prepull_images(busybox_images, k3d_cfg.registry_port, 'latest')


def _run_kai_post_install(operator_dir: Path) -> None:
    """Wait for Kai pods and create queues.

    Args:
        operator_dir: Root directory of the Grove operator source tree.

    Raises:
        RuntimeError: If Kai queue creation fails after retries.
    """
    console.print("[yellow]ℹ️  Waiting for Kai Scheduler pods to be ready...[/yellow]")
    sh.kubectl("wait", "--for=condition=Ready", "pods", "--all",
               "-n", NS_KAI_SCHEDULER, "--timeout=5m")
    console.print("[yellow]ℹ️  Creating default Kai queues (with retry for webhook readiness)...[/yellow]")
    try:
        _apply_kai_queues(operator_dir / REL_QUEUES_YAML)
        console.print("[green]✅ Kai queues created successfully[/green]")
    except (RuntimeError, RetryError):
        raise RuntimeError("Failed to create Kai queues after retries")


def _run_kubeconfig_merge(k3d_cfg: K3dConfig) -> None:
    """Merge k3d kubeconfig into the default kubeconfig file.

    Args:
        k3d_cfg: k3d cluster configuration with the cluster name.
    """
    console.print(Panel.fit("Configuring kubeconfig", style="bold blue"))
    default_kubeconfig_dir = Path.home() / ".kube"
    default_kubeconfig_dir.mkdir(parents=True, exist_ok=True)
    default_kubeconfig_path = default_kubeconfig_dir / "config"
    sh.k3d("kubeconfig", "merge", k3d_cfg.cluster_name, "-o", str(default_kubeconfig_path))
    default_kubeconfig_path.chmod(0o600)
    console.print(f"[green]  ✓ Merged to {default_kubeconfig_path}[/green]")


def _run_kwok(flags: ActionFlags, kwok_cfg: KwokConfig) -> None:
    """Install KWOK controller and create nodes.

    Args:
        flags: Resolved action flags with KWOK node count.
        kwok_cfg: KWOK configuration with batch size and node resource limits.
    """
    kwok_version = dep_value("kwok_controller", "version", default=DEFAULT_KWOK_VERSION)
    install_kwok_controller(kwok_version)
    if flags.kwok_node_count > 0:
        create_nodes(flags.kwok_node_count, kwok_cfg)


def _run_pyroscope(kwok_cfg: KwokConfig, script_dir: Path) -> None:
    """Install Pyroscope.

    Args:
        kwok_cfg: KWOK configuration with the Pyroscope namespace.
        script_dir: Directory containing the Pyroscope values file.
    """
    values_file = script_dir / "pyroscope-values.yaml"
    pyroscope_version = dep_value("pyroscope", "version", default="")
    install_pyroscope(kwok_cfg.pyroscope_ns, values_file, version=pyroscope_version)

def _run(
    flags: ActionFlags,
    k3d_cfg: K3dConfig,
    comp_cfg: ComponentConfig,
    kwok_cfg: KwokConfig,
    operator_dir: Path,
    script_dir: Path,
) -> None:
    """Orchestrate all requested actions.

    Args:
        flags: Resolved action flags controlling which steps to run.
        k3d_cfg: k3d cluster configuration.
        comp_cfg: Component versions and registry configuration.
        kwok_cfg: KWOK and Pyroscope configuration.
        operator_dir: Root directory of the Grove operator source tree.
        script_dir: Directory containing the e2e cluster script and helpers.

    Raises:
        RuntimeError: If any step fails.
    """
    if flags.delete_kwok_nodes:
        delete_kwok_nodes()
        return

    if flags.delete_k3d:
        delete_cluster(k3d_cfg)
        return

    if flags.create_k3d:
        _run_prerequisites(flags, operator_dir)
        _run_cluster_creation(k3d_cfg)
        if flags.prepull_images:
            _run_prepull(k3d_cfg)

    if flags.install_kai:
        install_kai_scheduler(comp_cfg)

    if flags.install_grove:
        deploy_grove_operator(k3d_cfg, comp_cfg, operator_dir, flags)

    if flags.install_kai:
        _run_kai_post_install(operator_dir)

    if flags.apply_topology:
        apply_topology_labels()

    if flags.create_k3d:
        _run_kubeconfig_merge(k3d_cfg)

    if flags.create_kwok_nodes:
        _run_kwok(flags, kwok_cfg)

    if flags.install_pyroscope:
        _run_pyroscope(kwok_cfg, script_dir)

# ============================================================================
# CLI entry point
# ============================================================================

@app.command()
def main(
    # Opt-out flags (on by default)
    skip_cluster_creation: bool = typer.Option(
        False, "--skip-cluster-creation", help="Skip k3d cluster creation"),
    skip_kai: bool = typer.Option(
        False, "--skip-kai", help="Skip Kai Scheduler installation"),
    skip_grove: bool = typer.Option(
        False, "--skip-grove", help="Skip Grove operator deployment"),
    skip_topology: bool = typer.Option(
        False, "--skip-topology", help="Skip topology label application"),
    skip_prepull: bool = typer.Option(
        False, "--skip-prepull", help="Skip image pre-pulling"),
    # Opt-in flags
    delete: bool = typer.Option(
        False, "--delete", help="Delete the k3d cluster and exit"),
    workers: int | None = typer.Option(
        None, "--workers", help="k3d worker nodes (overrides E2E_WORKER_NODES)"),
    registry: str | None = typer.Option(
        None, "--registry", help="Container registry URL"),
    # KWOK
    kwok_nodes: int | None = typer.Option(
        None, "--kwok-nodes", help="Create N KWOK simulated nodes"),
    kwok_batch_size: int | None = typer.Option(
        None, "--kwok-batch-size", help="Node creation batch size"),
    kwok_delete: bool = typer.Option(
        False, "--kwok-delete", help="Delete all KWOK simulated nodes"),
    # Pyroscope
    pyroscope: bool = typer.Option(
        False, "--pyroscope", help="Install Pyroscope via Helm"),
    pyroscope_namespace: str | None = typer.Option(
        None, "--pyroscope-namespace", help="Pyroscope namespace (default: pyroscope)"),
    # Grove tuning
    grove_profiling: bool = typer.Option(
        False, "--grove-profiling", help="Enable pprof"),
    grove_pcs_syncs: int | None = typer.Option(
        None, "--grove-pcs-syncs", help="PodCliqueSet concurrentSyncs"),
    grove_pclq_syncs: int | None = typer.Option(
        None, "--grove-pclq-syncs", help="PodClique concurrentSyncs"),
    grove_pcsg_syncs: int | None = typer.Option(
        None, "--grove-pcsg-syncs", help="PodCliqueScalingGroup concurrentSyncs"),
) -> None:
    """Unified cluster setup for Grove E2E testing.

    Default: creates k3d cluster + installs Kai + deploys Grove + applies
    topology + pre-pulls images. Use --skip-* flags to opt out of individual
    steps. All parameters are documented via --help on each CLI option.
    """
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%H:%M:%S",
    )

    skip_cluster_creation = _resolve_bool_flag("skip_cluster_creation", skip_cluster_creation)
    skip_kai = _resolve_bool_flag("skip_kai", skip_kai)
    skip_grove = _resolve_bool_flag("skip_grove", skip_grove)
    skip_topology = _resolve_bool_flag("skip_topology", skip_topology)
    skip_prepull = _resolve_bool_flag("skip_prepull", skip_prepull)
    delete = _resolve_bool_flag("delete", delete)
    kwok_delete = _resolve_bool_flag("kwok_delete", kwok_delete)
    pyroscope = _resolve_bool_flag("pyroscope", pyroscope)
    grove_profiling = _resolve_bool_flag("grove_profiling", grove_profiling)

    validate_flags(
        skip_cluster_creation=skip_cluster_creation,
        skip_kai=skip_kai,
        skip_grove=skip_grove,
        skip_prepull=skip_prepull,
        delete=delete,
        registry=registry,
        grove_profiling=grove_profiling,
        grove_pcs_syncs=grove_pcs_syncs,
        grove_pclq_syncs=grove_pclq_syncs,
        grove_pcsg_syncs=grove_pcsg_syncs,
    )

    k3d_cfg, comp_cfg, kwok_cfg, flags = resolve_config(
        skip_cluster_creation=skip_cluster_creation,
        skip_kai=skip_kai,
        skip_grove=skip_grove,
        skip_topology=skip_topology,
        skip_prepull=skip_prepull,
        delete=delete,
        workers=workers,
        registry=registry,
        kwok_nodes=kwok_nodes,
        kwok_batch_size=kwok_batch_size,
        kwok_delete=kwok_delete,
        pyroscope=pyroscope,
        pyroscope_namespace=pyroscope_namespace,
        grove_profiling=grove_profiling,
        grove_pcs_syncs=grove_pcs_syncs,
        grove_pclq_syncs=grove_pclq_syncs,
        grove_pcsg_syncs=grove_pcsg_syncs,
    )

    display_config(flags, k3d_cfg, comp_cfg, kwok_cfg)

    script_dir = Path(__file__).resolve().parent
    operator_dir = script_dir.parent.parent

    try:
        _run(flags, k3d_cfg, comp_cfg, kwok_cfg, operator_dir, script_dir)
    except Exception as e:
        console.print(f"[red]❌ {e}[/red]")
        sys.exit(1)


if __name__ == "__main__":
    app()
