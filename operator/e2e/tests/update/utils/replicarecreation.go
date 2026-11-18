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

// Package utils provides shared assertions and resource helpers for update e2e tests.
package utils

import (
	"context"
	"fmt"
	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"slices"
	"testing"
	"time"
)

// WaitForSameIndexReplacementCleanup waits for an active replacement at replicaIndex to finish deleting
// its source resources. It checks PCS progress only. Replacement child resources may not exist yet.
func WaitForSameIndexReplacementCleanup(tc *testctx.TestContext, replicaIndex int32) (*grovev1beta1.PodCliqueSet, error) {
	fetchPCS := waiter.FetchByName(tc.Workload.Name, k8sclient.Getter[*grovev1beta1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		if pcs == nil || pcs.Status.CurrentGenerationHash == nil || pcs.Status.UpdateProgress == nil {
			return false
		}
		progress := pcs.Status.UpdateProgress
		if !progress.RequiresReplicaRecreation || progress.UpdateEndedAt != nil {
			return false
		}
		for _, replicaProgress := range progress.CurrentlyUpdating {
			replacement := replicaProgress.Replacement
			if replicaProgress.ReplicaIndex != replicaIndex || replicaProgress.UpdateEndedAt != nil || replacement == nil {
				continue
			}
			if replacement.TargetReplicaIndex == replicaIndex && replacement.CanceledAt == nil && replacement.SourceCleanupCompletedAt != nil {
				return true
			}
		}
		return false
	})
	return waiter.New[*grovev1beta1.PodCliqueSet]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPCS, predicate)
}

// WaitForRecreatedPCLQ waits for the PodClique to reappear with a new UID. Its spec is asserted by the caller.
func WaitForRecreatedPCLQ(tc *testctx.TestContext, previous *grovev1beta1.PodClique) (*grovev1beta1.PodClique, error) {
	fetchPCLQ := waiter.FetchByName(previous.Name, k8sclient.Getter[*grovev1beta1.PodClique](tc.Client, previous.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodClique](func(pclq *grovev1beta1.PodClique) bool {
		return pclq != nil && pclq.DeletionTimestamp == nil && pclq.UID != previous.UID
	})
	return waiter.New[*grovev1beta1.PodClique]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPCLQ, predicate)
}

// WaitForRecreatedPCSG waits for the scaling group to reappear with a new UID. Its spec is asserted by the caller.
func WaitForRecreatedPCSG(tc *testctx.TestContext, previous *grovev1beta1.PodCliqueScalingGroup) (*grovev1beta1.PodCliqueScalingGroup, error) {
	fetchPCSG := waiter.FetchByName(previous.Name, k8sclient.Getter[*grovev1beta1.PodCliqueScalingGroup](tc.Client, previous.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueScalingGroup](func(pcsg *grovev1beta1.PodCliqueScalingGroup) bool {
		return pcsg != nil && pcsg.DeletionTimestamp == nil && pcsg.UID != previous.UID
	})
	return waiter.New[*grovev1beta1.PodCliqueScalingGroup]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPCSG, predicate)
}

// WaitForRecreatedPodGangMap waits for a new map with the requested entry count and generation.
// The caller asserts each entry's composition after this milestone.
func WaitForRecreatedPodGangMap(
	tc *testctx.TestContext,
	previous *grovev1beta1.PodGangMap,
	generationHash string,
	expectedEntryCount int,
) (*grovev1beta1.PodGangMap, error) {
	fetchPGM := waiter.FetchByName(previous.Name, k8sclient.Getter[*grovev1beta1.PodGangMap](tc.Client, previous.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodGangMap](func(pgm *grovev1beta1.PodGangMap) bool {
		if pgm == nil || pgm.DeletionTimestamp != nil || pgm.UID == previous.UID || len(pgm.Spec.Entries) != expectedEntryCount {
			return false
		}
		for _, entry := range pgm.Spec.Entries {
			if entry.PodCliqueSetGenerationHash != generationHash {
				return false
			}
		}
		return true
	})
	return waiter.New[*grovev1beta1.PodGangMap]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPGM, predicate)
}

// ValidateRecreationWhileBlocked checks that the update is still active and unapplied, and that the active
// replacement mappings and cancellation details have not changed. Supersession must clear active replacement
// cleanup markers before deleting the obsolete targets. Per-update start times may change.
func ValidateRecreationWhileBlocked(previous, current *grovev1beta1.PodCliqueSet) error {
	progress := current.Status.UpdateProgress
	if progress == nil || !progress.RequiresReplicaRecreation || progress.UpdateEndedAt != nil {
		return fmt.Errorf("recreation stopped while replacement readiness was blocked")
	}
	if current.Status.AppliedSchedulingConfigurationHash == nil ||
		!assert.ObjectsAreEqual(previous.Status.AppliedSchedulingConfigurationHash, current.Status.AppliedSchedulingConfigurationHash) {
		return fmt.Errorf("scheduling configuration was marked applied while replacement readiness was blocked")
	}
	previousSelections := previous.Status.UpdateProgress.CurrentlyUpdating
	if len(progress.CurrentlyUpdating) != len(previousSelections) {
		return fmt.Errorf("replica selections changed during supersession: before %d, after %d", len(previousSelections), len(progress.CurrentlyUpdating))
	}
	hasSupersededGeneration := current.Status.CurrentGenerationHash != nil &&
		*current.Status.CurrentGenerationHash != *previous.Status.CurrentGenerationHash
	for i, replicaProgress := range progress.CurrentlyUpdating {
		before := previousSelections[i]
		if replicaProgress.UpdateEndedAt != nil {
			return fmt.Errorf("replica %d update ended while replacement readiness was blocked", replicaProgress.ReplicaIndex)
		}
		expectedReplacement := before.Replacement.DeepCopy()
		if hasSupersededGeneration && expectedReplacement != nil && expectedReplacement.CanceledAt == nil {
			expectedReplacement.SourceCleanupCompletedAt = nil
		}
		if replicaProgress.ReplicaIndex != before.ReplicaIndex || !assert.ObjectsAreEqual(expectedReplacement, replicaProgress.Replacement) {
			return fmt.Errorf("unexpected replacement mapping, cleanup marker, or cancellation details during supersession for replica %d", before.ReplicaIndex)
		}
	}
	return nil
}

