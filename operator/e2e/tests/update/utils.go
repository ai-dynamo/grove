//go:build e2e

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

package update

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	common "github.com/ai-dynamo/grove/operator/api/common"
	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ============================================================================
// Common update utilities
// ============================================================================

// getPCS fetches a PodCliqueSet by name using the K8s client.
func getPCS(tc *testctx.TestContext, pcsName string) (*grovev1alpha1.PodCliqueSet, error) {
	var pcs grovev1alpha1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: pcsName, Namespace: tc.Namespace}, &pcs); err != nil {
		return nil, err
	}
	return &pcs, nil
}

// captureExistingPodNames returns a map of all current pod names for the workload.
func captureExistingPodNames(tc *testctx.TestContext) (map[string]bool, error) {
	pods, err := tc.ListPods()
	if err != nil {
		return nil, err
	}

	existingPodNames := make(map[string]bool, len(pods.Items))
	for _, pod := range pods.Items {
		existingPodNames[pod.Name] = true
	}

	tests.Logger.Debugf("Captured %d existing pods before spec change", len(existingPodNames))
	return existingPodNames, nil
}

// verifyPodHasUpdatedSpec verifies that a pod has the UPDATE_TRIGGER environment variable,
// indicating it was created with the updated spec.
func verifyPodHasUpdatedSpec(tc *testctx.TestContext, podName string) error {
	tc.T.Helper()

	var pod corev1.Pod
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: podName}, &pod); err != nil {
		return fmt.Errorf("failed to get pod %s: %w", podName, err)
	}

	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "UPDATE_TRIGGER" {
				return nil
			}
		}
	}

	return fmt.Errorf("pod %s does not have the UPDATE_TRIGGER environment variable", podName)
}

// deletePodAndWaitForTermination deletes a pod and waits until it is fully terminated.
func deletePodAndWaitForTermination(tc *testctx.TestContext, podName string) error {
	tc.T.Helper()

	if err := tc.Client.Delete(tc.Ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: tc.Namespace}}); err != nil {
		return fmt.Errorf("failed to delete pod %s: %w", podName, err)
	}

	pm := pods.NewPodManager(tc.Client, testctx.Logger)
	w := waiter.New[*corev1.PodList]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval)
	_, err := w.WaitFor(tc.Ctx, pm.FetchFunc(tc.Ctx, tc.Namespace, tc.GetLabelSelector()), pods.ExcludesPod(podName))
	if err != nil {
		return fmt.Errorf("failed to wait for the pod %s to be terminated: %w", podName, err)
	}

	tests.Logger.Debugf("Deleted pod %s, waiting for replacement...", podName)
	return nil
}

// getPodsForClique returns the names of all pods belonging to the specified clique.
func getPodsForClique(tc *testctx.TestContext, cliqueName string) ([]string, error) {
	tc.T.Helper()

	pods, err := tc.ListPods()
	if err != nil {
		return nil, err
	}

	var cliquePods []string
	for _, pod := range pods.Items {
		if pod.Labels == nil {
			continue
		}
		if pclq, ok := pod.Labels[common.LabelPodClique]; ok && strings.HasSuffix(pclq, "-"+cliqueName) {
			cliquePods = append(cliquePods, pod.Name)
		}
	}

	return cliquePods, nil
}

// getFirstPodForClique returns the name of the first pod belonging to the specified clique.
func getFirstPodForClique(tc *testctx.TestContext, cliqueName string) (string, error) {
	cliquePods, err := getPodsForClique(tc, cliqueName)
	if err != nil {
		return "", err
	}
	if len(cliquePods) == 0 {
		return "", fmt.Errorf("no pods found for %s", cliqueName)
	}
	return cliquePods[0], nil
}

// findFirstNewPodName returns the name of the first pod not present in the given set.
func findFirstNewPodName(tc *testctx.TestContext, existingPodNames map[string]bool) (string, error) {
	pods, err := tc.ListPods()
	if err != nil {
		return "", err
	}

	for _, pod := range pods.Items {
		if !existingPodNames[pod.Name] {
			return pod.Name, nil
		}
	}

	return "", fmt.Errorf("could not find newly created replacement pod")
}

// getPodsOnNode returns the names of pods scheduled on the specified node.
func getPodsOnNode(tc *testctx.TestContext, nodeName string) ([]string, error) {
	tc.T.Helper()

	pods, err := tc.ListPods()
	if err != nil {
		return nil, err
	}

	var podsOnNode []string
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == nodeName {
			podsOnNode = append(podsOnNode, pod.Name)
		}
	}

	return podsOnNode, nil
}

// getNodeForPod returns the node name where the specified pod is scheduled.
func getNodeForPod(tc *testctx.TestContext, podName string) (string, error) {
	tc.T.Helper()

	var pod corev1.Pod
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: podName}, &pod); err != nil {
		return "", fmt.Errorf("failed to get pod %s: %w", podName, err)
	}
	if pod.Spec.NodeName == "" {
		return "", fmt.Errorf("pod %s has no node assignment", podName)
	}
	return pod.Spec.NodeName, nil
}

// identifyHealthyNodePods returns the set of pod names that are NOT on the specified node.
func identifyHealthyNodePods(allPods map[string]bool, podsOnExcludedNode []string) map[string]bool {
	healthyNodePods := make(map[string]bool)
	excludedSet := make(map[string]bool, len(podsOnExcludedNode))
	for _, name := range podsOnExcludedNode {
		excludedSet[name] = true
	}
	for name := range allPods {
		if !excludedSet[name] {
			healthyNodePods[name] = true
		}
	}
	return healthyNodePods
}

// verifyPodNotOnNode asserts that the specified pod is not scheduled on the given node.
func verifyPodNotOnNode(tc *testctx.TestContext, podName string, excludedNodeName string) {
	tc.T.Helper()

	nodeName, err := getNodeForPod(tc, podName)
	if err != nil {
		tc.T.Fatalf("Failed to get node for pod %s: %v", podName, err)
	}
	if nodeName == excludedNodeName {
		tc.T.Fatalf("Pod %s is scheduled on excluded node %s — nodeAffinity exclusion not enforced", podName, excludedNodeName)
	}
	tests.Logger.Debugf("Pod %s is on node %s (not on excluded node %s)", podName, nodeName, excludedNodeName)
}

// verifyPodsStillPresent asserts that all pods in the given set are still present in the cluster.
func verifyPodsStillPresent(tc *testctx.TestContext, expectedPods map[string]bool) {
	tc.T.Helper()

	pods, err := tc.ListPods()
	if err != nil {
		tc.T.Fatalf("Failed to list pods: %v", err)
	}

	currentPods := make(map[string]bool, len(pods.Items))
	for _, pod := range pods.Items {
		currentPods[pod.Name] = true
	}

	for podName := range expectedPods {
		if !currentPods[podName] {
			tc.T.Fatalf("Pod %s was disrupted (deleted/recreated) — update strategy violated", podName)
		}
	}
}

