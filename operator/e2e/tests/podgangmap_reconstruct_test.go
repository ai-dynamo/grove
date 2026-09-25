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

package tests

import (
	"context"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgang"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgangmap"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// pgmReconstructTimeout bounds the wait for a deleted PodGangMap to be reconstructed. Reconstruction
// runs on the next reconcile and is near-instant, so a long wait here signals a real defect.
const pgmReconstructTimeout = 1 * time.Minute

// Test_PGMR1_ReconstructScaledPCSGAfterPodGangMapDelete verifies that a PodGangMap deleted after a
// PodCliqueScalingGroup was scaled beyond its template is reconstructed from the live scaled replicas,
// reuses the scale-out epoch so the scaled-out PodGang keeps its name, and leaves running pods intact.
// Scenario PGMR-1:
// 1. Initialize a 14-node Grove cluster
// 2. Deploy workload WL1 (standalone pc-a=2, PCSG sg-x=2 over pc-b+pc-c), verify 10 pods
// 3. Scale sg-x from 2 to 3, verify 14 pods, and capture the scale-out entry epoch
// 4. Delete the PodGangMap for replica 0
// 5. Verify the recreated PodGangMap has anchor sg-x indices [0,1], scale-out sg-x index [2] with the
//    same epoch, and the standalone pc-a count 2
// 6. Verify the scaled-out PodGang was not recreated (stable UID) and all 14 pods remain running
func Test_PGMR1_ReconstructScaledPCSGAfterPodGangMapDelete(t *testing.T) {
	ctx := t.Context()

	Logger.Info("1. Initialize a 14-node Grove cluster")
	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	verifier := podgangmap.NewVerifier(tc.Client, Logger)
	pcsNsName := types.NamespacedName{Namespace: tc.Namespace, Name: "workload1"}

	Logger.Info("3. Scale sg-x from 2 to 3, verify 14 pods, and capture the scale-out entry epoch")
	// Per PCSG replica sg-x holds pc-b(1)+pc-c(3)=4 pods. With pc-a(2) the totals are 2+4*2=10 at 2
	// replicas and 2+4*3=14 at 3 replicas.
	tc.ScalePCSGAcrossAllReplicasAndWait("workload1", "sg-x", 1, 3, 14, 0)

	if err := verifier.Verify(ctx, pcsNsName, 0,
		podgangmap.AnchorPCSGReplicaIndicesCheckFn("sg-x", []int32{0, 1}),
		podgangmap.ScaleOutPCSGReplicaIndicesCheckFn("sg-x", []int32{2}),
	); err != nil {
		t.Fatalf("PodGangMap not in expected state after scale-out: %v", err)
	}
	scaleOutEpoch := scaleOutEpochOf(t, ctx, verifier, pcsNsName)

	// Capture the scaled-out PodGang's UID so a delete-and-recreate can be detected after reconstruction.
	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	scaleOutPodGangName := apicommon.GenerateNonAnchorPodGangName(
		apicommon.ResourceNameReplica{Name: "workload1", Replica: 0}, scaleOutEpoch, "sg-x", 2)
	scaleOutPodGang, err := pgVerifier.Get(ctx, tc.Namespace, scaleOutPodGangName)
	if err != nil {
		t.Fatalf("Failed to get scaled-out PodGang %s: %v", scaleOutPodGangName, err)
	}
	uidBefore := scaleOutPodGang.UID

	Logger.Info("4. Delete the PodGangMap for replica 0")
	deletePodGangMap(t, tc, "workload1-0")

	Logger.Info("5. Verify the recreated PodGangMap reflects the live scaled replicas and reuses the epoch")
	if err := podgangmap.WaitUntilVerified(ctx, verifier, pcsNsName, 0, pgmReconstructTimeout, tc.Interval,
		podgangmap.AnchorPCSGReplicaIndicesCheckFn("sg-x", []int32{0, 1}),
		podgangmap.ScaleOutPCSGReplicaIndicesCheckFn("sg-x", []int32{2}),
		podgangmap.ScaleOutEpochCheckFn(scaleOutEpoch),
		podgangmap.AnchorStandalonePodCliqueCountCheckFn("pc-a", 2),
	); err != nil {
		t.Fatalf("%v", err)
	}

	Logger.Info("6. Verify the scaled-out PodGang was not churned and all 14 pods remain running")
	// Reconstruction reflects the scaled counts up front, so the scaled-out PodGang is never dropped
	// and recreated. A stable UID proves it is the same object.
	if err := pgVerifier.VerifyByName(ctx, tc.Namespace, scaleOutPodGangName, podgang.SameUIDCheckFn(uidBefore)); err != nil {
		t.Fatalf("scaled-out PodGang %s churned: %v", scaleOutPodGangName, err)
	}
	if err := tc.WaitForRunningPods(14); err != nil {
		t.Fatalf("Pods not all running after PodGangMap reconstruction: %v", err)
	}

	Logger.Info("🎉 PGMR-1 completed successfully!")
}

// Test_PGMR2_ReconstructScaledStandalonePCLQAfterPodGangMapDelete verifies that a PodGangMap deleted
// after a standalone PodClique was scaled beyond its template is reconstructed from the live scaled
// replicas, not the stale template count.
// Scenario PGMR-2:
// 1. Initialize a 14-node Grove cluster
// 2. Deploy workload WL1, verify 10 pods
// 3. Scale standalone pc-a from 2 to 4, verify 12 pods, and assert the anchor count is 4
// 4. Delete the PodGangMap for replica 0
// 5. Verify the recreated PodGangMap has the anchor pc-a count 4
// 6. Verify all 12 pods remain running
func Test_PGMR2_ReconstructScaledStandalonePCLQAfterPodGangMapDelete(t *testing.T) {
	ctx := t.Context()

	Logger.Info("1. Initialize a 14-node Grove cluster")
	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	verifier := podgangmap.NewVerifier(tc.Client, Logger)
	pcsNsName := types.NamespacedName{Namespace: tc.Namespace, Name: "workload1"}

	Logger.Info("3. Scale standalone pc-a from 2 to 4, verify 12 pods")
	// pc-a is standalone. Scaling it 2 to 4 adds 2 pods to the 10-pod baseline.
	tc.ScalePodCliqueAndWait("workload1-0-pc-a", 4, 12, 0)

	if err := verifier.Verify(ctx, pcsNsName, 0,
		podgangmap.AnchorStandalonePodCliqueCountCheckFn("pc-a", 4),
	); err != nil {
		t.Fatalf("PodGangMap not in expected state after scale-out: %v", err)
	}

	Logger.Info("4. Delete the PodGangMap for replica 0")
	deletePodGangMap(t, tc, "workload1-0")

	Logger.Info("5. Verify the recreated PodGangMap has the anchor pc-a count 4")
	if err := podgangmap.WaitUntilVerified(ctx, verifier, pcsNsName, 0, pgmReconstructTimeout, tc.Interval,
		podgangmap.AnchorStandalonePodCliqueCountCheckFn("pc-a", 4),
	); err != nil {
		t.Fatalf("%v", err)
	}

	Logger.Info("6. Verify all 12 pods remain running")
	if err := tc.WaitForRunningPods(12); err != nil {
		t.Fatalf("Pods not all running after PodGangMap reconstruction: %v", err)
	}

	Logger.Info("🎉 PGMR-2 completed successfully!")
}

// Test_PGMR3_ReconstructFreshBootstrapAfterPodGangMapDelete verifies that a PodGangMap deleted with no
// prior scaling is reconstructed to the same template shape, reusing the anchor epoch from the live
// anchor PodGang so pods are not stranded.
// Scenario PGMR-3:
// 1. Initialize a 14-node Grove cluster
// 2. Deploy workload WL1, verify 10 pods
// 3. Capture the anchor epoch
// 4. Delete the PodGangMap for replica 0
// 5. Verify the recreated PodGangMap has anchor sg-x indices [0,1], pc-a count 2, an empty scale-out,
//    and the anchor epoch is reused
// 6. Verify all 10 pods remain running
func Test_PGMR3_ReconstructFreshBootstrapAfterPodGangMapDelete(t *testing.T) {
	ctx := t.Context()

	Logger.Info("1. Initialize a 14-node Grove cluster")
	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	verifier := podgangmap.NewVerifier(tc.Client, Logger)
	pcsNsName := types.NamespacedName{Namespace: tc.Namespace, Name: "workload1"}

	Logger.Info("3. Capture the anchor epoch")
	anchorEpoch := anchorEpochOf(t, ctx, verifier, pcsNsName)

	Logger.Info("4. Delete the PodGangMap for replica 0")
	deletePodGangMap(t, tc, "workload1-0")

	Logger.Info("5. Verify the recreated PodGangMap matches the template shape and reuses the anchor epoch")
	if err := podgangmap.WaitUntilVerified(ctx, verifier, pcsNsName, 0, pgmReconstructTimeout, tc.Interval,
		podgangmap.AnchorPCSGReplicaIndicesCheckFn("sg-x", []int32{0, 1}),
		podgangmap.AnchorStandalonePodCliqueCountCheckFn("pc-a", 2),
		podgangmap.ScaleOutPCSGReplicaIndicesCheckFn("sg-x", nil),
		podgangmap.AnchorEpochCheckFn(anchorEpoch),
	); err != nil {
		t.Fatalf("%v", err)
	}

	Logger.Info("6. Verify all 10 pods remain running")
	if err := tc.WaitForRunningPods(10); err != nil {
		t.Fatalf("Pods not all running after PodGangMap reconstruction: %v", err)
	}

	Logger.Info("🎉 PGMR-3 completed successfully!")
}

// Test_PGMR4_ReconstructAfterScaleInThenPodGangMapDelete verifies that after a PodCliqueScalingGroup is
// scaled out and then scaled back in, a deleted PodGangMap is reconstructed without the drained index.
// Scenario PGMR-4:
// 1. Initialize a 14-node Grove cluster
// 2. Deploy workload WL1, verify 10 pods
// 3. Scale sg-x 2 to 3 (14 pods), then 3 to 2 (10 pods), and assert the scale-out entry drained
// 4. Delete the PodGangMap for replica 0
// 5. Verify the recreated PodGangMap has anchor sg-x indices [0,1] and an empty scale-out
// 6. Verify all 10 pods remain running
func Test_PGMR4_ReconstructAfterScaleInThenPodGangMapDelete(t *testing.T) {
	ctx := t.Context()

	Logger.Info("1. Initialize a 14-node Grove cluster")
	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	verifier := podgangmap.NewVerifier(tc.Client, Logger)
	pcsNsName := types.NamespacedName{Namespace: tc.Namespace, Name: "workload1"}

	Logger.Info("3. Scale sg-x 2 to 3 then 3 to 2, and assert the scale-out entry drained")
	tc.ScalePCSGAcrossAllReplicasAndWait("workload1", "sg-x", 1, 3, 14, 0)
	tc.ScalePCSGAcrossAllReplicasAndWait("workload1", "sg-x", 1, 2, 10, 0)

	if err := verifier.Verify(ctx, pcsNsName, 0,
		podgangmap.AnchorPCSGReplicaIndicesCheckFn("sg-x", []int32{0, 1}),
		podgangmap.ScaleOutPCSGReplicaIndicesCheckFn("sg-x", nil),
	); err != nil {
		t.Fatalf("PodGangMap not in expected state after scale-in: %v", err)
	}

	Logger.Info("4. Delete the PodGangMap for replica 0")
	deletePodGangMap(t, tc, "workload1-0")

	Logger.Info("5. Verify the recreated PodGangMap has anchor sg-x indices [0,1] and an empty scale-out")
	if err := podgangmap.WaitUntilVerified(ctx, verifier, pcsNsName, 0, pgmReconstructTimeout, tc.Interval,
		podgangmap.AnchorPCSGReplicaIndicesCheckFn("sg-x", []int32{0, 1}),
		podgangmap.ScaleOutPCSGReplicaIndicesCheckFn("sg-x", nil),
	); err != nil {
		t.Fatalf("%v", err)
	}

	Logger.Info("6. Verify all 10 pods remain running")
	if err := tc.WaitForRunningPods(10); err != nil {
		t.Fatalf("Pods not all running after PodGangMap reconstruction: %v", err)
	}

	Logger.Info("🎉 PGMR-4 completed successfully!")
}

// Test_PGMR5_RecoverAfterScalingPCSGBelowMinAvailable verifies that scaling a PodCliqueScalingGroup
// below MinAvailable, which drains the anchor entry to empty on an all-PCSG PodCliqueSet, does not
// wedge the PodGangMap. The anchor and ScaleOut entries vanish together while the group is at zero, and
// both return when the group is scaled back up, so the worker pods schedule again. This is the
// regression behind an all-PCSG workload that could not scale back from zero.
// Scenario PGMR-5:
// 1. Initialize a 4-node Grove cluster
// 2. Deploy workload WL-PCSG-ONLY (one PCSG worker=1 over worker-ldr+worker-wkr), verify 2 pods
// 3. Verify the PodGangMap starts with an anchor holding worker index 0 and an empty ScaleOut entry
// 4. Scale the worker group to 0, below MinAvailable, and verify both entries vanish
// 5. Scale the worker group back to 1, and verify the anchor holding worker index 0 and the empty
//    ScaleOut entry are both present again
// 6. Verify both worker pods are running again
func Test_PGMR5_RecoverAfterScalingPCSGBelowMinAvailable(t *testing.T) {
	ctx := t.Context()

	Logger.Info("1. Initialize a 4-node Grove cluster")
	Logger.Info("2. Deploy workload WL-PCSG-ONLY, and verify 2 newly created pods")
	tc, cleanup := testctx.PrepareTest(ctx, t, 4,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload-pcsg-only",
			YAMLPath:     "../yaml/workload-pcsg-only.yaml",
			Namespace:    "default",
			ExpectedPods: 2,
		}),
	)
	defer cleanup()

	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	verifier := podgangmap.NewVerifier(tc.Client, Logger)
	pcsNsName := types.NamespacedName{Namespace: tc.Namespace, Name: "workload-pcsg-only"}

	Logger.Info("3. Verify the PodGangMap starts with an anchor holding worker index 0 and an empty ScaleOut entry")
	if err := verifier.Verify(ctx, pcsNsName, 0,
		podgangmap.AnchorPCSGReplicaIndicesCheckFn("worker", []int32{0}),
		podgangmap.ScaleOutPCSGReplicaIndicesCheckFn("worker", nil),
	); err != nil {
		t.Fatalf("PodGangMap not in expected state at baseline: %v", err)
	}

	Logger.Info("4. Scale the worker group to 0, below MinAvailable, and verify both entries vanish")
	tc.ScalePCSGAcrossAllReplicasAndWait("workload-pcsg-only", "worker", 1, 0, 0, 0)
	if err := podgangmap.WaitUntilVerified(ctx, verifier, pcsNsName, 0, pgmReconstructTimeout, tc.Interval,
		podgangmap.NoEntriesCheckFn(),
	); err != nil {
		t.Fatalf("%v", err)
	}

	Logger.Info("5. Scale the worker group back to 1, and verify the anchor and empty ScaleOut entry are both present")
	tc.ScalePCSGAcrossAllReplicasAndWait("workload-pcsg-only", "worker", 1, 1, 2, 0)
	if err := podgangmap.WaitUntilVerified(ctx, verifier, pcsNsName, 0, pgmReconstructTimeout, tc.Interval,
		podgangmap.AnchorPCSGReplicaIndicesCheckFn("worker", []int32{0}),
		podgangmap.ScaleOutPCSGReplicaIndicesCheckFn("worker", nil),
	); err != nil {
		t.Fatalf("%v", err)
	}

	Logger.Info("6. Verify the anchor PodGang eventually records LastScheduled and LastReady")
	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	if err := podgang.WaitUntilVerified(ctx, pgVerifier, pcsNsName, pgmReconstructTimeout, tc.Interval,
		podgang.LastScheduledSetCheckFn(true),
		podgang.LastReadySetCheckFn(true),
	); err != nil {
		t.Fatalf("anchor PodGang did not record LastScheduled and LastReady: %v", err)
	}

	Logger.Info("7. Verify both worker pods are running again")
	if err := tc.WaitForRunningPods(2); err != nil {
		t.Fatalf("Pods not all running after scaling the group back up: %v", err)
	}

	Logger.Info("🎉 PGMR-5 completed successfully!")
}

