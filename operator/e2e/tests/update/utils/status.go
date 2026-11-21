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

//go:build e2e

package utils

import (
	"context"
	"fmt"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WaitForReplicaMidCoherentRoll blocks until the replica's PodGangMap carries entries at more than one
// generation hash. That means a coherent sub-step has committed a current-revision anchor alongside the
// draining old-revision entries. With the replica's readiness blocked, the roll then parks in this state.
func WaitForReplicaMidCoherentRoll(t *testing.T, tc *testctx.TestContext, pcsReplicaIndex int) {
	t.Helper()
	deadline := time.Now().Add(tc.Timeout)
	for time.Now().Before(deadline) {
		if pgm, err := GetReplicaPodGangMap(tc, pcsReplicaIndex); err == nil {
			generations := make(map[string]struct{})
			for _, entry := range pgm.Spec.Entries {
				generations[entry.PodCliqueSetGenerationHash] = struct{}{}
			}
			if len(generations) >= 2 {
				return
			}
		}
		time.Sleep(tc.Interval)
	}
	t.Fatalf("replica %d PodGangMap did not reach a multi-generation (mid coherent roll) state within %s", pcsReplicaIndex, tc.Timeout)
}

// WaitForRollingUpdateComplete waits for rolling update to complete by checking UpdatedReplicas.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the timeout (use a modified tc if a different timeout is needed).
func WaitForRollingUpdateComplete(tc *testctx.TestContext, expectedReplicas int32) error {
	pcsName := tc.Workload.Name

	pollCount := 0
	fetchPCS := waiter.FetchByName(pcsName, k8sclient.Getter[*grovev1beta1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		pollCount++
		if pcs == nil {
			// A transient cache miss returns a nil object. Keep polling.
			return false
		}

		endedAt := "<nil>"
		if pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt != nil {
			endedAt = pcs.Status.UpdateProgress.UpdateEndedAt.UTC().Format("15:04:05Z")
		}
		tests.Logger.Infof("[waitForRollingUpdateComplete] Poll #%d: UpdatedReplicas=%d/%d UpdateEndedAt=%s",
			pollCount, pcs.Status.UpdatedReplicas, expectedReplicas, endedAt)

		// Check if rolling update is complete:
		// - UpdatedReplicas should match expected
		// - UpdateProgress should exist with UpdateEndedAt set (not nil)
		if pcs.Status.UpdatedReplicas == expectedReplicas &&
			pcs.Status.UpdateProgress != nil &&
			pcs.Status.UpdateProgress.UpdateEndedAt != nil {
			tests.Logger.Debugf("[waitForRollingUpdateComplete] Rolling update completed after %d polls", pollCount)
			return true
		}

		return false
	})
	w := waiter.New[*grovev1beta1.PodCliqueSet]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval)
	err := w.WaitUntil(tc.Ctx, fetchPCS, predicate)
	return err
}

// WaitForOrdinalUpdating waits for a specific ordinal to start being updated during rolling update.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the timeout (use a modified tc if a different timeout is needed).
func WaitForOrdinalUpdating(tc *testctx.TestContext, ordinal int32) error {
	pcsName := tc.Workload.Name

	pollCount := 0
	fetchPCS := waiter.FetchByName(pcsName, k8sclient.Getter[*grovev1beta1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		pollCount++
		if pcs == nil {
			// A transient cache miss returns a nil object. Keep polling.
			return false
		}

		// Log status every few polls for debugging
		if pollCount%3 == 1 {
			currentOrdinal := int32(-1)
			if pcs.Status.UpdateProgress != nil && len(pcs.Status.UpdateProgress.CurrentlyUpdating) > 0 {
				currentOrdinal = pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex
			}
			tests.Logger.Debugf("[waitForOrdinalUpdating] Poll #%d: waiting for ordinal %d, currently updating ordinal: %d",
				pollCount, ordinal, currentOrdinal)
		}

		// Check if the target ordinal is currently being updated
		if pcs.Status.UpdateProgress != nil &&
			len(pcs.Status.UpdateProgress.CurrentlyUpdating) > 0 &&
			pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex == ordinal {
			tests.Logger.Debugf("[waitForOrdinalUpdating] Ordinal %d started updating after %d polls", ordinal, pollCount)
			return true
		}

		return false
	})
	w := waiter.New[*grovev1beta1.PodCliqueSet]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval)
	err := w.WaitUntil(tc.Ctx, fetchPCS, predicate)
	return err
}

// AssertNoActiveChildUpdates fails if any PCLQ or PCSG owned by the workload still has unfinished
// update progress after the PCS reports completion.
func AssertNoActiveChildUpdates(tc *testctx.TestContext) {
	tc.T.Helper()
	matchingPCS := client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}
	inNamespace := client.InNamespace(tc.Namespace)

	var pclqs grovev1beta1.PodCliqueList
	require.NoError(tc.T, tc.Client.List(tc.Ctx, &pclqs, inNamespace, matchingPCS))
	for i := range pclqs.Items {
		progress := pclqs.Items[i].Status.UpdateProgress
		assert.False(tc.T, progress != nil && progress.UpdateEndedAt == nil,
			"PodClique %s still has an active update", pclqs.Items[i].Name)
	}

	var pcsgs grovev1beta1.PodCliqueScalingGroupList
	require.NoError(tc.T, tc.Client.List(tc.Ctx, &pcsgs, inNamespace, matchingPCS))
	for i := range pcsgs.Items {
		progress := pcsgs.Items[i].Status.UpdateProgress
		assert.False(tc.T, progress != nil && progress.UpdateEndedAt == nil,
			"PodCliqueScalingGroup %s still has an active update", pcsgs.Items[i].Name)
	}
}

