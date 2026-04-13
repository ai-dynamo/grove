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

	kaitopologyv1alpha1 "github.com/NVIDIA/KAI-scheduler/pkg/apis/kai/v1alpha1"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
)

// PCSGTypeConfig defines configuration for a PCSG type verification.
type PCSGTypeConfig struct {
	Name string // Human-readable name (e.g., "decoder")
	FQN  string // Fully-qualified PCSG name
}

// TopologyVerifier provides Grove topology verification using pre-created Kubernetes clients.
type TopologyVerifier struct {
	clients *clients.Clients
	logger  *log.Logger
}

// NewTopologyVerifier creates a TopologyVerifier bound to the given clients.
func NewTopologyVerifier(clients *clients.Clients, logger *log.Logger) *TopologyVerifier {
	return &TopologyVerifier{clients: clients, logger: logger}
}

// VerifyClusterTopologyLevels verifies that a ClusterTopology CR exists with the expected topology levels.
func (tv *TopologyVerifier) VerifyClusterTopologyLevels(ctx context.Context, name string, expectedLevels []corev1alpha1.TopologyLevel) error {
	unstructuredCT, err := tv.clients.DynamicClient.Resource(clusterTopologyGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get ClusterTopology %s: %w", name, err)
	}

	var clusterTopology corev1alpha1.ClusterTopology
	if err := k8s.ConvertUnstructuredToTyped(unstructuredCT.Object, &clusterTopology); err != nil {
		return fmt.Errorf("failed to convert ClusterTopology to typed: %w", err)
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
	unstructuredTopology, err := tv.clients.DynamicClient.Resource(kaiTopologyGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get KAI Topology %s: %w", name, err)
	}

	var kaiTopology kaitopologyv1alpha1.Topology
	if err := k8s.ConvertUnstructuredToTyped(unstructuredTopology.Object, &kaiTopology); err != nil {
		return fmt.Errorf("failed to convert KAI Topology to typed: %w", err)
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

	firstNode, err := tv.clients.Clientset.CoreV1().Nodes().Get(ctx, firstPod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
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

		node, err := tv.clients.Clientset.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
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
			FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", pcsgLabel),
			"grove.io/podcliquescalinggroup-replica-index",
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
				FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", pcsgType.FQN),
				"grove.io/podcliquescalinggroup-replica-index",
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
