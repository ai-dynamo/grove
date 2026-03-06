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
from infra_manager.components import (
    apply_kai_queues,
    deploy_grove_operator,
    install_kai_scheduler,
    install_pyroscope,
)
from infra_manager.config import (
    ComponentConfig,
    GroveInstallOptions,
    K3dConfig,
    KwokConfig,
)
from infra_manager.constants import (
    DEPENDENCIES,
    OPERATOR_DIR,
    REL_PREPARE_CHARTS,
    REL_QUEUES_YAML,
    SCRIPT_DIR,
    dep_value,
)
from infra_manager.kwok import create_nodes, install_kwok_controller
from infra_manager.utils import require_command

# ============================================================================
# Internal helpers
# ============================================================================


def _check_prerequisites(install_kai: bool, install_grove: bool, operator_dir: Path) -> None:
    """Check CLI tools and prepare Helm charts.

    Args:
        install_kai: Whether Kai Scheduler will be installed.
        install_grove: Whether Grove operator will be deployed.
        operator_dir: Root directory of the Grove operator source tree.
    """
    prereqs = ["k3d", "kubectl", "docker"]
    if install_kai or install_grove:
        prereqs.append("helm")
    if install_grove:
        prereqs.extend(["skaffold", "jq"])
    console.print(Panel.fit("Checking prerequisites", style="bold blue"))
    for cmd in prereqs:
        require_command(cmd)
    console.print("[green]\u2705 All required tools are available[/green]")

    if install_grove:
        console.print(Panel.fit("Preparing Helm charts", style="bold blue"))
        prepare_charts = operator_dir / REL_PREPARE_CHARTS
        if prepare_charts.exists():
            sh.bash(str(prepare_charts))
            console.print("[green]\u2705 Charts prepared[/green]")


def _run_cluster_creation(k3d_cfg: K3dConfig) -> None:
    """Create k3d cluster and wait for nodes.

    Args:
        k3d_cfg: k3d cluster configuration including retry count.

    Raises:
        RetryError: If the cluster cannot be created after all retries.
    """
    create_cluster(k3d_cfg)
    wait_for_nodes()


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


def _run_prepull(k3d_cfg: K3dConfig) -> None:
    """Pre-pull images to local registry in a single batch.

    Args:
        k3d_cfg: k3d cluster configuration with the registry port.
    """
    groups: list[tuple[list[str], str]] = [
        (DEPENDENCIES["kai_scheduler"]["images"], DEPENDENCIES["kai_scheduler"]["version"]),
        (DEPENDENCIES["cert_manager"]["images"], DEPENDENCIES["cert_manager"]["version"]),
    ]
    busybox_images = dep_value("test_images", "busybox")
    if busybox_images:
        groups.append((busybox_images, "latest"))
    prepull_image_groups(groups, k3d_cfg.registry_port)


def _run_kai_post_install(operator_dir: Path) -> None:
    """Wait for Kai pods and create queues.

    Args:
        operator_dir: Root directory of the Grove operator source tree.

    Raises:
        RuntimeError: If Kai queue creation fails after retries.
    """
    from infra_manager.constants import NS_KAI_SCHEDULER

    console.print("[yellow]\u2139\ufe0f  Waiting for Kai Scheduler pods to be ready...[/yellow]")
    sh.kubectl("wait", "--for=condition=Ready", "pods", "--all", "-n", NS_KAI_SCHEDULER, "--timeout=5m")
    console.print("[yellow]\u2139\ufe0f  Creating default Kai queues (with retry for webhook readiness)...[/yellow]")
    try:
        apply_kai_queues(operator_dir / REL_QUEUES_YAML)
        console.print("[green]\u2705 Kai queues created successfully[/green]")
    except (RuntimeError, RetryError) as err:
        raise RuntimeError("Failed to create Kai queues after retries") from err


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
    console.print(f"[green]  \u2713 Merged to {default_kubeconfig_path}[/green]")


# ============================================================================
# New public API
# ============================================================================


