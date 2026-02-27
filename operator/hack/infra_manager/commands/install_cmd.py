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

"""Install subcommands (kai, grove, pyroscope)."""

from __future__ import annotations

import sh
import typer
from rich.panel import Panel

from infra_manager import console
from infra_manager.components import (
    deploy_grove_operator,
    install_kai_scheduler,
    install_pyroscope,
)
from infra_manager.config import ComponentConfig, GroveInstallOptions, K3dConfig
from infra_manager.constants import (
    DEFAULT_PYROSCOPE_NAMESPACE,
    OPERATOR_DIR,
    REL_PREPARE_CHARTS,
    SCRIPT_DIR,
    dep_value,
)
from infra_manager.utils import require_command

app = typer.Typer(help="Install components.")


@app.command()
def kai(
    version: str | None = typer.Option(None, "--version", help="Kai Scheduler Helm chart version"),
) -> None:
    """Install Kai Scheduler via Helm."""
    comp_cfg = ComponentConfig()
    if version is not None:
        comp_cfg = comp_cfg.model_copy(update={"kai_version": version})
    install_kai_scheduler(comp_cfg)


@app.command()
def grove(
    registry: str | None = typer.Option(None, "--registry", help="Container registry URL"),
    profiling: bool = typer.Option(False, "--profiling", help="Enable pprof"),
    pcs_syncs: int | None = typer.Option(None, "--pcs-syncs", help="PodCliqueSet concurrentSyncs"),
    pclq_syncs: int | None = typer.Option(None, "--pclq-syncs", help="PodClique concurrentSyncs"),
    pcsg_syncs: int | None = typer.Option(None, "--pcsg-syncs", help="PodCliqueScalingGroup concurrentSyncs"),
) -> None:
    """Build and deploy Grove operator via Skaffold."""
    for cmd in ("skaffold", "jq", "helm"):
        require_command(cmd)

    console.print(Panel.fit("Preparing Helm charts", style="bold blue"))
    prepare_charts = OPERATOR_DIR / REL_PREPARE_CHARTS
    if prepare_charts.exists():
        sh.bash(str(prepare_charts))
        console.print("[green]\u2705 Charts prepared[/green]")

    k3d_cfg = K3dConfig()
    comp_cfg = ComponentConfig()
    if registry is not None:
        comp_cfg = comp_cfg.model_copy(update={"registry": registry})

    options = GroveInstallOptions(
        registry=comp_cfg.registry,
        grove_profiling=profiling,
        grove_pcs_syncs=pcs_syncs,
        grove_pclq_syncs=pclq_syncs,
        grove_pcsg_syncs=pcsg_syncs,
    )
    deploy_grove_operator(k3d_cfg, comp_cfg, OPERATOR_DIR, options)


@app.command()
def pyroscope(
    namespace: str = typer.Option(DEFAULT_PYROSCOPE_NAMESPACE, "--namespace", help="Pyroscope namespace"),
) -> None:
    """Install Pyroscope via Helm."""
    values_file = SCRIPT_DIR / "infra_manager" / "pyroscope-values.yaml"
    pyroscope_version = dep_value("pyroscope", "version", default="")
    install_pyroscope(namespace, values_file, version=pyroscope_version)
