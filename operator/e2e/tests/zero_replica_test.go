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
	"os"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	internalconstants "github.com/ai-dynamo/grove/operator/internal/constants"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const zeroReplicaObservationWindow = 5 * time.Second

func Test_ZR1_AllIdleBootstrapAndStaleBreach(t *testing.T) {
	const pcsName = "zero-bootstrap"
	ctx := context.Background()
	tc, cleanup := prepareIdleWorkload(t, ctx, 1, pcsName, 0, nil)
	defer cleanup()

	workerName := pcsName + "-0-worker"
	pcsgName := pcsName + "-0-workers"
	worker := waitForPCLQ(t, ctx, tc, workerName)
	pcsg := waitForPCSG(t, ctx, tc, pcsgName)
	waitForAllIdleBootstrap(t, ctx, tc, pcsName)

	beforePCLQEvents := countEvents(t, ctx, tc, worker.UID, internalconstants.ReasonAllScheduledReplicasLost)
	beforePCSGEvents := countEvents(t, ctx, tc, pcsg.UID, internalconstants.ReasonAllScheduledReplicasLost)
	setStaleBreachConditions(t, ctx, tc, workerName, pcsgName)
	restartOperator(t, ctx, tc)

	waitForPCLQConditionReason(t, ctx, tc, workerName, apiconstants.ConditionReasonIdle)
	waitForPCSGConditionReason(t, ctx, tc, pcsgName, apiconstants.ConditionReasonIdle)
	assertStableFor(t, ctx, zeroReplicaObservationWindow, func(ctx context.Context) error {
		currentWorker := &grovecorev1alpha1.PodClique{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: workerName}, currentWorker); err != nil {
			return err
		}
		if currentWorker.UID != worker.UID {
			return fmt.Errorf("idle PodClique UID changed: %s -> %s", worker.UID, currentWorker.UID)
		}
		currentPCSG := &grovecorev1alpha1.PodCliqueScalingGroup{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pcsgName}, currentPCSG); err != nil {
			return err
		}
		if currentPCSG.UID != pcsg.UID {
			return fmt.Errorf("idle PodCliqueScalingGroup UID changed: %s -> %s", pcsg.UID, currentPCSG.UID)
		}
		if got := countEvents(t, ctx, tc, worker.UID, internalconstants.ReasonAllScheduledReplicasLost); got != beforePCLQEvents {
			return fmt.Errorf("PodClique AllScheduledReplicasLost event count changed: %d -> %d", beforePCLQEvents, got)
		}
		if got := countEvents(t, ctx, tc, pcsg.UID, internalconstants.ReasonAllScheduledReplicasLost); got != beforePCSGEvents {
			return fmt.Errorf("PodCliqueScalingGroup AllScheduledReplicasLost event count changed: %d -> %d", beforePCSGEvents, got)
		}
		return nil
	})
}

