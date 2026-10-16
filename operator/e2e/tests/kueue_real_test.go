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

package tests

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/grove/kueueworkload"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgang"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// The Test_KueueReal* scenarios need real kubelets for graceful termination and readiness probes, so they run on a
// real-node cluster (make run-e2e-kueue-real-full) instead of with the KWOK-based Test_Kueue<N>_* scenarios.

// Test_KueueReal1_DrainingPodReplacedWithoutOverlap verifies that Grove creates a deleted pod's replacement only once
// the pod is gone. Kueue counts a draining pod, so an earlier replacement would push the gang past its PodSet count
// and Kueue would finish the Workload as OutOfSync.
func Test_KueueReal1_DrainingPodReplacedWithoutOverlap(t *testing.T) {
	ctx := context.Background()
	expectedPods := 2

	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-real-drain",
			YAMLPath:     "../yaml/kueue-real-drain.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	requireKueueCRD(t, tc)

	Logger.Info("1. Deploy kueue-real-drain and wait until Kueue reports the gang ready")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	podGang := onlyPodGang(ctx, t, tc)
	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	original, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.PodsReady)
	if err != nil {
		t.Fatalf("Kueue Workload never reached PodsReady=True: %v", err)
	}
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != expectedPods {
		t.Fatalf("Expected %d pods, got %d", expectedPods, len(pods.Items))
	}
	victim, survivor := pods.Items[0], pods.Items[1]

	Logger.Info("2. Delete one pod; it turns unready and keeps running for 20s")
	if err := tc.Client.Delete(ctx, &victim); err != nil {
		t.Fatalf("Failed to delete pod %s: %v", victim.Name, err)
	}

	Logger.Info("3. Verify no replacement exists while the deleted pod drains, and Grove removes Kueue's finalizer from it")
	finalizerRemoved := false
	pollUntilTrue(t, tc.Timeout, time.Second, func() (bool, string) {
		current, err := tc.ListPods()
		if err != nil {
			return false, fmt.Sprintf("failed to list pods: %v", err)
		}
		var draining *corev1.Pod
		replacements := 0
		for i := range current.Items {
			switch pod := &current.Items[i]; pod.UID {
			case victim.UID:
				draining = pod
			case survivor.UID:
			default:
				replacements++
			}
		}
		if draining == nil {
			return true, ""
		}
		if replacements > 0 {
			t.Fatalf("Grove created a replacement while pod %s was still draining", victim.Name)
		}
		if !slices.Contains(draining.Finalizers, kueuePodFinalizer) {
			finalizerRemoved = true
		}
		return false, fmt.Sprintf("pod %s is still draining", victim.Name)
	})
	if !finalizerRemoved {
		t.Fatalf("Kueue's finalizer stayed on pod %s for its whole drain", victim.Name)
	}

	Logger.Info("4. Verify the replacement becomes ready next to the pod that was never deleted")
	if err := tc.WaitForPods(expectedPods); err != nil {
		t.Fatalf("Replacement pod did not become ready: %v", err)
	}
	current, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if !slices.ContainsFunc(current.Items, func(pod corev1.Pod) bool { return pod.UID == survivor.UID }) {
		t.Fatalf("Pod %s was replaced too, so the whole gang restarted", survivor.Name)
	}

	Logger.Info("5. Verify Kueue kept the same Workload without finishing or evicting it")
	wl, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.PodsReady)
	if err != nil {
		t.Fatalf("Kueue Workload did not return to PodsReady=True: %v", err)
	}
	if wl.UID != original.UID {
		t.Fatalf("Workload %s/%s was rebuilt (UID %s -> %s), so Kueue had finished it", wl.Namespace, wl.Name, original.UID, wl.UID)
	}
	if kueueworkload.Finished(wl) {
		t.Fatalf("Kueue finished Workload %s/%s", wl.Namespace, wl.Name)
	}
	if n := kueueworkload.EvictionCount(wl, kueuev1beta2.WorkloadEvictedByPodsReadyTimeout, kueuev1beta2.WorkloadWaitForRecovery); n > 0 {
		t.Fatalf("Kueue evicted Workload %s/%s %d time(s) while the pod was replaced", wl.Namespace, wl.Name, n)
	}

	Logger.Info("Test_KueueReal1_DrainingPodReplacedWithoutOverlap completed successfully!")
}

// Test_KueueReal2_UnreadyGangEvictedAndRequeued is Test_Kueue9 on real nodes: a pod that fails its readiness probe
// keeps the gang from PodsReady, so Kueue evicts the Workload after waitForPodsReady.timeout, the pods terminate,
// and Kueue requeues the same Workload.
func Test_KueueReal2_UnreadyGangEvictedAndRequeued(t *testing.T) {
	ctx := context.Background()
	expectedPods := 2

	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-real-notready",
			YAMLPath:     "../yaml/kueue-real-notready.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	requireKueueCRD(t, tc)

	Logger.Info("1. Deploy kueue-real-notready: 'stuck' runs but never passes its readiness probe")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	podGang := onlyPodGang(ctx, t, tc)
	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	admitted, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted)
	if err != nil {
		t.Fatalf("Kueue Workload not Admitted: %v", err)
	}
	originalPods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	Logger.Info("2. Verify Kueue evicts the Workload because the gang never became ready")
	evicted, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.EvictedByPodsReadyTimeout)
	if err != nil {
		t.Fatalf("Kueue Workload was not evicted for PodsReadyTimeout: %v", err)
	}
	if kueueworkload.EvictionCount(evicted, kueuev1beta2.WorkloadEvictedByPodsReadyTimeout, kueuev1beta2.WorkloadWaitForStart) == 0 {
		t.Fatalf("Kueue evicted Workload %s/%s, but not because the gang never became ready: %+v", evicted.Namespace, evicted.Name, evicted.Status.SchedulingStats)
	}

	Logger.Info("3. Verify every original pod terminates and is replaced, and Kueue requeues the same Workload")
	verifyGangRequeued(ctx, t, tc, wlVerifier, podGang, admitted.UID, originalPods)

	Logger.Info("Test_KueueReal2_UnreadyGangEvictedAndRequeued completed successfully!")
}

