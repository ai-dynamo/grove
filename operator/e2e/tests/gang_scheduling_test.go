//go:build e2e

// Copyright 2025 The Grove Authors.
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
	"strconv"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgang"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Test_GS1_GangSchedulingWithFullReplicas tests gang-scheduling behavior with insufficient resources
// Scenario GS-1:
// 1. Initialize a 10-node Grove cluster, then cordon 1 node
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon the node and verify all pods get scheduled
func Test_GS1_GangSchedulingWithFullReplicas(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 10-node Grove cluster, then cordon 1 node")
	// Setup test cluster with 10 worker nodes
	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	tc, cleanup := testctx.PrepareTest(ctx, t, 10,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(1)
	workerNodeToCordon := nodesToCordon[0]
	Logger.Debugf("🚫 Cordoned worker node: %s", workerNodeToCordon)

	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	verifier := podgang.NewVerifier(tc.Client, Logger)
	pcsNsName := types.NamespacedName{Namespace: tc.Namespace, Name: "workload1"}

	Logger.Info("4. While pods are pending, verify PodGang conditions Initialized=True, Scheduled=False, Ready=False with timestamps unset")
	if err := podgang.WaitUntilVerified(ctx, verifier, pcsNsName, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeInitialized, metav1.ConditionTrue),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionFalse),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionFalse),
		podgang.LastScheduledSetCheckFn(false),
		podgang.LastReadySetCheckFn(false),
	); err != nil {
		t.Fatalf("%v", err)
	}

	Logger.Info("5. Uncordon the node and verify all pods get scheduled")
	tc.UncordonNodesAndWaitForPods([]string{workerNodeToCordon}, expectedPods)

	// Verify that each pod is scheduled on a unique node, worker nodes have 150m memory
	// and workload pods requests 80m memory, so only 1 should fit per node
	Logger.Info("6. Verify that each pod is scheduled on a unique node")
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("7. Once scheduled, verify PodGang conditions Scheduled=True, Ready=True with timestamps set")
	if err := podgang.WaitUntilVerified(ctx, verifier, pcsNsName, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionTrue),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionTrue),
		podgang.LastScheduledSetCheckFn(true),
		podgang.LastReadySetCheckFn(true),
	); err != nil {
		t.Fatalf("%v", err)
	}

	Logger.Info("🎉 Gang-scheduling With Full Replicas test completed successfully!")
}

// Test_GS13_SimultaneousWakeWithMixedIdle verifies concurrent standalone and PCSG wake while another
// standalone clique remains idle. Depending on reconcile timing, the woken components may share the
// retained base anchor or receive distinct anchors; both paths must preserve membership and ordering.
func Test_GS13_SimultaneousWakeWithMixedIdle(t *testing.T) {
	const (
		pcsName       = "workload-idle-wake"
		idlePCLQName  = pcsName + "-0-idle"
		guardedName   = pcsName + "-0-guarded"
		workerName    = pcsName + "-0-worker"
		pcsgName      = pcsName + "-0-workers"
		prefillName   = pcsgName + "-0-prefill"
		decodeName    = pcsgName + "-0-decode"
		barrierWindow = 5 * time.Second
	)
	ctx := context.Background()
	tc, cleanup := testctx.PrepareTest(ctx, t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         pcsName,
			YAMLPath:     "../yaml/workload-idle-wake.yaml",
			Namespace:    "default",
			ExpectedPods: 0,
		}),
	)
	defer cleanup()

	Logger.Info("1. Deploy a PodCliqueSet whose standalone and scaling-group components are idle")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	wm := workload.NewWorkloadManager(tc.Client, Logger)
	if _, err := wm.WaitForPodClique(ctx, tc.Namespace, workerName, tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for idle PodClique: %v", err)
	}
	if _, err := wm.WaitForPCSG(ctx, tc.Namespace, pcsgName, tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for idle PodCliqueScalingGroup: %v", err)
	}
	if _, err := wm.WaitForPodClique(ctx, tc.Namespace, guardedName, tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for guarded PodClique: %v", err)
	}
	waitForNoPodGangs(t, ctx, tc, pcsName)
	waitForAllIdleBootstrap(t, ctx, tc, pcsName)

	Logger.Info("2. Verify main-resource and scale-subresource below-quorum updates are rejected")
	assertBelowQuorumRejected(t, ctx, tc, guardedName)

	Logger.Info("3. Wake the standalone PodClique and PodCliqueScalingGroup concurrently")
	scaleIdleComponents(t, ctx, tc, workerName, pcsgName, 1)
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for woken pods: %v", err)
	}
	firstWake := waitForWakeState(t, ctx, tc, pcsName, idlePCLQName, workerName, prefillName, decodeName)

	Logger.Info("4. Verify PCSG ownership remains enforced after removing the compatibility label")
	assertOwnerReferenceBlocksIndependentScale(t, ctx, tc, prefillName)

	Logger.Info("5. Hold old PodGangs and scale both components to zero")
	heldPodGangs := addPodGangFinalizers(t, ctx, tc, pcsName)
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods before scale-to-zero: %v", err)
	}
	originalUIDs := capturePodUIDs(pods)
	scaleIdleComponents(t, ctx, tc, workerName, pcsgName, 0)
	waitForRemovedMembershipAndTerminatingPodGangs(t, ctx, tc, pcsName, heldPodGangs)
	restartOperator(t, ctx, tc)
	assertBarrierRetainsWorkload(t, ctx, tc, originalUIDs, []string{workerName, prefillName, decodeName}, barrierWindow)

	Logger.Info("6. Release PodGang finalizers and verify the all-idle state converges")
	removePodGangFinalizers(t, ctx, tc, heldPodGangs)
	waitForAllIdle(t, ctx, tc, pcsName, prefillName, decodeName)

	Logger.Info("7. Wake both components again and verify epochs and PodGang names are fresh")
	scaleIdleComponents(t, ctx, tc, workerName, pcsgName, 1)
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for second wake: %v", err)
	}
	secondWake := waitForWakeState(t, ctx, tc, pcsName, idlePCLQName, workerName, prefillName, decodeName)
	for name := range secondWake.podGangNames {
		if firstWake.podGangNames.Has(name) {
			t.Fatalf("second wake reused old PodGang name %s", name)
		}
	}
}

type observedWakeState struct {
	podGangNames stringSet
}

type stringSet map[string]struct{}

