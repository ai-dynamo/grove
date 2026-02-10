#!/usr/bin/env python3
"""run_autoMNNVL_e2e.py - Run autoMNNVL e2e tests with a single configuration.

This script sets up the cluster (via setup_autoMNNVL_cluster.py) and runs the e2e tests.
All setup options are passed through to setup_autoMNNVL_cluster.py.

Usage: ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e.py [options]

Options:
  --skip-setup             Only run tests, skip cluster setup
  --skip-cluster-create    Reuse existing cluster (passed to setup script)
  --with-fake-gpu          Install fake GPU operator (default, passed to setup script)
  --without-fake-gpu       Skip fake GPU operator (passed to setup script)
  --mnnvl-enabled          Enable MNNVL feature (default, passed to setup script)
  --mnnvl-disabled         Disable MNNVL feature (passed to setup script)
  --build                  Build images (default, passed to setup script)
  --skip-build             Skip image build (passed to setup script)
  --skip-operator-wait     Don't wait for operator readiness (passed to setup script)
  --image <tag>            Use existing image (passed to setup script)
  --help                   Show this help message
"""

from __future__ import annotations

import os
import subprocess
import sys
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
def parse_args(argv: list[str]) -> tuple[bool, list[str]]:
    """Parse arguments and split into our flags vs. setup-script passthrough flags.

    Returns (skip_setup, setup_args).
    """
    skip_setup = False
    setup_args: list[str] = []

    i = 0
    while i < len(argv):
        arg = argv[i]
        if arg == "--skip-setup":
            skip_setup = True
        elif arg == "--help":
            print(__doc__)
            sys.exit(0)
        elif arg == "--image":
            # --image takes an argument, pass both
            setup_args.append(arg)
            i += 1
            if i < len(argv):
                setup_args.append(argv[i])
            else:
                log_error("--image requires a tag argument")
                sys.exit(1)
        else:
            setup_args.append(arg)
        i += 1

    return skip_setup, setup_args


# ---------------------------------------------------------------------------
# Cluster setup
# ---------------------------------------------------------------------------
def run_cluster_setup(setup_args: list[str]) -> None:
    args_str = " ".join(setup_args) if setup_args else "<none>"
    log_info(f"Running cluster setup with args: {args_str}")

    cmd = [sys.executable, str(SCRIPT_DIR / "setup_autoMNNVL_cluster.py")] + setup_args
    result = subprocess.run(cmd)
    if result.returncode != 0:
        log_error("Cluster setup failed!")
        sys.exit(1)


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
    skip_setup, setup_args = parse_args(sys.argv[1:])

    log_info("==========================================")
    log_info("MNNVL E2E Test Runner")
    log_info("==========================================")

    os.chdir(OPERATOR_DIR)

    # Step 1: Setup cluster (unless skipped)
    if not skip_setup:
        run_cluster_setup(setup_args)
    else:
        log_warning("Skipping cluster setup (--skip-setup)")

    log_info("==========================================")
    log_info("Environment ready. Running e2e tests...")
    log_info("==========================================")

    # Step 2: Run tests
    exit_code = run_e2e_tests()
    sys.exit(exit_code)


if __name__ == "__main__":
    main()
