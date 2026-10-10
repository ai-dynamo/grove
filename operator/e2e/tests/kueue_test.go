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
	"strings"
	"testing"
	"time"

	nameutils "github.com/ai-dynamo/grove/operator/api/common"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/kueueworkload"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgang"
	"github.com/ai-dynamo/grove/operator/e2e/grove/topology"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	// queueNameLabel is the PodCliqueSet label the kueue scheduler backend reads to resolve the
	// target Kueue LocalQueue. Mirrors (but is not imported from, since it is unexported there)
	// queueNameLabel in internal/scheduler/kueue/backend.go.
	queueNameLabel = "kueue.x-k8s.io/queue-name"

	// kueueAdmissionSchedulingGate is the scheduling gate Kueue's pod-group reconciler adds to every
	// pod it manages, removed only once the owning Workload is admitted. Verified against
	// sigs.k8s.io/kueue@v0.17.8/pkg/controller/jobs/pod/constants.SchedulingGateName.
	kueueAdmissionSchedulingGate = "kueue.x-k8s.io/admission"

	// kueueWorkloadCRDName is the Kueue Workload CustomResourceDefinition name, used by
	// requireKueueCRD as a defense-in-depth opt-in gate.
	kueueWorkloadCRDName = "workloads.kueue.x-k8s.io"
)

// requireKueueCRD skips the test when the Kueue Workload CRD is not installed. This is
// defense-in-depth only: the primary opt-in gate is the run-e2e-kueue-full Makefile target and the
// "kueue" e2e CI matrix entry, which only ever run Test_Kueue* against a cluster brought up with
// E2E_SCHEDULER__KUEUE__ENABLED=true (see operator/hack/infra_manager).
func requireKueueCRD(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	apiExtClient, err := apiextensionsclientset.NewForConfig(tc.Client.RestConfig)
	if err != nil {
		t.Fatalf("Failed to create API extensions client: %v", err)
	}
	if _, err := apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Get(tc.Ctx, kueueWorkloadCRDName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			t.Skipf("Kueue Workload CRD %q not found; cluster was not brought up with Kueue installed", kueueWorkloadCRDName)
		}
		t.Fatalf("Failed to check for Kueue Workload CRD %q: %v", kueueWorkloadCRDName, err)
	}
}

// deleteKueueQueueResources deletes the ClusterQueue and LocalQueue named queueName (both share the
// same name across this suite's dedicated-queue fixtures, e.g. kueue-queues-partial.yaml and
// kueue-queues-backlog.yaml). It deliberately leaves the shared "default-flavor" ResourceFlavor alone
// -- kueue-queues.yaml (applied once at cluster bring-up) and every other scenario's queue fixture
// reuse it. These are cluster-scoped resources that testctx.PrepareTest's cleanup func does not
// remove (it only cascades from PodCliqueSets), so callers must defer this explicitly, and after
// defer cleanup(), so the PodCliqueSet (and its Kueue Workload) referencing the queue is already gone
// before the queue itself is deleted.
func deleteKueueQueueResources(ctx context.Context, t *testing.T, cl client.Client, queueName string) {
	t.Helper()
	localQueue := &kueuev1beta2.LocalQueue{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: queueName}}
	if err := client.IgnoreNotFound(cl.Delete(ctx, localQueue)); err != nil {
		t.Errorf("Failed to delete LocalQueue %s: %v", queueName, err)
	}
	clusterQueue := &kueuev1beta2.ClusterQueue{ObjectMeta: metav1.ObjectMeta{Name: queueName}}
	if err := client.IgnoreNotFound(cl.Delete(ctx, clusterQueue)); err != nil {
		t.Errorf("Failed to delete ClusterQueue %s: %v", queueName, err)
	}
}

// deleteKueueTopologyQueueResources deletes the LocalQueue/ClusterQueue/ResourceFlavor created by
// kueue-queues-topology.yaml. Unlike deleteKueueQueueResources, this also deletes the
// ResourceFlavor: Test_Kueue4's flavor is TAS-bound and scenario-specific, not the shared
// "default-flavor" other scenarios reuse.
func deleteKueueTopologyQueueResources(ctx context.Context, t *testing.T, cl client.Client, queueName string) {
	t.Helper()
	deleteKueueQueueResources(ctx, t, cl, queueName)
	flavor := &kueuev1beta2.ResourceFlavor{ObjectMeta: metav1.ObjectMeta{Name: queueName + "-flavor"}}
	if err := client.IgnoreNotFound(cl.Delete(ctx, flavor)); err != nil {
		t.Errorf("Failed to delete ResourceFlavor %s: %v", flavor.Name, err)
	}
}

// pollUntilTrue polls cond every interval until it reports satisfied, failing t with the last
// unsatisfied message if timeout elapses first.
func pollUntilTrue(t *testing.T, timeout, interval time.Duration, cond func() (satisfied bool, msg string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastMsg string
	for {
		ok, msg := cond()
		if ok {
			return
		}
		lastMsg = msg
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s: %s", timeout, lastMsg)
		}
		time.Sleep(interval)
	}
}

// hasSchedulingGate reports whether pod carries a scheduling gate named name.
func hasSchedulingGate(pod *corev1.Pod, name string) bool {
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name == name {
			return true
		}
	}
	return false
}

// waitForPodGangCount polls until exactly wantCount PodGangs exist for the PodCliqueSet named by
// pcsNsName, and returns them. PodGang object materialization can lag slightly behind the pod
// count/phase settling that callers typically wait for first (e.g. after a PCSG scale), so this
// avoids flaking on that race rather than trusting a single List call.
func waitForPodGangCount(ctx context.Context, t *testing.T, v *podgang.Verifier, pcsNsName types.NamespacedName, wantCount int, timeout, interval time.Duration) []groveschedulerv1alpha1.PodGang {
	t.Helper()
	var podGangs []groveschedulerv1alpha1.PodGang
	pollUntilTrue(t, timeout, interval, func() (bool, string) {
		var err error
		podGangs, err = v.List(ctx, pcsNsName)
		if err != nil {
			return false, fmt.Sprintf("failed to list PodGangs: %v", err)
		}
		if len(podGangs) != wantCount {
			return false, fmt.Sprintf("found %d PodGangs, want %d", len(podGangs), wantCount)
		}
		return true, ""
	})
	return podGangs
}

// nonAnchorPodGangs returns the Tail/ScaleOut-role PodGangs among podGangs -- the ones materialized
// one-per-(PodCliqueScalingGroup,replica-index) for PCSG replicas beyond the anchor entry's indices
// (see buildNonAnchorPodGangInfos in
// internal/controller/podcliqueset/components/podgang/syncflow.go).
func nonAnchorPodGangs(podGangs []groveschedulerv1alpha1.PodGang) []groveschedulerv1alpha1.PodGang {
	var result []groveschedulerv1alpha1.PodGang
	for _, pg := range podGangs {
		role := pg.Labels[nameutils.LabelPodGangRole]
		if role == string(corev1alpha1.PodGangEntryRoleTail) || role == string(corev1alpha1.PodGangEntryRoleScaleOut) {
			result = append(result, pg)
		}
	}
	return result
}

