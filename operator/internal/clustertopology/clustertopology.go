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
	"reflect"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SynchronizeTopology synchronizes scheduler-specific topology resources at operator startup.
// Called before controllers start to ensure backend topologies exist for all ClusterTopology resources.
// TODO(multi-topology): refactor to list all admin-created ClusterTopologies and sync each, instead of creating from config.
func SynchronizeTopology(ctx context.Context, cl client.Client, logger logr.Logger, operatorCfg *configv1alpha1.OperatorConfiguration, backends map[string]scheduler.Backend) error {
	// TODO(multi-topology): resolve from PCS topologyName
	const defaultTopologyName = "grove-topology"
	if !operatorCfg.TopologyAwareScheduling.Enabled {
		logger.Info("cluster topology is disabled, deleting existing ClusterTopology resource if any")
		return deleteClusterTopology(ctx, cl, defaultTopologyName)
	}
	// create or update ClusterTopology based on configuration
	clusterTopology, err := ensureClusterTopology(ctx, cl, logger, defaultTopologyName, operatorCfg.TopologyAwareScheduling.Levels)
	if err != nil {
		return err
	}
	// Delegate scheduler-specific topology to each backend that supports it
	for _, b := range backends {
		tasBackend, ok := b.(scheduler.TopologyAwareSchedBackend)
		if !ok {
			logger.V(1).Info("Scheduler backend does not implement TopologyAwareSchedBackend, skipping topology sync", "backend", b.Name())
			continue
		}
		if err := tasBackend.SyncTopology(ctx, cl, clusterTopology); err != nil {
			return fmt.Errorf("failed to sync topology for backend %s: %w", b.Name(), err)
		}
	}
	return nil
}

// GetClusterTopologyLevels retrieves the TopologyLevels from the specified ClusterTopology resource.
func GetClusterTopologyLevels(ctx context.Context, cl client.Client, name string) ([]grovecorev1alpha1.TopologyLevel, error) {
	clusterTopology := &grovecorev1alpha1.ClusterTopology{}
	if err := cl.Get(ctx, client.ObjectKey{Name: name}, clusterTopology); err != nil {
		return nil, err
	}
	return clusterTopology.Spec.Levels, nil
}

// deleteClusterTopology deletes the ClusterTopology with the given name.
func deleteClusterTopology(ctx context.Context, cl client.Client, name string) error {
	if err := client.IgnoreNotFound(cl.Delete(ctx, &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: ctrl.ObjectMeta{
			Name: name,
		},
	})); err != nil {
		return fmt.Errorf("failed to delete ClusterTopology %s: %w", name, err)
	}
	return nil
}

// ensureClusterTopology ensures that the ClusterTopology is created or updated in the cluster.
func ensureClusterTopology(ctx context.Context, cl client.Client, logger logr.Logger, name string, topologyLevels []grovecorev1alpha1.TopologyLevel) (*grovecorev1alpha1.ClusterTopology, error) {
	desiredTopology := buildClusterTopology(name, topologyLevels)
	existingTopology := &grovecorev1alpha1.ClusterTopology{}
	err := cl.Get(ctx, client.ObjectKey{Name: name}, existingTopology)
	if err != nil {
		// If not found, create a new ClusterTopology
		if apierrors.IsNotFound(err) {
			if err = cl.Create(ctx, desiredTopology); err != nil {
				return nil, fmt.Errorf("failed to create ClusterTopology %s: %w", name, err)
			}
			logger.Info("Created ClusterTopology", "name", name)
			return desiredTopology, nil
		}
		return nil, fmt.Errorf("failed to get ClusterTopology %s: %w", name, err)
	}

	// Update existing ClusterTopology if there are changes
	if isClusterTopologyChanged(existingTopology, desiredTopology) {
		existingTopology.Spec = desiredTopology.Spec
		if err = cl.Update(ctx, existingTopology); err != nil {
			return nil, fmt.Errorf("failed to update ClusterTopology %s: %w", name, err)
		}
		logger.Info("Updated ClusterTopology successfully", "name", name)
	}
	return existingTopology, nil
}

// buildClusterTopology constructs a ClusterTopology resource based on the provided topology levels.
// Levels are stored in user-provided order (no automatic sorting).
func buildClusterTopology(name string, topologyLevels []grovecorev1alpha1.TopologyLevel) *grovecorev1alpha1.ClusterTopology {
	levels := make([]grovecorev1alpha1.TopologyLevel, len(topologyLevels))
	copy(levels, topologyLevels)

	return &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: ctrl.ObjectMeta{
			Name: name,
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: levels,
		},
	}
}

func isClusterTopologyChanged(oldTopology, newTopology *grovecorev1alpha1.ClusterTopology) bool {
	return !reflect.DeepEqual(oldTopology.Spec, newTopology.Spec)
}