// updatePCSUpdateStrategy changes the update strategy type of the PodCliqueSet.
// Uses tc.Workload.Name as the PCS name.
func updatePCSUpdateStrategy(tc *testctx.TestContext, strategyType grovev1alpha1.UpdateStrategyType) error {
	tc.T.Helper()

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var pcs grovev1alpha1.PodCliqueSet
		if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: tc.Workload.Name, Namespace: tc.Namespace}, &pcs); err != nil {
			return fmt.Errorf("failed to fetch PodCliqueSet: %w", err)
		}
		pcs.SetGroupVersionKind(grovev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet"))
		pcs.SetManagedFields(nil)
		pcs.SetResourceVersion("")

		if pcs.Spec.UpdateStrategy == nil {
			pcs.Spec.UpdateStrategy = &grovev1alpha1.PodCliqueSetUpdateStrategy{}
		}
		pcs.Spec.UpdateStrategy.Type = strategyType

		return tc.Client.Patch(tc.Ctx, &pcs, client.Apply, client.FieldOwner("e2e-rolling-update-test"), client.ForceOwnership)
	})
}

// triggerPodCliqueUpdate triggers an update by adding/updating an environment variable in a PodClique.
// Uses tc.Workload.Name as the PCS name.
func triggerPodCliqueUpdate(tc *testctx.TestContext, cliqueName string) error {
	pcsName := tc.Workload.Name
	updateValue := fmt.Sprintf("%d", time.Now().UnixNano())

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var pcs grovev1alpha1.PodCliqueSet
		if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: pcsName, Namespace: tc.Namespace}, &pcs); err != nil {
			return fmt.Errorf("failed to get PodCliqueSet: %w", err)
		}
		pcs.SetGroupVersionKind(grovev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet"))
		pcs.SetManagedFields(nil)
		pcs.SetResourceVersion("")

		found := false
		for i, clique := range pcs.Spec.Template.Cliques {
			if clique.Name == cliqueName {
				if len(clique.Spec.PodSpec.Containers) > 0 {
					container := &pcs.Spec.Template.Cliques[i].Spec.PodSpec.Containers[0]
					envVarFound := false
					for j := range container.Env {
						if container.Env[j].Name == "UPDATE_TRIGGER" {
							container.Env[j].Value = updateValue
							envVarFound = true
							break
						}
					}
					if !envVarFound {
						container.Env = append(container.Env, corev1.EnvVar{
							Name:  "UPDATE_TRIGGER",
							Value: updateValue,
						})
					}
					found = true
				}
				break
			}
		}

		if !found {
			return fmt.Errorf("clique %s not found in PodCliqueSet %s", cliqueName, pcsName)
		}

		return tc.Client.Patch(tc.Ctx, &pcs, client.Apply, client.FieldOwner("e2e-rolling-update-test"), client.ForceOwnership)
	})
}

// patchPCSWithSIGTERMIgnoringCommand patches all containers in the PCS to use a command that ignores SIGTERM
// and sets the termination grace period to 5 seconds. This makes pods ignore graceful shutdown but still
// allows updates to progress in a reasonable time for testing.
func patchPCSWithSIGTERMIgnoringCommand(tc *testctx.TestContext) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var pcs grovev1alpha1.PodCliqueSet
		if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: tc.Workload.Name, Namespace: tc.Namespace}, &pcs); err != nil {
			return fmt.Errorf("failed to get PodCliqueSet: %w", err)
		}
		pcs.SetGroupVersionKind(grovev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet"))
		pcs.SetManagedFields(nil)
		pcs.SetResourceVersion("")

		terminationGracePeriod := int64(5)
		for i := range pcs.Spec.Template.Cliques {
			pcs.Spec.Template.Cliques[i].Spec.PodSpec.TerminationGracePeriodSeconds = &terminationGracePeriod
			for j := range pcs.Spec.Template.Cliques[i].Spec.PodSpec.Containers {
				container := &pcs.Spec.Template.Cliques[i].Spec.PodSpec.Containers[j]
				container.Command = []string{"/bin/sh", "-c", "trap '' TERM; sleep infinity"}
			}
		}

		return tc.Client.Patch(tc.Ctx, &pcs, client.Apply, client.FieldOwner("e2e-rolling-update-test"), client.ForceOwnership)
	})
}

// waitForRollingUpdateComplete waits for rolling update to complete by checking UpdatedReplicas.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the timeout (use a modified tc if a different timeout is needed).
func waitForRollingUpdateComplete(tc *testctx.TestContext, expectedReplicas int32) error {
	pcsName := tc.Workload.Name

	pollCount := 0
	fetchPCS := waiter.FetchByName(pcsName, k8sclient.Getter[*grovev1alpha1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1alpha1.PodCliqueSet](func(pcs *grovev1alpha1.PodCliqueSet) bool {
		pollCount++

		// Log status every few polls for debugging
		if pollCount%3 == 1 {
			tests.Logger.Debugf("[waitForRollingUpdateComplete] Poll #%d: UpdatedReplicas=%d, expectedReplicas=%d, RollingUpdateProgress=%v",
				pollCount, pcs.Status.UpdatedReplicas, expectedReplicas, pcs.Status.RollingUpdateProgress != nil)
			if pcs.Status.RollingUpdateProgress != nil {
				tests.Logger.Debugf("  UpdateStartedAt=%v, UpdateEndedAt=%v, CurrentlyUpdating=%v",
					pcs.Status.RollingUpdateProgress.UpdateStartedAt,
					pcs.Status.RollingUpdateProgress.UpdateEndedAt,
					pcs.Status.RollingUpdateProgress.CurrentlyUpdating)
			}
		}

		// Check if rolling update is complete:
		// - UpdatedReplicas should match expected
		// - RollingUpdateProgress should exist with UpdateEndedAt set (not nil)
		if pcs.Status.UpdatedReplicas == expectedReplicas &&
			pcs.Status.RollingUpdateProgress != nil &&
			pcs.Status.RollingUpdateProgress.UpdateEndedAt != nil {
			tests.Logger.Debugf("[waitForRollingUpdateComplete] Rolling update completed after %d polls", pollCount)
			return true
		}

		return false
	})
	w := waiter.New[*grovev1alpha1.PodCliqueSet]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval)
	err := w.WaitUntil(tc.Ctx, fetchPCS, predicate)
	return err
}

