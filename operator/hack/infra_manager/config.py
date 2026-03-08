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

from pathlib import Path

import yaml
from pydantic import BaseModel, ConfigDict, Field, model_validator
from pydantic_settings import BaseSettings, PydanticBaseSettingsSource, SettingsConfigDict

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
    worker_nodes: int = Field(default=DEFAULT_WORKER_NODES, ge=1, le=100)  # Real k3d agent nodes only (KWOK nodes are separate)
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

    kai_version: str = Field(default=DEPENDENCIES["kai_scheduler"]["version"], pattern=r"^v[\d.]+(-[\w.]+)?$")
    skaffold_profile: str = DEFAULT_SKAFFOLD_PROFILE
    grove_namespace: str = DEFAULT_GROVE_NAMESPACE
    registry: str | None = None


class KwokConfig(BaseSettings):
    """KWOK configuration, auto-loaded from E2E_* env vars.

    Attributes:
        kwok_nodes: Number of KWOK nodes to create, or None to skip.
        kwok_batch_size: Number of nodes to create per kubectl apply batch.
        kwok_node_cpu: CPU capacity to advertise per KWOK node.
        kwok_node_memory: Memory capacity to advertise per KWOK node.
        kwok_max_pods: Maximum pods per KWOK node.
    """

    model_config = SettingsConfigDict(env_prefix="E2E_", extra="ignore")

    kwok_nodes: int | None = None
    kwok_batch_size: int = Field(default=DEFAULT_KWOK_BATCH_SIZE, ge=1)
    kwok_node_cpu: str = DEFAULT_KWOK_NODE_CPU
    kwok_node_memory: str = DEFAULT_KWOK_NODE_MEMORY
    kwok_max_pods: int = DEFAULT_KWOK_MAX_PODS


class ClusterParams(BaseModel):
    """Cluster sizing parameters for ClusterSetup.config.

    Attributes:
        worker_nodes: Number of k3d worker nodes.
        worker_memory: Memory limit per worker node.
    """

    model_config = ConfigDict(extra="forbid")

    worker_nodes: int = Field(default=DEFAULT_WORKER_NODES, ge=1, le=100)
    worker_memory: str = Field(default=DEFAULT_WORKER_MEMORY, pattern=r"^\d+[mMgG]?$")


class GroveParams(BaseModel):
    """Grove operator concurrent sync overrides for GroveSetup.config.

    Attributes:
        pcs_syncs: PodCliqueSet concurrent syncs override, or None.
        pclq_syncs: PodClique concurrent syncs override, or None.
        pcsg_syncs: PodCliqueScalingGroup concurrent syncs override, or None.
    """

    model_config = ConfigDict(extra="forbid")

    pcs_syncs: int | None = None
    pclq_syncs: int | None = None
    pcsg_syncs: int | None = None


class KaiSetup(BaseModel):
    """Kai Scheduler component options.

    Attributes:
        enabled: Install Kai Scheduler.
    """

    model_config = ConfigDict(extra="forbid")

    enabled: bool = True


class GroveSetup(BaseModel):
    """Grove operator component options.

    Attributes:
        enabled: Deploy Grove operator.
        profiling: Enable pprof on the Grove operator.
        config: Concurrent sync overrides.
    """

    model_config = ConfigDict(extra="forbid")

    enabled: bool = True
    profiling: bool = False
    config: GroveParams = GroveParams()


class ClusterSetup(BaseModel):
    """k3d cluster group: creation flag, registry/prepull options, and sizing config.

    Attributes:
        create: Whether to create the k3d cluster.
        prepull_images: Pre-pull images to the local k3d registry. Mutex with registry.
        registry: External container registry URL override. Mutex with prepull_images.
        config: Cluster sizing parameters.
    """

    model_config = ConfigDict(extra="forbid")

    create: bool = True
    prepull_images: bool = True
    registry: str | None = None
    config: ClusterParams = ClusterParams()

    @model_validator(mode="after")
    def _registry_prepull_mutex(self) -> "ClusterSetup":
        """Raise if both registry and prepull_images are set."""
        if self.registry is not None and self.prepull_images:
            raise ValueError("registry and prepull_images are mutually exclusive")
        return self