func Test_ZR2_StandaloneLifecycle(t *testing.T) {
	const pcsName = "zero-standalone"
	ctx := context.Background()
	tc, cleanup := prepareIdleWorkload(t, ctx, 3, pcsName, 3, nil)
	defer cleanup()

	workerName := pcsName + "-0-worker"
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Initial standalone pods did not become ready: %v", err)
	}
	waitForStandaloneMembership(t, ctx, tc, pcsName, "worker", 3)
	initialPods := podsForClique(t, tc, workerName)
	initialLocations := podUIDLocations(initialPods)
	initialPodGangs := podGangNameSet(t, ctx, tc, pcsName)
	initialEpoch := maxPGMEpoch(t, ctx, tc, pcsName)

	updateScale(t, ctx, tc, &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: workerName, Namespace: tc.Namespace},
	}, 1)
	waitForPodCountAndReady(t, tc, 1)
	waitForStandaloneMembership(t, ctx, tc, pcsName, "worker", 1)
	assertRetainedPodLocation(t, podsForClique(t, tc, workerName), initialLocations)

	updateScale(t, ctx, tc, &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: workerName, Namespace: tc.Namespace},
	}, 0)
	waitForAllIdle(t, ctx, tc, pcsName, pcsName+"-0-workers-0-prefill", pcsName+"-0-workers-0-decode")
	waitForAllIdleBootstrap(t, ctx, tc, pcsName)

	updateScale(t, ctx, tc, &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: workerName, Namespace: tc.Namespace},
	}, 1)
	waitForPodCountAndReady(t, tc, 1)
	waitForStandaloneMembership(t, ctx, tc, pcsName, "worker", 1)
	if wakeEpoch := maxPGMEpoch(t, ctx, tc, pcsName); wakeEpoch <= initialEpoch {
		t.Fatalf("wake epoch = %d, want greater than initial epoch %d", wakeEpoch, initialEpoch)
	}
	for name := range podGangNameSet(t, ctx, tc, pcsName) {
		if initialPodGangs.Has(name) {
			t.Fatalf("wake reused materialized PodGang %s", name)
		}
	}

	updateScale(t, ctx, tc, &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: workerName, Namespace: tc.Namespace},
	}, 3)
	waitForPodCountAndReady(t, tc, 3)
	waitForStandaloneMembership(t, ctx, tc, pcsName, "worker", 3)
	assertScaleStatus(t, ctx, tc, &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: workerName, Namespace: tc.Namespace},
	}, 3, true)
}

func Test_ZR3_PodCliqueScalingGroupLifecycle(t *testing.T) {
	const pcsName = "zero-pcsg"
	ctx := context.Background()
	tc, cleanup := prepareIdleWorkload(t, ctx, 6, pcsName, 0, func(pcs *grovecorev1alpha1.PodCliqueSet) {
		config := idlePCSGConfig(t, pcs)
		config.Replicas = ptr.To(int32(3))
	})
	defer cleanup()

	pcsgName := pcsName + "-0-workers"
	if err := tc.WaitForReadyPods(6); err != nil {
		t.Fatalf("Initial scaling-group pods did not become ready: %v", err)
	}
	waitForPCSGMembership(t, ctx, tc, pcsName, "workers", []int32{0, 1, 2})
	retainedPCLQUIDs := pclqUIDsForPCSGIndex(t, ctx, tc, pcsgName, "0")
	retainedPodLocations := podUIDLocations(podsForPCSGIndex(t, tc, "0"))
	initialPodGangs := podGangNameSet(t, ctx, tc, pcsName)

	updateScale(t, ctx, tc, &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: pcsgName, Namespace: tc.Namespace},
	}, 1)
	waitForPodCountAndReady(t, tc, 2)
	waitForPCSGMembership(t, ctx, tc, pcsName, "workers", []int32{0})
	waitForPCSGChildrenAbsent(t, ctx, tc, pcsgName, 1, 2)
	assertPCLQUIDs(t, ctx, tc, retainedPCLQUIDs)
	assertRetainedPodLocation(t, podsForPCSGIndex(t, tc, "0"), retainedPodLocations)

	updateScale(t, ctx, tc, &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: pcsgName, Namespace: tc.Namespace},
	}, 0)
	waitForAllIdle(t, ctx, tc, pcsName, pcsgName+"-0-prefill", pcsgName+"-0-decode")
	waitForAllIdleBootstrap(t, ctx, tc, pcsName)

	updateScale(t, ctx, tc, &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: pcsgName, Namespace: tc.Namespace},
	}, 2)
	waitForPodCountAndReady(t, tc, 4)
	waitForPCSGMembership(t, ctx, tc, pcsName, "workers", []int32{0, 1})
	for name := range podGangNameSet(t, ctx, tc, pcsName) {
		if initialPodGangs.Has(name) {
			t.Fatalf("PCSG wake reused materialized PodGang %s", name)
		}
	}
}

