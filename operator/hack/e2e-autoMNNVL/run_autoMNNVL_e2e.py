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

"""run_autoMNNVL_e2e.py - Run autoMNNVL e2e tests with a single configuration.

Expects an **existing** cluster (created by ``make run-e2e-mnnvl-full`` or
manually via ``create-cluster.py``).  This script:
  1. Configures the cluster via config-cluster.py  (fake GPU + MNNVL toggle)
  2. Runs go test ./e2e/tests/auto-mnnvl/...

Usage:
    # Via Makefile (recommended — handles cluster creation + cleanup):
    make run-e2e-mnnvl-full

    # Directly (cluster must already exist):
    ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e.py --fake-gpu=yes --auto-mnnvl=enabled

Options:
  --fake-gpu=yes|no        Passed to config-cluster.py (default: no)
  --auto-mnnvl=enabled|disabled
                           Passed to config-cluster.py (default: disabled)
  --skip-operator-wait     Passed to config-cluster.py
  --help                   Show this help message
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
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
_NC = "\033[0m"


def log_info(msg: str) -> None:
    print(f"{_BLUE}[INFO]{_NC} {msg}", flush=True)


def log_success(msg: str) -> None:
    print(f"{_GREEN}[SUCCESS]{_NC} {msg}", flush=True)


def log_warning(msg: str) -> None:
    print(f"{_YELLOW}[WARNING]{_NC} {msg}", flush=True)


def log_error(msg: str) -> None:
    print(f"{_RED}[ERROR]{_NC} {msg}", flush=True)


# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run autoMNNVL e2e tests with a single configuration.",
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
        help="Don't wait for the operator pod to become Ready.",
    )
    return parser.parse_args()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def _run_script(script: Path, args: list[str]) -> None:
    """Run a Python script as a subprocess; abort on failure."""
    cmd = [sys.executable, str(script)] + args
    log_info(f"Running: {' '.join(cmd)}")
    result = subprocess.run(cmd)
    if result.returncode != 0:
        log_error(f"Script failed with exit code {result.returncode}: {script.name}")
        sys.exit(result.returncode)


# ---------------------------------------------------------------------------
# E2E tests
# ---------------------------------------------------------------------------
E2E_LOG = "/tmp/mnnvl-e2e-results.log"


def run_e2e_tests() -> int:
    log_info("Running MNNVL e2e tests...")

    cmd = (
        "USE_EXISTING_CLUSTER=true go test -tags=e2e -v -timeout=30m -count=1"
        " ./e2e/tests/auto-mnnvl/..."
    )

    with open(E2E_LOG, "w") as log_file:
        proc = subprocess.Popen(
            cmd, shell=True, cwd=OPERATOR_DIR,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
        )
        assert proc.stdout is not None
        for line in proc.stdout:
            sys.stdout.write(line)
            log_file.write(line)
        proc.wait()

    if proc.returncode == 0:
        log_success("All MNNVL e2e tests passed!")
    else:
        log_error(f"Some MNNVL e2e tests failed. See {E2E_LOG} for details.")

    return proc.returncode


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
def main() -> None:
    args = parse_args()

    log_info("==========================================")
    log_info("MNNVL E2E Test Runner")
    log_info("==========================================")
    log_info(f"  Fake GPU:             {args.fake_gpu}")
    log_info(f"  Auto-MNNVL:           {args.auto_mnnvl}")
    log_info(f"  Skip operator wait:   {args.skip_operator_wait}")
    log_info("==========================================")

    os.chdir(OPERATOR_DIR)

    # Step 1: Configure cluster (fake GPU + MNNVL)
    config_args = [
        f"--fake-gpu={args.fake_gpu}",
        f"--auto-mnnvl={args.auto_mnnvl}",
    ]
    if args.skip_operator_wait:
        config_args.append("--skip-operator-wait")

    _run_script(CONFIG_CLUSTER_SCRIPT, config_args)

    # Step 2: Run tests
    log_info("==========================================")
    log_info("Environment ready. Running e2e tests...")
    log_info("==========================================")

    exit_code = run_e2e_tests()
    sys.exit(exit_code)


if __name__ == "__main__":
    main()
