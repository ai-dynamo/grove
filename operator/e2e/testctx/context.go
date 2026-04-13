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

package testctx

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/diagnostics"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/nodes"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/resources"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Logger is set by the tests package init() to share the singleton logger.
var Logger *utils.Logger

const (
	// DefaultPollTimeout is the timeout for most polling conditions
	DefaultPollTimeout = 4 * time.Minute
	// DefaultPollInterval is the interval for most polling conditions
	DefaultPollInterval = 5 * time.Second
)

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

// TestContext is the primary per-test helper struct.
// Clients are created once and shared; domain managers are created by tests on demand.
type TestContext struct {
	T   *testing.T
	Ctx context.Context

	// Shared clients (created once per test run, goroutine-safe)
	Clients *clients.Clients

	// Per-suite configuration
	Namespace string
	Timeout   time.Duration
	Interval  time.Duration
	Workload  *WorkloadConfig
}

// TestOption configures a TestContext.
type TestOption func(*TestContext)

// WithNamespace sets the namespace for the test context.
func WithNamespace(ns string) TestOption {
	return func(tc *TestContext) { tc.Namespace = ns }
}

// WithTimeout sets the default poll timeout.
func WithTimeout(d time.Duration) TestOption {
	return func(tc *TestContext) { tc.Timeout = d }
}

// WithInterval sets the default poll interval.
func WithInterval(d time.Duration) TestOption {
	return func(tc *TestContext) { tc.Interval = d }
}

// WithWorkload sets the workload configuration.
func WithWorkload(wc *WorkloadConfig) TestOption {
	return func(tc *TestContext) { tc.Workload = wc }
}

// NewTestContext creates a TestContext from shared clients with optional configuration.
func NewTestContext(t *testing.T, ctx context.Context, clients *clients.Clients, opts ...TestOption) *TestContext {
	tc := &TestContext{
		T:         t,
		Ctx:       ctx,
		Clients:   clients,
		Namespace: "default",
		Timeout:   DefaultPollTimeout,
		Interval:  DefaultPollInterval,
	}

	for _, opt := range opts {
		opt(tc)
	}

	return tc
}

// PrepareTest prepares the shared cluster and returns a TestContext with cleanup function.
func PrepareTest(ctx context.Context, t *testing.T, requiredWorkerNodes int, opts ...TestOption) (*TestContext, func()) {
	t.Helper()

	sharedCluster := setup.SharedCluster(Logger)
	if err := sharedCluster.PrepareForTest(ctx, requiredWorkerNodes); err != nil {
		t.Fatalf("Failed to prepare shared cluster: %v", err)
	}

	clients := sharedCluster.GetAllClients()
	tc := NewTestContext(t, ctx, clients, opts...)

	// Initialize diagnostics for cleanup
	diagMode := os.Getenv(diagnostics.ModeEnvVar)
	if diagMode == "" {
		diagMode = diagnostics.ModeFile
	}
	diagDir := os.Getenv(diagnostics.DirEnvVar)
	diag := diagnostics.NewDiagCollector(clients, tc.Namespace, diagMode, diagDir, Logger)

	cleanup := func() {
		if t.Failed() {
			diag.CollectAll(ctx, t.Name())
		}

		if err := sharedCluster.CleanupWorkloads(ctx); err != nil {
			Logger.Error("================================================================================")
			Logger.Error("=== CLEANUP FAILURE - COLLECTING DIAGNOSTICS ===")
			Logger.Error("================================================================================")
			diag.CollectAll(ctx, t.Name())
			sharedCluster.MarkCleanupFailed(err)
			t.Fatalf("Failed to cleanup workloads: %v. All subsequent tests will fail.", err)
		}
	}

	return tc, cleanup
}

// --- Convenience methods that delegate to managers with suite-scoped defaults ---

// newPodManager creates a PodManager for internal use by convenience methods.
func (tc *TestContext) newPodManager() *pods.PodManager {
	return pods.NewPodManager(tc.Clients, Logger)
}

