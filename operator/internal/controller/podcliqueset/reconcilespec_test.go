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

package podcliqueset

import (
	"context"
	"sync"
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestUpdateObservedGeneration tests the updateObservedGeneration function for PodCliqueSet
func TestUpdateObservedGeneration(t *testing.T) {
	tests := []struct {
		name               string
		setupPCS           func() *grovecorev1alpha1.PodCliqueSet
		expectPatchSkipped bool
		expectedGeneration int64
	}{
		{
			name: "patch_skipped_when_observed_equals_generation",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(1).
					Build()
				pcs.Generation = 5
				pcs.Status.ObservedGeneration = ptr.To(int64(5))
				return pcs
			},
			expectPatchSkipped: true,
			expectedGeneration: 5,
		},
		{
			name: "patch_made_when_observed_is_nil",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(1).
					Build()
				pcs.Generation = 3
				pcs.Status.ObservedGeneration = nil
				return pcs
			},
			expectPatchSkipped: false,
			expectedGeneration: 3,
		},
		{
			name: "patch_made_when_observed_differs_from_generation",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(1).
					Build()
				pcs.Generation = 7
				pcs.Status.ObservedGeneration = ptr.To(int64(4))
				return pcs
			},
			expectPatchSkipped: false,
			expectedGeneration: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := tt.setupPCS()
			originalObservedGen := pcs.Status.ObservedGeneration

			fakeClient := testutils.SetupFakeClient(pcs)
			reconciler := &Reconciler{client: fakeClient}

			result := reconciler.updateObservedGeneration(context.Background(), logr.Discard(), pcs)

			require.False(t, result.HasErrors(), "updateObservedGeneration should not return errors")

			// Verify the result continues reconciliation
			_, err := result.Result()
			assert.NoError(t, err)

			// Fetch the updated object from the fake client
			updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
			err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updatedPCS)
			require.NoError(t, err)

			// Verify ObservedGeneration is set correctly
			require.NotNil(t, updatedPCS.Status.ObservedGeneration, "ObservedGeneration should not be nil after update")
			assert.Equal(t, tt.expectedGeneration, *updatedPCS.Status.ObservedGeneration)

			if tt.expectPatchSkipped {
				// If patch was skipped, the ObservedGeneration should remain unchanged
				assert.Equal(t, originalObservedGen, updatedPCS.Status.ObservedGeneration)
			}
		})
	}
}

// TestGetKindSyncGroups tests that every expected component kind appears in exactly one
// sync group and that the number of groups is at least 1.
func TestGetKindSyncGroups(t *testing.T) {
	groups := getKindSyncGroups()
	assert.NotEmpty(t, groups)

	expectedKinds := []component.Kind{
		component.KindServiceAccount,
		component.KindRole,
		component.KindRoleBinding,
		component.KindServiceAccountTokenSecret,
		component.KindHeadlessService,
		component.KindHorizontalPodAutoscaler,
		component.KindPodCliqueSetReplica,
		component.KindComputeDomain,
		component.KindResourceClaim,
		component.KindPodClique,
		component.KindPodCliqueScalingGroup,
		component.KindPodGang,
	}
	seen := make(map[component.Kind]bool)
	for _, group := range groups {
		for _, k := range group {
			assert.False(t, seen[k], "kind %s appeared in more than one group", k)
			seen[k] = true
		}
	}
	for _, k := range expectedKinds {
		assert.True(t, seen[k], "kind %s should appear in some group", k)
	}
}

// TestComputeGenerationHash_ReplicaOnlyAndCliqueReorderAreNoOps documents the
// invariants of computeGenerationHash that protect against false rolling
// updates:
//
//  1. replica-only changes do not change the hash (HPA-style scale events
//     must not trigger a gang roll); and
//  2. reordering pcs.Spec.Template.Cliques does not change the hash, because
//     Cliques is +listType=map +listMapKey=name and represents a name-keyed
//     set with no inherent order. An upstream operator that emits the same
//     cliques in a different sequence (e.g. from non-deterministic Go map
//     iteration) must not cause Grove to roll the gang.
//
// This was previously TestComputeGenerationHashCurrentSensitivity and
// asserted the bugged behavior. After fixing computeGenerationHash to sort
// the cliques map-list before hashing, the assertion is flipped: the
// reordered hash must be IDENTICAL to the original.
func TestComputeGenerationHash_ReplicaOnlyAndCliqueReorderAreNoOps(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("frontend").
			WithLabels(map[string]string{"role": "frontend"}).
			WithReplicas(1).
			Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("planner").
			WithLabels(map[string]string{"role": "planner"}).
			WithReplicas(1).
			Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("prefill").
			WithLabels(map[string]string{"role": "prefill"}).
			WithReplicas(1).
			Build()).
		Build()

	originalHash := computeGenerationHash(pcs)

	replicaOnlyChange := pcs.DeepCopy()
	replicaOnlyChange.Spec.Template.Cliques[2].Spec.Replicas = 3
	assert.Equal(t, originalHash, computeGenerationHash(replicaOnlyChange),
		"replica-only changes are intentionally excluded from the generation hash")

	reordered := pcs.DeepCopy()
	reordered.Spec.Template.Cliques = []*grovecorev1alpha1.PodCliqueTemplateSpec{
		pcs.Spec.Template.Cliques[2].DeepCopy(),
		pcs.Spec.Template.Cliques[0].DeepCopy(),
		pcs.Spec.Template.Cliques[1].DeepCopy(),
	}
	assert.Equal(t, originalHash, computeGenerationHash(reordered),
		"clique slice order must not affect the generation hash — Cliques is +listType=map keyed by name")
}

