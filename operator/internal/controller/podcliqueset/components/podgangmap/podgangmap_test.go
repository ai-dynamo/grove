// /*
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
// */

package podgangmap

import (
	"context"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSync_NoPGMsExist_NoCoherentUpdate_NoOp(t *testing.T) {
	pcs := newTestPCS("abc12xyz", nil, nil)
	pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{
		Type: grovecorev1alpha1.CoherentStrategy,
	}

	cl := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
	r := &_resource{client: cl, scheme: groveclientscheme.Scheme, clk: clock.RealClock{}}

	err := r.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)
}

func TestSync_RollingRecreate_NoPGMsExist_NoOp(t *testing.T) {
	pcs := newTestPCS("abc12xyz", nil, nil)
	pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{
		Type: grovecorev1alpha1.RollingRecreateStrategy,
	}

	cl := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
	r := &_resource{client: cl, scheme: groveclientscheme.Scheme, clk: clock.RealClock{}}

	err := r.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)
}

func TestBuildEntryFromPodGang(t *testing.T) {
	pcs := newTestPCS("old-hash",
		[]grovecorev1alpha1.PodCliqueTemplateSpec{
			{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](2)}},
			{Name: "pleader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
			{Name: "pworker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To[int32](2)}},
			{Name: "dleader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
			{Name: "dworker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](2)}},
		},
		[]grovecorev1alpha1.PodCliqueScalingGroupConfig{
			{Name: "prefill", CliqueNames: []string{"pleader", "pworker"}, MinAvailable: ptr.To[int32](1)},
			{Name: "decode", CliqueNames: []string{"dleader", "dworker"}, MinAvailable: ptr.To[int32](1)},
		},
	)

	t.Run("BPG with standalone PCLQ and PCSGs", func(t *testing.T) {
		pg := groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0"},
			Spec: groveschedulerv1alpha1.PodGangSpec{
				PodGroups: []groveschedulerv1alpha1.PodGroup{
					{Name: "my-pcs-0-frontend", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 5)},
					{Name: "my-pcs-0-prefill-0-pleader", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 1)},
					{Name: "my-pcs-0-prefill-0-pworker", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 3)},
					{Name: "my-pcs-0-decode-0-dleader", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 1)},
					{Name: "my-pcs-0-decode-0-dworker", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 4)},
				},
			},
		}

		entry, err := buildEntryFromPodGang(pcs, "old-hash", pg)
		require.NoError(t, err)
		assert.Equal(t, "my-pcs-0", entry.Name)
		assert.Equal(t, "old-hash", entry.PodCliqueSetGenerationHash)
		assert.Equal(t, int32(5), entry.PodCliques["frontend"])
		assert.Equal(t, []int32{0}, entry.PCSGReplicaIndices["prefill"])
		assert.Equal(t, []int32{0}, entry.PCSGReplicaIndices["decode"])
	})

	t.Run("SPG with single PCSG replica", func(t *testing.T) {
		pg := groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-spg-1"},
			Spec: groveschedulerv1alpha1.PodGangSpec{
				PodGroups: []groveschedulerv1alpha1.PodGroup{
					{Name: "my-pcs-0-prefill-1-pleader", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 1)},
					{Name: "my-pcs-0-prefill-1-pworker", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 3)},
				},
			},
		}

		entry, err := buildEntryFromPodGang(pcs, "old-hash", pg)
		require.NoError(t, err)
		assert.Equal(t, "my-pcs-0-spg-1", entry.Name)
		assert.Empty(t, entry.PodCliques)
		assert.Equal(t, []int32{1}, entry.PCSGReplicaIndices["prefill"])
	})

	t.Run("standalone PCLQ only PodGang", func(t *testing.T) {
		pg := groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-mvu-0"},
			Spec: groveschedulerv1alpha1.PodGangSpec{
				PodGroups: []groveschedulerv1alpha1.PodGroup{
					{Name: "my-pcs-0-frontend", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 2)},
				},
			},
		}

		entry, err := buildEntryFromPodGang(pcs, "new-hash", pg)
		require.NoError(t, err)
		assert.Equal(t, int32(2), entry.PodCliques["frontend"])
		assert.Empty(t, entry.PCSGReplicaIndices)
	})

	t.Run("unknown PodGroup name returns error", func(t *testing.T) {
		pg := groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-bad"},
			Spec: groveschedulerv1alpha1.PodGangSpec{
				PodGroups: []groveschedulerv1alpha1.PodGroup{
					{Name: "my-pcs-0-unknown", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 1)},
				},
			},
		}

		_, err := buildEntryFromPodGang(pcs, "old-hash", pg)
		require.Error(t, err)
	})
}

