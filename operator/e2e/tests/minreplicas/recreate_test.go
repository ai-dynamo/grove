//go:build e2e

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

package minreplicas

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// newRecreatePCS returns a PodCliqueSet whose worker clique has replicas 2, init 1 and availability 1. It runs 2
// pods per replica.
func newRecreatePCS(name string, replicas int32, strategy grovev1beta1.UpdateStrategyType, breachTolerance time.Duration) *grovev1beta1.PodCliqueSet {
	return newPCS(name, replicas, strategy, []cliqueSpec{
		{name: workerClique, replicas: 2, init: 1, availability: 1, breachTolerance: breachTolerance},
	})
}

// requireAllChanged fails the test unless every UID in after differs from the one at the same index in before.
func requireAllChanged(t *testing.T, before, after []types.UID) {
	t.Helper()
	for i := range before {
		require.NotEqual(t, before[i], after[i], "the worker PodClique of replica %d must have been recreated", i)
	}
}

// Test_MRM6_RaisingInitRecreatesReplicasOneAtATime verifies that raising init.minReplicas of a standalone clique
// recreates every replica exactly once, one at a time, and that the new gang shape reaches the PodGangs.
func Test_MRM6_RaisingInitRecreatesReplicasOneAtATime(t *testing.T) {
	const pcsName = "mrm6"
	tc, cleanup := prepare(t, 10, pcsName)
	defer cleanup()
	deploy(t, tc, newRecreatePCS(pcsName, 3, grovev1beta1.RollingRecreateStrategy, 0))
	hashBefore := componentutils.ComputeGangShapeHash(getPCS(t, tc, pcsName))
	uidsBefore := workerUIDs(t, tc, pcsName, 3)

	tests.Logger.Info("1. Raise init.minReplicas from 1 to 2")
	observer := observeRecreate(tc, pcsName)
	requireAccepted(t, tc, pcsName, "init.minReplicas 2", setWorkerInit(2))
	pcs := waitForGangShapeCurrent(t, tc, pcsName)
	observer.stop()

	tests.Logger.Info("2. Every replica was recreated once, in index order, and never more than one was unavailable")
	require.NotEqual(t, hashBefore, componentutils.ComputeGangShapeHash(pcs), "the gang-shape hash must change")
	requireAllChanged(t, uidsBefore, workerUIDs(t, tc, pcsName, 3))
	require.Equal(t, onceEach(0, 1, 2), observer.workerDeletions(3), "each replica must be deleted once")
	require.Equal(t, []int{0, 1, 2}, observer.order, "replicas must be recreated in index order")
	require.LessOrEqual(t, observer.maxUnavailable, 1, "at most one replica may be unavailable at a time")

	tests.Logger.Info("3. The recreated PodCliques and their PodGangs carry init.minReplicas 2")
	for i := range 3 {
		require.Equal(t, int32(2), *getPCLQ(t, tc, pclqName(pcsName, i, workerClique)).Spec.Init.MinReplicas)
	}
	requirePodGroupMinReplicas(t, tc, pcsName, workerClique, 2)
}

// Test_MRM7_LoweringScalingGroupInitRecreatesReplicas verifies that lowering init.minReplicas of a scaling group
// recreates every replica once, one at a time, and that the scaling groups carry the new value.
func Test_MRM7_LoweringScalingGroupInitRecreatesReplicas(t *testing.T) {
	const pcsName = "mrm7"
	tc, cleanup := prepare(t, 10, pcsName)
	defer cleanup()
	deploy(t, tc, newPCS(pcsName, 2, grovev1beta1.RollingRecreateStrategy,
		[]cliqueSpec{
			{name: workerClique, replicas: 1, init: 1, availability: 1},
			{name: memberClique, replicas: 1, init: 1},
		},
		scalingGroupSpec{name: scalingGroup, cliqueNames: []string{memberClique}, replicas: 2, init: 2, availability: 1},
	))
	uidsBefore := workerUIDs(t, tc, pcsName, 2)

	tests.Logger.Info("1. Lower the scaling group's init.minReplicas from 2 to 1")
	observer := observeRecreate(tc, pcsName)
	requireAccepted(t, tc, pcsName, "scaling group init.minReplicas 1", setGroupInit(scalingGroup, 1))
	waitForGangShapeCurrent(t, tc, pcsName)
	observer.stop()

	tests.Logger.Info("2. Every replica was recreated once and never more than one was unavailable")
	requireAllChanged(t, uidsBefore, workerUIDs(t, tc, pcsName, 2))
	require.Equal(t, onceEach(0, 1), observer.workerDeletions(2))
	require.LessOrEqual(t, observer.maxUnavailable, 1, "at most one replica may be unavailable at a time")
	for i := range 2 {
		require.Equal(t, int32(1), *getPCSG(t, tc, pcsgName(pcsName, i, scalingGroup)).Spec.Init.MinReplicas)
	}
}

