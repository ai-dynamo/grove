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

"""Orchestration functions that compose domain modules into workflows."""

from __future__ import annotations

import threading
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

import sh
from rich.panel import Panel
from tenacity import RetryError

from infra_manager import console
from infra_manager.cluster import (
    apply_topology_labels,
    create_cluster,
    prepull_image_groups,
    wait_for_nodes,
)
from infra_manager.grove import deploy_grove_operator
from infra_manager.kai import apply_kai_queues, install_kai_scheduler
from infra_manager.pyroscope import install_pyroscope
from infra_manager.config import (
    ClusterConfig,
    SetupConfig,
)
from infra_manager.constants import (
    DEFAULT_KWOK_VERSION,
    DEPENDENCIES,
    NS_KAI_SCHEDULER,
    OPERATOR_DIR,
    REL_QUEUES_YAML,
    SCRIPT_DIR,
    dep_value,
)
from infra_manager.kwok import create_nodes, install_kwok_controller
from infra_manager.utils import require_command


def _check_prerequisites(install_kai: bool, install_grove: bool, grove_mode: str, operator_dir: Path) -> None:
    """Check CLI tools and prepare Helm charts.

    Args:
        install_kai: Whether Kai Scheduler will be installed.
        install_grove: Whether Grove operator will be deployed.
        grove_mode: Grove deployment mode ("local" or "image"); skaffold/jq only required for "local".
        operator_dir: Root directory of the Grove operator source tree.
    """
    prereqs = ["k3d", "kubectl", "docker"]
    if install_kai or install_grove:
        prereqs.append("helm")
    if install_grove and grove_mode == "local":
        prereqs.extend(["skaffold", "jq"])
    console.print(Panel.fit("Checking prerequisites", style="bold blue"))
    for cmd in prereqs:
        require_command(cmd)
    console.print("[green]\u2705 All required tools are available[/green]")



def _run_cluster_creation(cfg: ClusterConfig) -> None:
    """Create k3d cluster and wait for nodes.

    Args:
        cfg: Cluster configuration including retry count.

    Raises:
        RetryError: If the cluster cannot be created after all retries.
    """
    create_cluster(cfg)
    wait_for_nodes(cfg)


def _run_parallel(tasks: dict[str, Callable[[], None]]) -> None:
    """Run tasks in parallel, printing each task's output as a clean block.

    Args:
        tasks: Mapping of task name to callable.

    Raises:
        Exception: Re-raises the first exception from any failed task.
    """
    if not tasks:
        return

    outputs: dict[str, str] = {}
    lock = threading.Lock()

    def _run_task(name: str, fn: Callable) -> None:
        with console.buffered() as buf:
            fn()
        with lock:
            outputs[name] = buf.getvalue()

    with ThreadPoolExecutor(max_workers=len(tasks)) as executor:
        futures = {executor.submit(_run_task, name, fn): name for name, fn in tasks.items()}
        for future in as_completed(futures):
            future.result()

    for name in tasks:
        if outputs.get(name):
            console.print(outputs[name], end="")


def _run_prepull(registry_port: int) -> None:
    """Pre-pull images to local registry in a single batch.

    Args:
        registry_port: Port for the local container registry.
    """
    groups: list[tuple[list[str], str]] = [
        (DEPENDENCIES["kai_scheduler"]["images"], DEPENDENCIES["kai_scheduler"]["version"]),
        (DEPENDENCIES["cert_manager"]["images"], DEPENDENCIES["cert_manager"]["version"]),
    ]
    busybox_images = dep_value("test_images", "busybox")
    if busybox_images:
        groups.append((busybox_images, "latest"))
    prepull_image_groups(groups, registry_port)


def _run_kai_post_install(operator_dir: Path) -> None:
    """Wait for Kai pods and create queues.

    Args:
        operator_dir: Root directory of the Grove operator source tree.

    Raises:
        RuntimeError: If Kai queue creation fails after retries.
    """
    console.print("[yellow]\u2139\ufe0f  Waiting for Kai Scheduler pods to be ready...[/yellow]")
    sh.kubectl("wait", "--for=condition=Ready", "pods", "--all", "-n", NS_KAI_SCHEDULER, "--timeout=5m")
    console.print("[yellow]\u2139\ufe0f  Creating default Kai queues (with retry for webhook readiness)...[/yellow]")
    try:
        apply_kai_queues(operator_dir / REL_QUEUES_YAML)
        console.print("[green]\u2705 Kai queues created successfully[/green]")
    except (RuntimeError, RetryError) as err:
        raise RuntimeError("Failed to create Kai queues after retries") from err


