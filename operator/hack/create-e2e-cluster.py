#!/usr/bin/env python3
# /*
# Copyright 2025 The Grove Authors.
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

"""
create-e2e-cluster.py - k3d cluster setup for local E2E testing

Python Dependencies: See requirements.txt
Minimum Python version: 3.8+

For detailed usage information, run: ./hack/create-e2e-cluster.py --help
"""

import json
import os
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime
from pathlib import Path
from typing import List, Optional, Tuple

import docker
import sh
import typer
from pydantic import BaseModel, Field
from rich.console import Console
from rich.panel import Panel
from rich.progress import Progress, SpinnerColumn, TextColumn, BarColumn, TaskProgressColumn

# Initialize CLI app and console
app = typer.Typer(help="k3d cluster setup for local E2E testing")
console = Console(stderr=True)


# ============================================================================
# Configuration
# ============================================================================

# Kai Scheduler images that need to be pre-pulled for faster cluster creation
KAI_SCHEDULER_IMAGES = [
    "ghcr.io/nvidia/kai-scheduler/admission",
    "ghcr.io/nvidia/kai-scheduler/binder",
    "ghcr.io/nvidia/kai-scheduler/operator",
    "ghcr.io/nvidia/kai-scheduler/podgroupcontroller",
    "ghcr.io/nvidia/kai-scheduler/podgrouper",
    "ghcr.io/nvidia/kai-scheduler/queuecontroller",
    "ghcr.io/nvidia/kai-scheduler/scheduler",
]


class ClusterConfig(BaseModel):
    """Configuration loaded from environment variables with defaults."""

    cluster_name: str = Field(default_factory=lambda: os.getenv("E2E_CLUSTER_NAME", "shared-e2e-test-cluster"))
    registry_port: str = Field(default_factory=lambda: os.getenv("E2E_REGISTRY_PORT", "5001"))
    api_port: str = Field(default_factory=lambda: os.getenv("E2E_API_PORT", "6560"))
    lb_port: str = Field(default_factory=lambda: os.getenv("E2E_LB_PORT", "8090:80"))
    worker_nodes: int = Field(default_factory=lambda: int(os.getenv("E2E_WORKER_NODES", "30")))
    worker_memory: str = Field(default_factory=lambda: os.getenv("E2E_WORKER_MEMORY", "150m"))
    k3s_image: str = Field(default_factory=lambda: os.getenv("E2E_K3S_IMAGE", "rancher/k3s:v1.33.5-k3s1"))
    kai_version: str = Field(default_factory=lambda: os.getenv("E2E_KAI_VERSION", "v0.13.0-rc1"))
    skaffold_profile: str = Field(default_factory=lambda: os.getenv("E2E_SKAFFOLD_PROFILE", "topology-test"))
    max_retries: int = Field(default_factory=lambda: int(os.getenv("E2E_MAX_RETRIES", "3")))
    cluster_timeout: str = "120s"
    nodes_per_zone: int = 28
    nodes_per_block: int = 14
    nodes_per_rack: int = 7


# ============================================================================
# Utility functions
# ============================================================================

def require_command(cmd: str):
    """Check if a command exists."""
    try:
        sh.which(cmd)
    except sh.ErrorReturnCode:
        console.print(f"[red]❌ Required command '{cmd}' not found. Please install it first.[/red]")
        raise typer.Exit(1)


def run_cmd(cmd, *args, **kwargs) -> Tuple[int, any]:
    """Run a command, handling errors gracefully. Returns (exit_code, output)."""
    try:
        output = cmd(*args, **kwargs)
        return 0, output
    except sh.ErrorReturnCode as e:
        if not kwargs.get("_ok_code"):
            raise
        return e.exit_code, e


# ============================================================================
# Image pre-pulling functions
# ============================================================================