// TestProcessGenerationHashChange_CliqueReorderIsNoOp asserts the
// fixed-behavior end-to-end: when an existing PCS already has its current
// generation hash recorded and only the cliques map-list is reordered,
// processGenerationHashChange must NOT initialize a new UpdateProgress
// (which would cascade into a full gang rolling recreate via the
// PodCliqueSetReplica orchestrator and the per-PodClique controllers).
//
// This was previously TestProcessGenerationHashChangeTreatsCliqueReorderAsUpdate
// asserting the bugged behavior. After fixing computeGenerationHash, the
// assertion is flipped: UpdateProgress must remain nil.
func TestProcessGenerationHashChange_CliqueReorderIsNoOp(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("frontend").
			WithLabels(map[string]string{"role": "frontend"}).
			Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("planner").
			WithLabels(map[string]string{"role": "planner"}).
			Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("prefill").
			WithLabels(map[string]string{"role": "prefill"}).
			Build()).
		Build()
	originalHash := computeGenerationHash(pcs)

	reordered := pcs.DeepCopy()
	reordered.Spec.Template.Cliques = []*grovecorev1alpha1.PodCliqueTemplateSpec{
		pcs.Spec.Template.Cliques[2].DeepCopy(),
		pcs.Spec.Template.Cliques[0].DeepCopy(),
		pcs.Spec.Template.Cliques[1].DeepCopy(),
	}
	reordered.Status.CurrentGenerationHash = ptr.To(originalHash)

	fakeClient := testutils.SetupFakeClient(reordered)
	reconciler := &Reconciler{
		client:                        fakeClient,
		pcsGenerationHashExpectations: sync.Map{},
	}

	result := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), reordered)
	require.False(t, result.HasErrors())

	updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
	err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(reordered), updatedPCS)
	require.NoError(t, err)
	assert.Nil(t, updatedPCS.Status.UpdateProgress,
		"clique reorder must not start a rolling update — Cliques is name-keyed and slice order is not part of the desired state")
}

// TestComputeGenerationHash_InOrderStartupIsSensitiveToCliqueOrder pins the
// counterpart to the AnyOrder/Explicit invariant: when the PCS uses
// CliqueStartupTypeInOrder, the slice order DOES carry semantic meaning (it
// defines the startup chain), so the generation hash must change if the
// slice is reordered. Otherwise a real change to the startup ordering would
// silently be ignored.
func TestComputeGenerationHash_InOrderStartupIsSensitiveToCliqueOrder(t *testing.T) {
	startupInOrder := grovecorev1alpha1.CliqueStartupTypeInOrder

	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithCliqueStartupType(&startupInOrder).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("frontend").
			WithLabels(map[string]string{"role": "frontend"}).
			Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("planner").
			WithLabels(map[string]string{"role": "planner"}).
			Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("prefill").
			WithLabels(map[string]string{"role": "prefill"}).
			Build()).
		Build()
	originalHash := computeGenerationHash(pcs)

	reordered := pcs.DeepCopy()
	reordered.Spec.Template.Cliques = []*grovecorev1alpha1.PodCliqueTemplateSpec{
		pcs.Spec.Template.Cliques[2].DeepCopy(),
		pcs.Spec.Template.Cliques[0].DeepCopy(),
		pcs.Spec.Template.Cliques[1].DeepCopy(),
	}
	assert.NotEqual(t, originalHash, computeGenerationHash(reordered),
		"under CliqueStartupTypeInOrder, slice order encodes the startup chain — a different order is a real spec change")
}

