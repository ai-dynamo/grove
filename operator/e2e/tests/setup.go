//go:build e2e

package tests

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

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

var (
	// logger for the tests
	logger *utils.Logger

	// testImages are the Docker images to push to the test registry
	testImages = []string{"nginx:alpine-slim"}
)

func init() {
	// Initialize klog flags and set them to suppress stderr output.
	// This prevents warning messages like "restartPolicy will be ignored" from appearing in test output.
	// Comment this out if you want to see the warnings, but they all seem harmless and noisy.
	klog.InitFlags(nil)
	if err := flag.Set("logtostderr", "false"); err != nil {
		panic("Failed to set logtostderr flag")
	}

	if err := flag.Set("alsologtostderr", "false"); err != nil {
		panic("Failed to set alsologtostderr flag")
	}

	// increase logger verbosity for debugging
	logger = utils.NewTestLogger(utils.InfoLevel)
}

const (
	// defaultPollTimeout is the timeout for most polling conditions
	defaultPollTimeout = 30 * time.Second
	// defaultPollInterval is the interval for most polling conditions
	defaultPollInterval = 5 * time.Second
)

// prepareTestCluster is a helper function that prepares the shared cluster for a test
// with the specified number of worker nodes and returns the necessary clients and a cleanup function.
// The cleanup function will fatally fail the test if workload cleanup fails.
func prepareTestCluster(ctx context.Context, t *testing.T, requiredWorkerNodes int) (*kubernetes.Clientset, *rest.Config, dynamic.Interface, func()) {
	t.Helper()

	// Get the shared cluster instance
	sharedCluster := setup.SharedCluster(logger)

	// Prepare cluster with required worker nodes
	if err := sharedCluster.PrepareForTest(ctx, requiredWorkerNodes); err != nil {
		t.Fatalf("Failed to prepare shared cluster: %v", err)
	}

	// Get clients from shared cluster
	clientset, restConfig, dynamicClient := sharedCluster.GetClients()

	// Create cleanup function
	cleanup := func() {
		if err := sharedCluster.CleanupWorkloads(ctx); err != nil {
			t.Fatalf("Failed to cleanup workloads: %v", err)
		}
	}

	return clientset, restConfig, dynamicClient, cleanup
}

// getWorkerNodes retrieves the names of all worker nodes in the cluster,
// excluding control plane nodes. Returns an error if the node list cannot be retrieved.
func getWorkerNodes(ctx context.Context, clientset kubernetes.Interface) ([]string, error) {
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	workerNodes := make([]string, 0)
	for _, node := range nodes.Items {
		if _, isServer := node.Labels["node-role.kubernetes.io/control-plane"]; !isServer {
			workerNodes = append(workerNodes, node.Name)
		}
	}

	return workerNodes, nil
}

// assertPodsOnDistinctNodes asserts that the pods are scheduled on distinct nodes and fails the test if not.
func assertPodsOnDistinctNodes(t *testing.T, pods []v1.Pod) {
	t.Helper()

	assignedNodes := make(map[string]string, len(pods))
	for _, pod := range pods {
		nodeName := pod.Spec.NodeName
		if nodeName == "" {
			t.Fatalf("Pod %s is running but has no assigned node", pod.Name)
		}
		if existingPod, exists := assignedNodes[nodeName]; exists {
			t.Fatalf("Pods %s and %s are scheduled on the same node %s; expected unique nodes", existingPod, pod.Name, nodeName)
		}
		assignedNodes[nodeName] = pod.Name
	}
}

// verifyAllPodsArePending verifies that all pods matching the label selector are in pending state.
// Returns an error if verification fails or timeout occurs.
func verifyAllPodsArePending(ctx context.Context, clientset kubernetes.Interface, namespace, labelSelector string, timeout, interval time.Duration) error {
	return utils.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return false, err
		}

		// Check if all pods are pending
		for _, pod := range pods.Items {
			if pod.Status.Phase != v1.PodPending {
				logger.Debugf("Pod %s is not pending: %s", pod.Name, pod.Status.Phase)
				return false, nil
			}
		}

		return true, nil
	})
}

