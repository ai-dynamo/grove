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
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// TestContext holds common test parameters that are shared across many utility functions.
// This reduces repetitive parameter passing and makes function signatures cleaner.
type TestContext struct {
	T             *testing.T
	Ctx           context.Context
	Clientset     kubernetes.Interface
	DynamicClient dynamic.Interface
	RestConfig    *rest.Config
	Namespace     string
	Timeout       time.Duration
	Interval      time.Duration
	Nodes         []string
	Workload      *WorkloadConfig // Optional: workload configuration for the test
}

// pollForCondition is a wrapper around utils.PollForCondition that accepts TestContext
func pollForCondition(tc TestContext, condition func() (bool, error)) error {
	return utils.PollForCondition(tc.Ctx, tc.Timeout, tc.Interval, condition)
}

// listPods is a wrapper around utils.ListPods that accepts TestContext
func listPods(tc TestContext) (*v1.PodList, error) {
	return utils.ListPods(tc.Ctx, tc.Clientset, tc.Namespace, tc.getLabelSelector())
}

// waitForPods is a wrapper around utils.WaitForPods that accepts TestContext
func waitForPods(tc TestContext, expectedCount int) error {
	return utils.WaitForPods(tc.Ctx, tc.RestConfig, []string{tc.Namespace}, tc.getLabelSelector(), expectedCount, tc.Timeout, tc.Interval, logger)
}

// cordonNode is a wrapper around utils.SetNodeSchedulable that accepts TestContext
func cordonNode(tc TestContext, nodeName string) error {
	return utils.SetNodeSchedulable(tc.Ctx, tc.Clientset, nodeName, false)
}

// uncordonNode is a wrapper around utils.SetNodeSchedulable that accepts TestContext
func uncordonNode(tc TestContext, nodeName string) error {
	return utils.SetNodeSchedulable(tc.Ctx, tc.Clientset, nodeName, true)
}

// scalePodCliqueScalingGroup is a wrapper around utils.ScalePodCliqueScalingGroupWithClient that accepts TestContext.
// This scales the PCSG instance directly by patching its spec.replicas field.
// This is the correct approach for scaling existing PCSGs since the PCS controller only sets PCSG replicas
// during initial creation to support HPA scaling.
func scalePodCliqueScalingGroup(tc TestContext, name string, replicas int) error {
	return utils.ScalePodCliqueScalingGroupWithClient(tc.Ctx, tc.DynamicClient, tc.Namespace, name, replicas, tc.Timeout, tc.Interval)
}

// scalePodCliqueSet is a wrapper around utils.ScalePodCliqueSetWithClient that accepts TestContext
func scalePodCliqueSet(tc TestContext, name string, replicas int) error {
	return utils.ScalePodCliqueSetWithClient(tc.Ctx, tc.DynamicClient, tc.Namespace, name, replicas)
}

// applyYAMLFile is a wrapper around utils.ApplyYAMLFile that accepts TestContext
func applyYAMLFile(tc TestContext, yamlPath string) ([]utils.AppliedResource, error) {
	return utils.ApplyYAMLFile(tc.Ctx, yamlPath, tc.Namespace, tc.RestConfig, logger)
}

// waitForPodCount is a wrapper around utils.WaitForPodCount that accepts TestContext
func waitForPodCount(tc TestContext, expectedCount int) (*v1.PodList, error) {
	return utils.WaitForPodCount(tc.Ctx, tc.Clientset, tc.Namespace, tc.getLabelSelector(), expectedCount, tc.Timeout, tc.Interval)
}