// WaitForReplicaRecreationComplete waits until structural PCS replica recreation has finished: UpdateProgress
// has UpdateEndedAt set, AppliedSchedulingConfigurationHash equals the target scheduling hash the update
// converged to, and UpdatedReplicas matches expectedReplicas.
func WaitForReplicaRecreationComplete(tc *testctx.TestContext, expectedReplicas int32) error {
	fetchPCS := waiter.FetchByName(tc.Workload.Name, k8sclient.Getter[*grovev1beta1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		if pcs == nil || pcs.Status.UpdateProgress == nil {
			return false
		}
		progress := pcs.Status.UpdateProgress
		// The end marker is persisted before status reconciliation updates the replica counters.
		if progress.UpdateEndedAt == nil || pcs.Status.UpdatedReplicas != expectedReplicas || pcs.Status.AvailableReplicas != expectedReplicas {
			return false
		}
		return pcs.Status.AppliedSchedulingConfigurationHash != nil &&
			*pcs.Status.AppliedSchedulingConfigurationHash == progress.TargetSchedulingConfigurationHash
	})
	w := waiter.New[*grovev1beta1.PodCliqueSet]().WithTimeout(tc.Timeout).WithInterval(tc.Interval)
	return w.WaitUntil(tc.Ctx, fetchPCS, predicate)
}

// RequireCombinedMinAvailableChange changes both workload2 scheduling inputs in one merge patch.
func RequireCombinedMinAvailableChange(t *testing.T, tc *testctx.TestContext, standaloneMinAvailable, pcsgMinAvailable int32) {
	t.Helper()
	pcs, err := GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	base := pcs.DeepCopy()
	pclqTemplate := componentutils.FindPodCliqueTemplateSpecByName(pcs, "pc-a")
	require.NotNil(t, pclqTemplate)
	pclqTemplate.Spec.MinAvailable = new(standaloneMinAvailable)
	var pcsgConfig *grovev1beta1.PodCliqueScalingGroupConfig
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcs.Spec.Template.PodCliqueScalingGroupConfigs[i].Name == "sg-x" {
			pcsgConfig = &pcs.Spec.Template.PodCliqueScalingGroupConfigs[i]
			break
		}
	}
	require.NotNil(t, pcsgConfig)
	pcsgConfig.MinAvailable = new(pcsgMinAvailable)
	require.NoError(t, tc.Client.Patch(tc.Ctx, pcs, client.MergeFrom(base)), "patch both MinAvailable fields atomically")
}

// RequireSerialReplicaRecreated waits for a held replica's fresh resources and checks retained prior completions.
func RequireSerialReplicaRecreated(
	t *testing.T,
	tc *testctx.TestContext,
	before ReplicaRecreationSnapshot,
	pcsReplicaIndex int,
	want ExpectedReplicaConfiguration,
) ReplicaRecreationSnapshot {
	t.Helper()
	_, err := WaitForRecreatedPCLQ(tc, before.PCLQ)
	require.NoError(t, err, "replica %d standalone pc-a must be recreated", pcsReplicaIndex)
	require.NoError(t, WaitForStandaloneReplicaUnavailable(tc, "pc-a", pcsReplicaIndex))
	_, err = WaitForRecreatedPCSG(tc, before.PCSG)
	require.NoError(t, err, "replica %d sg-x must be recreated", pcsReplicaIndex)
	_, err = WaitForRecreatedPodGangMap(tc, before.PGM, want.PodGangMap.GenerationHash, 2)
	require.NoError(t, err, "replica %d PodGangMap must be recreated", pcsReplicaIndex)
	after := RequireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
	RequireReplicaResourceUIDsChanged(t, before, after)
	AssertReplicaConfiguration(t, after, want)
	require.Equal(t, want.PodGangMap.GenerationHash, *after.PCS.Status.CurrentGenerationHash)
	require.Equal(t, before.PCS.Status.AppliedSchedulingConfigurationHash, after.PCS.Status.AppliedSchedulingConfigurationHash,
		"an unavailable replacement must not advance the applied hash")
	progress := RequireActiveReplicaRecreation(t, after.PCS)
	require.Len(t, progress.CurrentlyUpdating, pcsReplicaIndex+1, "completed mappings must remain alongside the active replacement")
	seenPCSReplicaIndices := make(map[int32]struct{}, len(progress.CurrentlyUpdating))
	for _, selection := range progress.CurrentlyUpdating {
		require.GreaterOrEqual(t, selection.ReplicaIndex, int32(0))
		require.LessOrEqual(t, selection.ReplicaIndex, int32(pcsReplicaIndex))
		require.NotNil(t, selection.Replacement)
		require.Equal(t, selection.ReplicaIndex, selection.Replacement.TargetReplicaIndex)
		require.Nil(t, selection.Replacement.CanceledAt)
		seenPCSReplicaIndices[selection.ReplicaIndex] = struct{}{}
		if selection.ReplicaIndex == int32(pcsReplicaIndex) {
			require.Nil(t, selection.UpdateEndedAt, "held replica %d must remain unfinished", pcsReplicaIndex)
			require.NotNil(t, selection.Replacement.SourceCleanupCompletedAt)
			continue
		}
		require.NotNil(t, selection.UpdateEndedAt, "prior replica %d must remain completed", selection.ReplicaIndex)
	}
	require.Len(t, seenPCSReplicaIndices, pcsReplicaIndex+1, "source indices must be unique")
	return after
}

