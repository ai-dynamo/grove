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

package podgangmigrator

import (
	"context"
	"strconv"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testNamespace = "default"
	testPCSName   = "pcs"
	testPCSGName  = "sg"
	testGenHash   = "hash-1"
	podsPerPCLQ   = 2
	// testMembershipAnnotationKey is the scheduler-membership annotation the fake backend stamps. It
	// stands in for a real backend key such as Volcano's scheduling.k8s.io/group-name.
	testMembershipAnnotationKey = "scheduling.test/group-name"
)

// replicaEpochs holds the epochs of a replica's PodGangMap entries, one per role. Tests give each
// replica distinct epochs so an assertion on the resulting PodGang name confirms the migrator read that
// replica's own PodGangMap. If the migrator resolved a replica's PodCliques against a different
// replica's PodGangMap, the name would carry the wrong epoch and the assertion would fail.
type replicaEpochs struct {
	anchor   string
	tail     string
	scaleOut string
}

func TestSyncMigratesSingleReplica(t *testing.T) {
	epochs := replicaEpochs{anchor: "1000", tail: "1001"}
	objs := append([]client.Object{gatedPCS(1)}, legacyReplicaObjects(0, epochs)...)
	objs = append(objs, epochPodGangsForReplica(0, epochs)...)
	cl := newMigratorClient(objs...)
	operator := New(cl, groveclientscheme.Scheme, newFakeRegistry())

	err := operator.Sync(context.Background(), logr.Discard(), getPCS(t, cl))
	require.NoError(t, err)

	assertReplicaMigrated(t, cl, 0, epochs)
	assertGateCleared(t, cl)
}

func TestSyncMigratesMultipleReplicas(t *testing.T) {
	// Each replica has its own PodGangMap with distinct epochs, so migrating replica 1 against replica
	// 0's map would produce the wrong names and fail the assertions.
	epochsByReplica := map[int]replicaEpochs{
		0: {anchor: "1000", tail: "1001"},
		1: {anchor: "2000", tail: "2001"},
	}
	objs := []client.Object{gatedPCS(2)}
	for replicaIndex, epochs := range epochsByReplica {
		objs = append(objs, legacyReplicaObjects(replicaIndex, epochs)...)
		objs = append(objs, epochPodGangsForReplica(replicaIndex, epochs)...)
	}
	cl := newMigratorClient(objs...)
	operator := New(cl, groveclientscheme.Scheme, newFakeRegistry())

	err := operator.Sync(context.Background(), logr.Discard(), getPCS(t, cl))
	require.NoError(t, err)

	for replicaIndex, epochs := range epochsByReplica {
		assertReplicaMigrated(t, cl, replicaIndex, epochs)
	}
	assertGateCleared(t, cl)
}

