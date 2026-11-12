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
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestComputeChangedScope verifies that a component is in scope exactly when one of its PodCliques gets a new pod
// template hash once it is written from the update revision.
func TestComputeChangedScope(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "pcs-uid").
		WithStandaloneCliqueReplicas("frontend", 3).
		WithStandaloneCliqueReplicas("router", 1).
		WithScalingGroupConfig("decode", []string{"decodeworker"}, 3, 1).
		Build()
	const updateRevision = "test-pcs-update"
	unchanged := newTestRevision(t, pcs, &pcs.Spec.Template, 1)
	changedTemplate := pcs.Spec.Template.DeepCopy()
	for _, clique := range changedTemplate.Cliques {
		clique.Spec.PodSpec.NodeSelector = map[string]string{"pool": "older"}
	}
	changed := newTestRevision(t, pcs, changedTemplate, 2)
	hashOf := func(cliqueName string) string {
		return componentutils.ComputePCLQPodTemplateHash(componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName), pcs.Spec.Template.PriorityClassName)
	}
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-decode", testNamespace, testPCSName, 0).
		WithOwnerReference(apiconstants.KindPodCliqueSet, testPCSName, pcs.UID).Build()
	pcsg.UID = "pcsg-uid"
	standalone := func(cliqueName string, labels map[string]string) *grovecorev1beta1.PodClique {
		return newStandalonePCLQ(pcs, cliqueName, labels)
	}
	member := func(labels map[string]string) *grovecorev1beta1.PodClique {
		return testutils.NewPCSGPodCliqueBuilder("test-pcs-0-decode-0-decodeworker", testNamespace, testPCSName, pcsg.Name, 0, 0).
			WithOwnerReference(apiconstants.KindPodCliqueScalingGroup, pcsg.Name, pcsg.UID).
			WithLabels(labels).Build()
	}
	at := func(revision *appsv1.ControllerRevision, hash string) map[string]string {
		return map[string]string{apicommon.LabelPodCliqueSetRevision: revision.Name, apicommon.LabelPodTemplateHash: hash}
	}

	testCases := []struct {
		description       string
		pclqs             []client.Object
		wantStandalone    []string
		wantScalingGroups []string
	}{
		{
			description: "a standalone clique and a scaling group constituent both changed",
			pclqs: []client.Object{
				standalone("frontend", at(changed, "older-frontend")),
				standalone("router", at(unchanged, "foreign-router")),
				member(at(changed, "older-decodeworker")),
			},
			wantStandalone:    []string{"frontend"},
			wantScalingGroups: []string{"decode"},
		},
		{
			description: "only the scaling group constituent changed",
			pclqs: []client.Object{
				standalone("frontend", at(unchanged, "foreign-frontend")),
				member(at(changed, "older-decodeworker")),
			},
			wantStandalone:    []string{},
			wantScalingGroups: []string{"decode"},
		},
		{
			description: "nothing changed although every hash comes from another build",
			pclqs: []client.Object{
				standalone("frontend", at(unchanged, "foreign-frontend")),
				standalone("router", map[string]string{apicommon.LabelPodCliqueSetRevision: updateRevision, apicommon.LabelPodTemplateHash: "foreign-router"}),
				member(at(unchanged, "foreign-decodeworker")),
			},
			wantStandalone:    []string{},
			wantScalingGroups: []string{},
		},
		{
			description: "a PodClique without a revision label is compared by its hash",
			pclqs: []client.Object{
				standalone("frontend", map[string]string{apicommon.LabelPodTemplateHash: "older-frontend"}),
				standalone("router", map[string]string{apicommon.LabelPodTemplateHash: hashOf("router")}),
			},
			wantStandalone:    []string{"frontend"},
			wantScalingGroups: []string{},
		},
		{
			description: "a PodClique of another PodCliqueSet with the same name is ignored",
			pclqs: []client.Object{
				testutils.NewPodCliqueBuilder(testPCSName, "other-pcs-uid", "frontend", testNamespace, 0).
					WithLabels(at(changed, "older-frontend")).Build(),
			},
			wantStandalone:    []string{},
			wantScalingGroups: []string{},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			objects := append([]client.Object{pcs, pcsg, unchanged, changed}, tc.pclqs...)
			r := &Reconciler{client: testutils.SetupFakeClient(objects...)}
			scope, err := r.computeChangedScope(context.Background(), pcs, updateRevision)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStandalone, sets.List(scope.standalonePCLQs))
			assert.Equal(t, tc.wantScalingGroups, sets.List(scope.podCliqueScalingGroups))
		})
	}
}