// scalePodCliqueInPCS scales all PodClique instances for a given clique name across all PCS replicas.
// Uses tc.Workload.Name as the PCS name.
//
// IMPORTANT: This function scales PodClique resources directly rather than modifying the PCS template.
//
// Why we can't scale via the PCS template:
// The PCS controller intentionally preserves existing PodClique replica counts to support HPA
// (Horizontal Pod Autoscaler) scaling. When a PodClique already exists, the controller does:
//
//	if pclqExists {
//	    currentPCLQReplicas := pclq.Spec.Replicas  // Preserve existing value
//	    pclq.Spec = pclqTemplateSpec.Spec          // Apply template
//	    pclq.Spec.Replicas = currentPCLQReplicas   // Restore preserved value
//	}
//
// This design prevents the PCS controller from fighting with HPA, which directly mutates
// PodClique.Spec.Replicas. Without this behavior, HPA scaling would be immediately reverted
// on the next PCS reconciliation.
//
// As a result, the PCS template's clique replicas value is only used during initial PodClique
// creation. Post-creation scaling must be done by:
//   - HPA (configured via ScaleConfig in the PCS template)
//   - Direct patching of PodClique resources (what this function does)
//
// See: internal/controller/podcliqueset/components/podclique/podclique.go buildResource()
func scalePodCliqueInPCS(tc *testctx.TestContext, cliqueName string, replicas int32) error {
	pcsName := tc.Workload.Name

	// Get the PCS to find out how many replicas it has
	var pcs grovev1alpha1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: pcsName, Namespace: tc.Namespace}, &pcs); err != nil {
		return fmt.Errorf("failed to get PodCliqueSet: %w", err)
	}

	// Verify the clique exists in the PCS template
	found := false
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique.Name == cliqueName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("clique %s not found in PodCliqueSet %s template", cliqueName, pcsName)
	}

	// Scale each PodClique instance directly (one per PCS replica)
	// PodClique naming convention: {pcsName}-{replicaIndex}-{cliqueName}
	for replicaIndex := range pcs.Spec.Replicas {
		pclqName := fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, cliqueName)

		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Patch the PodClique's replicas directly
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"replicas": replicas,
				},
			}
			patchBytes, err := json.Marshal(patch)
			if err != nil {
				return fmt.Errorf("failed to marshal patch: %w", err)
			}

			pclq := grovev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{Name: pclqName, Namespace: tc.Namespace},
			}
			return tc.Client.Patch(tc.Ctx, &pclq, client.RawPatch(types.MergePatchType, patchBytes))
		}); err != nil {
			return fmt.Errorf("failed to scale PodClique %s: %w", pclqName, err)
		}
	}

	return nil
}

// ============================================================================
// RollingRecreate strategy utilities
// ============================================================================

// triggerRollingUpdate triggers a rolling update on the specified cliques and returns a channel
// that receives an error (or nil) when the rolling update finishes.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the wait timeout.
func triggerRollingUpdate(tc *testctx.TestContext, expectedReplicas int32, cliqueNames ...string) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		startTime := time.Now()

		// Trigger synchronously first
		for _, cliqueName := range cliqueNames {
			if err := triggerPodCliqueUpdate(tc, cliqueName); err != nil {
				errCh <- fmt.Errorf("failed to update PodClique %s spec: %w", cliqueName, err)
				return
			}
		}
		tests.Logger.Debugf("[triggerRollingUpdate] Triggered update on %v, waiting for completion...", cliqueNames)

		// Wait for completion
		err := waitForRollingUpdateComplete(tc, expectedReplicas)
		elapsed := time.Since(startTime)
		if err != nil {
			tests.Logger.Debugf("[triggerRollingUpdate] Rolling update FAILED after %v: %v", elapsed, err)
		} else {
			tests.Logger.Debugf("[triggerRollingUpdate] Rolling update completed in %v", elapsed)
		}
		errCh <- err
	}()
	return errCh
}

// waitForRollingUpdate starts polling for rolling update completion in the background and returns a channel.
// Use this when you need to trigger an update separately (e.g., when doing something between trigger and wait).
// For the common case, use triggerRollingUpdate which combines trigger + wait.
func waitForRollingUpdate(tc *testctx.TestContext, expectedReplicas int32) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- waitForRollingUpdateComplete(tc, expectedReplicas)
	}()
	return errCh
}

// waitForOrdinalUpdating waits for a specific ordinal to start being updated during rolling update.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the timeout (use a modified tc if a different timeout is needed).
func waitForOrdinalUpdating(tc *testctx.TestContext, ordinal int32) error {
	pcsName := tc.Workload.Name

	pollCount := 0
	fetchPCS := waiter.FetchByName(pcsName, k8sclient.Getter[*grovev1alpha1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1alpha1.PodCliqueSet](func(pcs *grovev1alpha1.PodCliqueSet) bool {
		pollCount++

		// Log status every few polls for debugging
		if pollCount%3 == 1 {
			currentOrdinal := int32(-1)
			if pcs.Status.RollingUpdateProgress != nil && pcs.Status.RollingUpdateProgress.CurrentlyUpdating != nil {
				currentOrdinal = pcs.Status.RollingUpdateProgress.CurrentlyUpdating.ReplicaIndex
			}
			tests.Logger.Debugf("[waitForOrdinalUpdating] Poll #%d: waiting for ordinal %d, currently updating ordinal: %d",
				pollCount, ordinal, currentOrdinal)
		}

		// Check if the target ordinal is currently being updated
		if pcs.Status.RollingUpdateProgress != nil &&
			pcs.Status.RollingUpdateProgress.CurrentlyUpdating != nil &&
			pcs.Status.RollingUpdateProgress.CurrentlyUpdating.ReplicaIndex == ordinal {
			tests.Logger.Debugf("[waitForOrdinalUpdating] Ordinal %d started updating after %d polls", ordinal, pollCount)
			return true
		}

		return false
	})
	w := waiter.New[*grovev1alpha1.PodCliqueSet]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval)
	err := w.WaitUntil(tc.Ctx, fetchPCS, predicate)
	return err
}

// getPodIdentifier returns a stable identifier for a pod based on its logical position in the workload.
// This identifier remains the same when a pod is replaced during rolling updates.
//
// Stability is guaranteed because the pod's internal hostname (pod.Spec.Hostname) is set by the
// operator based on the pod's logical position in the PodClique (pclqName-podIndex), not the pod's
// generated name. Note: this is the Kubernetes pod hostname field (what `hostname` returns inside
// the container), NOT the node/host where the pod is scheduled.
//
// See: internal/controller/podclique/components/pod/pod.go - configurePodHostname()
//
// During a rolling update:
//  1. The old pod is deleted (e.g., pod.Spec.Hostname: "workload1-pc-a-0-0")
//  2. The PodClique remains unchanged (same name and replica count)
//  3. The new pod is created to fill the same logical position
//  4. The new pod receives the same pod.Spec.Hostname (e.g., "workload1-pc-a-0-0")
//
// Only the pod name changes (due to GenerateName), while pod.Spec.Hostname represents the pod's
// logical position in the workload hierarchy and remains constant.
func getPodIdentifier(tc *testctx.TestContext, pod *corev1.Pod) string {
	tc.T.Helper()

	// Use hostname as the stable identifier (set by configurePodHostname in pod.go)
	// Format: {pclqName}-{podIndex} (e.g., "workload1-pc-a-0-0")
	if pod.Spec.Hostname == "" {
		tc.T.Fatalf("Pod %s does not have hostname set, cannot determine stable identifier", pod.Name)
	}

	return pod.Spec.Hostname
}