// newNodeManager creates a NodeManager for internal use by convenience methods.
func (tc *TestContext) newNodeManager() *nodes.NodeManager {
	return nodes.NewNodeManager(tc.Clients, Logger)
}

// newResourceManager creates a ResourceManager for internal use by convenience methods.
func (tc *TestContext) newResourceManager() *resources.ResourceManager {
	return resources.NewResourceManager(tc.Clients, Logger)
}

// newWorkloadManager creates a WorkloadManager for internal use by convenience methods.
func (tc *TestContext) newWorkloadManager() *workload.WorkloadManager {
	return workload.NewWorkloadManager(tc.Clients, Logger)
}

// GetLabelSelector returns the label selector for the current workload.
func (tc *TestContext) GetLabelSelector() string {
	if tc.Workload == nil {
		return ""
	}
	return tc.Workload.GetLabelSelector()
}

// PollForCondition wraps k8s.PollForCondition with suite defaults.
func (tc *TestContext) PollForCondition(condition func() (bool, error)) error {
	return k8s.PollForCondition(tc.Ctx, tc.Timeout, tc.Interval, condition)
}

// ListPods lists pods matching the current workload's label selector.
func (tc *TestContext) ListPods() (*v1.PodList, error) {
	return tc.newPodManager().List(tc.Ctx, tc.Namespace, tc.GetLabelSelector())
}

// WaitForPods waits for the expected pod count to be ready.
func (tc *TestContext) WaitForPods(expectedCount int) error {
	return tc.newPodManager().WaitForReady(tc.Ctx, []string{tc.Namespace}, tc.GetLabelSelector(), expectedCount, tc.Timeout, tc.Interval)
}

// WaitForPodCount waits for a specific number of pods and returns them.
func (tc *TestContext) WaitForPodCount(expectedCount int) (*v1.PodList, error) {
	return tc.newPodManager().WaitForCount(tc.Ctx, tc.Namespace, tc.GetLabelSelector(), expectedCount, tc.Timeout, tc.Interval)
}

// WaitForPodCountAndPhases waits for pods to reach specific total count and phase counts.
func (tc *TestContext) WaitForPodCountAndPhases(expectedTotal, expectedRunning, expectedPending int) error {
	return tc.newPodManager().WaitForCountAndPhases(tc.Ctx, tc.Namespace, tc.GetLabelSelector(), expectedTotal, expectedRunning, expectedPending, tc.Timeout, tc.Interval)
}

// WaitForPodPhases waits for pods to reach specific running and pending counts.
func (tc *TestContext) WaitForPodPhases(expectedRunning, expectedPending int) error {
	return tc.PollForCondition(func() (bool, error) {
		podList, err := tc.ListPods()
		if err != nil {
			return false, err
		}
		count := pods.CountPodsByPhase(podList)
		return count.Running == expectedRunning && count.Pending == expectedPending, nil
	})
}

// WaitForReadyPods waits for a specific number of pods to be ready.
func (tc *TestContext) WaitForReadyPods(expectedReady int) error {
	tc.T.Helper()
	return tc.PollForCondition(func() (bool, error) {
		podList, err := tc.ListPods()
		if err != nil {
			return false, err
		}
		return pods.CountReady(podList) == expectedReady, nil
	})
}

// WaitForRunningPods waits for a specific number of pods to be in Running phase.
func (tc *TestContext) WaitForRunningPods(expectedRunning int) error {
	tc.T.Helper()
	return tc.PollForCondition(func() (bool, error) {
		podList, err := tc.ListPods()
		if err != nil {
			return false, err
		}
		count := pods.CountPodsByPhase(podList)
		return count.Running == expectedRunning, nil
	})
}

// CordonNode marks a node as unschedulable.
func (tc *TestContext) CordonNode(nodeName string) error {
	return tc.newNodeManager().Cordon(tc.Ctx, nodeName)
}

// UncordonNode marks a node as schedulable.
func (tc *TestContext) UncordonNode(nodeName string) error {
	return tc.newNodeManager().Uncordon(tc.Ctx, nodeName)
}