def run_e2e_setup(
    *,
    skip_cluster_creation: bool = False,
    skip_kai: bool = False,
    skip_grove: bool = False,
    skip_topology: bool = False,
    skip_prepull: bool = False,
    workers: int | None = None,
    worker_memory: str | None = None,
    grove_options: GroveInstallOptions | None = None,
) -> None:
    """Run the E2E setup workflow: k3d + kai + grove + topology + prepull.

    Args:
        skip_cluster_creation: Whether to skip k3d cluster creation.
        skip_kai: Whether to skip Kai Scheduler installation.
        skip_grove: Whether to skip Grove operator deployment.
        skip_topology: Whether to skip topology label application.
        skip_prepull: Whether to skip image pre-pulling.
        workers: CLI override for worker node count, or None.
        worker_memory: CLI override for worker memory, or None.
        grove_options: Grove install options, or None for defaults.

    Raises:
        RuntimeError: If any step fails.
    """
    if grove_options is None:
        grove_options = GroveInstallOptions()

    create_k3d = not skip_cluster_creation
    install_kai = not skip_kai
    install_grove = not skip_grove
    apply_topo = not skip_topology
    do_prepull = not skip_prepull and create_k3d

    operator_dir = OPERATOR_DIR
    k3d_cfg = K3dConfig()
    comp_cfg = ComponentConfig()

    overrides: dict = {}
    if workers is not None:
        overrides["worker_nodes"] = workers
    if worker_memory is not None:
        overrides["worker_memory"] = worker_memory
    if overrides:
        k3d_cfg = k3d_cfg.model_copy(update=overrides)
    if grove_options.registry is not None:
        comp_cfg = comp_cfg.model_copy(update={"registry": grove_options.registry})

    _check_prerequisites(install_kai, install_grove, operator_dir)
    if create_k3d:
        _run_cluster_creation(k3d_cfg)

    # Phase 2: Parallel — prepull, topology, kai, grove all at once
    parallel_tasks: dict[str, Callable[[], None]] = {}
    if do_prepull:
        parallel_tasks["prepull"] = lambda: _run_prepull(k3d_cfg)
    if apply_topo:
        parallel_tasks["topology"] = apply_topology_labels
    if install_kai:
        parallel_tasks["kai"] = lambda: install_kai_scheduler(comp_cfg)
    if install_grove:
        parallel_tasks["grove"] = lambda: deploy_grove_operator(k3d_cfg, comp_cfg, operator_dir, grove_options)
    _run_parallel(parallel_tasks)

    # Phase 3: Sequential post-install (depends on kai)
    if install_kai:
        _run_kai_post_install(operator_dir)

    if create_k3d:
        _run_kubeconfig_merge(k3d_cfg)


def run_scale_setup(
    *,
    workers: int | None = None,
    worker_memory: str | None = None,
    grove_options: GroveInstallOptions | None = None,
    kwok_nodes: int = 100,
    kwok_batch_size: int | None = None,
    pyroscope_namespace: str | None = None,
) -> None:
    """Run full E2E setup + KWOK nodes + Pyroscope for scale testing.

    Args:
        workers: CLI override for worker node count, or None.
        worker_memory: CLI override for worker memory, or None.
        grove_options: Grove install options (pprof enabled by default).
        kwok_nodes: Number of KWOK simulated nodes to create.
        kwok_batch_size: KWOK node creation batch size override, or None.
        pyroscope_namespace: Pyroscope namespace override, or None.

    Raises:
        RuntimeError: If any step fails.
    """
    from infra_manager.constants import DEFAULT_KWOK_VERSION

    if grove_options is None:
        grove_options = GroveInstallOptions(grove_profiling=True)

    operator_dir = OPERATOR_DIR
    k3d_cfg = K3dConfig()
    comp_cfg = ComponentConfig()

    overrides: dict = {}
    if workers is not None:
        overrides["worker_nodes"] = workers
    if worker_memory is not None:
        overrides["worker_memory"] = worker_memory
    if overrides:
        k3d_cfg = k3d_cfg.model_copy(update=overrides)
    if grove_options.registry is not None:
        comp_cfg = comp_cfg.model_copy(update={"registry": grove_options.registry})

    kwok_cfg = KwokConfig()
    if kwok_batch_size is not None:
        kwok_cfg = kwok_cfg.model_copy(update={"kwok_batch_size": kwok_batch_size})
    if pyroscope_namespace is not None:
        kwok_cfg = kwok_cfg.model_copy(update={"pyroscope_ns": pyroscope_namespace})

    # Phase 1: Prerequisites + cluster creation
    _check_prerequisites(True, True, operator_dir)
    _run_cluster_creation(k3d_cfg)

    # Phase 2: ALL components + prepull + topology in parallel
    kwok_version = dep_value("kwok_controller", "version", default=DEFAULT_KWOK_VERSION)
    values_file = SCRIPT_DIR / "infra_manager" / "pyroscope-values.yaml"
    pyroscope_version = dep_value("pyroscope", "version", default="")

    parallel_tasks: dict[str, Callable[[], None]] = {
        "prepull": lambda: _run_prepull(k3d_cfg),
        "topology": apply_topology_labels,
        "kai": lambda: install_kai_scheduler(comp_cfg),
        "grove": lambda: deploy_grove_operator(k3d_cfg, comp_cfg, operator_dir, grove_options),
        "kwok": lambda: install_kwok_controller(kwok_version),
        "pyroscope": lambda: install_pyroscope(kwok_cfg.pyroscope_ns, values_file, version=pyroscope_version),
    }
    _run_parallel(parallel_tasks)

    # Phase 3: Post-install steps
    _run_kai_post_install(operator_dir)
    if kwok_nodes > 0:
        create_nodes(kwok_nodes, kwok_cfg)
    _run_kubeconfig_merge(k3d_cfg)
