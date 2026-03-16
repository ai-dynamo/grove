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

import logging
from pathlib import Path
from typing import Literal

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
    dep_value,
)

logger = logging.getLogger(__name__)


class ClusterConfig(BaseModel):
    """k3d cluster lifecycle: creation, registry, sizing, and retry config.

    Attributes:
        create: Whether to create the k3d cluster.
        prepull_images: Pre-pull images to the local k3d registry. Mutex with registry.
        registry: External container registry URL. Mutex with prepull_images.
        name: Name of the k3d cluster.
        registry_port: Port for the local container registry.
        api_port: Kubernetes API server port.
        lb_port: Load balancer port mapping (host:container).
        k3s_image: K3s Docker image to use.
        max_retries: Maximum cluster creation retry attempts.
        worker_nodes: Number of worker nodes to create.
        worker_memory: Memory limit per worker node.
        dind_memory_mode: Use kubelet system-reserved instead of --agents-memory (for DinD
            environments where --agents-memory is broken due to /proc/meminfo bind-mount).
    """

    model_config = ConfigDict(extra="forbid")

    create: bool = True
    prepull_images: bool = True
    registry: str | None = None
    name: str = DEFAULT_CLUSTER_NAME
    registry_port: int = Field(default=DEFAULT_REGISTRY_PORT, ge=1, le=65535)
    api_port: int = Field(default=DEFAULT_API_PORT, ge=1, le=65535)
    lb_port: str = DEFAULT_LB_PORT
    k3s_image: str = DEFAULT_K3S_IMAGE
    max_retries: int = Field(default=DEFAULT_CLUSTER_CREATE_MAX_RETRIES, ge=1, le=10)
    worker_nodes: int = Field(default=DEFAULT_WORKER_NODES, ge=1, le=100)
    worker_memory: str = Field(default=DEFAULT_WORKER_MEMORY, pattern=r"^\d+[mMgG]?$")
    dind_memory_mode: bool = False

    @model_validator(mode="after")
    def _registry_prepull_mutex(self) -> "ClusterConfig":
        """Raise if both registry and prepull_images are set."""
        if self.registry is not None and self.prepull_images:
            raise ValueError("registry and prepull_images are mutually exclusive")
        return self


class LocalBuildConfig(BaseModel):
    """Local skaffold build settings.

    Attributes:
        skaffold_profile: Skaffold profile for Grove deployment.
    """

    model_config = ConfigDict(extra="forbid")

    skaffold_profile: str = DEFAULT_SKAFFOLD_PROFILE


class ImageDeployConfig(BaseModel):
    """Existing image deployment settings (placeholder for future image-specific settings)."""

    model_config = ConfigDict(extra="forbid")


class KaiConfig(BaseModel):
    """Kai Scheduler settings.

    Attributes:
        enabled: Install Kai Scheduler.
        version: Kai Scheduler Helm chart version.
    """

    model_config = ConfigDict(extra="forbid")

    enabled: bool = True
    version: str = Field(default=dep_value("kai_scheduler", "version", default="v0.0.0"), pattern=r"^v[\d.]+(-[\w.]+)?$")


class SchedulerConfig(BaseModel):
    """Extensible scheduler backend group.

    Attributes:
        kai: Kai Scheduler settings.
    """

    model_config = ConfigDict(extra="forbid")

    kai: KaiConfig = KaiConfig()


class GroveConfig(BaseModel):
    """Grove operator settings: behavior, deployment mode, and namespace.

    Attributes:
        enabled: Deploy Grove operator.
        profiling: Enable pprof on the Grove operator.
        pcs_syncs: PodCliqueSet concurrent syncs override, or None.
        pclq_syncs: PodClique concurrent syncs override, or None.
        pcsg_syncs: PodCliqueScalingGroup concurrent syncs override, or None.
        qps: Kubernetes client QPS override, or None (uses helm chart default).
        burst: Kubernetes client burst override, or None (uses helm chart default).
        namespace: Kubernetes namespace for Grove operator.
        mode: Deployment mode — "local" (skaffold build) or "image" (existing image).
        local: Local skaffold build settings.
        image: Existing image deployment settings.
    """

    model_config = ConfigDict(extra="forbid")

    enabled: bool = True
    profiling: bool = False
    pcs_syncs: int | None = None
    pclq_syncs: int | None = None
    pcsg_syncs: int | None = None
    qps: float | None = None
    burst: int | None = None
    namespace: str = DEFAULT_GROVE_NAMESPACE
    mode: Literal["local", "image"] = "local"
    local: LocalBuildConfig = LocalBuildConfig()
    image: ImageDeployConfig = ImageDeployConfig()


class KwokConfig(BaseModel):
    """KWOK simulated nodes configuration.

    Attributes:
        nodes: Number of KWOK nodes to create; 0 disables KWOK.
        batch_size: Number of nodes to create per kubectl apply batch.
        node_cpu: CPU capacity to advertise per KWOK node.
        node_memory: Memory capacity to advertise per KWOK node.
        max_pods: Maximum pods per KWOK node.
    """

    model_config = ConfigDict(extra="forbid")

    nodes: int = Field(default=0, ge=0)
    batch_size: int = Field(default=DEFAULT_KWOK_BATCH_SIZE, ge=1)
    node_cpu: str = DEFAULT_KWOK_NODE_CPU
    node_memory: str = DEFAULT_KWOK_NODE_MEMORY
    max_pods: int = DEFAULT_KWOK_MAX_PODS