// kueueSchedulerClique builds a minimal PodCliqueTemplateSpec whose PodSpec.SchedulerName is "kueue",
// so the PodCliqueSet admission webhook resolves the kueue scheduler backend for it
// (validatePodCliqueSetWithBackend in internal/webhook/admission/pcs/validation/handler.go reads
// Spec.Template.Cliques[0].Spec.PodSpec.SchedulerName).
func kueueSchedulerClique(name string, replicas, minAvailable int32) *corev1alpha1.PodCliqueTemplateSpec {
	return testutils.NewPodCliqueTemplateSpecBuilder(name).
		WithRoleName(name + "-role").
		WithReplicas(replicas).
		WithMinAvailable(minAvailable).
		WithPodSpec(corev1.PodSpec{SchedulerName: "kueue"}).
		WithContainer(corev1.Container{Name: name, Image: "registry:5001/nginx:alpine-slim"}).
		Build()
}

// Test_Kueue1_FullGangAdmission verifies the base PodGang -> prebuilt Kueue Workload mapping for a
// single, full-gang standalone PodClique: the Workload exists at the PodGang's own namespace/name, is
// Admitted, targets the expected LocalQueue, and its single PodSet carries the full replica count with
// no MinCount (full gang).
func Test_Kueue1_FullGangAdmission(t *testing.T) {
	ctx := context.Background()
	expectedPods := 2

	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-workload1",
			YAMLPath:     "../yaml/kueue-workload1.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	requireKueueCRD(t, tc)

	Logger.Info("1. Deploy kueue-workload1 and wait for pods to be Running")
	if _, err := DeployWorkloadAndGetPods(tc, expectedPods); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("2. Find the PodGang for this PodCliqueSet")
	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	podGangs, err := pgVerifier.List(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name})
	if err != nil {
		t.Fatalf("Failed to list PodGangs: %v", err)
	}
	if len(podGangs) != 1 {
		t.Fatalf("Expected exactly 1 PodGang for a standalone full-gang PodCliqueSet, got %d", len(podGangs))
	}
	podGang := podGangs[0]

	Logger.Info("3. Verify the prebuilt Kueue Workload at the PodGang's own namespace/name")
	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	wl, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted)
	if err != nil {
		t.Fatalf("Kueue Workload for PodGang %s/%s not Admitted: %v", podGang.Namespace, podGang.Name, err)
	}
	if string(wl.Spec.QueueName) != "grove-e2e" {
		t.Fatalf("Workload QueueName = %q, want %q", wl.Spec.QueueName, "grove-e2e")
	}
	if len(wl.Spec.PodSets) != 1 {
		t.Fatalf("Expected exactly 1 PodSet, got %d", len(wl.Spec.PodSets))
	}
	if wl.Spec.PodSets[0].Count != int32(expectedPods) {
		t.Fatalf("PodSet Count = %d, want %d", wl.Spec.PodSets[0].Count, expectedPods)
	}
	if wl.Spec.PodSets[0].MinCount != nil {
		t.Fatalf("PodSet MinCount = %d, want nil (full gang)", *wl.Spec.PodSets[0].MinCount)
	}

	Logger.Info("Test_Kueue1_FullGangAdmission completed successfully!")
}

// Test_Kueue2_PartialGangAdmissionMinCount verifies partial-gang admission: a standalone PodClique
// with minAvailable < replicas maps to a Kueue podSet with MinCount set, and a tightly-quota'd
// ClusterQueue admits the Workload at that lower threshold instead of requiring full quota.
//
// It does NOT mean only MinCount pods run. Kueue's plain-Pod integration never implements
// per-pod partial admission (KEP-976 lists it as a Non-Goal, since all pods already exist by the
// time Kueue sees them); the admitted Count reaches podset.PodSetInfo.Count but podset.Merge (the
// function Pod.Run's ungate path actually calls) never reads it -- only job-type integrations
// consume Count themselves to shrink their pod template before unsuspending. So once Admitted,
// Kueue ungates every pod sharing the role, and all 4 reach Running; the ClusterQueue's
// status.flavorsUsage also under-reports actual consumption (100Mi charged vs. 200Mi running).
// Live-verified on a real cluster.
func Test_Kueue2_PartialGangAdmissionMinCount(t *testing.T) {
	ctx := context.Background()
	const queueName = "grove-e2e-partial"
	expectedPods := 4
	expectedMinCount := int32(2)

	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-workload2",
			YAMLPath:     "../yaml/kueue-workload2-partial.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	requireKueueCRD(t, tc)
	// Registered only after requireKueueCRD passes, so a skip (missing Kueue CRDs) never tries to
	// delete Kueue-typed objects the cluster can't even resolve. Relative order preserved (workload
	// cleanup before queue cleanup): deferred first, so it runs last.
	defer deleteKueueQueueResources(ctx, t, tc.Client, queueName)
	defer cleanup()

	Logger.Info("1. Apply the dedicated grove-e2e-partial queue")
	if _, err := tc.ApplyYAMLFile("../yaml/kueue-queues-partial.yaml"); err != nil {
		t.Fatalf("Failed to apply kueue-queues-partial.yaml: %v", err)
	}

	Logger.Info("2. Deploy kueue-workload2-partial and verify 4 pod objects exist regardless of admission")
	deployedPods, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	if len(deployedPods.Items) != expectedPods {
		t.Fatalf("Expected %d pod objects, got %d", expectedPods, len(deployedPods.Items))
	}

	Logger.Info("3. Find the PodGang and verify its prebuilt Kueue Workload is partially Admitted")
	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	podGangs, err := pgVerifier.List(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name})
	if err != nil {
		t.Fatalf("Failed to list PodGangs: %v", err)
	}
	if len(podGangs) != 1 {
		t.Fatalf("Expected exactly 1 PodGang, got %d", len(podGangs))
	}
	podGang := podGangs[0]

	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	wl, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted)
	if err != nil {
		t.Fatalf("Kueue Workload not Admitted: %v", err)
	}

	if len(wl.Spec.PodSets) != 1 {
		t.Fatalf("Expected exactly 1 PodSet, got %d", len(wl.Spec.PodSets))
	}
	if wl.Spec.PodSets[0].MinCount == nil || *wl.Spec.PodSets[0].MinCount != expectedMinCount {
		t.Fatalf("PodSet MinCount = %v, want %d", wl.Spec.PodSets[0].MinCount, expectedMinCount)
	}
	if wl.Status.Admission == nil || len(wl.Status.Admission.PodSetAssignments) != 1 {
		t.Fatalf("Expected exactly 1 Admission.PodSetAssignments entry, got: %+v", wl.Status.Admission)
	}
	assignedCount := wl.Status.Admission.PodSetAssignments[0].Count
	if assignedCount == nil || *assignedCount != expectedMinCount {
		t.Fatalf("Admission.PodSetAssignments[0].Count = %v, want %d (concrete proof of partial admission)", assignedCount, expectedMinCount)
	}

	Logger.Info("4. Verify all 4 pods reach Running (ungating is role-wide, not Count-limited; see doc comment)")
	if err := tc.WaitForPodCountAndPhases(expectedPods, expectedPods, 0); err != nil {
		t.Fatalf("Pod phase verification failed: %v", err)
	}

	Logger.Info("Test_Kueue2_PartialGangAdmissionMinCount completed successfully!")
}