class KwokSetup(BaseModel):
    """KWOK simulated nodes group.

    Attributes:
        nodes: KWOK simulated node count; 0 disables KWOK.
    """

    model_config = ConfigDict(extra="forbid")

    nodes: int = Field(default=0, ge=0)


class PyroscopeParams(BaseModel):
    """Pyroscope installation parameters for PyroscopeSetup.config.

    Attributes:
        namespace: Kubernetes namespace for Pyroscope installation.
    """

    model_config = ConfigDict(extra="forbid")

    namespace: str = DEFAULT_PYROSCOPE_NAMESPACE


class PyroscopeSetup(BaseModel):
    """Pyroscope profiler component options.

    Attributes:
        enabled: Install Pyroscope profiler.
        config: Pyroscope installation parameters.
    """

    model_config = ConfigDict(extra="forbid")

    enabled: bool = False
    config: PyroscopeParams = PyroscopeParams()


class SetupConfig(BaseSettings):
    """Unified setup configuration: YAML preset + E2E_* env var overrides + CLI flags.

    Env var format uses double-underscore delimiter:
      E2E_CLUSTER__CREATE=false
      E2E_CLUSTER__CONFIG__WORKER_NODES=10
      E2E_CLUSTER__REGISTRY=myregistry.io
      E2E_GROVE__PROFILING=true
      E2E_KWOK__NODES=500
      E2E_PYROSCOPE__ENABLED=true

    Fine-grained infra settings (k3s_image, cluster_name, ports, kai_version,
    kwok node resources, etc.) remain configurable via flat E2E_* env vars on
    the underlying K3dConfig/ComponentConfig/KwokConfig.

    Attributes:
    cluster: k3d cluster creation, registry/prepull, and sizing config.
        kai: Kai Scheduler component options.
        grove: Grove operator component options.
        kwok: KWOK simulated nodes config.
        pyroscope: Pyroscope profiler options.
    """

    model_config = SettingsConfigDict(env_prefix="E2E_", env_nested_delimiter="__", extra="forbid")

    cluster: ClusterSetup = ClusterSetup()
    kai: KaiSetup = KaiSetup()
    grove: GroveSetup = GroveSetup()
    kwok: KwokSetup = KwokSetup()
    pyroscope: PyroscopeSetup = PyroscopeSetup()

    @classmethod
    def settings_customise_sources(
        cls,
        settings_cls: type[BaseSettings],
        init_settings: PydanticBaseSettingsSource,
        env_settings: PydanticBaseSettingsSource,
        dotenv_settings: PydanticBaseSettingsSource,
        file_secret_settings: PydanticBaseSettingsSource,
    ) -> tuple[PydanticBaseSettingsSource, ...]:
        """Override source priority: env vars > YAML (init kwargs).

        Default pydantic-settings priority is init > env. We reverse this so
        that E2E_* env vars override values loaded from the YAML file.
        """
        return (env_settings, init_settings)


def _deep_merge(base: dict, override: dict) -> dict:
    """Recursively merge override into base dict, preserving unmentioned keys.

    Args:
        base: Base dictionary (not mutated).
        override: Values to merge in; nested dicts are merged recursively.

    Returns:
        New merged dictionary.
    """
    result = base.copy()
    for key, val in override.items():
        if key in result and isinstance(result[key], dict) and isinstance(val, dict):
            result[key] = _deep_merge(result[key], val)
        else:
            result[key] = val
    return result


def load_setup_config(path: Path) -> SetupConfig:
    """Load a SetupConfig from a YAML file with optional override YAML.

    Precedence (low → high):
      1. Base YAML (path)
      3. E2E_*__* env vars (applied automatically via SetupConfig.settings_customise_sources)
      4. CLI flags (caller's responsibility via model_copy)

    Sub-models use extra="forbid" — unknown YAML keys raise immediately.

    Args:
        path: Path to the base YAML config file (e.g. presets/e2e.yaml).

    Returns:
        Validated SetupConfig with overrides and env var values applied.

    Raises:
        FileNotFoundError: If config file does not exist.
        ValidationError: If the merged YAML contains invalid or unknown fields.
    """
    with open(path, encoding="utf-8") as f:
        data = yaml.safe_load(f) or {}
    return SetupConfig(**data)