// TestComputeGenerationHash_AnyOrderEqualsExplicit_WhenPodSpecsMatch pins that
// AnyOrder and Explicit do not add synthetic startup metadata to the generation
// hash. Their slice order is not part of the template hash input, and
// StartupType itself is immutable after creation, so including it would only
// broaden upgrade-time hash churn.
func TestComputeGenerationHash_AnyOrderEqualsExplicit_WhenPodSpecsMatch(t *testing.T) {
	startupAnyOrder := grovecorev1alpha1.CliqueStartupTypeAnyOrder
	startupExplicit := grovecorev1alpha1.CliqueStartupTypeExplicit

	build := func(st *grovecorev1alpha1.CliqueStartupType) *grovecorev1alpha1.PodCliqueSet {
		return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
			WithCliqueStartupType(st).
			WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("frontend").Build()).
			WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("planner").Build()).
			WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("prefill").Build()).
			Build()
	}
	hashAnyOrder := computeGenerationHash(build(&startupAnyOrder))
	hashExplicit := computeGenerationHash(build(&startupExplicit))

	assert.Equal(t, hashAnyOrder, hashExplicit,
		"AnyOrder and Explicit must not add startup markers that change hashes for otherwise identical templates")
}

// TestComputeGenerationHash_InOrderToAnyOrderFlipsHash pins that switching
// the StartupType from InOrder to AnyOrder is captured by the hash. Under
// InOrder the slice order encodes the startup chain; under AnyOrder it
// doesn't. This is the boundary case for the canonicalization split — a
// regression here would mean a real semantic change in startup behavior
// goes silently undetected by the rolling-update path.
func TestComputeGenerationHash_InOrderToAnyOrderFlipsHash(t *testing.T) {
	startupInOrder := grovecorev1alpha1.CliqueStartupTypeInOrder
	startupAnyOrder := grovecorev1alpha1.CliqueStartupTypeAnyOrder

	build := func(st *grovecorev1alpha1.CliqueStartupType) *grovecorev1alpha1.PodCliqueSet {
		return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
			WithCliqueStartupType(st).
			WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("frontend").Build()).
			WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("planner").Build()).
			WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("prefill").Build()).
			Build()
	}
	assert.NotEqual(t, computeGenerationHash(build(&startupInOrder)), computeGenerationHash(build(&startupAnyOrder)),
		"InOrder ↔ AnyOrder changes startup semantics and must flip the hash")
}

// TestComputeGenerationHash_CombinedReplicaChangeAndCliqueReorderIsNoOp
// covers the realistic Dynamo-operator scenario where a single PCS write
// both bumps replicas (because the Planner asked for a scale-up) AND happens
// to emit cliques in a different order (because the upstream operator
// constructed them from a Go map). Both axes are individually no-ops; they
// must remain a no-op when combined.
func TestComputeGenerationHash_CombinedReplicaChangeAndCliqueReorderIsNoOp(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("frontend").
			WithLabels(map[string]string{"role": "frontend"}).WithReplicas(1).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("planner").
			WithLabels(map[string]string{"role": "planner"}).WithReplicas(1).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("prefill").
			WithLabels(map[string]string{"role": "prefill"}).WithReplicas(1).Build()).
		Build()
	originalHash := computeGenerationHash(pcs)

	combined := pcs.DeepCopy()
	// Reorder cliques (Cliques is +listType=map keyed by name, no semantic
	// order under default AnyOrder StartupType).
	combined.Spec.Template.Cliques = []*grovecorev1alpha1.PodCliqueTemplateSpec{
		pcs.Spec.Template.Cliques[2].DeepCopy(),
		pcs.Spec.Template.Cliques[0].DeepCopy(),
		pcs.Spec.Template.Cliques[1].DeepCopy(),
	}
	// Bump replicas on one of the cliques (replica-only changes are
	// intentionally excluded from the generation hash).
	combined.Spec.Template.Cliques[0].Spec.Replicas = 5

	assert.Equal(t, originalHash, computeGenerationHash(combined),
		"replica bump + clique reorder is the realistic latency-mode scale-up patch shape — neither axis must flip the generation hash")
}

// TestComputeGenerationHash_RealCliqueTemplateChangeFlipsHash is the
// regression test in the other direction: if a real per-clique PodSpec
// change happens (e.g. an image bump rolled out via the DGD) the
// generation hash must change. Otherwise the rolling update would never
// kick off.
func TestComputeGenerationHash_RealCliqueTemplateChangeFlipsHash(t *testing.T) {
	build := func(image string) *grovecorev1alpha1.PodCliqueSet {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
			WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("frontend").Build()).
			WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").Build()).
			Build()
		pcs.Spec.Template.Cliques[1].Spec.PodSpec = corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: image}},
		}
		return pcs
	}
	assert.NotEqual(t, computeGenerationHash(build("worker:v1")), computeGenerationHash(build("worker:v2")),
		"a real per-clique image change must flip the generation hash; otherwise rolling updates would never start")
}

