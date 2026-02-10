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

"""setup_autoMNNVL_cluster.py - Set up a k3d cluster for autoMNNVL e2e testing.

This script handles cluster creation, fake GPU operator installation,
and Grove operator deployment with configurable options.

Usage: ./hack/e2e-autoMNNVL/setup_autoMNNVL_cluster.py [options]

Options:
  --skip-cluster-create    Reuse existing cluster (default: false)
  --shutdown               Delete cluster and exit (no other args)
  --with-fake-gpu          Install fake GPU operator (default)
  --without-fake-gpu       Skip fake GPU operator installation
  --mnnvl-enabled          Enable MNNVL feature in Grove (default)
  --mnnvl-disabled         Disable MNNVL feature in Grove
  --build                  Build images with skaffold/ko (default)
  --skip-build             Skip image build, reuse images already in registry
  --skip-operator-wait     Don't wait for operator pod readiness
  --image <tag>            Use existing image with specified tag
  --help                   Show this help message
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------
SCRIPT_DIR = Path(__file__).resolve().parent
OPERATOR_DIR = SCRIPT_DIR.parent.parent

# ---------------------------------------------------------------------------
# Cluster constants
# ---------------------------------------------------------------------------
CLUSTER_NAME = "mnnvl-test-cluster"
K3S_VERSION = "v1.34.2-k3s1"
REGISTRY_NAME = "mnnvl-registry"
REGISTRY_PORT = "5001"
IMAGE_TAG = "mnnvl-test"
GROVE_NAMESPACE = "grove-system"

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
# Shell helpers
# ---------------------------------------------------------------------------
def run(cmd: str | list[str], *, check: bool = True, capture: bool = False,
        cwd: Path | None = None, env: dict[str, str] | None = None,
        ) -> subprocess.CompletedProcess[str]:
    """Run a shell command, streaming output unless *capture* is True."""
    if isinstance(cmd, str):
        shell = True
    else:
        shell = False

    full_env = None
    if env:
        full_env = {**os.environ, **env}

    return subprocess.run(
        cmd,
        shell=shell,
        check=check,
        capture_output=capture,
        text=True,
        cwd=cwd,
        env=full_env,
    )


def run_quiet(cmd: str, *, check: bool = False) -> subprocess.CompletedProcess[str]:
    """Run a command suppressing stdout and stderr."""
    return subprocess.run(
        cmd, shell=True, check=check,
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, text=True,
    )


# ---------------------------------------------------------------------------
# Cluster lifecycle
# ---------------------------------------------------------------------------
def cleanup_cluster() -> None:
    log_info("Cleaning up existing k3d cluster...")
    run_quiet(f"k3d cluster delete {CLUSTER_NAME}")
    # Also clean up any orphaned registries
    run_quiet(f"docker stop k3d-{REGISTRY_NAME}")
    run_quiet(f"docker rm k3d-{REGISTRY_NAME}")


def prepull_images() -> None:
    log_info("Pre-pulling required images...")
    run(f"docker pull rancher/k3s:{K3S_VERSION}", check=False)
    run("docker pull ghcr.io/k3d-io/k3d-proxy:5.8.3", check=False)
    run("docker pull alpine:latest", check=False)


def create_cluster() -> None:
    log_info(f"Creating k3d cluster '{CLUSTER_NAME}' with k3s {K3S_VERSION}...")
    run(
        f"k3d cluster create {CLUSTER_NAME}"
        f" --servers 1"
        f" --agents 2"
        f" --image rancher/k3s:{K3S_VERSION}"
        f" --registry-create {REGISTRY_NAME}:0.0.0.0:{REGISTRY_PORT}"
        f" --port 6550:6443@loadbalancer"
        f" --wait"
    )
    log_success("Cluster created successfully")
    run("kubectl cluster-info")
    run("kubectl get nodes")


# ---------------------------------------------------------------------------
# Fake GPU operator
# ---------------------------------------------------------------------------
def install_fake_gpu_operator() -> None:
    log_info("Installing fake GPU operator with ComputeDomain support...")

    log_info("Cleaning up any conflicting resources...")
    run_quiet("kubectl delete runtimeclass nvidia")
    run_quiet("helm uninstall fake-gpu-operator -n gpu-operator")

    run(
        "helm upgrade -i fake-gpu-operator"
        " oci://ghcr.io/run-ai/fake-gpu-operator/fake-gpu-operator"
        " --namespace gpu-operator"
        " --create-namespace"
        " --devel"
        " --version 0.0.72"
        " --set computeDomainDraPlugin.enabled=true"
        " --wait"
    )

    log_info("Waiting for fake GPU operator to be ready...")
    run(
        "kubectl wait --for=condition=Ready pods"
        " -l app.kubernetes.io/name=fake-gpu-operator"
        " -n gpu-operator --timeout=120s",
        check=False,
    )

    result = run_quiet("kubectl get crd computedomains.resource.nvidia.com")
    if result.returncode == 0:
        log_success("ComputeDomain CRD installed")
    else:
        log_error("ComputeDomain CRD not found!")
        sys.exit(1)


def uninstall_fake_gpu_operator() -> None:
    log_info("Ensuring fake GPU operator is not installed...")
    run_quiet("helm uninstall fake-gpu-operator -n gpu-operator")
    run_quiet("kubectl delete runtimeclass nvidia")
    run_quiet("kubectl delete crd computedomains.resource.nvidia.com")
    log_success("Fake GPU operator removed (if it existed)")


# ---------------------------------------------------------------------------
# Build / push images
# ---------------------------------------------------------------------------
def build_and_push_images() -> None:
    """Build Grove images with skaffold/ko and push to the local k3d registry.

    Uses the same approach as the main e2e cluster setup (create-e2e-cluster.py):
    skaffold build with the ko builder compiles Go binaries into OCI images and
    pushes them directly to the registry -- no Docker build required.
    """
    log_info("Building and pushing Grove operator images with skaffold...")
    run("./hack/prepare-charts.sh", cwd=OPERATOR_DIR)

    push_repo = f"localhost:{REGISTRY_PORT}"
    build_date = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    env = {
        "VERSION": IMAGE_TAG,
        "LD_FLAGS": (
            "-X github.com/ai-dynamo/grove/operator/internal/version.gitCommit=e2e-test-commit "
            "-X github.com/ai-dynamo/grove/operator/internal/version.gitTreeState=clean "
            f"-X github.com/ai-dynamo/grove/operator/internal/version.buildDate={build_date} "
            "-X github.com/ai-dynamo/grove/operator/internal/version.gitVersion=mnnvl-e2e"
        ),
    }

    log_info(f"Running skaffold build (push to {push_repo})...")
    run(
        f"skaffold build --default-repo {push_repo}",
        cwd=OPERATOR_DIR,
        env=env,
    )

    log_success("Grove images built and pushed to registry")

    # Push alpine for test workloads (simple pull/tag/push, not a build)
    log_info("Pushing alpine image for test workloads...")
    run("docker pull alpine:latest", check=False)
    run(f"docker tag alpine:latest localhost:{REGISTRY_PORT}/alpine:latest")
    run(f"docker push localhost:{REGISTRY_PORT}/alpine:latest")

    log_success("All images pushed to registry")


# ---------------------------------------------------------------------------
# Grove operator
# ---------------------------------------------------------------------------
def install_grove_operator(mnnvl_flag: bool, *, skip_operator_wait: bool,
                           custom_image_tag: str) -> None:
    mnnvl_str = str(mnnvl_flag).lower()
    log_info(f"Installing Grove operator (MNNVL={mnnvl_str})...")

    image_repo = f"{REGISTRY_NAME}:{REGISTRY_PORT}/grove-operator"
    image_tag = IMAGE_TAG
    initc_image = f"{REGISTRY_NAME}:{REGISTRY_PORT}/grove-initc:{IMAGE_TAG}"

    if custom_image_tag:
        image_tag = custom_image_tag
        image_repo = "grove-operator"
        initc_image = f"grove-initc:{custom_image_tag}"

    # Uninstall existing if present
    run_quiet(f"helm uninstall grove-operator -n {GROVE_NAMESPACE}")
    # Wait for old pods to be fully removed before installing the new deployment;
    # otherwise kubectl wait may match both old (terminating) and new pods.
    run_quiet(
        f"kubectl wait --for=delete pods -l app.kubernetes.io/name=grove-operator"
        f" -n {GROVE_NAMESPACE} --timeout=60s"
    )

    run(
        f"helm install grove-operator ./charts"
        f" --namespace {GROVE_NAMESPACE}"
        f" --create-namespace"
        f' --set "image.repository={image_repo}"'
        f' --set "image.tag={image_tag}"'
        f' --set "deployment.env[0].name=GROVE_INIT_CONTAINER_IMAGE"'
        f' --set "deployment.env[0].value={initc_image}"'
        f' --set "config.network.autoMNNVLEnabled={mnnvl_str}"'
        f' --set "config.leaderElection.enabled=false"'
        f' --set "replicaCount=1"',
        cwd=OPERATOR_DIR,
    )

    if skip_operator_wait:
        log_warning("Skipping operator readiness check (--skip-operator-wait)")
        time.sleep(5)
    else:
        log_info("Waiting for Grove operator to be ready...")
        run(
            f"kubectl wait --for=condition=Ready pods"
            f" -l app.kubernetes.io/name=grove-operator"
            f" -n {GROVE_NAMESPACE} --timeout=120s"
        )

    # Verify MNNVL configuration
    log_info("Verifying MNNVL configuration...")
    result = run(
        f"kubectl get configmap -n {GROVE_NAMESPACE}"
        f" -l app.kubernetes.io/component=operator-configmap"
        r" -o jsonpath='{.items[0].data.config\.yaml}'",
        check=False,
        capture=True,
    )
    config_value = result.stdout if result.returncode == 0 else ""

    if f"autoMNNVLEnabled: {mnnvl_str}" in config_value:
        log_success(f"MNNVL is set to {mnnvl_str}")
    else:
        log_warning("Could not verify MNNVL setting")

    # Show operator logs
    log_info("Operator startup logs:")
    run(
        f"kubectl logs -n {GROVE_NAMESPACE} deployment/grove-operator --tail=20",
        check=False,
    )


# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Set up a k3d cluster for MNNVL e2e testing.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )

    parser.add_argument("--skip-cluster-create", action="store_true", default=False,
                        help="Reuse existing cluster")
    parser.add_argument("--shutdown", action="store_true", default=False,
                        help="Delete cluster and exit (no other args)")

    gpu_group = parser.add_mutually_exclusive_group()
    gpu_group.add_argument("--with-fake-gpu", action="store_true", default=None,
                           help="Install fake GPU operator (default)")
    gpu_group.add_argument("--without-fake-gpu", action="store_true", default=None,
                           help="Skip fake GPU operator installation")

    mnnvl_group = parser.add_mutually_exclusive_group()
    mnnvl_group.add_argument("--mnnvl-enabled", action="store_true", default=None,
                             help="Enable MNNVL feature in Grove (default)")
    mnnvl_group.add_argument("--mnnvl-disabled", action="store_true", default=None,
                             help="Disable MNNVL feature in Grove")

    build_group = parser.add_mutually_exclusive_group()
    build_group.add_argument("--build", action="store_true", default=None,
                             help="Build images with skaffold/ko (default)")
    build_group.add_argument("--skip-build", action="store_true", default=None,
                             help="Skip image build, reuse images already in registry")
    build_group.add_argument("--image", metavar="TAG", default=None,
                             help="Use existing image with specified tag")

    parser.add_argument("--skip-operator-wait", action="store_true", default=False,
                        help="Don't wait for operator pod readiness")

    return parser.parse_args()


def resolve_flags(args: argparse.Namespace) -> dict:
    """Resolve parsed args into the effective configuration dict."""
    # --shutdown must be the only meaningful argument
    if args.shutdown:
        other_flags = (
            args.skip_cluster_create
            or args.with_fake_gpu
            or args.without_fake_gpu
            or args.mnnvl_enabled
            or args.mnnvl_disabled
            or args.build
            or args.skip_build
            or args.image is not None
            or args.skip_operator_wait
        )
        if other_flags:
            log_error("Conflicting arguments: --shutdown must be the only argument")
            sys.exit(1)

    # Defaults when nothing explicit was given
    install_fake_gpu = True
    if args.without_fake_gpu:
        install_fake_gpu = False

    mnnvl_enabled = True
    if args.mnnvl_disabled:
        mnnvl_enabled = False

    do_build = True
    custom_image_tag = ""
    if args.skip_build:
        do_build = False
    elif args.image is not None:
        do_build = False
        custom_image_tag = args.image

    return {
        "shutdown": bool(args.shutdown),
        "skip_cluster_create": bool(args.skip_cluster_create),
        "install_fake_gpu": install_fake_gpu,
        "mnnvl_enabled": mnnvl_enabled,
        "build_images": do_build,
        "skip_operator_wait": bool(args.skip_operator_wait),
        "custom_image_tag": custom_image_tag,
    }


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
def print_config(cfg: dict) -> None:
    log_info("==========================================")
    log_info("MNNVL Cluster Setup")
    log_info("==========================================")
    log_info("Configuration:")
    log_info(f"  Skip cluster create: {cfg['skip_cluster_create']}")
    log_info(f"  Install fake GPU:    {cfg['install_fake_gpu']}")
    log_info(f"  MNNVL enabled:       {cfg['mnnvl_enabled']}")
    log_info(f"  Build images:        {cfg['build_images']}")
    log_info(f"  Skip operator wait:  {cfg['skip_operator_wait']}")
    if cfg["custom_image_tag"]:
        log_info(f"  Custom image tag:    {cfg['custom_image_tag']}")
    log_info("==========================================")


def main() -> None:
    args = parse_args()
    cfg = resolve_flags(args)

    # Handle --shutdown early
    if cfg["shutdown"]:
        cleanup_cluster()
        return

    print_config(cfg)

    os.chdir(OPERATOR_DIR)

    # Step 1: Cluster creation
    if not cfg["skip_cluster_create"]:
        cleanup_cluster()
        prepull_images()
        create_cluster()
    else:
        log_warning("Skipping cluster creation (using existing cluster)")

    # Step 2: Fake GPU operator
    if cfg["install_fake_gpu"]:
        install_fake_gpu_operator()
    else:
        uninstall_fake_gpu_operator()

    # Step 3: Build and push images with skaffold/ko
    # Skaffold builds Go binaries with ko and pushes directly to the k3d registry.
    if cfg["build_images"]:
        build_and_push_images()
    else:
        log_warning("Skipping image build (images already in registry)")

    # Step 4: Install Grove operator
    install_grove_operator(
        cfg["mnnvl_enabled"],
        skip_operator_wait=cfg["skip_operator_wait"],
        custom_image_tag=cfg["custom_image_tag"],
    )

    log_info("==========================================")
    log_success("Cluster setup complete!")
    log_info(f"  Fake GPU:      {cfg['install_fake_gpu']}")
    log_info(f"  MNNVL:         {cfg['mnnvl_enabled']}")
    log_info("==========================================")


if __name__ == "__main__":
    main()
