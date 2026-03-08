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

"""Composite setup subcommand."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import typer

from infra_manager.config import SetupConfig, load_setup_config
from infra_manager.orchestrator import run_setup

_PRESETS_DIR = Path(__file__).resolve().parent.parent / "presets"


def _non_none(**kwargs: Any) -> dict[str, Any]:
    """Return a dict containing only the kwargs whose values are not None.

    Args:
        **kwargs: Arbitrary keyword arguments to filter.

    Returns:
        Filtered dictionary with None values removed.
    """
    return {k: v for k, v in kwargs.items() if v is not None}


def _apply_updates(
    cfg: SetupConfig,
    field: str,
    top: dict[str, Any],
    nested: dict[str, Any] | None = None,
) -> SetupConfig:
    """Apply top-level and optional nested config updates to a SetupConfig field.

    If both top and nested are empty, returns cfg unchanged.

    Args:
        cfg: Current setup config.
        field: Name of the top-level SetupConfig field to update.
        top: Updates for the field's top-level attributes.
        nested: Updates for the field's nested .config sub-model, if any.

    Returns:
        Updated SetupConfig with the field replaced.
    """
    if not top and not nested:
        return cfg
    component = getattr(cfg, field)
    if nested:
        top["config"] = component.config.model_copy(update=nested)
    # model_validate re-runs validators (e.g. mutex checks) that model_copy skips
    updated = type(component).model_validate(component.model_copy(update=top).model_dump())
    return cfg.model_copy(update={field: updated})


def setup(
    config: Path = typer.Option(None, "--config", help="Path to setup config YAML (default: presets/e2e.yaml)"),
    override: Path = typer.Option(None, "--override", help="Optional YAML with values to merge on top of --config"),
    # cluster group
    create_cluster: bool | None = typer.Option(
        None, "--create-cluster/--no-create-cluster", help="Override cluster creation"
    ),
    worker_nodes: int | None = typer.Option(None, "--workers", help="Override k3d worker node count"),
    worker_memory: str | None = typer.Option(None, "--worker-memory", help="Override memory per worker"),
    registry: str | None = typer.Option(None, "--registry", help="Override container registry URL"),
    prepull_images: bool | None = typer.Option(
        None, "--prepull-images/--no-prepull-images", help="Override image pre-pulling"
    ),
    # kai group
    install_kai: bool | None = typer.Option(
        None, "--install-kai/--no-install-kai", help="Override Kai installation"
    ),
    # grove group
    install_grove: bool | None = typer.Option(
        None, "--install-grove/--no-install-grove", help="Override Grove deployment"
    ),
    grove_profiling: bool | None = typer.Option(
        None, "--grove-profiling/--no-grove-profiling", help="Override Grove pprof"
    ),
    grove_pcs_syncs: int | None = typer.Option(
        None, "--grove-pcs-syncs", help="Override PodCliqueSet concurrentSyncs"
    ),
    grove_pclq_syncs: int | None = typer.Option(
        None, "--grove-pclq-syncs", help="Override PodClique concurrentSyncs"
    ),
    grove_pcsg_syncs: int | None = typer.Option(
        None, "--grove-pcsg-syncs", help="Override PodCliqueScalingGroup concurrentSyncs"
    ),
    # kwok group
    kwok_nodes: int | None = typer.Option(None, "--kwok-nodes", help="Override KWOK node count (0 disables KWOK)"),
    # pyroscope group
    install_pyroscope: bool | None = typer.Option(
        None, "--install-pyroscope/--no-install-pyroscope", help="Override Pyroscope installation"
    ),
    pyroscope_ns: str | None = typer.Option(None, "--pyroscope-namespace", help="Override Pyroscope namespace"),
) -> None:
    """Run setup workflow from a YAML config file, with optional CLI overrides.

    Defaults to the e2e preset. Use --config presets/scale.yaml for scale testing.
    Use --override my.yaml to apply partial overrides on top of the base preset.
    E2E_* env vars override YAML values; CLI flags override everything.
    """
    config_path = config if config is not None else _PRESETS_DIR / "e2e.yaml"
    cfg = load_setup_config(config_path, override_path=override)

    cfg = _apply_updates(cfg, "cluster",
        _non_none(create=create_cluster, prepull_images=prepull_images, registry=registry),
        _non_none(worker_nodes=worker_nodes, worker_memory=worker_memory))
    cfg = _apply_updates(cfg, "kai",
        _non_none(enabled=install_kai))
    cfg = _apply_updates(cfg, "grove",
        _non_none(enabled=install_grove, profiling=grove_profiling),
        _non_none(pcs_syncs=grove_pcs_syncs, pclq_syncs=grove_pclq_syncs, pcsg_syncs=grove_pcsg_syncs))
    cfg = _apply_updates(cfg, "kwok",
        _non_none(nodes=kwok_nodes))
    cfg = _apply_updates(cfg, "pyroscope",
        _non_none(enabled=install_pyroscope),
        _non_none(namespace=pyroscope_ns))

    run_setup(cfg)
