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

"""Grove operator deployment and management."""

from __future__ import annotations

import json
import os
from datetime import datetime, timezone
from pathlib import Path

import sh
from rich.panel import Panel
from tenacity import RetryError, retry, stop_after_attempt, wait_fixed

from infra_manager import console
from infra_manager.config import ClusterConfig, GroveConfig
from infra_manager.constants import (
    DEFAULT_GROVE_NAMESPACE,
    E2E_TEST_COMMIT,
    E2E_TEST_TREE_STATE,
    E2E_TEST_VERSION,
    GROVE_INITC_IMAGE,
    GROVE_MODULE_PATH,
    GROVE_OPERATOR_IMAGE,
    HELM_RELEASE_GROVE,
    NS_DEFAULT,
    REL_CHARTS_DIR,
    REL_WORKLOAD_YAML,
    WEBHOOK_READY_KEYWORDS,
    WEBHOOK_READY_MAX_RETRIES,
    WEBHOOK_READY_POLL_INTERVAL_SECONDS,
)
from infra_manager.utils import collect_grove_helm_overrides, resolve_registry_repos


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
            "create",
            "-f",
            str(operator_dir / REL_WORKLOAD_YAML),
            "--dry-run=server",
            "-n",
            NS_DEFAULT,
        )
        output = str(result).lower()
    except sh.ErrorReturnCode as e:
        stdout = e.stdout.decode("utf-8", errors="replace") if isinstance(e.stdout, bytes) else str(e.stdout or "")
        stderr = e.stderr.decode("utf-8", errors="replace") if isinstance(e.stderr, bytes) else str(e.stderr or "")
        output = (stdout + stderr).lower()
    if not any(kw in output for kw in WEBHOOK_READY_KEYWORDS):
        raise RuntimeError("Grove webhook not ready")


def _build_grove_images(skaffold_profile: str, operator_dir: Path, push_repo: str) -> dict[str, str]:
    """Run skaffold build and return the built image map.

    Args:
        skaffold_profile: Skaffold profile to use for the build.
        operator_dir: Root directory of the Grove operator source tree.
        push_repo: Registry URL to push built images to.

    Returns:
        Dictionary mapping image names to their full tagged references.
    """
    console.print(f"[yellow]\u2139\ufe0f  Building images (push to {push_repo})...[/yellow]")
    build_output = json.loads(
        sh.skaffold(
            "build",
            "--default-repo",
            push_repo,
            "--profile",
            skaffold_profile,
            "--quiet",
            "--output={{json .}}",
            _cwd=str(operator_dir),
        )
    )
    return {build["imageName"]: build["tag"] for build in build_output.get("builds", [])}


def _deploy_grove_charts(
    skaffold_profile: str,
    namespace: str,
    operator_dir: Path,
    images: dict[str, str],
    pull_repo: str,
) -> None:
    """Run skaffold deploy with resolved image tags.

    Args:
        skaffold_profile: Skaffold profile to use for deployment.
        namespace: Kubernetes namespace to deploy Grove into.
        operator_dir: Root directory of the Grove operator source tree.
        images: Dictionary mapping image names to their full tagged references.
        pull_repo: Registry URL the cluster uses to pull images.
    """
    console.print("[yellow]Deploying with images:[/yellow]")
    for name, tag in images.items():
        console.print(f"  {name}={tag}")

    sh.skaffold(
        "deploy",
        "--profile",
        skaffold_profile,
        "--namespace",
        namespace,
        "--status-check=false",
        "--default-repo=",
        "--images",
        f"{GROVE_OPERATOR_IMAGE}={images[GROVE_OPERATOR_IMAGE]}",
        "--images",
        f"{GROVE_INITC_IMAGE}={images[GROVE_INITC_IMAGE]}",
        _cwd=str(operator_dir),
        _env={**os.environ, "CONTAINER_REGISTRY": pull_repo},
    )
    console.print("[green]\u2705 Grove operator deployed[/green]")