class PyroscopeConfig(BaseModel):
    """Pyroscope profiler configuration.

    Attributes:
        enabled: Install Pyroscope profiler.
        namespace: Kubernetes namespace for Pyroscope installation.
    """

    model_config = ConfigDict(extra="forbid")

    enabled: bool = False
    namespace: str = DEFAULT_PYROSCOPE_NAMESPACE


class SetupConfig(BaseSettings):
    """Unified setup configuration: YAML preset + E2E_*__* env var overrides + CLI flags.

    Env var format uses double-underscore delimiter:
      E2E_CLUSTER__CREATE=false
      E2E_CLUSTER__WORKER_NODES=10
      E2E_CLUSTER__REGISTRY=myregistry.io
      E2E_GROVE__PROFILING=true
      E2E_GROVE__LOCAL__SKAFFOLD_PROFILE=my-profile
      E2E_GROVE__NAMESPACE=grove-system
      E2E_SCHEDULER__KAI__VERSION=v1.2.3
      E2E_KWOK__NODES=500
      E2E_KWOK__BATCH_SIZE=100
      E2E_KWOK__NODE_CPU=64
      E2E_KWOK__NODE_MEMORY=512Gi
      E2E_KWOK__MAX_PODS=110
      E2E_PYROSCOPE__ENABLED=true

    Old env vars (E2E_CLUSTER_NAME, E2E_KAI_VERSION, etc.) are no longer supported.
    See Env Var Migration table in the plan for the full mapping.

    Attributes:
        cluster: k3d cluster creation, registry/prepull, and sizing config.
        scheduler: Scheduler backend group (kai).
        grove: Grove operator behavior and deployment config.
        kwok: KWOK simulated nodes config.
        pyroscope: Pyroscope profiler options.
    """

    model_config = SettingsConfigDict(env_prefix="E2E_", env_nested_delimiter="__", extra="forbid")

    cluster: ClusterConfig = ClusterConfig()
    scheduler: SchedulerConfig = SchedulerConfig()
    grove: GroveConfig = GroveConfig()
    kwok: KwokConfig = KwokConfig()
    pyroscope: PyroscopeConfig = PyroscopeConfig()

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
        dotenv_settings and file_secret_settings are intentionally excluded —
        we do not support .env files; all overrides go through YAML or env vars.
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


def _parse_set_override(s: str) -> dict:
    """Parse a Helm-style dot-notation override string into a nested dict.

    Splits on the first '=' only, so values containing '=' or '.' are safe.
    Key is dot-split to build a nested dict; list index syntax ([0]) is not supported.

    Args:
        s: Override string in 'key.subkey=value' format.

    Returns:
        Nested dictionary representing the override.

    Raises:
        ValueError: If the string does not contain '='.

    Examples:
        >>> _parse_set_override("cluster.worker_nodes=5")
        {"cluster": {"worker_nodes": "5"}}
        >>> _parse_set_override("scheduler.kai.version=v1.2.3")
        {"scheduler": {"kai": {"version": "v1.2.3"}}}
    """
    if "=" not in s:
        raise ValueError(f"Invalid --set override (missing '='): {s!r}")
    key, value = s.split("=", 1)
    parts = key.split(".")
    result: str | dict = value
    for part in reversed(parts):
        result = {part: result}
    return result


def load_setup_config(
    base_path: Path,
    values_paths: list[Path] | None = None,
    set_overrides: list[str] | None = None,
) -> SetupConfig:
    """Load a SetupConfig from a YAML file with optional overlay YAMLs and --set overrides.

    Precedence (low → high):
      1. Base YAML (base_path)
      2. Overlay YAMLs (values_paths, merged in order — later files win)
      3. --set overrides (set_overrides, applied last before env vars)
      4. E2E_*__* env vars (highest, applied automatically via settings_customise_sources)

    CLI named flags (e.g., --worker-nodes) are applied by the caller via model_copy
    after this function returns, giving them the highest priority.

    Sub-models use extra="forbid" — unknown YAML keys raise immediately.

    Args:
        base_path: Path to the base YAML config file (e.g. presets/e2e.yaml).
        values_paths: Optional list of overlay YAML files to merge in order.
        set_overrides: Optional list of dot-notation overrides (e.g. "cluster.worker_nodes=5").
            List index syntax ([0]) is not supported.

    Returns:
        Validated SetupConfig with all overrides and env var values applied.

    Raises:
        FileNotFoundError: If any config file does not exist.
        ValidationError: If the merged config contains invalid or unknown fields.
        ValueError: If any set_override string is malformed (missing '=').
    """
    with open(base_path, encoding="utf-8") as f:
        data = yaml.safe_load(f) or {}

    for overlay_path in values_paths or []:
        with open(overlay_path, encoding="utf-8") as f:
            overlay = yaml.safe_load(f) or {}
        data = _deep_merge(data, overlay)

    for override in set_overrides or []:
        parsed = _parse_set_override(override)
        data = _deep_merge(data, parsed)

    return SetupConfig(**data)
