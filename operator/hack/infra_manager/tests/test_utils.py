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

"""Unit tests for infra_manager.utils: helm override collection."""

import json

from infra_manager.config import GroveConfig
from infra_manager.utils import collect_grove_helm_overrides


def test_collect_grove_helm_overrides_qps_burst():
    """qps and burst fields produce the correct --set helm override tuples."""
    overrides = collect_grove_helm_overrides(GroveConfig(qps=200.0, burst=500))

    assert any("config.runtimeClientConnection.qps=200.0" in val for _, val in overrides)
    assert any("config.runtimeClientConnection.burst=500" in val for _, val in overrides)
    assert all(flag == "--set" for flag, val in overrides if "runtimeClientConnection" in val)


def test_collect_grove_helm_overrides_qps_burst_none():
    """qps and burst are omitted when not set."""
    overrides = collect_grove_helm_overrides(GroveConfig())

    assert not any("runtimeClientConnection.qps" in val for _, val in overrides)
    assert not any("runtimeClientConnection.burst" in val for _, val in overrides)


def test_collect_grove_helm_overrides_annotation_set_string():
    """Pyroscope annotation overrides use --set-string to preserve string types."""
    overrides = collect_grove_helm_overrides(GroveConfig(profiling=True))

    ann = [(flag, val) for flag, val in overrides if "podAnnotations." in val]
    assert ann, "expected at least one annotation override"
    assert all(flag == "--set-string" for flag, _ in ann)


def test_collect_grove_helm_overrides_was():
    """Enable WAS even when the skaffold profile would otherwise default to Kai."""
    overrides = collect_grove_helm_overrides(GroveConfig(), was_enabled=True)
    profiles = [
        json.loads(value.split("=", 1)[1])
        for flag, value in overrides
        if flag == "--set-json" and value.startswith("config.scheduler.profiles=")
    ]
    assert profiles == [[{"name": "default-scheduler", "config": {"gangScheduling": True}}]]
    assert ("--set", "config.scheduler.defaultProfileName=default-scheduler") in overrides
    assert ("--set", "config.topologyAwareScheduling.enabled=true") in overrides
    assert not any("config.scheduler.profiles[" in value for _, value in overrides)


def test_collect_grove_helm_overrides_without_was():
    """The default setup must not silently opt in to native gang scheduling."""
    assert collect_grove_helm_overrides(GroveConfig()) == []


def test_collect_grove_helm_overrides_kueue():
    """Kueue retains the existing scheduler profiles without enabling WAS."""
    assert collect_grove_helm_overrides(GroveConfig(), kueue_enabled=True) == [
        ("--set", "config.scheduler.profiles[0].name=default-scheduler"),
        ("--set", "config.scheduler.profiles[1].name=kai-scheduler"),
        ("--set", "config.scheduler.profiles[2].name=kueue"),
    ]