func Test_ZR4_AdmissionValidationAndRetry(t *testing.T) {
	const pcsName = "zero-admission"
	ctx := context.Background()
	tc, cleanup := prepareIdleWorkload(t, ctx, 4, pcsName, 0, func(pcs *grovecorev1alpha1.PodCliqueSet) {
		guarded := idleClique(t, pcs, "guarded")
		guarded.Spec.MinAvailable = ptr.To(int32(3))
	})
	defer cleanup()

	template := idleClique(t, loadIdlePCS(t, "unused"), "worker").Spec
	invalidPCLQ := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-pclq", Namespace: tc.Namespace},
		Spec:       template,
	}
	invalidPCLQ.Spec.Replicas = 1
	invalidPCLQ.Spec.MinAvailable = ptr.To(int32(2))
	requireInvalidCause(t, tc.Client.Create(ctx, invalidPCLQ), "spec")
	assertObjectNotFound(t, ctx, tc, client.ObjectKeyFromObject(invalidPCLQ), &grovecorev1alpha1.PodClique{})

	invalidPCSG := &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-pcsg", Namespace: tc.Namespace},
		Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
			Replicas:     1,
			MinAvailable: ptr.To(int32(2)),
			CliqueNames:  []string{"worker"},
		},
	}
	requireInvalidCause(t, tc.Client.Create(ctx, invalidPCSG), "spec")
	assertObjectNotFound(t, ctx, tc, client.ObjectKeyFromObject(invalidPCSG), &grovecorev1alpha1.PodCliqueScalingGroup{})

	invalidPCS := loadIdlePCS(t, "invalid-pcs-template")
	invalidTemplate := idleClique(t, invalidPCS, "worker")
	invalidTemplate.Spec.Replicas = 1
	invalidTemplate.Spec.MinAvailable = ptr.To(int32(2))
	requireInvalidCause(t, tc.Client.Create(ctx, invalidPCS), "spec.template.cliques[1].spec")
	assertObjectNotFound(t, ctx, tc, client.ObjectKeyFromObject(invalidPCS), &grovecorev1alpha1.PodCliqueSet{})

	guardedName := pcsName + "-0-guarded"
	waitForPCLQ(t, ctx, tc, guardedName)
	assertRejectedReplicaUpdate(t, ctx, tc, guardedName, 1)

	updateScale(t, ctx, tc, &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: guardedName, Namespace: tc.Namespace},
	}, 4)
	waitForPodCountAndReady(t, tc, 4)
	assertRejectedReplicaUpdate(t, ctx, tc, guardedName, 2)

	beforeEvents := countEvents(t, ctx, tc, waitForPCLQ(t, ctx, tc, guardedName).UID, "")
	for range 3 {
		assertRejectedScaleUpdate(t, ctx, tc, guardedName, 2)
	}
	guarded := waitForPCLQ(t, ctx, tc, guardedName)
	if guarded.Spec.Replicas != 4 {
		t.Fatalf("stored replicas = %d after rejected retries, want 4", guarded.Spec.Replicas)
	}
	if got := countEvents(t, ctx, tc, guarded.UID, ""); got != beforeEvents {
		t.Fatalf("warning event count changed after rejected scale retries: %d -> %d", beforeEvents, got)
	}

	updateScale(t, ctx, tc, &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: guardedName, Namespace: tc.Namespace},
	}, 0)
	waitForAllIdle(t, ctx, tc, pcsName, pcsName+"-0-workers-0-prefill", pcsName+"-0-workers-0-decode")
	assertScaleStatus(t, ctx, tc, &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: guardedName, Namespace: tc.Namespace},
	}, 0, true)
}