// AssertReplicaResourceUIDsUnchanged checks pending sources and completed targets are not recreated early or again.
func AssertReplicaResourceUIDsUnchanged(t *testing.T, before, after ReplicaRecreationSnapshot) {
	t.Helper()
	assert.Equal(t, before.PCLQ.UID, after.PCLQ.UID, "%s UID must remain unchanged", before.PCLQ.Name)
	assert.Equal(t, before.PCSG.UID, after.PCSG.UID, "%s UID must remain unchanged", before.PCSG.Name)
	assert.Equal(t, before.PGM.UID, after.PGM.UID, "%s UID must remain unchanged", before.PGM.Name)
}

// ReplicaRecreationSnapshot captures the PCS and one replica's pc-a, sg-x, and PodGangMap resources.
type ReplicaRecreationSnapshot struct {
	PCS  *grovev1beta1.PodCliqueSet
	PCLQ *grovev1beta1.PodClique
	PCSG *grovev1beta1.PodCliqueScalingGroup
	PGM  *grovev1beta1.PodGangMap
}

// RequireReplicaSnapshot fetches replica 0's resources and verifies the PCS scheduling hashes exist.
func RequireReplicaSnapshot(t *testing.T, tc *testctx.TestContext) ReplicaRecreationSnapshot {
	t.Helper()
	return RequireReplicaSnapshotForIndex(t, tc, 0)
}

// RequireReplicaSnapshotForIndex fetches one replica's resources and requires initialized PCS scheduling hashes.
func RequireReplicaSnapshotForIndex(t *testing.T, tc *testctx.TestContext, pcsReplicaIndex int) ReplicaRecreationSnapshot {
	t.Helper()
	pcs, err := GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err, "fetch PCS")
	require.NotNil(t, pcs.Status.CurrentGenerationHash)
	require.NotNil(t, pcs.Status.AppliedSchedulingConfigurationHash)
	require.NotEmpty(t, *pcs.Status.CurrentGenerationHash)
	require.NotEmpty(t, *pcs.Status.AppliedSchedulingConfigurationHash)
	var pclq grovev1beta1.PodClique
	var pcsg grovev1beta1.PodCliqueScalingGroup
	pclqName := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex}, "pc-a")
	pclqKey := types.NamespacedName{Namespace: tc.Namespace, Name: pclqName}
	pcsgKey := types.NamespacedName{Namespace: tc.Namespace, Name: PcsgFQNForReplica(tc, "sg-x", pcsReplicaIndex)}
	require.NoError(t, tc.Client.Get(tc.Ctx, pclqKey, &pclq), "fetch replica %d standalone pc-a", pcsReplicaIndex)
	require.NoError(t, tc.Client.Get(tc.Ctx, pcsgKey, &pcsg), "fetch replica %d sg-x", pcsReplicaIndex)
	pgm, err := GetReplicaPodGangMap(tc, pcsReplicaIndex)
	require.NoError(t, err, "fetch replica %d PodGangMap", pcsReplicaIndex)
	return ReplicaRecreationSnapshot{PCS: pcs, PCLQ: &pclq, PCSG: &pcsg, PGM: pgm}
}

// ExpectedReplicaConfiguration describes the child configuration and gang shape realized by a PCS replica.
type ExpectedReplicaConfiguration struct {
	StandaloneMinAvailable int32
	PCSGMinAvailable       int32
	PCSGReplicas           int32
	PodGangMap             ExpectedReplicaPodGangMap
}

// AssertReplicaConfiguration checks the applied generation markers, child configuration, and exact PGM composition.
func AssertReplicaConfiguration(t *testing.T, snapshot ReplicaRecreationSnapshot, want ExpectedReplicaConfiguration) {
	t.Helper()
	require.NotNil(t, snapshot.PCLQ.Spec.MinAvailable)
	require.NotNil(t, snapshot.PCSG.Spec.MinAvailable)
	require.NotEmpty(t, want.PodGangMap.GenerationHash, "expected applied generation must be known")
	assert.Equal(t, want.PodGangMap.GenerationHash, snapshot.PCLQ.Labels[apicommon.LabelPodCliqueSetGenerationHash], "pc-a applied generation")
	assert.Equal(t, want.PodGangMap.GenerationHash, snapshot.PCSG.Labels[apicommon.LabelPodCliqueSetGenerationHash], "sg-x applied generation")
	assert.Equal(t, want.StandaloneMinAvailable, *snapshot.PCLQ.Spec.MinAvailable, "pc-a MinAvailable")
	assert.Equal(t, want.PCSGMinAvailable, *snapshot.PCSG.Spec.MinAvailable, "sg-x MinAvailable")
	assert.Equal(t, want.PCSGReplicas, snapshot.PCSG.Spec.Replicas, "sg-x replicas")
	AssertReplicaPodGangMap(t, snapshot.PGM.Spec.Entries, want.PodGangMap)
}

