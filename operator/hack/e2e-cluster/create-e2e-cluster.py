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
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional

import docker
import sh
import typer
import yaml
from concurrent.futures import ThreadPoolExecutor, as_completed
from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict
from rich.console import Console
from rich.panel import Panel
from rich.progress import Progress, SpinnerColumn, TextColumn, BarColumn, TaskProgressColumn
from tenacity import retry, stop_after_attempt, wait_fixed, wait_exponential, retry_if_result, RetryError

console = Console(stderr=True)
logger = logging.getLogger(__name__)

app = typer.Typer(help="Unified cluster setup for Grove E2E testing.")


# ============================================================================
# Constants
# ============================================================================

def load_dependencies() -> dict:
    """Load dependency versions and images from dependencies.yaml."""
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


# ============================================================================
# Configuration classes
# ============================================================================

class K3dConfig(BaseSettings):
    """k3d cluster configuration, auto-loaded from E2E_* env vars."""

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    cluster_name: str = "shared-e2e-test-cluster"
    registry_port: int = Field(default=5001, ge=1, le=65535)
    api_port: int = Field(default=6560, ge=1, le=65535)
    lb_port: str = "8090:80"
    worker_nodes: int = Field(default=30, ge=1, le=100)
    worker_memory: str = Field(default="150m", pattern=r"^\d+[mMgG]?$")
    k3s_image: str = "rancher/k3s:v1.33.5-k3s1"
    max_retries: int = Field(default=3, ge=1, le=10)


class ComponentConfig(BaseSettings):
    """Component versions and registry, auto-loaded from E2E_* env vars."""

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    kai_version: str = Field(default=DEPENDENCIES['kai_scheduler']['version'],
                             pattern=r"^v[\d.]+(-[\w.]+)?$")
    skaffold_profile: str = "topology-test"
    registry: str | None = None


class KwokConfig(BaseSettings):
    """KWOK and Pyroscope configuration, auto-loaded from E2E_* env vars."""

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    kwok_nodes: int | None = None
    kwok_batch_size: int = Field(default=150, ge=1)
    kwok_node_cpu: str = "64"
    kwok_node_memory: str = "512Gi"
    kwok_max_pods: int = 110
    pyroscope_ns: str = "pyroscope"


# ============================================================================
# Action flags
# ============================================================================