func TestExtractCliqueName(t *testing.T) {
	pcs := newTestPCS("",
		[]grovecorev1alpha1.PodCliqueTemplateSpec{
			{Name: "frontend"},
			{Name: "pleader"},
			{Name: "pworker"},
		}, nil)

	t.Run("standalone PCLQ FQN", func(t *testing.T) {
		name, err := extractCliqueName("my-pcs-0-frontend", pcs)
		require.NoError(t, err)
		assert.Equal(t, "frontend", name)
	})

	t.Run("PCSG-owned PCLQ FQN", func(t *testing.T) {
		name, err := extractCliqueName("my-pcs-0-prefill-0-pworker", pcs)
		require.NoError(t, err)
		assert.Equal(t, "pworker", name)
	})

	t.Run("unknown name returns error", func(t *testing.T) {
		_, err := extractCliqueName("something-unknown", pcs)
		require.Error(t, err)
	})
}

// TestComputeCoherentUpdateEntries_FirstReconcile tests the first reconcile path where no
// PodGangMap exists yet. Old entries are built from existing PodGang resources, and the
// first MVU iteration is computed.
//
// Starting state:
//
//	PCS "my-pcs" with:
//	  - Frontend (standalone, 5 pods, minAvailable=2) — updated
//	  - Prefill PCSG (pleader+pworker, 2 replicas, minAvailable=1) — updated
//	Existing PodGangs:
//	  - BPG "my-pcs-0": {5 Frontend, 1 Prefill replica (pleader+pworker)}
//	  - SPG "my-pcs-0-spg-1": {1 Prefill replica (pleader+pworker)}
//	No PodGangMap exists yet.
//
// MVU template: {Frontend: 2 pods, Prefill: 1 replica}
//
// Expected: old BPG reduced {F:3, P:0}, SPG unchanged {P:1}, new MVU entry {F:2, P:1}.
func TestComputeCoherentUpdateEntries_PodGangMapNotFound(t *testing.T) {
	oldHash := "oldhash12345"
	newHash := "newhash12345"

	pcs := newTestPCS(newHash,
		[]grovecorev1alpha1.PodCliqueTemplateSpec{
			{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](2)}},
			{Name: "pleader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
			{Name: "pworker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To[int32](2)}},
		},
		[]grovecorev1alpha1.PodCliqueScalingGroupConfig{
			{Name: "prefill", CliqueNames: []string{"pleader", "pworker"}, MinAvailable: ptr.To[int32](1)},
		},
	)
	pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		UpdateStartedAt:               metav1.Now(),
		UpdatedStandalonePodCliques:   []string{"frontend"},
		UpdatedPodCliqueScalingGroups: []string{"prefill"},
	}

	pgLabels := map[string]string{
		"app.kubernetes.io/managed-by":        "grove-operator",
		"app.kubernetes.io/part-of":           "my-pcs",
		"app.kubernetes.io/component":         "podgang",
		"grove.io/podcliqueset-replica-index": "0",
	}

	// BPG: {5F, 1P (pleader+pworker)}
	bpg := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-pcs-0", Namespace: "default", Labels: pgLabels,
			OwnerReferences: []metav1.OwnerReference{{Kind: "PodCliqueSet", Name: "my-pcs", UID: pcs.UID, Controller: ptr.To(true)}},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "my-pcs-0-frontend", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 5)},
				{Name: "my-pcs-0-prefill-0-pleader", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 1)},
				{Name: "my-pcs-0-prefill-0-pworker", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 3)},
			},
		},
	}

	// SPG: {1P (pleader+pworker)}
	spg := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-pcs-0-spg-1", Namespace: "default", Labels: pgLabels,
			OwnerReferences: []metav1.OwnerReference{{Kind: "PodCliqueSet", Name: "my-pcs", UID: pcs.UID, Controller: ptr.To(true)}},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "my-pcs-0-prefill-1-pleader", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 1)},
				{Name: "my-pcs-0-prefill-1-pworker", PodReferences: make([]groveschedulerv1alpha1.NamespacedName, 3)},
			},
		},
	}

	// Live PCLQs with old hash.
	pclqs := []grovecorev1alpha1.PodClique{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-pcs-0-frontend", Namespace: "default",
				Labels:          map[string]string{"app.kubernetes.io/managed-by": "grove-operator", "app.kubernetes.io/part-of": "my-pcs", "grove.io/podcliqueset-replica-index": "0"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "PodCliqueSet", Name: "my-pcs"}},
			},
			Spec:   grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](2)},
			Status: grovecorev1alpha1.PodCliqueStatus{CurrentPodCliqueSetGenerationHash: &oldHash, CurrentPodTemplateHash: ptr.To("old-fe-hash")},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-pcs-0-prefill-0-pleader", Namespace: "default",
				Labels:          map[string]string{"app.kubernetes.io/managed-by": "grove-operator", "app.kubernetes.io/part-of": "my-pcs", "grove.io/podcliqueset-replica-index": "0", "grove.io/podcliquescalinggroup": "my-pcs-0-prefill", "grove.io/podcliquescalinggroup-replica-index": "0"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "PodCliqueScalingGroup", Name: "my-pcs-0-prefill"}},
			},
			Spec:   grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)},
			Status: grovecorev1alpha1.PodCliqueStatus{CurrentPodCliqueSetGenerationHash: &oldHash, CurrentPodTemplateHash: ptr.To("old-pl-hash")},
		},
	}

	cl := testutils.NewTestClientBuilder().WithObjects(pcs, bpg, spg).Build()
	fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
	r := &_resource{client: cl, scheme: groveclientscheme.Scheme, clk: fakeClk}

	template, err := computeMVUTemplate(pcs)
	require.NoError(t, err)
	entries, err := r.computeCoherentUpdateEntries(context.Background(), pcs, 0, "", pclqs, template)
	require.NoError(t, err)

	// Expected: 2 old entries (BPG reduced + SPG) + 1 new MVU entry.
	// BPG: F reduced from 5→3, P reduced from 1→0. Since P=0 but F=3 still present, BPG stays.
	// SPG: P=1 unchanged (deduction took from BPG first).
	// New: MVU {F:2, P:1}.
	require.Len(t, entries, 3)

	// Old BPG (reduced)
	assert.Equal(t, "my-pcs-0", entries[0].Name)
	assert.Equal(t, oldHash, entries[0].PodCliqueSetGenerationHash)
	assert.Equal(t, int32(3), entries[0].PodCliques["frontend"])

	// Old SPG (unchanged) — retains prefill[1].
	assert.Equal(t, "my-pcs-0-spg-1", entries[1].Name)
	assert.Equal(t, oldHash, entries[1].PodCliqueSetGenerationHash)
	assert.Equal(t, []int32{1}, entries[1].PCSGReplicaIndices["prefill"])

	// New MVU entry — absorbs prefill[0] from BPG. Name is <pcs>-<replica>-<unix-nano>
	// where the nano is the FakeClock's value plus the intra-builder counter (0 for first entry).
	expectedName := "my-pcs-0-1000"
	assert.Equal(t, expectedName, entries[2].Name)
	assert.Equal(t, newHash, entries[2].PodCliqueSetGenerationHash)
	assert.Equal(t, int32(2), entries[2].PodCliques["frontend"])
	assert.Equal(t, []int32{0}, entries[2].PCSGReplicaIndices["prefill"])
}