func TestSyncMigratesScaleOutReplica(t *testing.T) {
	epochs := replicaEpochs{anchor: "1000", scaleOut: "1002"}
	// PodGangMap with an anchor entry holding PCSG replica 0 and a ScaleOut entry holding PCSG replica 1.
	pgm := testutils.NewPodGangMapBuilder(testPCSName, testNamespace, types.UID("uid"), 0).WithEntries(
		testutils.NewAnchorEntry(testGenHash, epochs.anchor, testPCSGName, 0),
		testutils.NewScaleOutEntry(testGenHash, epochs.scaleOut, testPCSGName, 1),
	).Build()

	objs := []client.Object{gatedPCS(1), pgm}
	// PCSG replica 0 is the anchor member on the base PodGang.
	for _, cliqueName := range []string{"pcb", "pcc"} {
		objs = append(objs, pcsgPCLQWithPods(0, 0, cliqueName, legacyBasePodGangName(0), "")...)
	}
	// PCSG replica 1 is a scaled-out member on the scaled PodGang, carrying base-podgang.
	for _, cliqueName := range []string{"pcb", "pcc"} {
		objs = append(objs, pcsgPCLQWithPods(0, 1, cliqueName, legacyScaledPodGangName(0), legacyBasePodGangName(0))...)
	}
	// The epoch PodGangs the anchor and ScaleOut entries materialize into must exist for the gate to lift.
	scaleOutRnr := apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0}
	objs = append(objs,
		testutils.NewPodGangBuilder(apicommon.GenerateAnchorPodGangName(scaleOutRnr, epochs.anchor), testNamespace).Build(),
		testutils.NewPodGangBuilder(apicommon.GenerateNonAnchorPodGangName(scaleOutRnr, epochs.scaleOut, testPCSGName, 1), testNamespace).Build(),
	)

	cl := newMigratorClient(objs...)
	operator := New(cl, groveclientscheme.Scheme, newFakeRegistry())

	err := operator.Sync(context.Background(), logr.Discard(), getPCS(t, cl))
	require.NoError(t, err)

	rnr := apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0}
	wantByPCLQ := map[string]string{
		pcsgPCLQName(0, 0, "pcb"): apicommon.GenerateAnchorPodGangName(rnr, epochs.anchor),
		pcsgPCLQName(0, 0, "pcc"): apicommon.GenerateAnchorPodGangName(rnr, epochs.anchor),
		pcsgPCLQName(0, 1, "pcb"): apicommon.GenerateNonAnchorPodGangName(rnr, epochs.scaleOut, testPCSGName, 1),
		pcsgPCLQName(0, 1, "pcc"): apicommon.GenerateNonAnchorPodGangName(rnr, epochs.scaleOut, testPCSGName, 1),
	}
	for pclqName, wantPodGang := range wantByPCLQ {
		pclq := getPCLQ(t, cl, pclqName)
		assert.Equal(t, wantPodGang, pclq.Labels[apicommon.LabelPodGang], "PodClique %s PodGang label", pclqName)
		assert.NotContains(t, pclq.Labels, apicommon.LabelBasePodGang, "PodClique %s must not carry base-podgang", pclqName)
		for _, pod := range listPodsOfPCLQ(t, cl, pclqName) {
			assert.Equal(t, wantPodGang, pod.Labels[apicommon.LabelPodGang], "Pod %s PodGang label", pod.Name)
			assert.NotContains(t, pod.Labels, apicommon.LabelBasePodGang, "Pod %s must not carry base-podgang", pod.Name)
		}
	}
	assertGateCleared(t, cl)
}

func TestSyncIsNoOpWhenNotGated(t *testing.T) {
	epochs := replicaEpochs{anchor: "1000", tail: "1001"}
	cl := newMigratorClient(legacyReplicaObjects(0, epochs)...)
	operator := New(cl, groveclientscheme.Scheme, newFakeRegistry())

	// PodCliqueSet without the migration gate condition, so Sync must not act.
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").WithReplicas(1).Build()
	err := operator.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)

	// The standalone PodClique keeps its legacy PodGang name.
	pclq := getPCLQ(t, cl, standaloneCliqueName(0))
	assert.Equal(t, legacyBasePodGangName(0), pclq.Labels[apicommon.LabelPodGang])
}

func TestSyncPreservesForeignLabelsAndSpec(t *testing.T) {
	epochs := replicaEpochs{anchor: "1000", tail: "1001"}
	objs := append([]client.Object{gatedPCS(1)}, legacyReplicaObjects(0, epochs)...)
	objs = append(objs, epochPodGangsForReplica(0, epochs)...)
	cl := newMigratorClient(objs...)
	operator := New(cl, groveclientscheme.Scheme, newFakeRegistry())

	err := operator.Sync(context.Background(), logr.Discard(), getPCS(t, cl))
	require.NoError(t, err)

	pclq := getPCLQ(t, cl, standaloneCliqueName(0))
	assert.Equal(t, "bar", pclq.Labels["example.com/foo"], "foreign PodClique label must be preserved")
	assert.Equal(t, int32(1), pclq.Spec.Replicas, "PodClique spec must be untouched")

	for _, pod := range listPodsOfPCLQ(t, cl, standaloneCliqueName(0)) {
		assert.Equal(t, "bar", pod.Labels["example.com/foo"], "foreign Pod label must be preserved")
	}
}

