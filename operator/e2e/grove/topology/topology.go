//go:build e2e

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

package topology

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	kaitopologyv1alpha1 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/kai/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PCSGTypeConfig defines configuration for a PCSG type verification.
type PCSGTypeConfig struct {
	Name string // Human-readable name (e.g., "decoder")
	FQN  string // Fully-qualified PCSG name
}

// TopologyVerifier provides Grove topology verification using a controller-runtime client.
type TopologyVerifier struct {
	cl     client.Client
	logger *log.Logger
}

// NewTopologyVerifier creates a TopologyVerifier bound to the given client.
func NewTopologyVerifier(cl client.Client, logger *log.Logger) *TopologyVerifier {
	return &TopologyVerifier{cl: cl, logger: logger}
}

// VerifyClusterTopologyLevels verifies that a ClusterTopology CR exists with the expected topology levels.
func (tv *TopologyVerifier) VerifyClusterTopologyLevels(ctx context.Context, name string, expectedLevels []corev1alpha1.TopologyLevel) error {
	var clusterTopology corev1alpha1.ClusterTopology
	if err := tv.cl.Get(ctx, types.NamespacedName{Name: name}, &clusterTopology); err != nil {
		return fmt.Errorf("failed to get ClusterTopology %s: %w", name, err)
	}

	if len(clusterTopology.Spec.Levels) != len(expectedLevels) {
		return fmt.Errorf("ClusterTopology has %d levels, expected %d", len(clusterTopology.Spec.Levels), len(expectedLevels))
	}

	for i, level := range clusterTopology.Spec.Levels {
		if level.Domain != expectedLevels[i].Domain || level.Key != expectedLevels[i].Key {
			return fmt.Errorf("ClusterTopology level %d: got domain=%s key=%s, expected domain=%s key=%s",
				i, level.Domain, level.Key, expectedLevels[i].Domain, expectedLevels[i].Key)
		}
	}

	tv.logger.Infof("ClusterTopology %s verified with %d levels", name, len(expectedLevels))
	return nil
}

// VerifyKAITopologyLevels verifies that a KAI Topology CR exists with the expected levels.
func (tv *TopologyVerifier) VerifyKAITopologyLevels(ctx context.Context, name string, expectedKeys []string) error {
	var kaiTopology kaitopologyv1alpha1.Topology
	if err := tv.cl.Get(ctx, types.NamespacedName{Name: name}, &kaiTopology); err != nil {
		return fmt.Errorf("failed to get KAI Topology %s: %w", name, err)
	}

	if len(kaiTopology.Spec.Levels) != len(expectedKeys) {
		return fmt.Errorf("KAI Topology has %d levels, expected %d", len(kaiTopology.Spec.Levels), len(expectedKeys))
	}

	for i, level := range kaiTopology.Spec.Levels {
		if level.NodeLabel != expectedKeys[i] {
			return fmt.Errorf("KAI Topology level %d: got key=%s, expected key=%s", i, level.NodeLabel, expectedKeys[i])
		}
	}

	hasClusterTopologyOwner := false
	for _, ref := range kaiTopology.OwnerReferences {
		if ref.Kind == "ClusterTopology" && ref.Name == name {
			hasClusterTopologyOwner = true
			break
		}
	}

	if !hasClusterTopologyOwner {
		return fmt.Errorf("KAI Topology does not have ClusterTopology %s as owner", name)
	}

	tv.logger.Infof("KAI Topology %s verified with %d levels and correct owner reference", name, len(expectedKeys))
	return nil
}

// FilterPodsByLabel filters pods by a specific label key-value pair.
func FilterPodsByLabel(pods []v1.Pod, labelKey, labelValue string) []v1.Pod {
	var filtered []v1.Pod
	for _, pod := range pods {
		if val, ok := pod.Labels[labelKey]; ok && val == labelValue {
			filtered = append(filtered, pod)
		}
	}
	return filtered
}

