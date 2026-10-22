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
	"context"
	"fmt"
	"testing"
	"time"

	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgang"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
)

// newCoherentPCS returns a one-replica Coherent PodCliqueSet whose worker clique has replicas 4, init 3 and
// availability 1, so rollingUpdate.minReplicas defaults to 1 and can be raised to 3.
func newCoherentPCS(name string) *grovev1beta1.PodCliqueSet {
	return newPCS(name, 1, grovev1beta1.CoherentStrategy, []cliqueSpec{{name: workerClique, replicas: 4, init: 3, availability: 1}})
}

// startHeldCoherentUpdate changes the worker pod template while the fresh pods of the PodCliqueSet are held
// NotReady, so the Coherent update stays in progress until the returned stage is released.
func startHeldCoherentUpdate(t *testing.T, tc *testctx.TestContext, pcsName string) (podStage, string) {
	t.Helper()
	hold := holdStage(pcsName, 0)
	hold.apply(t, tc)
	revision := newRevision()
	requireAccepted(t, tc, pcsName, "pod template change", changePodTemplate(revision, workerClique))
	waitForPCS(t, tc, pcsName, "the Coherent update to start", isUpdateInProgress)
	return hold, revision
}

// Test_MRM16_RollingUpdateMinReplicasChangedWhileIdle verifies that rollingUpdate.minReplicas can change while no
// update is in progress, that the change restarts nothing, and that the next Coherent update uses the new unit.
func Test_MRM16_RollingUpdateMinReplicasChangedWhileIdle(t *testing.T) {
	const pcsName = "mrm16"
	tc, cleanup := prepare(t, 8, pcsName)
	defer cleanup()
	deploy(t, tc, newCoherentPCS(pcsName))
	podsBefore := podUIDs(t, tc, pcsName)

	tests.Logger.Info("1. Raise rollingUpdate.minReplicas and maxUnavailable from 1 to 2: nothing restarts")
	requireAccepted(t, tc, pcsName, "rollingUpdate.minReplicas 2", setWorkerRollingUpdate(ptr.To[int32](2), ptr.To(intstr.FromInt32(2))))
	requireStable(t, 10*time.Second, "changing rollingUpdate.minReplicas must restart nothing", func() error {
		pcs := getPCS(t, tc, pcsName)
		if pcs.Status.UpdateProgress != nil || pcs.Status.GangShapeRecreate != nil {
			return fmt.Errorf("rollingUpdate.minReplicas change started an update or a recreate")
		}
		if current := podUIDs(t, tc, pcsName); !sameUIDs(current, podsBefore) {
			return fmt.Errorf("rollingUpdate.minReplicas change replaced pods")
		}
		return nil
	})
	requireNoGangShapeRecreate(t, getPCS(t, tc, pcsName))

	tests.Logger.Info("2. The next Coherent update schedules a unit of 2 worker pods and takes down at most 2")
	sampler := pods.NewPodCountSampler()
	sampler.Start(tc.Ctx, sampleInterval, func(ctx context.Context) ([]corev1.Pod, error) {
		return listLivePods(ctx, tc, pcsName)
	}, func(pod *corev1.Pod) bool { return !isPodReady(pod) })
	hold, revision := startHeldCoherentUpdate(t, tc, pcsName)
	waitForPodGroupMinReplicas(t, tc, pcsName, pclqName(pcsName, 0, workerClique), 2)
	hold.release(t, tc)
	waitForRollingUpdateComplete(t, tc, pcsName, revision)
	require.Equal(t, 2, sampler.Stop(), "the update must take down the new unit of 2 pods at a time")
}