// TestComputeCoherentUpdateEntries_SubsequentReconcile tests the path where PodGangMap
// already exists from a prior iteration. Verifies correct separation by hash and assembly.
//
// Starting state:
//
//	PCS "my-pcs" with Frontend (standalone, 5 pods, minAvail=2) + Prefill PCSG (2 replicas, minAvail=1).
//	PodGangMap exists with:
//	  - Old entry "bpg-0" (old hash): {F:3, P:1} (reduced from first iteration)
//	  - New entry "my-pcs-0-500" (new hash): {F:2, P:1} (from first iteration)
//
// MVU template: {Frontend: 2, Prefill: 1}
// Remaining in old: F=3, P=1. canFormMVU? Yes (3≥2, 1≥1). After deduction: F=1, P=0.
// canFormAnother? No (P=0<1). Absorb F=1. MVU becomes {F:3, P:1}. Old entry removed (all zero).
//
// Expected: 0 old entries + 1 existing new entry + 1 new MVU entry = 2 entries.
func TestComputeCoherentUpdateEntries_SubsequentReconcile(t *testing.T) {
	oldHash := "oldhash12345"
	newHash := "newhash12345"

	pcs := newTestPCS(newHash,
		[]grovecorev1alpha1.PodCliqueTemplateSpec{
			{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](2)}},
			{Name: "pleader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
			{Name: "pworker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To[int32](2)}},
		},
		[]grovecorev1alpha1.PodCliqueScalingGroupConfig{
			{Name: "prefill", CliqueNames: []string{"pleader", "pworker"}, MinAvailable: ptr.To[int32](1)},
		},
	)
	pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		UpdateStartedAt:               metav1.Now(),
		UpdatedStandalonePodCliques:   []string{"frontend"},
		UpdatedPodCliqueScalingGroups: []string{"prefill"},
	}

	// Existing PodGangMap from prior iteration. After the first MVU iteration the BPG holds
	// prefill[1] (prefill[0] was absorbed into the first MPG). The first MPG entry holds prefill[0].
	pgm := &grovecorev1alpha1.PodGangMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-pcs-0", Namespace: "default",
			Labels: getLabels("my-pcs", 0),
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(), Kind: "PodCliqueSet", Name: "my-pcs", UID: pcs.UID, Controller: ptr.To(true)},
			},
		},
		Spec: grovecorev1alpha1.PodGangMapSpec{
			PodCliqueSetReplicaIndex: 0,
			Entries: []grovecorev1alpha1.PodGangEntry{
				{Name: "bpg-0", PodCliqueSetGenerationHash: oldHash, PodCliques: map[string]int32{"frontend": 3}, PCSGReplicaIndices: map[string][]int32{"prefill": {1}}},
				{Name: "my-pcs-0-500", PodCliqueSetGenerationHash: newHash, PodCliques: map[string]int32{"frontend": 2}, PCSGReplicaIndices: map[string][]int32{"prefill": {0}}},
			},
		},
	}

	// Live PCLQ with old hash.
	pclqs := []grovecorev1alpha1.PodClique{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-pcs-0-frontend", Namespace: "default",
				Labels:          map[string]string{"app.kubernetes.io/managed-by": "grove-operator", "app.kubernetes.io/part-of": "my-pcs", "grove.io/podcliqueset-replica-index": "0"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "PodCliqueSet", Name: "my-pcs"}},
			},
			Spec:   grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](2)},
			Status: grovecorev1alpha1.PodCliqueStatus{CurrentPodCliqueSetGenerationHash: &oldHash, CurrentPodTemplateHash: ptr.To("old-fe-hash")},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-pcs-0-prefill-0-pleader", Namespace: "default",
				Labels:          map[string]string{"app.kubernetes.io/managed-by": "grove-operator", "app.kubernetes.io/part-of": "my-pcs", "grove.io/podcliqueset-replica-index": "0", "grove.io/podcliquescalinggroup": "my-pcs-0-prefill", "grove.io/podcliquescalinggroup-replica-index": "0"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "PodCliqueScalingGroup", Name: "my-pcs-0-prefill"}},
			},
			Spec:   grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)},
			Status: grovecorev1alpha1.PodCliqueStatus{CurrentPodCliqueSetGenerationHash: &oldHash, CurrentPodTemplateHash: ptr.To("old-pl-hash")},
		},
	}

	// Prior iteration's MPG has reached Available — required for canAdvanceMVUIteration to permit
	// the next iteration to be minted.
	priorMPG := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-500", Namespace: "default"},
		Status: groveschedulerv1alpha1.PodGangStatus{
			Conditions: []metav1.Condition{{
				Type:               string(groveschedulerv1alpha1.PodGangConditionTypeReady),
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             groveschedulerv1alpha1.ConditionReasonPodGangReady,
			}},
		},
	}

	cl := testutils.NewTestClientBuilder().WithObjects(pcs, pgm, priorMPG).Build()
	fakeClk := clocktesting.NewFakeClock(time.Unix(0, 2000))
	r := &_resource{client: cl, scheme: groveclientscheme.Scheme, clk: fakeClk}

	template, err := computeMVUTemplate(pcs)
	require.NoError(t, err)
	entries, err := r.computeCoherentUpdateEntries(context.Background(), pcs, 0, "my-pcs-0", pclqs, template)
	require.NoError(t, err)

	// Old entry {F:3, P:1}: deduct F:2 + P:1 for MVU → F:1, P:0.
	// canFormAnother? No (P:0<1). Absorb F:1 → MVU becomes {F:3, P:1}. Old entry fully consumed → removed.
	// Result: 0 old + 1 existing new + 1 new this iteration = 2 entries.
	require.Len(t, entries, 2)

	// First: existing new entry from prior iteration (preserved, prefill[0]).
	assert.Equal(t, "my-pcs-0-500", entries[0].Name)
	assert.Equal(t, newHash, entries[0].PodCliqueSetGenerationHash)
	assert.Equal(t, int32(2), entries[0].PodCliques["frontend"])
	assert.Equal(t, []int32{0}, entries[0].PCSGReplicaIndices["prefill"])

	// Second: new MVU from this iteration with absorbed frontend pod and prefill[1]. Name is
	// <pcs>-<replica>-<unix-nano> from the FakeClock + intra-builder counter (0 for first new entry).
	expectedName := "my-pcs-0-2000"
	assert.Equal(t, expectedName, entries[1].Name)
	assert.Equal(t, newHash, entries[1].PodCliqueSetGenerationHash)
	assert.Equal(t, int32(3), entries[1].PodCliques["frontend"])
	assert.Equal(t, []int32{1}, entries[1].PCSGReplicaIndices["prefill"])
}

