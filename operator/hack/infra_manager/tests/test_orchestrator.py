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

"""Scheduler image pre-pull selection must respect both independent switches."""

from unittest.mock import patch

import pytest

from infra_manager import orchestrator


@pytest.mark.parametrize(
    "kai_enabled,kueue_enabled",
    [(False, False), (True, False), (False, True), (True, True)],
    ids=["neither", "kai", "kueue", "both"],
)
def test_prepull_scheduler_images(kai_enabled, kueue_enabled):
    deps = orchestrator.DEPENDENCIES
    expected = [(deps["cert_manager"]["images"], deps["cert_manager"]["version"])]
    if kai_enabled:
        expected.insert(0, (deps["kai_scheduler"]["images"], deps["kai_scheduler"]["version"]))
    if kueue_enabled:
        expected.append((deps["kueue"]["images"], f"v{deps['kueue']['version']}"))
    expected.append((["test/busybox"], "latest"))

    with patch.object(orchestrator, "dep_value", return_value=["test/busybox"]), \
         patch.object(orchestrator, "prepull_image_groups") as prepull:
        orchestrator._run_prepull(5001, kai_enabled, kueue_enabled)

    prepull.assert_called_once_with(expected, 5001)