def prepull_images(images: List[str], registry_port: str, version: str) -> None:
    """
    Pre-pull images in parallel and push them to the local k3d registry.
    This significantly speeds up cluster creation by avoiding image pulls during pod startup.
    """
    if not images:
        return

    console.print(Panel.fit("Pre-pulling images to local registry", style="bold blue"))
    console.print(f"[yellow]Pre-pulling {len(images)} images in parallel (this speeds up cluster startup)...[/yellow]")

    # Initialize Docker client
    try:
        client = docker.from_env()
    except Exception as e:
        console.print(f"[yellow]⚠️  Failed to connect to Docker: {e}[/yellow]")
        console.print("[yellow]⚠️  Skipping image pre-pull (cluster will pull images on-demand)[/yellow]")
        return

    def pull_tag_push(image_name: str) -> Tuple[str, bool, Optional[str]]:
        """Pull an image, tag it for local registry, and push it."""
        full_image = f"{image_name}:{version}"
        registry_image = f"localhost:{registry_port}/{image_name}:{version}"

        try:
            # Pull from remote registry
            client.images.pull(full_image)

            # Tag for local registry
            image = client.images.get(full_image)
            image.tag(registry_image)

            # Push to local registry
            client.images.push(registry_image, stream=False)

            return (image_name, True, None)
        except docker.errors.ImageNotFound:
            return (image_name, False, "Image not found")
        except docker.errors.APIError as e:
            return (image_name, False, f"Docker API error: {e}")
        except Exception as e:
            return (image_name, False, str(e))

    # Pull images in parallel with progress tracking
    with Progress(
        SpinnerColumn(),
        TextColumn("[progress.description]{task.description}"),
        BarColumn(),
        TaskProgressColumn(),
        console=console,
    ) as progress:
        task = progress.add_task("[cyan]Pulling images...", total=len(images))

        failed_images = []
        with ThreadPoolExecutor(max_workers=5) as executor:
            futures = {executor.submit(pull_tag_push, img): img for img in images}

            for future in as_completed(futures):
                image_name, success, error = future.result()
                progress.advance(task)

                if success:
                    console.print(f"[green]✓ {image_name}[/green]")
                else:
                    console.print(f"[red]✗ {image_name} - {error}[/red]")
                    failed_images.append(image_name)

    if failed_images:
        console.print(f"[yellow]⚠️  Failed to pre-pull {len(failed_images)} images[/yellow]")
        console.print("[yellow]   Cluster will pull these images on-demand (may be slower)[/yellow]")
    else:
        console.print(f"[green]✅ Successfully pre-pulled all {len(images)} images[/green]")


# ============================================================================
# Cluster operations
# ============================================================================

def delete_cluster(config: ClusterConfig):
    """Delete the k3d cluster."""
    console.print(f"[yellow]ℹ️  Deleting k3d cluster '{config.cluster_name}'...[/yellow]")
    exit_code, _ = run_cmd(sh.k3d, "cluster", "delete", config.cluster_name, _ok_code=[0, 1])
    if exit_code == 0:
        console.print(f"[green]✅ Cluster '{config.cluster_name}' deleted[/green]")
    else:
        console.print(f"[yellow]⚠️  Cluster '{config.cluster_name}' not found or already deleted[/yellow]")


def create_cluster(config: ClusterConfig) -> bool:
    """Create a k3d cluster with retry logic."""
    console.print(Panel.fit("Creating k3d cluster", style="bold blue"))

    console.print("[yellow]Configuration:[/yellow]")
    for key, value in config.model_dump().items():
        if key not in ['nodes_per_zone', 'nodes_per_block', 'nodes_per_rack', 'cluster_timeout', 'max_retries']:
            console.print(f"  {key:20s}: {value}")

    for attempt in range(1, config.max_retries + 1):
        console.print(f"[yellow]ℹ️  Cluster creation attempt {attempt} of {config.max_retries}...[/yellow]")

        # Clean up any partial cluster
        run_cmd(sh.k3d, "cluster", "delete", config.cluster_name, _ok_code=[0, 1])

        # Create cluster
        exit_code, _ = run_cmd(
            sh.k3d, "cluster", "create", config.cluster_name,
            "--servers", "1",
            "--agents", str(config.worker_nodes),
            "--image", config.k3s_image,
            "--api-port", config.api_port,
            "--port", f"{config.lb_port}@loadbalancer",
            "--registry-create", f"registry:0.0.0.0:{config.registry_port}",
            "--k3s-arg", "--node-taint=node_role.e2e.grove.nvidia.com=agent:NoSchedule@agent:*",
            "--k3s-node-label", "node_role.e2e.grove.nvidia.com=agent@agent:*",
            "--k3s-node-label", "nvidia.com/gpu.deploy.operands=false@server:*",
            "--k3s-node-label", "nvidia.com/gpu.deploy.operands=false@agent:*",
            "--agents-memory", config.worker_memory,
            "--timeout", config.cluster_timeout,
            "--wait",
            _ok_code=[0, 1]
        )

        if exit_code == 0:
            console.print(f"[green]✅ Cluster created successfully on attempt {attempt}[/green]")
            return True

        if attempt < config.max_retries:
            console.print("[yellow]⚠️  Cluster creation failed, retrying in 10 seconds...[/yellow]")
            time.sleep(10)

    console.print(f"[red]❌ Cluster creation failed after {config.max_retries} attempts[/red]")
    return False


def wait_for_nodes():
    """Wait for all nodes to be ready."""
    console.print("[yellow]ℹ️  Waiting for all nodes to be ready...[/yellow]")
    sh.kubectl("wait", "--for=condition=Ready", "nodes", "--all", "--timeout=5m")
    console.print("[green]✅ All nodes are ready[/green]")


