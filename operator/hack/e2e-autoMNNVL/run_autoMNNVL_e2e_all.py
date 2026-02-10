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

"""run_autoMNNVL_e2e_all.py - Run autoMNNVL e2e tests with all 4 configurations.

This script runs the autoMNNVL e2e tests with all possible configurations:
  1. Feature enabled  + CRD supported   (fake GPU installed)
  2. Feature disabled + CRD supported   (fake GPU installed)
  3. Feature enabled  + CRD unsupported (no fake GPU)
  4. Feature disabled + CRD unsupported (no fake GPU)

Images are built with skaffold/ko and pushed to the k3d registry as part of
cluster setup -- no Docker build is required.

Usage: ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e_all.py [options]

Options:
  --keep-cluster  Keep cluster after all configs (no shutdown)
  --help          Show this help message
"""

from __future__ import annotations

import argparse
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------
SCRIPT_DIR = Path(__file__).resolve().parent
OPERATOR_DIR = SCRIPT_DIR.parent.parent

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
# Configuration matrix
# ---------------------------------------------------------------------------
@dataclass
class ConfigEntry:
    num: int
    name: str
    fake_gpu_flag: str
    mnnvl_flag: str
    extra_flags: list[str]


# The four configurations we test.
#
# Images are built with skaffold/ko as part of cluster setup (no upfront Docker
# build). Configs that create a new cluster also build+push images; configs that
# reuse the cluster skip the build since images are already in the registry.
#
# Config 3 uses --skip-operator-wait because the operator intentionally exits
# (preflight failure) in this invalid configuration; the e2e test itself
# validates the expected failure behaviour.
CONFIGS: list[ConfigEntry] = [
    ConfigEntry(1, "Config1_SupportedAndEnabled",
                "--with-fake-gpu", "--mnnvl-enabled",
                []),
    ConfigEntry(2, "Config2_SupportedButDisabled",
                "--with-fake-gpu", "--mnnvl-disabled",
                ["--skip-cluster-create", "--skip-build"]),
    ConfigEntry(3, "Config3_UnsupportedButEnabled",
                "--without-fake-gpu", "--mnnvl-enabled",
                ["--skip-operator-wait"]),
    ConfigEntry(4, "Config4_UnsupportedAndDisabled",
                "--without-fake-gpu", "--mnnvl-disabled",
                ["--skip-cluster-create", "--skip-build"]),
]

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run MNNVL e2e tests with all 4 configurations.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--keep-cluster", action="store_true", default=False,
                        help="Keep cluster after all configs (no shutdown)")
    return parser.parse_args()


def run_config(cfg: ConfigEntry) -> bool:
    """Run a single configuration. Returns True on success."""
    log_header("==========================================")
    log_header(f"Running: {cfg.name}")
    log_header(f"  Fake GPU: {cfg.fake_gpu_flag}")
    log_header(f"  MNNVL:    {cfg.mnnvl_flag}")
    log_header("==========================================")

    cmd = [
        sys.executable,
        str(SCRIPT_DIR / "run_autoMNNVL_e2e.py"),
        cfg.fake_gpu_flag,
        cfg.mnnvl_flag,
        *cfg.extra_flags,
    ]
    result = subprocess.run(cmd)
    passed = result.returncode == 0

    if passed:
        log_success(f"{cfg.name}: PASSED")
    else:
        log_error(f"{cfg.name}: FAILED")

    print(flush=True)
    return passed


def shutdown_cluster() -> None:
    log_info("Shutting down cluster...")
    subprocess.run(
        [sys.executable, str(SCRIPT_DIR / "setup_autoMNNVL_cluster.py"), "--shutdown"],
    )


def print_summary(results: dict[str, str]) -> bool:
    """Print the final summary table. Returns True if all passed."""
    print(flush=True)
    log_info("==========================================")
    log_info("MNNVL E2E Test Summary")
    log_info("==========================================")

    all_passed = True
    for name, status in results.items():
        if status == "PASS":
            log_success(f"{name}: {status}")
        elif status == "FAIL":
            log_error(f"{name}: {status}")
            all_passed = False
        else:
            log_warning(f"{name}: {status}")
            all_passed = False

    log_info("==========================================")

    if all_passed:
        log_success("All configurations PASSED!")
    else:
        log_error("Some configurations FAILED!")

    return all_passed


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
def main() -> None:
    args = parse_args()

    log_info("==========================================")
    log_info("MNNVL E2E Full Test Matrix")
    log_info("==========================================")
    log_info("This will run all 4 configurations:")
    log_info("  1. Feature enabled  + CRD supported")
    log_info("  2. Feature disabled + CRD supported")
    log_info("  3. Feature enabled  + CRD unsupported")
    log_info("  4. Feature disabled + CRD unsupported")
    log_info("==========================================")
    print(flush=True)

    # Run all configurations
    results: dict[str, str] = {cfg.name: "NOT_RUN" for cfg in CONFIGS}

    for cfg in CONFIGS:
        passed = run_config(cfg)
        results[cfg.name] = "PASS" if passed else "FAIL"

    # Shutdown cluster unless requested to keep it
    if not args.keep_cluster:
        shutdown_cluster()
    else:
        log_warning("Keeping cluster (--keep-cluster)")

    # Print summary and exit
    all_passed = print_summary(results)
    sys.exit(0 if all_passed else 1)


if __name__ == "__main__":
    main()
