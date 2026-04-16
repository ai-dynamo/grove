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

"""Unit tests for infra_manager.config: flat models, deep merge, set overrides, and overlay precedence."""

from pathlib import Path

import pytest
from pydantic import ValidationError

from infra_manager.config import (
    ClusterConfig,
    GroveConfig,
    _deep_merge,
    _parse_set_override,
    load_setup_config,
)

_HACK_DIR = Path(__file__).resolve().parent.parent.parent
_E2E_YAML = _HACK_DIR / "e2e.yaml"
_SCALE_YAML = _HACK_DIR / "scale.yaml"


# ---------------------------------------------------------------------------
# _deep_merge
# ---------------------------------------------------------------------------


def test_deep_merge_nested():
    """Nested override wins; unmentioned base keys survive; top-level replacement works."""
    base = {"a": {"x": 1, "y": 2}, "b": 10}
    override = {"a": {"x": 99}, "b": 20}
    result = _deep_merge(base, override)

    assert result["a"]["x"] == 99
    assert result["a"]["y"] == 2
    assert result["b"] == 20
    # base must not be mutated
    assert base == {"a": {"x": 1, "y": 2}, "b": 10}
    assert base["a"] == {"x": 1, "y": 2}


# ---------------------------------------------------------------------------
# _parse_set_override — parametrized happy-path cases
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "input_str,expected",
    [
        (
            "cluster.worker_nodes=5",
            {"cluster": {"worker_nodes": "5"}},
        ),
        (
            "scheduler.kai.version=v1.2.3",
            {"scheduler": {"kai": {"version": "v1.2.3"}}},
        ),
        (
            "cluster.registry=myregistry.io/myproject",
            {"cluster": {"registry": "myregistry.io/myproject"}},
        ),
    ],
)
def test_parse_set_override(input_str, expected):
    """Dot-notation key is split into nested dict; value after first '=' is kept verbatim."""
    assert _parse_set_override(input_str) == expected


def test_parse_set_override_missing_equals():
    """Missing '=' raises ValueError."""
    with pytest.raises(ValueError, match="missing '='"):
        _parse_set_override("cluster.worker_nodes")


# ---------------------------------------------------------------------------
# load_setup_config — YAML presets
# ---------------------------------------------------------------------------


def test_load_config_e2e_defaults():
    """e2e.yaml values load correctly; unset fields use model defaults."""
    cfg = load_setup_config(_E2E_YAML)

    assert cfg.cluster.worker_nodes == 30
    assert cfg.scheduler.kai.enabled is True
    assert cfg.grove.enabled is True
    assert cfg.grove.profiling is False
    assert cfg.kwok.nodes == 0
    assert cfg.pyroscope.enabled is False


def test_load_config_scale():
    """scale.yaml values load correctly including kwok and pyroscope sections."""
    cfg = load_setup_config(_SCALE_YAML)

    assert cfg.cluster.worker_nodes == 5
    assert cfg.grove.profiling is True
    assert cfg.kwok.nodes == 100
    assert cfg.pyroscope.enabled is True


# ---------------------------------------------------------------------------
# load_setup_config — --set overrides
# ---------------------------------------------------------------------------


def test_set_override_applied():
    """--set overrides win over base YAML values."""
    cfg = load_setup_config(
        _E2E_YAML,
        set_overrides=["cluster.worker_nodes=5", "grove.profiling=true"],
    )

    assert cfg.cluster.worker_nodes == 5
    assert cfg.grove.profiling is True


def test_nested_set_override():
    """Nested --set override (scheduler.kai.enabled=false) is applied correctly."""
    cfg = load_setup_config(
        _E2E_YAML,
        set_overrides=["scheduler.kai.enabled=false"],
    )

    assert cfg.scheduler.kai.enabled is False


# ---------------------------------------------------------------------------
# load_setup_config — overlay YAML precedence
# ---------------------------------------------------------------------------


def test_overlay_yaml_precedence():
    """Overlay YAML wins over base YAML for matching keys; unrelated keys survive."""
    cfg = load_setup_config(_E2E_YAML, values_paths=[_SCALE_YAML])

    assert cfg.kwok.nodes == 100
    assert cfg.cluster.worker_nodes == 5


# ---------------------------------------------------------------------------
# ClusterConfig — mutex validator
# ---------------------------------------------------------------------------


def test_cluster_mutex_validator():
    """registry and prepull_images=True together raises ValidationError."""
    with pytest.raises(ValidationError):
        ClusterConfig(registry="myregistry.io", prepull_images=True)


# ---------------------------------------------------------------------------
# Env var priority
# ---------------------------------------------------------------------------


# ---------------------------------------------------------------------------
# GroveConfig — qps/burst defaults and overrides
# ---------------------------------------------------------------------------


def test_grove_qps_burst_defaults():
    """GroveConfig() has qps=None and burst=None by default."""
    cfg = GroveConfig()

    assert cfg.qps is None
    assert cfg.burst is None


def test_set_override_grove_qps():
    """--set grove.qps=200 is parsed as a float."""
    cfg = load_setup_config(_E2E_YAML, set_overrides=["grove.qps=200"])

    assert cfg.grove.qps == 200.0


def test_set_override_grove_burst():
    """--set grove.burst=500 is parsed as an int."""
    cfg = load_setup_config(_E2E_YAML, set_overrides=["grove.burst=500"])

    assert cfg.grove.burst == 500


# ---------------------------------------------------------------------------
# Env var priority
# ---------------------------------------------------------------------------


def test_env_var_beats_set_override(monkeypatch):
    """E2E_CLUSTER__WORKER_NODES env var wins over a --set override for the same field."""
    monkeypatch.setenv("E2E_CLUSTER__WORKER_NODES", "5")

    # Use 99 as --set value (distinct from e2e.yaml default of 30 and env var 5)
    cfg = load_setup_config(
        _E2E_YAML,
        set_overrides=["cluster.worker_nodes=99"],
    )

    assert cfg.cluster.worker_nodes == 5