func Test_ZR5_PodCliqueSetReplicaIsolation(t *testing.T) {
	const pcsName = "zero-isolation"
	ctx := context.Background()
	tc, cleanup := prepareIdleWorkload(t, ctx, 2, pcsName, 1, func(pcs *grovecorev1alpha1.PodCliqueSet) {
		pcs.Spec.Replicas = 2
	})
	defer cleanup()

	if err := tc.WaitForReadyPods(2); err != nil {
		t.Fatalf("Initial replica-isolation pods did not become ready: %v", err)
	}
	replicaOnePCLQ := pcsName + "-1-worker"
	replicaOnePods := podsForClique(t, tc, replicaOnePCLQ)
	replicaOneLocations := podUIDLocations(replicaOnePods)
	replicaOnePGM := getPGM(t, ctx, tc, pcsName, 1).DeepCopy()
	replicaOnePodGangs := podGangUIDsForPCSReplica(t, ctx, tc, pcsName, "1")

	updateScale(t, ctx, tc, &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName + "-0-worker", Namespace: tc.Namespace},
	}, 0)
	waitForPodCountAndReady(t, tc, 1)
	assertRetainedPodLocation(t, podsForClique(t, tc, replicaOnePCLQ), replicaOneLocations)
	if current := getPGM(t, ctx, tc, pcsName, 1); !reflect.DeepEqual(replicaOnePGM.Spec.Entries, current.Spec.Entries) {
		t.Fatalf("PodGangMap for untouched PCS replica changed")
	}
	assertPodGangUIDs(t, ctx, tc, replicaOnePodGangs)
}

func prepareIdleWorkload(
	t *testing.T,
	ctx context.Context,
	workerNodes int,
	name string,
	workerReplicas int32,
	mutate func(*grovecorev1alpha1.PodCliqueSet),
) (*testctx.TestContext, func()) {
	t.Helper()
	tc, cleanup := testctx.PrepareTest(ctx, t, workerNodes,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         name,
			Namespace:    "default",
			ExpectedPods: int(workerReplicas),
		}),
	)
	pcs := loadIdlePCS(t, name)
	idleClique(t, pcs, "worker").Spec.Replicas = workerReplicas
	if mutate != nil {
		mutate(pcs)
	}
	if err := tc.Client.Create(ctx, pcs); err != nil {
		cleanup()
		t.Fatalf("Failed to create PodCliqueSet %s: %v", name, err)
	}
	return tc, cleanup
}

func loadIdlePCS(t *testing.T, name string) *grovecorev1alpha1.PodCliqueSet {
	t.Helper()
	data, err := os.ReadFile("../yaml/workload-idle-wake.yaml")
	if err != nil {
		t.Fatalf("Failed to read idle workload fixture: %v", err)
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	if err := yaml.Unmarshal(data, pcs); err != nil {
		t.Fatalf("Failed to decode idle workload fixture: %v", err)
	}
	pcs.Name = name
	pcs.Namespace = "default"
	pcs.ResourceVersion = ""
	pcs.UID = ""
	pcs.Labels["app"] = name
	return pcs
}

func idleClique(t *testing.T, pcs *grovecorev1alpha1.PodCliqueSet, name string) *grovecorev1alpha1.PodCliqueTemplateSpec {
	t.Helper()
	for i := range pcs.Spec.Template.Cliques {
		if pcs.Spec.Template.Cliques[i].Name == name {
			return pcs.Spec.Template.Cliques[i]
		}
	}
	t.Fatalf("PodClique template %s not found", name)
	return nil
}

func idlePCSGConfig(t *testing.T, pcs *grovecorev1alpha1.PodCliqueSet) *grovecorev1alpha1.PodCliqueScalingGroupConfig {
	t.Helper()
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcs.Spec.Template.PodCliqueScalingGroupConfigs[i].Name == "workers" {
			return &pcs.Spec.Template.PodCliqueScalingGroupConfigs[i]
		}
	}
	t.Fatal("PodCliqueScalingGroup config workers not found")
	return nil
}