func (s stringSet) Has(value string) bool {
	_, ok := s[value]
	return ok
}

func waitForWakeState(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName, idlePCLQName, workerName, prefillName, decodeName string) observedWakeState {
	t.Helper()
	var observed observedWakeState
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pgm := &grovecorev1alpha1.PodGangMap{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pcsName + "-0"}, pgm); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		var workerAnchor, pcsgAnchor *grovecorev1alpha1.PodGangEntry
		epochs := stringSet{}
		for i := range pgm.Spec.Entries {
			entry := &pgm.Spec.Entries[i]
			if epochs.Has(entry.Epoch) {
				return false, nil
			}
			epochs[entry.Epoch] = struct{}{}
			if entry.Role != grovecorev1alpha1.PodGangEntryRoleAnchor {
				continue
			}
			if _, carriesIdle := entry.PodCliques["idle"]; carriesIdle {
				return false, nil
			}
			if entry.PodCliques["worker"] == 1 {
				workerAnchor = entry
			}
			if indices := entry.PCSGReplicaIndices["workers"]; len(indices) == 1 && indices[0] == 0 {
				pcsgAnchor = entry
			}
		}
		if workerAnchor == nil || pcsgAnchor == nil {
			return false, nil
		}
		if workerAnchor != pcsgAnchor && workerAnchor.Epoch == pcsgAnchor.Epoch {
			return false, nil
		}
		if workerAnchor == pcsgAnchor {
			if len(workerAnchor.DependsOn) != 0 {
				return false, nil
			}
		} else {
			first, second := workerAnchor, pcsgAnchor
			firstEpoch, firstErr := strconv.ParseInt(first.Epoch, 10, 64)
			secondEpoch, secondErr := strconv.ParseInt(second.Epoch, 10, 64)
			if firstErr != nil || secondErr != nil {
				return false, nil
			}
			if firstEpoch > secondEpoch {
				first, second = second, first
			}
			if len(first.DependsOn) != 0 || !slices.Equal(second.DependsOn, []string{first.Epoch}) {
				return false, nil
			}
		}
		rnr := apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}
		workerPodGangName := apicommon.GenerateAnchorPodGangName(rnr, workerAnchor.Epoch)
		pcsgPodGangName := apicommon.GenerateAnchorPodGangName(rnr, pcsgAnchor.Epoch)
		prefillDependencies := []string(nil)
		if workerAnchor == pcsgAnchor {
			prefillDependencies = []string{workerName}
		}
		expectedPodGangs := map[string]string{
			workerName:  workerPodGangName,
			prefillName: pcsgPodGangName,
			decodeName:  pcsgPodGangName,
		}
		expectedDependencies := map[string][]string{
			workerName:  nil,
			prefillName: prefillDependencies,
			decodeName:  {prefillName},
		}
		for name, dependencies := range expectedDependencies {
			pclq := &grovecorev1alpha1.PodClique{}
			if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, pclq); err != nil {
				return false, client.IgnoreNotFound(err)
			}
			if pclq.Labels[apicommon.LabelPodGang] != expectedPodGangs[name] ||
				!slices.Equal(pclq.Spec.StartsAfter, dependencies) {
				return false, nil
			}
		}
		idle := &grovecorev1alpha1.PodClique{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: idlePCLQName}, idle); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if idle.Spec.Replicas != 0 || len(idle.Spec.StartsAfter) != 0 {
			return false, nil
		}
		podGangs := &groveschedulerv1alpha1.PodGangList{}
		if err := tc.Client.List(ctx, podGangs,
			client.InNamespace(tc.Namespace),
			client.MatchingLabels{apicommon.LabelPartOfKey: pcsName},
		); err != nil {
			return false, err
		}
		expectedNames := map[string]struct{}{workerPodGangName: {}, pcsgPodGangName: {}}
		if len(podGangs.Items) != len(expectedNames) {
			return false, nil
		}
		expectedGroups := map[string]int{
			workerName:  1,
			prefillName: 1,
			decodeName:  1,
		}
		for i := range podGangs.Items {
			if _, ok := expectedNames[podGangs.Items[i].Name]; !ok {
				return false, nil
			}
			for _, group := range podGangs.Items[i].Spec.PodGroups {
				wantRefs, ok := expectedGroups[group.Name]
				if !ok || len(group.PodReferences) != wantRefs {
					return false, nil
				}
				delete(expectedGroups, group.Name)
			}
		}
		if len(expectedGroups) != 0 {
			return false, nil
		}
		observed.podGangNames = expectedNames
		return true, nil
	}); err != nil {
		t.Fatalf("Wake state did not converge: %v", err)
	}
	return observed
}

