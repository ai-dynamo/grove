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

from pathlib import Path

import sh
from rich.panel import Panel
from tenacity import RetryError

from e2e_manager import console
from e2e_manager.cluster import (
    apply_topology_labels,
    create_cluster,
    delete_cluster,
    prepull_images,
    wait_for_nodes,
)
from e2e_manager.components import (
    _apply_kai_queues,
    deploy_grove_operator,
    install_kai_scheduler,
    install_pyroscope,
)
from e2e_manager.config import ActionFlags, ComponentConfig, K3dConfig, KwokConfig
from e2e_manager.constants import (
    DEPENDENCIES,
    REL_PREPARE_CHARTS,
    REL_QUEUES_YAML,
    dep_value,
)
from e2e_manager.kwok import create_nodes, delete_kwok_nodes, install_kwok_controller
from e2e_manager.utils import require_command


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
    console.print("[green]\u2705 All required tools are available[/green]")

    if flags.install_grove:
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
    from e2e_manager.constants import NS_KAI_SCHEDULER

    console.print("[yellow]\u2139\ufe0f  Waiting for Kai Scheduler pods to be ready...[/yellow]")
    sh.kubectl("wait", "--for=condition=Ready", "pods", "--all",
               "-n", NS_KAI_SCHEDULER, "--timeout=5m")
    console.print("[yellow]\u2139\ufe0f  Creating default Kai queues (with retry for webhook readiness)...[/yellow]")
    try:
        _apply_kai_queues(operator_dir / REL_QUEUES_YAML)
        console.print("[green]\u2705 Kai queues created successfully[/green]")
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
    console.print(f"[green]  \u2713 Merged to {default_kubeconfig_path}[/green]")


def _run_kwok(flags: ActionFlags, kwok_cfg: KwokConfig) -> None:
    """Install KWOK controller and create nodes.

    Args:
        flags: Resolved action flags with KWOK node count.
        kwok_cfg: KWOK configuration with batch size and node resource limits.
    """
    from e2e_manager.constants import DEFAULT_KWOK_VERSION

    if flags.kwok_node_count > 0:
        kwok_version = dep_value("kwok_controller", "version", default=DEFAULT_KWOK_VERSION)
        install_kwok_controller(kwok_version)
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


def run(
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