def install_kai_scheduler(config: ClusterConfig):
    """Install Kai Scheduler using Helm."""
    console.print(Panel.fit("Installing Kai Scheduler", style="bold blue"))
    console.print(f"[yellow]Version: {config.kai_version}[/yellow]")

    # Delete existing installation (ignore errors)
    run_cmd(sh.helm, "uninstall", "kai-scheduler", "-n", "kai-scheduler", _ok_code=[0, 1])

    sh.helm(
        "install", "kai-scheduler",
        f"oci://ghcr.io/nvidia/kai-scheduler/kai-scheduler",
        "--version", config.kai_version,
        "--namespace", "kai-scheduler",
        "--create-namespace",
        "--set", "global.tolerations[0].key=node-role.kubernetes.io/control-plane",
        "--set", "global.tolerations[0].operator=Exists",
        "--set", "global.tolerations[0].effect=NoSchedule",
        "--set", "global.tolerations[1].key=node_role.e2e.grove.nvidia.com",
        "--set", "global.tolerations[1].operator=Equal",
        "--set", "global.tolerations[1].value=agent",
        "--set", "global.tolerations[1].effect=NoSchedule"
    )
    console.print("[green]✅ Kai Scheduler installed[/green]")


def deploy_grove_operator(config: ClusterConfig, operator_dir: Path):
    """Deploy Grove operator using Skaffold."""
    console.print(Panel.fit("Deploying Grove operator", style="bold blue"))

    # Delete existing installation (ignore errors)
    run_cmd(sh.helm, "uninstall", "grove-operator", "-n", "grove-system", _ok_code=[0, 1])

    # Set environment for skaffold build
    build_date = datetime.utcnow().strftime("%Y-%m-%dT%H:%M:%SZ")
    os.environ.update({
        "VERSION": "E2E_TESTS",
        "LD_FLAGS": (
            "-X github.com/ai-dynamo/grove/operator/internal/version.gitCommit=e2e-test-commit "
            "-X github.com/ai-dynamo/grove/operator/internal/version.gitTreeState=clean "
            f"-X github.com/ai-dynamo/grove/operator/internal/version.buildDate={build_date} "
            "-X github.com/ai-dynamo/grove/operator/internal/version.gitVersion=E2E_TESTS"
        )
    })

    push_repo = f"localhost:{config.registry_port}"
    pull_repo = f"registry:{config.registry_port}"

    console.print(f"[yellow]ℹ️  Building images (push to {push_repo})...[/yellow]")

    # Build images
    build_output = json.loads(
        sh.skaffold(
            "build",
            "--default-repo", push_repo,
            "--profile", config.skaffold_profile,
            "--quiet",
            "--output={{json .}}",
            _cwd=str(operator_dir)
        )
    )

    # Parse image tags
    images = {}
    for build in build_output.get("builds", []):
        name = build["imageName"]
        images[name] = build["tag"].replace(push_repo, pull_repo)

    console.print("[yellow]Deploying with images:[/yellow]")
    for name, tag in images.items():
        console.print(f"  {name}={tag}")

    os.environ["CONTAINER_REGISTRY"] = pull_repo

    # Deploy
    sh.skaffold(
        "deploy",
        "--profile", config.skaffold_profile,
        "--namespace", "grove-system",
        "--status-check=false",
        "--default-repo=",
        "--images", f"grove-operator={images['grove-operator']}",
        "--images", f"grove-initc={images['grove-initc']}",
        _cwd=str(operator_dir)
    )

    console.print("[green]✅ Grove operator deployed[/green]")

    # Wait for Grove pods
    console.print("[yellow]ℹ️  Waiting for Grove pods to be ready...[/yellow]")
    sh.kubectl("wait", "--for=condition=Ready", "pods", "--all", "-n", "grove-system", "--timeout=5m")

    # Wait for webhook
    console.print("[yellow]ℹ️  Waiting for Grove webhook to be ready...[/yellow]")
    for i in range(1, 61):
        exit_code, result = run_cmd(
            sh.kubectl, "create", "-f", str(operator_dir / "e2e/yaml/workload1.yaml"),
            "--dry-run=server", "-n", "default",
            _ok_code=[0, 1]
        )

        # Get output text - handle both string and ErrorReturnCode
        if isinstance(result, str):
            output = result.lower()
        else:
            output = (str(result.stdout) + str(result.stderr)).lower()

        if any(kw in output for kw in ["validated", "denied", "error", "invalid", "created", "podcliqueset"]):
            console.print("[green]✅ Grove webhook is ready[/green]")
            break

        if i == 60:
            console.print(f"[red]❌ Timed out waiting for Grove webhook[/red]")
            console.print(f"Last response: {output}")
            raise typer.Exit(1)

        console.print(f"[yellow]Webhook not ready yet, retrying in 5s... ({i}/60)[/yellow]")
        time.sleep(5)