// RequireRecreatedReplica waits for fresh replica 0 resources with the expected configuration and an
// established cleanup barrier. Its standalone PodClique must remain unavailable while readiness is blocked.
func RequireRecreatedReplica(
	t *testing.T,
	tc *testctx.TestContext,
	before ReplicaRecreationSnapshot,
	want ExpectedReplicaConfiguration,
) ReplicaRecreationSnapshot {
	t.Helper()
	pclq, err := WaitForRecreatedPCLQ(tc, before.PCLQ)
	require.NoError(t, err, "standalone pc-a must be recreated")
	require.NoError(t, WaitForStandaloneReplicaUnavailable(tc, "pc-a", 0))
	pcsg, err := WaitForRecreatedPCSG(tc, before.PCSG)
	require.NoError(t, err, "sg-x must be recreated")
	expectedEntryCount := 2
	if want.PodGangMap.TailIndices != nil {
		expectedEntryCount++
	}
	pgm, err := WaitForRecreatedPodGangMap(tc, before.PGM, want.PodGangMap.GenerationHash, expectedEntryCount)
	require.NoError(t, err, "PodGangMap must be recreated at the target generation")
	snapshot := RequireReplicaSnapshot(t, tc)
	require.Equal(t, pclq.UID, snapshot.PCLQ.UID, "recreated pc-a must remain the observed target")
	require.Equal(t, pcsg.UID, snapshot.PCSG.UID, "recreated sg-x must remain the observed target")
	require.Equal(t, pgm.UID, snapshot.PGM.UID, "recreated PodGangMap must remain the observed target")
	RequireReplicaResourceUIDsChanged(t, before, snapshot)
	require.Equal(t, want.PodGangMap.GenerationHash, *snapshot.PCS.Status.CurrentGenerationHash)
	require.Equal(t, before.PCS.Status.AppliedSchedulingConfigurationHash, snapshot.PCS.Status.AppliedSchedulingConfigurationHash,
		"an unavailable recreated target must not be marked applied")
	AssertReplicaConfiguration(t, snapshot, want)
	progress := RequireActiveReplicaRecreation(t, snapshot.PCS)
	replicaProgress := RequireActiveReplacement(t, progress, ExpectedReplicaReplacement{
		SourcePCSReplicaIndex: 0,
		TargetPCSReplicaIndex: 0,
	})
	require.NotNil(t, replicaProgress.Replacement.SourceCleanupCompletedAt)
	return snapshot
}

// RequireReplicaResourceUIDsChanged checks that structural recreation replaced every replica-scoped resource.
func RequireReplicaResourceUIDsChanged(t *testing.T, before, after ReplicaRecreationSnapshot) {
	t.Helper()
	require.NotEqual(t, before.PCLQ.UID, after.PCLQ.UID, "%s UID must change", before.PCLQ.Name)
	require.NotEqual(t, before.PCSG.UID, after.PCSG.UID, "%s UID must change", before.PCSG.Name)
	require.NotEqual(t, before.PGM.UID, after.PGM.UID, "%s UID must change", before.PGM.Name)
}

// RequireActiveReplicaRecreation returns the update progress of an unfinished structural recreation.
func RequireActiveReplicaRecreation(t *testing.T, pcs *grovev1beta1.PodCliqueSet) *grovev1beta1.PodCliqueSetUpdateProgress {
	t.Helper()
	require.NotNil(t, pcs)
	progress := pcs.Status.UpdateProgress
	require.NotNil(t, progress, "PCS %s must have update progress", pcs.Name)
	require.True(t, progress.RequiresReplicaRecreation, "PCS %s must require structural recreation", pcs.Name)
	require.Nil(t, progress.UpdateEndedAt, "PCS %s recreation must remain unfinished", pcs.Name)
	return progress
}

// ExpectedReplicaReplacement describes the source-to-target mapping committed by a replacement.
type ExpectedReplicaReplacement struct {
	SourcePCSReplicaIndex int32
	TargetPCSReplicaIndex int32
}

// RequireActiveReplacement returns the single selected replica's progress, requiring an unfinished,
// non-canceled replacement with the expected source and target indices.
func RequireActiveReplacement(
	t *testing.T,
	progress *grovev1beta1.PodCliqueSetUpdateProgress,
	want ExpectedReplicaReplacement,
) *grovev1beta1.PodCliqueSetReplicaUpdateProgress {
	t.Helper()
	require.NotNil(t, progress)
	require.Len(t, progress.CurrentlyUpdating, 1, "expected a single selected replacement")
	replicaProgress := &progress.CurrentlyUpdating[0]
	require.Equal(t, want.SourcePCSReplicaIndex, replicaProgress.ReplicaIndex, "replacement source index")
	require.Nil(t, replicaProgress.UpdateEndedAt, "replica %d replacement must remain unfinished", want.SourcePCSReplicaIndex)
	require.NotNil(t, replicaProgress.Replacement, "replica %d must have replacement progress", want.SourcePCSReplicaIndex)
	require.Equal(t, want.TargetPCSReplicaIndex, replicaProgress.Replacement.TargetReplicaIndex, "replacement target index")
	require.Nil(t, replicaProgress.Replacement.CanceledAt, "replica %d replacement must not be canceled", want.SourcePCSReplicaIndex)
	return replicaProgress
}

// RecreationTarget identifies the PCS generation and scheduling configuration of an update target.
type RecreationTarget struct {
	GenerationHash string
	SchedulingHash string
}