// verifyOnePodDeletedAtATime verifies only one pod globally is being deleted at a time
// during rolling updates.
//
// This function handles the fact that Kubernetes watch events can arrive out of order.
// Specifically, the ADDED event for a replacement pod can arrive before the DELETE event
// for the old pod, even though the deletion was initiated first. This happens because:
// 1. The old pod is marked for deletion (DeletionTimestamp is set)
// 2. The controller creates a replacement pod while the old one is terminating
// 3. Watch events for both can arrive in any order
//
// To handle this, we track both ADDED and DELETE events and only consider a hostname
// as "actively deleting" if there's no replacement pod that was added.
func verifyOnePodDeletedAtATime(tc *testctx.TestContext, events []podEvent) {
	tc.T.Helper()

	// Log all events for debugging
	tests.Logger.Debug("=== Starting verifyOnePodDeletedAtATime analysis ===")
	tests.Logger.Debugf("Total events captured: %d", len(events))

	for i, event := range events {
		podclique := ""
		if event.pod.Labels != nil {
			podclique = event.pod.Labels[common.LabelPodClique]
		}
		tests.Logger.Debugf("Event[%d]: Type=%s PodName=%s Hostname=%s PodClique=%s Timestamp=%v",
			i, event.eventType, event.pod.Name, event.pod.Spec.Hostname, podclique, event.timestamp.Format("15:04:05.000"))
	}

	// Track the most recent ADDED pod for each hostname (podID).
	// Key: hostname, Value: pod name of the most recent ADDED event
	addedPods := make(map[string]string)

	// Track DELETE events for each hostname.
	// Key: hostname, Value: pod name of the deleted pod
	deletedPods := make(map[string]string)

	maxConcurrentDeletions := 0
	var violationEvents []string

	tests.Logger.Debug("=== Processing events to find concurrent deletions ===")
	for i, event := range events {
		podID := getPodIdentifier(tc, event.pod)
		podName := event.pod.Name

		switch event.eventType {
		case watch.Deleted:
			// Record the DELETE event for this hostname
			deletedPods[podID] = podName
			tests.Logger.Debugf("Event[%d] DELETE: podID=%s, podName=%s", i, podID, podName)
		case watch.Added:
			// Record the ADDED event for this hostname
			addedPods[podID] = podName
			tests.Logger.Debugf("Event[%d] ADDED: podID=%s, podName=%s", i, podID, podName)
		case watch.Modified:
			tests.Logger.Debugf("Event[%d] MODIFIED: podID=%s (ignored for deletion tracking)", i, podID)
		}

		// Calculate "actively deleting" hostnames: those with a DELETE event where
		// either no ADDED event exists, or the ADDED pod is the same as the deleted pod
		// (meaning the replacement hasn't been added yet)
		activelyDeleting := make(map[string]bool)
		for hostname, deletedPodName := range deletedPods {
			addedPodName, hasAdded := addedPods[hostname]
			// A hostname is "actively deleting" if:
			// 1. No replacement pod has been added, OR
			// 2. The added pod is the same as the deleted pod (no actual replacement)
			if !hasAdded || addedPodName == deletedPodName {
				activelyDeleting[hostname] = true
			}
		}

		currentlyDeleting := len(activelyDeleting)
		if currentlyDeleting > 0 {
			deletingKeys := make([]string, 0, len(activelyDeleting))
			for k := range activelyDeleting {
				deletingKeys = append(deletingKeys, k)
			}
			slices.Sort(deletingKeys)
			tests.Logger.Debugf("Event[%d] State: currentlyDeleting=%d, activelyDeleting=%v",
				i, currentlyDeleting, deletingKeys)
		}

		// Track the maximum number of concurrent deletions observed.
		if currentlyDeleting > maxConcurrentDeletions {
			maxConcurrentDeletions = currentlyDeleting
			deletingKeys := make([]string, 0, len(activelyDeleting))
			for k := range activelyDeleting {
				deletingKeys = append(deletingKeys, k)
			}
			slices.Sort(deletingKeys)
			violationEvents = append(violationEvents, fmt.Sprintf(
				"Event[%d]: maxConcurrentDeletions increased to %d, activelyDeleting=%v",
				i, maxConcurrentDeletions, deletingKeys))
		}
	}

	tests.Logger.Debugf("=== Analysis complete: maxConcurrentDeletions=%d ===", maxConcurrentDeletions)
	if len(violationEvents) > 0 {
		tests.Logger.Debug("Violation events:")
		for _, v := range violationEvents {
			tests.Logger.Debugf("  %s", v)
		}
	}

	// Assert that at most 1 pod was being deleted/replaced at any point in time,
	// which ensures the rolling update respects the MaxUnavailable=1 constraint at the global level.
	if maxConcurrentDeletions > 1 {
		tc.T.Fatalf("Expected at most 1 pod being deleted at a time, but found %d concurrent deletions", maxConcurrentDeletions)
	}
}

// verifyOnePodDeletedAtATimePerPodclique verifies only one pod per Podclique is being deleted at a time
// during rolling updates.
//
// This function processes a sequence of pod events and tracks the number of individual pods
// that are in a "deleting" state within each Podclique at any given time.
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// See verifyOnePCSGReplicaDeletedAtATime for detailed explanation.
func verifyOnePodDeletedAtATimePerPodclique(tc *testctx.TestContext, events []podEvent) {
	tc.T.Helper()

	// Track DELETE and ADDED counts separately per podID per PodClique to handle out-of-order events.
	// Map structure: podcliqueName -> podID -> count
	deletedCount := make(map[string]map[string]int)
	addedCount := make(map[string]map[string]int)
	maxConcurrentDeletionsPerPodclique := make(map[string]int)

	for _, event := range events {
		// All pods should have labels - if nil, that's a bug
		if event.pod.Labels == nil {
			tc.T.Fatalf("Pod %s has no labels, which indicates a bug in pod creation", event.pod.Name)
		}

		podcliqueName, ok := event.pod.Labels[common.LabelPodClique]
		if !ok {
			tc.T.Fatalf("Pod %s does not have grove.io/podclique label", event.pod.Name)
		}

		podID := getPodIdentifier(tc, event.pod)

		switch event.eventType {
		case watch.Deleted:
			if deletedCount[podcliqueName] == nil {
				deletedCount[podcliqueName] = make(map[string]int)
			}
			deletedCount[podcliqueName][podID]++
		case watch.Added:
			if addedCount[podcliqueName] == nil {
				addedCount[podcliqueName] = make(map[string]int)
			}
			addedCount[podcliqueName][podID]++
		}

		// Calculate "actively deleting" pods per PodClique: those where DELETE count > ADDED count.
		for podclique, pcDeleted := range deletedCount {
			activelyDeleting := 0
			for podID, deletes := range pcDeleted {
				adds := 0
				if addedCount[podclique] != nil {
					adds = addedCount[podclique][podID]
				}
				if deletes > adds {
					activelyDeleting++
				}
			}
			if activelyDeleting > maxConcurrentDeletionsPerPodclique[podclique] {
				maxConcurrentDeletionsPerPodclique[podclique] = activelyDeleting
			}
		}
	}

	// Assert that at most 1 pod per Podclique was in the deletion-to-creation window at any point in time,
	// which ensures the rolling update deletes pods sequentially within each Podclique.
	for podclique, maxDeletions := range maxConcurrentDeletionsPerPodclique {
		if maxDeletions > 1 {
			tc.T.Fatalf("Expected at most 1 pod being deleted at a time in Podclique %s, but found %d concurrent deletions", podclique, maxDeletions)
		}
	}
}