func waitForAllIdleBootstrap(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName string) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pods, err := tc.ListPods()
		if err != nil || len(pods.Items) != 0 {
			return false, err
		}
		if len(listPodGangs(t, ctx, tc, pcsName).Items) != 0 {
			return false, nil
		}
		pclqs := &grovecorev1alpha1.PodCliqueList{}
		if err := tc.Client.List(ctx, pclqs,
			client.InNamespace(tc.Namespace),
			client.MatchingLabels{apicommon.LabelPartOfKey: pcsName},
		); err != nil {
			return false, err
		}
		for i := range pclqs.Items {
			owner := metav1.GetControllerOfNoCopy(&pclqs.Items[i])
			if owner != nil && owner.Kind == apiconstants.KindPodCliqueScalingGroup {
				return false, nil
			}
		}
		pgm := getPGM(t, ctx, tc, pcsName, 0)
		var baseAnchor, scaleOut bool
		for i := range pgm.Spec.Entries {
			entry := &pgm.Spec.Entries[i]
			if len(entry.PodCliques) != 0 || hasPCSGIndices(entry.PCSGReplicaIndices) {
				return false, nil
			}
			if entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor &&
				entry.AnchorIndex != nil && *entry.AnchorIndex == 0 {
				baseAnchor = true
			}
			if entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut {
				scaleOut = true
			}
		}
		pcs := &grovecorev1alpha1.PodCliqueSet{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pcsName}, pcs); err != nil {
			return false, err
		}
		return baseAnchor && scaleOut && pcs.Status.AvailableReplicas == pcs.Spec.Replicas, nil
	}); err != nil {
		t.Fatalf("All-idle bootstrap did not converge: %v", err)
	}
}

func hasPCSGIndices(indices map[string][]int32) bool {
	for _, values := range indices {
		if len(values) != 0 {
			return true
		}
	}
	return false
}

func setStaleBreachConditions(t *testing.T, ctx context.Context, tc *testctx.TestContext, pclqName, pcsgName string) {
	t.Helper()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pclq := &grovecorev1alpha1.PodClique{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pclqName}, pclq); err != nil {
			return err
		}
		meta.SetStatusCondition(&pclq.Status.Conditions, metav1.Condition{
			Type:               apiconstants.ConditionTypeMinAvailableBreached,
			Status:             metav1.ConditionTrue,
			Reason:             apiconstants.ConditionReasonInsufficientScheduledPods,
			ObservedGeneration: pclq.Generation,
		})
		return tc.Client.Status().Update(ctx, pclq)
	}); err != nil {
		t.Fatalf("Failed to seed stale PodClique breach: %v", err)
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pcsgName}, pcsg); err != nil {
			return err
		}
		meta.SetStatusCondition(&pcsg.Status.Conditions, metav1.Condition{
			Type:               apiconstants.ConditionTypeMinAvailableBreached,
			Status:             metav1.ConditionTrue,
			Reason:             apiconstants.ConditionReasonInsufficientAvailablePCSGReplicas,
			ObservedGeneration: pcsg.Generation,
		})
		return tc.Client.Status().Update(ctx, pcsg)
	}); err != nil {
		t.Fatalf("Failed to seed stale PodCliqueScalingGroup breach: %v", err)
	}
}

func waitForPCSG(t *testing.T, ctx context.Context, tc *testctx.TestContext, name string) *grovecorev1alpha1.PodCliqueScalingGroup {
	t.Helper()
	var result *grovecorev1alpha1.PodCliqueScalingGroup
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, pcsg); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		result = pcsg
		return true, nil
	}); err != nil {
		t.Fatalf("PodCliqueScalingGroup %s did not appear: %v", name, err)
	}
	return result
}

func waitForPCSGConditionReason(t *testing.T, ctx context.Context, tc *testctx.TestContext, name, reason string) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, pcsg); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		condition := meta.FindStatusCondition(pcsg.Status.Conditions, apiconstants.ConditionTypeMinAvailableBreached)
		return condition != nil && condition.Status == metav1.ConditionFalse &&
			condition.Reason == reason && condition.ObservedGeneration == pcsg.Generation, nil
	}); err != nil {
		t.Fatalf("PodCliqueScalingGroup %s condition did not reach reason %s: %v", name, reason, err)
	}
}

func countEvents(t *testing.T, ctx context.Context, tc *testctx.TestContext, uid types.UID, reason string) int {
	t.Helper()
	events := &corev1.EventList{}
	if err := tc.Client.List(ctx, events, client.InNamespace(tc.Namespace)); err != nil {
		t.Fatalf("Failed to list events: %v", err)
	}
	count := 0
	for i := range events.Items {
		event := &events.Items[i]
		if event.InvolvedObject.UID == uid && (reason == "" || event.Reason == reason) &&
			(reason != "" || event.Type == corev1.EventTypeWarning) {
			count++
		}
	}
	return count
}