// Test_PGMR6_RecoverAfterScalingStandalonePCLQToZero verifies that scaling the only standalone
// PodClique of a PodCliqueSet to zero drains the anchor entry to empty and, because there is no
// PodCliqueScalingGroup, leaves the PodGangMap with no entries at all, without wedging it. The
// PodClique's pods are deleted as it scales to zero, and scaling it back up rebuilds the anchor and
// recreates the pod.
// Scenario PGMR-6:
// 1. Initialize a 4-node Grove cluster
// 2. Deploy workload WL-PCLQ-ONLY (one standalone PodClique solo=1), verify 1 pod
// 3. Verify the PodGangMap starts with an anchor holding the solo PodClique count 1
// 4. Scale solo to 0, and verify the PodGangMap holds no entries and no pods remain
// 5. Scale solo back to 1, and verify the anchor holds the solo count 1 again
// 6. Verify the anchor PodGang eventually records LastScheduled and LastReady
// 7. Verify the pod is running again
func Test_PGMR6_RecoverAfterScalingStandalonePCLQToZero(t *testing.T) {
	ctx := t.Context()

	Logger.Info("1. Initialize a 4-node Grove cluster")
	Logger.Info("2. Deploy workload WL-PCLQ-ONLY, and verify 1 newly created pod")
	tc, cleanup := testctx.PrepareTest(ctx, t, 4,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload-pclq-only",
			YAMLPath:     "../yaml/workload-pclq-only.yaml",
			Namespace:    "default",
			ExpectedPods: 1,
		}),
	)
	defer cleanup()

	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	verifier := podgangmap.NewVerifier(tc.Client, Logger)
	pcsNsName := types.NamespacedName{Namespace: tc.Namespace, Name: "workload-pclq-only"}

	Logger.Info("3. Verify the PodGangMap starts with an anchor holding the solo PodClique count 1")
	if err := verifier.Verify(ctx, pcsNsName, 0,
		podgangmap.AnchorStandalonePodCliqueCountCheckFn("solo", 1),
	); err != nil {
		t.Fatalf("PodGangMap not in expected state at baseline: %v", err)
	}

	Logger.Info("4. Scale solo to 0, and verify the PodGangMap holds no entries and no pods remain")
	tc.ScalePodCliqueAndWait("workload-pclq-only-0-solo", 0, 0, 0)
	if err := podgangmap.WaitUntilVerified(ctx, verifier, pcsNsName, 0, pgmReconstructTimeout, tc.Interval,
		podgangmap.NoEntriesCheckFn(),
	); err != nil {
		t.Fatalf("%v", err)
	}

	Logger.Info("5. Scale solo back to 1, and verify the anchor holds the solo count 1 again")
	tc.ScalePodCliqueAndWait("workload-pclq-only-0-solo", 1, 1, 0)
	if err := podgangmap.WaitUntilVerified(ctx, verifier, pcsNsName, 0, pgmReconstructTimeout, tc.Interval,
		podgangmap.AnchorStandalonePodCliqueCountCheckFn("solo", 1),
	); err != nil {
		t.Fatalf("%v", err)
	}

	Logger.Info("6. Verify the anchor PodGang eventually records LastScheduled and LastReady")
	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	if err := podgang.WaitUntilVerified(ctx, pgVerifier, pcsNsName, pgmReconstructTimeout, tc.Interval,
		podgang.LastScheduledSetCheckFn(true),
		podgang.LastReadySetCheckFn(true),
	); err != nil {
		t.Fatalf("anchor PodGang did not record LastScheduled and LastReady: %v", err)
	}

	Logger.Info("7. Verify the pod is running again")
	if err := tc.WaitForRunningPods(1); err != nil {
		t.Fatalf("Pod not running after scaling the PodClique back up: %v", err)
	}

	Logger.Info("🎉 PGMR-6 completed successfully!")
}