// verifySinglePCSReplicaUpdatedFirst verifies that a single PCS replica is fully updated before another replica begins.
//
// This function ensures strict replica-level ordering during PodCliqueSet rolling updates:
// - Once a replica starts updating (any pod is deleted), NO other replica can start updating
// - The updating replica must complete ALL pod updates across ALL its PodCliques before another replica begins
// - A replica is considered "complete" only when all pods across all PodCliques have been deleted and re-added
//
// Background:
// A PodCliqueSet (PCS) can have multiple replicas, where each replica contains multiple PodCliques.
// For example, with replicas=2, you might have:
//   - Replica 0: pc-a (2 pods), pc-b (1 pod), pc-c (3 pods) = 6 total pods
//   - Replica 1: pc-a (2 pods), pc-b (1 pod), pc-c (3 pods) = 6 total pods
//
// During a rolling update, the system should update Replica 0 completely (all 6 pods across all PodCliques)
// before starting any updates to Replica 1.
//
// Implementation Details:
// - Tracks DELETE and ADDED counts separately per pod identifier per replica to handle out-of-order events
// - Uses stable pod identifiers (hostname) to correlate pod deletions with additions
// - A pod is "in-flight" (being updated) if DELETE count > ADDED count for that pod
// - A replica is "actively updating" if it has any pods in-flight
// - Only considers Deleted/Added events; Modified events are ignored as they don't affect ordering
//
// Note: This verification is most meaningful when PCS has replicas > 1. For replicas=1, it provides
// minimal validation since there's only one replica to update.
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// Specifically, ADDED events for new pods can arrive BEFORE DELETE events for old pods because:
// 1. Old pods are deleted (start terminating with deletion timestamp)
// 2. New pods are created immediately (new pods are ADDED)
// 3. DELETE events only arrive when old pods are fully terminated (after grace period)
//
// To handle this, we track both DELETE and ADDED counts and calculate "actively updating"
// replicas based on the difference (DELETE > ADDED means the pod is still in-flight).
func verifySinglePCSReplicaUpdatedFirst(tc *testctx.TestContext, events []podEvent) {
	tc.T.Helper()

	tests.Logger.Debug("=== Starting verifySinglePCSReplicaUpdatedFirst analysis ===")
	tests.Logger.Debugf("Total events captured: %d", len(events))

	// Log all events for debugging
	for i, event := range events {
		replicaIdx := 0
		if val, ok := event.pod.Labels[common.LabelPodCliqueSetReplicaIndex]; ok {
			replicaIdx, _ = strconv.Atoi(val)
		}
		podclique := ""
		if event.pod.Labels != nil {
			podclique = event.pod.Labels[common.LabelPodClique]
		}
		tests.Logger.Debugf("Event[%d]: Type=%s PodName=%s Hostname=%s Replica=%d PodClique=%s Timestamp=%v",
			i, event.eventType, event.pod.Name, event.pod.Spec.Hostname, replicaIdx, podclique, event.timestamp.Format("15:04:05.000"))
	}

	// Track DELETE and ADDED counts separately per pod identifier per replica to handle out-of-order events.
	// Structure: replicaIndex -> podID -> count
	deletedCount := make(map[int]map[string]int)
	addedCount := make(map[int]map[string]int)

	maxConcurrentUpdatingReplicas := 0
	var violationEvent string

	tests.Logger.Debug("=== Processing events to find concurrent replica updates ===")
	for i, event := range events {
		// Extract replica index from pod labels (defaults to 0 if not present)
		replicaIdx := 0
		if val, ok := event.pod.Labels[common.LabelPodCliqueSetReplicaIndex]; ok {
			replicaIdx, _ = strconv.Atoi(val)
		}

		// Extract PodClique name - skip pods without this label
		_, ok := event.pod.Labels[common.LabelPodClique]
		if !ok {
			continue
		}

		// Get stable pod identifier (hostname) that persists across pod replacements
		podID := getPodIdentifier(tc, event.pod)

		// Track DELETE and ADDED events
		switch event.eventType {
		case watch.Deleted:
			if deletedCount[replicaIdx] == nil {
				deletedCount[replicaIdx] = make(map[string]int)
			}
			deletedCount[replicaIdx][podID]++
			tests.Logger.Debugf("Event[%d] DELETE: replica=%d podID=%s deleted=%d added=%d",
				i, replicaIdx, podID, deletedCount[replicaIdx][podID], getReplicaPodCount(addedCount, replicaIdx, podID))

		case watch.Added:
			if addedCount[replicaIdx] == nil {
				addedCount[replicaIdx] = make(map[string]int)
			}
			addedCount[replicaIdx][podID]++
			tests.Logger.Debugf("Event[%d] ADDED: replica=%d podID=%s deleted=%d added=%d",
				i, replicaIdx, podID, getReplicaPodCount(deletedCount, replicaIdx, podID), addedCount[replicaIdx][podID])
		}

		// Calculate which replicas are "actively updating" - those with any pods where DELETE > ADDED
		// A pod is "in-flight" if it has been deleted but not yet replaced (DELETE count > ADDED count)
		activelyUpdating := make(map[int]int) // replicaIdx -> count of pods in-flight
		for replica, deletedPods := range deletedCount {
			inFlight := 0
			for podID, deletes := range deletedPods {
				adds := getReplicaPodCount(addedCount, replica, podID)
				if deletes > adds {
					inFlight++
				}
			}
			if inFlight > 0 {
				activelyUpdating[replica] = inFlight
			}
		}

		numUpdatingReplicas := len(activelyUpdating)
		if numUpdatingReplicas > 0 {
			replicas := make([]string, 0, len(activelyUpdating))
			for idx, count := range activelyUpdating {
				replicas = append(replicas, fmt.Sprintf("replica-%d(%d pods)", idx, count))
			}
			slices.Sort(replicas)
			tests.Logger.Debugf("Event[%d] State: numUpdatingReplicas=%d activelyUpdating=%v",
				i, numUpdatingReplicas, replicas)
		}

		if numUpdatingReplicas > maxConcurrentUpdatingReplicas {
			maxConcurrentUpdatingReplicas = numUpdatingReplicas
			replicas := make([]string, 0, len(activelyUpdating))
			for idx, count := range activelyUpdating {
				replicas = append(replicas, fmt.Sprintf("replica-%d(%d pods)", idx, count))
			}
			slices.Sort(replicas)
			violationEvent = fmt.Sprintf("Event[%d] %s: maxConcurrentUpdatingReplicas increased to %d, activelyUpdating=%v",
				i, event.eventType, maxConcurrentUpdatingReplicas, replicas)
		}
	}

	tests.Logger.Debugf("=== Analysis complete: maxConcurrentUpdatingReplicas=%d ===", maxConcurrentUpdatingReplicas)
	if violationEvent != "" {
		tests.Logger.Debugf("Max concurrent replicas event: %s", violationEvent)
	}

	// Assert that at most 1 replica was being updated at any point in time.
	// This ensures the rolling update respects replica-level serialization.
	if maxConcurrentUpdatingReplicas > 1 {
		tc.T.Fatalf("Expected at most 1 PCS replica to be updating at a time, but found %d replicas updating concurrently. %s",
			maxConcurrentUpdatingReplicas, violationEvent)
	}
}

