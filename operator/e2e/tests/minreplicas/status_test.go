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
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
)

// startHeldRecreate holds the fresh pods of replica 0, raises init.minReplicas of the worker clique to init, and
// waits until replica 0 has been recreated and is waiting to become available. It returns the hold stage and the
// new gang-shape hash.
func startHeldRecreate(t *testing.T, tc *testctx.TestContext, pcsName string, init int32) (podStage, string) {
	t.Helper()
	hold := holdStage(pcsName, 0)
	hold.apply(t, tc)
	requireAccepted(t, tc, pcsName, fmt.Sprintf("init.minReplicas %d", init), setWorkerInit(init))
	hash := componentutils.ComputeGangShapeHash(getPCS(t, tc, pcsName))
	waitForPCS(t, tc, pcsName, "replica 0 to wait for availability", func(pcs *grovev1beta1.PodCliqueSet) bool {
		return isWaitingOnReplica(pcs, hash, 0)
	})
	return hold, hash
}

// isWaitingOnReplica reports whether pcs is recreating to hash and waits for the recreated replica to become available.
func isWaitingOnReplica(pcs *grovev1beta1.PodCliqueSet, hash string, replicaIndex int32) bool {
	progress := pcs.Status.GangShapeRecreate
	cond := gangShapeCondition(pcs)
	return progress != nil && progress.GangShapeHash == hash && ptr.Equal(progress.CurrentlyRecreating, ptr.To(replicaIndex)) &&
		cond != nil && cond.Status == metav1.ConditionTrue && cond.Reason == apiconstants.ConditionReasonWaitingForAvailableReplicas
}

// Test_MRM21_RecreateStatus verifies status.gangShapeRecreate and the GangShapeRecreateInProgress condition through
// a recreate, that a recreate does not use updateProgress, so a rollingUpdate.minReplicas change is still
// accepted, and that a PodCliqueSet without a recreate carries no condition.
func Test_MRM21_RecreateStatus(t *testing.T) {
	const pcsName = "mrm21"
	tc, cleanup := prepare(t, 10, pcsName)
	defer cleanup()
	deploy(t, tc, newRecreatePCS(pcsName, 3, grovev1beta1.CoherentStrategy, 0))

	tests.Logger.Info("1. Before any recreate there is no status and no condition")
	requireNoGangShapeRecreate(t, getPCS(t, tc, pcsName))

	tests.Logger.Info("2. During the recreate the status names the replica and the hash")
	observer := observeRecreate(tc, pcsName)
	hold, hash := startHeldRecreate(t, tc, pcsName, 2)
	pcs := getPCS(t, tc, pcsName)
	require.False(t, pcs.Status.GangShapeRecreate.StartedAt.IsZero())
	require.Nil(t, pcs.Status.UpdateProgress, "a recreate must not use updateProgress")

	tests.Logger.Info("3. A rollingUpdate.minReplicas change is accepted during a recreate")
	requireAccepted(t, tc, pcsName, "rollingUpdate.minReplicas 2 during a recreate", setWorkerRollingUpdate(ptr.To[int32](2), ptr.To(intstr.FromInt32(2))))

	tests.Logger.Info("4. Once every replica is recreated the status is cleared and the condition stays False")
	hold.release(t, tc)
	pcs = waitForGangShapeCurrent(t, tc, pcsName)
	observer.stop()
	require.Equal(t, hash, componentutils.ComputeGangShapeHash(pcs))
	require.True(t, observer.reasons.Has(apiconstants.ConditionReasonWaitingForAvailableReplicas))
	require.Empty(t, observer.reasons.Difference(sets.New(apiconstants.ConditionReasonRecreatingReplica, apiconstants.ConditionReasonWaitingForAvailableReplicas)).UnsortedList(),
		"unexpected condition reasons while in progress")
	require.False(t, observer.updateProgressSeen, "a recreate must not use updateProgress")
	requireStable(t, 10*time.Second, "the condition must stay False with reason GangShapeCurrent", func() error {
		cond := gangShapeCondition(getPCS(t, tc, pcsName))
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != apiconstants.ConditionReasonGangShapeCurrent {
			return fmt.Errorf("condition changed to %v", cond)
		}
		return nil
	})
}

// Test_MRM22_InitChangedAgainDuringRecreate verifies that an init.minReplicas change during a recreate moves the
// recreate to the new hash and makes the replicas already recreated stale again.
func Test_MRM22_InitChangedAgainDuringRecreate(t *testing.T) {
	const pcsName = "mrm22"
	tc, cleanup := prepare(t, 13, pcsName)
	defer cleanup()
	deploy(t, tc, newPCS(pcsName, 3, grovev1beta1.RollingRecreateStrategy, []cliqueSpec{{name: workerClique, replicas: 3, init: 1, availability: 1}}))

	tests.Logger.Info("1. Raise init.minReplicas to 2 and hold the recreated replica 0")
	hold, firstHash := startHeldRecreate(t, tc, pcsName, 2)
	waitForPCLQ(t, tc, pclqName(pcsName, 0, workerClique), "the first hash", func(pclq *grovev1beta1.PodClique) bool {
		return pclq.Labels[common.LabelGangShapeHash] == firstHash
	})

	tests.Logger.Info("2. Raise init.minReplicas to 3: the recreate moves to the new hash")
	requireAccepted(t, tc, pcsName, "init.minReplicas 3", setWorkerInit(3))
	secondHash := componentutils.ComputeGangShapeHash(getPCS(t, tc, pcsName))
	require.NotEqual(t, firstHash, secondHash)
	waitForPCS(t, tc, pcsName, "the second hash in status", func(pcs *grovev1beta1.PodCliqueSet) bool {
		return pcs.Status.GangShapeRecreate != nil && pcs.Status.GangShapeRecreate.GangShapeHash == secondHash
	})

	tests.Logger.Info("3. Release replica 0: every replica ends on the new hash, and replica 0 was recreated twice")
	hold.release(t, tc)
	waitForGangShapeCurrent(t, tc, pcsName)
	require.Equal(t, map[int]int{0: 2, 1: 1, 2: 1}, gangShapeDeletions(t, tc, pcsName))
}

// Test_MRM23_StalledRecreateKeepsWaiting verifies that while the recreated replica cannot become available, the
// recreate stays at WaitingForAvailableReplicas and deletes no other replica.
func Test_MRM23_StalledRecreateKeepsWaiting(t *testing.T) {
	const pcsName = "mrm23"
	tc, cleanup := prepare(t, 10, pcsName)
	defer cleanup()
	deploy(t, tc, newRecreatePCS(pcsName, 3, grovev1beta1.RollingRecreateStrategy, 0))
	uidsBefore := workerUIDs(t, tc, pcsName, 3)

	hold, hash := startHeldRecreate(t, tc, pcsName, 2)
	requireStable(t, 30*time.Second, "a stalled recreate must keep waiting", func() error {
		if !isWaitingOnReplica(getPCS(t, tc, pcsName), hash, 0) {
			return fmt.Errorf("the recreate stopped waiting on replica 0")
		}
		uids := workerUIDs(t, tc, pcsName, 3)
		if uids[1] != uidsBefore[1] || uids[2] != uidsBefore[2] {
			return fmt.Errorf("another replica was deleted during a stalled recreate")
		}
		return nil
	})
	require.Equal(t, onceEach(0), gangShapeDeletions(t, tc, pcsName))

	hold.release(t, tc)
	waitForGangShapeCurrent(t, tc, pcsName)
	require.Equal(t, onceEach(0, 1, 2), gangShapeDeletions(t, tc, pcsName))
}