// Test_Kueue3_PCSGScalingAddsRemovesWholePodGang verifies that PodCliqueScalingGroup replica-count
// scaling adds and removes whole PodGangs (and hence whole Kueue Workloads), without disturbing the
// anchor PodGang/Workload covering the PodCliqueScalingGroup's original replicas.
//
// Deviation from the original scenario design (verified against the real PodGangMap reconciliation
// code in internal/controller/podcliqueset/components/podgangmap/steadystate.go and
// .../podgang/syncflow.go, not assumed): the kueue backend's ValidatePodCliqueSet requires
// PodCliqueScalingGroup.MinAvailable == Replicas at PodCliqueSet creation time (the "all-or-nothing"
// rule). buildBootstrapAnchorEntry places PCSGReplicaIndices[0, MinAvailable) -- i.e. every replica
// present at creation -- into the single anchor PodGang, and buildBootstrapTailEntry creates no Tail
// entry when Replicas == MinAvailable. So the INITIAL deploy here produces exactly 1 PodGang (the
// anchor, with 2 PodGroups -- one per initial PodCliqueScalingGroup replica), not 2 separate PodGangs
// as the original design assumed. Separate, whole PodGangs only appear once the
// PodCliqueScalingGroup's own replica count (its own /scale subresource, independent of the
// PodCliqueSet-level admission webhook) is scaled beyond its original MinAvailable: those new indices
// land in a ScaleOut entry, which the PodGang materializer (buildNonAnchorPodGangInfos) expands into
// one PodGang per (PodCliqueScalingGroup, index). Scaling back down to exactly MinAvailable drains
// those ScaleOut indices first (drainReplicaIndicesForScaleIn's role order: ScaleOut, Tail, Anchor),
// never touching the anchor -- scaling below MinAvailable was not exercised here because it would
// drain into the anchor's own indices, mutating (not deleting) an existing PodGang whose prebuilt
// Kueue Workload is immutable once created; ensureWorkload has no repair path for that case (it only
// rebuilds a Finished or deactivated Workload, see Test_Kueue5), so it is out of scope for this
// scaffold.
func Test_Kueue3_PCSGScalingAddsRemovesWholePodGang(t *testing.T) {
	ctx := context.Background()
	const (
		cliqueReplicasPerPCSGReplica = 1
		initialPCSGReplicas          = 2
		scaledUpPCSGReplicas         = 4
	)
	expectedPods := cliqueReplicasPerPCSGReplica * initialPCSGReplicas

	tc, cleanup := testctx.PrepareTest(ctx, t, 4,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-pcsg-scale",
			YAMLPath:     "../yaml/kueue-pcsg-scale.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	requireKueueCRD(t, tc)

	Logger.Info("1. Deploy kueue-pcsg-scale and wait for pods to be Running")
	if _, err := DeployWorkloadAndGetPods(tc, expectedPods); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	pcsNsName := types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name}

	Logger.Info("2. Verify exactly 1 (anchor) PodGang exists, bundling both initial PCSG replicas")
	podGangs, err := pgVerifier.List(ctx, pcsNsName)
	if err != nil {
		t.Fatalf("Failed to list PodGangs: %v", err)
	}
	if len(podGangs) != 1 {
		t.Fatalf("Expected exactly 1 (anchor) PodGang after initial deploy, got %d", len(podGangs))
	}
	anchorPodGang := podGangs[0]

	anchorWL, err := wlVerifier.WaitUntilVerified(ctx, anchorPodGang.Namespace, anchorPodGang.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted)
	if err != nil {
		t.Fatalf("Anchor Kueue Workload not Admitted: %v", err)
	}
	anchorWorkloadUID := anchorWL.UID
	if len(anchorWL.Spec.PodSets) != initialPCSGReplicas {
		t.Fatalf("Anchor Workload has %d PodSets, want %d (one per initial PCSG replica)", len(anchorWL.Spec.PodSets), initialPCSGReplicas)
	}

	Logger.Info("3. Scale PCSG sg-worker replicas 2 -> 4 and verify 2 new whole PodGangs appear")
	pcsgInstanceName := fmt.Sprintf("%s-0-sg-worker", tc.Workload.Name)
	tc.ScalePCSGInstanceAndWait(pcsgInstanceName, scaledUpPCSGReplicas, cliqueReplicasPerPCSGReplica*scaledUpPCSGReplicas, 0)

	// 1 anchor (untouched) + 2 new non-anchor PodGangs (one per added replica index).
	podGangsAfterScaleUp := waitForPodGangCount(ctx, t, pgVerifier, pcsNsName, 3, tc.Timeout, tc.Interval)
	newPodGangs := nonAnchorPodGangs(podGangsAfterScaleUp)
	if len(newPodGangs) != 2 {
		t.Fatalf("Expected 2 non-anchor PodGangs after scale-up, got %d", len(newPodGangs))
	}

	Logger.Info("4. Verify the original anchor PodGang and Workload are untouched")
	refreshedAnchorPodGang, err := pgVerifier.Get(ctx, anchorPodGang.Namespace, anchorPodGang.Name)
	if err != nil {
		t.Fatalf("Failed to re-fetch anchor PodGang: %v", err)
	}
	if refreshedAnchorPodGang.UID != anchorPodGang.UID {
		t.Fatalf("Anchor PodGang was recreated during PCSG scale-up: UID changed from %s to %s", anchorPodGang.UID, refreshedAnchorPodGang.UID)
	}
	refreshedAnchorWL, err := wlVerifier.Get(ctx, anchorPodGang.Namespace, anchorPodGang.Name)
	if err != nil {
		t.Fatalf("Failed to re-fetch anchor Kueue Workload: %v", err)
	}
	if refreshedAnchorWL.UID != anchorWorkloadUID {
		t.Fatalf("Anchor Kueue Workload was recreated during PCSG scale-up: UID changed from %s to %s", anchorWorkloadUID, refreshedAnchorWL.UID)
	}
	if !kueueworkload.Admitted(refreshedAnchorWL) {
		t.Fatalf("Anchor Kueue Workload is no longer Admitted after PCSG scale-up")
	}

	Logger.Info("5. Verify each new PodGang has a distinct, independently Admitted Kueue Workload")
	newWorkloadUIDs := make(map[types.UID]struct{}, len(newPodGangs))
	for i := range newPodGangs {
		pg := newPodGangs[i]
		wl, err := wlVerifier.WaitUntilVerified(ctx, pg.Namespace, pg.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted)
		if err != nil {
			t.Fatalf("New PodGang %s/%s's Kueue Workload not Admitted: %v", pg.Namespace, pg.Name, err)
		}
		if wl.UID == anchorWorkloadUID {
			t.Fatalf("New PodGang %s/%s unexpectedly shares the anchor Workload's UID", pg.Namespace, pg.Name)
		}
		newWorkloadUIDs[wl.UID] = struct{}{}
	}
	if len(newWorkloadUIDs) != 2 {
		t.Fatalf("Expected 2 distinct new Kueue Workload UIDs, got %d", len(newWorkloadUIDs))
	}

	Logger.Info("6. Scale PCSG sg-worker replicas 4 -> 2 (back to MinAvailable) and verify the 2 new PodGangs are removed")
	tc.ScalePCSGInstanceAndWait(pcsgInstanceName, initialPCSGReplicas, expectedPods, 0)

	podGangsAfterScaleDown := waitForPodGangCount(ctx, t, pgVerifier, pcsNsName, 1, tc.Timeout, tc.Interval)
	if podGangsAfterScaleDown[0].UID != anchorPodGang.UID {
		t.Fatalf("The surviving PodGang is not the original anchor: UID %s, want %s", podGangsAfterScaleDown[0].UID, anchorPodGang.UID)
	}

	Logger.Info("7. Verify the 2 removed PodGangs' Kueue Workloads were garbage-collected via owner reference cascade")
	for i := range newPodGangs {
		pg := newPodGangs[i]
		pollUntilTrue(t, tc.Timeout, tc.Interval, func() (bool, string) {
			_, err := wlVerifier.Get(ctx, pg.Namespace, pg.Name)
			if apierrors.IsNotFound(err) {
				return true, ""
			}
			return false, fmt.Sprintf("Get(%s/%s) = %v, want NotFound", pg.Namespace, pg.Name, err)
		})
	}

	Logger.Info("Test_Kueue3_PCSGScalingAddsRemovesWholePodGang completed successfully!")
}

