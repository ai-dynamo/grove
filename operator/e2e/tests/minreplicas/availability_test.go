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
	"slices"
	"testing"
	"time"

	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// deployWithOneCrashLoopingPod deploys a one-replica PodCliqueSet whose worker clique has replicas 2, init 2, the
// given availability and breach tolerance, then makes one of its pods crash-loop. It returns the worker PodClique
// once it has one ready pod.
func deployWithOneCrashLoopingPod(t *testing.T, pcsName string, availability int32, breachTolerance time.Duration) (*testctx.TestContext, *grovev1beta1.PodClique) {
	t.Helper()
	tc, cleanup := prepare(t, 5, pcsName)
	t.Cleanup(cleanup)
	deploy(t, tc, newPCS(pcsName, 1, grovev1beta1.RollingRecreateStrategy, []cliqueSpec{
		{name: workerClique, replicas: 2, init: 2, availability: availability, breachTolerance: breachTolerance},
	}))
	crashLoopStage(pcsName, 0).apply(t, tc)
	deletePods(t, tc, livePodsOfReplica(t, tc, pcsName, 0)[0])
	pclq := waitForPCLQ(t, tc, pclqName(pcsName, 0, workerClique), "one ready pod and one crash-looping pod", func(pclq *grovev1beta1.PodClique) bool {
		return pclq.Status.ReadyReplicas == 1 && pclq.Status.Replicas == 2 && breachCondition(pclq) != nil
	})
	return tc, pclq
}

// Test_MRM12_RaisingAvailabilityBreachesInPlace verifies that raising availability.minReplicas above the available
// replicas makes MinAvailableBreached True at once, without recreating anything, and that gang termination
// follows after the breach tolerance.
func Test_MRM12_RaisingAvailabilityBreachesInPlace(t *testing.T) {
	const pcsName = "mrm12"
	tc, pclq := deployWithOneCrashLoopingPod(t, pcsName, 1, 30*time.Second)
	require.True(t, isNotBreached(pclq), "one ready pod must satisfy availability 1")
	pods := podUIDs(t, tc, pcsName)

	tests.Logger.Info("1. Raise availability.minReplicas from 1 to 2: the PodClique breaches in place")
	requireAccepted(t, tc, pcsName, "availability.minReplicas 2", setCliqueAvailability(workerClique, 2))
	breached := waitForPCLQ(t, tc, pclq.Name, "MinAvailableBreached True", isBreached)
	require.Equal(t, pclq.UID, breached.UID, "raising availability must not recreate the PodClique")
	require.Subset(t, podUIDs(t, tc, pcsName), pods, "raising availability must not recreate pods")
	requireNoGangShapeRecreate(t, getPCS(t, tc, pcsName))

	tests.Logger.Info("2. After the breach tolerance the replica is gang-terminated")
	waitForPCLQ(t, tc, pclq.Name, "replica 0 to be gang-terminated", func(current *grovev1beta1.PodClique) bool {
		return current.UID != pclq.UID
	})
	require.GreaterOrEqual(t, gangTerminations(t, tc, pcsName)[0], 1)
}

// Test_MRM13_LoweringAvailabilityClearsBreach verifies that lowering availability.minReplicas to the available
// replicas clears MinAvailableBreached, so the replica is not gang-terminated.
func Test_MRM13_LoweringAvailabilityClearsBreach(t *testing.T) {
	const pcsName = "mrm13"
	const breachTolerance = 30 * time.Second
	tc, pclq := deployWithOneCrashLoopingPod(t, pcsName, 2, breachTolerance)
	pclq = waitForPCLQ(t, tc, pclq.Name, "MinAvailableBreached True", isBreached)
	breachStart := breachCondition(pclq).LastTransitionTime.Time

	tests.Logger.Info("1. Lower availability.minReplicas from 2 to 1: the breach clears")
	requireAccepted(t, tc, pcsName, "availability.minReplicas 1", setCliqueAvailability(workerClique, 1))
	waitForPCLQ(t, tc, pclq.Name, "MinAvailableBreached False", isNotBreached)

	tests.Logger.Info("2. The replica is not gang-terminated after the breach tolerance")
	requireStable(t, time.Until(breachStart.Add(breachTolerance+15*time.Second)), "the replica must not be gang-terminated", func() error {
		return requireSamePCLQ(t, tc, pclq.Name, pclq.UID)
	})
	require.Zero(t, gangTerminations(t, tc, pcsName)[0])
}

