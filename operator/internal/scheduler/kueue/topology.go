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

package kueue

import (
	"context"
	"fmt"
	"reflect"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

var _ scheduler.TopologyAwareBackend = (*schedulerBackend)(nil)

func (b *schedulerBackend) TopologyGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta2",
		Resource: "topologies",
	}
}

func (b *schedulerBackend) TopologyResourceName(ct *grovecorev1alpha1.ClusterTopologyBinding) string {
	return ct.Name
}

func (b *schedulerBackend) SyncTopology(ctx context.Context, k8sClient client.Client, ct *grovecorev1alpha1.ClusterTopologyBinding) error {
	if k8sClient == nil {
		k8sClient = b.client
	}
	logger := log.FromContext(ctx)

	desiredTopology, err := buildKueueTopology(ct.Name, ct, b.scheme)
	if err != nil {
		return fmt.Errorf("failed to build Kueue Topology: %w", err)
	}

	existingTopology := &kueuev1beta2.Topology{}
	if err = k8sClient.Get(ctx, client.ObjectKey{Name: ct.Name}, existingTopology); err != nil {
		if apierrors.IsNotFound(err) {
			if err = k8sClient.Create(ctx, desiredTopology); err != nil {
				return fmt.Errorf("failed to create Kueue Topology %s: %w", ct.Name, err)
			}
			logger.Info("Created Kueue Topology", "name", ct.Name)
			return nil
		}
		return fmt.Errorf("failed to get Kueue Topology %s: %w", ct.Name, err)
	}

	if !metav1.IsControlledBy(existingTopology, ct) {
		return fmt.Errorf("kueue Topology %s is not owned by ClusterTopologyBinding %s", ct.Name, ct.Name)
	}
	if isKueueTopologyChanged(existingTopology, desiredTopology) {
		if err = k8sClient.Delete(ctx, existingTopology); err != nil {
			return fmt.Errorf("failed to recreate (action: delete) existing Kueue Topology %s: %w", ct.Name, err)
		}
		if err = k8sClient.Create(ctx, desiredTopology); err != nil {
			return fmt.Errorf("failed to recreate (action: create) Kueue Topology %s: %w", ct.Name, err)
		}
		logger.Info("Recreated Kueue Topology with updated levels", "name", ct.Name)
	}
	return nil
}

func (b *schedulerBackend) OnTopologyDelete(_ context.Context, _ client.Client, _ *grovecorev1alpha1.ClusterTopologyBinding) error {
	return nil
}

func (b *schedulerBackend) CheckTopologyDrift(ctx context.Context, ct *grovecorev1alpha1.ClusterTopologyBinding, ref grovecorev1alpha1.SchedulerTopologyBinding) (bool, string, int64, error) {
	existingTopology := &kueuev1beta2.Topology{}
	if err := b.client.Get(ctx, client.ObjectKey{Name: ref.TopologyReference}, existingTopology); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Kueue Topology %q not found", ref.TopologyReference), 0, nil
		}
		return false, "", 0, fmt.Errorf("failed to get Kueue Topology %s: %w", ref.TopologyReference, err)
	}
	desired := desiredKueueTopologyLevels(ct)
	if !reflect.DeepEqual(existingTopology.Spec.Levels, desired) {
		return false, "Kueue Topology levels differ from ClusterTopologyBinding levels", existingTopology.Generation, nil
	}
	return true, "", existingTopology.Generation, nil
}

func buildKueueTopology(name string, ct *grovecorev1alpha1.ClusterTopologyBinding, scheme *runtime.Scheme) (*kueuev1beta2.Topology, error) {
	topology := &kueuev1beta2.Topology{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kueuev1beta2.TopologySpec{Levels: desiredKueueTopologyLevels(ct)},
	}
	if err := controllerutil.SetControllerReference(ct, topology, scheme); err != nil {
		return nil, fmt.Errorf("failed to set owner reference for Kueue Topology: %w", err)
	}
	return topology, nil
}

func isKueueTopologyChanged(oldTopology, newTopology *kueuev1beta2.Topology) bool {
	return !reflect.DeepEqual(oldTopology.Spec.Levels, newTopology.Spec.Levels)
}

func desiredKueueTopologyLevels(ct *grovecorev1alpha1.ClusterTopologyBinding) []kueuev1beta2.TopologyLevel {
	levels := make([]kueuev1beta2.TopologyLevel, 0, len(ct.Spec.Levels))
	for _, level := range ct.Spec.Levels {
		levels = append(levels, kueuev1beta2.TopologyLevel{NodeLabel: level.Key})
	}
	return levels
}