// Test_Kueue4_TopologyAwarePodSetRequest verifies that the kueue backend, as a
// scheduler.TopologyAwareBackend auto-synced by the shared "grove-topology" ClusterTopologyBinding,
// resolves a clique's required host-level topology constraint into the matching Kueue Workload
// PodSet's TopologyRequest, and that placement actually honors it.
func Test_Kueue4_TopologyAwarePodSetRequest(t *testing.T) {
	ctx := context.Background()
	const queueName = "grove-e2e-topology"
	expectedPods := 2

	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-topology",
			YAMLPath:     "../yaml/kueue-topology.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	requireKueueCRD(t, tc)
	// Registered only after requireKueueCRD passes, so a skip (missing Kueue CRDs) never tries to
	// delete Kueue-typed objects the cluster can't even resolve. Relative order preserved (workload
	// cleanup before queue cleanup): deferred first, so it runs last.
	defer deleteKueueTopologyQueueResources(ctx, t, tc.Client, queueName)
	defer cleanup()

	topologyVerifier := topology.NewTopologyVerifier(tc.Client, Logger)
	ensureGroveTopology(ctx, t, topologyVerifier)

	Logger.Info("1. Apply the dedicated TAS-bound grove-e2e-topology queue")
	if _, err := tc.ApplyYAMLFile("../yaml/kueue-queues-topology.yaml"); err != nil {
		t.Fatalf("Failed to apply kueue-queues-topology.yaml: %v", err)
	}

	Logger.Info("2. Deploy kueue-topology and wait for pods to be Running")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify the Kueue Workload's PodSet carries the required host-level TopologyRequest")
	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	podGangs, err := pgVerifier.List(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name})
	if err != nil {
		t.Fatalf("Failed to list PodGangs: %v", err)
	}
	if len(podGangs) != 1 {
		t.Fatalf("Expected exactly 1 PodGang, got %d", len(podGangs))
	}
	podGang := podGangs[0]

	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	wl, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted)
	if err != nil {
		t.Fatalf("Kueue Workload not Admitted: %v", err)
	}
	if len(wl.Spec.PodSets) != 1 {
		t.Fatalf("Expected exactly 1 PodSet, got %d", len(wl.Spec.PodSets))
	}
	topoReq := wl.Spec.PodSets[0].TopologyRequest
	if topoReq == nil || topoReq.Required == nil || *topoReq.Required != setup.TopologyLabelHostname {
		t.Fatalf("Expected PodSet TopologyRequest.Required = %q, got %+v", setup.TopologyLabelHostname, topoReq)
	}

	Logger.Info("4. Verify actual pod placement honored the host-level pack constraint")
	if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, allPods, setup.TopologyLabelHostname); err != nil {
		t.Fatalf("Failed to verify pods on same host: %v", err)
	}

	Logger.Info("Test_Kueue4_TopologyAwarePodSetRequest completed successfully!")
}

// Test_Kueue5_WorkloadRepairAfterFinished exercises ensureWorkload's delete-and-recreate repair of a
// Finished prebuilt Kueue Workload end to end. Kueue finishes a serving pod group's Workload only on
// errors such as falling out of sync with its pods, so a Finished condition is patched directly onto the
// Workload's status, and a single pod is deleted to invoke PreparePod for its replacement, which is the
// path that re-runs ensureWorkload with an uncached read (see backend.go's ensureWorkload and PreparePod).
func Test_Kueue5_WorkloadRepairAfterFinished(t *testing.T) {
	ctx := context.Background()
	expectedPods := 2

	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-workload1",
			YAMLPath:     "../yaml/kueue-workload1.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
		testctx.WithTimeout(10*time.Minute),
	)
	defer cleanup()
	requireKueueCRD(t, tc)

	Logger.Info("1. Deploy kueue-workload1 and wait for Admitted=True + pods Running")
	if _, err := DeployWorkloadAndGetPods(tc, expectedPods); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	podGangs, err := pgVerifier.List(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name})
	if err != nil {
		t.Fatalf("Failed to list PodGangs: %v", err)
	}
	if len(podGangs) != 1 {
		t.Fatalf("Expected exactly 1 PodGang, got %d", len(podGangs))
	}
	podGang := podGangs[0]

	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	originalWL, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted)
	if err != nil {
		t.Fatalf("Kueue Workload not Admitted: %v", err)
	}
	originalUID := originalWL.UID

	Logger.Info("2. Force a WorkloadFinished=True condition directly onto the Workload's status")
	apimeta.SetStatusCondition(&originalWL.Status.Conditions, metav1.Condition{
		Type:    kueuev1beta2.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "E2ETestForced",
		Message: "forced by Test_Kueue5 to deterministically exercise ensureWorkload's repair path",
	})
	if err := tc.Client.Status().Update(ctx, originalWL); err != nil {
		t.Fatalf("Failed to patch Kueue Workload status to Finished=True: %v", err)
	}

	Logger.Info("3. Delete one pod to invoke PreparePod for its replacement, which re-runs ensureWorkload")
	podList, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	if len(podList.Items) == 0 {
		t.Fatalf("Expected at least 1 pod to delete, found none")
	}
	podToDelete := podList.Items[0]
	if err := tc.Client.Delete(ctx, &podToDelete); err != nil {
		t.Fatalf("Failed to delete pod %s: %v", podToDelete.Name, err)
	}

	Logger.Info("4. Verify a new Workload (different UID) appears at the same namespace/name and reaches Admitted=True")
	repaired, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval,
		func(wl *kueuev1beta2.Workload) bool {
			return kueueworkload.UIDChanged(originalUID)(wl) && kueueworkload.Admitted(wl)
		},
	)
	if err != nil {
		t.Fatalf("Kueue Workload was not repaired and re-admitted: %v", err)
	}
	Logger.Infof("Workload repaired: old UID=%s, new UID=%s", originalUID, repaired.UID)

	Logger.Info("5. Verify pods return to Running")
	if err := tc.WaitForPods(expectedPods); err != nil {
		t.Fatalf("Failed to wait for pods to return to Running: %v", err)
	}

	Logger.Info("Test_Kueue5_WorkloadRepairAfterFinished completed successfully!")
}

