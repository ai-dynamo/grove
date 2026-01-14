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

	kaitopologyv1alpha1 "github.com/NVIDIA/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// SynchronizeTopology synchronizes Grove ClusterTopology and KAI scheduler Topology resources based on the operator configuration.
func SynchronizeTopology(ctx context.Context, cl client.Client, logger logr.Logger, operatorCfg *configv1alpha1.OperatorConfiguration) error {
	if !operatorCfg.TopologyAwareScheduling.Enabled {
		logger.Info("cluster topology is disabled, deleting existing ClusterTopology resource if any")
		return deleteClusterTopology(ctx, cl, grovecorev1alpha1.DefaultClusterTopologyName)
	}
	// create or update ClusterTopology based on configuration
	clusterTopology, err := ensureClusterTopology(ctx, cl, logger, grovecorev1alpha1.DefaultClusterTopologyName, operatorCfg.TopologyAwareScheduling.Levels)
	if err != nil {
		return err
	}
	// create or update KAI Topology based on ClusterTopology
	return ensureKAITopology(ctx, cl, logger, grovecorev1alpha1.DefaultClusterTopologyName, clusterTopology)
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
// The function checks if required TopologyDomain (host) is present, if not add it.
// kubernetes.io/hostname label is added by the Kubelet (see https://kubernetes.io/docs/reference/node/node-labels/)
// Therefore it is assumed that the host topology level will always be available. In case the admin fails to specify it,
// we correct that error by explicitly adding it when creating/updating the ClusterTopology resource.
func buildClusterTopology(name string, topologyLevels []grovecorev1alpha1.TopologyLevel) *grovecorev1alpha1.ClusterTopology {
	sortedTopologyLevels := make([]grovecorev1alpha1.TopologyLevel, len(topologyLevels))
	copy(sortedTopologyLevels, topologyLevels)
	// Sort topology levels to have a consistent order, arranging from broadest to narrowest domain.
	grovecorev1alpha1.SortTopologyLevels(sortedTopologyLevels)

	return &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: ctrl.ObjectMeta{
			Name: name,
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: sortedTopologyLevels,
		},
	}
}

func isClusterTopologyChanged(oldTopology, newTopology *grovecorev1alpha1.ClusterTopology) bool {
	return !reflect.DeepEqual(oldTopology.Spec, newTopology.Spec)
}

// ensureKAITopology ensures that the corresponding KAI Topology resource is created.
func ensureKAITopology(ctx context.Context, cl client.Client, logger logr.Logger, name string, clusterTopology *grovecorev1alpha1.ClusterTopology) error {
	desiredTopology, err := buildKAITopology(name, clusterTopology, cl.Scheme())
	if err != nil {
		return fmt.Errorf("failed to build KAI Topology: %w", err)
	}
	existingTopology := &kaitopologyv1alpha1.Topology{}
	if err = cl.Get(ctx, client.ObjectKey{Name: name}, existingTopology); err != nil {
		if apierrors.IsNotFound(err) {
			if err = cl.Create(ctx, desiredTopology); err != nil {
				return fmt.Errorf("failed to create KAI Topology %s: %w", name, err)
			}
			logger.Info("Created KAI Topology", "name", name)
			return nil
		}
		return fmt.Errorf("failed to get KAI Topology %s: %w", name, err)
	}

	// If the existing KAI topology does not have passed in ClusterTopology as owner, then error out.
	if !metav1.IsControlledBy(existingTopology, clusterTopology) {
		return fmt.Errorf("KAI Topology %s is not owned by ClusterTopology %s. It is required that KAI Topology by this name is created by Grove operator and has ClusterTopology set as its owner", name, clusterTopology.Name)
	}

	if isKAITopologyChanged(existingTopology, desiredTopology) {
		// KAI Topology needs to be updated. Since KAI Topology has immutable levels, we need to delete and recreate it.
		// See https://github.com/NVIDIA/KAI-Scheduler/blob/130ab4468f96b25b1899ad2eced0a072dff033e9/pkg/apis/kai/v1alpha1/topology_types.go#L60
		if err = cl.Delete(ctx, existingTopology); err != nil {
			return fmt.Errorf("failed to recreate (action: delete) existing KAI Topology %s: %w", name, err)
		}
		if err = cl.Create(ctx, desiredTopology); err != nil {
			return fmt.Errorf("failed to recreate (action: create) KAI Topology %s: %w", name, err)
		}
		logger.Info("Recreated KAI Topology with updated levels", "name", name)
	}
	return nil
}

func buildKAITopology(name string, clusterTopology *grovecorev1alpha1.ClusterTopology, scheme *runtime.Scheme) (*kaitopologyv1alpha1.Topology, error) {
	kaiTopologyLevels := lo.Map(clusterTopology.Spec.Levels, func(clusterTopologyLevel grovecorev1alpha1.TopologyLevel, _ int) kaitopologyv1alpha1.TopologyLevel {
		return kaitopologyv1alpha1.TopologyLevel{
			NodeLabel: clusterTopologyLevel.Key,
		}
	})
	kaiTopology := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: kaiTopologyLevels,
		},
	}
	if err := controllerutil.SetControllerReference(clusterTopology, kaiTopology, scheme); err != nil {
		return nil, fmt.Errorf("failed to set owner reference for KAI Topology: %w", err)
	}
	return kaiTopology, nil
}

func isKAITopologyChanged(oldTopology, newTopology *kaitopologyv1alpha1.Topology) bool {
	return !reflect.DeepEqual(oldTopology.Spec.Levels, newTopology.Spec.Levels)
}
