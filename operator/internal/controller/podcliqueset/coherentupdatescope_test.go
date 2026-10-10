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

package podcliqueset

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
)

func TestNewlyChangedInScope(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStandaloneCliqueReplicas("frontend", 3).
		WithStandaloneCliqueReplicas("router", 1).
		WithScalingGroupConfig("decode", []string{"decodeworker"}, 3, 1).
		Build()
	hashOf := func(cliqueName string) string {
		return componentutils.ComputePCLQPodTemplateHash(componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName), pcs.Spec.Template.PriorityClassName)
	}
	testCases := []struct {
		description       string
		deployedHash      map[string]string
		wantStandalone    []string
		wantScalingGroups []string
	}{
		{
			description:       "a standalone clique and a scaling group constituent both changed",
			deployedHash:      map[string]string{"frontend": "stale", "router": hashOf("router"), "decodeworker": "stale"},
			wantStandalone:    []string{"frontend"},
			wantScalingGroups: []string{"decode"},
		},
		{
			description:       "only the scaling group constituent changed",
			deployedHash:      map[string]string{"frontend": hashOf("frontend"), "router": hashOf("router"), "decodeworker": "stale"},
			wantStandalone:    []string{},
			wantScalingGroups: []string{"decode"},
		},
		{
			description:       "nothing changed",
			deployedHash:      map[string]string{"frontend": hashOf("frontend"), "router": hashOf("router"), "decodeworker": hashOf("decodeworker")},
			wantStandalone:    []string{},
			wantScalingGroups: []string{},
		},
		{
			description:       "a clique with no deployed hash counts as changed",
			deployedHash:      map[string]string{"router": hashOf("router"), "decodeworker": hashOf("decodeworker")},
			wantStandalone:    []string{"frontend"},
			wantScalingGroups: []string{},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			scope := newlyChangedInScope(pcs, tc.deployedHash)
			assert.Equal(t, tc.wantStandalone, sets.List(scope.standalonePCLQs))
			assert.Equal(t, tc.wantScalingGroups, sets.List(scope.podCliqueScalingGroups))
		})
	}
}

func TestPreviousPendingInScope(t *testing.T) {
	const currentHash = "v1"
	pcs := &grovecorev1alpha1.PodCliqueSet{
		Status: grovecorev1alpha1.PodCliqueSetStatus{
			CurrentGenerationHash: ptr.To(currentHash),
			UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				InScopeStandalonePodCliques:   []string{"frontend"},
				InScopePodCliqueScalingGroups: []string{"decode"},
			},
		},
	}
	// An old-generation entry carrying an in-scope standalone (frontend) and PCSG (decode), plus components
	// a pending replica happens to carry but that were never in scope (router, prefill).
	oldEntry := grovecorev1alpha1.PodGangEntry{
		PodCliqueSetGenerationHash: "v0",
		PodCliques:                 map[string]int32{"frontend": 1, "router": 1},
		PCSGReplicaIndices:         map[string][]int32{"decode": {0}, "prefill": {0}},
	}
	currentEntry := grovecorev1alpha1.PodGangEntry{
		PodCliqueSetGenerationHash: currentHash,
		PodCliques:                 map[string]int32{"frontend": 2},
		PCSGReplicaIndices:         map[string][]int32{"decode": {0, 1}},
	}
	pgmWith := func(entries ...grovecorev1alpha1.PodGangEntry) grovecorev1alpha1.PodGangMap {
		return grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: entries}}
	}

	t.Run("previous-scope components still on old entries are pending, others are excluded", func(t *testing.T) {
		pending := previousPendingInScope(pcs, []grovecorev1alpha1.PodGangMap{pgmWith(oldEntry, currentEntry)}, currentHash)
		assert.Equal(t, []string{"frontend"}, sets.List(pending.standalonePCLQs))
		assert.Equal(t, []string{"decode"}, sets.List(pending.podCliqueScalingGroups))
	})

	t.Run("a component fully migrated to the in-flight update's target is not carried forward", func(t *testing.T) {
		pending := previousPendingInScope(pcs, []grovecorev1alpha1.PodGangMap{pgmWith(currentEntry)}, currentHash)
		assert.Empty(t, sets.List(pending.standalonePCLQs))
		assert.Empty(t, sets.List(pending.podCliqueScalingGroups))
	})
}

