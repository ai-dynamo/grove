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

"""Utility functions for kubectl, helm overrides, and command checks."""

from __future__ import annotations

import subprocess

import sh

from infra_manager.constants import (
    DEFAULT_PPROF_BIND_ADDRESS,
    HELM_KEY_ANNOTATION_PREFIX,
    HELM_KEY_PCLQ_SYNCS,
    HELM_KEY_PCS_SYNCS,
    HELM_KEY_PCSG_SYNCS,
    HELM_KEY_PPROF_BIND_ADDRESS,
    HELM_KEY_PROFILING,
    KWOK_GITHUB_REPO,
)


def kwok_release_url(version: str) -> str:
    """Build the GitHub release base URL for a KWOK version.

    Args:
        version: KWOK release version tag (e.g. ``v0.7.0``).

    Returns:
        Full GitHub release download URL.
    """
    return f"https://github.com/{KWOK_GITHUB_REPO}/releases/download/{version}"


def resolve_registry_repos(registry: str | None, port: int) -> tuple[str, str]:
    """Resolve push/pull registry repos.

    k3d uses separate names for push (localhost:<port>) and pull (registry:<port>)
    because the push happens from the host while the pull happens inside the cluster.

    Args:
        registry: Explicit registry URL override, or None for k3d local.
        port: k3d local registry port number.

    Returns:
        Tuple of (push_repo, pull_repo) registry URLs.
    """
    if registry:
        return registry, registry
    return f"localhost:{port}", f"registry:{port}"


def collect_grove_helm_overrides(
    profiling: bool = False,
    pcs_syncs: int | None = None,
    pclq_syncs: int | None = None,
    pcsg_syncs: int | None = None,
) -> list[str]:
    """Build helm override strings from grove tuning options.

    Args:
        profiling: Whether to enable pprof on Grove.
        pcs_syncs: PodCliqueSet concurrent syncs override, or None.
        pclq_syncs: PodClique concurrent syncs override, or None.
        pcsg_syncs: PodCliqueScalingGroup concurrent syncs override, or None.

    Returns:
        List of ``key=value`` strings for ``helm --set`` arguments.
    """
    overrides: list[tuple[bool, str, str]] = [
        (profiling, HELM_KEY_PROFILING, "true"),
        (profiling, HELM_KEY_PPROF_BIND_ADDRESS, DEFAULT_PPROF_BIND_ADDRESS),
        (pcs_syncs is not None, HELM_KEY_PCS_SYNCS, str(pcs_syncs)),
        (pclq_syncs is not None, HELM_KEY_PCLQ_SYNCS, str(pclq_syncs)),
        (pcsg_syncs is not None, HELM_KEY_PCSG_SYNCS, str(pcsg_syncs)),
    ]
    result = [f"{key}={value}" for enabled, key, value in overrides if enabled]
    if profiling:
        result.extend(_pyroscope_annotation_overrides())
    return result


def _pyroscope_annotation_overrides() -> list[str]:
    """Build Grafana/Pyroscope scrape annotation overrides for helm --set.

    Returns:
        List of ``annotations.<escaped-key>=value`` strings.
    """
    port = DEFAULT_PPROF_BIND_ADDRESS.split(":")[-1]
    annotations = {
        "profiles.grafana.com/cpu.scrape": "true",
        "profiles.grafana.com/cpu.port": port,
        "profiles.grafana.com/memory.scrape": "true",
        "profiles.grafana.com/memory.port": port,
        "profiles.grafana.com/goroutine.scrape": "true",
        "profiles.grafana.com/goroutine.port": port,
    }
    return [
        f"{HELM_KEY_ANNOTATION_PREFIX}.{key.replace('.', '\\.')}={value}"
        for key, value in annotations.items()
    ]


def require_command(cmd: str) -> None:
    """Check if a command exists on the system PATH.

    Args:
        cmd: Name of the CLI command to check.

    Raises:
        RuntimeError: If the command is not found.
    """
    try:
        sh.which(cmd)
    except sh.ErrorReturnCode as err:
        raise RuntimeError(f"Required command '{cmd}' not found. Please install it first.") from err


def run_kubectl(args: list[str], timeout: int = 30) -> tuple[bool, str, str]:
    """Run a kubectl command via subprocess and return (success, stdout, stderr).

    Uses subprocess instead of sh because kubectl output parsing requires
    precise control over stdout/stderr separation that sh's combined output
    makes unreliable (e.g., checking webhook readiness keywords).

    Args:
        args: kubectl arguments (e.g. ``["get", "pods", "-n", "default"]``).
        timeout: Maximum seconds to wait for the command to complete.

    Returns:
        Tuple of (success, stdout, stderr).
    """
    try:
        result = subprocess.run(
            ["kubectl", *args],
            capture_output=True,
            text=True,
            timeout=timeout,
        )
        return result.returncode == 0, result.stdout, result.stderr
    except (subprocess.SubprocessError, OSError) as exc:
        return False, "", str(exc)