// VerifyPodsInSameTopologyDomain verifies that all pods are in the same topology domain.
func (tv *TopologyVerifier) VerifyPodsInSameTopologyDomain(ctx context.Context, pods []v1.Pod, topologyKey string) error {
	if len(pods) == 0 {
		return errors.New("no pods provided for topology verification")
	}

	firstPod := pods[0]
	if firstPod.Spec.NodeName == "" {
		return fmt.Errorf("pod %s has no assigned node", firstPod.Name)
	}

	var firstNode v1.Node
	if err := tv.cl.Get(ctx, types.NamespacedName{Name: firstPod.Spec.NodeName}, &firstNode); err != nil {
		return fmt.Errorf("failed to get node %s: %w", firstPod.Spec.NodeName, err)
	}

	expectedValue, ok := firstNode.Labels[topologyKey]
	if !ok {
		return fmt.Errorf("node %s does not have topology label %s", firstNode.Name, topologyKey)
	}

	for _, pod := range pods[1:] {
		if pod.Spec.NodeName == "" {
			return fmt.Errorf("pod %s has no assigned node", pod.Name)
		}

		var node v1.Node
		if err := tv.cl.Get(ctx, types.NamespacedName{Name: pod.Spec.NodeName}, &node); err != nil {
			return fmt.Errorf("failed to get node %s: %w", pod.Spec.NodeName, err)
		}

		actualValue, ok := node.Labels[topologyKey]
		if !ok {
			return fmt.Errorf("node %s does not have topology label %s", node.Name, topologyKey)
		}

		if actualValue != expectedValue {
			return fmt.Errorf("pod %s is in topology domain %s=%s, but expected %s=%s",
				pod.Name, topologyKey, actualValue, topologyKey, expectedValue)
		}
	}

	tv.logger.Infof("Verified %d pods are in same topology domain %s=%s", len(pods), topologyKey, expectedValue)
	return nil
}

// VerifyLabeledPodsInTopologyDomain filters pods by label, verifies count, and checks topology domain.
func (tv *TopologyVerifier) VerifyLabeledPodsInTopologyDomain(ctx context.Context, allPods []v1.Pod, labelKey, labelValue string, expectedCount int, topologyKey string) error {
	filteredPods := FilterPodsByLabel(allPods, labelKey, labelValue)
	if len(filteredPods) != expectedCount {
		return fmt.Errorf("expected %d pods with %s=%s, got %d",
			expectedCount, labelKey, labelValue, len(filteredPods))
	}

	return tv.VerifyPodsInSameTopologyDomain(ctx, filteredPods, topologyKey)
}

// VerifyPCSGReplicasInTopologyDomain verifies that each PCSG replica's pods are in the same topology domain.
func (tv *TopologyVerifier) VerifyPCSGReplicasInTopologyDomain(ctx context.Context, allPods []v1.Pod, pcsgLabel string, replicaCount, podsPerReplica int, topologyLabel string) error {
	for replica := 0; replica < replicaCount; replica++ {
		replicaPods := FilterPodsByLabel(
			FilterPodsByLabel(allPods, common.LabelPodCliqueScalingGroup, pcsgLabel),
			common.LabelPodCliqueScalingGroupReplicaIndex,
			fmt.Sprintf("%d", replica),
		)
		if len(replicaPods) != podsPerReplica {
			return fmt.Errorf("expected %d PCSG replica %d pods, got %d", podsPerReplica, replica, len(replicaPods))
		}
		if err := tv.VerifyPodsInSameTopologyDomain(ctx, replicaPods, topologyLabel); err != nil {
			return fmt.Errorf("failed to verify PCSG replica %d pods in same topology domain: %w", replica, err)
		}
	}
	return nil
}

// VerifyMultiTypePCSGReplicas verifies multiple PCSG types across replicas.
func (tv *TopologyVerifier) VerifyMultiTypePCSGReplicas(ctx context.Context, allPods []v1.Pod, pcsgTypes []PCSGTypeConfig, replicasPerType, podsPerReplica int, topologyLabel string) error {
	for _, pcsgType := range pcsgTypes {
		for replica := 0; replica < replicasPerType; replica++ {
			replicaPods := FilterPodsByLabel(
				FilterPodsByLabel(allPods, common.LabelPodCliqueScalingGroup, pcsgType.FQN),
				common.LabelPodCliqueScalingGroupReplicaIndex,
				fmt.Sprintf("%d", replica),
			)
			if len(replicaPods) != podsPerReplica {
				return fmt.Errorf("expected %d %s replica-%d pods, got %d",
					podsPerReplica, pcsgType.Name, replica, len(replicaPods))
			}
			if err := tv.VerifyPodsInSameTopologyDomain(ctx, replicaPods, topologyLabel); err != nil {
				return fmt.Errorf("failed to verify %s replica-%d pods in same topology domain: %w",
					pcsgType.Name, replica, err)
			}
		}
	}
	return nil
}