// Test_Kueue6_ValidatePodCliqueSetAndScaleRejection is a table-driven test of the kueue backend's
// admission-webhook rejections: ValidatePodCliqueSet (Create) and ValidatePodCliqueScale (PodClique
// /scale subresource). Mirrors Test_TAS22_PodCliqueSetTopologyCELValidation's structure.
func Test_Kueue6_ValidatePodCliqueSetAndScaleRejection(t *testing.T) {
	ctx := context.Background()
	tc, cleanup := testctx.PrepareTest(ctx, t, 0)
	defer cleanup()
	requireKueueCRD(t, tc)

	newPCS := func(name string) *corev1alpha1.PodCliqueSet {
		pcs := testutils.NewPodCliqueSetBuilder(name, "default", uuid.NewUUID()).
			WithReplicas(1).
			Build()
		pcs.Labels = map[string]string{queueNameLabel: "grove-e2e"}
		return pcs
	}

	t.Run("PCSG minAvailable < replicas is rejected", func(t *testing.T) {
		pcs := newPCS("kueue-t6-pcsg-min")
		pcs.Spec.Template.Cliques = []*corev1alpha1.PodCliqueTemplateSpec{kueueSchedulerClique("worker", 1, 1)}
		pcs.Spec.Template.PodCliqueScalingGroupConfigs = []corev1alpha1.PodCliqueScalingGroupConfig{
			{
				Name:         "sg",
				CliqueNames:  []string{"worker"},
				Replicas:     ptr.To(int32(2)),
				MinAvailable: ptr.To(int32(1)),
			},
		}
		err := tc.Client.Create(ctx, pcs)
		if err == nil {
			t.Fatalf("Expected PCSG minAvailable < replicas to be rejected, but create succeeded")
		}
		wantErrSubstr := "kueue backend requires PodCliqueScalingGroups to set minAvailable == replicas"
		if !strings.Contains(err.Error(), wantErrSubstr) {
			t.Fatalf("Expected error to contain %q, got: %v", wantErrSubstr, err)
		}
	})

	t.Run("PCSG-member PodClique minAvailable < replicas is rejected", func(t *testing.T) {
		pcs := newPCS("kueue-t6-member-min")
		pcs.Spec.Template.Cliques = []*corev1alpha1.PodCliqueTemplateSpec{kueueSchedulerClique("worker", 2, 1)}
		pcs.Spec.Template.PodCliqueScalingGroupConfigs = []corev1alpha1.PodCliqueScalingGroupConfig{
			{
				Name:         "sg",
				CliqueNames:  []string{"worker"},
				Replicas:     ptr.To(int32(2)),
				MinAvailable: ptr.To(int32(2)),
			},
		}
		err := tc.Client.Create(ctx, pcs)
		if err == nil {
			t.Fatalf("Expected PCSG-member minAvailable < replicas to be rejected, but create succeeded")
		}
		wantErrSubstr := "kueue backend requires PodCliques that are members of a PodCliqueScalingGroup to set minAvailable == replicas"
		if !strings.Contains(err.Error(), wantErrSubstr) {
			t.Fatalf("Expected error to contain %q, got: %v", wantErrSubstr, err)
		}
	})

	t.Run("more than one partial-gang standalone PodClique is rejected", func(t *testing.T) {
		pcs := newPCS("kueue-t6-two-partial")
		pcs.Spec.Template.Cliques = []*corev1alpha1.PodCliqueTemplateSpec{
			kueueSchedulerClique("worker-a", 4, 2),
			kueueSchedulerClique("worker-b", 4, 2),
		}
		err := tc.Client.Create(ctx, pcs)
		if err == nil {
			t.Fatalf("Expected 2 partial-gang standalone PodCliques to be rejected, but create succeeded")
		}
		wantErrSubstr := "kueue backend allows at most one standalone PodClique with minAvailable < replicas"
		if !strings.Contains(err.Error(), wantErrSubstr) {
			t.Fatalf("Expected error to contain %q, got: %v", wantErrSubstr, err)
		}
	})

	t.Run("autoScalingConfig on a PodClique is rejected", func(t *testing.T) {
		clique := testutils.NewPodCliqueTemplateSpecBuilder("worker").
			WithRoleName("worker-role").
			WithReplicas(2).
			WithPodSpec(corev1.PodSpec{SchedulerName: "kueue"}).
			WithContainer(corev1.Container{Name: "worker", Image: "registry:5001/nginx:alpine-slim"}).
			WithScaleConfig(ptr.To(int32(1)), 4).
			Build()
		pcs := newPCS("kueue-t6-autoscale")
		pcs.Spec.Template.Cliques = []*corev1alpha1.PodCliqueTemplateSpec{clique}
		err := tc.Client.Create(ctx, pcs)
		if err == nil {
			t.Fatalf("Expected autoScalingConfig on a PodClique to be rejected, but create succeeded")
		}
		wantErrSubstr := "kueue backend does not support autoScalingConfig on a PodClique"
		if !strings.Contains(err.Error(), wantErrSubstr) {
			t.Fatalf("Expected error to contain %q, got: %v", wantErrSubstr, err)
		}
	})

	t.Run("PCSG resolving both a required and a preferred topology domain is rejected", func(t *testing.T) {
		ensureGroveTopology(ctx, t, topology.NewTopologyVerifier(tc.Client, Logger))
		pcs := newPCS("kueue-t6-pcsg-topology")
		pcs.Spec.Template.Cliques = []*corev1alpha1.PodCliqueTemplateSpec{kueueSchedulerClique("worker", 2, 2)}
		pcs.Spec.Template.PodCliqueScalingGroupConfigs = []corev1alpha1.PodCliqueScalingGroupConfig{
			{
				Name:         "sg",
				CliqueNames:  []string{"worker"},
				Replicas:     ptr.To(int32(1)),
				MinAvailable: ptr.To(int32(1)),
				TopologyConstraint: &corev1alpha1.TopologyConstraint{
					TopologyName: "grove-topology",
					Pack: &corev1alpha1.TopologyPackConstraint{
						RequiredDomain:  corev1alpha1.TopologyDomainBlock,
						PreferredDomain: corev1alpha1.TopologyDomainRack,
					},
				},
			},
		}
		err := tc.Client.Create(ctx, pcs)
		if err == nil {
			t.Fatalf("Expected a PCSG resolving both a required and a preferred topology domain to be rejected, but create succeeded")
		}
		wantErrSubstr := "kueue backend does not support a PodClique resolving both a required and a preferred topology domain"
		if !strings.Contains(err.Error(), wantErrSubstr) {
			t.Fatalf("Expected error to contain %q, got: %v", wantErrSubstr, err)
		}
	})

	t.Run("PodGang with more PodGroups than Kueue's podSet limit is rejected", func(t *testing.T) {
		pcs := newPCS("kueue-t6-podset-limit")
		for _, name := range []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"} {
			pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, kueueSchedulerClique(name, 1, 1))
		}
		err := tc.Client.Create(ctx, pcs)
		if err == nil {
			t.Fatalf("Expected a PodCliqueSet whose base PodGang has 9 PodGroups to be rejected, but create succeeded")
		}
		wantErrSubstr := "kueue backend allows at most 8 PodGroups per PodGang"
		if !strings.Contains(err.Error(), wantErrSubstr) {
			t.Fatalf("Expected error to contain %q, got: %v", wantErrSubstr, err)
		}
	})

	t.Run("valid partial-gang standalone PodClique is accepted", func(t *testing.T) {
		pcs := newPCS("kueue-t6-valid")
		pcs.Spec.Template.Cliques = []*corev1alpha1.PodCliqueTemplateSpec{kueueSchedulerClique("worker", 4, 2)}
		// Dry run: the webhook still runs, but no Workload or pods are created.
		if err := tc.Client.Create(ctx, pcs, client.DryRunAll); err != nil {
			t.Fatalf("Expected valid partial-gang PodCliqueSet create to succeed, got: %v", err)
		}
	})

	t.Run("PodClique scale is rejected by ValidatePodCliqueScale", func(t *testing.T) {
		if _, err := tc.ApplyYAMLFile("../yaml/kueue-workload1.yaml"); err != nil {
			t.Fatalf("Failed to apply kueue-workload1.yaml: %v", err)
		}
		pclqName := nameutils.GeneratePodCliqueName(nameutils.ResourceNameReplica{Name: "kueue-workload1", Replica: 0}, "worker")
		if _, err := workload.WaitForPodCliqueStandalone(ctx, tc.Client, tc.Namespace, pclqName, tc.Timeout, tc.Interval); err != nil {
			t.Fatalf("Failed waiting for PodClique %s to exist: %v", pclqName, err)
		}
		err := tc.ScalePodClique(pclqName, 4)
		if err == nil {
			t.Fatalf("Expected PodClique scale to be denied by the kueue backend, but it succeeded")
		}
		wantErrSubstr := "kueue backend does not support scaling a PodClique"
		if !strings.Contains(err.Error(), wantErrSubstr) {
			t.Fatalf("Expected scale error to contain %q, got: %v", wantErrSubstr, err)
		}
	})

	Logger.Info("Test_Kueue6_ValidatePodCliqueSetAndScaleRejection completed successfully!")
}

