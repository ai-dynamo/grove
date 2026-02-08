// /*
// Copyright 2025 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package setup

import (
	"fmt"
)

const (
	// WorkerNodeLabelKey is the label key used to identify worker nodes in e2e tests.
	// This can be changed if infrastructure changes.
	WorkerNodeLabelKey = "node_role.e2e.grove.nvidia.com"
	// WorkerNodeLabelValue is the label value for worker node identification in e2e tests.
	WorkerNodeLabelValue = "agent"

	// TopologyLabelZone is the Kubernetes label key for zone topology domain.
	TopologyLabelZone = "kubernetes.io/zone"
	// TopologyLabelBlock is the Kubernetes label key for the block topology domain.
	TopologyLabelBlock = "kubernetes.io/block"
	// TopologyLabelRack is the Kubernetes label key for the rack topology domain.
	TopologyLabelRack = "kubernetes.io/rack"
	// TopologyLabelHostname is the Kubernetes label key for the hostname topology domain.
	TopologyLabelHostname = "kubernetes.io/hostname"
)

// GetWorkerNodeLabelSelector returns the label selector for worker nodes in e2e tests.
// Returns a formatted string "key=value" for use with Kubernetes label selectors.
func GetWorkerNodeLabelSelector() string {
	return fmt.Sprintf("%s=%s", WorkerNodeLabelKey, WorkerNodeLabelValue)
}