// CreateClusterTopology creates a ClusterTopology CR with the given name and levels.
func (tv *TopologyVerifier) CreateClusterTopology(ctx context.Context, name string, levels []corev1alpha1.TopologyLevel) error {
	ct := &corev1alpha1.ClusterTopology{
		TypeMeta:   metav1.TypeMeta{APIVersion: "grove.io/v1alpha1", Kind: "ClusterTopology"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1alpha1.ClusterTopologySpec{Levels: levels},
	}
	if err := tv.cl.Create(ctx, ct); err != nil {
		return fmt.Errorf("failed to create ClusterTopology %s: %w", name, err)
	}
	tv.logger.Infof("Created ClusterTopology %s with %d levels", name, len(levels))
	return nil
}

// EnsureClusterTopology creates a ClusterTopology if it does not already exist.
// If it already exists it is left unchanged. This is safe to call from multiple
// tests that share the same cluster-scoped ClusterTopology.
func (tv *TopologyVerifier) EnsureClusterTopology(ctx context.Context, name string, levels []corev1alpha1.TopologyLevel) error {
	ct := &corev1alpha1.ClusterTopology{
		TypeMeta:   metav1.TypeMeta{APIVersion: "grove.io/v1alpha1", Kind: "ClusterTopology"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1alpha1.ClusterTopologySpec{Levels: levels},
	}
	if err := tv.cl.Create(ctx, ct); err != nil {
		if apierrors.IsAlreadyExists(err) {
			tv.logger.Infof("ClusterTopology %s already exists, skipping creation", name)
			return nil
		}
		return fmt.Errorf("failed to create ClusterTopology %s: %w", name, err)
	}
	tv.logger.Infof("Created ClusterTopology %s with %d levels", name, len(levels))
	return nil
}

// CreateClusterTopologyWithSchedulerReferences creates a ClusterTopology CR with levels and schedulerTopologyReferences.
func (tv *TopologyVerifier) CreateClusterTopologyWithSchedulerReferences(ctx context.Context, name string, levels []corev1alpha1.TopologyLevel, refs []corev1alpha1.SchedulerTopologyReference) error {
	ct := &corev1alpha1.ClusterTopology{
		TypeMeta:   metav1.TypeMeta{APIVersion: "grove.io/v1alpha1", Kind: "ClusterTopology"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels:                      levels,
			SchedulerTopologyReferences: refs,
		},
	}
	if err := tv.cl.Create(ctx, ct); err != nil {
		return fmt.Errorf("failed to create ClusterTopology %s with scheduler references: %w", name, err)
	}
	tv.logger.Infof("Created ClusterTopology %s with %d levels and %d scheduler references", name, len(levels), len(refs))
	return nil
}

// UpdateClusterTopologyLevels fetches an existing ClusterTopology and updates its levels.
func (tv *TopologyVerifier) UpdateClusterTopologyLevels(ctx context.Context, name string, levels []corev1alpha1.TopologyLevel) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var ct corev1alpha1.ClusterTopology
		if err := tv.cl.Get(ctx, types.NamespacedName{Name: name}, &ct); err != nil {
			return fmt.Errorf("failed to get ClusterTopology %s for update: %w", name, err)
		}
		ct.Spec.Levels = levels
		if err := tv.cl.Update(ctx, &ct); err != nil {
			return fmt.Errorf("failed to update ClusterTopology %s levels: %w", name, err)
		}
		tv.logger.Infof("Updated ClusterTopology %s to %d levels", name, len(levels))
		return nil
	})
}

// DeleteClusterTopology deletes a ClusterTopology CR by name.
func (tv *TopologyVerifier) DeleteClusterTopology(ctx context.Context, name string) error {
	ct := &corev1alpha1.ClusterTopology{}
	ct.Name = name
	if err := tv.cl.Delete(ctx, ct); err != nil {
		return fmt.Errorf("failed to delete ClusterTopology %s: %w", name, err)
	}
	tv.logger.Infof("Deleted ClusterTopology %s", name)
	return nil
}