// RequireSchedulingTargetChanged verifies a new structural target without advancing the applied baseline.
func RequireSchedulingTargetChanged(t *testing.T, before, after *grovev1beta1.PodCliqueSet) RecreationTarget {
	t.Helper()
	require.NotNil(t, before.Status.CurrentGenerationHash)
	require.NotNil(t, before.Status.AppliedSchedulingConfigurationHash)
	require.NotNil(t, after.Status.CurrentGenerationHash)
	require.NotEqual(t, *before.Status.CurrentGenerationHash, *after.Status.CurrentGenerationHash, "PCS generation must change")
	progress := RequireActiveReplicaRecreation(t, after)
	require.NotEmpty(t, progress.TargetSchedulingConfigurationHash)
	previousSchedulingTarget := *before.Status.AppliedSchedulingConfigurationHash
	if before.Status.UpdateProgress != nil && before.Status.UpdateProgress.UpdateEndedAt == nil {
		previousSchedulingTarget = before.Status.UpdateProgress.TargetSchedulingConfigurationHash
	}
	require.NotEqual(t, previousSchedulingTarget, progress.TargetSchedulingConfigurationHash, "scheduling target must change")
	require.Equal(t, before.Status.AppliedSchedulingConfigurationHash, after.Status.AppliedSchedulingConfigurationHash,
		"an unavailable replacement must not advance the applied scheduling baseline")
	return RecreationTarget{GenerationHash: *after.Status.CurrentGenerationHash, SchedulingHash: progress.TargetSchedulingConfigurationHash}
}

// RequireRecreationSuperseded verifies target supersession, replacement preservation, cleanup reset,
// and update timestamps, returning the new target even when it matches the applied configuration.
func RequireRecreationSuperseded(t *testing.T, before, after *grovev1beta1.PodCliqueSet) RecreationTarget {
	t.Helper()
	beforeProgress := RequireActiveReplicaRecreation(t, before)
	require.Len(t, beforeProgress.CurrentlyUpdating, 1)
	beforeReplicaProgress := beforeProgress.CurrentlyUpdating[0]
	require.NotNil(t, beforeReplicaProgress.Replacement)
	wantReplacement := ExpectedReplicaReplacement{
		SourcePCSReplicaIndex: beforeReplicaProgress.ReplicaIndex,
		TargetPCSReplicaIndex: beforeReplicaProgress.Replacement.TargetReplicaIndex,
	}
	RequireActiveReplacement(t, beforeProgress, wantReplacement)
	target := RequireSchedulingTargetChanged(t, before, after)
	progress := RequireActiveReplicaRecreation(t, after)
	replicaProgress := RequireActiveReplacement(t, progress, wantReplacement)
	require.NoError(t, ValidateRecreationWhileBlocked(before, after), "supersession must preserve replacement commitments")
	require.Nil(t, replicaProgress.Replacement.SourceCleanupCompletedAt, "supersession must restart cleanup")
	require.NotNil(t, progress.PreviousPodCliqueSetGenerationHash)
	assert.Equal(t, *before.Status.CurrentGenerationHash, *progress.PreviousPodCliqueSetGenerationHash)
	assert.Equal(t, progress.UpdateStartedAt, replicaProgress.UpdateStartedAt, "replacement must use the new update's start time")
	return target
}

// RequireCleanupAfterSupersession waits for a fresh same-index cleanup barrier at the superseding target.
func RequireCleanupAfterSupersession(t *testing.T, tc *testctx.TestContext, updatePCS *grovev1beta1.PodCliqueSet) {
	t.Helper()
	updateProgress := RequireActiveReplicaRecreation(t, updatePCS)
	require.NotNil(t, updatePCS.Status.CurrentGenerationHash)
	require.Len(t, updateProgress.CurrentlyUpdating, 1)
	updateReplicaProgress := updateProgress.CurrentlyUpdating[0]
	require.NotNil(t, updateReplicaProgress.Replacement)
	require.Nil(t, updateReplicaProgress.Replacement.SourceCleanupCompletedAt, "supersession must reset the cleanup barrier")
	wantReplacement := ExpectedReplicaReplacement{
		SourcePCSReplicaIndex: updateReplicaProgress.ReplicaIndex,
		TargetPCSReplicaIndex: updateReplicaProgress.Replacement.TargetReplicaIndex,
	}
	require.Equal(t, wantReplacement.SourcePCSReplicaIndex, wantReplacement.TargetPCSReplicaIndex, "cleanup barrier requires a same-index replacement")
	cleanupPCS, err := WaitForSameIndexReplacementCleanup(tc, wantReplacement.SourcePCSReplicaIndex)
	require.NoError(t, err, "superseded target resources must be cleaned up")
	require.NotNil(t, cleanupPCS.Status.CurrentGenerationHash)
	require.Equal(t, *updatePCS.Status.CurrentGenerationHash, *cleanupPCS.Status.CurrentGenerationHash)
	cleanupProgress := RequireActiveReplicaRecreation(t, cleanupPCS)
	cleanupReplicaProgress := RequireActiveReplacement(t, cleanupProgress, wantReplacement)
	cleanupAt := cleanupReplicaProgress.Replacement.SourceCleanupCompletedAt
	require.NotNil(t, cleanupAt)
	assert.False(t, cleanupAt.Before(&updateProgress.UpdateStartedAt), "cleanup completion must not predate supersession")
}