// TestSyncHoldsGateUntilEpochPodGangsExist asserts the migrator does not clear the gate while the
// epoch-scheme PodGangs are absent. It relabels, returns a continue-and-requeue signal, and leaves the
// PodGangMigrationInProgress condition in place, so a later reconcile clears the gate once the PodGang
// component has created them.
func TestSyncHoldsGateUntilEpochPodGangsExist(t *testing.T) {
	epochs := replicaEpochs{anchor: "1000", tail: "1001"}
	// The legacy objects and PodGangMap are present, but the epoch PodGangs are not created yet.
	cl := newMigratorClient(append([]client.Object{gatedPCS(1)}, legacyReplicaObjects(0, epochs)...)...)
	operator := New(cl, groveclientscheme.Scheme, newFakeRegistry())

	err := operator.Sync(context.Background(), logr.Discard(), getPCS(t, cl))
	testutils.AssertGroveError(t, &groveerr.GroveError{Code: groveerr.ErrCodeContinueReconcileAndRequeue, Operation: "Sync"}, err)

	// The relabeling still happened, but the gate is held until the epoch PodGangs exist.
	assertReplicaMigrated(t, cl, 0, epochs)
	assertGateStillPresent(t, cl)
}

func TestMigratePodGangLabelsMissingLabelsReturnsError(t *testing.T) {
	// A PodClique with no labels at all is an unexpected internal state. A label-less object cannot be
	// reached through Sync, since the replica list selector would not match it, so the guard is
	// exercised directly.
	r := New(newMigratorClient(), groveclientscheme.Scheme, newFakeRegistry()).(*_resource)
	pclq := &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "no-labels", Namespace: testNamespace}}

	err := r.migratePodGangLabels(context.Background(), pclq, "target")
	testutils.AssertGroveError(t, &groveerr.GroveError{Code: errCodeMissingLabels, Operation: "Sync"}, err)
}

// TestMigratePodRewritesStaleSchedulerMembershipAnnotation asserts migratePodLabelsAndAnnotations rewrites the
// scheduler-membership annotation from the legacy PodGang name to the epoch-based name, drops
// base-podgang, and leaves foreign annotations untouched.
func TestMigratePodRewritesStaleSchedulerMembershipAnnotation(t *testing.T) {
	pod := testutils.NewPodBuilder("p0", testNamespace).
		WithLabels(map[string]string{
			apicommon.LabelPodGang:     "legacy-podgang",
			apicommon.LabelBasePodGang: "legacy-podgang",
			apicommon.LabelPodClique:   "pclq",
		}).
		Build()
	pod.Annotations = map[string]string{testMembershipAnnotationKey: "legacy-podgang", "example.com/keep": "v"}
	cl := newMigratorClient(pod)
	r := New(cl, groveclientscheme.Scheme, newFakeRegistry()).(*_resource)

	require.NoError(t, r.migratePodLabelsAndAnnotations(context.Background(), pod, "epoch-podgang"))

	got := getPod(t, cl, pod.Name)
	assert.Equal(t, "epoch-podgang", got.Labels[apicommon.LabelPodGang])
	assert.NotContains(t, got.Labels, apicommon.LabelBasePodGang)
	assert.Equal(t, "epoch-podgang", got.Annotations[testMembershipAnnotationKey], "stale scheduler membership annotation must be rewritten")
	assert.Equal(t, "v", got.Annotations["example.com/keep"], "foreign annotation must be preserved")
}