// deletePodGangMap deletes the named PodGangMap, failing the test on error.
func deletePodGangMap(t *testing.T, tc *testctx.TestContext, name string) {
	t.Helper()
	pgm := &grovev1alpha1.PodGangMap{ObjectMeta: metav1.ObjectMeta{Namespace: tc.Namespace, Name: name}}
	if err := tc.Client.Delete(tc.Ctx, pgm); err != nil {
		t.Fatalf("Failed to delete PodGangMap %s: %v", name, err)
	}
}

// scaleOutEpochOf returns the epoch of replica 0's single ScaleOut entry.
func scaleOutEpochOf(t *testing.T, ctx context.Context, v *podgangmap.Verifier, pcsNsName types.NamespacedName) string {
	t.Helper()
	return epochOfRole(t, ctx, v, pcsNsName, grovev1alpha1.PodGangEntryRoleScaleOut)
}

// anchorEpochOf returns the epoch of replica 0's single anchor entry.
func anchorEpochOf(t *testing.T, ctx context.Context, v *podgangmap.Verifier, pcsNsName types.NamespacedName) string {
	t.Helper()
	return epochOfRole(t, ctx, v, pcsNsName, grovev1alpha1.PodGangEntryRoleAnchor)
}

// epochOfRole returns the epoch of replica 0's single entry with the given role.
func epochOfRole(t *testing.T, ctx context.Context, v *podgangmap.Verifier, pcsNsName types.NamespacedName, role grovev1alpha1.PodGangEntryRole) string {
	t.Helper()
	pgm, err := v.Get(ctx, pcsNsName, 0)
	if err != nil {
		t.Fatalf("Failed to get PodGangMap: %v", err)
	}
	var epochs []string
	for i := range pgm.Spec.Entries {
		if pgm.Spec.Entries[i].Role == role {
			epochs = append(epochs, pgm.Spec.Entries[i].Epoch)
		}
	}
	if len(epochs) != 1 {
		t.Fatalf("expected exactly one %s entry, found %d", role, len(epochs))
	}
	return epochs[0]
}
