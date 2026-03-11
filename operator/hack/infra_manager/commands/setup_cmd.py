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

import typer

from infra_manager.config import load_setup_config
from infra_manager.constants import SCRIPT_DIR
from infra_manager.orchestrator import run_setup


def setup(
    config: Path = typer.Option(None, "--config", help="Path to setup config YAML"),
    values: list[Path] = typer.Option([], "-f", "--values", help="Override YAML files (stackable, merged in order)"),
    set_overrides: list[str] = typer.Option([], "--set", help="Dot-notation overrides: --set cluster.worker_nodes=5 (list index syntax not supported; env vars take priority)"),
) -> None:
    """Run setup workflow from a YAML config file, with optional overrides.

    Defaults to the e2e preset (e2e.yaml). Use --config scale.yaml for scale testing.
    Use -f my.yaml to apply partial overrides on top of the base preset (stackable).
    Use --set cluster.worker_nodes=5 for inline dot-notation overrides.
    E2E_* env vars override YAML values.
    """
    config_path = config if config is not None else SCRIPT_DIR / "e2e.yaml"
    cfg = load_setup_config(config_path, values_paths=values, set_overrides=set_overrides)
    run_setup(cfg)
