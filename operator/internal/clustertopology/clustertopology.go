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

package clustertopology

import (
	"context"
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SynchronizeTopology synchronizes scheduler-specific topology resources at operator startup.
// Lists all existing ClusterTopology resources and ensures backend topologies exist for each.
// Called before controllers start to avoid races with PCS reconciliation.
func SynchronizeTopology(ctx context.Context, cl client.Client, logger logr.Logger, backends map[string]scheduler.Backend) error {
	ctList := &grovecorev1alpha1.ClusterTopologyList{}
	if err := cl.List(ctx, ctList); err != nil {
		return fmt.Errorf("failed to list ClusterTopology resources: %w", err)
	}
	for i := range ctList.Items {
		ct := &ctList.Items[i]
		for _, b := range backends {
			tasBackend, ok := b.(scheduler.TopologyAwareSchedBackend)
			if !ok {
				logger.V(1).Info("Scheduler backend does not implement TopologyAwareSchedBackend, skipping topology sync", "backend", b.Name())
				continue
			}
			// Only sync auto-managed backends (not listed in schedulerReferences).
			// Externally-managed backends are handled by the CT controller via CheckTopologyDrift.
			if hasSchedulerReference(ct.Spec.SchedulerReferences, b.Name()) {
				continue
			}
			if err := tasBackend.SyncTopology(ctx, cl, ct); err != nil {
				return fmt.Errorf("failed to sync topology %s for backend %s: %w", ct.Name, b.Name(), err)
			}
		}
		logger.Info("Synchronized backend topologies for ClusterTopology", "name", ct.Name)
	}
	return nil
}

// hasSchedulerReference returns true if the given scheduler name is listed in the schedulerReferences.
func hasSchedulerReference(refs []grovecorev1alpha1.SchedulerReference, schedulerName string) bool {
	for _, ref := range refs {
		if ref.SchedulerName == schedulerName {
			return true
		}
	}
	return false
}

// GetClusterTopologyLevels retrieves the TopologyLevels from the specified ClusterTopology resource.
func GetClusterTopologyLevels(ctx context.Context, cl client.Client, name string) ([]grovecorev1alpha1.TopologyLevel, error) {
	clusterTopology := &grovecorev1alpha1.ClusterTopology{}
	if err := cl.Get(ctx, client.ObjectKey{Name: name}, clusterTopology); err != nil {
		return nil, err
	}
	return clusterTopology.Spec.Levels, nil
}