// TestMigratePodSkipsMembershipWhenBackendHasNoAnnotations asserts migratePodLabelsAndAnnotations migrates labels but adds
// no membership annotation when the Pod's scheduler backend does not implement
// PodGangMembershipAnnotator (for example the plain kube backend).
func TestMigratePodSkipsMembershipWhenBackendHasNoAnnotations(t *testing.T) {
	pod := testutils.NewPodBuilder("p0", testNamespace).
		WithLabels(map[string]string{
			apicommon.LabelPodGang:   "legacy-podgang",
			apicommon.LabelPodClique: "pclq",
		}).
		Build()
	cl := newMigratorClient(pod)
	r := New(cl, groveclientscheme.Scheme, fakeRegistry{backend: fakeBackend{}}).(*_resource)

	require.NoError(t, r.migratePodLabelsAndAnnotations(context.Background(), pod, "epoch-podgang"))

	got := getPod(t, cl, pod.Name)
	assert.Equal(t, "epoch-podgang", got.Labels[apicommon.LabelPodGang])
	assert.NotContains(t, got.Annotations, testMembershipAnnotationKey, "backend without membership annotations must add none")
}

func TestDeleteIsNoOp(t *testing.T) {
	operator := New(newMigratorClient(), groveclientscheme.Scheme, newFakeRegistry())
	err := operator.Delete(context.Background(), logr.Discard(), metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace})
	assert.NoError(t, err)
}

func TestGetExistingResourceNamesReturnsNil(t *testing.T) {
	operator := New(newMigratorClient(), groveclientscheme.Scheme, newFakeRegistry())
	names, err := operator.GetExistingResourceNames(context.Background(), logr.Discard(), metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace})
	require.NoError(t, err)
	assert.Nil(t, names)
}

// assertReplicaMigrated verifies every PodClique of the replica and all its Pods carry the expected
// epoch-based PodGang name and no base-podgang label.
func assertReplicaMigrated(t *testing.T, cl client.Client, pcsReplicaIndex int, epochs replicaEpochs) {
	t.Helper()
	for pclqName, wantPodGang := range wantedPodGangByPCLQ(pcsReplicaIndex, epochs) {
		pclq := getPCLQ(t, cl, pclqName)
		assert.Equal(t, wantPodGang, pclq.Labels[apicommon.LabelPodGang], "PodClique %s PodGang label", pclqName)
		assert.NotContains(t, pclq.Labels, apicommon.LabelBasePodGang, "PodClique %s must not carry base-podgang", pclqName)

		pods := listPodsOfPCLQ(t, cl, pclqName)
		assert.Len(t, pods, podsPerPCLQ)
		for _, pod := range pods {
			assert.Equal(t, wantPodGang, pod.Labels[apicommon.LabelPodGang], "Pod %s PodGang label", pod.Name)
			assert.NotContains(t, pod.Labels, apicommon.LabelBasePodGang, "Pod %s must not carry base-podgang", pod.Name)
			assert.Equal(t, wantPodGang, pod.Annotations[testMembershipAnnotationKey], "Pod %s scheduler membership annotation", pod.Name)
		}
	}
}

// assertGateCleared verifies the migration gate condition is removed from the PodCliqueSet.
func assertGateCleared(t *testing.T, cl client.Client) {
	t.Helper()
	pcs := getPCS(t, cl)
	assert.Nil(t, meta.FindStatusCondition(pcs.Status.Conditions, apiconstants.ConditionTypePodGangMigrationInProgress))
}

// assertGateStillPresent verifies the migration gate condition remains on the PodCliqueSet.
func assertGateStillPresent(t *testing.T, cl client.Client) {
	t.Helper()
	pcs := getPCS(t, cl)
	assert.NotNil(t, meta.FindStatusCondition(pcs.Status.Conditions, apiconstants.ConditionTypePodGangMigrationInProgress))
}