func TestMergeInScopes(t *testing.T) {
	newInScope := updateScope{
		standalonePCLQs:        sets.New("frontend"),
		podCliqueScalingGroups: sets.New("decode"),
	}
	previousInScope := updateScope{
		standalonePCLQs:        sets.New("router"),
		podCliqueScalingGroups: sets.New("decode", "prefill"),
	}
	merged := mergeInScopes(newInScope, previousInScope)
	assert.Equal(t, []string{"frontend", "router"}, sets.List(merged.standalonePCLQs))
	assert.Equal(t, []string{"decode", "prefill"}, sets.List(merged.podCliqueScalingGroups))
}

func TestComputeCoherentUpdateScope(t *testing.T) {
	pcsBuilder := func() *testutils.PodCliqueSetBuilder {
		return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 3).
			WithStandaloneCliqueReplicas("router", 1)
	}
	hashOf := func(pcs *grovecorev1alpha1.PodCliqueSet, cliqueName string) string {
		return componentutils.ComputePCLQPodTemplateHash(componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName), pcs.Spec.Template.PriorityClassName)
	}
	pclqWithHash := func(cliqueName, podTemplateHash string) *grovecorev1alpha1.PodClique {
		return testutils.NewPodCliqueBuilder(testPCSName, "uid", cliqueName, testNamespace, 0).
			WithLabels(map[string]string{apicommon.LabelPodTemplateHash: podTemplateHash}).Build()
	}

	t.Run("fresh update scopes only the components whose pod template changed", func(t *testing.T) {
		pcs := pcsBuilder().Build()
		// frontend's deployed label is stale (changed), router's matches the new hash (unchanged).
		r := &Reconciler{client: testutils.SetupFakeClient(pcs,
			pclqWithHash("frontend", "stale-hash"),
			pclqWithHash("router", hashOf(pcs, "router")))}

		scope, err := r.computeCoherentUpdateScope(context.Background(), pcs)

		require.NoError(t, err)
		assert.Equal(t, []string{"frontend"}, sets.List(scope.standalonePCLQs))
		assert.Empty(t, sets.List(scope.podCliqueScalingGroups))
	})

	t.Run("mid-flight update merges the newly changed and the still-pending components", func(t *testing.T) {
		// A coherent update targeting v1 is in flight with frontend in scope and still migrating (it has a
		// v0 entry). A new edit now changes router. The scope must carry frontend forward and add router.
		pcs := pcsBuilder().
			WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}).
			WithPodCliqueSetGenerationHash(ptr.To("v1")).
			WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{InScopeStandalonePodCliques: []string{"frontend"}}).
			Build()
		pgm := testutils.NewPodGangMapBuilder(testPCSName, testNamespace, "uid", 0).
			WithEntries(
				grovecorev1alpha1.PodGangEntry{PodCliqueSetGenerationHash: "v0", PodCliques: map[string]int32{"frontend": 1}},
				grovecorev1alpha1.PodGangEntry{PodCliqueSetGenerationHash: "v1", PodCliques: map[string]int32{"frontend": 2, "router": 1}},
			).Build()
		r := &Reconciler{client: testutils.SetupFakeClient(pcs, pgm,
			pclqWithHash("frontend", hashOf(pcs, "frontend")), // unchanged by this edit, carried via pending
			pclqWithHash("router", "stale-hash"))}             // changed by this edit

		scope, err := r.computeCoherentUpdateScope(context.Background(), pcs)

		require.NoError(t, err)
		assert.Equal(t, []string{"frontend", "router"}, sets.List(scope.standalonePCLQs))
		assert.Empty(t, sets.List(scope.podCliqueScalingGroups))
	})
}
