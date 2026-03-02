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

"""Constants, dependency loading, and dep_value helper."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import yaml

# -- Resolved paths --
SCRIPT_DIR = Path(__file__).resolve().parent.parent
OPERATOR_DIR = SCRIPT_DIR.parent


def load_dependencies() -> dict:
    """Load dependency versions and images from dependencies.yaml.

    Returns:
        Parsed YAML content as a nested dictionary.
    """
    deps_file = Path(__file__).resolve().parent / "dependencies.yaml"
    with open(deps_file) as f:
        return yaml.safe_load(f)


DEPENDENCIES = load_dependencies()


def dep_value(*keys: str, default: Any = None) -> Any:
    """Safely traverse the DEPENDENCIES dict by key path.

    Args:
        *keys: Sequence of dictionary keys to traverse.
        default: Value to return if any key is missing.

    Returns:
        The value at the nested key path, or *default* if not found.
    """
    node = DEPENDENCIES
    for key in keys:
        if not isinstance(node, dict):
            return default
        node = node.get(key)
        if node is None:
            return default
    return node


CLUSTER_TIMEOUT = "120s"
NODES_PER_ZONE = 28
NODES_PER_BLOCK = 14
NODES_PER_RACK = 7

WEBHOOK_READY_MAX_RETRIES = 60
WEBHOOK_READY_POLL_INTERVAL_SECONDS = 5

KAI_QUEUE_MAX_RETRIES = 12
KAI_QUEUE_POLL_INTERVAL_SECONDS = 5

CLUSTER_CREATE_RETRY_WAIT_SECONDS = 10

NODE_CONDITIONS = [
    {"type": "Ready", "status": "True", "reason": "KubeletReady", "message": "kubelet is posting ready status"},
    {
        "type": "MemoryPressure",
        "status": "False",
        "reason": "KubeletHasSufficientMemory",
        "message": "kubelet has sufficient memory available",
    },
    {
        "type": "DiskPressure",
        "status": "False",
        "reason": "KubeletHasNoDiskPressure",
        "message": "kubelet has no disk pressure",
    },
    {
        "type": "PIDPressure",
        "status": "False",
        "reason": "KubeletHasSufficientPID",
        "message": "kubelet has sufficient PID available",
    },
    {
        "type": "NetworkUnavailable",
        "status": "False",
        "reason": "RouteCreated",
        "message": "RouteController created a route",
    },
]

E2E_NODE_ROLE_KEY = "node_role.e2e.grove.nvidia.com"
KAI_SCHEDULER_OCI = "oci://ghcr.io/nvidia/kai-scheduler/kai-scheduler"
GROVE_OPERATOR_IMAGE = "grove-operator"
GROVE_INITC_IMAGE = "grove-initc"
GROVE_MODULE_PATH = "github.com/ai-dynamo/grove/operator/internal/version"
KWOK_ANNOTATION_KEY = "kwok.x-k8s.io/node"
KWOK_FAKE_NODE_TAINT_KEY = "fake-node"
WEBHOOK_READY_KEYWORDS = ["validated", "denied", "error", "invalid", "created", "podcliqueset"]

# -- Namespaces --
NS_KAI_SCHEDULER = "kai-scheduler"
NS_KUBE_SYSTEM = "kube-system"
NS_DEFAULT = "default"

# -- Helm releases --
HELM_RELEASE_KAI = "kai-scheduler"
HELM_RELEASE_GROVE = "grove-operator"
HELM_RELEASE_PYROSCOPE = "pyroscope"

# -- Helm repos --
HELM_REPO_GRAFANA = "grafana"
HELM_REPO_GRAFANA_URL = "https://grafana.github.io/helm-charts"
HELM_CHART_PYROSCOPE = "grafana/pyroscope"

# -- Topology labels --
LABEL_ZONE = "kubernetes.io/zone"
LABEL_BLOCK = "kubernetes.io/block"
LABEL_RACK = "kubernetes.io/rack"
LABEL_HOSTNAME = "kubernetes.io/hostname"
LABEL_TYPE = "type"
LABEL_TYPE_KWOK = "kwok"
LABEL_CONTROL_PLANE = "node-role.kubernetes.io/control-plane"

# -- Relative paths --
REL_WORKLOAD_YAML = "e2e/yaml/workload1.yaml"
REL_QUEUES_YAML = "e2e/yaml/queues.yaml"
REL_PREPARE_CHARTS = "hack/prepare-charts.sh"
REL_CHARTS_DIR = "charts"

# -- KWOK --
KWOK_GITHUB_REPO = "kubernetes-sigs/kwok"
KWOK_MANIFESTS = ("kwok.yaml", "stage-fast.yaml")
KWOK_CONTROLLER_DEPLOYMENT = "kwok-controller"
KWOK_IP_PREFIX = "10.0"
KWOK_IP_OCTET_SIZE = 256

# -- Helm override keys --
HELM_KEY_PROFILING = "config.debugging.enableProfiling"
HELM_KEY_PPROF_BIND_ADDRESS = "config.debugging.pprofBindAddress"
HELM_KEY_PCS_SYNCS = "config.controllers.podCliqueSet.concurrentSyncs"
HELM_KEY_PCLQ_SYNCS = "config.controllers.podClique.concurrentSyncs"
HELM_KEY_PCSG_SYNCS = "config.controllers.podCliqueScalingGroup.concurrentSyncs"

# -- K3d cluster defaults --
DEFAULT_CLUSTER_NAME = "shared-e2e-test-cluster"
DEFAULT_REGISTRY_PORT = 5001
DEFAULT_API_PORT = 6560
DEFAULT_LB_PORT = "8090:80"
DEFAULT_WORKER_NODES = 30
DEFAULT_WORKER_MEMORY = "150m"
DEFAULT_K3S_IMAGE = "rancher/k3s:v1.33.5-k3s1"
DEFAULT_CLUSTER_CREATE_MAX_RETRIES = 3

# -- Scale test cluster defaults --
DEFAULT_SCALE_WORKER_NODES = 5
DEFAULT_SCALE_WORKER_MEMORY = "300m"

# -- Component defaults --
DEFAULT_SKAFFOLD_PROFILE = "topology-test"
DEFAULT_GROVE_NAMESPACE = "grove-system"

# -- KWOK defaults --
DEFAULT_KWOK_VERSION = "v0.7.0"
DEFAULT_KWOK_BATCH_SIZE = 150
DEFAULT_KWOK_NODE_CPU = "64"
DEFAULT_KWOK_NODE_MEMORY = "512Gi"
DEFAULT_KWOK_MAX_PODS = 110
DEFAULT_PYROSCOPE_NAMESPACE = "pyroscope"

# -- E2E build metadata --
E2E_TEST_VERSION = "E2E_TESTS"
E2E_TEST_COMMIT = "e2e-test-commit"
E2E_TEST_TREE_STATE = "clean"

# -- Parallelism & limits --
DEFAULT_IMAGE_PULL_MAX_WORKERS = 5