// ExpectedReplicaRecreationCompletion describes the final scheduling state, identities, and replica configuration.
type ExpectedReplicaRecreationCompletion struct {
	GenerationHash    string
	SchedulingHash    string
	Replicas          int32
	Pods              int
	PCSReplicaIndices []int
	Configuration     ExpectedReplicaConfiguration
}

// RequireReplicaRecreationConverged waits for completion and verifies status, ready pods, replica identities,
// child configuration, gang composition, and the absence of active child updates.
func RequireReplicaRecreationConverged(t *testing.T, tc *testctx.TestContext, want ExpectedReplicaRecreationCompletion) {
	t.Helper()
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	// Inspect the final state even on timeout so a failure also reports the target PGM composition.
	assert.NoError(t, WaitForReplicaRecreationComplete(&tcLongTimeout, want.Replicas))
	require.NoError(t, tc.WaitForPods(want.Pods))
	after := RequireReplicaSnapshot(t, tc)
	AssertReplicaRecreationCompleted(t, after.PCS, want)
	require.NoError(t, WaitForClaimedPCSReplicaIndices(tc, want.PCSReplicaIndices))
	AssertReplicaConfiguration(t, after, want.Configuration)
	AssertGenerationHashConverged(tc)
	AssertNoActiveChildUpdates(tc)
}

// AssertReplicaRecreationCompleted checks the completed update's hashes, progress, and replica counts.
func AssertReplicaRecreationCompleted(t *testing.T, pcs *grovev1beta1.PodCliqueSet, want ExpectedReplicaRecreationCompletion) {
	t.Helper()
	require.NotNil(t, pcs)
	require.NotNil(t, pcs.Status.CurrentGenerationHash)
	require.NotNil(t, pcs.Status.AppliedSchedulingConfigurationHash)
	assert.Equal(t, want.GenerationHash, *pcs.Status.CurrentGenerationHash, "PCS generation hash")
	assert.Equal(t, want.SchedulingHash, *pcs.Status.AppliedSchedulingConfigurationHash, "applied scheduling hash")
	require.NotNil(t, pcs.Status.UpdateProgress)
	assert.Equal(t, want.SchedulingHash, pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash, "target scheduling hash")
	assert.NotNil(t, pcs.Status.UpdateProgress.UpdateEndedAt, "recreation must have completed")
	assert.Empty(t, pcs.Status.UpdateProgress.CurrentlyUpdating, "completed recreation must release its selections")
	assert.Equal(t, want.Replicas, pcs.Status.AvailableReplicas, "available PCS replicas")
	assert.Equal(t, want.Replicas, pcs.Status.UpdatedReplicas, "updated PCS replicas")
}

// SerialRecreationObserver records a single scheduling change from a healthy, dense PCS identity set.
// It validates every watched status, not just polled milestones. All access is synchronous in the caller;
// neither clients nor watches are stored in this lifecycle state.
type SerialRecreationObserver struct {
	baseline              RecreationTarget
	target                RecreationTarget
	pcsReplicas           int32
	selectionsBySource    map[int32]*grovev1beta1.PodCliqueSetReplicaUpdateProgress
	selectionOrder        []int32
	activePCSReplicaIndex *int32
	isComplete            bool
}

// NewSerialRecreationObserver tracks serial replacement of a healthy, dense PCS identity set.
func NewSerialRecreationObserver(pcsReplicas int32, baseline RecreationTarget) *SerialRecreationObserver {
	return &SerialRecreationObserver{pcsReplicas: pcsReplicas, baseline: baseline}
}

// WaitForActivePCSReplica preserves the same watch across held-replica milestones. The caller owns Stop.
func (o *SerialRecreationObserver) WaitForActivePCSReplica(
	ctx context.Context,
	watcher watch.Interface,
	pcsReplicaIndex int32,
) (*grovev1beta1.PodCliqueSet, error) {
	pcs, err := o.observeUntil(ctx, watcher, func() bool {
		return o.activePCSReplicaIndex != nil && *o.activePCSReplicaIndex == pcsReplicaIndex
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for active PCS replica %d: %w", pcsReplicaIndex, err)
	}
	return pcs, nil
}

func (o *SerialRecreationObserver) observeUntil(ctx context.Context, watcher watch.Interface, reached func() bool) (*grovev1beta1.PodCliqueSet, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case event, open := <-watcher.ResultChan():
			if !open {
				return nil, fmt.Errorf("PodCliqueSet watch closed before the serial recreation milestone")
			}
			switch event.Type {
			case watch.Error:
				return nil, fmt.Errorf("PodCliqueSet watch failed: %w", apierrors.FromObject(event.Object))
			case watch.Deleted:
				return nil, fmt.Errorf("PodCliqueSet deleted during serial recreation")
			case watch.Bookmark:
				continue
			case watch.Added, watch.Modified:
			default:
				return nil, fmt.Errorf("unexpected PodCliqueSet watch event %q", event.Type)
			}
			pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
			if !ok || pcs == nil {
				return nil, fmt.Errorf("unexpected PodCliqueSet watch object %T", event.Object)
			}
			if err := o.observe(pcs.Status); err != nil {
				return nil, err
			}
			if reached() {
				return pcs.DeepCopy(), nil
			}
		}
	}
}

