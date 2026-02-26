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

"""Kai Scheduler, Grove operator, and Pyroscope installation."""

from __future__ import annotations

import json
import os
from datetime import datetime, timezone
from pathlib import Path

import sh
from rich.panel import Panel
from tenacity import RetryError, retry, stop_after_attempt, wait_fixed

from infra_manager import console
from infra_manager.config import ActionFlags, ComponentConfig, K3dConfig
from infra_manager.constants import (
    E2E_NODE_ROLE_KEY,
    E2E_TEST_COMMIT,
    E2E_TEST_TREE_STATE,
    E2E_TEST_VERSION,
    GROVE_INITC_IMAGE,
    GROVE_MODULE_PATH,
    GROVE_OPERATOR_IMAGE,
    HELM_CHART_PYROSCOPE,
    HELM_RELEASE_GROVE,
    HELM_RELEASE_KAI,
    HELM_RELEASE_PYROSCOPE,
    HELM_REPO_GRAFANA,
    HELM_REPO_GRAFANA_URL,
    KAI_QUEUE_MAX_RETRIES,
    KAI_QUEUE_POLL_INTERVAL_SECONDS,
    KAI_SCHEDULER_OCI,
    LABEL_CONTROL_PLANE,
    NS_DEFAULT,
    NS_KAI_SCHEDULER,
    REL_CHARTS_DIR,
    REL_WORKLOAD_YAML,
    WEBHOOK_READY_KEYWORDS,
    WEBHOOK_READY_MAX_RETRIES,
    WEBHOOK_READY_POLL_INTERVAL_SECONDS,
)
from infra_manager.utils import (
    collect_grove_helm_overrides,
    require_command,
    resolve_registry_repos,
    run_kubectl,
)


# ============================================================================
# Kai Scheduler
# ============================================================================

def install_kai_scheduler(comp_cfg: ComponentConfig) -> None:
    """Install Kai Scheduler using Helm.

    Args:
        comp_cfg: Component configuration with the Kai version.
    """
    console.print(Panel.fit("Installing Kai Scheduler", style="bold blue"))
    console.print(f"[yellow]Version: {comp_cfg.kai_version}[/yellow]")
    try:
        sh.helm("uninstall", HELM_RELEASE_KAI, "-n", NS_KAI_SCHEDULER)
        console.print("[yellow]   Removed existing Kai Scheduler release[/yellow]")
    except sh.ErrorReturnCode_1:
        console.print("[yellow]   No existing Kai Scheduler release found[/yellow]")
    sh.helm(
        "install", HELM_RELEASE_KAI,
        KAI_SCHEDULER_OCI,
        "--version", comp_cfg.kai_version,
        "--namespace", NS_KAI_SCHEDULER,
        "--create-namespace",
        "--set", f"global.tolerations[0].key={LABEL_CONTROL_PLANE}",
        "--set", "global.tolerations[0].operator=Exists",
        "--set", "global.tolerations[0].effect=NoSchedule",
        "--set", f"global.tolerations[1].key={E2E_NODE_ROLE_KEY}",
        "--set", "global.tolerations[1].operator=Equal",
        "--set", "global.tolerations[1].value=agent",
        "--set", "global.tolerations[1].effect=NoSchedule",
    )
    console.print("[green]\u2705 Kai Scheduler installed[/green]")


@retry(
    stop=stop_after_attempt(KAI_QUEUE_MAX_RETRIES),
    wait=wait_fixed(KAI_QUEUE_POLL_INTERVAL_SECONDS),
    reraise=True,
)
def _apply_kai_queues(queues_file: Path) -> None:
    """Apply Kai queue CRs with retry for webhook readiness.

    Args:
        queues_file: Path to the Kai queues YAML manifest.

    Raises:
        RuntimeError: If the Kai queue webhook is not ready.
    """
    try:
        sh.kubectl("apply", "-f", str(queues_file))
    except sh.ErrorReturnCode:
        raise RuntimeError("Kai queue webhook not ready")


# ============================================================================
# Grove operator
# ============================================================================

@retry(
    stop=stop_after_attempt(WEBHOOK_READY_MAX_RETRIES),
    wait=wait_fixed(WEBHOOK_READY_POLL_INTERVAL_SECONDS),
    reraise=True,
)
def _check_grove_webhook_ready(operator_dir: Path) -> None:
    """Dry-run kubectl create to verify the Grove webhook is responding.

    Args:
        operator_dir: Root directory of the Grove operator source tree.

    Raises:
        RuntimeError: If the webhook response does not contain expected keywords.
    """
    try:
        result = sh.kubectl(
            "create", "-f", str(operator_dir / REL_WORKLOAD_YAML),
            "--dry-run=server", "-n", NS_DEFAULT,
        )
        output = str(result).lower()
    except sh.ErrorReturnCode as e:
        output = (str(e.stdout) + str(e.stderr)).lower()
    if not any(kw in output for kw in WEBHOOK_READY_KEYWORDS):
        raise RuntimeError("Grove webhook not ready")


def _build_grove_images(comp_cfg: ComponentConfig, operator_dir: Path, push_repo: str) -> dict[str, str]:
    """Run skaffold build and return the built image map.

    Args:
        comp_cfg: Component configuration with the skaffold profile.
        operator_dir: Root directory of the Grove operator source tree.
        push_repo: Registry URL to push built images to.

    Returns:
        Dictionary mapping image names to their full tagged references.
    """
    console.print(f"[yellow]\u2139\ufe0f  Building images (push to {push_repo})...[/yellow]")
    build_output = json.loads(
        sh.skaffold(
            "build",
            "--default-repo", push_repo,
            "--profile", comp_cfg.skaffold_profile,
            "--quiet",
            "--output={{json .}}",
            _cwd=str(operator_dir)
        )
    )
    return {build["imageName"]: build["tag"] for build in build_output.get("builds", [])}