// getReplicaPodCount safely gets a count from a nested map (replicaIdx -> podID -> count), returning 0 if not found
func getReplicaPodCount(m map[int]map[string]int, replicaIdx int, podID string) int {
	if m[replicaIdx] == nil {
		return 0
	}
	return m[replicaIdx][podID]
}

// verifyOnePCSGReplicaDeletedAtATime verifies only one PCSG replica globally is deleted at a time
// during rolling updates.
//
// SCOPE: GLOBAL across all PCSGs
// This is the strictest constraint - only ONE PCSG replica in the entire system can be rolling at any time.
// If you have multiple PCSGs (e.g., sg-x and sg-y), only one replica from any of them can be rolling.
//
// Compare with verifyOnePCSGReplicaDeletedAtATimePerPCSG which allows concurrent rolling across different PCSGs.
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// Specifically, ADDED events for new pods can arrive BEFORE DELETE events for old pods because:
// 1. Old PodCliques are deleted (pods start terminating with deletion timestamp)
// 2. New PodCliques are created immediately (new pods are ADDED)
// 3. DELETE events only arrive when old pods are fully terminated (after grace period)
//
// The controller considers a replica "update complete" when new pods are READY, not when old
// pods are fully terminated. So we track by comparing ADDED vs DELETE counts, allowing
// ADDED to offset DELETE regardless of order.
func verifyOnePCSGReplicaDeletedAtATime(tc *testctx.TestContext, events []podEvent) {
	tc.T.Helper()

	tests.Logger.Debug("=== Starting verifyOnePCSGReplicaDeletedAtATime analysis ===")
	tests.Logger.Debugf("Total events captured: %d", len(events))

	// Log all PCSG-related events for debugging
	for i, event := range events {
		if event.pod.Labels == nil {
			continue
		}
		replicaID, hasReplicaLabel := event.pod.Labels[common.LabelPodCliqueScalingGroupReplicaIndex]
		if !hasReplicaLabel {
			continue
		}
		pcsgName := event.pod.Labels[common.LabelPodCliqueScalingGroup]
		podclique := event.pod.Labels[common.LabelPodClique]
		tests.Logger.Debugf("Event[%d]: Type=%s PodName=%s PCSG=%s ReplicaID=%s PodClique=%s Timestamp=%v",
			i, event.eventType, event.pod.Name, pcsgName, replicaID, podclique, event.timestamp.Format("15:04:05.000"))
	}

	// Track DELETE and ADDED counts separately per replica to handle out-of-order events.
	// Key format: "<pcsg-name>-<replica-index>"
	deletedCount := make(map[string]int)
	addedCount := make(map[string]int)
	maxConcurrentDeletions := 0
	var violationEvent string

	tests.Logger.Debug("=== Processing events to find concurrent deletions ===")
	for i, event := range events {
		// All pods should have labels - if nil, that's a bug
		if event.pod.Labels == nil {
			tc.T.Fatalf("Pod %s has no labels, which indicates a bug in pod creation", event.pod.Name)
		}

		// Only process pods that belong to a PCSG (not all pods have this label)
		replicaID, hasReplicaLabel := event.pod.Labels[common.LabelPodCliqueScalingGroupReplicaIndex]
		if !hasReplicaLabel {
			continue
		}

		pcsgName, ok := event.pod.Labels[common.LabelPodCliqueScalingGroup]
		if !ok {
			tc.T.Fatalf("Pod %s has PCSG replica index but no PCSG name label", event.pod.Name)
		}

		// Create a composite key to uniquely identify this PCSG replica globally
		replicaKey := fmt.Sprintf("%s-%s", pcsgName, replicaID)

		switch event.eventType {
		case watch.Deleted:
			deletedCount[replicaKey]++
			tests.Logger.Debugf("Event[%d] DELETE: pod=%s replica=%s deleted=%d added=%d",
				i, event.pod.Name, replicaKey, deletedCount[replicaKey], addedCount[replicaKey])
		case watch.Added:
			addedCount[replicaKey]++
			tests.Logger.Debugf("Event[%d] ADDED: pod=%s replica=%s deleted=%d added=%d",
				i, event.pod.Name, replicaKey, deletedCount[replicaKey], addedCount[replicaKey])
		}

		// Calculate "actively rolling" replicas: those where DELETE count > ADDED count.
		// This means more pods have been deleted than replaced, so the replica is still rolling.
		activelyRolling := make(map[string]int)
		for replica, deletes := range deletedCount {
			adds := addedCount[replica]
			if deletes > adds {
				activelyRolling[replica] = deletes - adds
			}
		}

		currentConcurrent := len(activelyRolling)
		if currentConcurrent > 0 {
			replicas := make([]string, 0, len(activelyRolling))
			for k, v := range activelyRolling {
				replicas = append(replicas, fmt.Sprintf("%s:%d", k, v))
			}
			slices.Sort(replicas)
			tests.Logger.Debugf("Event[%d] State: concurrent=%d activelyRolling=%v", i, currentConcurrent, replicas)
		}
		if currentConcurrent > maxConcurrentDeletions {
			maxConcurrentDeletions = currentConcurrent
			replicas := make([]string, 0, len(activelyRolling))
			for k, v := range activelyRolling {
				replicas = append(replicas, fmt.Sprintf("%s:%d", k, v))
			}
			slices.Sort(replicas)
			violationEvent = fmt.Sprintf("Event[%d] maxConcurrentDeletions increased to %d, activelyRolling=%v", i, maxConcurrentDeletions, replicas)
		}
	}

	tests.Logger.Debugf("=== Analysis complete: maxConcurrentDeletions=%d ===", maxConcurrentDeletions)
	if violationEvent != "" {
		tests.Logger.Debugf("Violation: %s", violationEvent)
	}

	// Assert that at most 1 replica was being deleted/replaced at any point in time.
	// This ensures the rolling update respects the MaxUnavailable=1 constraint at the global level.
	if maxConcurrentDeletions > 1 {
		tc.T.Fatalf("Expected at most 1 PCSG replica being deleted at a time, but found %d concurrent deletions", maxConcurrentDeletions)
	}
}

