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

package topology

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	kaitopologyv1alpha1 "github.com/NVIDIA/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/samber/lo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var logger = ctrl.Log.WithName("topology-manager")

// EnsureTopology creates or updates the ClusterTopology CR from configuration
func EnsureTopology(ctx context.Context, client client.Client, name string, levels []configv1alpha1.TopologyLevel) error {
	topology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: convertTopologyLevels(levels),
		},
	}
	upsertTopology, err := upsertClusterTopology(ctx, client, topology)
	if err != nil {
		return err
	}
	return ensureKAITopology(ctx, client, upsertTopology)
}

// upsertClusterTopology ensures that a ClusterTopology object exists by creating or updating it based on the desired state.
func upsertClusterTopology(ctx context.Context, k8sClient client.Client, desireClusterTopology *corev1alpha1.ClusterTopology) (*corev1alpha1.ClusterTopology, error) {
	existing := &corev1alpha1.ClusterTopology{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: desireClusterTopology.Name}, existing); err != nil {
		if apierrors.IsNotFound(err) {
			if err := k8sClient.Create(ctx, desireClusterTopology); err != nil {
				return nil, fmt.Errorf("failed to create ClusterTopology: %w", err)
			}
			return desireClusterTopology, nil
		}
		return nil, fmt.Errorf("failed to get existing ClusterTopology: %w", err)
	}
	isChanged := isClusterTopologyChanged(existing, desireClusterTopology)
	if !isChanged {
		return existing, nil
	}
	existing.Spec = desireClusterTopology.Spec
	err := k8sClient.Update(ctx, existing)
	if err != nil {
		return nil, fmt.Errorf("failed to update ClusterTopology: %w", err)
	}
	return existing, nil
}

// isClusterTopologyChanged checks if the ClusterTopology is changed
func isClusterTopologyChanged(existing *corev1alpha1.ClusterTopology, desireClusterTopology *corev1alpha1.ClusterTopology) bool {
	existingLevels := lo.SliceToMap(existing.Spec.Levels, func(l corev1alpha1.TopologyLevel) (corev1alpha1.TopologyDomain, string) {
		return l.Domain, l.Key
	})
	newLevels := lo.SliceToMap(desireClusterTopology.Spec.Levels, func(l corev1alpha1.TopologyLevel) (corev1alpha1.TopologyDomain, string) {
		return l.Domain, l.Key
	})
	isChanged := !reflect.DeepEqual(existingLevels, newLevels)
	return isChanged
}

// convertTopologyLevels converts configuration topology levels to core API topology levels
func convertTopologyLevels(levels []configv1alpha1.TopologyLevel) []corev1alpha1.TopologyLevel {
	result := make([]corev1alpha1.TopologyLevel, len(levels))
	for i, level := range levels {
		result[i] = corev1alpha1.TopologyLevel{
			Domain: corev1alpha1.TopologyDomain(level.Domain),
			Key:    level.Key,
		}
	}
	return result
}

// convertClusterTopologyToKai converts ClusterTopology to KAI Topology with sorted levels
func convertClusterTopologyToKai(clusterTopology *corev1alpha1.ClusterTopology) *kaitopologyv1alpha1.Topology {
	sortedLevels := make([]corev1alpha1.TopologyLevel, len(clusterTopology.Spec.Levels))
	copy(sortedLevels, clusterTopology.Spec.Levels)

	sort.Slice(sortedLevels, func(i, j int) bool {
		return sortedLevels[i].Domain.Compare(sortedLevels[j].Domain) < 0
	})

	kaiLevels := make([]kaitopologyv1alpha1.TopologyLevel, len(sortedLevels))
	for i, level := range sortedLevels {
		kaiLevels[i] = kaitopologyv1alpha1.TopologyLevel{
			NodeLabel: level.Key,
		}
	}

	return &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterTopology.Name,
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: kaiLevels,
		},
	}
}

func createKAITopology(ctx context.Context, client client.Client, clusterTopology *corev1alpha1.ClusterTopology, kaiTopology *kaitopologyv1alpha1.Topology) error {
	if err := controllerutil.SetControllerReference(clusterTopology, kaiTopology, client.Scheme()); err != nil {
		return fmt.Errorf("failed to set owner reference for KAI Topology: %w", err)
	}
	if err := client.Create(ctx, kaiTopology); err != nil {
		return fmt.Errorf("failed to create KAI Topology: %w", err)
	}
	return nil
}

func ensureKAITopology(ctx context.Context, client client.Client, clusterTopology *corev1alpha1.ClusterTopology) error {
	desiredKaiTopology := convertClusterTopologyToKai(clusterTopology)

	existing := &kaitopologyv1alpha1.Topology{}
	err := client.Get(ctx, types.NamespacedName{Name: desiredKaiTopology.Name}, existing)

	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := createKAITopology(ctx, client, clusterTopology, desiredKaiTopology); err != nil {
				return err
			}
			logger.Info("KAI topology created successfully", "name", desiredKaiTopology.Name)
			return nil
		}
		return fmt.Errorf("failed to get existing KAI Topology: %w", err)
	}

	if !metav1.IsControlledBy(existing, clusterTopology) {
		return fmt.Errorf("KAI Topology %s is owned by a different controller", existing.Name)
	}

	if !reflect.DeepEqual(existing.Spec.Levels, desiredKaiTopology.Spec.Levels) {
		// levels have changed, levels are immutable, delete existing and recreate
		if err = client.Delete(ctx, existing); err != nil {
			return fmt.Errorf("failed to delete KAI Topology for recreation: %w", err)
		}

		if err = createKAITopology(ctx, client, clusterTopology, desiredKaiTopology); err != nil {
			return err
		}
		logger.Info("KAI topology recreated with new levels", "name", desiredKaiTopology.Name)
		return nil
	}

	logger.Info("KAI topology unchanged", "name", desiredKaiTopology.Name)
	return nil
}

// EnsureDeleteClusterTopology deletes the ClusterTopology with the given name if it exists
func EnsureDeleteClusterTopology(ctx context.Context, client client.Client, name string) error {
	kaiTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	err := client.Delete(ctx, kaiTopology)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete Cluster-Topology: %w", err)
	}
	return nil
}