// Test_MRM8_RecreateUnderEveryUpdateStrategy verifies that an init.minReplicas change recreates the replicas
// under every update strategy, including OnDelete.
func Test_MRM8_RecreateUnderEveryUpdateStrategy(t *testing.T) {
	for _, strategy := range []grovev1beta1.UpdateStrategyType{
		grovev1beta1.RollingRecreateStrategy,
		grovev1beta1.CoherentStrategy,
		grovev1beta1.OnDeleteStrategy,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			pcsName := "mrm8-" + strings.ToLower(string(strategy))
			tc, cleanup := prepare(t, 8, pcsName)
			defer cleanup()
			deploy(t, tc, newRecreatePCS(pcsName, 2, strategy, 0))
			uidsBefore := workerUIDs(t, tc, pcsName, 2)

			observer := observeRecreate(tc, pcsName)
			requireAccepted(t, tc, pcsName, "init.minReplicas 2", setWorkerInit(2))
			waitForGangShapeCurrent(t, tc, pcsName)
			observer.stop()

			requireAllChanged(t, uidsBefore, workerUIDs(t, tc, pcsName, 2))
			require.Equal(t, onceEach(0, 1), observer.workerDeletions(2))
		})
	}
}

// Test_MRM9_RecreateOrderAndPacing verifies that an unavailable replica is recreated first, that the next replica
// is only deleted once the recreated one is available, and that the rest follow in index order.
func Test_MRM9_RecreateOrderAndPacing(t *testing.T) {
	const pcsName = "mrm9"
	tc, cleanup := prepare(t, 14, pcsName)
	defer cleanup()
	deploy(t, tc, newRecreatePCS(pcsName, 4, grovev1beta1.RollingRecreateStrategy, 0))

	tests.Logger.Info("1. Make replica 2 unavailable without a breach, by holding its pods Pending")
	hold := holdStage(pcsName, 2)
	hold.apply(t, tc)
	deletePods(t, tc, livePodsOfReplica(t, tc, pcsName, 2)...)
	waitForPCLQ(t, tc, pclqName(pcsName, 2, workerClique), "no ready pods", func(pclq *grovev1beta1.PodClique) bool {
		return pclq.Status.ReadyReplicas == 0 && !isBreached(pclq)
	})
	uidsBefore := workerUIDs(t, tc, pcsName, 4)

	tests.Logger.Info("2. Raise init.minReplicas: replica 2 goes first")
	observer := observeRecreate(tc, pcsName)
	requireAccepted(t, tc, pcsName, "init.minReplicas 2", setWorkerInit(2))
	waitForPCS(t, tc, pcsName, "replica 2 to be recreated", func(pcs *grovev1beta1.PodCliqueSet) bool {
		progress := pcs.Status.GangShapeRecreate
		return progress != nil && progress.CurrentlyRecreating != nil && *progress.CurrentlyRecreating == 2
	})

	tests.Logger.Info("3. While recreated replica 2 cannot become available, no other replica is deleted")
	waitForPCS(t, tc, pcsName, "reason WaitingForAvailableReplicas", func(pcs *grovev1beta1.PodCliqueSet) bool {
		cond := gangShapeCondition(pcs)
		return cond != nil && cond.Reason == apiconstants.ConditionReasonWaitingForAvailableReplicas
	})
	requireStable(t, 20*time.Second, "no other replica may be deleted", func() error {
		uids := workerUIDs(t, tc, pcsName, 4)
		for _, i := range []int{0, 1, 3} {
			if uids[i] != uidsBefore[i] {
				return fmt.Errorf("replica %d was deleted while recreated replica 2 was unavailable", i)
			}
		}
		return nil
	})

	tests.Logger.Info("4. Release replica 2: the others are recreated in index order")
	hold.release(t, tc)
	waitForGangShapeCurrent(t, tc, pcsName)
	observer.stop()
	require.Equal(t, []int{2, 0, 1, 3}, observer.order)
	require.Equal(t, onceEach(0, 1, 2, 3), observer.workerDeletions(4))
	require.LessOrEqual(t, observer.maxUnavailable, 1, "at most one replica may be unavailable at a time")
	requireAllChanged(t, uidsBefore, workerUIDs(t, tc, pcsName, 4))
}