// verifyOnePCSGReplicaDeletedAtATimePerPCSG verifies only one replica per PCSG is deleted at a time
// during rolling updates.
//
// SCOPE: PER-PCSG (allows concurrent rolling across different PCSGs)
// This constraint allows multiple PCSGs to roll simultaneously, but within each PCSG, only one replica
// can be rolling at a time. For example, if you have sg-x and sg-y:
//   - ALLOWED: sg-x replica 0 AND sg-y replica 0 rolling simultaneously
//   - NOT ALLOWED: sg-x replica 0 AND sg-x replica 1 rolling simultaneously
//
// Compare with verifyOnePCSGReplicaDeletedAtATime which enforces a global constraint (stricter).
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// See verifyOnePCSGReplicaDeletedAtATime for detailed explanation.
func verifyOnePCSGReplicaDeletedAtATimePerPCSG(tc *testctx.TestContext, events []podEvent) {
	tc.T.Helper()

	// Track DELETE and ADDED counts separately per replica per PCSG to handle out-of-order events.
	// Map structure: pcsgName -> replicaIndex -> count
	deletedCount := make(map[string]map[string]int)
	addedCount := make(map[string]map[string]int)
	maxConcurrentDeletionsPerPCSG := make(map[string]int)

	for _, event := range events {
		// All pods should have labels - if nil, that's a bug
		if event.pod.Labels == nil {
			tc.T.Fatalf("Pod %s has no labels, which indicates a bug in pod creation", event.pod.Name)
		}

		// Only process pods that belong to a PCSG (not all pods have this label)
		replicaID, hasReplicaLabel := event.pod.Labels[common.LabelPodCliqueScalingGroupReplicaIndex]
		if !hasReplicaLabel {
			continue
		}

		pcsgName, ok := event.pod.Labels[common.LabelPodCliqueScalingGroup]
		if !ok {
			tc.T.Fatalf("Pod %s has PCSG replica index but no PCSG name label", event.pod.Name)
		}

		switch event.eventType {
		case watch.Deleted:
			if deletedCount[pcsgName] == nil {
				deletedCount[pcsgName] = make(map[string]int)
			}
			deletedCount[pcsgName][replicaID]++
			tests.Logger.Debugf("Pod %s deleted, replica %s in PCSG %s: deleted=%d added=%d",
				event.pod.Name, replicaID, pcsgName,
				deletedCount[pcsgName][replicaID], getCount(addedCount, pcsgName, replicaID))
		case watch.Added:
			if addedCount[pcsgName] == nil {
				addedCount[pcsgName] = make(map[string]int)
			}
			addedCount[pcsgName][replicaID]++
			tests.Logger.Debugf("Pod %s added, replica %s in PCSG %s: deleted=%d added=%d",
				event.pod.Name, replicaID, pcsgName,
				getCount(deletedCount, pcsgName, replicaID), addedCount[pcsgName][replicaID])
		}

		// Calculate "actively rolling" replicas per PCSG: those where DELETE count > ADDED count.
		for pcsg, pcsgDeleted := range deletedCount {
			activelyRolling := 0
			for replica, deletes := range pcsgDeleted {
				adds := getCount(addedCount, pcsg, replica)
				if deletes > adds {
					activelyRolling++
				}
			}
			if activelyRolling > maxConcurrentDeletionsPerPCSG[pcsg] {
				maxConcurrentDeletionsPerPCSG[pcsg] = activelyRolling
			}
		}
	}

	// Assert that at most 1 replica per PCSG was being deleted/replaced at any point in time.
	// This ensures the rolling update respects the MaxUnavailable=1 constraint at the PCSG level.
	for pcsg, maxDeletions := range maxConcurrentDeletionsPerPCSG {
		if maxDeletions > 1 {
			tc.T.Fatalf("Expected at most 1 replica being deleted at a time in PCSG %s, but found %d concurrent deletions", pcsg, maxDeletions)
		}
	}
}

// getCount safely gets a count from a nested map, returning 0 if not found
func getCount(m map[string]map[string]int, key1, key2 string) int {
	if m[key1] == nil {
		return 0
	}
	return m[key1][key2]
}

// scalePodClique scales a PodClique and returns a channel that receives an error when the expected pod count is reached.
// Uses tc.Workload.Name as the PCS name.
// The operation runs asynchronously - receive from the returned channel to block until complete.
// If delayMs > 0, the operation will sleep for that duration before starting.
func scalePodClique(tc *testctx.TestContext, cliqueName string, replicas int32, expectedTotalPods, delayMs int) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		startTime := time.Now()

		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		tests.Logger.Debugf("[scalePodClique] Scaling %s to %d replicas, expecting %d total pods", cliqueName, replicas, expectedTotalPods)

		if err := scalePodCliqueInPCS(tc, cliqueName, replicas); err != nil {
			errCh <- fmt.Errorf("failed to scale PodClique %s: %w", cliqueName, err)
			return
		}

		tests.Logger.Debugf("[scalePodClique] Scale patch applied, waiting for pods...")

		// Wait for pods to reach expected count
		pm := pods.NewPodManager(tc.Client, tests.Logger)
		_, err := pm.WaitForCount(tc.Ctx, tc.Namespace, tc.GetLabelSelector(), expectedTotalPods, tc.Timeout, tc.Interval)
		elapsed := time.Since(startTime)
		if err != nil {
			tests.Logger.Debugf("[scalePodClique] Scale %s FAILED after %v: %v", cliqueName, elapsed, err)
			errCh <- fmt.Errorf("failed to wait for pods after scaling PodClique %s: %w", cliqueName, err)
			return
		}
		tests.Logger.Debugf("[scalePodClique] Scale %s completed in %v (pods=%d)", cliqueName, elapsed, expectedTotalPods)
		errCh <- nil
	}()
	return errCh
}

// ============================================================================
// OnDelete strategy utilities
// ============================================================================

