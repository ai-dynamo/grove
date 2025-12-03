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

package tests

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/util/retry"
)

// triggerRollingUpdate triggers a rolling update on the specified cliques and returns an errgroup.Group
// that completes when the rolling update finishes.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the wait timeout.
func triggerRollingUpdate(tc TestContext, expectedReplicas int32, cliqueNames ...string) *errgroup.Group {
	g := new(errgroup.Group)
	g.Go(func() error {
		startTime := time.Now()

		// Trigger synchronously first
		for _, cliqueName := range cliqueNames {
			if err := triggerPodCliqueRollingUpdate(tc, cliqueName); err != nil {
				return fmt.Errorf("failed to update PodClique %s spec: %w", cliqueName, err)
			}
		}
		logger.Debugf("[triggerRollingUpdate] Triggered update on %v, waiting for completion...", cliqueNames)

		// Wait for completion
		err := waitForRollingUpdateComplete(tc, expectedReplicas)
		elapsed := time.Since(startTime)
		if err != nil {
			logger.Debugf("[triggerRollingUpdate] Rolling update FAILED after %v: %v", elapsed, err)
		} else {
			logger.Debugf("[triggerRollingUpdate] Rolling update completed in %v", elapsed)
		}
		return err
	})
	return g
}

// triggerPodCliqueRollingUpdate triggers a rolling update by adding/updating an environment variable in a PodClique.
// Uses tc.Workload.Name as the PCS name.
func triggerPodCliqueRollingUpdate(tc TestContext, cliqueName string) error {
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	pcsName := tc.Workload.Name

	// Use current timestamp to ensure the value changes
	updateValue := fmt.Sprintf("%d", time.Now().Unix())

	// Retry on conflict errors (optimistic concurrency control)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the unstructured PodCliqueSet
		unstructuredPCS, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Get(tc.Ctx, pcsName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get PodCliqueSet: %w", err)
		}

		// Convert unstructured to typed PodCliqueSet
		var pcs grovev1alpha1.PodCliqueSet
		err = convertUnstructuredToTyped(unstructuredPCS.Object, &pcs)
		if err != nil {
			return fmt.Errorf("failed to convert to PodCliqueSet: %w", err)
		}

		// Find and update the appropriate clique
		found := false
		for i, clique := range pcs.Spec.Template.Cliques {
			if clique.Name == cliqueName {
				// Update the first container's environment variable
				if len(clique.Spec.PodSpec.Containers) > 0 {
					container := &pcs.Spec.Template.Cliques[i].Spec.PodSpec.Containers[0]

					// Check if ROLLING_UPDATE_TRIGGER env var exists
					envVarFound := false
					for j := range container.Env {
						if container.Env[j].Name == "ROLLING_UPDATE_TRIGGER" {
							container.Env[j].Value = updateValue
							envVarFound = true
							break
						}
					}

					// If not found, add new env var
					if !envVarFound {
						container.Env = append(container.Env, corev1.EnvVar{
							Name:  "ROLLING_UPDATE_TRIGGER",
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

		// Convert back to unstructured
		updatedUnstructured, err := convertTypedToUnstructured(&pcs)
		if err != nil {
			return fmt.Errorf("failed to convert to unstructured: %w", err)
		}

		// Update the resource - will return conflict error if resource was modified
		_, err = tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Update(tc.Ctx, updatedUnstructured, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		return nil
	})
}

// patchPCSWithSIGTERMIgnoringCommand patches all containers in the PCS to use a command that ignores SIGTERM
// and sets the termination grace period to 5 seconds. This makes pods ignore graceful shutdown but still
// allows rolling updates to progress in a reasonable time for testing.
// Uses tc.Workload.Name as the PCS name.
func patchPCSWithSIGTERMIgnoringCommand(tc TestContext) error {
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	pcsName := tc.Workload.Name

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		unstructuredPCS, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Get(tc.Ctx, pcsName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get PodCliqueSet: %w", err)
		}

		var pcs grovev1alpha1.PodCliqueSet
		err = convertUnstructuredToTyped(unstructuredPCS.Object, &pcs)
		if err != nil {
			return fmt.Errorf("failed to convert to PodCliqueSet: %w", err)
		}

		// Update all cliques: set termination grace period and make containers ignore SIGTERM
		terminationGracePeriod := int64(5)
		for i := range pcs.Spec.Template.Cliques {
			// Set termination grace period to 5 seconds
			pcs.Spec.Template.Cliques[i].Spec.PodSpec.TerminationGracePeriodSeconds = &terminationGracePeriod

			// Update all containers to use a command that ignores SIGTERM
			for j := range pcs.Spec.Template.Cliques[i].Spec.PodSpec.Containers {
				container := &pcs.Spec.Template.Cliques[i].Spec.PodSpec.Containers[j]
				// Use shell command that traps and ignores SIGTERM, then sleeps forever
				container.Command = []string{"/bin/sh", "-c", "trap '' TERM; sleep infinity"}
			}
		}

		updatedUnstructured, err := convertTypedToUnstructured(&pcs)
		if err != nil {
			return fmt.Errorf("failed to convert to unstructured: %w", err)
		}

		_, err = tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Update(tc.Ctx, updatedUnstructured, metav1.UpdateOptions{})
		return err
	})
}

// waitForRollingUpdate starts polling for rolling update completion in the background and returns an errgroup.Group.
// Use this when you need to trigger an update separately (e.g., when doing something between trigger and wait).
// For the common case, use triggerRollingUpdate which combines trigger + wait.
func waitForRollingUpdate(tc TestContext, expectedReplicas int32) *errgroup.Group {
	g := new(errgroup.Group)
	g.Go(func() error {
		return waitForRollingUpdateComplete(tc, expectedReplicas)
	})
	return g
}

// waitForRollingUpdateComplete waits for rolling update to complete by checking UpdatedReplicas.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the timeout (use a modified tc if a different timeout is needed).
func waitForRollingUpdateComplete(tc TestContext, expectedReplicas int32) error {
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	pcsName := tc.Workload.Name

	pollCount := 0
	return pollForCondition(tc, func() (bool, error) {
		pollCount++
		unstructuredPCS, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Get(tc.Ctx, pcsName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		var pcs grovev1alpha1.PodCliqueSet
		err = convertUnstructuredToTyped(unstructuredPCS.Object, &pcs)
		if err != nil {
			return false, err
		}

		// Log status every few polls for debugging
		if pollCount%3 == 1 {
			logger.Debugf("[waitForRollingUpdateComplete] Poll #%d: UpdatedReplicas=%d, expectedReplicas=%d, RollingUpdateProgress=%v",
				pollCount, pcs.Status.UpdatedReplicas, expectedReplicas, pcs.Status.RollingUpdateProgress != nil)
			if pcs.Status.RollingUpdateProgress != nil {
				logger.Debugf("  UpdateStartedAt=%v, UpdateEndedAt=%v, CurrentlyUpdating=%v",
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
			logger.Debugf("[waitForRollingUpdateComplete] Rolling update completed after %d polls", pollCount)
			return true, nil
		}

		return false, nil
	})
}

// waitForOrdinalUpdating waits for a specific ordinal to start being updated during rolling update.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the timeout (use a modified tc if a different timeout is needed).
func waitForOrdinalUpdating(tc TestContext, ordinal int32) error {
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	pcsName := tc.Workload.Name

	pollCount := 0
	return pollForCondition(tc, func() (bool, error) {
		pollCount++
		unstructuredPCS, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Get(tc.Ctx, pcsName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		var pcs grovev1alpha1.PodCliqueSet
		err = convertUnstructuredToTyped(unstructuredPCS.Object, &pcs)
		if err != nil {
			return false, err
		}

		// Log status every few polls for debugging
		if pollCount%3 == 1 {
			currentOrdinal := int32(-1)
			if pcs.Status.RollingUpdateProgress != nil && pcs.Status.RollingUpdateProgress.CurrentlyUpdating != nil {
				currentOrdinal = pcs.Status.RollingUpdateProgress.CurrentlyUpdating.ReplicaIndex
			}
			logger.Debugf("[waitForOrdinalUpdating] Poll #%d: waiting for ordinal %d, currently updating ordinal: %d",
				pollCount, ordinal, currentOrdinal)
		}

		// Check if the target ordinal is currently being updated
		if pcs.Status.RollingUpdateProgress != nil &&
			pcs.Status.RollingUpdateProgress.CurrentlyUpdating != nil &&
			pcs.Status.RollingUpdateProgress.CurrentlyUpdating.ReplicaIndex == ordinal {
			logger.Debugf("[waitForOrdinalUpdating] Ordinal %d started updating after %d polls", ordinal, pollCount)
			return true, nil
		}

		return false, nil
	})
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
func getPodIdentifier(tc TestContext, pod *corev1.Pod) string {
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
func verifyOnePodDeletedAtATime(tc TestContext, events []podEvent) {
	tc.T.Helper()

	// Log all events for debugging
	logger.Debug("=== Starting verifyOnePodDeletedAtATime analysis ===")
	logger.Debugf("Total events captured: %d", len(events))

	for i, event := range events {
		podclique := ""
		if event.Pod.Labels != nil {
			podclique = event.Pod.Labels["grove.io/podclique"]
		}
		logger.Debugf("Event[%d]: Type=%s PodName=%s Hostname=%s PodClique=%s Timestamp=%v",
			i, event.Type, event.Pod.Name, event.Pod.Spec.Hostname, podclique, event.Timestamp.Format("15:04:05.000"))
	}

	// Track the most recent ADDED pod for each hostname (podID).
	// Key: hostname, Value: pod name of the most recent ADDED event
	addedPods := make(map[string]string)

	// Track DELETE events for each hostname.
	// Key: hostname, Value: pod name of the deleted pod
	deletedPods := make(map[string]string)

	maxConcurrentDeletions := 0
	var violationEvents []string

	logger.Debug("=== Processing events to find concurrent deletions ===")
	for i, event := range events {
		podID := getPodIdentifier(tc, event.Pod)
		podName := event.Pod.Name

		switch event.Type {
		case watch.Deleted:
			// Record the DELETE event for this hostname
			deletedPods[podID] = podName
			logger.Debugf("Event[%d] DELETE: podID=%s, podName=%s", i, podID, podName)
		case watch.Added:
			// Record the ADDED event for this hostname
			addedPods[podID] = podName
			logger.Debugf("Event[%d] ADDED: podID=%s, podName=%s", i, podID, podName)
		case watch.Modified:
			logger.Debugf("Event[%d] MODIFIED: podID=%s (ignored for deletion tracking)", i, podID)
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
			logger.Debugf("Event[%d] State: currentlyDeleting=%d, activelyDeleting=%v",
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

	logger.Debugf("=== Analysis complete: maxConcurrentDeletions=%d ===", maxConcurrentDeletions)
	if len(violationEvents) > 0 {
		logger.Debug("Violation events:")
		for _, v := range violationEvents {
			logger.Debugf("  %s", v)
		}
	}

	// Assert that at most 1 pod was being deleted/replaced at any point in time,
	// which ensures the rolling update respects the MaxUnavailable=1 constraint at the global level.
	if maxConcurrentDeletions > 1 {
		tc.T.Fatalf("Expected at most 1 pod being deleted at a time, but found %d concurrent deletions", maxConcurrentDeletions)
	}
}

// getMapKeys returns a slice of keys from a map[string]time.Time for logging
func getMapKeys(m map[string]time.Time) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// verifyOnePodDeletedAtATimePerPodclique verifies only one pod per Podclique is being deleted at a time
// during rolling updates.
//
// This function processes a sequence of pod events and tracks the number of individual pods
// that are in a "deleting" state within each Podclique at any given time.
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// See verifyOnePCSGReplicaDeletedAtATime for detailed explanation.
func verifyOnePodDeletedAtATimePerPodclique(tc TestContext, events []podEvent) {
	tc.T.Helper()

	// Track DELETE and ADDED counts separately per podID per PodClique to handle out-of-order events.
	// Map structure: podcliqueName -> podID -> count
	deletedCount := make(map[string]map[string]int)
	addedCount := make(map[string]map[string]int)
	maxConcurrentDeletionsPerPodclique := make(map[string]int)

	for _, event := range events {
		// All pods should have labels - if nil, that's a bug
		if event.Pod.Labels == nil {
			tc.T.Fatalf("Pod %s has no labels, which indicates a bug in pod creation", event.Pod.Name)
		}

		podcliqueName, ok := event.Pod.Labels["grove.io/podclique"]
		if !ok {
			tc.T.Fatalf("Pod %s does not have grove.io/podclique label", event.Pod.Name)
		}

		podID := getPodIdentifier(tc, event.Pod)

		switch event.Type {
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
func verifySinglePCSReplicaUpdatedFirst(tc TestContext, events []podEvent) {
	tc.T.Helper()

	logger.Debug("=== Starting verifySinglePCSReplicaUpdatedFirst analysis ===")
	logger.Debugf("Total events captured: %d", len(events))

	// Log all events for debugging
	for i, event := range events {
		replicaIdx := 0
		if val, ok := event.Pod.Labels["grove.io/podcliqueset-replica-index"]; ok {
			replicaIdx, _ = strconv.Atoi(val)
		}
		podclique := ""
		if event.Pod.Labels != nil {
			podclique = event.Pod.Labels["grove.io/podclique"]
		}
		logger.Debugf("Event[%d]: Type=%s PodName=%s Hostname=%s Replica=%d PodClique=%s Timestamp=%v",
			i, event.Type, event.Pod.Name, event.Pod.Spec.Hostname, replicaIdx, podclique, event.Timestamp.Format("15:04:05.000"))
	}

	// Track DELETE and ADDED counts separately per pod identifier per replica to handle out-of-order events.
	// Structure: replicaIndex -> podID -> count
	deletedCount := make(map[int]map[string]int)
	addedCount := make(map[int]map[string]int)

	maxConcurrentUpdatingReplicas := 0
	var violationEvent string

	logger.Debug("=== Processing events to find concurrent replica updates ===")
	for i, event := range events {
		// Extract replica index from pod labels (defaults to 0 if not present)
		replicaIdx := 0
		if val, ok := event.Pod.Labels["grove.io/podcliqueset-replica-index"]; ok {
			replicaIdx, _ = strconv.Atoi(val)
		}

		// Extract PodClique name - skip pods without this label
		_, ok := event.Pod.Labels["grove.io/podclique"]
		if !ok {
			continue
		}

		// Get stable pod identifier (hostname) that persists across pod replacements
		podID := getPodIdentifier(tc, event.Pod)

		// Track DELETE and ADDED events
		switch event.Type {
		case watch.Deleted:
			if deletedCount[replicaIdx] == nil {
				deletedCount[replicaIdx] = make(map[string]int)
			}
			deletedCount[replicaIdx][podID]++
			logger.Debugf("Event[%d] DELETE: replica=%d podID=%s deleted=%d added=%d",
				i, replicaIdx, podID, deletedCount[replicaIdx][podID], getReplicaPodCount(addedCount, replicaIdx, podID))

		case watch.Added:
			if addedCount[replicaIdx] == nil {
				addedCount[replicaIdx] = make(map[string]int)
			}
			addedCount[replicaIdx][podID]++
			logger.Debugf("Event[%d] ADDED: replica=%d podID=%s deleted=%d added=%d",
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
			logger.Debugf("Event[%d] State: numUpdatingReplicas=%d activelyUpdating=%v",
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
				i, event.Type, maxConcurrentUpdatingReplicas, replicas)
		}
	}

	logger.Debugf("=== Analysis complete: maxConcurrentUpdatingReplicas=%d ===", maxConcurrentUpdatingReplicas)
	if violationEvent != "" {
		logger.Debugf("Max concurrent replicas event: %s", violationEvent)
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
func verifyOnePCSGReplicaDeletedAtATime(tc TestContext, events []podEvent) {
	tc.T.Helper()

	logger.Debug("=== Starting verifyOnePCSGReplicaDeletedAtATime analysis ===")
	logger.Debugf("Total events captured: %d", len(events))

	// Log all PCSG-related events for debugging
	for i, event := range events {
		if event.Pod.Labels == nil {
			continue
		}
		replicaID, hasReplicaLabel := event.Pod.Labels["grove.io/podcliquescalinggroup-replica-index"]
		if !hasReplicaLabel {
			continue
		}
		pcsgName := event.Pod.Labels["grove.io/podcliquescalinggroup"]
		podclique := event.Pod.Labels["grove.io/podclique"]
		logger.Debugf("Event[%d]: Type=%s PodName=%s PCSG=%s ReplicaID=%s PodClique=%s Timestamp=%v",
			i, event.Type, event.Pod.Name, pcsgName, replicaID, podclique, event.Timestamp.Format("15:04:05.000"))
	}

	// Track DELETE and ADDED counts separately per replica to handle out-of-order events.
	// Key format: "<pcsg-name>-<replica-index>"
	deletedCount := make(map[string]int)
	addedCount := make(map[string]int)
	maxConcurrentDeletions := 0
	var violationEvent string

	logger.Debug("=== Processing events to find concurrent deletions ===")
	for i, event := range events {
		// All pods should have labels - if nil, that's a bug
		if event.Pod.Labels == nil {
			tc.T.Fatalf("Pod %s has no labels, which indicates a bug in pod creation", event.Pod.Name)
		}

		// Only process pods that belong to a PCSG (not all pods have this label)
		replicaID, hasReplicaLabel := event.Pod.Labels["grove.io/podcliquescalinggroup-replica-index"]
		if !hasReplicaLabel {
			continue
		}

		pcsgName, ok := event.Pod.Labels["grove.io/podcliquescalinggroup"]
		if !ok {
			tc.T.Fatalf("Pod %s has PCSG replica index but no PCSG name label", event.Pod.Name)
		}

		// Create a composite key to uniquely identify this PCSG replica globally
		replicaKey := fmt.Sprintf("%s-%s", pcsgName, replicaID)

		switch event.Type {
		case watch.Deleted:
			deletedCount[replicaKey]++
			logger.Debugf("Event[%d] DELETE: pod=%s replica=%s deleted=%d added=%d",
				i, event.Pod.Name, replicaKey, deletedCount[replicaKey], addedCount[replicaKey])
		case watch.Added:
			addedCount[replicaKey]++
			logger.Debugf("Event[%d] ADDED: pod=%s replica=%s deleted=%d added=%d",
				i, event.Pod.Name, replicaKey, deletedCount[replicaKey], addedCount[replicaKey])
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
			logger.Debugf("Event[%d] State: concurrent=%d activelyRolling=%v", i, currentConcurrent, replicas)
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

	logger.Debugf("=== Analysis complete: maxConcurrentDeletions=%d ===", maxConcurrentDeletions)
	if violationEvent != "" {
		logger.Debugf("Violation: %s", violationEvent)
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
//   - ✅ ALLOWED: sg-x replica 0 AND sg-y replica 0 rolling simultaneously
//   - ❌ NOT ALLOWED: sg-x replica 0 AND sg-x replica 1 rolling simultaneously
//
// Compare with verifyOnePCSGReplicaDeletedAtATime which enforces a global constraint (stricter).
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// See verifyOnePCSGReplicaDeletedAtATime for detailed explanation.
func verifyOnePCSGReplicaDeletedAtATimePerPCSG(tc TestContext, events []podEvent) {
	tc.T.Helper()

	// Track DELETE and ADDED counts separately per replica per PCSG to handle out-of-order events.
	// Map structure: pcsgName -> replicaIndex -> count
	deletedCount := make(map[string]map[string]int)
	addedCount := make(map[string]map[string]int)
	maxConcurrentDeletionsPerPCSG := make(map[string]int)

	for _, event := range events {
		// All pods should have labels - if nil, that's a bug
		if event.Pod.Labels == nil {
			tc.T.Fatalf("Pod %s has no labels, which indicates a bug in pod creation", event.Pod.Name)
		}

		// Only process pods that belong to a PCSG (not all pods have this label)
		replicaID, hasReplicaLabel := event.Pod.Labels["grove.io/podcliquescalinggroup-replica-index"]
		if !hasReplicaLabel {
			continue
		}

		pcsgName, ok := event.Pod.Labels["grove.io/podcliquescalinggroup"]
		if !ok {
			tc.T.Fatalf("Pod %s has PCSG replica index but no PCSG name label", event.Pod.Name)
		}

		switch event.Type {
		case watch.Deleted:
			if deletedCount[pcsgName] == nil {
				deletedCount[pcsgName] = make(map[string]int)
			}
			deletedCount[pcsgName][replicaID]++
			logger.Debugf("Pod %s deleted, replica %s in PCSG %s: deleted=%d added=%d",
				event.Pod.Name, replicaID, pcsgName,
				deletedCount[pcsgName][replicaID], getCount(addedCount, pcsgName, replicaID))
		case watch.Added:
			if addedCount[pcsgName] == nil {
				addedCount[pcsgName] = make(map[string]int)
			}
			addedCount[pcsgName][replicaID]++
			logger.Debugf("Pod %s added, replica %s in PCSG %s: deleted=%d added=%d",
				event.Pod.Name, replicaID, pcsgName,
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

// scalePodClique scales a PodClique and returns an errgroup.Group that completes when the expected pod count is reached.
// Uses tc.Workload.Name as the PCS name.
// The operation runs asynchronously - call Wait() on the returned Group to block until complete.
// If delayMs > 0, the operation will sleep for that duration before starting.
func scalePodClique(tc TestContext, cliqueName string, replicas int32, expectedTotalPods, delayMs int) *errgroup.Group {
	g := new(errgroup.Group)
	g.Go(func() error {
		startTime := time.Now()

		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		logger.Debugf("[scalePodClique] Scaling %s to %d replicas, expecting %d total pods", cliqueName, replicas, expectedTotalPods)

		if err := scalePodCliqueInPCS(tc, cliqueName, replicas); err != nil {
			return fmt.Errorf("failed to scale PodClique %s: %w", cliqueName, err)
		}

		logger.Debugf("[scalePodClique] Scale patch applied, waiting for pods...")

		// Wait for pods to reach expected count
		pollCount := 0
		err := pollForCondition(tc, func() (bool, error) {
			pollCount++
			pods, err := listPods(tc)
			if err != nil {
				return false, err
			}
			if pollCount%3 == 1 {
				logger.Debugf("[scalePodClique] Poll #%d: current pods=%d, expected=%d", pollCount, len(pods.Items), expectedTotalPods)
			}
			return len(pods.Items) == expectedTotalPods, nil
		})
		elapsed := time.Since(startTime)
		if err != nil {
			logger.Debugf("[scalePodClique] Scale %s FAILED after %v: %v", cliqueName, elapsed, err)
			return fmt.Errorf("failed to wait for pods after scaling PodClique %s: %w", cliqueName, err)
		}
		logger.Debugf("[scalePodClique] Scale %s completed in %v (pods=%d)", cliqueName, elapsed, expectedTotalPods)
		return nil
	})
	return g
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
func scalePodCliqueInPCS(tc TestContext, cliqueName string, replicas int32) error {
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	pclqGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliques"}
	pcsName := tc.Workload.Name

	// Get the PCS to find out how many replicas it has
	unstructuredPCS, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Get(tc.Ctx, pcsName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get PodCliqueSet: %w", err)
	}

	var pcs grovev1alpha1.PodCliqueSet
	if err := convertUnstructuredToTyped(unstructuredPCS.Object, &pcs); err != nil {
		return fmt.Errorf("failed to convert to PodCliqueSet: %w", err)
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

			_, err = tc.DynamicClient.Resource(pclqGVR).Namespace(tc.Namespace).Patch(
				tc.Ctx, pclqName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
			return err
		}); err != nil {
			return fmt.Errorf("failed to scale PodClique %s: %w", pclqName, err)
		}
	}

	return nil
}