// Test_Kueue7_QueuedWorkloadAdmitsWhenQuotaFrees verifies that a Workload genuinely queues (pods
// created but scheduling-gated, not Admitted) while its ClusterQueue's quota is fully consumed by
// another Workload, and admits once that quota frees up.
func Test_Kueue7_QueuedWorkloadAdmitsWhenQuotaFrees(t *testing.T) {
	ctx := context.Background()
	const (
		queueName  = "grove-e2e-backlog"
		fillerName = "kueue-workload-filler"
	)
	expectedPods := 2

	tc, cleanup := testctx.PrepareTest(ctx, t, 4,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-workload-queued",
			YAMLPath:     "../yaml/kueue-workload-queued.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	requireKueueCRD(t, tc)
	// Registered only after requireKueueCRD passes, so a skip (missing Kueue CRDs) never tries to
	// delete Kueue-typed objects the cluster can't even resolve. Relative order preserved (workload
	// cleanup before queue cleanup): deferred first, so it runs last.
	defer deleteKueueQueueResources(ctx, t, tc.Client, queueName)
	defer cleanup()

	Logger.Info("1. Apply the dedicated grove-e2e-backlog queue")
	if _, err := tc.ApplyYAMLFile("../yaml/kueue-queues-backlog.yaml"); err != nil {
		t.Fatalf("Failed to apply kueue-queues-backlog.yaml: %v", err)
	}

	pm := pods.NewPodManager(tc.Client, Logger)
	fillerSelector := fmt.Sprintf("%s=%s", nameutils.LabelPartOfKey, fillerName)
	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)

	Logger.Info("2. Deploy the filler workload and wait for it to be Admitted + Running (consumes the entire quota)")
	if _, err := tc.ApplyYAMLFile("../yaml/kueue-workload-filler.yaml"); err != nil {
		t.Fatalf("Failed to apply filler workload: %v", err)
	}
	if err := pm.WaitForCountAndPhases(ctx, tc.Namespace, fillerSelector, expectedPods, expectedPods, 0, tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for filler pods to be Running: %v", err)
	}
	fillerPodGangs, err := pgVerifier.List(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: fillerName})
	if err != nil {
		t.Fatalf("Failed to list filler PodGangs: %v", err)
	}
	if len(fillerPodGangs) != 1 {
		t.Fatalf("Expected exactly 1 filler PodGang, got %d", len(fillerPodGangs))
	}
	fillerPodGang := fillerPodGangs[0]
	if _, err := wlVerifier.WaitUntilVerified(ctx, fillerPodGang.Namespace, fillerPodGang.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted); err != nil {
		t.Fatalf("Filler Kueue Workload not Admitted (test setup broken): %v", err)
	}

	Logger.Info("3. Deploy the queued workload; its pods are created immediately but stay gated/un-Admitted")
	queuedPods, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy queued workload: %v", err)
	}
	if len(queuedPods.Items) != expectedPods {
		t.Fatalf("Expected %d queued pod objects, got %d", expectedPods, len(queuedPods.Items))
	}

	queuedPodGangs, err := pgVerifier.List(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name})
	if err != nil {
		t.Fatalf("Failed to list queued PodGangs: %v", err)
	}
	if len(queuedPodGangs) != 1 {
		t.Fatalf("Expected exactly 1 queued PodGang, got %d", len(queuedPodGangs))
	}
	queuedPodGang := queuedPodGangs[0]

	queuedWL, err := wlVerifier.Get(ctx, queuedPodGang.Namespace, queuedPodGang.Name)
	if err != nil {
		t.Fatalf("Failed to get queued Kueue Workload: %v", err)
	}
	if kueueworkload.Admitted(queuedWL) {
		t.Fatalf("Expected queued Workload to NOT be Admitted while the filler holds the quota")
	}

	Logger.Info("4. Verify the ClusterQueue reports exactly 1 pending workload")
	pollUntilTrue(t, tc.Timeout, tc.Interval, func() (bool, string) {
		var cq kueuev1beta2.ClusterQueue
		if err := tc.Client.Get(ctx, client.ObjectKey{Name: queueName}, &cq); err != nil {
			return false, fmt.Sprintf("failed to get ClusterQueue %s: %v", queueName, err)
		}
		if cq.Status.PendingWorkloads != 1 {
			return false, fmt.Sprintf("ClusterQueue %s PendingWorkloads = %d, want 1", queueName, cq.Status.PendingWorkloads)
		}
		return true, ""
	})

	Logger.Info("5. Verify queued pods are Pending with the Kueue admission scheduling gate")
	queuedPodList, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list queued pods: %v", err)
	}
	if len(queuedPodList.Items) != expectedPods {
		t.Fatalf("Expected %d queued pods, got %d", expectedPods, len(queuedPodList.Items))
	}
	for i := range queuedPodList.Items {
		pod := &queuedPodList.Items[i]
		if pod.Status.Phase != corev1.PodPending {
			t.Fatalf("Expected queued pod %s to be Pending, got %s", pod.Name, pod.Status.Phase)
		}
		if !hasSchedulingGate(pod, kueueAdmissionSchedulingGate) {
			t.Fatalf("Expected queued pod %s to carry scheduling gate %q, got gates: %+v", pod.Name, kueueAdmissionSchedulingGate, pod.Spec.SchedulingGates)
		}
	}

	Logger.Info("6. Hold for a short window and re-verify the queued Workload has not admitted immediately")
	time.Sleep(30 * time.Second)
	queuedWLRecheck, err := wlVerifier.Get(ctx, queuedPodGang.Namespace, queuedPodGang.Name)
	if err != nil {
		t.Fatalf("Failed to re-get queued Kueue Workload: %v", err)
	}
	if kueueworkload.Admitted(queuedWLRecheck) {
		t.Fatalf("Queued Workload became Admitted before the filler was deleted (quota should still be fully consumed)")
	}

	Logger.Info("7. Delete the filler PodCliqueSet to free its quota (the trigger for this scenario)")
	fillerPCS := &corev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Namespace: tc.Namespace, Name: fillerName}}
	if err := client.IgnoreNotFound(tc.Client.Delete(ctx, fillerPCS)); err != nil {
		t.Fatalf("Failed to delete filler PodCliqueSet: %v", err)
	}

	Logger.Info("8. Poll the queued Workload until Admitted=True")
	if _, err := wlVerifier.WaitUntilVerified(ctx, queuedPodGang.Namespace, queuedPodGang.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted); err != nil {
		t.Fatalf("Queued Kueue Workload did not become Admitted after filler quota freed: %v", err)
	}

	Logger.Info("9. Verify queued pods' scheduling gates are removed and pods reach Running")
	if err := tc.WaitForPods(expectedPods); err != nil {
		t.Fatalf("Failed to wait for queued pods to become Running: %v", err)
	}
	finalPods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list final queued pods: %v", err)
	}
	for i := range finalPods.Items {
		pod := &finalPods.Items[i]
		if hasSchedulingGate(pod, kueueAdmissionSchedulingGate) {
			t.Fatalf("Expected pod %s to have its Kueue admission scheduling gate removed, still present: %+v", pod.Name, pod.Spec.SchedulingGates)
		}
	}

	Logger.Info("Test_Kueue7_QueuedWorkloadAdmitsWhenQuotaFrees completed successfully!")
}