def _deploy_grove_charts(
    comp_cfg: ComponentConfig,
    operator_dir: Path,
    images: dict[str, str],
    pull_repo: str,
) -> None:
    """Run skaffold deploy with resolved image tags.

    Args:
        comp_cfg: Component configuration with skaffold profile and namespace.
        operator_dir: Root directory of the Grove operator source tree.
        images: Dictionary mapping image names to their full tagged references.
        pull_repo: Registry URL the cluster uses to pull images.
    """
    console.print("[yellow]Deploying with images:[/yellow]")
    for name, tag in images.items():
        console.print(f"  {name}={tag}")

    os.environ["CONTAINER_REGISTRY"] = pull_repo
    sh.skaffold(
        "deploy",
        "--profile", comp_cfg.skaffold_profile,
        "--namespace", comp_cfg.grove_namespace,
        "--status-check=false",
        "--default-repo=",
        "--images", f"{GROVE_OPERATOR_IMAGE}={images[GROVE_OPERATOR_IMAGE]}",
        "--images", f"{GROVE_INITC_IMAGE}={images[GROVE_INITC_IMAGE]}",
        _cwd=str(operator_dir)
    )
    console.print("[green]\u2705 Grove operator deployed[/green]")


def _apply_grove_helm_overrides(operator_dir: Path, helm_set_values: list[str], grove_namespace: str) -> None:
    """Apply helm value overrides via ``helm upgrade --reuse-values``.

    Args:
        operator_dir: Root directory of the Grove operator source tree.
        helm_set_values: List of ``key=value`` strings for ``--set`` arguments.
        grove_namespace: Kubernetes namespace where Grove is installed.
    """
    if not helm_set_values:
        return
    console.print("[yellow]\u2139\ufe0f  Applying Grove helm overrides...[/yellow]")
    set_args = [item for val in helm_set_values for item in ("--set", val)]
    chart_path = str(operator_dir / REL_CHARTS_DIR)
    sh.helm(
        "upgrade", HELM_RELEASE_GROVE, chart_path,
        "-n", grove_namespace,
        "--reuse-values",
        *set_args,
    )
    console.print("[green]\u2705 Grove helm overrides applied[/green]")


def _wait_grove_webhook(operator_dir: Path) -> None:
    """Wait for Grove webhook to become responsive.

    Args:
        operator_dir: Root directory of the Grove operator source tree.

    Raises:
        RuntimeError: If the webhook does not become ready within the timeout.
    """
    console.print("[yellow]\u2139\ufe0f  Waiting for Grove webhook to be ready...[/yellow]")
    try:
        _check_grove_webhook_ready(operator_dir)
        console.print("[green]\u2705 Grove webhook is ready[/green]")
    except (RuntimeError, RetryError):
        raise RuntimeError("Timed out waiting for Grove webhook")


def deploy_grove_operator(
    k3d_cfg: K3dConfig,
    comp_cfg: ComponentConfig,
    operator_dir: Path,
    flags: ActionFlags,
) -> None:
    """Deploy Grove operator using Skaffold.

    Builds images, deploys charts, applies helm overrides, and waits for
    the webhook to become responsive.

    Args:
        k3d_cfg: k3d cluster configuration for registry port resolution.
        comp_cfg: Component configuration with skaffold profile and namespace.
        operator_dir: Root directory of the Grove operator source tree.
        flags: Resolved action flags containing registry and tuning overrides.
    """
    console.print(Panel.fit("Deploying Grove operator", style="bold blue"))
    try:
        sh.helm("uninstall", HELM_RELEASE_GROVE, "-n", comp_cfg.grove_namespace)
        console.print("[yellow]   Removed existing Grove operator release[/yellow]")
    except sh.ErrorReturnCode_1:
        console.print("[yellow]   No existing Grove operator release found[/yellow]")

    build_date = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    os.environ.update({
        "VERSION": E2E_TEST_VERSION,
        "LD_FLAGS": (
            f"-X {GROVE_MODULE_PATH}.gitCommit={E2E_TEST_COMMIT} "
            f"-X {GROVE_MODULE_PATH}.gitTreeState={E2E_TEST_TREE_STATE} "
            f"-X {GROVE_MODULE_PATH}.buildDate={build_date} "
            f"-X {GROVE_MODULE_PATH}.gitVersion={E2E_TEST_VERSION}"
        )
    })

    push_repo, pull_repo = resolve_registry_repos(flags.registry, k3d_cfg.registry_port)

    raw_images = _build_grove_images(comp_cfg, operator_dir, push_repo)
    images = {name: tag.replace(push_repo, pull_repo) for name, tag in raw_images.items()}

    _deploy_grove_charts(comp_cfg, operator_dir, images, pull_repo)

    helm_overrides = collect_grove_helm_overrides(flags)
    _apply_grove_helm_overrides(operator_dir, helm_overrides, comp_cfg.grove_namespace)

    console.print("[yellow]\u2139\ufe0f  Waiting for Grove deployment rollout...[/yellow]")
    sh.kubectl("rollout", "status", "deployment", "-n", comp_cfg.grove_namespace, "--timeout=5m")

    _wait_grove_webhook(operator_dir)


# ============================================================================
# Pyroscope
# ============================================================================

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
