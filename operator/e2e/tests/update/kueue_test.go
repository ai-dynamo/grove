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

package update

import (
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/e2e/grove/kueueworkload"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgang"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// The Test_KueueUpdate* scenarios run on a cluster with Kueue installed (make run-e2e-kueue-full
// TEST_PATTERN='^Test_KueueUpdate') and use a PodCliqueSet with a standalone frontend (1 replica) and two
// PodCliqueScalingGroups, sg-prefill and sg-decode (2 replicas each, minAvailable 2), so every Pod starts in the
// base PodGang.

const (
	// frontend 1 + sg-prefill 2 x prefill 1 + sg-decode 2 x decode 1.
	kueueUpdateExpectedPods = 5
	// prebuiltWorkloadNameLabel names the Kueue Workload a Pod belongs to (internal/scheduler/kueue/backend.go).
	prebuiltWorkloadNameLabel = "kueue.x-k8s.io/prebuilt-workload-name"
)

// kueueUpdateFixture is the PodCliqueSet a Kueue update scenario deploys.
type kueueUpdateFixture struct {
	name        string
	yamlPath    string
	workerNodes int
}

// Test_KueueUpdate1_RollingRecreateKeepsWorkload verifies that a RollingRecreate update of every component
// replaces the Pods within the same PodGang, so Kueue keeps the same Workload ready and never evicts the gang.
func Test_KueueUpdate1_RollingRecreateKeepsWorkload(t *testing.T) {
	testKueueRollingRecreateKeepsWorkload(t, kueueUpdateFixture{name: "kueue-update-rr", yamlPath: "../../yaml/kueue-update-rr.yaml", workerNodes: 5})
}

func testKueueRollingRecreateKeepsWorkload(t *testing.T, fixture kueueUpdateFixture) {
	tests.Logger.Infof("1. Deploy %s and wait until Kueue reports the gang ready", fixture.name)
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: fixture.name,
		workloadYAML: fixture.yamlPath,
		workerNodes:  fixture.workerNodes,
		expectedPods: kueueUpdateExpectedPods,
	})
	defer cleanup()
	wlVerifier := kueueworkload.NewVerifier(tc.Client, tests.Logger)
	podGang := waitForSinglePodGang(t, tc)
	original := waitForGangWorkload(t, tc, wlVerifier, podGang.Name)

	tests.Logger.Info("2. Update frontend, prefill, and decode together and wait for the update to complete")
	updateAllKueueComponents(t, tc)

	tests.Logger.Info("3. Verify every Pod was replaced within the same PodGang")
	if got := waitForSinglePodGang(t, tc); got.UID != podGang.UID {
		t.Fatalf("PodGang %s was replaced by %s during the update", podGang.Name, got.Name)
	}
	assertPodsUpdatedInWorkload(t, tc, podGang.Name)

	tests.Logger.Info("4. Verify Kueue kept the same Workload ready without evicting it")
	wl := waitForGangWorkload(t, tc, wlVerifier, podGang.Name)
	if wl.UID != original.UID {
		t.Fatalf("Workload %s was rebuilt (UID %s -> %s)", wl.Name, original.UID, wl.UID)
	}
	if kueueworkload.Finished(wl) {
		t.Fatalf("Kueue finished Workload %s during the update", wl.Name)
	}
	if n := kueueworkload.EvictionCount(wl, kueuev1beta2.WorkloadEvictedByPodsReadyTimeout, kueuev1beta2.WorkloadWaitForRecovery); n > 0 {
		t.Fatalf("Kueue evicted Workload %s %d time(s) during the update", wl.Name, n)
	}
}

// Test_KueueUpdate2_CoherentReplacesWorkload verifies that a Coherent update of every component moves all Pods to a
// new anchor PodGang, whose new Workload Kueue admits while it deletes the old one. The update changes every
// component at once because a narrower update would drain a PodGroup off the old PodGang while its Workload still
// counts it.
func Test_KueueUpdate2_CoherentReplacesWorkload(t *testing.T) {
	testKueueCoherentReplacesWorkload(t, kueueUpdateFixture{name: "kueue-update-coherent", yamlPath: "../../yaml/kueue-update-coherent.yaml", workerNodes: 5})
}