def _run_kubeconfig_merge(cluster_name: str) -> None:
    """Merge k3d kubeconfig into the default kubeconfig file.

    Args:
        cluster_name: Name of the k3d cluster to merge kubeconfig for.
    """
    console.print(Panel.fit("Configuring kubeconfig", style="bold blue"))
    default_kubeconfig_dir = Path.home() / ".kube"
    default_kubeconfig_dir.mkdir(parents=True, exist_ok=True)
    default_kubeconfig_path = default_kubeconfig_dir / "config"
    sh.k3d("kubeconfig", "merge", cluster_name, "-o", str(default_kubeconfig_path))
    default_kubeconfig_path.chmod(0o600)
    console.print(f"[green]  \u2713 Merged to {default_kubeconfig_path}[/green]")


def run_setup(cfg: SetupConfig) -> None:
    """Run the unified setup workflow driven by a SetupConfig.

    Topology labels are applied only when cfg.cluster.create is True.
    KWOK activates when cfg.kwok.nodes > 0. Pyroscope activates when
    cfg.pyroscope.enabled is True.

    Note: prepull_images is skipped when create_cluster=False or an external
    registry is set -- the registry is assumed to already contain images. A
    warning is logged when this override occurs.

    Args:
        cfg: Fully resolved setup configuration (YAML + env vars + CLI overrides).

    Raises:
        RuntimeError: If any required step fails.
    """
    use_kwok = cfg.kwok.nodes > 0
    do_prepull = cfg.cluster.create and cfg.cluster.prepull_images and cfg.cluster.registry is None

    if cfg.cluster.prepull_images and not do_prepull:
        console.print("[yellow]⚠  prepull_images=true but skipping prepull (cluster not being created or external registry set)[/yellow]")

    operator_dir = OPERATOR_DIR

    # Phase 1: Prerequisites + cluster creation
    _check_prerequisites(cfg.scheduler.kai.enabled, cfg.grove.enabled, cfg.grove.mode, operator_dir)
    if cfg.cluster.create:
        _run_cluster_creation(cfg.cluster)

    # Phase 2: All opted-in components in parallel
    parallel_tasks: dict[str, Callable[[], None]] = {}
    if cfg.cluster.create:
        parallel_tasks["topology"] = apply_topology_labels
    if do_prepull:
        parallel_tasks["prepull"] = lambda: _run_prepull(cfg.cluster.registry_port)
    if cfg.scheduler.kai.enabled:
        parallel_tasks["kai"] = lambda: install_kai_scheduler(cfg.scheduler.kai)
    if cfg.grove.enabled:
        parallel_tasks["grove"] = lambda: deploy_grove_operator(cfg.grove, cfg.cluster, operator_dir)
    if cfg.pyroscope.enabled:
        values_file = SCRIPT_DIR / "infra_manager" / "pyroscope-values.yaml"
        pyroscope_version = dep_value("pyroscope", "version", default="")
        if not pyroscope_version:
            raise RuntimeError("pyroscope version not found in dependencies.yaml")
        pyroscope_ns = cfg.pyroscope.namespace
        parallel_tasks["pyroscope"] = lambda: install_pyroscope(
            pyroscope_ns, values_file, version=pyroscope_version
        )
    if use_kwok:
        kwok_version = dep_value("kwok_controller", "version", default=DEFAULT_KWOK_VERSION)
        parallel_tasks["kwok"] = lambda: install_kwok_controller(kwok_version)
    _run_parallel(parallel_tasks)

    # Phase 3: Post-install
    if cfg.scheduler.kai.enabled:
        _run_kai_post_install(operator_dir)
    if use_kwok:
        create_nodes(cfg.kwok)
    if cfg.cluster.create:
        _run_kubeconfig_merge(cfg.cluster.name)