// AssertGenerationHashConverged fails the test unless every PodClique and PodCliqueScalingGroup of the
// workload reports CurrentPodCliqueSetGenerationHash equal to the PodCliqueSet's CurrentGenerationHash.
func AssertGenerationHashConverged(tc *testctx.TestContext) {
	tc.T.Helper()
	pcs, err := GetPCS(tc, tc.Workload.Name)
	if err != nil {
		tc.T.Fatalf("failed to get PodCliqueSet: %v", err)
	}
	if pcs.Status.CurrentGenerationHash == nil {
		tc.T.Fatalf("PodCliqueSet %s has no CurrentGenerationHash", pcs.Name)
	}
	want := *pcs.Status.CurrentGenerationHash
	inNamespace := client.InNamespace(tc.Namespace)
	matchingPCS := client.MatchingLabels{apicommon.LabelPartOfKey: pcs.Name}

	var pclqList grovev1beta1.PodCliqueList
	if err := tc.Client.List(tc.Ctx, &pclqList, inNamespace, matchingPCS); err != nil {
		tc.T.Fatalf("failed to list PodCliques: %v", err)
	}
	for i := range pclqList.Items {
		pclq := &pclqList.Items[i]
		if pclq.Status.CurrentPodCliqueSetGenerationHash == nil || *pclq.Status.CurrentPodCliqueSetGenerationHash != want {
			tc.T.Fatalf("PodClique %s did not converge to generation hash %s, got %v", pclq.Name, want, pclq.Status.CurrentPodCliqueSetGenerationHash)
		}
	}

	var pcsgList grovev1beta1.PodCliqueScalingGroupList
	if err := tc.Client.List(tc.Ctx, &pcsgList, inNamespace, matchingPCS); err != nil {
		tc.T.Fatalf("failed to list PodCliqueScalingGroups: %v", err)
	}
	for i := range pcsgList.Items {
		pcsg := &pcsgList.Items[i]
		if pcsg.Status.CurrentPodCliqueSetGenerationHash == nil || *pcsg.Status.CurrentPodCliqueSetGenerationHash != want {
			tc.T.Fatalf("PodCliqueScalingGroup %s did not converge to generation hash %s, got %v", pcsg.Name, want, pcsg.Status.CurrentPodCliqueSetGenerationHash)
		}
	}
}

// WaitForGenerationHashChange waits until the PodCliqueSet's Status.CurrentGenerationHash differs from
// previousHash, signaling that the operator has observed a newly triggered update. It is a stable signal
// for "a new update started", unlike the transient CurrentlyUpdating state which a poll can miss when a
// roll completes within one interval. Uses tc.Workload.Name as the PCS name and tc.Timeout for the timeout.
func WaitForGenerationHashChange(tc *testctx.TestContext, previousHash string) error {
	pcsName := tc.Workload.Name

	pollCount := 0
	fetchPCS := waiter.FetchByName(pcsName, k8sclient.Getter[*grovev1beta1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		pollCount++
		if pcs == nil || pcs.Status.CurrentGenerationHash == nil {
			// A transient cache miss returns a nil object. Keep polling.
			return false
		}
		if *pcs.Status.CurrentGenerationHash != previousHash {
			tests.Logger.Debugf("[waitForGenerationHashChange] new generation hash %s observed after %d polls",
				*pcs.Status.CurrentGenerationHash, pollCount)
			return true
		}
		return false
	})
	w := waiter.New[*grovev1beta1.PodCliqueSet]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval)
	return w.WaitUntil(tc.Ctx, fetchPCS, predicate)
}

// WaitForPCSUpdateCondition waits for the PodCliqueSet UpdateInProgress condition to match.
func WaitForPCSUpdateCondition(tc *testctx.TestContext, wantStatus metav1.ConditionStatus, wantReason string) error {
	fetch := waiter.FetchFunc[*grovev1beta1.PodCliqueSet](func(context.Context) (*grovev1beta1.PodCliqueSet, error) {
		return GetPCS(tc, tc.Workload.Name)
	})
	_, err := waiter.New[*grovev1beta1.PodCliqueSet]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).WithRetryOnError().
		WaitFor(tc.Ctx, fetch, UpdateInProgressConditionMet(func(p *grovev1beta1.PodCliqueSet) []metav1.Condition { return p.Status.Conditions }, wantStatus, wantReason))
	if err != nil {
		return fmt.Errorf("PodCliqueSet UpdateInProgress did not reach status=%s reason=%s: %w", wantStatus, wantReason, err)
	}
	return nil
}

// UpdateInProgressConditionMet builds a predicate satisfied when the UpdateInProgress condition read
// by getConds matches wantStatus and wantReason.
func UpdateInProgressConditionMet[T any](getConds func(T) []metav1.Condition, wantStatus metav1.ConditionStatus, wantReason string) waiter.Predicate[T] {
	return func(obj T) bool {
		cond := meta.FindStatusCondition(getConds(obj), apiconstants.ConditionTypeUpdateInProgress)
		return cond != nil && cond.Status == wantStatus && cond.Reason == wantReason
	}
}

// AssertUpdateInProgressCleared fails the test unless the PodCliqueSet UpdateInProgress condition has
// settled to False with reason NoActiveUpdate.
func AssertUpdateInProgressCleared(tc *testctx.TestContext) {
	tc.T.Helper()
	if err := WaitForPCSUpdateCondition(tc, metav1.ConditionFalse, apiconstants.ConditionReasonNoActiveUpdate); err != nil {
		tc.T.Fatalf("PodCliqueSet UpdateInProgress condition was not cleared after the rolling update: %v", err)
	}
}