// Test_MRM17_RollingUpdateMinReplicasFrozenDuringCoherentUpdate verifies that rollingUpdate.minReplicas cannot
// change while a Coherent update is in progress, and can once it has finished.
func Test_MRM17_RollingUpdateMinReplicasFrozenDuringCoherentUpdate(t *testing.T) {
	const pcsName = "mrm17"
	tc, cleanup := prepare(t, 8, pcsName)
	defer cleanup()
	deploy(t, tc, newCoherentPCS(pcsName))
	unitOf2 := setWorkerRollingUpdate(ptr.To[int32](2), ptr.To(intstr.FromInt32(2)))

	tests.Logger.Info("1. During a Coherent update, a rollingUpdate.minReplicas change is rejected")
	hold, revision := startHeldCoherentUpdate(t, tc, pcsName)
	requireRejected(t, tc, pcsName, "rollingUpdate.minReplicas change during a Coherent update", "must not change while a Coherent update is in progress", unitOf2)

	tests.Logger.Info("2. Once the update has finished, the change is accepted")
	hold.release(t, tc)
	waitForRollingUpdateComplete(t, tc, pcsName, revision)
	requireAccepted(t, tc, pcsName, "rollingUpdate.minReplicas change after the update", unitOf2)
}

// Test_MRM18_StrategySwitchAwayFromCoherent verifies that the update strategy cannot change away from Coherent
// while a Coherent update is in progress, and that switching away and back is accepted once it has finished.
func Test_MRM18_StrategySwitchAwayFromCoherent(t *testing.T) {
	const pcsName = "mrm18"
	tc, cleanup := prepare(t, 8, pcsName)
	defer cleanup()
	deploy(t, tc, newCoherentPCS(pcsName))

	tests.Logger.Info("1. During a Coherent update, switching to RollingRecreate is rejected")
	hold, revision := startHeldCoherentUpdate(t, tc, pcsName)
	requireRejected(t, tc, pcsName, "switching to RollingRecreate during a Coherent update", "must not change away from Coherent while a Coherent update is in progress",
		setStrategy(grovev1beta1.RollingRecreateStrategy))

	tests.Logger.Info("2. Once the update has finished, switching to RollingRecreate and back to Coherent is accepted")
	hold.release(t, tc)
	waitForRollingUpdateComplete(t, tc, pcsName, revision)
	requireAccepted(t, tc, pcsName, "switching to RollingRecreate", setStrategy(grovev1beta1.RollingRecreateStrategy))
	require.Nil(t, cliqueTemplate(getPCS(t, tc, pcsName), workerClique).RollingUpdate.MinReplicas)
	requireAccepted(t, tc, pcsName, "switching back to Coherent", setStrategy(grovev1beta1.CoherentStrategy))
	require.Equal(t, int32(1), *cliqueTemplate(getPCS(t, tc, pcsName), workerClique).RollingUpdate.MinReplicas,
		"switching to Coherent must default rollingUpdate.minReplicas to availability.minReplicas")
}

// waitForPodGroupMinReplicas waits until some PodGang of the PodCliqueSet pcsName has a pod group for the
// PodClique pclqFQN that asks for minReplicas.
func waitForPodGroupMinReplicas(t *testing.T, tc *testctx.TestContext, pcsName, pclqFQN string, minReplicas int32) {
	t.Helper()
	verifier := podgang.NewVerifier(tc.Client, tests.Logger)
	nsName := types.NamespacedName{Namespace: testNamespace, Name: pcsName}
	err := wait.PollUntilContextTimeout(tc.Ctx, pollInterval, recreateTimeout, true, func(ctx context.Context) (bool, error) {
		podGangs, err := verifier.List(ctx, nsName)
		if err != nil {
			return false, err
		}
		for _, pg := range podGangs {
			for _, group := range pg.Spec.PodGroups {
				if group.Name == pclqFQN && group.MinReplicas == minReplicas {
					return true, nil
				}
			}
		}
		return false, nil
	})
	require.NoError(t, err, "no PodGang of %s has a pod group for %s with minReplicas %d", pcsName, pclqFQN, minReplicas)
}

func sameUIDs(a, b []types.UID) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[types.UID]bool, len(a))
	for _, uid := range a {
		seen[uid] = true
	}
	for _, uid := range b {
		if !seen[uid] {
			return false
		}
	}
	return true
}