func assertStableFor(t *testing.T, ctx context.Context, duration time.Duration, check func(context.Context) error) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for {
		if err := check(ctx); err != nil {
			t.Fatal(err)
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

func updateScale(t *testing.T, ctx context.Context, tc *testctx.TestContext, obj client.Object, replicas int32) {
	t.Helper()
	scale := &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: obj.GetName(), Namespace: obj.GetNamespace()},
		Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
	}
	if err := tc.Client.SubResource("scale").Update(ctx, obj, client.WithSubResourceBody(scale)); err != nil {
		t.Fatalf("Failed to scale %T %s to %d: %v", obj, obj.GetName(), replicas, err)
	}
}

func assertScaleStatus(t *testing.T, ctx context.Context, tc *testctx.TestContext, obj client.Object, replicas int32, wantSelector bool) {
	t.Helper()
	scale := &autoscalingv1.Scale{}
	if err := tc.Client.SubResource("scale").Get(ctx, obj, scale); err != nil {
		t.Fatalf("Failed to get scale for %T %s: %v", obj, obj.GetName(), err)
	}
	if scale.Spec.Replicas != replicas {
		t.Fatalf("scale spec replicas = %d, want %d", scale.Spec.Replicas, replicas)
	}
	if wantSelector && scale.Status.Selector == "" {
		t.Fatal("scale status selector is empty")
	}
}

func waitForPodCountAndReady(t *testing.T, tc *testctx.TestContext, count int) {
	t.Helper()
	if _, err := tc.WaitForPodCount(count); err != nil {
		t.Fatalf("Pod count did not converge to %d: %v", count, err)
	}
	if err := tc.WaitForReadyPods(count); err != nil {
		t.Fatalf("Ready pod count did not converge to %d: %v", count, err)
	}
}

func waitForStandaloneMembership(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName, cliqueName string, replicas int32) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pgm := getPGM(t, ctx, tc, pcsName, 0)
		total := int32(0)
		for i := range pgm.Spec.Entries {
			total += pgm.Spec.Entries[i].PodCliques[cliqueName]
		}
		if total != replicas {
			return false, nil
		}
		podGangs := listPodGangs(t, ctx, tc, pcsName)
		podGroups := 0
		references := 0
		for i := range podGangs.Items {
			for _, group := range podGangs.Items[i].Spec.PodGroups {
				if group.Name == pcsName+"-0-"+cliqueName {
					podGroups++
					references += len(group.PodReferences)
				}
			}
		}
		return podGroups == 1 && references == int(replicas), nil
	}); err != nil {
		t.Fatalf("Standalone membership did not converge to %d: %v", replicas, err)
	}
}

func waitForPCSGMembership(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName, pcsgName string, want []int32) {
	t.Helper()
	slices.Sort(want)
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pgm := getPGM(t, ctx, tc, pcsName, 0)
		var got []int32
		for i := range pgm.Spec.Entries {
			got = append(got, pgm.Spec.Entries[i].PCSGReplicaIndices[pcsgName]...)
		}
		slices.Sort(got)
		return slices.Equal(got, want), nil
	}); err != nil {
		t.Fatalf("PCSG membership did not converge to %v: %v", want, err)
	}
}

func getPGM(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName string, replica int) *grovecorev1alpha1.PodGangMap {
	t.Helper()
	pgm := &grovecorev1alpha1.PodGangMap{}
	name := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: pcsName, Replica: replica})
	if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, pgm); err != nil {
		t.Fatalf("Failed to get PodGangMap %s: %v", name, err)
	}
	return pgm
}

func maxPGMEpoch(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName string) int64 {
	t.Helper()
	var maxEpoch int64
	for _, entry := range getPGM(t, ctx, tc, pcsName, 0).Spec.Entries {
		epoch, err := strconv.ParseInt(entry.Epoch, 10, 64)
		if err != nil {
			t.Fatalf("Invalid epoch %q: %v", entry.Epoch, err)
		}
		maxEpoch = max(maxEpoch, epoch)
	}
	return maxEpoch
}