// Test_Kueue8_AllOrNothingPodsReadyEvictsAndReadmits verifies Kueue's waitForPodsReady eviction: a
// Workload that is Admitted but never reaches PodsReady=true (because one of its two PodGroups is
// permanently unschedulable) gets Evicted after the configured timeout, which deletes every pod in
// the group and sends it through admission again -- an "all or nothing" pod start. kueue-values.yaml
// configures a 15s waitForPodsReady.timeout for this purpose.
func Test_Kueue8_AllOrNothingPodsReadyEvictsAndReadmits(t *testing.T) {
	ctx := context.Background()
	expectedPods := 2

	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-allornothing",
			YAMLPath:     "../yaml/kueue-workload-allornothing.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	requireKueueCRD(t, tc)
	defer cleanup()

	Logger.Info("1. Deploy kueue-allornothing: 'ready' schedules fine, 'stuck' requests memory no node can satisfy")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("2. Verify the Workload admits immediately (quota fits; readiness is a separate, later check)")
	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	podGangs, err := pgVerifier.List(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name})
	if err != nil {
		t.Fatalf("Failed to list PodGangs: %v", err)
	}
	if len(podGangs) != 1 {
		t.Fatalf("Expected exactly 1 PodGang, got %d", len(podGangs))
	}
	podGang := podGangs[0]

	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	admitted, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted)
	if err != nil {
		t.Fatalf("Kueue Workload not Admitted: %v", err)
	}
	originalUID := admitted.UID

	originalPods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list original pods: %v", err)
	}
	originalPodUIDs := make(map[types.UID]struct{}, len(originalPods.Items))
	for _, pod := range originalPods.Items {
		originalPodUIDs[pod.UID] = struct{}{}
	}

	Logger.Info("3. Verify Kueue evicts the Workload with reason PodsReadyTimeout once the configured timeout elapses")
	if _, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.EvictedByPodsReadyTimeout); err != nil {
		t.Fatalf("Kueue Workload was not evicted for PodsReadyTimeout: %v", err)
	}

	// "stuck" never gets a node, so KWOK never strips Kueue's pod finalizer from it: it only goes away
	// because Grove removes that finalizer from Pods being deleted (RemovePodFinalizers).
	Logger.Info("4. Verify every original pod is gone, replaced by a new admission attempt")
	pollUntilTrue(t, tc.Timeout, tc.Interval, func() (bool, string) {
		current, err := tc.ListPods()
		if err != nil {
			return false, fmt.Sprintf("failed to list pods: %v", err)
		}
		sawNewPod := false
		for _, pod := range current.Items {
			if _, stillOriginal := originalPodUIDs[pod.UID]; stillOriginal {
				return false, fmt.Sprintf("pod %s (uid %s) from the original admission still exists", pod.Name, pod.UID)
			}
			sawNewPod = true
		}
		if !sawNewPod {
			return false, "no replacement pods created yet"
		}
		return true, ""
	})

	Logger.Info("5. Verify Kueue requeued the same Workload: Grove's serving pod groups never finish")
	current, err := wlVerifier.Get(ctx, podGang.Namespace, podGang.Name)
	if err != nil {
		t.Fatalf("Failed to get current Kueue Workload: %v", err)
	}
	if current.UID != originalUID {
		t.Fatalf("Expected Workload %s/%s to be requeued in place, but it was rebuilt: original UID=%s, current UID=%s", podGang.Namespace, podGang.Name, originalUID, current.UID)
	}

	Logger.Info("Test_Kueue8_AllOrNothingPodsReadyEvictsAndReadmits completed successfully!")
}

// kwokStageCrashloopKueueStuckPath/Name point at the KWOK Stage that holds the
// kueue-allornothing-notready fixture's "stuck" pod Running-but-NotReady (simulated
// CrashLoopBackOff) instead of leaving it permanently unschedulable like Test_Kueue8's fixture.
const (
	kwokStageCrashloopKueueStuckPath = "../yaml/kwok/pod-crashloop-kueue-allornothing-notready-stuck.yaml"
	kwokStageCrashloopKueueStuckName = "pod-crashloop-kueue-allornothing-notready-stuck"
)