func testKueueCoherentReplacesWorkload(t *testing.T, fixture kueueUpdateFixture) {
	tests.Logger.Infof("1. Deploy %s and wait until Kueue reports the gang ready", fixture.name)
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: fixture.name,
		workloadYAML: fixture.yamlPath,
		workerNodes:  fixture.workerNodes,
		expectedPods: kueueUpdateExpectedPods,
	})
	defer cleanup()
	wlVerifier := kueueworkload.NewVerifier(tc.Client, tests.Logger)
	oldPodGang := waitForSinglePodGang(t, tc)
	waitForGangWorkload(t, tc, wlVerifier, oldPodGang.Name)

	tests.Logger.Info("2. Update frontend, prefill, and decode together and wait for the update to complete")
	updateAllKueueComponents(t, tc)
	assertPodGangMapSingleGeneration(t, tc)

	tests.Logger.Info("3. Verify every Pod moved to a new PodGang")
	newPodGang := waitForSinglePodGang(t, tc)
	if newPodGang.Name == oldPodGang.Name {
		t.Fatalf("PodGang %s was kept, want a new anchor PodGang", oldPodGang.Name)
	}
	assertPodsUpdatedInWorkload(t, tc, newPodGang.Name)

	tests.Logger.Info("4. Verify Kueue admitted a Workload for the new PodGang and deleted the old one")
	newWl := waitForGangWorkload(t, tc, wlVerifier, newPodGang.Name)
	if n := kueueworkload.EvictionCount(newWl, kueuev1beta2.WorkloadEvictedByPodsReadyTimeout, kueuev1beta2.WorkloadWaitForStart); n > 0 {
		t.Fatalf("Kueue evicted Workload %s %d time(s) before it became ready", newWl.Name, n)
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := wlVerifier.Get(tc.Ctx, tc.Namespace, oldPodGang.Name)
		assert.True(c, apierrors.IsNotFound(err), "Workload %s still exists: %v", oldPodGang.Name, err)
	}, tc.Timeout, tc.Interval)
}

// updateAllKueueComponents updates frontend, prefill, and decode in one PodCliqueSet change and waits until the
// update completes and every Pod is ready.
func updateAllKueueComponents(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	if err := triggerPodCliqueUpdate(tc, "frontend", "prefill", "decode"); err != nil {
		t.Fatalf("Failed to trigger the update: %v", err)
	}
	if err := waitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("Update did not complete: %v", err)
	}
	assertUpdateInProgressCleared(tc)
	assertGenerationHashConverged(tc)
	if err := tc.WaitForPods(kueueUpdateExpectedPods); err != nil {
		t.Fatalf("Pods did not become ready after the update: %v", err)
	}
}

// waitForSinglePodGang waits until the PodCliqueSet has exactly one PodGang and returns it.
func waitForSinglePodGang(t *testing.T, tc *testctx.TestContext) groveschedulerv1alpha1.PodGang {
	t.Helper()
	pgVerifier := podgang.NewVerifier(tc.Client, tests.Logger)
	var podGangs []groveschedulerv1alpha1.PodGang
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		podGangs, err = pgVerifier.List(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name})
		require.NoError(c, err)
		assert.Len(c, podGangs, 1)
	}, tc.Timeout, tc.Interval)
	return podGangs[0]
}

// waitForGangWorkload waits until the Workload of the named PodGang is PodsReady, and verifies it has one
// single-Pod podSet per PodClique.
func waitForGangWorkload(t *testing.T, tc *testctx.TestContext, wlVerifier *kueueworkload.Verifier, podGangName string) *kueuev1beta2.Workload {
	t.Helper()
	wl, err := wlVerifier.WaitUntilVerified(tc.Ctx, tc.Namespace, podGangName, tc.Timeout, tc.Interval, kueueworkload.PodsReady)
	if err != nil {
		t.Fatalf("Kueue Workload %s never reached PodsReady=True: %v", podGangName, err)
	}
	wantPodSets := []string{standaloneCliqueFQN(tc, "frontend")}
	for _, sg := range []struct{ pcsgName, cliqueName string }{{"sg-prefill", "prefill"}, {"sg-decode", "decode"}} {
		for replica := range 2 {
			wantPodSets = append(wantPodSets, common.GeneratePodCliqueName(common.ResourceNameReplica{Name: pcsgFQN(tc, sg.pcsgName), Replica: replica}, sg.cliqueName))
		}
	}
	var gotPodSets []string
	for _, podSet := range wl.Spec.PodSets {
		gotPodSets = append(gotPodSets, string(podSet.Name))
		assert.Equalf(t, int32(1), podSet.Count, "podSet %s count", podSet.Name)
	}
	assert.ElementsMatch(t, wantPodSets, gotPodSets, "Workload %s podSets", wl.Name)
	return wl
}

// assertPodsUpdatedInWorkload verifies that every Pod runs the updated spec and belongs to the named Workload.
func assertPodsUpdatedInWorkload(t *testing.T, tc *testctx.TestContext, workloadName string) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := tc.ListPods()
		require.NoError(c, err)
		assert.Len(c, pods.Items, kueueUpdateExpectedPods)
		for _, pod := range pods.Items {
			assert.NoError(c, verifyPodHasUpdatedSpec(tc, pod.Name))
			assert.Equalf(c, workloadName, pod.Labels[prebuiltWorkloadNameLabel], "Pod %s Workload", pod.Name)
		}
	}, tc.Timeout, tc.Interval)
}
