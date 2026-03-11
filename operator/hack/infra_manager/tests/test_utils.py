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

"""Unit tests for infra_manager.utils: helm override collection."""

from infra_manager.config import GroveConfig
from infra_manager.utils import collect_grove_helm_overrides


def test_collect_grove_helm_overrides_qps_burst():
    """qps and burst fields produce the correct helm override strings."""
    overrides = collect_grove_helm_overrides(GroveConfig(qps=200.0, burst=500))

    assert any("config.runtimeClientConnection.qps=200.0" in o for o in overrides)
    assert any("config.runtimeClientConnection.burst=500" in o for o in overrides)


def test_collect_grove_helm_overrides_qps_burst_none():
    """qps and burst are omitted when not set."""
    overrides = collect_grove_helm_overrides(GroveConfig())

    assert not any("runtimeClientConnection.qps" in o for o in overrides)
    assert not any("runtimeClientConnection.burst" in o for o in overrides)