def apply_topology_labels(config: ClusterConfig):
    """Apply topology labels to worker nodes."""
    console.print(Panel.fit("Applying topology labels to worker nodes", style="bold blue"))

    # Get worker nodes sorted by name
    nodes_output = sh.kubectl(
        "get", "nodes",
        "-l", "node_role.e2e.grove.nvidia.com=agent",
        "-o", "jsonpath={.items[*].metadata.name}"
    ).strip()

    worker_nodes = sorted(nodes_output.split())

    for idx, node in enumerate(worker_nodes):
        zone = idx // config.nodes_per_zone
        block = idx // config.nodes_per_block
        rack = idx // config.nodes_per_rack

        sh.kubectl(
            "label", "node", node,
            f"kubernetes.io/zone=zone-{zone}",
            f"kubernetes.io/block=block-{block}",
            f"kubernetes.io/rack=rack-{rack}",
            "--overwrite"
        )

    console.print(f"[green]✅ Applied topology labels to {len(worker_nodes)} worker nodes[/green]")


# ============================================================================
# CLI Commands
# ============================================================================

@app.command()
def main(
    skip_kai: bool = typer.Option(False, "--skip-kai", help="Skip Kai Scheduler installation"),
    skip_grove: bool = typer.Option(False, "--skip-grove", help="Skip Grove operator deployment"),
    skip_topology: bool = typer.Option(False, "--skip-topology", help="Skip topology label application"),
    skip_prepull: bool = typer.Option(False, "--skip-prepull", help="Skip image pre-pulling (faster but cluster startup will be slower)"),
    delete: bool = typer.Option(False, "--delete", help="Delete the cluster and exit"),
):
    """
    Create and configure a k3d cluster for Grove E2E tests.

    This script creates a k3d cluster with Grove operator, Kai scheduler,
    and required topology configuration for E2E testing.

    Image pre-pulling speeds up cluster creation by pulling Kai Scheduler images
    in parallel and pushing them to the local registry before installation.

    Environment variables can override defaults (see E2E_* variables).
    """

    config = ClusterConfig()
    script_dir = Path(__file__).resolve().parent
    operator_dir = script_dir.parent

    # Handle delete mode
    if delete:
        delete_cluster(config)
        return

    # Check prerequisites
    console.print(Panel.fit("Checking prerequisites", style="bold blue"))
    for cmd in ["k3d", "kubectl", "docker"]:
        require_command(cmd)
    if not skip_kai:
        require_command("helm")
    if not skip_grove:
        for cmd in ["skaffold", "jq"]:
            require_command(cmd)
    console.print("[green]✅ All required tools are available[/green]")

    # Prepare charts
    if not skip_grove:
        console.print(Panel.fit("Preparing Helm charts", style="bold blue"))
        prepare_charts = operator_dir / "hack/prepare-charts.sh"
        if prepare_charts.exists():
            sh.bash(str(prepare_charts))
            console.print("[green]✅ Charts prepared[/green]")

    # Create cluster
    if not create_cluster(config):
        raise typer.Exit(1)

    wait_for_nodes()

    # Pre-pull images if not skipped (before installing Kai)
    if not skip_kai and not skip_prepull:
        prepull_images(KAI_SCHEDULER_IMAGES, config.registry_port, config.kai_version)

    # Install components
    if not skip_kai:
        install_kai_scheduler(config)

    if not skip_grove:
        deploy_grove_operator(config, operator_dir)

    # Wait for Kai and apply queues
    if not skip_kai:
        console.print("[yellow]ℹ️  Waiting for Kai Scheduler pods to be ready...[/yellow]")
        sh.kubectl("wait", "--for=condition=Ready", "pods", "--all", "-n", "kai-scheduler", "--timeout=5m")

        console.print("[yellow]ℹ️  Creating default Kai queues...[/yellow]")
        sh.kubectl("apply", "-f", str(operator_dir / "e2e/yaml/queues.yaml"))

    # Apply topology
    if not skip_topology:
        apply_topology_labels(config)

    # Print success message
    console.print(Panel.fit("Cluster setup complete!", style="bold green"))
    console.print("[yellow]To run E2E tests against this cluster:[/yellow]")
    console.print(f"\n  export E2E_REGISTRY_PORT={config.registry_port}")
    console.print("  make test-e2e")
    console.print("  make test-e2e TEST_PATTERN=Test_GS  # specific tests\n")
    console.print(f"[green]✅ Cluster '{config.cluster_name}' is ready for E2E testing![/green]")


if __name__ == "__main__":
    app()
