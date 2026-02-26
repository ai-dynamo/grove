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
infra-manager.py - Unified cluster setup for Grove E2E testing.

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
    ./infra-manager.py

    # Full e2e but skip image pre-pulling
    ./infra-manager.py --skip-prepull

    # Deploy only grove on existing cluster
    ./infra-manager.py --skip-cluster-creation --skip-kai --skip-topology --skip-prepull

    # Delete cluster
    ./infra-manager.py --delete

    # Scale test on existing cluster
    ./infra-manager.py --skip-cluster-creation --skip-kai --skip-grove --skip-topology --skip-prepull --kwok-nodes 1000 --pyroscope

    # Delete all KWOK nodes
    ./infra-manager.py --skip-cluster-creation --skip-kai --skip-grove --skip-topology --skip-prepull --kwok-delete

For detailed usage information, run: ./infra-manager.py --help
"""

from __future__ import annotations

import logging
import sys
from pathlib import Path

import typer

from infra_manager import console
from infra_manager.config import display_config, resolve_config, validate_flags
from infra_manager.orchestrator import run
from infra_manager.utils import resolve_bool_flag

app = typer.Typer(help="Unified cluster setup for Grove E2E testing.")


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

    skip_cluster_creation = resolve_bool_flag("skip_cluster_creation", skip_cluster_creation)
    skip_kai = resolve_bool_flag("skip_kai", skip_kai)
    skip_grove = resolve_bool_flag("skip_grove", skip_grove)
    skip_topology = resolve_bool_flag("skip_topology", skip_topology)
    skip_prepull = resolve_bool_flag("skip_prepull", skip_prepull)
    delete = resolve_bool_flag("delete", delete)
    kwok_delete = resolve_bool_flag("kwok_delete", kwok_delete)
    pyroscope = resolve_bool_flag("pyroscope", pyroscope)
    grove_profiling = resolve_bool_flag("grove_profiling", grove_profiling)

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
    operator_dir = script_dir.parent

    try:
        run(flags, k3d_cfg, comp_cfg, kwok_cfg, operator_dir, script_dir)
    except Exception as e:
        console.print(f"[red]\u274c {e}[/red]")
        sys.exit(1)


if __name__ == "__main__":
    app()