// Test_KueueReal3_LostReadinessEvictedAfterRecoveryTimeout verifies Kueue's recovery timeout: a gang that was ready
// and then stays unready past waitForPodsReady.recoveryTimeout is evicted, Grove recreates its pods, and Kueue admits
// the same Workload again. The recovery timeout defaults to the 2m timeout that run-e2e-kueue-real-full sets.
func Test_KueueReal3_LostReadinessEvictedAfterRecoveryTimeout(t *testing.T) {
	ctx := context.Background()
	expectedPods := 2

	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-real-recovery",
			YAMLPath:     "../yaml/kueue-real-recovery.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	requireKueueCRD(t, tc)

	Logger.Info("1. Deploy kueue-real-recovery and wait until Kueue reports the gang ready")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	podGang := onlyPodGang(ctx, t, tc)
	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	ready, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.PodsReady)
	if err != nil {
		t.Fatalf("Kueue Workload never reached PodsReady=True: %v", err)
	}
	originalPods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	Logger.Info("2. Verify Kueue evicts the Workload once the pods, unready 60s after starting, exceed the recovery timeout")
	evicted, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.EvictedByPodsReadyTimeout)
	if err != nil {
		t.Fatalf("Kueue Workload was not evicted for PodsReadyTimeout: %v", err)
	}
	if kueueworkload.EvictionCount(evicted, kueuev1beta2.WorkloadEvictedByPodsReadyTimeout, kueuev1beta2.WorkloadWaitForRecovery) == 0 {
		t.Fatalf("Kueue evicted Workload %s/%s, but not for the recovery timeout: %+v", evicted.Namespace, evicted.Name, evicted.Status.SchedulingStats)
	}

	Logger.Info("3. Verify every original pod terminates and is replaced, and Kueue requeues the same Workload")
	verifyGangRequeued(ctx, t, tc, wlVerifier, podGang, ready.UID, originalPods)

	Logger.Info("4. Verify Kueue admits the Workload again")
	readmitted, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, readmittedAfterEviction)
	if err != nil {
		t.Fatalf("Kueue never admitted Workload %s/%s again after evicting it: %v", podGang.Namespace, podGang.Name, err)
	}
	if readmitted.UID != ready.UID {
		t.Fatalf("Workload %s/%s was rebuilt (UID %s -> %s) instead of readmitted", readmitted.Namespace, readmitted.Name, ready.UID, readmitted.UID)
	}

	Logger.Info("Test_KueueReal3_LostReadinessEvictedAfterRecoveryTimeout completed successfully!")
}

// onlyPodGang returns the single PodGang of tc's PodCliqueSet; its Workload has the same name.
func onlyPodGang(ctx context.Context, t *testing.T, tc *testctx.TestContext) groveschedulerv1alpha1.PodGang {
	t.Helper()
	pcs := types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name}
	return waitForPodGangCount(ctx, t, podgang.NewVerifier(tc.Client, Logger), pcs, 1, tc.Timeout, tc.Interval)[0]
}

// readmittedAfterEviction reports whether Kueue admitted wl again: on readmission Kueue resets Evicted to False.
func readmittedAfterEviction(wl *kueuev1beta2.Workload) bool {
	return kueueworkload.Admitted(wl) && !kueueworkload.EvictedByPodsReadyTimeout(wl)
}

// verifyGangRequeued waits until every pod in originalPods is gone and replaced, then checks that Kueue requeued the
// Workload instead of Grove rebuilding it.
func verifyGangRequeued(ctx context.Context, t *testing.T, tc *testctx.TestContext, wlVerifier *kueueworkload.Verifier, podGang groveschedulerv1alpha1.PodGang, workloadUID types.UID, originalPods *corev1.PodList) {
	t.Helper()
	pollUntilTrue(t, tc.Timeout, tc.Interval, func() (bool, string) {
		current, err := tc.ListPods()
		if err != nil {
			return false, fmt.Sprintf("failed to list pods: %v", err)
		}
		for i := range current.Items {
			pod := &current.Items[i]
			if slices.ContainsFunc(originalPods.Items, func(original corev1.Pod) bool { return original.UID == pod.UID }) {
				return false, fmt.Sprintf("pod %s from the original admission still exists", pod.Name)
			}
		}
		if len(current.Items) == 0 {
			return false, "no replacement pods created yet"
		}
		return true, ""
	})
	wl, err := wlVerifier.Get(ctx, podGang.Namespace, podGang.Name)
	if err != nil {
		t.Fatalf("Failed to get Kueue Workload: %v", err)
	}
	if wl.UID != workloadUID {
		t.Fatalf("Workload %s/%s was rebuilt (UID %s -> %s) instead of requeued", wl.Namespace, wl.Name, workloadUID, wl.UID)
	}
}
