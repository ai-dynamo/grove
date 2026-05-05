// /*
// Copyright 2026 The Grove Authors.
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

package kai

import (
	"context"
	"fmt"
	"reflect"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	kaitopologyv1alpha1 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/samber/lo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var _ scheduler.TopologyAwareSchedBackend = (*schedulerBackend)(nil)

// TopologyGVR returns the GroupVersionResource of the KAI Topology CRD.
func (b *schedulerBackend) TopologyGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "kai.scheduler",
		Version:  "v1alpha1",
		Resource: "topologies",
	}
}

// TopologyResourceName returns the name of the KAI Topology resource for the given ClusterTopology.
// KAI topology resources are always named after their ClusterTopology.
func (b *schedulerBackend) TopologyResourceName(ct *grovecorev1alpha1.ClusterTopology) string {
	return ct.Name
}

// SyncTopology creates or updates the KAI Topology resource for the given ClusterTopology.
func (b *schedulerBackend) SyncTopology(ctx context.Context, k8sClient client.Client, ct *grovecorev1alpha1.ClusterTopology) error {
	if k8sClient == nil {
		k8sClient = b.client
	}
	logger := log.FromContext(ctx)

	desiredTopology, err := buildKAITopology(ct.Name, ct, b.scheme)
	if err != nil {
		return fmt.Errorf("failed to build KAI Topology: %w", err)
	}

	existingTopology := &kaitopologyv1alpha1.Topology{}
	if err = k8sClient.Get(ctx, client.ObjectKey{Name: ct.Name}, existingTopology); err != nil {
		if apierrors.IsNotFound(err) {
			if err = k8sClient.Create(ctx, desiredTopology); err != nil {
				return fmt.Errorf("failed to create KAI Topology %s: %w", ct.Name, err)
			}
			logger.Info("Created KAI Topology", "name", ct.Name)
			return nil
		}
		return fmt.Errorf("failed to get KAI Topology %s: %w", ct.Name, err)
	}

	// If the existing KAI topology does not have passed in ClusterTopology as owner, then error out.
	if !metav1.IsControlledBy(existingTopology, ct) {
		return fmt.Errorf("KAI Topology %s is not owned by ClusterTopology %s. It is required that KAI Topology by this name is created by Grove operator and has ClusterTopology set as its owner", ct.Name, ct.Name)
	}

	if isKAITopologyChanged(existingTopology, desiredTopology) {
		// KAI Topology needs to be updated. Since KAI Topology has immutable levels, we need to delete and recreate it.
		// See https://github.com/kai-scheduler/KAI-Scheduler/blob/130ab4468f96b25b1899ad2eced0a072dff033e9/pkg/apis/kai/v1alpha1/topology_types.go#L60
		if err = k8sClient.Delete(ctx, existingTopology); err != nil {
			return fmt.Errorf("failed to recreate (action: delete) existing KAI Topology %s: %w", ct.Name, err)
		}
		if err = k8sClient.Create(ctx, desiredTopology); err != nil {
			return fmt.Errorf("failed to recreate (action: create) KAI Topology %s: %w", ct.Name, err)
		}
		logger.Info("Recreated KAI Topology with updated levels", "name", ct.Name)
	}
	return nil
}

// OnTopologyDelete is a no-op for KAI; the OwnerReference cascade handles deletion.
func (b *schedulerBackend) OnTopologyDelete(_ context.Context, _ client.Client, _ *grovecorev1alpha1.ClusterTopology) error {
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

// desiredKAITopologyLevels converts ClusterTopology levels to KAI topology levels.
// Used for drift comparison without constructing a full KAI Topology object with owner references.
func desiredKAITopologyLevels(ct *grovecorev1alpha1.ClusterTopology) []kaitopologyv1alpha1.TopologyLevel {
	return lo.Map(ct.Spec.Levels, func(level grovecorev1alpha1.TopologyLevel, _ int) kaitopologyv1alpha1.TopologyLevel {
		return kaitopologyv1alpha1.TopologyLevel{
			NodeLabel: level.Key,
		}
	})
}

// CheckTopologyDrift compares the named KAI Topology resource against the ClusterTopology levels.
func (b *schedulerBackend) CheckTopologyDrift(ctx context.Context, ct *grovecorev1alpha1.ClusterTopology, ref grovecorev1alpha1.SchedulerTopologyReference) (bool, string, int64, error) {
	existingTopology := &kaitopologyv1alpha1.Topology{}
	if err := b.client.Get(ctx, client.ObjectKey{Name: ref.TopologyReference}, existingTopology); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("KAI Topology %q not found", ref.TopologyReference), 0, nil
		}
		return false, "", 0, fmt.Errorf("failed to get KAI Topology %s: %w", ref.TopologyReference, err)
	}
	desired := desiredKAITopologyLevels(ct)
	if !reflect.DeepEqual(existingTopology.Spec.Levels, desired) {
		return false, "KAI Topology levels differ from ClusterTopology levels", existingTopology.Generation, nil
	}
	return true, "", existingTopology.Generation, nil
}
