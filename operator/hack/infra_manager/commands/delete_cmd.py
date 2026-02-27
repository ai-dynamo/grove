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

"""Delete subcommands (k3d-cluster, kwok-nodes)."""

from __future__ import annotations

import typer

from infra_manager.cluster import delete_cluster
from infra_manager.config import K3dConfig
from infra_manager.kwok import delete_kwok_nodes

app = typer.Typer(help="Delete infrastructure resources.")


@app.command("k3d-cluster")
def k3d_cluster(
    cluster_name: str | None = typer.Option(None, "--cluster-name", help="k3d cluster name"),
) -> None:
    """Delete the k3d cluster."""
    k3d_cfg = K3dConfig()
    if cluster_name is not None:
        k3d_cfg = k3d_cfg.model_copy(update={"cluster_name": cluster_name})
    delete_cluster(k3d_cfg)


@app.command("kwok-nodes")
def kwok_nodes() -> None:
    """Delete all KWOK simulated nodes and uninstall controller."""
    delete_kwok_nodes()