func podGangNameSet(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName string) stringSet {
	t.Helper()
	result := stringSet{}
	for _, podGang := range listPodGangs(t, ctx, tc, pcsName).Items {
		result[podGang.Name] = struct{}{}
	}
	return result
}

func podsForClique(t *testing.T, tc *testctx.TestContext, pclqName string) []corev1.Pod {
	t.Helper()
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	var result []corev1.Pod
	for _, pod := range pods.Items {
		if pod.Labels[apicommon.LabelPodClique] == pclqName {
			result = append(result, pod)
		}
	}
	return result
}

func podsForPCSGIndex(t *testing.T, tc *testctx.TestContext, index string) []corev1.Pod {
	t.Helper()
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	var result []corev1.Pod
	for _, pod := range pods.Items {
		if pod.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex] == index {
			result = append(result, pod)
		}
	}
	return result
}

func podUIDLocations(pods []corev1.Pod) map[types.UID]string {
	result := make(map[types.UID]string, len(pods))
	for _, pod := range pods {
		result[pod.UID] = pod.Spec.NodeName
	}
	return result
}

func assertRetainedPodLocation(t *testing.T, pods []corev1.Pod, original map[types.UID]string) {
	t.Helper()
	if len(pods) == 0 {
		t.Fatal("no retained pods found")
	}
	for _, pod := range pods {
		node, ok := original[pod.UID]
		if !ok {
			t.Fatalf("pod %s has new UID %s", pod.Name, pod.UID)
		}
		if pod.Spec.NodeName != node {
			t.Fatalf("pod %s moved from node %s to %s", pod.Name, node, pod.Spec.NodeName)
		}
	}
}

func waitForPCSGChildrenAbsent(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsgName string, indices ...int) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		for _, index := range indices {
			for _, clique := range []string{"prefill", "decode"} {
				name := fmt.Sprintf("%s-%d-%s", pcsgName, index, clique)
				err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, &grovecorev1alpha1.PodClique{})
				if err == nil {
					return false, nil
				}
				if !apierrors.IsNotFound(err) {
					return false, err
				}
			}
		}
		return true, nil
	}); err != nil {
		t.Fatalf("Scaled-in PCSG children remained: %v", err)
	}
}

func pclqUIDsForPCSGIndex(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsgName, index string) map[string]types.UID {
	t.Helper()
	result := map[string]types.UID{}
	for _, clique := range []string{"prefill", "decode"} {
		name := pcsgName + "-" + index + "-" + clique
		pclq := waitForPCLQ(t, ctx, tc, name)
		result[name] = pclq.UID
	}
	return result
}

func assertPCLQUIDs(t *testing.T, ctx context.Context, tc *testctx.TestContext, want map[string]types.UID) {
	t.Helper()
	for name, uid := range want {
		pclq := &grovecorev1alpha1.PodClique{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, pclq); err != nil {
			t.Fatalf("Failed to get retained PodClique %s: %v", name, err)
		}
		if pclq.UID != uid {
			t.Fatalf("PodClique %s UID changed: %s -> %s", name, uid, pclq.UID)
		}
	}
}

func requireInvalidCause(t *testing.T, err error, field string) {
	t.Helper()
	if !apierrors.IsInvalid(err) {
		t.Fatalf("error = %v, want StatusReasonInvalid", err)
	}
	statusErr, ok := err.(apierrors.APIStatus)
	if !ok {
		t.Fatalf("error type %T does not implement APIStatus", err)
	}
	status := statusErr.Status()
	if status.Reason != metav1.StatusReasonInvalid || status.Details == nil {
		t.Fatalf("status = %#v, want Invalid with details", status)
	}
	for _, cause := range status.Details.Causes {
		if cause.Field == field {
			return
		}
	}
	t.Fatalf("field causes = %#v, want exact field %q", status.Details.Causes, field)
}

func assertObjectNotFound(t *testing.T, ctx context.Context, tc *testctx.TestContext, key client.ObjectKey, obj client.Object) {
	t.Helper()
	if err := tc.Client.Get(ctx, key, obj); !apierrors.IsNotFound(err) {
		t.Fatalf("Get %T %s after rejected create = %v, want NotFound", obj, key, err)
	}
}

