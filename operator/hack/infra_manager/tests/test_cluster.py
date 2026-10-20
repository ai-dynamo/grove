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

"""Cluster lifecycle and setup wiring tests without Docker or Kubernetes."""

from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock

import pytest
import typer
import yaml
from typer.testing import CliRunner

from infra_manager import cluster, orchestrator
from infra_manager.commands import delete_cmd
from infra_manager.config import ClusterConfig, SetupConfig


@pytest.mark.parametrize("was_enabled", [False, True])
def test_create_cluster(monkeypatch, tmp_path, was_enabled):
    """Feature gates are opt-in; registry aliases and kubeconfig stay cluster-local."""
    kubeconfig = tmp_path / "task" / "kubeconfig"
    monkeypatch.setenv("KUBECONFIG", str(kubeconfig))
    calls = []
    registry_configs = []

    def k3d(*args):
        calls.append(args)
        if args[:2] == ("cluster", "create"):
            registry_path = Path(args[args.index("--registry-config") + 1])
            registry_configs.append(yaml.safe_load(registry_path.read_text()))
        elif args[:2] == ("kubeconfig", "merge"):
            Path(args[args.index("-o") + 1]).touch()

    monkeypatch.setattr(cluster, "sh", SimpleNamespace(k3d=k3d))
    cfg = ClusterConfig(name="was-test", registry_port=15001, max_retries=1)
    cluster.create_cluster(cfg, was_enabled)

    create = calls[1]
    assert calls[0] == ("cluster", "delete", cfg.name)
    assert create[:3] == ("cluster", "create", cfg.name)
    assert "--kubeconfig-update-default=false" in create
    assert "--kubeconfig-switch-context=false" in create
    assert create[create.index("--registry-create") + 1] == "was-test-registry:0.0.0.0:15001"
    assert registry_configs == [{"mirrors": {
        alias: {"endpoint": ["http://was-test-registry:5000"]}
        for alias in ["registry:5001", "registry:15001"]
    }}]
    assert calls[2] == (
        "kubeconfig", "merge", cfg.name, "-o", str(kubeconfig), "--kubeconfig-switch-context"
    )
    assert kubeconfig.stat().st_mode & 0o777 == 0o600
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


def test_create_cluster_rejects_kubeconfig_list(monkeypatch):
    """Reject ambiguous output paths before deleting or creating any cluster."""
    monkeypatch.setenv("KUBECONFIG", "/tmp/one:/tmp/two")
    k3d = Mock()
    monkeypatch.setattr(cluster, "sh", SimpleNamespace(k3d=k3d))
    with pytest.raises(ValueError, match="single file"):
        cluster.create_cluster(ClusterConfig())
    k3d.assert_not_called()


def test_was_setup(monkeypatch):
    """The config switch reaches both the control plane and the operator deployment."""
    cfg = SetupConfig(
        scheduler={"was": {"enabled": True}, "kai": {"enabled": False}},
        kwok={"nodes": 0},
    )
    create = Mock()
    deploy = Mock()
    pull = Mock()
    monkeypatch.setattr(orchestrator, "_check_prerequisites", Mock())
    monkeypatch.setattr(orchestrator, "create_cluster", create)
    monkeypatch.setattr(orchestrator, "wait_for_nodes", Mock())
    monkeypatch.setattr(orchestrator, "apply_topology_labels", Mock())
    monkeypatch.setattr(orchestrator, "deploy_grove_operator", deploy)
    monkeypatch.setattr(orchestrator, "prepull_image_groups", pull)
    orchestrator.run_setup(cfg)
    create.assert_called_once_with(cfg.cluster, True)
    deploy.assert_called_once_with(cfg.grove, cfg.cluster, orchestrator.OPERATOR_DIR, False, True)
    assert pull.call_args.args == (
        [(orchestrator.dep_value("test_images", "busybox"), "latest")],
        cfg.cluster.registry_port,
    )


def test_delete_cluster_uses_configured_name(monkeypatch):
    """Cleanup must target the same cluster name as setup, with CLI taking precedence."""
    monkeypatch.setenv("E2E_CLUSTER__NAME", "was-test")
    delete = Mock()
    monkeypatch.setattr(delete_cmd, "delete_cluster", delete)
    app = typer.Typer()
    app.add_typer(delete_cmd.app, name="delete")
    runner = CliRunner()
    result = runner.invoke(app, ["delete", "k3d-cluster"])
    assert result.exit_code == 0, result.output
    assert delete.call_args.args[0].name == "was-test"
    result = runner.invoke(app, ["delete", "k3d-cluster", "--cluster-name", "explicit-test"])
    assert result.exit_code == 0, result.output
    assert delete.call_args.args[0].name == "explicit-test"