// TestComputeCoherentUpdateEntries_GateBlocksAdvancementWhenPriorMPGNotAvailable verifies that
// canAdvanceMVUIteration prevents minting another MVU iteration when the prior new-hash MPG's
// PodGang resource has not yet reached PodGangConditionTypeReady. The expected behaviour is
// that computeCoherentUpdateEntries returns the existing PGM contents (oldEntries +
// existingNewEntries) unchanged for this reconcile, deferring the next iteration's mint to a
// later reconcile when the prior PodGang has stabilised.
//
// Setup mirrors TestComputeCoherentUpdateEntries_SubsequentReconcile (PGM has one prior new-hash
// MPG entry plus an old-hash entry to drain) but seeds the prior MPG's PodGang with
// Available=False instead of True.
func TestComputeCoherentUpdateEntries_GateBlocksAdvancementWhenPriorMPGNotAvailable(t *testing.T) {
	oldHash := "oldhash12345"
	newHash := "newhash12345"

	pcs := newTestPCS(newHash,
		[]grovecorev1alpha1.PodCliqueTemplateSpec{
			{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](2)}},
			{Name: "pleader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
			{Name: "pworker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To[int32](2)}},
		},
		[]grovecorev1alpha1.PodCliqueScalingGroupConfig{
			{Name: "prefill", CliqueNames: []string{"pleader", "pworker"}, MinAvailable: ptr.To[int32](1)},
		},
	)
	pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		UpdateStartedAt:               metav1.Now(),
		UpdatedStandalonePodCliques:   []string{"frontend"},
		UpdatedPodCliqueScalingGroups: []string{"prefill"},
	}

	pgm := &grovecorev1alpha1.PodGangMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-pcs-0", Namespace: "default",
			Labels: getLabels("my-pcs", 0),
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(), Kind: "PodCliqueSet", Name: "my-pcs", UID: pcs.UID, Controller: ptr.To(true)},
			},
		},
		Spec: grovecorev1alpha1.PodGangMapSpec{
			PodCliqueSetReplicaIndex: 0,
			Entries: []grovecorev1alpha1.PodGangEntry{
				{Name: "bpg-0", PodCliqueSetGenerationHash: oldHash, PodCliques: map[string]int32{"frontend": 3}, PCSGReplicaIndices: map[string][]int32{"prefill": {1}}},
				{Name: "my-pcs-0-500", PodCliqueSetGenerationHash: newHash, PodCliques: map[string]int32{"frontend": 2}, PCSGReplicaIndices: map[string][]int32{"prefill": {0}}},
			},
		},
	}

	// Prior MPG's PodGang exists but has NOT reached Available — gate must refuse to advance.
	priorMPG := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-500", Namespace: "default"},
		Status: groveschedulerv1alpha1.PodGangStatus{
			Conditions: []metav1.Condition{{
				Type:               string(groveschedulerv1alpha1.PodGangConditionTypeReady),
				Status:             metav1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
				Reason:             "PodGangNotAvailable",
			}},
		},
	}

	pclqs := []grovecorev1alpha1.PodClique{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-pcs-0-frontend", Namespace: "default",
				Labels:          map[string]string{"app.kubernetes.io/managed-by": "grove-operator", "app.kubernetes.io/part-of": "my-pcs", "grove.io/podcliqueset-replica-index": "0"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "PodCliqueSet", Name: "my-pcs"}},
			},
			Spec:   grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](2)},
			Status: grovecorev1alpha1.PodCliqueStatus{CurrentPodCliqueSetGenerationHash: &oldHash, CurrentPodTemplateHash: ptr.To("old-fe-hash")},
		},
	}

	cl := testutils.NewTestClientBuilder().WithObjects(pcs, pgm, priorMPG).Build()
	r := &_resource{client: cl, scheme: groveclientscheme.Scheme, clk: clock.RealClock{}}

	template, err := computeMVUTemplate(pcs)
	require.NoError(t, err)
	entries, err := r.computeCoherentUpdateEntries(context.Background(), pcs, 0, "my-pcs-0", pclqs, template)
	require.NoError(t, err)

	// Gate blocked the advance: returned entries are exactly the existing PGM contents
	// (one old-hash entry + one new-hash entry). No new iteration was minted.
	require.Len(t, entries, 2)
	entryByName := map[string]grovecorev1alpha1.PodGangEntry{}
	for _, e := range entries {
		entryByName[e.Name] = e
	}
	require.Contains(t, entryByName, "bpg-0")
	require.Contains(t, entryByName, "my-pcs-0-500")
	// Old-hash entry preserved unchanged.
	assert.Equal(t, oldHash, entryByName["bpg-0"].PodCliqueSetGenerationHash)
	assert.Equal(t, int32(3), entryByName["bpg-0"].PodCliques["frontend"])
	// Existing new-hash entry preserved unchanged.
	assert.Equal(t, newHash, entryByName["my-pcs-0-500"].PodCliqueSetGenerationHash)
	assert.Equal(t, int32(2), entryByName["my-pcs-0-500"].PodCliques["frontend"])
}