func assertRejectedReplicaUpdate(t *testing.T, ctx context.Context, tc *testctx.TestContext, name string, replicas int32) {
	t.Helper()
	before := waitForPCLQ(t, ctx, tc, name)
	updated := before.DeepCopy()
	updated.Spec.Replicas = replicas
	requireInvalidCause(t, tc.Client.Update(ctx, updated), "spec")
	after := waitForPCLQ(t, ctx, tc, name)
	if after.Spec.Replicas != before.Spec.Replicas || after.ResourceVersion != before.ResourceVersion {
		t.Fatalf("rejected update changed stored object: replicas %d -> %d, resourceVersion %s -> %s",
			before.Spec.Replicas, after.Spec.Replicas, before.ResourceVersion, after.ResourceVersion)
	}
	assertRejectedScaleUpdate(t, ctx, tc, name, replicas)
}

func assertRejectedScaleUpdate(t *testing.T, ctx context.Context, tc *testctx.TestContext, name string, replicas int32) {
	t.Helper()
	before := waitForPCLQ(t, ctx, tc, name)
	scale := &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tc.Namespace},
		Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
	}
	requireInvalidCause(t, tc.Client.SubResource("scale").Update(ctx, before, client.WithSubResourceBody(scale)), "spec")
	after := waitForPCLQ(t, ctx, tc, name)
	if after.Spec.Replicas != before.Spec.Replicas {
		t.Fatalf("rejected scale changed stored replicas: %d -> %d", before.Spec.Replicas, after.Spec.Replicas)
	}
}

func podGangUIDsForPCSReplica(t *testing.T, ctx context.Context, tc *testctx.TestContext, pcsName, replica string) map[string]types.UID {
	t.Helper()
	result := map[string]types.UID{}
	for _, podGang := range listPodGangs(t, ctx, tc, pcsName).Items {
		if podGang.Labels[apicommon.LabelPodCliqueSetReplicaIndex] == replica {
			result[podGang.Name] = podGang.UID
		}
	}
	if len(result) == 0 {
		t.Fatalf("no PodGangs found for PCS replica %s", replica)
	}
	return result
}

func assertPodGangUIDs(t *testing.T, ctx context.Context, tc *testctx.TestContext, want map[string]types.UID) {
	t.Helper()
	for name, uid := range want {
		podGang := &groveschedulerv1alpha1.PodGang{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, podGang); err != nil {
			t.Fatalf("Failed to get retained PodGang %s: %v", name, err)
		}
		if podGang.UID != uid {
			t.Fatalf("PodGang %s UID changed: %s -> %s", name, uid, podGang.UID)
		}
	}
}

func restartOperator(t *testing.T, ctx context.Context, tc *testctx.TestContext) {
	t.Helper()
	operatorPods := &corev1.PodList{}
	if err := tc.Client.List(ctx, operatorPods,
		client.InNamespace(setup.OperatorNamespace),
		setup.OperatorPodLabels,
	); err != nil {
		t.Fatalf("Failed to list operator pods: %v", err)
	}
	if len(operatorPods.Items) != 1 {
		t.Fatalf("operator pod count = %d, want 1", len(operatorPods.Items))
	}
	oldUID := operatorPods.Items[0].UID
	if err := tc.Client.Delete(ctx, &operatorPods.Items[0]); err != nil {
		t.Fatalf("Failed to restart operator pod %s: %v", operatorPods.Items[0].Name, err)
	}
	if err := wait.PollUntilContextTimeout(ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		current := &corev1.PodList{}
		if err := tc.Client.List(ctx, current,
			client.InNamespace(setup.OperatorNamespace),
			setup.OperatorPodLabels,
		); err != nil {
			return false, err
		}
		for i := range current.Items {
			if current.Items[i].UID != oldUID && isPodReady(&current.Items[i]) {
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		t.Fatalf("Operator did not recover after restart: %v", err)
	}
}
