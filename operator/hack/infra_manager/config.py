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

"""Configuration classes and config models."""

from __future__ import annotations

from dataclasses import dataclass

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict
from infra_manager.constants import (
    DEFAULT_API_PORT,
    DEFAULT_CLUSTER_CREATE_MAX_RETRIES,
    DEFAULT_CLUSTER_NAME,
    DEFAULT_GROVE_NAMESPACE,
    DEFAULT_K3S_IMAGE,
    DEFAULT_KWOK_BATCH_SIZE,
    DEFAULT_KWOK_MAX_PODS,
    DEFAULT_KWOK_NODE_CPU,
    DEFAULT_KWOK_NODE_MEMORY,
    DEFAULT_LB_PORT,
    DEFAULT_PYROSCOPE_NAMESPACE,
    DEFAULT_REGISTRY_PORT,
    DEFAULT_SKAFFOLD_PROFILE,
    DEFAULT_WORKER_MEMORY,
    DEFAULT_WORKER_NODES,
    DEPENDENCIES,
)


# ============================================================================
# Configuration classes
# ============================================================================

class K3dConfig(BaseSettings):
    """k3d cluster configuration, auto-loaded from E2E_* env vars.

    Attributes:
        cluster_name: Name of the k3d cluster.
        registry_port: Port for the local container registry.
        api_port: Kubernetes API server port.
        lb_port: Load balancer port mapping (host:container).
        worker_nodes: Number of worker nodes to create.
        worker_memory: Memory limit per worker node.
        k3s_image: K3s Docker image to use.
        max_retries: Maximum cluster creation retry attempts.
    """

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    cluster_name: str = DEFAULT_CLUSTER_NAME
    registry_port: int = Field(default=DEFAULT_REGISTRY_PORT, ge=1, le=65535)
    api_port: int = Field(default=DEFAULT_API_PORT, ge=1, le=65535)
    lb_port: str = DEFAULT_LB_PORT
    worker_nodes: int = Field(default=DEFAULT_WORKER_NODES, ge=1, le=100)
    worker_memory: str = Field(default=DEFAULT_WORKER_MEMORY, pattern=r"^\d+[mMgG]?$")
    k3s_image: str = DEFAULT_K3S_IMAGE
    max_retries: int = Field(default=DEFAULT_CLUSTER_CREATE_MAX_RETRIES, ge=1, le=10)


class ComponentConfig(BaseSettings):
    """Component versions and registry, auto-loaded from E2E_* env vars.

    Attributes:
        kai_version: Kai Scheduler Helm chart version.
        skaffold_profile: Skaffold profile for Grove deployment.
        grove_namespace: Kubernetes namespace for Grove operator.
        registry: Container registry URL override, or None for k3d local.
    """

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    kai_version: str = Field(default=DEPENDENCIES['kai_scheduler']['version'],
                             pattern=r"^v[\d.]+(-[\w.]+)?$")
    skaffold_profile: str = DEFAULT_SKAFFOLD_PROFILE
    grove_namespace: str = DEFAULT_GROVE_NAMESPACE
    registry: str | None = None


class KwokConfig(BaseSettings):
    """KWOK and Pyroscope configuration, auto-loaded from E2E_* env vars.

    Attributes:
        kwok_nodes: Number of KWOK nodes to create, or None to skip.
        kwok_batch_size: Number of nodes to create per kubectl apply batch.
        kwok_node_cpu: CPU capacity to advertise per KWOK node.
        kwok_node_memory: Memory capacity to advertise per KWOK node.
        kwok_max_pods: Maximum pods per KWOK node.
        pyroscope_ns: Kubernetes namespace for Pyroscope installation.
    """

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    kwok_nodes: int | None = None
    kwok_batch_size: int = Field(default=DEFAULT_KWOK_BATCH_SIZE, ge=1)
    kwok_node_cpu: str = DEFAULT_KWOK_NODE_CPU
    kwok_node_memory: str = DEFAULT_KWOK_NODE_MEMORY
    kwok_max_pods: int = DEFAULT_KWOK_MAX_PODS
    pyroscope_ns: str = DEFAULT_PYROSCOPE_NAMESPACE


# ============================================================================
# Grove install options
# ============================================================================

@dataclass(frozen=True)
class GroveInstallOptions:
    """Options for deploying the Grove operator.

    Attributes:
        registry: Container registry URL override, or None for k3d local.
        grove_profiling: Whether to enable pprof on Grove.
        grove_pcs_syncs: PodCliqueSet concurrent syncs override, or None.
        grove_pclq_syncs: PodClique concurrent syncs override, or None.
        grove_pcsg_syncs: PodCliqueScalingGroup concurrent syncs override, or None.
    """

    registry: str | None = None
    grove_profiling: bool = False
    grove_pcs_syncs: int | None = None
    grove_pclq_syncs: int | None = None
    grove_pcsg_syncs: int | None = None
