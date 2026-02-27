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

"""config-cluster.py - Declarative configuration for an existing E2E cluster.

This script applies or removes configuration on top of a cluster that was
already created by infra-manager.py.  It is **idempotent**: running it
twice with the same flags is a no-op.

Supported configuration axes:

  --fake-gpu=yes|no       Install / uninstall the fake-gpu-operator (and its
                          ComputeDomain CRD).  Default: no.
  --auto-mnnvl=enabled|disabled
                          Enable / disable the autoMNNVL feature on the
                          existing Grove operator deployment via
                          ``helm upgrade --reuse-values``.  Default: disabled.
  --skip-operator-wait    Don't wait for the operator pod to become Ready
                          after the helm upgrade (useful when the operator is
                          expected to crash, e.g. Config 3).

Usage:
    # Install fake GPU + enable MNNVL
    python config-cluster.py --fake-gpu=yes --auto-mnnvl=enabled

    # Uninstall fake GPU + disable MNNVL
    python config-cluster.py --fake-gpu=no --auto-mnnvl=disabled

    # Enable MNNVL without fake GPU, operator expected to crash
    python config-cluster.py --fake-gpu=no --auto-mnnvl=enabled --skip-operator-wait
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
import time

# ---------------------------------------------------------------------------
# Coloured logging helpers
# ---------------------------------------------------------------------------
_RED = "\033[0;31m"
_GREEN = "\033[0;32m"
_YELLOW = "\033[1;33m"
_BLUE = "\033[0;34m"
_CYAN = "\033[0;36m"
_NC = "\033[0m"


def log_info(msg: str) -> None:
    print(f"{_BLUE}[INFO]{_NC} {msg}", flush=True)


def log_success(msg: str) -> None:
    print(f"{_GREEN}[SUCCESS]{_NC} {msg}", flush=True)


def log_warning(msg: str) -> None:
    print(f"{_YELLOW}[WARNING]{_NC} {msg}", flush=True)


def log_error(msg: str) -> None:
    print(f"{_RED}[ERROR]{_NC} {msg}", flush=True)


def log_header(msg: str) -> None:
    print(f"{_CYAN}[CONFIG]{_NC} {msg}", flush=True)


# ---------------------------------------------------------------------------
# Shell helpers
# ---------------------------------------------------------------------------
def run(
    cmd: str,
    *,
    check: bool = True,
    capture: bool = False,
) -> subprocess.CompletedProcess[str]:
    """Run a shell command, streaming output unless *capture* is True."""
    return subprocess.run(
        cmd,
        shell=True,
        check=check,
        capture_output=capture,
        text=True,
    )


def run_or_warn(cmd: str, warning_msg: str) -> subprocess.CompletedProcess[str]:
    """Run a command and log a warning if it fails (instead of raising)."""
    result = run(cmd, check=False)
    if result.returncode != 0:
        log_warning(f"{warning_msg} (exit code {result.returncode})")
    return result


def run_quiet(cmd: str, *, check: bool = False) -> subprocess.CompletedProcess[str]:
    """Run a command suppressing stdout and stderr. Returns the result."""
    return subprocess.run(
        cmd, shell=True, check=check,
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, text=True,
    )


# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
GROVE_NAMESPACE = "grove-system"
GROVE_RELEASE = "grove-operator"
FAKE_GPU_NAMESPACE = "gpu-operator"
FAKE_GPU_RELEASE = "fake-gpu-operator"
FAKE_GPU_CHART = "oci://ghcr.io/run-ai/fake-gpu-operator/fake-gpu-operator"
FAKE_GPU_VERSION = "0.0.72"
COMPUTE_DOMAIN_CRD = "computedomains.resource.nvidia.com"


# ---------------------------------------------------------------------------
# Fake GPU operator  (declarative / idempotent)
# ---------------------------------------------------------------------------
def _fake_gpu_installed() -> bool:
    """Return True if the fake-gpu-operator CRD is currently installed."""
    result = run_quiet(f"kubectl get crd {COMPUTE_DOMAIN_CRD}")
    return result.returncode == 0


def ensure_fake_gpu(desired: bool) -> None:
    """Ensure the fake GPU operator matches *desired* state."""
    currently_installed = _fake_gpu_installed()

    if desired and not currently_installed:
        log_info("Installing fake GPU operator with ComputeDomain support...")

        # Clean up any leftover resources from a previous partial install
        run_quiet("kubectl delete runtimeclass nvidia")
        run_quiet(f"helm uninstall {FAKE_GPU_RELEASE} -n {FAKE_GPU_NAMESPACE}")

        run(
            f"helm upgrade -i {FAKE_GPU_RELEASE}"
            f" {FAKE_GPU_CHART}"
            f" --namespace {FAKE_GPU_NAMESPACE}"
            f" --create-namespace"
            f" --devel"
            f" --version {FAKE_GPU_VERSION}"
            f" --set computeDomainDraPlugin.enabled=true"
            f" --wait"
        )

        log_info("Waiting for fake GPU operator pods to be ready...")
        run_or_warn(
            f"kubectl wait --for=condition=Ready pods"
            f" -l app.kubernetes.io/name=fake-gpu-operator"
            f" -n {FAKE_GPU_NAMESPACE} --timeout=120s",
            "Fake GPU operator pods did not become ready",
        )

        # Verify CRD exists
        result = run_quiet(f"kubectl get crd {COMPUTE_DOMAIN_CRD}")
        if result.returncode == 0:
            log_success("ComputeDomain CRD installed")
        else:
            log_error("ComputeDomain CRD not found after install!")
            sys.exit(1)

    elif not desired and currently_installed:
        log_info("Removing fake GPU operator...")
        run_quiet(f"helm uninstall {FAKE_GPU_RELEASE} -n {FAKE_GPU_NAMESPACE}")
        run_quiet("kubectl delete runtimeclass nvidia")
        run_quiet(f"kubectl delete crd {COMPUTE_DOMAIN_CRD}")
        log_success("Fake GPU operator removed")

    elif desired and currently_installed:
        log_info("Fake GPU operator is already installed -- nothing to do")

    else:
        log_info("Fake GPU operator is already absent -- nothing to do")


# ---------------------------------------------------------------------------
# Operator restart-cycle helpers
# ---------------------------------------------------------------------------
def _get_operator_restart_count() -> int:
    """Return the restart count of the first operator container, or 0 on failure."""
    result = run(
        f"kubectl get pods -n {GROVE_NAMESPACE}"
        f" -l app.kubernetes.io/name=grove-operator"
        f" -o jsonpath='{{.items[0].status.containerStatuses[0].restartCount}}'",
        check=False,
        capture=True,
    )
    raw = result.stdout.strip().strip("'") if result.returncode == 0 else ""
    return int(raw) if raw.isdigit() else 0


def _wait_for_cert_refresh_restart(
    timeout: int = 60,
    poll_interval: int = 3,
) -> None:
    """Poll for the operator cert-refresh restart instead of using a fixed sleep.

    After a helm upgrade the operator may exit once to pick up rotated certs.
    This function detects the container restart (via restartCount) and then
    waits for the pod to become Ready again.  If no restart is observed within
    *timeout* seconds the function returns normally (the restart may not be
    needed every time).
    """
    initial = _get_operator_restart_count()
    log_info(
        f"Watching for operator cert refresh restart (current restartCount: {initial})..."
    )

    elapsed = 0
    restarted = False
    while elapsed < timeout:
        time.sleep(poll_interval)
        elapsed += poll_interval
        current = _get_operator_restart_count()
        if current > initial:
            log_info(
                f"Operator restarted (restartCount {initial} -> {current})"
            )
            restarted = True
            break

    if not restarted:
        log_info(f"No cert refresh restart detected within {timeout}s — continuing")
        return

    log_info("Waiting for operator pod to be ready after cert refresh restart...")
    run_or_warn(
        f"kubectl rollout status deployment/{GROVE_RELEASE}"
        f" -n {GROVE_NAMESPACE} --timeout=120s",
        "Grove operator rollout did not complete after cert refresh restart",
    )
    run_or_warn(
        f"kubectl wait --for=condition=Ready pods"
        f" -l app.kubernetes.io/name=grove-operator"
        f" -n {GROVE_NAMESPACE} --timeout=120s",
        "Grove operator pod did not become ready after cert refresh restart",
    )


# ---------------------------------------------------------------------------
# Auto-MNNVL toggle  (declarative / idempotent)
# ---------------------------------------------------------------------------
def ensure_auto_mnnvl(enabled: bool, *, skip_wait: bool = False) -> None:
    """Set the autoMNNVLEnabled flag on the Grove operator deployment.

    Uses ``helm upgrade --reuse-values`` so all other chart values stay
    unchanged.  The operator pod is restarted automatically by Helm.
    """
    desired_str = str(enabled).lower()
    log_info(f"Setting autoMNNVLEnabled={desired_str} on {GROVE_RELEASE}...")

    run(
        f"helm upgrade {GROVE_RELEASE} charts"
        f" --namespace {GROVE_NAMESPACE}"
        f" --reuse-values"
        f' --set "config.network.autoMNNVLEnabled={desired_str}"'
    )

    if not skip_wait:
        log_info("Waiting for Grove operator pod to be ready...")
        # The upgrade may trigger a rolling restart; wait for the new pod.
        run_or_warn(
            f"kubectl rollout status deployment/{GROVE_RELEASE}"
            f" -n {GROVE_NAMESPACE} --timeout=120s",
            "Grove operator rollout did not complete",
        )
        run_or_warn(
            f"kubectl wait --for=condition=Ready pods"
            f" -l app.kubernetes.io/name=grove-operator"
            f" -n {GROVE_NAMESPACE} --timeout=120s",
            "Grove operator pod did not become ready",
        )
        # Cert-rotation may exit the process to trigger a restart; poll for it
        # rather than using a fixed sleep.
        _wait_for_cert_refresh_restart()
    else:
        log_warning("Skipping operator readiness check (--skip-operator-wait)")
        time.sleep(5)

    # Verify the configuration
    log_info("Verifying MNNVL configuration...")
    result = run(
        f"kubectl get configmap -n {GROVE_NAMESPACE}"
        f" -l app.kubernetes.io/component=operator-configmap"
        r" -o jsonpath='{.items[0].data.config\.yaml}'",
        check=False,
        capture=True,
    )
    config_value = result.stdout if result.returncode == 0 else ""

    if f"autoMNNVLEnabled: {desired_str}" in config_value:
        log_success(f"autoMNNVLEnabled is {desired_str}")
    else:
        log_warning(f"Could not verify autoMNNVLEnabled setting (expected {desired_str})")

    # Show operator logs for debugging
    log_info("Operator startup logs:")
    run(
        f"kubectl logs -n {GROVE_NAMESPACE} deployment/{GROVE_RELEASE} --tail=20",
        check=False,
    )


# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Declarative configuration for an existing E2E cluster.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--fake-gpu",
        choices=["yes", "no"],
        default="no",
        help="Install (yes) or uninstall (no) the fake GPU operator. Default: no.",
    )
    parser.add_argument(
        "--auto-mnnvl",
        choices=["enabled", "disabled"],
        default="disabled",
        help="Enable or disable autoMNNVL on the Grove operator. Default: disabled.",
    )
    parser.add_argument(
        "--skip-operator-wait",
        action="store_true",
        default=False,
        help="Don't wait for the operator pod to become Ready after helm upgrade.",
    )
    return parser.parse_args()


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
def main() -> None:
    args = parse_args()

    want_fake_gpu = args.fake_gpu == "yes"
    want_mnnvl = args.auto_mnnvl == "enabled"

    log_header("==========================================")
    log_header("Cluster Configuration (declarative)")
    log_header("==========================================")
    log_header(f"  Fake GPU:         {'yes' if want_fake_gpu else 'no'}")
    log_header(f"  Auto-MNNVL:       {'enabled' if want_mnnvl else 'disabled'}")
    log_header(f"  Skip operator wait: {args.skip_operator_wait}")
    log_header("==========================================")

    # Change to the operator directory (helm chart path is relative)
    script_dir = os.path.dirname(os.path.abspath(__file__))
    operator_dir = os.path.join(script_dir, "..")
    os.chdir(operator_dir)

    # 1. Fake GPU operator
    ensure_fake_gpu(want_fake_gpu)

    # 2. Auto-MNNVL
    ensure_auto_mnnvl(want_mnnvl, skip_wait=args.skip_operator_wait)

    log_success("==========================================")
    log_success("Cluster configuration applied successfully")
    log_success("==========================================")


if __name__ == "__main__":
    main()
