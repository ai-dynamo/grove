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

"""Composite setup subcommands (e2e, scale, apply-topology, prepull-images)."""

from __future__ import annotations

import typer

from infra_manager.cluster import apply_topology_labels, prepull_images
from infra_manager.config import GroveInstallOptions, K3dConfig
from infra_manager.constants import (
    DEPENDENCIES,
    DEFAULT_SCALE_WORKER_MEMORY,
    DEFAULT_SCALE_WORKER_NODES,
    dep_value,
)
from infra_manager.orchestrator import run_e2e_setup, run_scale_setup

app = typer.Typer(help="Composite setup workflows.")


@app.command()
def e2e(
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
    workers: int | None = typer.Option(
        None, "--workers", help="k3d worker nodes (overrides E2E_WORKER_NODES)"),
    worker_memory: str | None = typer.Option(
        None, "--worker-memory", help="Memory per worker node"),
    registry: str | None = typer.Option(
        None, "--registry", help="Container registry URL"),
    grove_profiling: bool = typer.Option(
        False, "--grove-profiling", help="Enable pprof"),
    grove_pcs_syncs: int | None = typer.Option(
        None, "--grove-pcs-syncs", help="PodCliqueSet concurrentSyncs"),
    grove_pclq_syncs: int | None = typer.Option(
        None, "--grove-pclq-syncs", help="PodClique concurrentSyncs"),
    grove_pcsg_syncs: int | None = typer.Option(
        None, "--grove-pcsg-syncs", help="PodCliqueScalingGroup concurrentSyncs"),
) -> None:
    """Full E2E setup: k3d + kai + grove + topology + prepull.

    Use --skip-* flags to opt out of individual steps.
    """
    grove_options = GroveInstallOptions(
        registry=registry,
        grove_profiling=grove_profiling,
        grove_pcs_syncs=grove_pcs_syncs,
        grove_pclq_syncs=grove_pclq_syncs,
        grove_pcsg_syncs=grove_pcsg_syncs,
    )
    run_e2e_setup(
        skip_cluster_creation=skip_cluster_creation,
        skip_kai=skip_kai,
        skip_grove=skip_grove,
        skip_topology=skip_topology,
        skip_prepull=skip_prepull,
        workers=workers,
        worker_memory=worker_memory,
        grove_options=grove_options,
    )


@app.command()
def scale(
    workers: int = typer.Option(
        DEFAULT_SCALE_WORKER_NODES, "--workers", help="k3d worker nodes for scale testing"),
    worker_memory: str = typer.Option(
        DEFAULT_SCALE_WORKER_MEMORY, "--worker-memory", help="Memory per worker node"),
    registry: str | None = typer.Option(
        None, "--registry", help="Container registry URL"),
    kwok_nodes: int = typer.Option(
        100, "--kwok-nodes", help="Number of KWOK simulated nodes"),
    kwok_batch_size: int | None = typer.Option(
        None, "--kwok-batch-size", help="KWOK node creation batch size"),
    pcs_syncs: int | None = typer.Option(
        None, "--pcs-syncs", help="PodCliqueSet concurrentSyncs"),
    pclq_syncs: int | None = typer.Option(
        None, "--pclq-syncs", help="PodClique concurrentSyncs"),
    pcsg_syncs: int | None = typer.Option(
        None, "--pcsg-syncs", help="PodCliqueScalingGroup concurrentSyncs"),
    pyroscope_namespace: str | None = typer.Option(
        None, "--pyroscope-namespace", help="Pyroscope namespace"),
) -> None:
    """Full E2E + KWOK nodes + Pyroscope + pprof for scale testing."""
    grove_options = GroveInstallOptions(
        registry=registry,
        grove_profiling=True,
        grove_pcs_syncs=pcs_syncs,
        grove_pclq_syncs=pclq_syncs,
        grove_pcsg_syncs=pcsg_syncs,
    )
    run_scale_setup(
        workers=workers,
        worker_memory=worker_memory,
        grove_options=grove_options,
        kwok_nodes=kwok_nodes,
        kwok_batch_size=kwok_batch_size,
        pyroscope_namespace=pyroscope_namespace,
    )


@app.command("apply-topology")
def apply_topology() -> None:
    """Apply topology labels to worker nodes."""
    apply_topology_labels()


@app.command("prepull-images")
def prepull_images_cmd(
    registry_port: int | None = typer.Option(None, "--registry-port", help="Local registry port"),
) -> None:
    """Pre-pull images to local k3d registry."""
    k3d_cfg = K3dConfig()
    if registry_port is not None:
        k3d_cfg = k3d_cfg.model_copy(update={"registry_port": registry_port})

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