// epochPodGangsForReplica builds the epoch-scheme PodGangs the migrator expects for a replica whose
// PodGangMap has an anchor entry (PCSG replicas 0 and 1) and a tail entry (PCSG replica 2), matching
// replicaPGM.
func epochPodGangsForReplica(pcsReplicaIndex int, epochs replicaEpochs) []client.Object {
	rnr := apicommon.ResourceNameReplica{Name: testPCSName, Replica: pcsReplicaIndex}
	return []client.Object{
		testutils.NewPodGangBuilder(apicommon.GenerateAnchorPodGangName(rnr, epochs.anchor), testNamespace).Build(),
		testutils.NewPodGangBuilder(apicommon.GenerateNonAnchorPodGangName(rnr, epochs.tail, testPCSGName, 2), testNamespace).Build(),
	}
}

func standaloneCliqueName(pcsReplicaIndex int) string {
	return apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: pcsReplicaIndex}, "pcd")
}

func pcsgPCLQName(pcsReplicaIndex, pcsgReplicaIndex int, cliqueName string) string {
	pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: pcsReplicaIndex}, testPCSGName)
	return apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgFQN, Replica: pcsgReplicaIndex}, cliqueName)
}

func legacyBasePodGangName(pcsReplicaIndex int) string {
	return apicommon.GenerateBasePodGangName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: pcsReplicaIndex})
}

func legacyScaledPodGangName(pcsReplicaIndex int) string {
	pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: pcsReplicaIndex}, testPCSGName)
	return apicommon.CreatePodGangNameFromPCSGFQN(pcsgFQN, 0)
}

// gatedPCS returns the test PodCliqueSet with the given replica count, carrying the migration gate
// condition, the arg the migrator acts on.
func gatedPCS(replicas int32) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithReplicas(replicas).
		WithStatusConditions(metav1.Condition{
			Type:   apiconstants.ConditionTypePodGangMigrationInProgress,
			Status: metav1.ConditionTrue,
			Reason: "LegacyPodGangsPendingMigration",
		}).Build()
}

// wantedPodGangByPCLQ maps every PodClique of a legacy replica to the epoch-based PodGang name it must
// carry after migration. The standalone clique and the anchor PCSG replicas (0 and 1) resolve to the
// anchor PodGang. The scaled PCSG replica (2) resolves to the non-anchor PodGang.
func wantedPodGangByPCLQ(pcsReplicaIndex int, epochs replicaEpochs) map[string]string {
	rnr := apicommon.ResourceNameReplica{Name: testPCSName, Replica: pcsReplicaIndex}
	anchorName := apicommon.GenerateAnchorPodGangName(rnr, epochs.anchor)
	return map[string]string{
		standaloneCliqueName(pcsReplicaIndex):   anchorName,
		pcsgPCLQName(pcsReplicaIndex, 0, "pcb"): anchorName,
		pcsgPCLQName(pcsReplicaIndex, 0, "pcc"): anchorName,
		pcsgPCLQName(pcsReplicaIndex, 1, "pcb"): anchorName,
		pcsgPCLQName(pcsReplicaIndex, 1, "pcc"): anchorName,
		pcsgPCLQName(pcsReplicaIndex, 2, "pcb"): apicommon.GenerateNonAnchorPodGangName(rnr, epochs.tail, testPCSGName, 2),
		pcsgPCLQName(pcsReplicaIndex, 2, "pcc"): apicommon.GenerateNonAnchorPodGangName(rnr, epochs.tail, testPCSGName, 2),
	}
}

// legacyReplicaObjects builds the legacy-scheme object graph for one replica: its PodGangMap, the
// standalone and PodCliqueScalingGroup-owned PodCliques, and their Pods. Anchor members (standalone,
// PCSG replicas 0 and 1) carry the legacy base PodGang name. The scaled PCSG replica (2) carries the
// legacy scaled PodGang name plus the base-podgang label. It does not include the PodCliqueSet, since a
// single PodCliqueSet spans all replicas.
func legacyReplicaObjects(pcsReplicaIndex int, epochs replicaEpochs) []client.Object {
	objs := []client.Object{replicaPGM(pcsReplicaIndex, epochs)}

	// Standalone PodClique on the base PodGang.
	objs = append(objs, standalonePCLQWithPods(pcsReplicaIndex)...)

	// PCSG replicas 0 and 1 are anchor members, on the base PodGang, no base-podgang label.
	for _, pcsgReplicaIndex := range []int{0, 1} {
		for _, cliqueName := range []string{"pcb", "pcc"} {
			objs = append(objs, pcsgPCLQWithPods(pcsReplicaIndex, pcsgReplicaIndex, cliqueName, legacyBasePodGangName(pcsReplicaIndex), "")...)
		}
	}
	// PCSG replica 2 is the scaled member, on the scaled PodGang, carrying base-podgang.
	for _, cliqueName := range []string{"pcb", "pcc"} {
		objs = append(objs, pcsgPCLQWithPods(pcsReplicaIndex, 2, cliqueName, legacyScaledPodGangName(pcsReplicaIndex), legacyBasePodGangName(pcsReplicaIndex))...)
	}
	return objs
}