// Test_Kueue9_AllOrNothingRunningNotReadyEvictsAndReadmits is Test_Kueue8's scenario with a
// different trigger: instead of a permanently unschedulable pod, "stuck" actually gets scheduled
// and starts running, but a KWOK Stage holds it Running with Ready=False (a simulated
// CrashLoopBackOff), so it never reaches PodsReady. This exercises waitForPodsReady's literal
// meaning: a started-but-not-ready container, not merely an unscheduled one.
func Test_Kueue9_AllOrNothingRunningNotReadyEvictsAndReadmits(t *testing.T) {
	ctx := context.Background()
	expectedPods := 2

	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-allornothing-notready",
			YAMLPath:     "../yaml/kueue-workload-allornothing-notready.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	requireKueueCRD(t, tc)
	defer cleanup()

	if err := kwok.ApplyStage(ctx, tc.Client, kwokStageCrashloopKueueStuckPath); err != nil {
		t.Fatalf("Failed to apply KWOK stage %s: %v", kwokStageCrashloopKueueStuckName, err)
	}
	defer func() {
		if err := kwok.DeleteStage(ctx, tc.Client, kwokStageCrashloopKueueStuckName); err != nil {
			t.Errorf("Failed to delete KWOK stage %s: %v", kwokStageCrashloopKueueStuckName, err)
		}
	}()

	Logger.Info("1. Deploy kueue-allornothing-notready: 'ready' becomes Ready normally, 'stuck' is held Running-but-NotReady by the KWOK stage")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("2. Verify the Workload admits immediately (quota fits; readiness is a separate, later check)")
	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	podGangs, err := pgVerifier.List(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name})
	if err != nil {
		t.Fatalf("Failed to list PodGangs: %v", err)
	}
	if len(podGangs) != 1 {
		t.Fatalf("Expected exactly 1 PodGang, got %d", len(podGangs))
	}
	podGang := podGangs[0]

	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	admitted, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.Admitted)
	if err != nil {
		t.Fatalf("Kueue Workload not Admitted: %v", err)
	}
	originalUID := admitted.UID

	originalPods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list original pods: %v", err)
	}
	originalPodUIDs := make(map[types.UID]struct{}, len(originalPods.Items))
	for _, pod := range originalPods.Items {
		originalPodUIDs[pod.UID] = struct{}{}
	}

	Logger.Info("3. Verify Kueue evicts the Workload with reason PodsReadyTimeout once the configured timeout elapses")
	if _, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, kueueworkload.EvictedByPodsReadyTimeout); err != nil {
		t.Fatalf("Kueue Workload was not evicted for PodsReadyTimeout: %v", err)
	}

	Logger.Info("4. Verify every original pod is gone, replaced by a new admission attempt")
	pollUntilTrue(t, tc.Timeout, tc.Interval, func() (bool, string) {
		current, err := tc.ListPods()
		if err != nil {
			return false, fmt.Sprintf("failed to list pods: %v", err)
		}
		sawNewPod := false
		for _, pod := range current.Items {
			if _, stillOriginal := originalPodUIDs[pod.UID]; stillOriginal {
				return false, fmt.Sprintf("pod %s (uid %s) from the original admission still exists", pod.Name, pod.UID)
			}
			sawNewPod = true
		}
		if !sawNewPod {
			return false, "no replacement pods created yet"
		}
		return true, ""
	})

	Logger.Info("5. Verify Kueue requeued the same Workload: Grove's serving pod groups never finish")
	current, err := wlVerifier.Get(ctx, podGang.Namespace, podGang.Name)
	if err != nil {
		t.Fatalf("Failed to get current Kueue Workload: %v", err)
	}
	if current.UID != originalUID {
		t.Fatalf("Expected Workload %s/%s to be requeued in place, but it was rebuilt: original UID=%s, current UID=%s", podGang.Namespace, podGang.Name, originalUID, current.UID)
	}

	Logger.Info("Test_Kueue9_AllOrNothingRunningNotReadyEvictsAndReadmits completed successfully!")
}

// Test_Kueue10_UnsortedCliquesKeepWorkloadInSync verifies Kueue accepts the prebuilt Workload of a PodGang whose
// cliques aren't in name order: Kueue checks a Workload's PodSets against its pods' PodSets sorted by name.
func Test_Kueue10_UnsortedCliquesKeepWorkloadInSync(t *testing.T) {
	ctx := context.Background()
	expectedPods := 2

	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "kueue-clique-order",
			YAMLPath:     "../yaml/kueue-workload-clique-order.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	requireKueueCRD(t, tc)

	Logger.Info("1. Deploy kueue-clique-order, whose cliques are listed prefill before decode")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	pgVerifier := podgang.NewVerifier(tc.Client, Logger)
	podGangs, err := pgVerifier.List(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name})
	if err != nil {
		t.Fatalf("Failed to list PodGangs: %v", err)
	}
	if len(podGangs) != 1 {
		t.Fatalf("Expected exactly 1 PodGang, got %d", len(podGangs))
	}
	podGang := podGangs[0]

	// Kueue sets PodsReady only after finding the Workload in sync with every pod of the group, so an out-of-sync
	// Workload never gets it: Kueue finishes it instead, and Grove may then rebuild it under a new UID.
	Logger.Info("2. Verify Kueue reports every pod Ready without ever finishing the Workload")
	wlVerifier := kueueworkload.NewVerifier(tc.Client, Logger)
	original, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval, waiter.AlwaysTrue[*kueuev1beta2.Workload])
	if err != nil {
		t.Fatalf("Kueue Workload not found: %v", err)
	}
	wl, err := wlVerifier.WaitUntilVerified(ctx, podGang.Namespace, podGang.Name, tc.Timeout, tc.Interval,
		func(wl *kueuev1beta2.Workload) bool {
			return kueueworkload.PodsReady(wl) || kueueworkload.Finished(wl) || kueueworkload.UIDChanged(original.UID)(wl)
		},
	)
	if err != nil {
		t.Fatalf("Kueue Workload never reached PodsReady=True: %v", err)
	}
	if kueueworkload.Finished(wl) {
		cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueuev1beta2.WorkloadFinished)
		podSetNames := make([]kueuev1beta2.PodSetReference, 0, len(wl.Spec.PodSets))
		for _, podSet := range wl.Spec.PodSets {
			podSetNames = append(podSetNames, podSet.Name)
		}
		t.Fatalf("Kueue finished Workload %s/%s (reason %s: %s); its PodSets are %v", wl.Namespace, wl.Name, cond.Reason, cond.Message, podSetNames)
	}
	if wl.UID != original.UID {
		t.Fatalf("Workload %s/%s was rebuilt (UID %s -> %s), which Grove does only once Kueue finishes or deactivates it", wl.Namespace, wl.Name, original.UID, wl.UID)
	}

	Logger.Info("3. Verify both pods are Running")
	if err := tc.WaitForPods(expectedPods); err != nil {
		t.Fatalf("Failed to wait for pods to be Running: %v", err)
	}

	Logger.Info("Test_Kueue10_UnsortedCliquesKeepWorkloadInSync completed successfully!")
}