// Test_MRM10_GangTerminationDuringRecreate verifies that gang termination is suspended only for the replica
// being recreated, that another replica's breach still gang-terminates it, and that a stale replica deleted by
// gang termination is not recreated again.
func Test_MRM10_GangTerminationDuringRecreate(t *testing.T) {
	const pcsName = "mrm10"
	const breachTolerance = 15 * time.Second
	tc, cleanup := prepare(t, 12, pcsName)
	defer cleanup()
	deploy(t, tc, newRecreatePCS(pcsName, 3, grovev1beta1.RollingRecreateStrategy, breachTolerance))
	uidsBefore := workerUIDs(t, tc, pcsName, 3)

	tests.Logger.Info("1. Make every fresh pod of replicas 0 and 1 crash-loop, then raise init.minReplicas")
	crashLoop := crashLoopStage(pcsName, 0, 1)
	crashLoop.apply(t, tc)
	observer := observeRecreate(tc, pcsName)
	requireAccepted(t, tc, pcsName, "init.minReplicas 2", setWorkerInit(2))

	tests.Logger.Info("2. Recreated replica 0 breaches, but is not gang-terminated")
	replica0 := pclqName(pcsName, 0, workerClique)
	recreated := waitForPCLQ(t, tc, replica0, "the recreated replica 0 to breach", func(pclq *grovev1beta1.PodClique) bool {
		return pclq.UID != uidsBefore[0] && isBreached(pclq)
	})
	hash := recreated.Labels[common.LabelGangShapeHash]

	tests.Logger.Info("3. Replica 1 breaches and is gang-terminated after the breach tolerance")
	deletePods(t, tc, livePodsOfReplica(t, tc, pcsName, 1)...)
	replica1 := waitForPCLQ(t, tc, pclqName(pcsName, 1, workerClique), "replica 1 to be gang-terminated", func(pclq *grovev1beta1.PodClique) bool {
		return pclq.UID != uidsBefore[1]
	})
	progress := getPCS(t, tc, pcsName).Status.GangShapeRecreate
	require.True(t, progress != nil && ptr.Equal(progress.CurrentlyRecreating, ptr.To[int32](0)),
		"replica 1 must have been deleted by gang termination, while the recreate was still on replica 0")
	require.Equal(t, hash, replica1.Labels[common.LabelGangShapeHash], "gang termination must recreate replica 1 with the current gang shape")

	tests.Logger.Info("4. Replica 0 has been breached for longer than the tolerance and was never gang-terminated")
	pclq := getPCLQ(t, tc, replica0)
	require.Equal(t, recreated.UID, pclq.UID, "the replica being recreated must not be gang-terminated")
	require.Greater(t, time.Since(breachCondition(pclq).LastTransitionTime.Time), breachTolerance)
	pcs := getPCS(t, tc, pcsName)
	require.NotNil(t, pcs.Status.GangShapeRecreate)
	require.Equal(t, int32(0), *pcs.Status.GangShapeRecreate.CurrentlyRecreating)

	tests.Logger.Info("5. Release replicas 0 and 1: only replica 2 is still recreated")
	crashLoop.release(t, tc)
	waitForGangShapeCurrent(t, tc, pcsName)
	observer.stop()
	require.Equal(t, []int{0, 2}, observer.order, "replica 1 must not be recreated again after gang termination")
	require.Equal(t, onceEach(0, 1, 2), observer.workerDeletions(3), "replica 1 must only be deleted by its gang termination")
}

// Test_MRM11_InitAndPodTemplateChangedTogether verifies that when init.minReplicas and the pod template change in
// one update, each replica is deleted once, comes back with the new template, and the rolling update finishes.
func Test_MRM11_InitAndPodTemplateChangedTogether(t *testing.T) {
	for _, strategy := range []grovev1beta1.UpdateStrategyType{grovev1beta1.RollingRecreateStrategy, grovev1beta1.CoherentStrategy} {
		t.Run(string(strategy), func(t *testing.T) {
			pcsName := "mrm11-" + strings.ToLower(string(strategy))
			tc, cleanup := prepare(t, 10, pcsName)
			defer cleanup()
			deploy(t, tc, newRecreatePCS(pcsName, 3, strategy, 0))

			observer := observeRecreate(tc, pcsName)
			revision := newRevision()
			requireAccepted(t, tc, pcsName, "init.minReplicas and pod template change", setWorkerInit(2), changePodTemplate(revision, workerClique))
			waitForGangShapeCurrent(t, tc, pcsName)
			waitForRollingUpdateComplete(t, tc, pcsName, revision)
			observer.stop()

			require.Equal(t, onceEach(0, 1, 2), observer.workerDeletions(3), "each replica must be deleted once")
			for i := range 3 {
				require.LessOrEqual(t, observer.distinctPods(i), 4, "replica %d must only replace its 2 pods once", i)
			}
		})
	}
}