// replicaPGM is the PodGangMap of one replica: an anchor entry holding PCSG replicas 0 and 1, and a
// tail entry holding PCSG replica 2.
func replicaPGM(pcsReplicaIndex int, epochs replicaEpochs) *grovecorev1alpha1.PodGangMap {
	return testutils.NewPodGangMapBuilder(testPCSName, testNamespace, types.UID("uid"), pcsReplicaIndex).WithEntries(
		testutils.NewAnchorEntry(testGenHash, epochs.anchor, testPCSGName, 0, 1),
		testutils.NewTailEntry(testGenHash, epochs.tail, testPCSGName, 2),
	).Build()
}

// standalonePCLQWithPods builds the standalone PodClique of a replica on the base PodGang and its Pods.
func standalonePCLQWithPods(pcsReplicaIndex int) []client.Object {
	pclqName := standaloneCliqueName(pcsReplicaIndex)
	podGangName := legacyBasePodGangName(pcsReplicaIndex)
	pclq := testutils.NewPodCliqueBuilder(testPCSName, "uid", "pcd", testNamespace, int32(pcsReplicaIndex)).
		WithLabels(legacyPodGangLabels(podGangName, "")).
		WithLabels(map[string]string{"example.com/foo": "bar"}).
		Build()
	pclq.Name = pclqName
	return append([]client.Object{pclq}, podsForPCLQ(pcsReplicaIndex, pclqName, podGangName, "")...)
}

// pcsgPCLQWithPods builds a PodCliqueScalingGroup-owned PodClique with the given legacy PodGang labels
// and its Pods.
func pcsgPCLQWithPods(pcsReplicaIndex, pcsgReplicaIndex int, cliqueName, podGangName, basePodGangName string) []client.Object {
	pclqName := pcsgPCLQName(pcsReplicaIndex, pcsgReplicaIndex, cliqueName)
	pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: pcsReplicaIndex}, testPCSGName)
	pclq := testutils.NewPCSGPodCliqueBuilder(pclqName, testNamespace, testPCSName, pcsgFQN, pcsReplicaIndex, pcsgReplicaIndex).
		WithLabels(legacyPodGangLabels(podGangName, basePodGangName)).
		Build()
	return append([]client.Object{pclq}, podsForPCLQ(pcsReplicaIndex, pclqName, podGangName, basePodGangName)...)
}

// podsForPCLQ builds podsPerPCLQ Pods owned by the named PodClique, carrying the same legacy PodGang
// labels as their owner PodClique and the labels the migrator selects on.
func podsForPCLQ(pcsReplicaIndex int, pclqName, podGangName, basePodGangName string) []client.Object {
	pods := make([]client.Object, 0, podsPerPCLQ)
	for i := 0; i < podsPerPCLQ; i++ {
		labels := legacyPodGangLabels(podGangName, basePodGangName)
		labels[apicommon.LabelPodClique] = pclqName
		labels[apicommon.LabelPodCliqueSetReplicaIndex] = strconv.Itoa(pcsReplicaIndex)
		labels[apicommon.LabelPartOfKey] = testPCSName
		labels["example.com/foo"] = "bar"
		pod := testutils.NewPodBuilder(pclqName+"-"+strconv.Itoa(i), testNamespace).
			WithOwner(pclqName).
			WithLabels(labels).
			Build()
		// Seed the stale scheduler-membership annotation pointing at the legacy PodGang, so a passing
		// assertion after migration confirms the migrator rewrote it to the epoch-based name.
		pod.Annotations = map[string]string{testMembershipAnnotationKey: podGangName}
		pods = append(pods, pod)
	}
	return pods
}