def _apply_grove_helm_overrides(
    operator_dir: Path,
    helm_overrides: list[tuple[str, str]],
    grove_namespace: str,
) -> None:
    """Apply helm value overrides via ``helm upgrade --reuse-values``.

    Args:
        operator_dir: Root directory of the Grove operator source tree.
        helm_overrides: List of ``(helm_flag, "key=value")`` tuples, where
            ``helm_flag`` is ``"--set"`` or ``"--set-string"``.
        grove_namespace: Kubernetes namespace where Grove is installed.
    """
    if not helm_overrides:
        return
    console.print("[yellow]\u2139\ufe0f  Applying Grove helm overrides...[/yellow]")
    set_args = [item for flag, val in helm_overrides for item in (flag, val)]
    chart_path = str(operator_dir / REL_CHARTS_DIR)
    sh.helm(
        "upgrade",
        HELM_RELEASE_GROVE,
        chart_path,
        "-n",
        grove_namespace,
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
    except (RuntimeError, RetryError) as err:
        raise RuntimeError("Timed out waiting for Grove webhook") from err


def deploy_grove_operator(
    grove_cfg: GroveConfig,
    cluster_cfg: ClusterConfig,
    operator_dir: Path,
) -> None:
    """Deploy Grove operator using Skaffold.

    Builds images, deploys charts, applies helm overrides, and waits for
    the webhook to become responsive. Only "local" mode (skaffold build) is
    currently implemented.

    Args:
        grove_cfg: Grove operator configuration with namespace, mode, and profiling settings.
        cluster_cfg: Cluster configuration for registry resolution.
        operator_dir: Root directory of the Grove operator source tree.

    Raises:
        NotImplementedError: If grove_cfg.mode is not "local".
    """
    if grove_cfg.mode != "local":
        raise NotImplementedError(f"Grove deploy mode {grove_cfg.mode!r} is not yet implemented")

    console.print(Panel.fit("Deploying Grove operator", style="bold blue"))
    try:
        sh.helm("uninstall", HELM_RELEASE_GROVE, "-n", grove_cfg.namespace)
        console.print("[yellow]   Removed existing Grove operator release[/yellow]")
    except sh.ErrorReturnCode_1:
        console.print("[yellow]   No existing Grove operator release found[/yellow]")

    build_date = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    os.environ.update(
        {
            "VERSION": E2E_TEST_VERSION,
            "LD_FLAGS": (
                f"-X {GROVE_MODULE_PATH}.gitCommit={E2E_TEST_COMMIT} "
                f"-X {GROVE_MODULE_PATH}.gitTreeState={E2E_TEST_TREE_STATE} "
                f"-X {GROVE_MODULE_PATH}.buildDate={build_date} "
                f"-X {GROVE_MODULE_PATH}.gitVersion={E2E_TEST_VERSION}"
            ),
        }
    )

    if cluster_cfg.registry:
        push_repo = pull_repo = cluster_cfg.registry
    else:
        push_repo, pull_repo = resolve_registry_repos(cluster_cfg.registry_port)

    raw_images = _build_grove_images(grove_cfg.local.skaffold_profile, operator_dir, push_repo)
    images = {name: tag.replace(push_repo, pull_repo) for name, tag in raw_images.items()}

    _deploy_grove_charts(grove_cfg.local.skaffold_profile, grove_cfg.namespace, operator_dir, images, pull_repo)

    helm_overrides = collect_grove_helm_overrides(grove_cfg)
    console.print("[yellow]\u2139\ufe0f  Waiting for Grove deployment rollout...[/yellow]")
    sh.kubectl("rollout", "status", "deployment", "-n", grove_cfg.namespace, "--timeout=5m")

    _apply_grove_helm_overrides(operator_dir, helm_overrides, grove_cfg.namespace)

    _wait_grove_webhook(operator_dir)


def uninstall_grove_operator(grove_namespace: str | None = None) -> None:
    """Uninstall Grove operator via Helm.

    Args:
        grove_namespace: Kubernetes namespace where Grove is installed, or None for default.
    """
    namespace = grove_namespace or DEFAULT_GROVE_NAMESPACE
    console.print(Panel.fit("Uninstalling Grove operator", style="bold blue"))
    try:
        sh.helm("uninstall", HELM_RELEASE_GROVE, "-n", namespace)
        console.print("[green]\u2705 Grove operator uninstalled[/green]")
    except sh.ErrorReturnCode_1:
        console.print("[yellow]No existing Grove operator release found[/yellow]")
