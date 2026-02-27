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

Expects an **existing** cluster (created by ``make run-e2e-mnnvl-full`` or
manually via ``infra-manager.py setup e2e``).  The script reconfigures the cluster
between test suites using config-cluster.py (declarative / idempotent).

Configurations:
  1. Feature enabled  + CRD supported   (fake GPU installed)
  2. Feature disabled + CRD supported   (fake GPU installed)
  3. Feature enabled  + CRD unsupported (no fake GPU) -- operator expected to crash
  4. Feature disabled + CRD unsupported (no fake GPU)

Usage:
    # Via Makefile (recommended — handles cluster creation + cleanup):
    make run-e2e-mnnvl-full

    # Directly (cluster must already exist):
    ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e_all.py
"""

from __future__ import annotations

import os
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------
SCRIPT_DIR = Path(__file__).resolve().parent
OPERATOR_DIR = SCRIPT_DIR.parent.parent
CONFIG_CLUSTER_SCRIPT = OPERATOR_DIR / "hack" / "config-cluster.py"

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
    name: str
    config_flags: list[str] = field(default_factory=list)


# The four configurations we test.
#
# Order is optimised to minimise reconfiguration between runs:
#   Config4 → matches the initial cluster state (no fake GPU, MNNVL disabled)
#   Config3 → only toggle: enable MNNVL
#   Config1 → only toggle: install fake GPU
#   Config2 → only toggle: disable MNNVL
#
# Config 3 uses --skip-operator-wait because the operator intentionally exits
# (preflight failure) in this invalid configuration; the e2e test itself
# validates the expected failure behaviour.
CONFIGS: list[ConfigEntry] = [
    ConfigEntry("Config1_UnsupportedAndDisabled",
                ["--fake-gpu=no", "--auto-mnnvl=disabled"]),
    ConfigEntry("Config2_UnsupportedButEnabled",
                ["--fake-gpu=no", "--auto-mnnvl=enabled", "--skip-operator-wait"]),
    ConfigEntry("Config3_SupportedAndEnabled",
                ["--fake-gpu=yes", "--auto-mnnvl=enabled"]),
    ConfigEntry("Config4_SupportedButDisabled",
                ["--fake-gpu=yes", "--auto-mnnvl=disabled"]),
]


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def _run_script(script: Path, args: list[str]) -> int:
    """Run a Python script as a subprocess. Returns the exit code."""
    cmd = [sys.executable, str(script)] + args
    log_info(f"Running: {' '.join(cmd)}")
    result = subprocess.run(cmd)
    return result.returncode


def run_e2e_tests() -> int:
    """Run the MNNVL e2e Go tests. Returns exit code."""
    log_info("Running MNNVL e2e tests...")
    cmd = (
        "USE_EXISTING_CLUSTER=true go test -tags=e2e -v -timeout=30m -count=1"
        " ./e2e/tests/auto-mnnvl/..."
    )
    proc = subprocess.Popen(
        cmd, shell=True, cwd=OPERATOR_DIR,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
    )
    assert proc.stdout is not None
    for line in proc.stdout:
        sys.stdout.write(line)
    proc.wait()
    return proc.returncode


# ---------------------------------------------------------------------------
# Per-config runner
# ---------------------------------------------------------------------------
def run_config(cfg: ConfigEntry) -> bool:
    """Configure the cluster for *cfg*, run tests, and return True on success."""
    log_header("==========================================")
    log_header(f"Running: {cfg.name}")
    log_header(f"  Flags: {' '.join(cfg.config_flags)}")
    log_header("==========================================")

    # Reconfigure cluster
    rc = _run_script(CONFIG_CLUSTER_SCRIPT, cfg.config_flags)
    if rc != 0:
        log_error(f"config-cluster.py failed for {cfg.name}")
        return False

    # Run tests
    rc = run_e2e_tests()
    passed = rc == 0

    if passed:
        log_success(f"{cfg.name}: PASSED")
    else:
        log_error(f"{cfg.name}: FAILED")

    print(flush=True)
    return passed


# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
def print_summary(results: dict[str, str]) -> bool:
    """Print the final summary table. Returns True if all passed."""
    print(flush=True)
    log_info("==========================================")
    log_info("MNNVL E2E Test Summary")
    log_info("==========================================")

    all_passed = True
    for name, status in results.items():
        if status == "PASS":
            log_success(f"  {name}: {status}")
        elif status == "FAIL":
            log_error(f"  {name}: {status}")
            all_passed = False
        else:
            log_warning(f"  {name}: {status}")
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
    log_info("==========================================")
    log_info("MNNVL E2E Full Test Matrix")
    log_info("==========================================")
    log_info("This will run all 4 configurations:")
    for cfg in CONFIGS:
        log_info(f"  - {cfg.name}  ({' '.join(cfg.config_flags)})")
    log_info("==========================================")
    print(flush=True)

    os.chdir(OPERATOR_DIR)

    # Run each configuration (cluster already exists, created by Makefile)
    results: dict[str, str] = {cfg.name: "NOT_RUN" for cfg in CONFIGS}

    for cfg in CONFIGS:
        passed = run_config(cfg)
        results[cfg.name] = "PASS" if passed else "FAIL"

    # Print summary and exit
    all_passed = print_summary(results)
    sys.exit(0 if all_passed else 1)


if __name__ == "__main__":
    main()