// Test_MRM14_AvailabilityReachesExistingChildren verifies that an availability.minReplicas change is written to
// the existing PodCliques and PodCliqueScalingGroups without recreating pods, including a PodClique whose spec a
// Coherent update preserves until its replica is updated.
func Test_MRM14_AvailabilityReachesExistingChildren(t *testing.T) {
	const pcsName = "mrm14"
	tc, cleanup := prepare(t, 14, pcsName)
	defer cleanup()
	deploy(t, tc, newPCS(pcsName, 2, grovev1beta1.CoherentStrategy,
		[]cliqueSpec{
			{name: workerClique, replicas: 2, init: 2, availability: 1, breachTolerance: time.Hour},
			{name: memberClique, replicas: 1, init: 1},
		},
		scalingGroupSpec{name: scalingGroup, cliqueNames: []string{memberClique}, replicas: 2, init: 2, availability: 1},
	))
	pods := podUIDs(t, tc, pcsName)

	tests.Logger.Info("1. Raise availability.minReplicas of the clique and the scaling group to 2")
	requireAccepted(t, tc, pcsName, "availability.minReplicas 2", setCliqueAvailability(workerClique, 2), setGroupAvailability(scalingGroup, 2))
	for i := range 2 {
		waitForPCLQ(t, tc, pclqName(pcsName, i, workerClique), "availability.minReplicas 2", pclqAvailabilityIs(2))
		waitForPCSG(t, tc, pcsgName(pcsName, i, scalingGroup), "availability.minReplicas 2", func(pcsg *grovev1beta1.PodCliqueScalingGroup) bool {
			return ptr.Deref(componentutils.Availability(pcsg.Spec.Termination).MinReplicas, 0) == 2
		})
	}
	require.ElementsMatch(t, pods, podUIDs(t, tc, pcsName), "an availability change must not recreate pods")

	tests.Logger.Info("2. Start a Coherent update and hold it on its first replica")
	hold := holdStage(pcsName, 0, 1)
	hold.apply(t, tc)
	revision := newRevision()
	requireAccepted(t, tc, pcsName, "pod template change", changePodTemplate(revision, workerClique))
	waitForPCS(t, tc, pcsName, "the Coherent update to start", isUpdateInProgress)

	tests.Logger.Info("3. Lower availability.minReplicas: it also reaches the PodClique whose spec the update preserves")
	requireAccepted(t, tc, pcsName, "availability.minReplicas 1 during the update", setCliqueAvailability(workerClique, 1))
	preserved := 0
	for i := range 2 {
		pclq := waitForPCLQ(t, tc, pclqName(pcsName, i, workerClique), "availability.minReplicas 1", pclqAvailabilityIs(1))
		if podSpecRevision(pclq) != revision {
			preserved++
		}
	}
	require.Positive(t, preserved, "one replica's PodClique must still carry the old pod template")

	tests.Logger.Info("4. Release the update and let it finish")
	hold.release(t, tc)
	waitForRollingUpdateComplete(t, tc, pcsName, revision)
}

// Test_MRM15_BreachToleranceChangedInPlace verifies that a changed breachTolerance applies to a breach that is
// already in progress, whether it is shortened or extended.
func Test_MRM15_BreachToleranceChangedInPlace(t *testing.T) {
	t.Run("shortened", func(t *testing.T) {
		const pcsName = "mrm15-shortened"
		tc, pclq := deployWithOneCrashLoopingPod(t, pcsName, 2, 10*time.Minute)
		pclq = waitForPCLQ(t, tc, pclq.Name, "MinAvailableBreached True", isBreached)
		time.Sleep(20 * time.Second)

		tests.Logger.Info("Shorten breachTolerance from 10m to 30s: the replica is gang-terminated within the new tolerance")
		changedAt := time.Now()
		requireAccepted(t, tc, pcsName, "breachTolerance 30s", setCliqueBreachTolerance(workerClique, 30*time.Second))
		waitForPCLQ(t, tc, pclq.Name, "replica 0 to be gang-terminated", func(current *grovev1beta1.PodClique) bool {
			return current.UID != pclq.UID
		})
		require.Less(t, time.Since(changedAt), 2*time.Minute, "gang termination must follow the shortened tolerance")
	})

	t.Run("extended", func(t *testing.T) {
		const pcsName = "mrm15-extended"
		const breachTolerance = 30 * time.Second
		tc, pclq := deployWithOneCrashLoopingPod(t, pcsName, 2, breachTolerance)
		pclq = waitForPCLQ(t, tc, pclq.Name, "MinAvailableBreached True", isBreached)
		breachStart := breachCondition(pclq).LastTransitionTime.Time

		tests.Logger.Info("Extend breachTolerance from 30s to 10m: the replica is not gang-terminated after 30s")
		requireAccepted(t, tc, pcsName, "breachTolerance 10m", setCliqueBreachTolerance(workerClique, 10*time.Minute))
		requireStable(t, time.Until(breachStart.Add(breachTolerance+20*time.Second)), "the replica must not be gang-terminated", func() error {
			return requireSamePCLQ(t, tc, pclq.Name, pclq.UID)
		})
	})
}

func pclqAvailabilityIs(minReplicas int32) func(*grovev1beta1.PodClique) bool {
	return func(pclq *grovev1beta1.PodClique) bool {
		return ptr.Deref(componentutils.Availability(pclq.Spec.Termination).MinReplicas, 0) == minReplicas
	}
}

// podSpecRevision returns the UPDATE_TRIGGER value of the PodClique's pod template.
func podSpecRevision(pclq *grovev1beta1.PodClique) string {
	env := pclq.Spec.PodSpec.Containers[0].Env
	if i := slices.IndexFunc(env, func(e corev1.EnvVar) bool { return e.Name == updateTriggerEnv }); i >= 0 {
		return env[i].Value
	}
	return ""
}

// requireSamePCLQ returns an error when the PodClique name no longer has uid.
func requireSamePCLQ(t *testing.T, tc *testctx.TestContext, name string, uid types.UID) error {
	if current := getPCLQ(t, tc, name); current.UID != uid {
		return fmt.Errorf("PodClique %s was recreated", name)
	}
	return nil
}