// TestBuildResource_EntriesAreSorted verifies that buildResource sorts PodGangMap entries
// by name regardless of the order they are passed in.
func TestBuildResource_EntriesAreSorted(t *testing.T) {
	pcs := newTestPCS("abc12xyz", nil, nil)

	r := &_resource{scheme: groveclientscheme.Scheme, clk: clock.RealClock{}}

	// Pass entries in deliberately reverse-alphabetical order.
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "my-pcs-0-zzz-hash-2", PodCliques: map[string]int32{"frontend": 1}},
		{Name: "my-pcs-0-mmm-hash-1", PodCliques: map[string]int32{"frontend": 2}},
		{Name: "my-pcs-0-aaa-hash-0", PodCliques: map[string]int32{"frontend": 3}},
	}

	pgm := emptyPodGangMap(client.ObjectKey{Namespace: "default", Name: "my-pcs-0"})
	require.NoError(t, r.buildResource(pgm, pcs, 0, entries))

	require.Len(t, pgm.Spec.Entries, 3)
	assert.Equal(t, "my-pcs-0-aaa-hash-0", pgm.Spec.Entries[0].Name)
	assert.Equal(t, "my-pcs-0-mmm-hash-1", pgm.Spec.Entries[1].Name)
	assert.Equal(t, "my-pcs-0-zzz-hash-2", pgm.Spec.Entries[2].Name)
}

