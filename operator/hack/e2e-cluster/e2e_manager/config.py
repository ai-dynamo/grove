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

"""Configuration classes, ActionFlags, and config resolution/display."""

from __future__ import annotations

from dataclasses import dataclass

import typer
from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict
from rich.panel import Panel

from e2e_manager import console, logger
from e2e_manager.constants import (
    DEFAULT_API_PORT,
    DEFAULT_CLUSTER_CREATE_MAX_RETRIES,
    DEFAULT_CLUSTER_NAME,
    DEFAULT_GROVE_NAMESPACE,
    DEFAULT_K3S_IMAGE,
    DEFAULT_KWOK_BATCH_SIZE,
    DEFAULT_KWOK_MAX_PODS,
    DEFAULT_KWOK_NODE_CPU,
    DEFAULT_KWOK_NODE_MEMORY,
    DEFAULT_LB_PORT,
    DEFAULT_PYROSCOPE_NAMESPACE,
    DEFAULT_REGISTRY_PORT,
    DEFAULT_SKAFFOLD_PROFILE,
    DEFAULT_WORKER_MEMORY,
    DEFAULT_WORKER_NODES,
    DEPENDENCIES,
)


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
