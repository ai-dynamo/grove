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

"""Pyroscope installation and management."""

from __future__ import annotations

from pathlib import Path

import sh
from rich.panel import Panel

from infra_manager import console
from infra_manager.constants import (
    HELM_CHART_PYROSCOPE,
    HELM_RELEASE_PYROSCOPE,
    HELM_REPO_GRAFANA,
    HELM_REPO_GRAFANA_URL,
)
from infra_manager.utils import require_command, run_kubectl


def install_pyroscope(namespace: str, values_file: Path | None = None, version: str = "") -> None:
    """Install Pyroscope via Helm.

    Args:
        namespace: Kubernetes namespace for Pyroscope installation.
        values_file: Optional path to a Helm values override file.
        version: Helm chart version to install, or empty string for latest.

    Raises:
        RuntimeError: If namespace creation fails.
    """
    console.print(Panel.fit(f"Installing Pyroscope (namespace: {namespace})", style="bold blue"))
    require_command("helm")

    sh.helm("repo", "add", HELM_REPO_GRAFANA, HELM_REPO_GRAFANA_URL, "--force-update")
    sh.helm("repo", "update", HELM_REPO_GRAFANA)

    ok, _, stderr = run_kubectl(["create", "namespace", namespace])
    if not ok and "AlreadyExists" not in stderr:
        raise RuntimeError(f"Failed to create namespace {namespace}: {stderr}")

    helm_args = ["upgrade", "--install", HELM_RELEASE_PYROSCOPE, HELM_CHART_PYROSCOPE, "-n", namespace]
    if version:
        helm_args += ["--version", version]
    if values_file and values_file.exists():
        helm_args += ["-f", str(values_file)]
    sh.helm(*helm_args)
    console.print("[green]\u2705 Pyroscope installed[/green]")