// legacyPodGangLabels returns the legacy PodGang labels: the podgang name always, and base-podgang only
// when basePodGangName is non-empty (scaled members).
func legacyPodGangLabels(podGangName, basePodGangName string) map[string]string {
	labels := map[string]string{apicommon.LabelPodGang: podGangName}
	if basePodGangName != "" {
		labels[apicommon.LabelBasePodGang] = basePodGangName
	}
	return labels
}

func newMigratorClient(objs ...client.Object) client.Client {
	return testutils.NewTestClientBuilder().
		WithObjects(objs...).
		WithStatusSubresource(&grovecorev1alpha1.PodCliqueSet{}).
		Build()
}

func getPCLQ(t *testing.T, cl client.Client, name string) *grovecorev1alpha1.PodClique {
	t.Helper()
	pclq := &grovecorev1alpha1.PodClique{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, pclq))
	return pclq
}

func getPCS(t *testing.T, cl client.Client) *grovecorev1alpha1.PodCliqueSet {
	t.Helper()
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testPCSName}, pcs))
	return pcs
}

func listPodsOfPCLQ(t *testing.T, cl client.Client, pclqName string) []corev1.Pod {
	t.Helper()
	podList := &corev1.PodList{}
	require.NoError(t, cl.List(context.Background(), podList,
		client.InNamespace(testNamespace),
		client.MatchingLabels(map[string]string{apicommon.LabelPodClique: pclqName})))
	return podList.Items
}

func getPod(t *testing.T, cl client.Client, name string) *corev1.Pod {
	t.Helper()
	pod := &corev1.Pod{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, pod))
	return pod
}

// newFakeRegistry returns a scheduler.Registry whose only backend implements
// PodGangMembershipAnnotator, so migratePodLabelsAndAnnotations resolves it for every Pod regardless of
// spec.schedulerName.
func newFakeRegistry() scheduler.Registry {
	return fakeRegistry{backend: fakeAnnotatingBackend{}}
}

// fakeRegistry is a scheduler.Registry that always resolves to a single backend.
type fakeRegistry struct {
	backend scheduler.Backend
}

func (f fakeRegistry) Get(_ string) scheduler.Backend { return f.backend }

func (f fakeRegistry) GetDefault() scheduler.Backend { return f.backend }

func (f fakeRegistry) GetOrDefault(_ string) scheduler.Backend { return f.backend }

func (f fakeRegistry) All() map[string]scheduler.Backend {
	return map[string]scheduler.Backend{f.backend.Name(): f.backend}
}

func (f fakeRegistry) AllTopologyAware() map[string]scheduler.TopologyAwareBackend {
	return nil
}

// fakeBackend is a no-op scheduler.Backend that carries no PodGang membership annotations, standing in
// for a backend such as the plain kube backend.
type fakeBackend struct{}

func (fakeBackend) Name() string { return "fake" }

func (fakeBackend) Init(_ client.Client) error { return nil }

func (fakeBackend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

func (fakeBackend) PreparePod(_ *corev1.Pod) error { return nil }

func (fakeBackend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}

// fakeAnnotatingBackend is a fakeBackend that also reports a scheduler-membership annotation, standing
// in for a backend such as Volcano or KAI.
type fakeAnnotatingBackend struct{ fakeBackend }

func (fakeAnnotatingBackend) PodGangMembershipAnnotations(newPodGangName string) map[string]string {
	return map[string]string{testMembershipAnnotationKey: newPodGangName}
}