// observe validates one status and retains an owned copy of the latest source-to-target commitments.
func (o *SerialRecreationObserver) observe(status grovev1beta1.PodCliqueSetStatus) error {
	if status.CurrentGenerationHash == nil {
		return fmt.Errorf("PCS generation hash disappeared")
	}
	if o.target.GenerationHash == "" && *status.CurrentGenerationHash == o.baseline.GenerationHash {
		return nil // Status writes before the combined spec change are not part of this recreation.
	}
	progress := status.UpdateProgress
	if progress == nil {
		return fmt.Errorf("recreation progress disappeared")
	}
	target := RecreationTarget{GenerationHash: *status.CurrentGenerationHash, SchedulingHash: progress.TargetSchedulingConfigurationHash}
	if target.SchedulingHash == "" || target.SchedulingHash == o.baseline.SchedulingHash {
		return fmt.Errorf("recreation has no new scheduling target")
	}
	if o.target.GenerationHash != "" && target != o.target {
		return fmt.Errorf("recreation target changed: before %+v, after %+v", o.target, target)
	}
	if progress.UpdateEndedAt != nil {
		if status.AppliedSchedulingConfigurationHash == nil || *status.AppliedSchedulingConfigurationHash != target.SchedulingHash {
			return fmt.Errorf("completed recreation did not apply its target")
		}
		if len(progress.CurrentlyUpdating) != 0 {
			return fmt.Errorf("completed recreation retained selections")
		}
		if len(o.selectionOrder) != int(o.pcsReplicas) {
			return fmt.Errorf("recreation completed after observing %d of %d selections", len(o.selectionOrder), o.pcsReplicas)
		}
		o.activePCSReplicaIndex = nil
		o.isComplete = true
		return nil
	}
	if !progress.RequiresReplicaRecreation {
		return fmt.Errorf("structural recreation mode disappeared before completion")
	}
	if status.AppliedSchedulingConfigurationHash == nil || *status.AppliedSchedulingConfigurationHash != o.baseline.SchedulingHash {
		return fmt.Errorf("scheduling hash advanced before overall completion")
	}
	selectionsBySource, activePCSReplicaIndex, err := indexSerialRecreationSelections(progress.CurrentlyUpdating, o.pcsReplicas)
	if err != nil {
		return err
	}
	for pcsReplicaIndex, before := range o.selectionsBySource {
		after, exists := selectionsBySource[pcsReplicaIndex]
		if !exists {
			return fmt.Errorf("source %d mapping disappeared before overall completion", pcsReplicaIndex)
		}
		if before.UpdateEndedAt != nil && after.UpdateEndedAt == nil {
			return fmt.Errorf("completed source %d became unfinished", pcsReplicaIndex)
		}
		if !before.UpdateStartedAt.Equal(&after.UpdateStartedAt) {
			return fmt.Errorf("source %d replacement start time changed", pcsReplicaIndex)
		}
	}
	for pcsReplicaIndex, selection := range selectionsBySource {
		if _, exists := o.selectionsBySource[pcsReplicaIndex]; exists {
			continue
		}
		if pcsReplicaIndex != int32(len(o.selectionOrder)) {
			return fmt.Errorf("selected source %d, expected next source %d", pcsReplicaIndex, len(o.selectionOrder))
		}
		if selection.UpdateEndedAt != nil {
			return fmt.Errorf("source %d was already complete when first observed", pcsReplicaIndex)
		}
		if pcsReplicaIndex > 0 {
			previous := selectionsBySource[pcsReplicaIndex-1]
			if previous == nil || previous.UpdateEndedAt == nil {
				return fmt.Errorf("source %d selected before its predecessor completed", pcsReplicaIndex)
			}
			if selection.UpdateStartedAt.Before(previous.UpdateEndedAt) {
				return fmt.Errorf("source %d started before its predecessor completed", pcsReplicaIndex)
			}
		}
		o.selectionOrder = append(o.selectionOrder, pcsReplicaIndex)
	}
	o.target = target
	o.selectionsBySource = selectionsBySource
	o.activePCSReplicaIndex = activePCSReplicaIndex
	return nil
}

// indexSerialRecreationSelections rejects malformed commitments and enforces a single active replacement.
func indexSerialRecreationSelections(
	selections []grovev1beta1.PodCliqueSetReplicaUpdateProgress,
	pcsReplicas int32,
) (map[int32]*grovev1beta1.PodCliqueSetReplicaUpdateProgress, *int32, error) {
	selectionsBySource := make(map[int32]*grovev1beta1.PodCliqueSetReplicaUpdateProgress, len(selections))
	var activePCSReplicaIndex *int32
	activeCount := 0
	for _, selection := range selections {
		pcsReplicaIndex := selection.ReplicaIndex
		if pcsReplicaIndex < 0 || pcsReplicaIndex >= pcsReplicas {
			return nil, nil, fmt.Errorf("unexpected source %d", pcsReplicaIndex)
		}
		if _, exists := selectionsBySource[pcsReplicaIndex]; exists {
			return nil, nil, fmt.Errorf("duplicate source %d", pcsReplicaIndex)
		}
		if selection.Replacement == nil {
			return nil, nil, fmt.Errorf("source %d has no replacement progress", pcsReplicaIndex)
		}
		if selection.Replacement.TargetReplicaIndex != pcsReplicaIndex {
			return nil, nil, fmt.Errorf("source %d targets %d, expected same-index replacement", pcsReplicaIndex, selection.Replacement.TargetReplicaIndex)
		}
		if selection.Replacement.CanceledAt != nil {
			return nil, nil, fmt.Errorf("source %d replacement was canceled", pcsReplicaIndex)
		}
		if selection.UpdateEndedAt == nil {
			activeCount++
			activePCSReplicaIndex = new(pcsReplicaIndex)
		}
		selectionsBySource[pcsReplicaIndex] = selection.DeepCopy()
	}
	if activeCount > 1 {
		return nil, nil, fmt.Errorf("serial recreation has %d unfinished non-canceled replacements", activeCount)
	}
	return selectionsBySource, activePCSReplicaIndex, nil
}