func assertBelowQuorumRejected(t *testing.T, ctx context.Context, tc *testctx.TestContext, pclqName string) {
	t.Helper()
	pclq := &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: pclqName, Namespace: tc.Namespace}}
	err := tc.Client.Patch(ctx, pclq, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"replicas":1}}`)))
	if !apierrors.IsInvalid(err) {
		t.Fatalf("main-resource below-quorum update error = %v, want Invalid", err)
	}
	assertPCLQReplicas(t, ctx, tc, pclqName, 0)

	scale := &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: pclqName, Namespace: tc.Namespace},
		Spec:       autoscalingv1.ScaleSpec{Replicas: 1},
	}
	err = tc.Client.SubResource("scale").Update(ctx, pclq, client.WithSubResourceBody(scale))
	if !apierrors.IsInvalid(err) {
		t.Fatalf("scale-subresource below-quorum update error = %v, want Invalid", err)
	}
	assertPCLQReplicas(t, ctx, tc, pclqName, 0)
}

func assertOwnerReferenceBlocksIndependentScale(t *testing.T, ctx context.Context, tc *testctx.TestContext, pclqName string) {
	t.Helper()
	pclq := &grovecorev1alpha1.PodClique{}
	key := client.ObjectKey{Namespace: tc.Namespace, Name: pclqName}
	if err := tc.Client.Get(ctx, key, pclq); err != nil {
		t.Fatalf("Failed to get PCSG-owned PodClique: %v", err)
	}
	ownerLabel := pclq.Labels[apicommon.LabelPodCliqueScalingGroup]
	delete(pclq.Labels, apicommon.LabelPodCliqueScalingGroup)
	if err := tc.Client.Update(ctx, pclq); err != nil {
		t.Fatalf("Failed to remove PCSG owner label: %v", err)
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &grovecorev1alpha1.PodClique{}
		if err := tc.Client.Get(ctx, key, current); err != nil {
			return err
		}
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations["e2e.grove.io/metadata-update"] = "allowed"
		current.Finalizers = append(current.Finalizers, "e2e.grove.io/metadata-update")
		return tc.Client.Update(ctx, current)
	}); err != nil {
		t.Fatalf("Metadata-only update on PCSG-owned PodClique was rejected: %v", err)
	}
	defer func() {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &grovecorev1alpha1.PodClique{}
			if err := tc.Client.Get(ctx, key, current); err != nil {
				return client.IgnoreNotFound(err)
			}
			current.Finalizers = slices.DeleteFunc(current.Finalizers, func(value string) bool {
				return value == "e2e.grove.io/metadata-update"
			})
			current.Labels[apicommon.LabelPodCliqueScalingGroup] = ownerLabel
			return tc.Client.Update(ctx, current)
		}); err != nil {
			t.Errorf("Failed to restore PCSG-owned PodClique metadata: %v", err)
		}
	}()

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &grovecorev1alpha1.PodClique{}
		if err := tc.Client.Get(ctx, key, current); err != nil {
			return err
		}
		return tc.Client.Update(ctx, current)
	}); err != nil {
		t.Fatalf("Replicas no-op update on PCSG-owned PodClique was rejected: %v", err)
	}
	if err := tc.Client.Get(ctx, key, pclq); err != nil {
		t.Fatalf("Failed to refresh PCSG-owned PodClique: %v", err)
	}
	noOpScale := &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: pclqName, Namespace: tc.Namespace},
		Spec:       autoscalingv1.ScaleSpec{Replicas: pclq.Spec.Replicas},
	}
	if err := tc.Client.SubResource("scale").Update(ctx, pclq, client.WithSubResourceBody(noOpScale)); err != nil {
		t.Fatalf("Replicas no-op scale on PCSG-owned PodClique was rejected: %v", err)
	}
	currentScale := &autoscalingv1.Scale{}
	if err := tc.Client.SubResource("scale").Get(ctx, pclq, currentScale); err != nil {
		t.Fatalf("Failed to get scale for PCSG-owned PodClique: %v", err)
	}
	if currentScale.Status.Selector != "" {
		t.Fatalf("PCSG-owned PodClique published autoscaler selector %q", currentScale.Status.Selector)
	}

	err := retry.OnError(retry.DefaultRetry, apierrors.IsConflict, func() error {
		current := &grovecorev1alpha1.PodClique{}
		if err := tc.Client.Get(ctx, key, current); err != nil {
			return err
		}
		current.Spec.Replicas = 0
		return tc.Client.Update(ctx, current)
	})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("PCSG-owned PodClique main update error = %v, want Forbidden", err)
	}
	scale := &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: pclqName, Namespace: tc.Namespace},
		Spec:       autoscalingv1.ScaleSpec{Replicas: 0},
	}
	err = tc.Client.SubResource("scale").Update(ctx, pclq, client.WithSubResourceBody(scale))
	if !apierrors.IsForbidden(err) {
		t.Fatalf("PCSG-owned PodClique scale error = %v, want Forbidden", err)
	}
	assertPCLQReplicas(t, ctx, tc, pclqName, 1)
}

func assertPCLQReplicas(t *testing.T, ctx context.Context, tc *testctx.TestContext, name string, expected int32) {
	t.Helper()
	pclq := &grovecorev1alpha1.PodClique{}
	if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, pclq); err != nil {
		t.Fatalf("Failed to get PodClique %s: %v", name, err)
	}
	if pclq.Spec.Replicas != expected {
		t.Fatalf("PodClique %s replicas = %d, want %d", name, pclq.Spec.Replicas, expected)
	}
}

func scaleIdleComponents(t *testing.T, ctx context.Context, tc *testctx.TestContext, workerName, pcsgName string, replicas int32) {
	t.Helper()
	errCh := make(chan error, 2)
	go func() {
		errCh <- tc.Client.Patch(ctx,
			&grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: workerName, Namespace: tc.Namespace}},
			client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))))
	}()
	go func() {
		errCh <- tc.Client.Patch(ctx,
			&grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: pcsgName, Namespace: tc.Namespace}},
			client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))))
	}()
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("Failed to scale idle component to %d: %v", replicas, err)
		}
	}
}

func addPodGangFinalizers(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName string) []client.ObjectKey {
	t.Helper()
	podGangs := listPodGangs(t, ctx, tc, pcsName)
	if len(podGangs.Items) == 0 {
		t.Fatal("no PodGangs available for deletion barrier")
	}
	keys := make([]client.ObjectKey, 0, len(podGangs.Items))
	for i := range podGangs.Items {
		podGang := &podGangs.Items[i]
		podGang.Finalizers = append(podGang.Finalizers, "e2e.grove.io/hold")
		if err := tc.Client.Update(ctx, podGang); err != nil {
			t.Fatalf("Failed to add finalizer to PodGang %s: %v", podGang.Name, err)
		}
		keys = append(keys, client.ObjectKeyFromObject(podGang))
	}
	return keys
}

func waitForRemovedMembershipAndTerminatingPodGangs(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName string, podGangKeys []client.ObjectKey) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pgm := &grovecorev1alpha1.PodGangMap{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pcsName + "-0"}, pgm); err != nil {
			return false, err
		}
		for i := range pgm.Spec.Entries {
			if pgm.Spec.Entries[i].PodCliques["worker"] > 0 ||
				len(pgm.Spec.Entries[i].PCSGReplicaIndices["workers"]) > 0 {
				return false, nil
			}
		}
		for _, key := range podGangKeys {
			podGang := &groveschedulerv1alpha1.PodGang{}
			if err := tc.Client.Get(ctx, key, podGang); err != nil {
				return false, err
			}
			if podGang.DeletionTimestamp == nil {
				return false, nil
			}
		}
		return true, nil
	}); err != nil {
		t.Fatalf("Scale-to-zero did not reach deletion barrier: %v", err)
	}
}

func assertBarrierRetainsWorkload(t *testing.T, ctx context.Context, tc *testctx.TestContext, originalUIDs map[types.UID]struct{}, pclqNames []string, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for {
		pods, err := tc.ListPods()
		if err != nil {
			t.Fatalf("Failed to list pods during barrier observation: %v", err)
		}
		currentUIDs := capturePodUIDs(pods)
		for uid := range originalUIDs {
			if !currentUIDsHas(currentUIDs, uid) {
				t.Fatalf("Pod UID %s was deleted before old PodGang removal", uid)
			}
		}
		for _, name := range pclqNames {
			if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, &grovecorev1alpha1.PodClique{}); err != nil {
				t.Fatalf("PodClique %s was deleted before old PodGang removal: %v", name, err)
			}
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func currentUIDsHas(uids map[types.UID]struct{}, uid types.UID) bool {
	_, ok := uids[uid]
	return ok
}

func removePodGangFinalizers(t *testing.T, ctx context.Context, tc *testctx.TestContext, keys []client.ObjectKey) {
	t.Helper()
	for _, key := range keys {
		podGang := &groveschedulerv1alpha1.PodGang{}
		if err := tc.Client.Get(ctx, key, podGang); err != nil {
			t.Fatalf("Failed to get held PodGang %s: %v", key.Name, err)
		}
		podGang.Finalizers = nil
		if err := tc.Client.Update(ctx, podGang); err != nil {
			t.Fatalf("Failed to release PodGang %s: %v", key.Name, err)
		}
	}
}

func waitForAllIdle(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName, prefillName, decodeName string) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pods, err := tc.ListPods()
		if err != nil || len(pods.Items) != 0 {
			return false, err
		}
		if len(listPodGangs(t, ctx, tc, pcsName).Items) != 0 {
			return false, nil
		}
		for _, name := range []string{prefillName, decodeName} {
			err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, &grovecorev1alpha1.PodClique{})
			if err == nil || !apierrors.IsNotFound(err) {
				return false, err
			}
		}
		return true, nil
	}); err != nil {
		t.Fatalf("All-idle state did not converge: %v", err)
	}
}

func waitForNoPodGangs(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName string) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		return len(listPodGangs(t, ctx, tc, pcsName).Items) == 0, nil
	}); err != nil {
		t.Fatalf("PodGangs remained for all-idle workload: %v", err)
	}
}

func listPodGangs(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName string) *groveschedulerv1alpha1.PodGangList {
	t.Helper()
	podGangs := &groveschedulerv1alpha1.PodGangList{}
	if err := tc.Client.List(ctx, podGangs,
		client.InNamespace(tc.Namespace),
		client.MatchingLabels{apicommon.LabelPartOfKey: pcsName},
	); err != nil {
		t.Fatalf("Failed to list PodGangs: %v", err)
	}
	return podGangs
}

// Test_GS2_GangSchedulingWithScalingFullReplicas verifies gang-scheduling behavior when scaling a PodCliqueScalingGroup
// Scenario GS-2:
// 1. Initialize a 14-node Grove cluster, then cordon 5 nodes
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node to allow scheduling and verify pods get scheduled
// 5. Wait for pods to become ready
// 6. Scale PCSG replicas to 3 and verify 4 new pending pods
// 7. Uncordon remaining nodes and verify all pods get scheduled
func Test_GS2_GangSchedulingWithScalingFullReplicas(t *testing.T) {
	ctx := context.Background()

	// Setup cluster (shared or individual based on test run mode)
	Logger.Info("1. Initialize a 14-node Grove cluster, then cordon 5 nodes")

	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(5)

	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	expectedPods := 10
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	Logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	Logger.Info("5. Wait for pods to become ready")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[:1], expectedPods)

	Logger.Info("6. Scale PCSG replicas to 3 and verify 4 new pending pods")
	pcsgName := "workload1-0-sg-x"
	if err := tc.ScalePCSG(pcsgName, 3); err != nil {
		t.Fatalf("Failed to scale PodCliqueScalingGroup %s: %v", pcsgName, err)
	}

	expectedScaledPods := 14
	_, err = tc.WaitForPodCount(expectedScaledPods)
	if err != nil {
		t.Fatalf("Failed to wait for scaled pods to be created: %v", err)
	}

	if err := tc.WaitForPodCountAndPhases(expectedScaledPods, expectedPods, 4); err != nil {
		t.Fatalf("Pod phase verification failed: %v", err)
	}

	Logger.Info("7. Uncordon remaining nodes and verify all pods get scheduled")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[1:], expectedScaledPods)

	// Verify that each pod is scheduled on a unique node
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCSG scaling test completed successfully!")
}

// TestGangSchedulingWithPCSScalingFullReplicas verifies gang-scheduling behavior when scaling a PodCliqueSet
// Scenario GS-3:
// 1. Initialize a 20-node Grove cluster, then cordon 11 nodes
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node to allow scheduling and verify pods get scheduled
// 5. Wait for pods to become ready
// 6. Scale PCS replicas to 2 and verify 10 new pending pods
// 7. Uncordon remaining nodes and verify all pods get scheduled
func Test_GS3_GangSchedulingWithPCSScalingFullReplicas(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 20-node Grove cluster, then cordon 11 nodes")
	tc, cleanup := testctx.PrepareTest(ctx, t, 20,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(11)

	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	// workloadNamespace set via tc.Namespace
	expectedPods := 10
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	Logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	Logger.Info("5. Wait for pods to become ready")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[:1], expectedPods)

	Logger.Info("6. Scale PCS replicas to 2 and verify 10 new pending pods")
	pcsName := "workload1"
	replicas := int32(2)
	expectedScaledPods := int(replicas) * expectedPods
	tc.ScalePCSAndWait(pcsName, replicas, expectedScaledPods, expectedPods)

	expectedNewPending := expectedScaledPods - expectedPods
	if err := tc.WaitForPodCountAndPhases(expectedScaledPods, expectedPods, expectedNewPending); err != nil {
		t.Fatalf("Failed to wait for scaled pods with expected phases: %v", err)
	}

	Logger.Info("7. Uncordon remaining nodes and verify all pods get scheduled")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[1:], expectedScaledPods)

	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS scaling test completed successfully!")
}

// Test_GS4_GangSchedulingWithPCSAndPCSGScalingFullReplicas verifies gang scheduling while scaling both PodCliqueSet and PodCliqueScalingGroup replicas
// Scenario GS-4:
// 1. Initialize a 28-node Grove cluster, then cordon 19 nodes
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node to allow scheduling and verify pods get scheduled
// 5. Wait for pods to become ready
// 6. Scale PCSG replicas to 3 and verify 4 new pending pods
// 7. Uncordon 4 nodes and verify scaled pods get scheduled
// 8. Scale PCS replicas to 2 and verify 10 new pending pods
// 9. Scale PCSG replicas to 3 and verify 4 new pending pods
// 10. Uncordon remaining nodes and verify all pods get scheduled
func Test_GS4_GangSchedulingWithPCSAndPCSGScalingFullReplicas(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster, then cordon 19 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(19)

	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	// workloadNamespace set via tc.Namespace
	expectedPods := 10
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	Logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	Logger.Info("5. Wait for pods to become ready")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[:1], expectedPods)

	Logger.Info("6. Scale PCSG replicas to 3 and verify 4 new pending pods")
	pcsgName := "workload1-0-sg-x"
	tc.ScalePCSGInstanceAndWait(pcsgName, 3, 14, 4)

	Logger.Info("7. Uncordon 4 nodes and verify scaled pods get scheduled")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[1:5], 14)

	Logger.Info("8. Scale PCS replicas to 2 and verify 10 new pending pods")
	tc.ScalePCSAndWait("workload1", 2, 24, 10)
	tc.UncordonNodesAndWaitForPods(nodesToCordon[5:15], 24)

	Logger.Info("9. Scale PCSG replicas to 3 and verify 4 new pending pods")
	secondReplicaPCSGName := "workload1-1-sg-x"
	tc.ScalePCSGInstanceAndWait(secondReplicaPCSGName, 3, 28, 4)

	Logger.Info("10. Uncordon remaining nodes and verify all pods get scheduled")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[15:19], 28)

	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

// Test_GS5_GangSchedulingWithMinReplicas tests gang-scheduling behavior with min-replicas
// Scenario GS-5:
// 1. Initialize a 10-node Grove cluster, then cordon 8 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 5. Wait for scheduled pods to become ready
// 6. Uncordon 7 nodes and verify all remaining workload pods get scheduled
func Test_GS5_GangSchedulingWithMinReplicas(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 10-node Grove cluster, then cordon 8 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 10,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(8)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	// workloadNamespace set via tc.Namespace
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	tc.UncordonNodes(nodesToCordon[:1])

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := tc.WaitForPodPhases(3, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	Logger.Info("5. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	Logger.Info("6. Uncordon 7 nodes and verify all remaining workload pods get scheduled")
	tc.UncordonNodes(nodesToCordon[1:])

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling min-replicas test (GS-5) completed successfully!")
}

// Test_GS6_GangSchedulingWithPCSGScalingMinReplicas tests gang-scheduling behavior with PCSG scaling and min-replicas
// Scenario GS-6:
// 1. Initialize a 14-node Grove cluster, then cordon 12 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 5. Wait for scheduled pods to become ready
// 6. Uncordon 7 nodes and verify the remaining workload pods get scheduled
// 7. Wait for scheduled pods to become ready
// 8. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods
// 9. Verify all newly created pods are pending due to insufficient resources
// 10. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})
// 11. Wait for scheduled pods to become ready
// 12. Uncordon 2 nodes and verify remaining workload pods get scheduled
func Test_GS6_GangSchedulingWithPCSGScalingMinReplicas(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(12)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	// Based on workload2 min-replicas: pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1}
	tc.UncordonNodes(nodesToCordon[:1])

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := tc.WaitForPodPhases(3, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	Logger.Info("5. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	Logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	sevenNodesToUncordon := nodesToCordon[1:8]
	tc.UncordonNodes(sevenNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	Logger.Info("8. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	// Scale PCSG sg-x to 3 replicas and verify 4 newly created pods
	pcsgName := "workload2-0-sg-x"
	// Expected total pods after scaling: 10 (initial) + 4 (new from scaling sg-x from 2 to 3) = 14
	expectedPodsAfterScaling := 14
	expectedNewPendingPods := 4

	tc.ScalePCSGInstanceAndWait(pcsgName, 3, expectedPodsAfterScaling, expectedNewPendingPods)

	Logger.Info("9. Verify all newly created pods are pending due to insufficient resources")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(false, 4); err != nil {
		t.Fatalf("Failed to verify all pending pods have Unschedulable events: %v", err)
	}

	Logger.Info("10. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})")
	// Uncordon 2 nodes and verify exactly 2 more pods get scheduled
	// pcs-0-{sg-x-2-pc-b = 1, sg-x-2-pc-c = 1} (min-replicas for the new PCSG replica)
	twoNodesToUncordon := nodesToCordon[8:10]
	tc.UncordonNodes(twoNodesToUncordon)

	// Wait for exactly 2 more pods to be scheduled (min-replicas for new PCSG replica)
	if err := tc.WaitForPodPhases(12, 2); err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled after PCSG scaling: %v", err)
	}

	Logger.Info("11. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(12); err != nil {
		t.Fatalf("Failed to wait for 12 pods to become ready: %v", err)
	}

	Logger.Info("12. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	// Uncordon remaining 2 nodes and verify all remaining workload pods get scheduled
	remainingNodesToUncordon := nodesToCordon[10:12]
	tc.UncordonNodes(remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(14); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCSG scaling min-replicas test (GS-6) completed successfully!")
}

// Test_GS7_GangSchedulingWithPCSGScalingMinReplicasAdvanced1 tests advanced gang-scheduling behavior with PCSG scaling and min-replicas
// Scenario GS-7:
// 1. Initialize a 14-node Grove cluster, then cordon 12 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 5. Wait for scheduled pods to become ready
// 6. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-1-pc-b=1, sg-x-1-pc-c=1})
// 7. Wait for scheduled pods to become ready
// 8. Uncordon 5 nodes and verify the remaining workload pods get scheduled
// 9. Wait for scheduled pods to become ready
// 10. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods
// 11. Verify all newly created pods are pending due to insufficient resources
// 12. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})
// 13. Wait for scheduled pods to become ready
// 14. Uncordon 2 nodes and verify remaining workload pods get scheduled
func Test_GS7_GangSchedulingWithPCSGScalingMinReplicasAdvanced1(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(12)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	pods, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := tc.UncordonNode(firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := tc.WaitForPodPhases(3, len(pods.Items)-3); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	Logger.Info("5. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	Logger.Info("6. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-1-pc-b=1, sg-x-1-pc-c=1})")
	twoNodesToUncordon := nodesToCordon[1:3]
	tc.UncordonNodes(twoNodesToUncordon)

	// Wait for exactly 2 more pods to be scheduled (sg-x-1 min-replicas)
	if err := tc.WaitForPodPhases(5, len(pods.Items)-5); err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled: %v", err)
	}

	Logger.Info("7. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(5); err != nil {
		t.Fatalf("Failed to wait for 5 scheduled pods to become ready: %v", err)
	}

	Logger.Info("8. Uncordon 5 nodes and verify the remaining workload pods get scheduled")
	fiveNodesToUncordon := nodesToCordon[3:8]
	tc.UncordonNodes(fiveNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Verify all 10 initial pods are running
	pods, err = tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	Logger.Info("9. Wait for scheduled pods to become ready (already verified above)")
	Logger.Info("11. Verify all newly created pods are pending due to insufficient resources (verified in scalePCSGInstanceAndWait)")
	Logger.Info("10. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	pcsgName := "workload2-0-sg-x"
	expectedPodsAfterScaling := 14
	expectedNewPendingPods := 4
	tc.ScalePCSGInstanceAndWait(pcsgName, 3, expectedPodsAfterScaling, expectedNewPendingPods)

	Logger.Info("12. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})")
	twoMoreNodesToUncordon := nodesToCordon[8:10]
	tc.UncordonNodes(twoMoreNodesToUncordon)

	// Wait for exactly 2 more pods to be scheduled (min-replicas for new PCSG replica)
	if err := tc.WaitForPodPhases(12, 2); err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled after PCSG scaling: %v", err)
	}

	Logger.Info("13. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(12); err != nil {
		t.Fatalf("Failed to wait for 12 pods to become ready: %v", err)
	}

	Logger.Info("14. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[10:12]
	tc.UncordonNodes(remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(14); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCSG scaling min-replicas advanced1 test (GS-7) completed successfully! All workload pods transitioned correctly through advanced PCSG scaling with min-replicas.")
}

// TestGangSchedulingWithPCSGScalingMinReplicasAdvanced2 tests advanced gang-scheduling behavior with early PCSG scaling and min-replicas
// Scenario GS-8:
// 1. Initialize a 14-node Grove cluster, then cordon 12 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Set pcs-0-sg-x resource replicas equal to 3, verify 4 more newly created pods
// 5. Verify all 14 newly created pods are pending due to insufficient resources
// 6. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 7. Wait for scheduled pods to become ready
// 8. Uncordon 4 nodes and verify 4 more pods get scheduled (pcs-0-{sg-x-1-pc-b=1, sg-x-1-pc-c=1}, pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})
// 9. Wait for scheduled pods to become ready
// 10. Uncordon 7 nodes and verify the remaining workload pods get scheduled
func Test_GS8_GangSchedulingWithPCSGScalingMinReplicasAdvanced2(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(12)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Set pcs-0-sg-x resource replicas equal to 3, verify 4 more newly created pods")
	pcsgName := "workload2-0-sg-x"
	expectedPodsAfterScaling := 14
	tc.ScalePCSGInstanceAndWait(pcsgName, 3, expectedPodsAfterScaling, expectedPodsAfterScaling)

	Logger.Info("5. Verify all 14 newly created pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("6. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := tc.UncordonNode(firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	// expectedPodsAfterScaling is 14, so 14-3 = 11 pending
	if err := tc.WaitForPodPhases(3, 11); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	Logger.Info("7. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	Logger.Info("8. Uncordon 4 nodes and verify 4 more pods get scheduled")
	fourNodesToUncordon := nodesToCordon[1:5]
	tc.UncordonNodes(fourNodesToUncordon)

	// Wait for exactly 4 more pods to be scheduled (sg-x-1 and sg-x-2 min-replicas)
	// Total is 14, so 14-7 = 7 pending
	if err := tc.WaitForPodPhases(7, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 4 more pods to be scheduled: %v", err)
	}

	Logger.Info("9. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(7); err != nil {
		t.Fatalf("Failed to wait for 7 scheduled pods to become ready: %v", err)
	}

	Logger.Info("10. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[5:]
	tc.UncordonNodes(remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(14); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

// TestGangSchedulingWithPCSScalingMinReplicas tests gang-scheduling behavior with PodCliqueSet scaling and min-replicas
// Scenario GS-9:
// 1. Initialize a 20-node Grove cluster, then cordon 18 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 5. Wait for scheduled pods to become ready
// 6. Uncordon 7 nodes and verify the remaining workload pods get scheduled
// 7. Wait for scheduled pods to become ready
// 8. Set PCS resource replicas equal to 2, then verify 10 more newly created pods
// 9. Uncordon 3 nodes and verify another 3 pods get scheduled (pcs-1-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 10. Wait for scheduled pods to become ready
// 11. Uncordon 7 nodes and verify the remaining workload pods get scheduled
func Test_GS9_GangSchedulingWithPCSScalingMinReplicas(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 20-node Grove cluster, then cordon 18 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 20,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(18)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	pods, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := tc.UncordonNode(firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := tc.WaitForPodPhases(3, len(pods.Items)-3); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	Logger.Info("5. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	Logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	Logger.Info("7. Wait for scheduled pods to become ready")
	sevenNodesToUncordon := nodesToCordon[1:8]
	tc.UncordonNodes(sevenNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	Logger.Info("8. Set PCS resource replicas equal to 2, then verify 10 more newly created pods")
	// Scale PodCliqueSet to 2 replicas and verify 10 more newly created pods
	pcsName := "workload2"

	// Expected total pods after scaling: 10 (initial) + 10 (new from scaling PCS from 1 to 2) = 20
	expectedPodsAfterScaling := 20
	expectedNewPendingPods := 10
	tc.ScalePCSAndWait(pcsName, 2, expectedPodsAfterScaling, expectedNewPendingPods)

	Logger.Info("9. Uncordon 3 nodes and verify another 3 pods get scheduled (pcs-1-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	threeNodesToUncordon := nodesToCordon[8:11]
	tc.UncordonNodes(threeNodesToUncordon)

	// Wait for exactly 3 more pods to be scheduled (min-replicas for new PCS replica)
	if err := tc.WaitForPodPhases(13, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 3 more pods to be scheduled after PCS scaling: %v", err)
	}

	Logger.Info("10. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(13); err != nil {
		t.Fatalf("Failed to wait for 13 pods to become ready: %v", err)
	}

	Logger.Info("11. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[11:18]
	tc.UncordonNodes(remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(20); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 20 pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

// Test_GS10_GangSchedulingWithPCSScalingMinReplicasAdvanced tests advanced gang-scheduling behavior with early PCS scaling and min-replicas
// Scenario GS-10:
// 1. Initialize a 20-node Grove cluster, then cordon 18 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Set PCS resource replicas equal to 2, then verify 10 more newly created pods
// 5. Verify all 20 newly created pods are pending due to insufficient resources
// 6. Uncordon 4 nodes and verify a total of 6 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1}, pcs-1-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 7. Wait for scheduled pods to become ready
// 8. Uncordon 4 nodes and verify 4 more pods get scheduled (pcs-0-{sg-x-1-pc-b=1, sg-x-1-pc-c=1}, pcs-1-{sg-x-1-pc-b=1, sg-x-1-pc-c=1})
// 9. Wait for scheduled pods to become ready
// 10. Uncordon 10 nodes and verify the remaining workload pods get scheduled
func Test_GS10_GangSchedulingWithPCSScalingMinReplicasAdvanced(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 20-node Grove cluster, then cordon 18 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 20,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(18)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Set PCS resource replicas equal to 2, then verify 10 more newly created pods")
	pcsName := "workload2"

	// Expected total pods after scaling: 10 (initial) + 10 (new from scaling PCS from 1 to 2) = 20
	expectedPodsAfterScaling := 20
	tc.ScalePCSAndWait(pcsName, 2, expectedPodsAfterScaling, expectedPodsAfterScaling)

	Logger.Info("5. Verify all 20 newly created pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("6. Uncordon 4 nodes and verify a total of 6 pods get scheduled")
	fourNodesToUncordon := nodesToCordon[0:4]
	tc.UncordonNodes(fourNodesToUncordon)

	// Wait for exactly 6 pods to be scheduled (min-replicas for both PCS replicas)
	// expectedPodsAfterScaling is 20, so 20-6 = 14 pending
	if err := tc.WaitForPodPhases(6, 14); err != nil {
		t.Fatalf("Failed to wait for exactly 6 pods to be scheduled: %v", err)
	}

	Logger.Info("7. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(6); err != nil {
		t.Fatalf("Failed to wait for 6 scheduled pods to become ready: %v", err)
	}

	Logger.Info("8. Uncordon 4 nodes and verify 4 more pods get scheduled")
	fourMoreNodesToUncordon := nodesToCordon[4:8]
	tc.UncordonNodes(fourMoreNodesToUncordon)

	// Wait for exactly 4 more pods to be scheduled (sg-x-1 for both PCS replicas)
	// Total is 20, so 20-10 = 10 pending
	if err := tc.WaitForPodPhases(10, 10); err != nil {
		t.Fatalf("Failed to wait for exactly 4 more pods to be scheduled: %v", err)
	}

	Logger.Info("9. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(10); err != nil {
		t.Fatalf("Failed to wait for 10 scheduled pods to become ready: %v", err)
	}

	Logger.Info("10. Uncordon 10 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[8:18]
	tc.UncordonNodes(remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(20); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 20 pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

// Test_GS11_GangSchedulingWithPCSAndPCSGScalingMinReplicas tests gang-scheduling behavior with both PCS and PCSG scaling using min-replicas
// Scenario GS-11:
// 1. Initialize a 28-node Grove cluster, then cordon 26 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node
// 5. Wait for min-replicas pods to be scheduled and ready (should be 3 pods for min-available)
// 6. Uncordon 7 nodes and verify the remaining workload pods get scheduled
// 7. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods
// 8. Verify all newly created pods are pending due to insufficient resources
// 9. Uncordon 2 nodes
// 10. Wait for 2 more pods to be scheduled and ready (min-available for sg-x-2)
// 11. Uncordon 2 nodes and verify remaining workload pods get scheduled
// 12. Set pcs resource replicas equal to 2, then verify 10 more newly created pods
// 13. Uncordon 3 nodes
// 14. Wait for 3 more pods to be scheduled (min-available for pcs-1)
// 15. Uncordon 7 nodes and verify the remaining workload pods get scheduled
// 16. Set pcs-1-sg-x resource replicas equal to 3, then verify 4 newly created pods
// 17. Verify all newly created pods are pending due to insufficient resources
// 18. Uncordon 2 nodes
// 19. Wait for 2 more pods to be scheduled (min-available for pcs-1-sg-x-2)
// 20. Uncordon 2 nodes and verify remaining workload pods get scheduled
func Test_GS11_GangSchedulingWithPCSAndPCSGScalingMinReplicas(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster, then cordon 26 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(26)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Uncordon 1 node")
	firstNodeToUncordon := nodesToCordon[0]
	if err := tc.UncordonNode(firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	Logger.Info("5. Wait for min-replicas pods to be scheduled and ready (should be 3 pods for min-available)")
	if err := tc.WaitForRunningPods(3); err != nil {
		t.Fatalf("Failed to wait for min-replicas pods to be scheduled: %v", err)
	}

	Logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesFirstWave := nodesToCordon[1:8]
	tc.UncordonNodes(remainingNodesFirstWave)

	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for first wave pods to be ready: %v", err)
	}

	Logger.Info("7. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	pcsgName := "workload2-0-sg-x"
	tc.ScalePCSGInstanceAndWait(pcsgName, 3, 14, 4)

	Logger.Info("8. Verify all newly created pods are pending due to insufficient resources")
	expectedRunning := 10 // Initial 10 pods from first wave
	expectedPending := 4  // 4 new pods from PCSG scaling
	if err := tc.WaitForPodCountAndPhases(14, expectedRunning, expectedPending); err != nil {
		t.Fatalf("Failed to verify newly created pods are pending: %v", err)
	}

	Logger.Info("9. Uncordon 2 nodes")
	remainingNodesSecondWave := nodesToCordon[8:10]
	tc.UncordonNodes(remainingNodesSecondWave)

	Logger.Info("10. Wait for 2 more pods to be scheduled and ready (min-available for sg-x-2)")
	if err := tc.WaitForRunningPods(12); err != nil {
		t.Fatalf("Failed to wait for PCSG partial scheduling: %v", err)
	}

	Logger.Info("11. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	remainingNodesThirdWave := nodesToCordon[10:12]
	tc.UncordonNodes(remainingNodesThirdWave)

	if err := tc.WaitForPods(14); err != nil {
		t.Fatalf("Failed to wait for PCSG completion pods to be ready: %v", err)
	}

	Logger.Info("12. Set pcs resource replicas equal to 2, then verify 10 more newly created pods")
	tc.ScalePCSAndWait("workload2", 2, 24, 10)

	Logger.Info("13. Uncordon 3 nodes")
	remainingNodesFourthWave := nodesToCordon[12:15]
	tc.UncordonNodes(remainingNodesFourthWave)

	Logger.Info("14. Wait for 3 more pods to be scheduled (min-available for pcs-1)")
	if err := tc.WaitForRunningPods(17); err != nil {
		t.Fatalf("Failed to wait for PCS partial scheduling: %v", err)
	}

	Logger.Info("15. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesFifthWave := nodesToCordon[15:22]
	tc.UncordonNodes(remainingNodesFifthWave)

	if err := tc.WaitForPods(24); err != nil {
		t.Fatalf("Failed to wait for PCS completion pods to be ready: %v", err)
	}

	Logger.Info("16. Set pcs-1-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	secondReplicaPCSGName := "workload2-1-sg-x"
	tc.ScalePCSGInstanceAndWait(secondReplicaPCSGName, 3, 28, 4)

	Logger.Info("17. Verify all newly created pods are pending due to insufficient resources")
	expectedRunning = 24 // All previous pods should be running
	expectedPending = 4  // 4 new pods from second PCSG scaling
	if err := tc.WaitForPodCountAndPhases(28, expectedRunning, expectedPending); err != nil {
		t.Fatalf("Failed to verify newly created pods are pending after second PCSG scaling: %v", err)
	}

	Logger.Info("18. Uncordon 2 nodes")
	remainingNodesSixthWave := nodesToCordon[22:24]
	tc.UncordonNodes(remainingNodesSixthWave)

	Logger.Info("19. Wait for 2 more pods to be scheduled (min-available for pcs-1-sg-x-2)")
	if err := tc.WaitForRunningPods(26); err != nil {
		t.Fatalf("Failed to wait for final PCSG partial scheduling: %v", err)
	}

	Logger.Info("20. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	finalNodes := nodesToCordon[24:26]
	tc.UncordonNodes(finalNodes)

	if err := tc.WaitForPods(28); err != nil {
		t.Fatalf("Failed to wait for all final pods to be ready: %v", err)
	}

	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")

}

// Test_GS12_GangSchedulingWithComplexPCSGScaling tests gang-scheduling behavior with complex PCSG scaling operations
// Scenario GS-12:
// 1. Initialize a 28-node Grove cluster, then cordon 26 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Set pcs resource replicas equal to 2, then verify 10 more newly created pods
// 5. Verify all 20 newly created pods are pending due to insufficient resources
// 6. Set both pcs-0-sg-x and pcs-1-sg-x resource replicas equal to 3, verify 8 newly created pods
// 7. Verify all 28 created pods are pending due to insufficient resources
// 8. Uncordon 4 nodes and verify a total of 6 pods get scheduled (pcs-0 and pcs-1 min-available)
// 9. Wait for scheduled pods to become ready
// 10. Uncordon 8 nodes and verify 8 more pods get scheduled (remaining PCSG pods)
// 11. Wait for scheduled pods to become ready
// 12. Uncordon 14 nodes and verify the remaining workload pods get scheduled
func Test_GS12_GangSchedulingWithComplexPCSGScaling(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster, then cordon 26 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(26)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Set pcs resource replicas equal to 2, then verify 10 more newly created pods")
	tc.ScalePCSAndWait("workload2", 2, 20, 20)

	Logger.Info("5. Verify all 20 newly created pods are pending due to insufficient resources")
	if err := tc.WaitForPodCountAndPhases(20, 0, 20); err != nil {
		t.Fatalf("Failed to verify all 20 pods are pending: %v", err)
	}

	Logger.Info("6. Set both pcs-0-sg-x and pcs-1-sg-x resource replicas equal to 3, verify 8 newly created pods")

	pcsg1Name := "workload2-0-sg-x"
	tc.ScalePCSGInstanceAndWait(pcsg1Name, 3, 24, 24)

	pcsg2Name := "workload2-1-sg-x"
	tc.ScalePCSGInstanceAndWait(pcsg2Name, 3, 28, 28)

	Logger.Info("7. Verify all 28 created pods are pending due to insufficient resources")
	if err := tc.WaitForPodCountAndPhases(28, 0, 28); err != nil {
		t.Fatalf("Failed to verify all 28 pods are pending: %v", err)
	}

	Logger.Info("8. Uncordon 4 nodes and verify a total of 6 pods get scheduled (pcs-0 and pcs-1 min-available)")
	firstWaveNodes := nodesToCordon[:4]
	tc.UncordonNodes(firstWaveNodes)

	if err := tc.WaitForRunningPods(6); err != nil {
		t.Fatalf("Failed to wait for 6 pods to be scheduled: %v", err)
	}

	Logger.Info("9. Wait for scheduled pods to become ready (only the 6 that are scheduled)")
	if err := tc.WaitForReadyPods(6); err != nil {
		t.Fatalf("Failed to wait for 6 pods to be ready: %v", err)
	}

	Logger.Info("10. Uncordon 8 nodes and verify 8 more pods get scheduled (remaining PCSG pods)")
	secondWaveNodes := nodesToCordon[4:12]
	tc.UncordonNodes(secondWaveNodes)

	if err := tc.WaitForRunningPods(14); err != nil {
		t.Fatalf("Failed to wait for 8 more pods to be scheduled: %v", err)
	}

	Logger.Info("11. Wait for scheduled pods to become ready (only the 14 that are scheduled)")
	if err := tc.WaitForReadyPods(14); err != nil {
		t.Fatalf("Failed to wait for 14 pods to be ready: %v", err)
	}

	Logger.Info("12. Uncordon 14 nodes and verify the remaining workload pods get scheduled")
	finalWaveNodes := nodesToCordon[12:26]
	tc.UncordonNodes(finalWaveNodes)

	if err := tc.WaitForPods(28); err != nil {
		t.Fatalf("Failed to wait for all final pods to be ready: %v", err)
	}

	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}