func waitForOnDeleteUpdateComplete(tc *testctx.TestContext) error {
	pollCount := 0
	fetchPCS := waiter.FetchFunc[*grovev1alpha1.PodCliqueSet](func(_ context.Context) (*grovev1alpha1.PodCliqueSet, error) {
		return getPCS(tc, tc.Workload.Name)
	})
	predicate := waiter.Predicate[*grovev1alpha1.PodCliqueSet](func(pcs *grovev1alpha1.PodCliqueSet) bool {
		pollCount++
		if workload.IsOnDeleteUpdateComplete(pcs) {
			tests.Logger.Debugf("[waitForOnDeleteUpdateComplete] OnDelete update marked complete after %d polls (UpdatedReplicas=%d)",
				pollCount, pcs.Status.UpdatedReplicas)
			return true
		}
		return false
	})
	w := waiter.New[*grovev1alpha1.PodCliqueSet]().WithTimeout(tc.Timeout).WithInterval(tc.Interval)
	_, err := w.WaitFor(tc.Ctx, fetchPCS, predicate)
	return err
}

func verifyUpdateProgressFields(tc *testctx.TestContext) {
	tc.T.Helper()

	pcs, err := getPCS(tc, tc.Workload.Name)
	if err != nil {
		tc.T.Fatalf("Failed to get PCS for UpdateProgress: %v", err)
	}
	updateProgress := pcs.Status.RollingUpdateProgress

	if updateProgress == nil {
		tc.T.Fatalf("UpdateProgress should not be nil after OnDelete update")
	}

	if updateProgress.UpdateStartedAt.IsZero() {
		tc.T.Fatalf("UpdateProgress.UpdateStartedAt should be set")
	}

	if updateProgress.UpdateEndedAt == nil {
		tc.T.Fatalf("UpdateProgress.UpdateEndedAt should be set for OnDelete strategy")
	}

	if updateProgress.UpdatedPodCliques != nil {
		tc.T.Fatalf("UpdateProgress.UpdatedPodCliques should be nil for OnDelete strategy, got %v", updateProgress.UpdatedPodCliques)
	}

	if updateProgress.UpdatedPodCliqueScalingGroups != nil {
		tc.T.Fatalf("UpdateProgress.UpdatedPodCliqueScalingGroups should be nil for OnDelete strategy, got %v", updateProgress.UpdatedPodCliqueScalingGroups)
	}

	if updateProgress.CurrentlyUpdating != nil {
		tc.T.Fatalf("UpdateProgress.CurrentlyUpdating should be nil for OnDelete strategy, got %v", updateProgress.CurrentlyUpdating)
	}
}

func verifyNoPodsDeleted(tc *testctx.TestContext, events []podEvent, existingPodNames map[string]bool) {
	tc.T.Helper()

	deletedPods := []string{}
	for _, event := range events {
		if event.eventType == watch.Deleted && existingPodNames[event.pod.Name] {
			deletedPods = append(deletedPods, event.pod.Name)
		}
	}

	if len(deletedPods) > 0 {
		tc.T.Fatalf("OnDelete strategy violated: pods were automatically deleted when spec changed: %v", deletedPods)
	}
}

func waitForOnDeleteUpdateCompleteWithTimeout(tc *testctx.TestContext, timeout time.Duration) error {
	tcWithTimeout := *tc
	tcWithTimeout.Timeout = timeout
	return waitForOnDeleteUpdateComplete(&tcWithTimeout)
}

func verifyNoAutomaticDeletionAfterUpdate(
	tc *testctx.TestContext,
	tracker *updateTracker,
	existingPodNames map[string]bool,
	expectedPods int,
	verifyProgressFields bool,
) {
	tc.T.Helper()

	time.Sleep(10 * time.Second)

	if err := waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		tc.T.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	if verifyProgressFields {
		verifyUpdateProgressFields(tc)
	}

	tracker.stop()
	events := tracker.getEvents()
	verifyNoPodsDeleted(tc, events, existingPodNames)

	pods, err := tc.ListPods()
	if err != nil {
		tc.T.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != expectedPods {
		tc.T.Fatalf("Expected %d pods to still be running, but got %d", expectedPods, len(pods.Items))
	}
}

// excludeNodeFromPodCliqueAffinity updates the PCS spec to add a nodeAffinity exclusion
// (kubernetes.io/hostname NotIn [nodeName]) to the specified PodClique's nodeAffinity.
// This simulates the node failure recovery workflow from GREP-291 Example Usage,
// where a failed node is excluded by adding a NotIn matchExpression for its hostname.
func excludeNodeFromPodCliqueAffinity(tc *testctx.TestContext, cliqueName string, nodeName string) error {
	tc.T.Helper()

	pcsName := tc.Workload.Name
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var pcs grovev1alpha1.PodCliqueSet
		if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: pcsName, Namespace: tc.Namespace}, &pcs); err != nil {
			return fmt.Errorf("failed to get PodCliqueSet: %w", err)
		}
		pcs.SetGroupVersionKind(grovev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet"))
		pcs.SetManagedFields(nil)
		pcs.SetResourceVersion("")

		found := false
		for i, clique := range pcs.Spec.Template.Cliques {
			if clique.Name == cliqueName {
				affinity := pcs.Spec.Template.Cliques[i].Spec.PodSpec.Affinity
				if affinity == nil || affinity.NodeAffinity == nil ||
					affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil ||
					len(affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
					return fmt.Errorf("clique %s has no existing nodeAffinity nodeSelectorTerms to append to", cliqueName)
				}

				// Append a NotIn matchExpression to the first nodeSelectorTerm, matching the GREP-291 Example Usage:
				//   - key: kubernetes.io/hostname
				//     operator: NotIn
				//     values: [<failed-node-name>]
				terms := &affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0]
				terms.MatchExpressions = append(terms.MatchExpressions, corev1.NodeSelectorRequirement{
					Key:      "kubernetes.io/hostname",
					Operator: corev1.NodeSelectorOpNotIn,
					Values:   []string{nodeName},
				})

				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("clique %s not found in PodCliqueSet %s", cliqueName, pcsName)
		}

		return tc.Client.Patch(tc.Ctx, &pcs, client.Apply, client.FieldOwner("e2e-rolling-update-test"), client.ForceOwnership)
	})
}

// verifyPodHasNodeAffinityExclusion checks that the given pod's spec contains a
// kubernetes.io/hostname NotIn matchExpression that excludes the specified node name.
func verifyPodHasNodeAffinityExclusion(tc *testctx.TestContext, podName string, excludedNode string) error {
	tc.T.Helper()

	var pod corev1.Pod
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: podName}, &pod); err != nil {
		return fmt.Errorf("failed to get pod %s: %w", podName, err)
	}

	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil ||
		pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return fmt.Errorf("pod %s has no requiredDuringSchedulingIgnoredDuringExecution nodeAffinity", podName)
	}

	for _, term := range pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key == "kubernetes.io/hostname" && expr.Operator == corev1.NodeSelectorOpNotIn {
				if slices.Contains(expr.Values, excludedNode) {
					return nil
				}
			}
		}
	}

	return fmt.Errorf("pod %s does not have kubernetes.io/hostname NotIn [%s] in its nodeAffinity", podName, excludedNode)
}
