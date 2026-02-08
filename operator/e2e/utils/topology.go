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

//go:build e2e

package utils

import (
	"context"
	"errors"
	"fmt"

	kaitopologyv1alpha1 "github.com/NVIDIA/KAI-scheduler/pkg/apis/kai/v1alpha1"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var (
	clusterTopologyGVR = schema.GroupVersionResource{
		Group:    "grove.io",
		Version:  "v1alpha1",
		Resource: "clustertopologies",
	}

	kaiTopologyGVR = schema.GroupVersionResource{
		Group:    "kai.scheduler",
		Version:  "v1alpha1",
		Resource: "topologies",
	}

	kaiPodGroupGVR = schema.GroupVersionResource{
		Group:    "scheduling.run.ai",
		Version:  "v2alpha2",
		Resource: "podgroups",
	}
)

// VerifyClusterTopologyLevels verifies that a ClusterTopology CR exists with the expected topology levels
func VerifyClusterTopologyLevels(ctx context.Context, dynamicClient dynamic.Interface, name string, expectedLevels []corev1alpha1.TopologyLevel, logger *Logger) error {
	// Get unstructured ClusterTopology
	unstructuredCT, err := dynamicClient.Resource(clusterTopologyGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get ClusterTopology %s: %w", name, err)
	}

	// Convert to typed ClusterTopology
	var clusterTopology corev1alpha1.ClusterTopology
	if err := ConvertUnstructuredToTyped(unstructuredCT.Object, &clusterTopology); err != nil {
		return fmt.Errorf("failed to convert ClusterTopology to typed: %w", err)
	}

	// Compare levels using typed structs
	if len(clusterTopology.Spec.Levels) != len(expectedLevels) {
		return fmt.Errorf("ClusterTopology has %d levels, expected %d", len(clusterTopology.Spec.Levels), len(expectedLevels))
	}

	for i, level := range clusterTopology.Spec.Levels {
		if level.Domain != expectedLevels[i].Domain || level.Key != expectedLevels[i].Key {
			return fmt.Errorf("ClusterTopology level %d: got domain=%s key=%s, expected domain=%s key=%s",
				i, level.Domain, level.Key, expectedLevels[i].Domain, expectedLevels[i].Key)
		}
	}

	logger.Infof("ClusterTopology %s verified with %d levels", name, len(expectedLevels))
	return nil
}

// VerifyKAITopologyLevels verifies that a KAI Topology CR exists with the expected levels
func VerifyKAITopologyLevels(ctx context.Context, dynamicClient dynamic.Interface, name string, expectedKeys []string, logger *Logger) error {
	// Get unstructured KAI Topology (cluster-scoped resource)
	unstructuredTopology, err := dynamicClient.Resource(kaiTopologyGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get KAI Topology %s: %w", name, err)
	}

	// Convert to typed KAI Topology
	var kaiTopology kaitopologyv1alpha1.Topology
	if err := ConvertUnstructuredToTyped(unstructuredTopology.Object, &kaiTopology); err != nil {
		return fmt.Errorf("failed to convert KAI Topology to typed: %w", err)
	}

	// Verify levels using typed fields
	if len(kaiTopology.Spec.Levels) != len(expectedKeys) {
		return fmt.Errorf("KAI Topology has %d levels, expected %d", len(kaiTopology.Spec.Levels), len(expectedKeys))
	}

	for i, level := range kaiTopology.Spec.Levels {
		if level.NodeLabel != expectedKeys[i] {
			return fmt.Errorf("KAI Topology level %d: got key=%s, expected key=%s", i, level.NodeLabel, expectedKeys[i])
		}
	}

	// Verify owner reference using typed struct
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

	logger.Infof("KAI Topology %s verified with %d levels and correct owner reference", name, len(expectedKeys))
	return nil
}

// FilterPodsByLabel filters pods by a specific label key-value pair
func FilterPodsByLabel(pods []v1.Pod, labelKey, labelValue string) []v1.Pod {
	filtered := make([]v1.Pod, 0)
	for _, pod := range pods {
		if val, ok := pod.Labels[labelKey]; ok && val == labelValue {
			filtered = append(filtered, pod)
		}
	}
	return filtered
}

// VerifyPodsInSameTopologyDomain verifies that all pods are in the same topology domain (zone, rack, block, host)
func VerifyPodsInSameTopologyDomain(ctx context.Context, clientset kubernetes.Interface, pods []v1.Pod, topologyKey string, logger *Logger) error {
	if len(pods) == 0 {
		return errors.New("no pods provided for topology verification")
	}

	// Get the first pod's node to establish the expected topology value
	firstPod := pods[0]
	if firstPod.Spec.NodeName == "" {
		return fmt.Errorf("pod %s has no assigned node", firstPod.Name)
	}

	firstNode, err := clientset.CoreV1().Nodes().Get(ctx, firstPod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node %s: %w", firstPod.Spec.NodeName, err)
	}

	expectedValue, ok := firstNode.Labels[topologyKey]
	if !ok {
		return fmt.Errorf("node %s does not have topology label %s", firstNode.Name, topologyKey)
	}

	// Verify all other pods are in the same topology domain
	for _, pod := range pods[1:] {
		if pod.Spec.NodeName == "" {
			return fmt.Errorf("pod %s has no assigned node", pod.Name)
		}

		node, err := clientset.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
		if err != nil {
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

	logger.Infof("Verified %d pods are in same topology domain %s=%s", len(pods), topologyKey, expectedValue)
	return nil
}

// VerifyLabeledPodsInTopologyDomain filters pods by label, verifies count, and checks topology domain.
func VerifyLabeledPodsInTopologyDomain(
	ctx context.Context,
	clientset kubernetes.Interface,
	allPods []v1.Pod,
	labelKey, labelValue string,
	expectedCount int,
	topologyKey string,
	logger *Logger,
) error {
	filteredPods := FilterPodsByLabel(allPods, labelKey, labelValue)
	if len(filteredPods) != expectedCount {
		return fmt.Errorf(
			"expected %d pods with %s=%s, got %d",
			expectedCount, labelKey, labelValue, len(filteredPods),
		)
	}

	return VerifyPodsInSameTopologyDomain(ctx, clientset, filteredPods, topologyKey, logger)
}

// PCSGTypeConfig defines configuration for a PCSG type verification
type PCSGTypeConfig struct {
	Name string // Human-readable name (e.g., "decoder")
	FQN  string // Fully-qualified PCSG name
}

// VerifyPCSGReplicasInTopologyDomain verifies that each PCSG replica's pods
// are in the same topology domain (e.g., rack, host).
func VerifyPCSGReplicasInTopologyDomain(
	ctx context.Context,
	clientset kubernetes.Interface,
	allPods []v1.Pod,
	pcsgLabel string,
	replicaCount int,
	podsPerReplica int,
	topologyLabel string,
	logger *Logger,
) error {
	for replica := 0; replica < replicaCount; replica++ {
		replicaPods := FilterPodsByLabel(
			FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", pcsgLabel),
			"grove.io/podcliquescalinggroup-replica-index",
			fmt.Sprintf("%d", replica),
		)
		if len(replicaPods) != podsPerReplica {
			return fmt.Errorf("expected %d PCSG replica %d pods, got %d", podsPerReplica, replica, len(replicaPods))
		}
		if err := VerifyPodsInSameTopologyDomain(ctx, clientset, replicaPods, topologyLabel, logger); err != nil {
			return fmt.Errorf("failed to verify PCSG replica %d pods in same topology domain: %w", replica, err)
		}
	}
	return nil
}

// VerifyMultiTypePCSGReplicas verifies multiple PCSG types across replicas.
// Each PCSG type has multiple replicas, and each replica's pods should be in the same topology domain.
func VerifyMultiTypePCSGReplicas(
	ctx context.Context,
	clientset kubernetes.Interface,
	allPods []v1.Pod,
	pcsgTypes []PCSGTypeConfig,
	replicasPerType int,
	podsPerReplica int,
	topologyLabel string,
	logger *Logger,
) error {
	for _, pcsgType := range pcsgTypes {
		for replica := 0; replica < replicasPerType; replica++ {
			replicaPods := FilterPodsByLabel(
				FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", pcsgType.FQN),
				"grove.io/podcliquescalinggroup-replica-index",
				fmt.Sprintf("%d", replica),
			)
			if len(replicaPods) != podsPerReplica {
				return fmt.Errorf("expected %d %s replica-%d pods, got %d",
					podsPerReplica, pcsgType.Name, replica, len(replicaPods))
			}
			if err := VerifyPodsInSameTopologyDomain(ctx, clientset, replicaPods, topologyLabel, logger); err != nil {
				return fmt.Errorf("failed to verify %s replica-%d pods in same topology domain: %w",
					pcsgType.Name, replica, err)
			}
		}
	}
	return nil
}
