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

"""Kueue installation and management."""

from __future__ import annotations

from pathlib import Path

import sh
from rich.panel import Panel
from tenacity import retry, stop_after_attempt, wait_fixed

from infra_manager import console
from infra_manager.config import KueueConfig
from infra_manager.constants import (
    HELM_RELEASE_KUEUE,
    KUEUE_QUEUE_MAX_RETRIES,
    KUEUE_QUEUE_POLL_INTERVAL_SECONDS,
    NS_KUEUE,
    dep_value,
)


def install_kueue(cfg: KueueConfig, values_file: Path | None = None) -> None:
    """Install Kueue using Helm.

    Args:
        cfg: Kueue configuration with the version.
        values_file: Optional path to a Helm values override file. Callers should pass
            kueue-values.yaml, which restricts Kueue's job-framework integrations to "pod"
            only; otherwise Kueue's default "batch/job" integration installs a mutating
            webhook for all batch/v1 Jobs cluster-wide, which can reject unrelated
            installers' internal Jobs (e.g. Kai Scheduler's crd-upgrader hook) if it runs
            before Kueue's webhook pods are ready.
    """
    console.print(Panel.fit("Installing Kueue", style="bold blue"))
    console.print(f"[yellow]Version: {cfg.version}[/yellow]")
    helm_chart = dep_value("kueue", "helm_chart")
    try:
        sh.helm("uninstall", HELM_RELEASE_KUEUE, "-n", NS_KUEUE)
        console.print("[yellow]   Removed existing Kueue release[/yellow]")
    except sh.ErrorReturnCode_1:
        console.print("[yellow]   No existing Kueue release found[/yellow]")
    helm_args = [
        "install",
        HELM_RELEASE_KUEUE,
        helm_chart,
        "--version",
        cfg.version,
        "--namespace",
        NS_KUEUE,
        "--create-namespace",
    ]
    if values_file and values_file.exists():
        helm_args += ["-f", str(values_file)]
    sh.helm(*helm_args)
    console.print("[green]✅ Kueue installed[/green]")


@retry(
    stop=stop_after_attempt(KUEUE_QUEUE_MAX_RETRIES),
    wait=wait_fixed(KUEUE_QUEUE_POLL_INTERVAL_SECONDS),
    reraise=True,
)
def apply_kueue_queues(queues_file: Path) -> None:
    """Apply Kueue ResourceFlavor/ClusterQueue/LocalQueue CRs with retry for webhook readiness.

    Args:
        queues_file: Path to the Kueue queues YAML manifest.

    Raises:
        RuntimeError: If the Kueue validating webhook is not ready.
    """
    try:
        sh.kubectl("apply", "-f", str(queues_file))
    except sh.ErrorReturnCode as err:
        raise RuntimeError("Kueue webhook not ready") from err


def uninstall_kueue() -> None:
    """Uninstall Kueue via Helm."""
    console.print(Panel.fit("Uninstalling Kueue", style="bold blue"))
    try:
        sh.helm("uninstall", HELM_RELEASE_KUEUE, "-n", NS_KUEUE)
        console.print("[green]✅ Kueue uninstalled[/green]")
    except sh.ErrorReturnCode_1:
        console.print("[yellow]No existing Kueue release found[/yellow]")