// verifyPodsArePendingWithUnschedulableEvents verifies that pods are pending with Unschedulable events from kai-scheduler.
// If allPodsMustBePending is true, verifies ALL pods are pending; otherwise only checks pending pods for Unschedulable events.
// Returns an error if verification fails, or nil if successful after finding Unschedulable events for all (pending) pods.
func verifyPodsArePendingWithUnschedulableEvents(ctx context.Context, clientset kubernetes.Interface, namespace, labelSelector string, allPodsMustBePending bool, timeout, interval time.Duration) error {
	// First verify all pods are pending if required
	if allPodsMustBePending {
		if err := verifyAllPodsArePending(ctx, clientset, namespace, labelSelector, timeout, interval); err != nil {
			return fmt.Errorf("not all pods are pending: %w", err)
		}
	}

	// Now verify that all pending pods have Unschedulable events
	return utils.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return false, err
		}

		// Track pods with Unschedulable events
		podsWithUnschedulableEvent := 0
		pendingCount := 0

		for _, pod := range pods.Items {
			// Check if pod is pending
			if pod.Status.Phase == v1.PodPending {
				pendingCount++

				// Check for Unschedulable event from kai-scheduler
				events, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
					FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", pod.Name),
				})
				if err != nil {
					return false, err
				}

				// Find the most recent event
				var mostRecentEvent *v1.Event
				for i := range events.Items {
					event := &events.Items[i]
					if mostRecentEvent == nil || event.LastTimestamp.After(mostRecentEvent.LastTimestamp.Time) {
						mostRecentEvent = event
					}
				}

				// Check if the most recent event is Warning/Unschedulable from kai-scheduler or Warning/PodGrouperWarning from pod-grouper
				if mostRecentEvent != nil &&
					mostRecentEvent.Type == v1.EventTypeWarning &&
					((mostRecentEvent.Reason == "Unschedulable" && mostRecentEvent.Source.Component == "kai-scheduler") ||
						(mostRecentEvent.Reason == "PodGrouperWarning" && mostRecentEvent.Source.Component == "pod-grouper")) {
					logger.Debugf("Pod %s has Unschedulable event: %s", pod.Name, mostRecentEvent.Message)
					podsWithUnschedulableEvent++
				} else if mostRecentEvent != nil {
					logger.Debugf("Pod %s most recent event is not Unschedulable: type=%s, reason=%s, component=%s",
						pod.Name, mostRecentEvent.Type, mostRecentEvent.Reason, mostRecentEvent.Source.Component)
				}
			}
		}

		// Return true only when all pending pods have the Unschedulable event
		if podsWithUnschedulableEvent == pendingCount {
			return true, nil
		}

		logger.Debugf("Waiting for all pending pods to have Unschedulable events: %d/%d", podsWithUnschedulableEvent, pendingCount)
		return false, nil
	})
}

// waitForPodConditions polls until the expected pod state is reached or timeout occurs.
// Returns the current state (total, running, pending) for logging purposes.
func waitForPodConditions(ctx context.Context, clientset kubernetes.Interface, namespace, labelSelector string, expectedTotalPods, expectedPending int, timeout, interval time.Duration) (int, int, int, error) {
	var lastTotal, lastRunning, lastPending int

	err := utils.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			return false, err
		}

		lastTotal = len(pods.Items)
		lastRunning = 0
		lastPending = 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				lastRunning++
			case v1.PodPending:
				lastPending++
			}
		}

		// Check if conditions are met
		return lastTotal == expectedTotalPods && lastPending == expectedPending, nil
	})

	return lastTotal, lastRunning, lastPending, err
}

// scalePCSGAndWait scales a PCSG and waits for the expected pod conditions to be reached.
func scalePCSGAndWait(t *testing.T, ctx context.Context, clientset kubernetes.Interface, dynamicClient dynamic.Interface, namespace, labelSelector, pcsgName string, replicas int32, expectedTotalPods, expectedPending int, timeout, interval time.Duration) {
	t.Helper()

	pcsgGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquescalinggroups"}
	patchBytes, err := json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": replicas,
		},
	})
	if err != nil {
		t.Fatalf("Failed to marshal PCSG patch: %v", err)
	}

	if _, err := dynamicClient.Resource(pcsgGVR).Namespace(namespace).Patch(ctx, pcsgName, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		t.Fatalf("Failed to scale PodCliqueScalingGroup %s: %v", pcsgName, err)
	}

	totalPods, runningPods, pendingPods, err := waitForPodConditions(ctx, clientset, namespace, labelSelector, expectedTotalPods, expectedPending, timeout, interval)
	if err != nil {
		t.Fatalf("Failed to wait for expected pod conditions after PCSG scaling: %v. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
			err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
	}
}

// scalePCSAndWait scales a PCS and waits for the expected pod conditions to be reached.
func scalePCSAndWait(t *testing.T, ctx context.Context, clientset kubernetes.Interface, dynamicClient dynamic.Interface, namespace, labelSelector, pcsName string, replicas int32, expectedTotalPods, expectedPending int, timeout, interval time.Duration) {
	t.Helper()

	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	patchBytes, err := json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": replicas,
		},
	})
	if err != nil {
		t.Fatalf("Failed to marshal PCS patch: %v", err)
	}

	if _, err := dynamicClient.Resource(pcsGVR).Namespace(namespace).Patch(ctx, pcsName, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		t.Fatalf("Failed to scale PodCliqueSet %s: %v", pcsName, err)
	}

	totalPods, runningPods, pendingPods, err := waitForPodConditions(ctx, clientset, namespace, labelSelector, expectedTotalPods, expectedPending, timeout, interval)
	if err != nil {
		t.Fatalf("Failed to wait for expected pod conditions after PCS scaling: %v. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
			err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
	}
}
