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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var _ scheduler.TopologyAwareBackend = (*schedulerBackend)(nil)

var topologyGVK = schema.GroupVersionKind{
	Group:   "kueue.x-k8s.io",
	Version: "v1beta2",
	Kind:    "Topology",
}

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

	existingTopology := newKueueTopology(ct.Name)
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
		return fmt.Errorf("Kueue Topology %s is not owned by ClusterTopologyBinding %s", ct.Name, ct.Name)
	}
	if !reflect.DeepEqual(kueueTopologyLevels(existingTopology), desiredKueueTopologyLevels(ct)) {
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
	existingTopology := newKueueTopology(ref.TopologyReference)
	if err := b.client.Get(ctx, client.ObjectKey{Name: ref.TopologyReference}, existingTopology); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Kueue Topology %q not found", ref.TopologyReference), 0, nil
		}
		return false, "", 0, fmt.Errorf("failed to get Kueue Topology %s: %w", ref.TopologyReference, err)
	}
	if !reflect.DeepEqual(kueueTopologyLevels(existingTopology), desiredKueueTopologyLevels(ct)) {
		return false, "Kueue Topology levels differ from ClusterTopologyBinding levels", existingTopology.GetGeneration(), nil
	}
	return true, "", existingTopology.GetGeneration(), nil
}

func buildKueueTopology(name string, ct *grovecorev1alpha1.ClusterTopologyBinding, scheme *runtime.Scheme) (*unstructured.Unstructured, error) {
	topology := newKueueTopology(name)
	topology.SetOwnerReferences(nil)
	if err := unstructured.SetNestedSlice(topology.Object, desiredKueueTopologyLevels(ct), "spec", "levels"); err != nil {
		return nil, err
	}
	if err := controllerutil.SetControllerReference(ct, topology, scheme); err != nil {
		return nil, fmt.Errorf("failed to set owner reference for Kueue Topology: %w", err)
	}
	return topology, nil
}

func newKueueTopology(name string) *unstructured.Unstructured {
	topology := &unstructured.Unstructured{}
	topology.SetGroupVersionKind(topologyGVK)
	topology.SetName(name)
	return topology
}

func desiredKueueTopologyLevels(ct *grovecorev1alpha1.ClusterTopologyBinding) []any {
	levels := make([]any, 0, len(ct.Spec.Levels))
	for _, level := range ct.Spec.Levels {
		levels = append(levels, map[string]any{"nodeLabel": level.Key})
	}
	return levels
}

func kueueTopologyLevels(topology *unstructured.Unstructured) []any {
	levels, _, _ := unstructured.NestedSlice(topology.Object, "spec", "levels")
	return levels
}