// --- Test helpers ---

// newTestPCS creates a minimal PodCliqueSet for use in unit tests.
func newTestPCS(generationHash string, cliques []grovecorev1alpha1.PodCliqueTemplateSpec, pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig) *grovecorev1alpha1.PodCliqueSet {
	cliquePtrs := make([]*grovecorev1alpha1.PodCliqueTemplateSpec, len(cliques))
	for i := range cliques {
		cliquePtrs[i] = &cliques[i]
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-pcs",
			Namespace: "default",
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques:                      cliquePtrs,
				PodCliqueScalingGroupConfigs: pcsgConfigs,
			},
		},
	}
	if generationHash != "" {
		pcs.Status.CurrentGenerationHash = &generationHash
	}
	return pcs
}

func TestNewPodGangEntryBuilder(t *testing.T) {
	t.Run("name encodes pcs name, replica index, and clock-based suffix", func(t *testing.T) {
		baseTime := time.Unix(0, 1748266985000000000)
		fakeClk := clocktesting.NewFakeClock(baseTime)
		builder := NewPodGangEntryBuilder("my-pcs", 0, "abc123", fakeClk)

		entry := builder(map[string]int32{"worker": 2}, map[string][]int32{"sg": {0}}, nil)

		assert.Equal(t, "my-pcs-0-1748266985000000000", entry.Name)
		assert.Equal(t, "abc123", entry.PodCliqueSetGenerationHash)
		assert.Equal(t, map[string]int32{"worker": 2}, entry.PodCliques)
		assert.Equal(t, map[string][]int32{"sg": {0}}, entry.PCSGReplicaIndices)
		assert.Nil(t, entry.DependsOn)
	})

	t.Run("counter salts successive calls under same nano", func(t *testing.T) {
		// FakeClock does not advance unless Step is called, so successive entries
		// share the same Now() but the +i salt makes their names distinct.
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
		builder := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)

		e1 := builder(nil, nil, nil)
		e2 := builder(nil, nil, nil)
		e3 := builder(nil, nil, nil)

		assert.Equal(t, "my-pcs-0-1000", e1.Name)
		assert.Equal(t, "my-pcs-0-1001", e2.Name)
		assert.Equal(t, "my-pcs-0-1002", e3.Name)
	})

	t.Run("clock advances after construction do not affect subsequent calls", func(t *testing.T) {
		// A builder represents one batch. The epoch is captured once at NewPodGangEntryBuilder
		// and shared across every entry the builder produces. Clock advances mid-batch are
		// ignored so all entries carry the same grove.io/epoch label, only the within-batch
		// counter +i changes between calls.
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
		builder := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)

		e1 := builder(nil, nil, nil)
		fakeClk.Step(500 * time.Nanosecond)
		e2 := builder(nil, nil, nil)

		assert.Equal(t, "my-pcs-0-1000", e1.Name)
		assert.Equal(t, "my-pcs-0-1001", e2.Name)
	})

	t.Run("two builder instances have independent counters", func(t *testing.T) {
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 5000))
		b1 := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)
		b2 := NewPodGangEntryBuilder("my-pcs", 1, "h", fakeClk)

		b1Entry := b1(nil, nil, nil) // i=0 -> 5000
		b2Entry := b2(nil, nil, nil) // i=0 -> 5000 (independent counter)

		assert.Equal(t, "my-pcs-0-5000", b1Entry.Name)
		assert.Equal(t, "my-pcs-1-5000", b2Entry.Name)
		// Different replica index segment guarantees uniqueness across builders even with same suffix.
	})

	t.Run("dependsOn is preserved", func(t *testing.T) {
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
		builder := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)

		e1 := builder(nil, nil, nil)
		e2 := builder(nil, nil, []string{e1.Name})

		assert.Equal(t, []string{e1.Name}, e2.DependsOn)
	})

	t.Run("all entries from one builder share the same grove.io/epoch label", func(t *testing.T) {
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
		builder := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)

		e1 := builder(nil, nil, nil)
		fakeClk.Step(500 * time.Nanosecond)
		e2 := builder(nil, nil, nil)
		e3 := builder(nil, nil, nil)

		assert.Equal(t, "1000", e1.Labels[apicommon.LabelEpoch])
		assert.Equal(t, "1000", e2.Labels[apicommon.LabelEpoch])
		assert.Equal(t, "1000", e3.Labels[apicommon.LabelEpoch])
	})

	t.Run("separate builders produce separate epoch labels", func(t *testing.T) {
		fakeClk := clocktesting.NewFakeClock(time.Unix(0, 1000))
		b1 := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)
		fakeClk.Step(500 * time.Nanosecond)
		b2 := NewPodGangEntryBuilder("my-pcs", 0, "h", fakeClk)

		e1 := b1(nil, nil, nil)
		e2 := b2(nil, nil, nil)

		assert.Equal(t, "1000", e1.Labels[apicommon.LabelEpoch])
		assert.Equal(t, "1500", e2.Labels[apicommon.LabelEpoch])
	})
}