// WaitForKAITopology polls until the KAI Topology exists with the expected level keys and owner reference.
func (tv *TopologyVerifier) WaitForKAITopology(ctx context.Context, name string, expectedKeys []string, timeout, interval time.Duration) error {
	fetchFn := waiter.FetchFunc[*kaitopologyv1alpha1.Topology](func(ctx context.Context) (*kaitopologyv1alpha1.Topology, error) {
		var kaiTopology kaitopologyv1alpha1.Topology
		err := tv.cl.Get(ctx, types.NamespacedName{Name: name}, &kaiTopology)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return &kaiTopology, err
	})
	return waiter.New[*kaitopologyv1alpha1.Topology]().
		WithTimeout(timeout).
		WithInterval(interval).
		WithLogger(tv.logger).
		WithRetryOnError().
		WaitUntil(ctx, fetchFn, func(kaiTopology *kaitopologyv1alpha1.Topology) bool {
			if kaiTopology == nil {
				return false
			}
			if len(kaiTopology.Spec.Levels) != len(expectedKeys) {
				return false
			}
			for i, level := range kaiTopology.Spec.Levels {
				if level.NodeLabel != expectedKeys[i] {
					return false
				}
			}
			for _, ref := range kaiTopology.OwnerReferences {
				if ref.Kind == "ClusterTopology" && ref.Name == name {
					return true
				}
			}
			return false
		})
}

// WaitForClusterTopologyCondition polls until the ClusterTopology has a condition matching the expected type, status, and reason.
func (tv *TopologyVerifier) WaitForClusterTopologyCondition(ctx context.Context, name, conditionType, expectedStatus, expectedReason string, timeout, interval time.Duration) error {
	fetchFn := waiter.FetchFunc[*corev1alpha1.ClusterTopology](func(ctx context.Context) (*corev1alpha1.ClusterTopology, error) {
		var ct corev1alpha1.ClusterTopology
		err := tv.cl.Get(ctx, types.NamespacedName{Name: name}, &ct)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return &ct, err
	})
	return waiter.New[*corev1alpha1.ClusterTopology]().
		WithTimeout(timeout).
		WithInterval(interval).
		WithLogger(tv.logger).
		WithRetryOnError().
		WaitUntil(ctx, fetchFn, func(ct *corev1alpha1.ClusterTopology) bool {
			if ct == nil {
				return false
			}
			for _, cond := range ct.Status.Conditions {
				if cond.Type == conditionType && string(cond.Status) == expectedStatus && cond.Reason == expectedReason {
					return true
				}
			}
			return false
		})
}

// WaitForPCSCondition polls until the PodCliqueSet has a condition matching the expected type, status, and reason.
func (tv *TopologyVerifier) WaitForPCSCondition(ctx context.Context, namespace, name, conditionType, expectedStatus, expectedReason string, timeout, interval time.Duration) error {
	fetchFn := waiter.FetchFunc[*corev1alpha1.PodCliqueSet](func(ctx context.Context) (*corev1alpha1.PodCliqueSet, error) {
		var pcs corev1alpha1.PodCliqueSet
		err := tv.cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &pcs)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return &pcs, err
	})
	return waiter.New[*corev1alpha1.PodCliqueSet]().
		WithTimeout(timeout).
		WithInterval(interval).
		WithLogger(tv.logger).
		WithRetryOnError().
		WaitUntil(ctx, fetchFn, func(pcs *corev1alpha1.PodCliqueSet) bool {
			if pcs == nil {
				return false
			}
			for _, cond := range pcs.Status.Conditions {
				if cond.Type == conditionType && string(cond.Status) == expectedStatus && cond.Reason == expectedReason {
					return true
				}
			}
			return false
		})
}

// VerifyClusterTopologySchedulerStatuses checks that the ClusterTopology has the expected number of
// SchedulerTopologyStatuses and returns them.
func (tv *TopologyVerifier) VerifyClusterTopologySchedulerStatuses(ctx context.Context, name string, expectedCount int) ([]corev1alpha1.SchedulerTopologyStatus, error) {
	var clusterTopology corev1alpha1.ClusterTopology
	if err := tv.cl.Get(ctx, types.NamespacedName{Name: name}, &clusterTopology); err != nil {
		return nil, fmt.Errorf("failed to get ClusterTopology %s: %w", name, err)
	}

	statuses := clusterTopology.Status.SchedulerTopologyStatuses
	if len(statuses) != expectedCount {
		return nil, fmt.Errorf("ClusterTopology %s has %d scheduler topology statuses, expected %d", name, len(statuses), expectedCount)
	}

	tv.logger.Infof("ClusterTopology %s has %d scheduler topology statuses as expected", name, expectedCount)
	return statuses, nil
}