// waitForPodCountAndPhases is a wrapper around utils.WaitForPodCountAndPhases that accepts TestContext
func waitForPodCountAndPhases(tc TestContext, expectedTotal, expectedRunning, expectedPending int) error {
	return utils.WaitForPodCountAndPhases(tc.Ctx, tc.Clientset, tc.Namespace, tc.getLabelSelector(), expectedTotal, expectedRunning, expectedPending, tc.Timeout, tc.Interval)
}

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
func getWorkerNodes(tc TestContext) ([]string, error) {
	nodes, err := tc.Clientset.CoreV1().Nodes().List(tc.Ctx, metav1.ListOptions{})
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

// listPodsAndAssertDistinctNodes lists pods and asserts they are on distinct nodes in one call.
// This helper reduces the repetitive pattern of listing pods, checking errors, and asserting.
func listPodsAndAssertDistinctNodes(tc TestContext) {
	tc.T.Helper()
	pods, err := listPods(tc)
	if err != nil {
		tc.T.Fatalf("Failed to list workload pods: %v", err)
	}
	assertPodsOnDistinctNodes(tc.T, pods.Items)
}

// verifyAllPodsArePending verifies that all pods matching the label selector are in pending state.
// Returns an error if verification fails or timeout occurs.
func verifyAllPodsArePending(tc TestContext) error {
	return pollForCondition(tc, func() (bool, error) {
		pods, err := listPods(tc)
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
// expectedPendingCount is the expected number of pending pods (pass 0 to skip count validation)
// Returns an error if verification fails, or nil if successful after finding Unschedulable events for all (pending) pods.
func verifyPodsArePendingWithUnschedulableEvents(tc TestContext, allPodsMustBePending bool, expectedPendingCount int) error {
	// First verify all pods are pending if required
	if allPodsMustBePending {
		if err := verifyAllPodsArePending(tc); err != nil {
			return fmt.Errorf("not all pods are pending: %w", err)
		}
	}

	// Now verify that all pending pods have Unschedulable events
	return pollForCondition(tc, func() (bool, error) {
		pods, err := listPods(tc)
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
				events, err := tc.Clientset.CoreV1().Events(tc.Namespace).List(tc.Ctx, metav1.ListOptions{
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

		// Verify expected pending count if specified
		if expectedPendingCount > 0 && pendingCount != expectedPendingCount {
			logger.Debugf("Expected %d pending pods but found %d pending pods", expectedPendingCount, pendingCount)
			return false, nil
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
func waitForPodConditions(tc TestContext, expectedTotalPods, expectedPending int) (int, int, int, error) {
	var lastTotal, lastRunning, lastPending int

	err := pollForCondition(tc, func() (bool, error) {
		pods, err := listPods(tc)
		if err != nil {
			return false, err
		}

		count := utils.CountPodsByPhase(pods)
		lastTotal = count.Total
		lastRunning = count.Running
		lastPending = count.Pending

		// Check if conditions are met
		return lastTotal == expectedTotalPods && lastPending == expectedPending, nil
	})

	return lastTotal, lastRunning, lastPending, err
}

// scalePCSGInstanceAndWait scales a specific PCSG instance directly and waits for the expected pod conditions.
// pcsgInstanceName is the full PCSG instance name (e.g., "workload1-0-sg-x")
// This scales only the specified PCSG instance, not all instances across PCS replicas.
// Use this for tests that need asymmetric PCSG configurations across PCS replicas.
func scalePCSGInstanceAndWait(tc TestContext, pcsgInstanceName string, replicas int32, expectedTotalPods, expectedPending int) {
	tc.T.Helper()

	if err := scalePodCliqueScalingGroup(tc, pcsgInstanceName, int(replicas)); err != nil {
		tc.T.Fatalf("Failed to scale PodCliqueScalingGroup instance %s: %v", pcsgInstanceName, err)
	}

	totalPods, runningPods, pendingPods, err := waitForPodConditions(tc, expectedTotalPods, expectedPending)
	if err != nil {
		tc.T.Fatalf("Failed to wait for expected pod conditions after PCSG instance scaling: %v. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
			err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
	}
}

// scalePCSGAcrossAllReplicasAndWait scales a PCSG across all PCS replicas by directly patching each PCSG instance.
// pcsName is the name of the PodCliqueSet (e.g., "workload1")
// pcsgName is the name of the PCSG as defined in the PCS template (e.g., "sg-x")
// pcsReplicas is the number of PCS replicas (each has its own PCSG instance)
//
// This function scales PCSG instances directly (e.g., "workload1-0-sg-x", "workload1-1-sg-x") rather than
// modifying the PCS template. This is necessary because the PCS controller only sets PCSG replicas during
// initial creation to support HPA scaling - post-creation scaling must be done directly on the PCSG resource.
func scalePCSGAcrossAllReplicasAndWait(tc TestContext, pcsName, pcsgName string, pcsReplicas, pcsgReplicas int32, expectedTotalPods, expectedPending int) {
	tc.T.Helper()

	// Scale each PCSG instance across all PCS replicas
	for replicaIndex := int32(0); replicaIndex < pcsReplicas; replicaIndex++ {
		pcsgInstanceName := fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, pcsgName)
		if err := scalePodCliqueScalingGroup(tc, pcsgInstanceName, int(pcsgReplicas)); err != nil {
			tc.T.Fatalf("Failed to scale PodCliqueScalingGroup instance %s: %v", pcsgInstanceName, err)
		}
	}

	totalPods, runningPods, pendingPods, err := waitForPodConditions(tc, expectedTotalPods, expectedPending)
	if err != nil {
		tc.T.Fatalf("Failed to wait for expected pod conditions after PCSG scaling across all replicas: %v. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
			err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
	}
}

// scalePCSAndWait scales a PCS and waits for the expected pod conditions to be reached.
func scalePCSAndWait(tc TestContext, pcsName string, replicas int32, expectedTotalPods, expectedPending int) {
	tc.T.Helper()

	if err := scalePodCliqueSet(tc, pcsName, int(replicas)); err != nil {
		tc.T.Fatalf("Failed to scale PodCliqueSet %s: %v", pcsName, err)
	}

	totalPods, runningPods, pendingPods, err := waitForPodConditions(tc, expectedTotalPods, expectedPending)
	if err != nil {
		tc.T.Fatalf("Failed to wait for expected pod conditions after PCS scaling: %v. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
			err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
	}
}

// cordonNodes cordons multiple nodes.
// This helper reduces repetition of the cordon loop pattern found throughout tests.
func cordonNodes(tc TestContext, nodes []string) {
	tc.T.Helper()
	for _, nodeName := range nodes {
		if err := cordonNode(tc, nodeName); err != nil {
			tc.T.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}
}

// uncordonNodes uncordons multiple nodes.
// This helper reduces repetition of the uncordon loop pattern found throughout tests.
func uncordonNodes(tc TestContext, nodes []string) {
	tc.T.Helper()
	for _, nodeName := range nodes {
		if err := uncordonNode(tc, nodeName); err != nil {
			tc.T.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}
}

// waitForPodPhases waits for pods to reach specific running and pending counts.
// This helper reduces repetition of the polling pattern for checking pod phases.
func waitForPodPhases(tc TestContext, expectedRunning, expectedPending int) error {
	tc.T.Helper()
	return pollForCondition(tc, func() (bool, error) {
		pods, err := listPods(tc)
		if err != nil {
			return false, err
		}

		count := utils.CountPodsByPhase(pods)
		return count.Running == expectedRunning && count.Pending == expectedPending, nil
	})
}

// waitForReadyPods waits for a specific number of pods to be ready (Running + Ready condition).
// This helper reduces repetition of the polling pattern for checking pod ready state.
func waitForReadyPods(tc TestContext, expectedReady int) error {
	tc.T.Helper()
	return pollForCondition(tc, func() (bool, error) {
		pods, err := listPods(tc)
		if err != nil {
			return false, err
		}

		readyCount := utils.CountReadyPods(pods)
		return readyCount == expectedReady, nil
	})
}

// setupAndCordonNodes retrieves worker nodes, validates the count, and cordons the specified number.
// Returns the nodes that were cordoned.
func setupAndCordonNodes(tc TestContext, numToCordon int) []string {
	tc.T.Helper()

	workerNodes, err := getWorkerNodes(tc)
	if err != nil {
		tc.T.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < numToCordon {
		tc.T.Fatalf("expected at least %d worker nodes to cordon, but found %d", numToCordon, len(workerNodes))
	}

	nodesToCordon := workerNodes[:numToCordon]
	cordonNodes(tc, nodesToCordon)

	return nodesToCordon
}

// WorkloadConfig defines configuration for deploying and verifying a workload.
type WorkloadConfig struct {
	Name         string
	YAMLPath     string
	Namespace    string
	ExpectedPods int
}

// GetLabelSelector returns the label selector calculated from the workload name.
// The label selector follows the pattern: "app.kubernetes.io/part-of=<name>"
func (w WorkloadConfig) GetLabelSelector() string {
	return fmt.Sprintf("app.kubernetes.io/part-of=%s", w.Name)
}

// deployAndVerifyWorkload applies a workload YAML and waits for the expected pod count.
// Uses tc.Workload for configuration. Returns the pod list after successful deployment.
func deployAndVerifyWorkload(tc TestContext) (*v1.PodList, error) {
	tc.T.Helper()

	if tc.Workload == nil {
		return nil, fmt.Errorf("tc.Workload is nil, must be set before calling deployAndVerifyWorkload")
	}

	_, err := applyYAMLFile(tc, tc.Workload.YAMLPath)
	if err != nil {
		return nil, fmt.Errorf("failed to apply workload YAML: %w", err)
	}

	pods, err := waitForPodCount(tc, tc.Workload.ExpectedPods)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for pods to be created: %w", err)
	}

	return pods, nil
}

// getLabelSelector returns the label selector for the current workload.
// This is a convenience method to avoid repeatedly calling tc.Workload.GetLabelSelector().
func (tc TestContext) getLabelSelector() string {
	if tc.Workload == nil {
		return ""
	}
	return tc.Workload.GetLabelSelector()
}

// verifyAllPodsArePendingWithSleep verifies all pods are pending after a fixed delay.
// The sleep is a workaround for https://github.com/NVIDIA/grove/issues/226
func verifyAllPodsArePendingWithSleep(tc TestContext) {
	tc.T.Helper()
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(tc); err != nil {
		tc.T.Fatalf("Failed to verify all pods are pending: %v", err)
	}
}

// uncordonNodesAndWaitForPods uncordons the specified nodes and waits for pods to be ready.
// This helper combines the common pattern of uncordoning nodes followed by waiting for pods.
func uncordonNodesAndWaitForPods(tc TestContext, nodes []string, expectedPods int) {
	tc.T.Helper()

	uncordonNodes(tc, nodes)

	if err := waitForPods(tc, expectedPods); err != nil {
		tc.T.Fatalf("Failed to wait for pods to be ready: %v", err)
	}
}

// waitForRunningPods waits for a specific number of pods to be in Running phase (not necessarily ready).
// This is useful for checking min-replicas scheduling where pods need to be running but may not be fully ready yet.
func waitForRunningPods(tc TestContext, expectedRunning int) error {
	tc.T.Helper()
	return pollForCondition(tc, func() (bool, error) {
		pods, err := listPods(tc)
		if err != nil {
			return false, err
		}

		count := utils.CountPodsByPhase(pods)
		return count.Running == expectedRunning, nil
	})
}

// scalePCS scales a PCS and returns an errgroup.Group that completes when the expected pod count is reached.
// The operation runs asynchronously - call Wait() on the returned Group to block until complete.
// If delayMs > 0, the operation will sleep for that duration before starting.
func scalePCS(tc TestContext, pcsName string, replicas int32, expectedTotalPods, expectedPending, delayMs int) *errgroup.Group {
	g := new(errgroup.Group)
	g.Go(func() error {
		startTime := time.Now()

		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		if err := scalePodCliqueSet(tc, pcsName, int(replicas)); err != nil {
			return fmt.Errorf("failed to scale PodCliqueSet %s: %w", pcsName, err)
		}

		totalPods, runningPods, pendingPods, err := waitForPodConditions(tc, expectedTotalPods, expectedPending)
		elapsed := time.Since(startTime)
		if err != nil {
			logger.Infof("[scalePCS] Scale %s FAILED after %v: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
				pcsName, elapsed, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
			return fmt.Errorf("failed to wait for expected pod conditions after PCS scaling: %w. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
				err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
		}
		logger.Infof("[scalePCS] Scale %s completed in %v (replicas=%d, pods=%d)", pcsName, elapsed, replicas, totalPods)
		return nil
	})
	return g
}

// scalePCSGAcrossAllReplicas scales a PCSG across all PCS replicas and returns an errgroup.Group.
// The operation runs asynchronously - call Wait() on the returned Group to block until complete.
// If delayMs > 0, the operation will sleep for that duration before starting.
func scalePCSGAcrossAllReplicas(tc TestContext, pcsName, pcsgName string, pcsReplicas, pcsgReplicas int32, expectedTotalPods, expectedPending, delayMs int) *errgroup.Group {
	g := new(errgroup.Group)
	g.Go(func() error {
		startTime := time.Now()

		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		// Scale each PCSG instance across all PCS replicas
		for replicaIndex := int32(0); replicaIndex < pcsReplicas; replicaIndex++ {
			pcsgInstanceName := fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, pcsgName)
			if err := scalePodCliqueScalingGroup(tc, pcsgInstanceName, int(pcsgReplicas)); err != nil {
				return fmt.Errorf("failed to scale PodCliqueScalingGroup instance %s: %w", pcsgInstanceName, err)
			}
		}

		totalPods, runningPods, pendingPods, err := waitForPodConditions(tc, expectedTotalPods, expectedPending)
		elapsed := time.Since(startTime)
		if err != nil {
			logger.Infof("[scalePCSGAcrossAllReplicas] Scale %s FAILED after %v: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
				pcsgName, elapsed, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
			return fmt.Errorf("failed to wait for expected pod conditions after PCSG scaling across all replicas: %w. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
				err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
		}
		logger.Infof("[scalePCSGAcrossAllReplicas] Scale %s completed in %v (pcsgReplicas=%d, pods=%d)", pcsgName, elapsed, pcsgReplicas, totalPods)
		return nil
	})
	return g
}

// convertUnstructuredToTyped converts an unstructured map to a typed object
func convertUnstructuredToTyped(u map[string]interface{}, typed interface{}) error {
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, typed)
}

// convertTypedToUnstructured converts a typed object to an unstructured object
func convertTypedToUnstructured(typed interface{}) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(typed)
	if err != nil {
		return nil, err
	}
	var unstructuredMap map[string]interface{}
	err = json.Unmarshal(data, &unstructuredMap)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: unstructuredMap}, nil
}