// CordonNodes cordons multiple nodes.
func (tc *TestContext) CordonNodes(nodes []string) {
	tc.T.Helper()
	for _, nodeName := range nodes {
		if err := tc.CordonNode(nodeName); err != nil {
			tc.T.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}
}

// UncordonNodes uncordons multiple nodes.
func (tc *TestContext) UncordonNodes(nodes []string) {
	tc.T.Helper()
	for _, nodeName := range nodes {
		if err := tc.UncordonNode(nodeName); err != nil {
			tc.T.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}
}

// GetWorkerNodes retrieves the names of all worker nodes in the cluster.
func (tc *TestContext) GetWorkerNodes() ([]string, error) {
	return tc.newNodeManager().GetWorkerNodes(tc.Ctx)
}

// ScalePCS scales a PodCliqueSet to the specified replica count.
func (tc *TestContext) ScalePCS(name string, replicas int) error {
	return tc.newWorkloadManager().ScalePCS(tc.Ctx, tc.Namespace, name, replicas)
}

// ScalePCSG scales a PodCliqueScalingGroup to the specified replica count.
func (tc *TestContext) ScalePCSG(name string, replicas int) error {
	return tc.newWorkloadManager().ScalePCSG(tc.Ctx, tc.Namespace, name, replicas, tc.Timeout, tc.Interval)
}

// ApplyYAMLFile applies a YAML file to the cluster.
func (tc *TestContext) ApplyYAMLFile(yamlPath string) ([]resources.AppliedResource, error) {
	return tc.newResourceManager().ApplyYAMLFile(tc.Ctx, yamlPath, tc.Namespace)
}

// DeployAndVerifyWorkload applies a workload YAML and waits for the expected pod count.
func (tc *TestContext) DeployAndVerifyWorkload() (*v1.PodList, error) {
	tc.T.Helper()
	if tc.Workload == nil {
		return nil, fmt.Errorf("tc.Workload is nil, must be set before calling DeployAndVerifyWorkload")
	}

	_, err := tc.ApplyYAMLFile(tc.Workload.YAMLPath)
	if err != nil {
		return nil, fmt.Errorf("failed to apply workload YAML: %w", err)
	}

	pods, err := tc.WaitForPodCount(tc.Workload.ExpectedPods)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for pods to be created: %w", err)
	}

	return pods, nil
}

// VerifyAllPodsArePending verifies that all pods matching the label selector are pending.
func (tc *TestContext) VerifyAllPodsArePending() error {
	return tc.PollForCondition(func() (bool, error) {
		pods, err := tc.ListPods()
		if err != nil {
			return false, err
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase != v1.PodPending {
				return false, nil
			}
		}
		return true, nil
	})
}

// VerifyPodsArePendingWithUnschedulableEvents verifies that pods are pending with Unschedulable events.
func (tc *TestContext) VerifyPodsArePendingWithUnschedulableEvents(allPodsMustBePending bool, expectedPendingCount int) error {
	if allPodsMustBePending {
		if err := tc.VerifyAllPodsArePending(); err != nil {
			return fmt.Errorf("not all pods are pending: %w", err)
		}
	}

	return tc.PollForCondition(func() (bool, error) {
		pods, err := tc.ListPods()
		if err != nil {
			return false, err
		}

		podsWithUnschedulableEvent := 0
		pendingCount := 0

		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodPending {
				pendingCount++

				events, err := tc.Clients.Clientset.CoreV1().Events(tc.Namespace).List(tc.Ctx, metav1.ListOptions{
					FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", pod.Name),
				})
				if err != nil {
					return false, err
				}

				var mostRecentEvent *v1.Event
				for i := range events.Items {
					event := &events.Items[i]
					if mostRecentEvent == nil || event.LastTimestamp.After(mostRecentEvent.LastTimestamp.Time) {
						mostRecentEvent = event
					}
				}

				if mostRecentEvent != nil &&
					mostRecentEvent.Type == v1.EventTypeWarning &&
					((mostRecentEvent.Reason == "Unschedulable" && mostRecentEvent.Source.Component == "kai-scheduler") ||
						(mostRecentEvent.Reason == "PodGrouperWarning" && mostRecentEvent.Source.Component == "pod-grouper")) {
					podsWithUnschedulableEvent++
				}
			}
		}

		if expectedPendingCount > 0 && pendingCount != expectedPendingCount {
			return false, nil
		}

		return podsWithUnschedulableEvent == pendingCount, nil
	})
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

// ListPodsAndAssertDistinctNodes lists pods and asserts they are on distinct nodes.
func (tc *TestContext) ListPodsAndAssertDistinctNodes() {
	tc.T.Helper()
	pods, err := tc.ListPods()
	if err != nil {
		tc.T.Fatalf("Failed to list workload pods: %v", err)
	}
	assertPodsOnDistinctNodes(tc.T, pods.Items)
}

// SetupAndCordonNodes retrieves worker nodes and cordons the specified number.
func (tc *TestContext) SetupAndCordonNodes(numToCordon int) []string {
	tc.T.Helper()

	workerNodes, err := tc.GetWorkerNodes()
	if err != nil {
		tc.T.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < numToCordon {
		tc.T.Fatalf("expected at least %d worker nodes to cordon, but found %d", numToCordon, len(workerNodes))
	}

	nodesToCordon := workerNodes[:numToCordon]
	tc.CordonNodes(nodesToCordon)

	return nodesToCordon
}

// UncordonNodesAndWaitForPods uncordons nodes and waits for pods to be ready.
func (tc *TestContext) UncordonNodesAndWaitForPods(nodes []string, expectedPods int) {
	tc.T.Helper()
	tc.UncordonNodes(nodes)
	if err := tc.WaitForPods(expectedPods); err != nil {
		tc.T.Fatalf("Failed to wait for pods to be ready: %v", err)
	}
}

// VerifyAllPodsArePendingWithSleep verifies all pods are pending after a fixed delay.
func (tc *TestContext) VerifyAllPodsArePendingWithSleep() {
	tc.T.Helper()
	time.Sleep(30 * time.Second)
	if err := tc.VerifyAllPodsArePending(); err != nil {
		tc.T.Fatalf("Failed to verify all pods are pending: %v", err)
	}
}

// WaitForPodConditions polls until the expected pod state is reached.
func (tc *TestContext) WaitForPodConditions(expectedTotalPods, expectedPending int) (int, int, int, error) {
	var lastTotal, lastRunning, lastPending int

	err := tc.PollForCondition(func() (bool, error) {
		podList, err := tc.ListPods()
		if err != nil {
			return false, err
		}
		count := pods.CountPodsByPhase(podList)
		lastTotal = count.Total
		lastRunning = count.Running
		lastPending = count.Pending
		return lastTotal == expectedTotalPods && lastPending == expectedPending, nil
	})

	return lastTotal, lastRunning, lastPending, err
}

// ScalePCSAndWait scales a PCS and waits for the expected pod conditions.
func (tc *TestContext) ScalePCSAndWait(pcsName string, replicas int32, expectedTotalPods, expectedPending int) {
	tc.T.Helper()

	if err := tc.ScalePCS(pcsName, int(replicas)); err != nil {
		tc.T.Fatalf("Failed to scale PodCliqueSet %s: %v", pcsName, err)
	}

	totalPods, runningPods, pendingPods, err := tc.WaitForPodConditions(expectedTotalPods, expectedPending)
	if err != nil {
		tc.T.Fatalf("Failed to wait for expected pod conditions after PCS scaling: %v. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
			err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
	}
}

// ScalePCSGInstanceAndWait scales a specific PCSG instance and waits for expected pod conditions.
func (tc *TestContext) ScalePCSGInstanceAndWait(pcsgInstanceName string, replicas int32, expectedTotalPods, expectedPending int) {
	tc.T.Helper()

	if err := tc.ScalePCSG(pcsgInstanceName, int(replicas)); err != nil {
		tc.T.Fatalf("Failed to scale PodCliqueScalingGroup instance %s: %v", pcsgInstanceName, err)
	}

	totalPods, runningPods, pendingPods, err := tc.WaitForPodConditions(expectedTotalPods, expectedPending)
	if err != nil {
		tc.T.Fatalf("Failed to wait for expected pod conditions after PCSG instance scaling: %v. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
			err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
	}
}

// ScalePCSGAcrossAllReplicasAndWait scales a PCSG across all PCS replicas and waits.
func (tc *TestContext) ScalePCSGAcrossAllReplicasAndWait(pcsName, pcsgName string, pcsReplicas, pcsgReplicas int32, expectedTotalPods, expectedPending int) {
	tc.T.Helper()

	for replicaIndex := int32(0); replicaIndex < pcsReplicas; replicaIndex++ {
		pcsgInstanceName := fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, pcsgName)
		if err := tc.ScalePCSG(pcsgInstanceName, int(pcsgReplicas)); err != nil {
			tc.T.Fatalf("Failed to scale PodCliqueScalingGroup instance %s: %v", pcsgInstanceName, err)
		}
	}

	totalPods, runningPods, pendingPods, err := tc.WaitForPodConditions(expectedTotalPods, expectedPending)
	if err != nil {
		tc.T.Fatalf("Failed to wait for expected pod conditions after PCSG scaling across all replicas: %v. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
			err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
	}
}

// ScalePCSAsync scales a PCS asynchronously and returns an error channel.
func (tc *TestContext) ScalePCSAsync(pcsName string, replicas int32, expectedTotalPods, expectedPending, delayMs int) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		startTime := time.Now()

		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		if err := tc.ScalePCS(pcsName, int(replicas)); err != nil {
			errCh <- fmt.Errorf("failed to scale PodCliqueSet %s: %w", pcsName, err)
			return
		}

		totalPods, runningPods, pendingPods, err := tc.WaitForPodConditions(expectedTotalPods, expectedPending)
		elapsed := time.Since(startTime)
		if err != nil {
			Logger.Infof("[scalePCS] Scale %s FAILED after %v: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
				pcsName, elapsed, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
			errCh <- fmt.Errorf("failed to wait for expected pod conditions after PCS scaling: %w. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
				err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
			return
		}
		Logger.Infof("[scalePCS] Scale %s completed in %v (replicas=%d, pods=%d)", pcsName, elapsed, replicas, totalPods)
		errCh <- nil
	}()
	return errCh
}

// ScalePCSGAcrossAllReplicasAsync scales a PCSG across all PCS replicas asynchronously.
func (tc *TestContext) ScalePCSGAcrossAllReplicasAsync(pcsName, pcsgName string, pcsReplicas, pcsgReplicas int32, expectedTotalPods, expectedPending, delayMs int) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		startTime := time.Now()

		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		for replicaIndex := int32(0); replicaIndex < pcsReplicas; replicaIndex++ {
			pcsgInstanceName := fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, pcsgName)
			if err := tc.ScalePCSG(pcsgInstanceName, int(pcsgReplicas)); err != nil {
				errCh <- fmt.Errorf("failed to scale PodCliqueScalingGroup instance %s: %w", pcsgInstanceName, err)
				return
			}
		}

		totalPods, runningPods, pendingPods, err := tc.WaitForPodConditions(expectedTotalPods, expectedPending)
		elapsed := time.Since(startTime)
		if err != nil {
			Logger.Infof("[scalePCSGAcrossAllReplicas] Scale %s FAILED after %v: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
				pcsgName, elapsed, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
			errCh <- fmt.Errorf("failed to wait for expected pod conditions after PCSG scaling across all replicas: %w. Final state: total=%d, running=%d, pending=%d (expected: total=%d, pending=%d)",
				err, totalPods, runningPods, pendingPods, expectedTotalPods, expectedPending)
			return
		}
		Logger.Infof("[scalePCSGAcrossAllReplicas] Scale %s completed in %v (pcsgReplicas=%d, pods=%d)", pcsgName, elapsed, pcsgReplicas, totalPods)
		errCh <- nil
	}()
	return errCh
}