@dataclass(frozen=True)
class ActionFlags:
    """Single source of truth for what actions to perform."""

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
    """Validate flag combinations. Raises typer.BadParameter on hard failures."""
    if not skip_prepull and skip_cluster_creation and not delete:
        raise typer.BadParameter(
            "--skip-prepull must be set when --skip-cluster-creation is used (prepull requires k3d)"
        )

    install_grove = not skip_grove and not delete
    if delete and (not skip_kai or not skip_grove):
        logger.warning("--delete is set; install flags will be ignored")

    if install_grove and not registry and skip_cluster_creation:
        raise typer.BadParameter(
            "--registry is required when installing Grove without k3d cluster creation"
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
    """Merge CLI > env > defaults and return resolved config objects + action flags."""
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
    """Print only config relevant to requested actions."""
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

def delete_cluster(k3d_cfg: K3dConfig) -> None:
    """Delete the k3d cluster."""
    console.print(f"[yellow]ℹ️  Deleting k3d cluster '{k3d_cfg.cluster_name}'...[/yellow]")
    exit_code, _ = run_cmd(sh.k3d, "cluster", "delete", k3d_cfg.cluster_name, _ok_code=[0, 1])
    if exit_code == 0:
        console.print(f"[green]✅ Cluster '{k3d_cfg.cluster_name}' deleted[/green]")
    else:
        console.print(f"[yellow]⚠️  Cluster '{k3d_cfg.cluster_name}' not found or already deleted[/yellow]")


def create_cluster(k3d_cfg: K3dConfig) -> bool:
    """Create a k3d cluster with retry logic."""
    console.print(Panel.fit("Creating k3d cluster", style="bold blue"))

    for attempt in range(1, k3d_cfg.max_retries + 1):
        console.print(f"[yellow]ℹ️  Cluster creation attempt {attempt} of {k3d_cfg.max_retries}...[/yellow]")
        exit_code, _ = run_cmd(sh.k3d, "cluster", "delete", k3d_cfg.cluster_name, _ok_code=[0, 1])
        if exit_code == 0:
            console.print("[yellow]   Removed existing cluster[/yellow]")
        else:
            console.print("[yellow]   No existing cluster found (proceeding with creation)[/yellow]")

        exit_code, _ = run_cmd(
            sh.k3d, "cluster", "create", k3d_cfg.cluster_name,
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
            _ok_code=[0, 1]
        )

        if exit_code == 0:
            console.print(f"[green]✅ Cluster created successfully on attempt {attempt}[/green]")
            return True

        if attempt < k3d_cfg.max_retries:
            console.print("[yellow]⚠️  Cluster creation failed, retrying in 10 seconds...[/yellow]")
            time.sleep(10)

    console.print(f"[red]❌ Cluster creation failed after {k3d_cfg.max_retries} attempts[/red]")
    return False


def wait_for_nodes() -> None:
    """Wait for all nodes to be ready."""
    console.print("[yellow]ℹ️  Waiting for all nodes to be ready...[/yellow]")
    sh.kubectl("wait", "--for=condition=Ready", "nodes", "--all", "--timeout=5m")
    console.print("[green]✅ All nodes are ready[/green]")


def install_kai_scheduler(comp_cfg: ComponentConfig) -> None:
    """Install Kai Scheduler using Helm."""
    console.print(Panel.fit("Installing Kai Scheduler", style="bold blue"))
    console.print(f"[yellow]Version: {comp_cfg.kai_version}[/yellow]")
    run_cmd(sh.helm, "uninstall", "kai-scheduler", "-n", "kai-scheduler", _ok_code=[0, 1])
    sh.helm(
        "install", "kai-scheduler",
        _KAI_SCHEDULER_OCI,
        "--version", comp_cfg.kai_version,
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


def _build_grove_images(comp_cfg: ComponentConfig, operator_dir: Path, push_repo: str) -> dict[str, str]:
    """Run skaffold build, return {imageName: tag} map."""
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
    """Run skaffold deploy with resolved image tags."""
    console.print("[yellow]Deploying with images:[/yellow]")
    for name, tag in images.items():
        console.print(f"  {name}={tag}")

    os.environ["CONTAINER_REGISTRY"] = pull_repo
    sh.skaffold(
        "deploy",
        "--profile", comp_cfg.skaffold_profile,
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
    k3d_cfg: K3dConfig,
    comp_cfg: ComponentConfig,
    operator_dir: Path,
    flags: ActionFlags,
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

    if flags.registry:
        push_repo = flags.registry
        pull_repo = flags.registry
    else:
        push_repo = f"localhost:{k3d_cfg.registry_port}"
        pull_repo = f"registry:{k3d_cfg.registry_port}"

    raw_images = _build_grove_images(comp_cfg, operator_dir, push_repo)
    images = {name: tag.replace(push_repo, pull_repo) for name, tag in raw_images.items()}

    _deploy_grove_charts(comp_cfg, operator_dir, images, pull_repo)

    helm_overrides = []
    if flags.grove_profiling:
        helm_overrides.append("config.debugging.enableProfiling=true")
    if flags.grove_pcs_syncs is not None:
        helm_overrides.append(f"config.controllers.podCliqueSet.concurrentSyncs={flags.grove_pcs_syncs}")
    if flags.grove_pclq_syncs is not None:
        helm_overrides.append(f"config.controllers.podClique.concurrentSyncs={flags.grove_pclq_syncs}")
    if flags.grove_pcsg_syncs is not None:
        helm_overrides.append(f"config.controllers.podCliqueScalingGroup.concurrentSyncs={flags.grove_pcsg_syncs}")

    _apply_grove_helm_overrides(operator_dir, helm_overrides)

    console.print("[yellow]ℹ️  Waiting for Grove deployment rollout...[/yellow]")
    sh.kubectl("rollout", "status", "deployment", "-n", "grove-system", "--timeout=5m")

    _wait_grove_webhook(operator_dir)


def _node_sort_key(name: str) -> tuple[str, int]:
    m = re.search(r"-(\d+)$", name)
    return re.sub(r"-\d+$", "", name), (int(m.group(1)) if m else 0)


def apply_topology_labels() -> None:
    """Apply topology labels to worker nodes using module-level constants."""
    console.print(Panel.fit("Applying topology labels to worker nodes", style="bold blue"))

    nodes_output = sh.kubectl(
        "get", "nodes",
        "-l", "!node-role.kubernetes.io/control-plane",
        "-o", "jsonpath={.items[*].metadata.name}"
    ).strip()

    worker_nodes = sorted(nodes_output.split(), key=_node_sort_key)
    for idx, node in enumerate(worker_nodes):
        zone = idx // NODES_PER_ZONE
        block = idx // NODES_PER_BLOCK
        rack = idx // NODES_PER_RACK
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

def topology_labels(node_id: int) -> dict[str, str]:
    """Compute topology labels for a KWOK node using multi-zone index arithmetic."""
    return {
        "kubernetes.io/zone": f"zone-{node_id // NODES_PER_ZONE}",
        "kubernetes.io/block": f"block-{node_id // NODES_PER_BLOCK}",
        "kubernetes.io/rack": f"rack-{node_id // NODES_PER_RACK}",
        "kubernetes.io/hostname": f"kwok-node-{node_id}",
        "type": "kwok",
    }


def node_manifest(node_id: int, kwok_cfg: KwokConfig) -> dict:
    """Build the Kubernetes Node manifest for a KWOK node."""
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
    node_ids: list[int], kwok_cfg: KwokConfig
) -> tuple[list[int], list[tuple[int, str]]]:
    """Create a batch of KWOK nodes with a single kubectl apply."""
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
    """Create KWOK nodes in batches."""
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
# Kai queue helper
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


# ============================================================================
# Orchestration
# ============================================================================

def _run(
    flags: ActionFlags,
    k3d_cfg: K3dConfig,
    comp_cfg: ComponentConfig,
    kwok_cfg: KwokConfig,
    operator_dir: Path,
    script_dir: Path,
) -> None:
    """Orchestrate all requested actions. Raises RuntimeError on failure."""
    if flags.delete_kwok_nodes:
        delete_kwok_nodes()
        return

    if flags.delete_k3d:
        delete_cluster(k3d_cfg)
        return

    if flags.create_k3d:
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
            prepare_charts = operator_dir / "hack/prepare-charts.sh"
            if prepare_charts.exists():
                sh.bash(str(prepare_charts))
                console.print("[green]✅ Charts prepared[/green]")

        if not create_cluster(k3d_cfg):
            raise RuntimeError(f"Cluster creation failed after {k3d_cfg.max_retries} attempts")
        wait_for_nodes()

        if flags.prepull_images:
            prepull_images(DEPENDENCIES['kai_scheduler']['images'],
                           k3d_cfg.registry_port, DEPENDENCIES['kai_scheduler']['version'])
            prepull_images(DEPENDENCIES['cert_manager']['images'],
                           k3d_cfg.registry_port, DEPENDENCIES['cert_manager']['version'])
            busybox_images = DEPENDENCIES.get('test_images', {}).get('busybox')
            if busybox_images:
                prepull_images(busybox_images, k3d_cfg.registry_port, 'latest')

    if flags.install_kai:
        install_kai_scheduler(comp_cfg)

    if flags.install_grove:
        deploy_grove_operator(k3d_cfg, comp_cfg, operator_dir, flags)

    if flags.install_kai:
        console.print("[yellow]ℹ️  Waiting for Kai Scheduler pods to be ready...[/yellow]")
        sh.kubectl("wait", "--for=condition=Ready", "pods", "--all",
                   "-n", "kai-scheduler", "--timeout=5m")
        console.print("[yellow]ℹ️  Creating default Kai queues (with retry for webhook readiness)...[/yellow]")
        try:
            _apply_kai_queues(operator_dir / "e2e/yaml/queues.yaml")
            console.print("[green]✅ Kai queues created successfully[/green]")
        except (RuntimeError, RetryError):
            raise RuntimeError("Failed to create Kai queues after retries")

    if flags.apply_topology:
        apply_topology_labels()

    if flags.create_k3d:
        console.print(Panel.fit("Configuring kubeconfig", style="bold blue"))
        default_kubeconfig_dir = Path.home() / ".kube"
        default_kubeconfig_dir.mkdir(parents=True, exist_ok=True)
        default_kubeconfig_path = default_kubeconfig_dir / "config"
        sh.k3d("kubeconfig", "merge", k3d_cfg.cluster_name, "-o", str(default_kubeconfig_path))
        default_kubeconfig_path.chmod(0o600)
        console.print(f"[green]  ✓ Merged to {default_kubeconfig_path}[/green]")

    if flags.create_kwok_nodes:
        kwok_version = DEPENDENCIES.get("kwok_controller", {}).get("version", "v0.7.0")
        install_kwok_controller(kwok_version)
        if flags.kwok_node_count > 0:
            create_nodes(flags.kwok_node_count, kwok_cfg)

    if flags.install_pyroscope:
        values_file = script_dir / "pyroscope-values.yaml"
        pyroscope_version = DEPENDENCIES.get("pyroscope", {}).get("version", "")
        install_pyroscope(kwok_cfg.pyroscope_ns, values_file, version=pyroscope_version)

    if flags.create_k3d:
        console.print(Panel.fit("Cluster setup complete!", style="bold green"))
        console.print("[yellow]To run E2E tests against this cluster:[/yellow]")
        console.print(f"\n  export E2E_REGISTRY_PORT={k3d_cfg.registry_port}")
        console.print("  make run-e2e")
        console.print("  make run-e2e TEST_PATTERN=Test_GS  # specific tests\n")
        console.print(f"[green]✅ Cluster '{k3d_cfg.cluster_name}' is ready for E2E testing![/green]")


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
    workers: Optional[int] = typer.Option(
        None, "--workers", help="k3d worker nodes (overrides E2E_WORKER_NODES)"),
    registry: Optional[str] = typer.Option(
        None, "--registry", help="Container registry URL"),
    # KWOK
    kwok_nodes: Optional[int] = typer.Option(
        None, "--kwok-nodes", help="Create N KWOK simulated nodes"),
    kwok_batch_size: Optional[int] = typer.Option(
        None, "--kwok-batch-size", help="Node creation batch size"),
    kwok_delete: bool = typer.Option(
        False, "--kwok-delete", help="Delete all KWOK simulated nodes"),
    # Pyroscope
    pyroscope: bool = typer.Option(
        False, "--pyroscope", help="Install Pyroscope via Helm"),
    pyroscope_namespace: Optional[str] = typer.Option(
        None, "--pyroscope-namespace", help="Pyroscope namespace (default: pyroscope)"),
    # Grove tuning
    grove_profiling: bool = typer.Option(
        False, "--grove-profiling", help="Enable pprof"),
    grove_pcs_syncs: Optional[int] = typer.Option(
        None, "--grove-pcs-syncs", help="PodCliqueSet concurrentSyncs"),
    grove_pclq_syncs: Optional[int] = typer.Option(
        None, "--grove-pclq-syncs", help="PodClique concurrentSyncs"),
    grove_pcsg_syncs: Optional[int] = typer.Option(
        None, "--grove-pcsg-syncs", help="PodCliqueScalingGroup concurrentSyncs"),
) -> None:
    """Unified cluster setup for Grove E2E testing.

    Default: creates k3d cluster + installs Kai + deploys Grove + applies topology + pre-pulls images.
    Use --skip-* flags to opt out of individual steps.
    """
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%H:%M:%S",
    )

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
