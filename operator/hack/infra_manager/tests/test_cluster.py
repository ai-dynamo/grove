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

"""WAS feature-gate and setup wiring tests without Docker or Kubernetes."""

from types import SimpleNamespace
from unittest.mock import Mock

import pytest

from infra_manager import cluster, grove, orchestrator
from infra_manager.config import ClusterConfig, GroveConfig, SetupConfig


@pytest.mark.parametrize("was_enabled", [False, True])
def test_create_cluster_was(monkeypatch, was_enabled):
    """Native scheduling APIs and control-plane feature gates are opt-in."""
    calls = []

    def k3d(*args):
        calls.append(args)

    monkeypatch.setattr(cluster, "sh", SimpleNamespace(k3d=k3d))
    cfg = ClusterConfig(name="was-test", max_retries=1, was_enabled=was_enabled)
    cluster.create_cluster(cfg)

    create = calls[1]
    assert calls[0] == ("cluster", "delete", cfg.name)
    assert create[:3] == ("cluster", "create", cfg.name)
    gates = [arg for arg in create if isinstance(arg, str) and "feature-gates=" in arg]
    runtime_config = [arg for arg in create if isinstance(arg, str) and "runtime-config=" in arg]
    if was_enabled:
        assert set(gates) == {
            f"--{component}-arg=feature-gates=GenericWorkload=true,CompositePodGroup=true,TopologyAwareWorkloadScheduling=true@server:*"
            for component in ["kube-apiserver", "kube-scheduler"]
        } | {"--kube-controller-manager-arg=feature-gates=GenericWorkload=true@server:*"}
        assert runtime_config == [
            "--kube-apiserver-arg=runtime-config=scheduling.k8s.io/v1beta1=true,scheduling.k8s.io/v1alpha3=true@server:*"
        ]
    else:
        assert gates == []
        assert runtime_config == []


@pytest.mark.parametrize("create_cluster", [False, True])
@pytest.mark.parametrize("was_enabled", [False, True])
def test_was_setup(monkeypatch, create_cluster, was_enabled):
    """The cluster switch also configures Grove when reusing an existing cluster."""
    cfg = SetupConfig(
        cluster={"create": create_cluster, "was_enabled": was_enabled},
        scheduler={"kai": {"enabled": False}},
        kwok={"nodes": 0},
    )
    create = Mock()
    deploy = Mock(wraps=grove.deploy_grove_operator)
    overrides = Mock(return_value=[])
    monkeypatch.setattr(orchestrator, "_check_prerequisites", Mock())
    monkeypatch.setattr(orchestrator, "create_cluster", create)
    monkeypatch.setattr(orchestrator, "wait_for_nodes", Mock())
    monkeypatch.setattr(orchestrator, "apply_topology_labels", Mock())
    monkeypatch.setattr(orchestrator, "deploy_grove_operator", deploy)
    monkeypatch.setattr(orchestrator, "_run_prepull", Mock())
    monkeypatch.setattr(orchestrator, "_run_kubeconfig_merge", Mock())
    monkeypatch.setattr(grove, "sh", SimpleNamespace(helm=Mock(), kubectl=Mock()))
    monkeypatch.setattr(grove, "os", SimpleNamespace(environ={}))
    monkeypatch.setattr(grove, "_build_grove_images", Mock(return_value={}))
    monkeypatch.setattr(grove, "_deploy_grove_charts", Mock())
    monkeypatch.setattr(grove, "collect_grove_helm_overrides", overrides)
    monkeypatch.setattr(grove, "_apply_grove_helm_overrides", Mock())
    monkeypatch.setattr(grove, "_wait_grove_webhook", Mock())
    orchestrator.run_setup(cfg)
    deploy.assert_called_once_with(cfg.grove, cfg.cluster, orchestrator.OPERATOR_DIR, False)
    overrides.assert_called_once_with(cfg.grove, False, was_enabled)
    if create_cluster:
        create.assert_called_once_with(cfg.cluster)
    else:
        create.assert_not_called()


def test_deploy_grove_rejects_was_with_kueue(monkeypatch, tmp_path):
    """Reject unsupported setup combinations before changing the Grove deployment."""
    helm = Mock()
    build = Mock()
    monkeypatch.setattr(grove, "sh", SimpleNamespace(helm=helm))
    monkeypatch.setattr(grove, "_build_grove_images", build)
    with pytest.raises(ValueError, match="WAS and Kueue cannot both be enabled in this test setup"):
        grove.deploy_grove_operator(GroveConfig(), ClusterConfig(was_enabled=True), tmp_path, kueue_enabled=True)
    helm.assert_not_called()
    build.assert_not_called()
