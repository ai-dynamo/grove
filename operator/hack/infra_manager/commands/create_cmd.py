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

"""Create subcommands (k3d-cluster, kwok-nodes)."""

from __future__ import annotations

import typer

from infra_manager.cluster import create_cluster, wait_for_nodes
from infra_manager.config import K3dConfig, KwokConfig
from infra_manager.constants import DEFAULT_KWOK_VERSION, dep_value
from infra_manager.kwok import create_nodes, install_kwok_controller

app = typer.Typer(help="Create infrastructure resources.")


@app.command("k3d-cluster")
def k3d_cluster(
    workers: int | None = typer.Option(None, "--workers", help="Number of worker nodes"),
    image: str | None = typer.Option(None, "--image", help="K3s Docker image"),
    cluster_name: str | None = typer.Option(None, "--cluster-name", help="k3d cluster name"),
    registry_port: int | None = typer.Option(None, "--registry-port", help="Local registry port"),
) -> None:
    """Create a k3d cluster and wait for nodes."""
    k3d_cfg = K3dConfig()
    overrides: dict = {}
    if workers is not None:
        overrides["worker_nodes"] = workers
    if image is not None:
        overrides["k3s_image"] = image
    if cluster_name is not None:
        overrides["cluster_name"] = cluster_name
    if registry_port is not None:
        overrides["registry_port"] = registry_port
    if overrides:
        k3d_cfg = k3d_cfg.model_copy(update=overrides)

    create_cluster(k3d_cfg)
    wait_for_nodes()


@app.command("kwok-nodes")
def kwok_nodes(
    nodes: int = typer.Option(100, "--nodes", help="Number of KWOK nodes to create"),
    batch_size: int | None = typer.Option(None, "--batch-size", help="Node creation batch size"),
) -> None:
    """Install KWOK controller and create simulated nodes."""
    kwok_cfg = KwokConfig()
    if batch_size is not None:
        kwok_cfg = kwok_cfg.model_copy(update={"kwok_batch_size": batch_size})

    kwok_version = dep_value("kwok_controller", "version", default=DEFAULT_KWOK_VERSION)
    install_kwok_controller(kwok_version)
    create_nodes(nodes, kwok_cfg)