// TestInitUpdateProgress tests the initUpdateProgress function for both OnDelete and RollingRecreate strategies
func TestInitUpdateProgress(t *testing.T) {
	tests := []struct {
		name                   string
		setupPCS               func() *grovecorev1alpha1.PodCliqueSet
		expectUpdateEndedAtSet bool
	}{
		{
			name: "rolling_recreate_strategy_does_not_set_update_ended_at",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.RollingRecreateStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					Build()
				pcs.Status.UpdatedReplicas = 3 // should be reset to 0
				return pcs
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "nil_update_strategy_does_not_set_update_ended_at",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					Build()
				pcs.Status.UpdatedReplicas = 2 // should be reset to 0
				return pcs
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "on_delete_strategy_sets_update_ended_at_immediately",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.OnDeleteStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					Build()
				pcs.Status.UpdatedReplicas = 5 // should be reset to 0
				return pcs
			},
			expectUpdateEndedAtSet: true,
		},
		{
			name: "existing_rollingrecreate_in_progress_is_reset_when_hash_changes",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				updateStartedAt := metav1.Now()
				replicaUpdateStartedAt := metav1.Time{Time: updateStartedAt.Add(time.Second)}

				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(3).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.RollingRecreateStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt:               updateStartedAt,
						UpdatedPodCliqueScalingGroups: []string{"pcsg-1", "pcsg-2"},
						UpdatedPodCliques:             []string{"pclq-1", "pclq-2", "pclq-3"},
						CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
							{
								ReplicaIndex:    1,
								UpdateStartedAt: replicaUpdateStartedAt,
							},
						},
					}).
					Build()
				pcs.Status.UpdatedReplicas = 2
				return pcs
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "existing_on_delete_update_is_reset_when_hash_changes",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				updateStartedAt := metav1.Now()

				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(3).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.OnDeleteStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt:               updateStartedAt,
						UpdateEndedAt:                 ptr.To(updateStartedAt),
						UpdatedPodCliqueScalingGroups: []string{"pcsg-1"},
						UpdatedPodCliques:             []string{"pclq-1", "pclq-2"},
					}).
					Build()
				pcs.Status.UpdatedReplicas = 1
				return pcs
			},
			expectUpdateEndedAtSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := tt.setupPCS()

			fakeClient := testutils.SetupFakeClient(pcs)
			reconciler := &Reconciler{
				client:                        fakeClient,
				pcsGenerationHashExpectations: sync.Map{},
			}

			newGenerationHash := "new-hash"
			pcsObjectName := pcs.Namespace + "/" + pcs.Name

			err := reconciler.initUpdateProgress(context.Background(), pcs, pcsObjectName, newGenerationHash)
			require.NoError(t, err, "initUpdateProgress should not return errors")

			// Fetch the updated PCS from the fake client
			updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
			err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updatedPCS)
			require.NoError(t, err)

			// Verify UpdateProgress is set
			require.NotNil(t, updatedPCS.Status.UpdateProgress, "UpdateProgress should be set")
			assert.NotEmpty(t, updatedPCS.Status.UpdateProgress.UpdateStartedAt, "UpdateStartedAt should be set")

			// Verify UpdateEndedAt based on strategy
			if tt.expectUpdateEndedAtSet {
				require.NotNil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be set for OnDelete strategy")
			} else {
				assert.Nil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be nil for non-OnDelete strategy")
			}

			assert.Nil(t, updatedPCS.Status.UpdateProgress.UpdatedPodCliqueScalingGroups, "UpdatedPodCliqueScalingGroups should be nil")
			assert.Nil(t, updatedPCS.Status.UpdateProgress.UpdatedPodCliques, "UpdatedPodCliques should be nil")
			// Currently updating is not set by the initUpdateProgress function
			assert.Nil(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating, "CurrentlyUpdating should be nil")
			assert.Equal(t, int32(0), updatedPCS.Status.UpdatedReplicas, "UpdatedReplicas should be reset to 0")
			require.NotNil(t, updatedPCS.Status.CurrentGenerationHash)
			assert.Equal(t, newGenerationHash, *updatedPCS.Status.CurrentGenerationHash)
		})
	}
}