// WaitForCompletion consumes the remainder of the original watch, including the final active replacement.
func (o *SerialRecreationObserver) WaitForCompletion(ctx context.Context, watcher watch.Interface) (*grovev1beta1.PodCliqueSet, error) {
	pcs, err := o.observeUntil(ctx, watcher, func() bool { return o.isComplete })
	if err != nil {
		return nil, fmt.Errorf("waiting for serial recreation completion: %w", err)
	}
	return pcs, nil
}

// WaitForRecreationReplicaCount observes a scale change without permitting active replacement commitments to change.
// The watch must be started before scaling. Watch loss is an error, not a reason to skip intermediate statuses.
func WaitForRecreationReplicaCount(ctx context.Context, watcher watch.Interface, before *grovev1beta1.PodCliqueSet, pcsReplicas int32) (*grovev1beta1.PodCliqueSet, error) {
	event, err := watchtools.UntilWithoutRetry(ctx, watcher, func(event watch.Event) (bool, error) {
		switch event.Type {
		case watch.Error:
			return false, apierrors.FromObject(event.Object)
		case watch.Deleted:
			return false, fmt.Errorf("PCS deleted during recreation scaling")
		case watch.Added, watch.Modified:
		default:
			return false, nil
		}
		pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
		if !ok || pcs == nil {
			return false, fmt.Errorf("unexpected recreation scaling object %T", event.Object)
		}
		if err := ValidateRecreationWhileBlocked(before, pcs); err != nil {
			return false, err
		}
		if !assert.ObjectsAreEqual(before.Status.CurrentGenerationHash, pcs.Status.CurrentGenerationHash) ||
			before.Status.UpdateProgress.TargetSchedulingConfigurationHash != pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash {
			return false, fmt.Errorf("PCS scaling superseded the scheduling target")
		}
		return pcs.Spec.Replicas == pcsReplicas, nil
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for recreation scale to %d replicas: %w", pcsReplicas, err)
	}
	return event.Object.(*grovev1beta1.PodCliqueSet).DeepCopy(), nil
}

// HoldReplicaSourceCleanup adds a test finalizer to keep a source PodClique terminating.
// The returned release is idempotent and removes only this finalizer from the original UID.
func HoldReplicaSourceCleanup(tc *testctx.TestContext, source *grovev1beta1.PodClique) (func() error, error) {
	const finalizer = "e2e.grove.io/hold-recreation-source-cleanup"
	key := client.ObjectKeyFromObject(source)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current grovev1beta1.PodClique
		if err := tc.Client.Get(tc.Ctx, key, &current); err != nil {
			return err
		}
		if current.UID != source.UID {
			return fmt.Errorf("source %s UID changed before cleanup hold", key)
		}
		base := current.DeepCopy()
		controllerutil.AddFinalizer(&current, finalizer)
		return tc.Client.Patch(tc.Ctx, &current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	}); err != nil {
		return nil, fmt.Errorf("hold source cleanup: %w", err)
	}
	return func() error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var current grovev1beta1.PodClique
			if err := tc.Client.Get(tc.Ctx, key, &current); err != nil {
				return client.IgnoreNotFound(err)
			}
			if current.UID != source.UID {
				return nil
			}
			base := current.DeepCopy()
			if !controllerutil.RemoveFinalizer(&current, finalizer) {
				return nil
			}
			return tc.Client.Patch(tc.Ctx, &current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		})
	}, nil
}

// WaitForReplicaSourceDeletion waits until cleanup reaches the held source PodClique.
func WaitForReplicaSourceDeletion(tc *testctx.TestContext, source *grovev1beta1.PodClique) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var current grovev1beta1.PodClique
		if err := tc.Client.Get(ctx, client.ObjectKeyFromObject(source), &current); err != nil {
			return false, err
		}
		if current.UID != source.UID {
			return false, fmt.Errorf("source %s was recreated before cleanup completed", source.Name)
		}
		return current.DeletionTimestamp != nil, nil
	})
}

// RequireRecreationResumed checks that a restart preserved the active update and its cleanup commitment.
func RequireRecreationResumed(t *testing.T, before, after *grovev1beta1.PodCliqueSet) {
	t.Helper()
	beforeProgress := RequireActiveReplicaRecreation(t, before)
	afterProgress := RequireActiveReplicaRecreation(t, after)
	require.NotNil(t, before.Status.CurrentGenerationHash)
	require.NotNil(t, after.Status.CurrentGenerationHash)
	assert.Equal(t, before.Status.CurrentGenerationHash, after.Status.CurrentGenerationHash, "restart must preserve the target generation")
	assert.Equal(t, beforeProgress.TargetSchedulingConfigurationHash, afterProgress.TargetSchedulingConfigurationHash, "restart must preserve the scheduling target")
	assert.Equal(t, beforeProgress.UpdateStartedAt, afterProgress.UpdateStartedAt, "restart must not initialize another update")
	assert.Equal(t, beforeProgress.PreviousPodCliqueSetGenerationHash, afterProgress.PreviousPodCliqueSetGenerationHash)
	require.NoError(t, ValidateRecreationWhileBlocked(before, after), "restart must preserve the mapping, cleanup barrier, and unapplied baseline")
}

// SelectionOrder returns an owned copy of the observed source selection order.
func (o *SerialRecreationObserver) SelectionOrder() []int32 { return slices.Clone(o.selectionOrder) }