func TestPreviousPendingInScope(t *testing.T) {
	const currentHash = "v1"
	pcs := &grovecorev1beta1.PodCliqueSet{
		Status: grovecorev1beta1.PodCliqueSetStatus{
			CurrentGenerationHash: ptr.To(currentHash),
			UpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				InScopeStandalonePodCliques:   []string{"frontend"},
				InScopePodCliqueScalingGroups: []string{"decode"},
			},
		},
	}
	// An old-generation entry carrying an in-scope standalone (frontend) and PCSG (decode), plus components
	// a pending replica happens to carry but that were never in scope (router, prefill).
	oldEntry := grovecorev1beta1.PodGangEntry{
		PodCliqueSetGenerationHash: "v0",
		PodCliques:                 map[string]int32{"frontend": 1, "router": 1},
		PCSGReplicaIndices:         map[string][]int32{"decode": {0}, "prefill": {0}},
	}
	currentEntry := grovecorev1beta1.PodGangEntry{
		PodCliqueSetGenerationHash: currentHash,
		PodCliques:                 map[string]int32{"frontend": 2},
		PCSGReplicaIndices:         map[string][]int32{"decode": {0, 1}},
	}
	pgmWith := func(entries ...grovecorev1beta1.PodGangEntry) grovecorev1beta1.PodGangMap {
		return grovecorev1beta1.PodGangMap{Spec: grovecorev1beta1.PodGangMapSpec{Entries: entries}}
	}

	t.Run("previous-scope components still on old entries are pending, others are excluded", func(t *testing.T) {
		pending := previousPendingInScope(pcs, []grovecorev1beta1.PodGangMap{pgmWith(oldEntry, currentEntry)}, currentHash)
		assert.Equal(t, []string{"frontend"}, sets.List(pending.standalonePCLQs))
		assert.Equal(t, []string{"decode"}, sets.List(pending.podCliqueScalingGroups))
	})

	t.Run("a component fully migrated to the in-flight update's target is not carried forward", func(t *testing.T) {
		pending := previousPendingInScope(pcs, []grovecorev1beta1.PodGangMap{pgmWith(currentEntry)}, currentHash)
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

// TestWithPreviousPendingScope verifies that a new edit during a coherent update keeps the components the update has
// not finished rolling.
func TestWithPreviousPendingScope(t *testing.T) {
	// A coherent update targeting v1 is in flight with frontend in scope and still migrating (it has a v0 entry). A
	// new edit now changes router. The scope must carry frontend forward and add router.
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStandaloneCliqueReplicas("frontend", 3).
		WithStandaloneCliqueReplicas("router", 1).
		WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.CoherentStrategy}).
		WithPodCliqueSetGenerationHash(ptr.To("v1")).
		WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{InScopeStandalonePodCliques: []string{"frontend"}}).
		Build()
	pgm := testutils.NewPodGangMapBuilder(testPCSName, testNamespace, "uid", 0).
		WithEntries(
			grovecorev1beta1.PodGangEntry{PodCliqueSetGenerationHash: "v0", PodCliques: map[string]int32{"frontend": 1}},
			grovecorev1beta1.PodGangEntry{PodCliqueSetGenerationHash: "v1", PodCliques: map[string]int32{"frontend": 2, "router": 1}},
		).Build()
	r := &Reconciler{client: testutils.SetupFakeClient(pcs, pgm)}
	changed := updateScope{standalonePCLQs: sets.New("router"), podCliqueScalingGroups: sets.New[string]()}

	scope, err := r.withPreviousPendingScope(context.Background(), pcs, changed)

	require.NoError(t, err)
	assert.Equal(t, []string{"frontend", "router"}, sets.List(scope.standalonePCLQs))
	assert.Empty(t, sets.List(scope.podCliqueScalingGroups))

	t.Run("no coherent update in progress keeps the scope", func(t *testing.T) {
		idle := pcs.DeepCopy()
		idle.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
		scope, err := r.withPreviousPendingScope(context.Background(), idle, changed)
		require.NoError(t, err)
		assert.Equal(t, []string{"router"}, sets.List(scope.standalonePCLQs))
	})
}
