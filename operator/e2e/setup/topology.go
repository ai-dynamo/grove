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
	"context"
	"fmt"
	"sort"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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

	// NodesPerZone is the number of nodes per zone.
	NodesPerZone = 28
	// NodesPerBlock is the number of nodes per block (28 / 2 blocks).
	NodesPerBlock = 14
	// NodesPerRack is the number of nodes per rack (28 / 4 racks).
	NodesPerRack = 7
)

// GetZoneForNodeIndex returns the zone label for a given node index.
// Both the index parameter and the returned zone number are 0-based.
// e.g., nodes 0-27 → zone-0, nodes 28-55 → zone-1, etc.
func GetZoneForNodeIndex(idx int) string {
	zoneNum := idx / NodesPerZone
	return fmt.Sprintf("zone-%d", zoneNum)
}

// GetBlockForNodeIndex returns the block label for a given node index.
// Both the index parameter and the returned block number are 0-based.
// e.g., nodes 0-13 → block-0, nodes 14-27 → block-1
func GetBlockForNodeIndex(idx int) string {
	blockNum := idx / NodesPerBlock
	return fmt.Sprintf("block-%d", blockNum)
}

// GetRackForNodeIndex returns the rack label for a given node index.
// Both the index parameter and the returned rack number are 0-based.
// e.g., nodes 0-6 → rack-0, nodes 7-13 → rack-1, etc.
func GetRackForNodeIndex(idx int) string {
	rackNum := idx / NodesPerRack
	return fmt.Sprintf("rack-%d", rackNum)
}

// GetWorkerNodeLabelSelector returns the label selector for worker nodes in e2e tests.
// Returns a formatted string "key=value" for use with Kubernetes label selectors.
func GetWorkerNodeLabelSelector() string {
	return fmt.Sprintf("%s=%s", WorkerNodeLabelKey, WorkerNodeLabelValue)
}
