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

package podcliquesetreplica

import (
	"errors"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestInitializePCSGenerationBaseline(t *testing.T) {
	t.Parallel()
	startedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	tests := []struct {
		name            string
		currentHash     *string
		appliedHash     *string
		progress        *grovecorev1beta1.PodCliqueSetUpdateProgress
		wantInitialized bool
		wantCurrent     string
		wantApplied     string
		wantTarget      string
	}{
		{name: "fresh PCS", wantInitialized: true, wantCurrent: "combined", wantApplied: "desired"},
		{name: "idle legacy identity is preserved", currentHash: new("legacy"), wantInitialized: true, wantCurrent: "legacy", wantApplied: "desired"},
		{
			name: "active legacy target is filled from the applied baseline", currentHash: new("legacy"), appliedHash: new("applied"),
			progress:        &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: startedAt},
			wantInitialized: true, wantCurrent: "legacy", wantApplied: "applied", wantTarget: "applied",
		},
		{
			name: "recorded target reconstructs missing applied metadata", currentHash: new("legacy"),
			progress:        &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: startedAt, TargetSchedulingConfigurationHash: "recorded"},
			wantInitialized: true, wantCurrent: "legacy", wantApplied: "recorded", wantTarget: "recorded",
		},
		{
			name: "missing applied metadata and target are filled together", currentHash: new("legacy"),
			progress:        &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: startedAt},
			wantInitialized: true, wantCurrent: "legacy", wantApplied: "desired", wantTarget: "desired",
		},
		{
			name: "completed history is preserved", currentHash: new("legacy"),
			progress:        &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: startedAt, UpdateEndedAt: &startedAt, TargetSchedulingConfigurationHash: "recorded"},
			wantInitialized: true, wantCurrent: "legacy", wantApplied: "recorded", wantTarget: "recorded",
		},
		{
			name: "existing fields are not overwritten", currentHash: new("combined"), appliedHash: new("applied"),
			progress:    &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: startedAt, TargetSchedulingConfigurationHash: "target"},
			wantCurrent: "combined", wantApplied: "applied", wantTarget: "target",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithPodCliqueSetGenerationHash(tc.currentHash).
				WithAppliedSchedulingConfigurationHash(tc.appliedHash).
				WithUpdateProgress(tc.progress.DeepCopy()).Build()

			assert.Equal(t, tc.wantInitialized, initializePCSGenerationBaseline(pcs, "desired", "combined"))
			assert.Equal(t, new(tc.wantCurrent), pcs.Status.CurrentGenerationHash)
			assert.Equal(t, new(tc.wantApplied), pcs.Status.AppliedSchedulingConfigurationHash)
			if tc.progress == nil {
				assert.Nil(t, pcs.Status.UpdateProgress)
			} else {
				wantProgress := tc.progress.DeepCopy()
				wantProgress.TargetSchedulingConfigurationHash = tc.wantTarget
				assert.Equal(t, wantProgress, pcs.Status.UpdateProgress)
			}
			assert.False(t, initializePCSGenerationBaseline(pcs, "new-desired", "new-combined"), "baseline initialization is idempotent")
		})
	}
}

func TestPreparePCSGenerationChange(t *testing.T) {
	t.Parallel()
	startedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	reconcileTime := metav1.NewTime(time.Unix(200, 0).UTC())
	tests := []struct {
		name         string
		configure    func(*grovecorev1beta1.PodCliqueSet)
		wantUpdate   bool
		wantRecreate bool
		wantEnded    bool
	}{
		{name: "idle legacy identity remains unchanged"},
		{name: "count-only edits preserve legacy identity", configure: func(pcs *grovecorev1beta1.PodCliqueSet) { pcs.Spec.Replicas = 4 }},
		{name: "matching combined identity is retained", configure: func(pcs *grovecorev1beta1.PodCliqueSet) {
			_, _, combinedHash := computePCSHashes(pcs)
			pcs.Status.CurrentGenerationHash = new(combinedHash)
		}},
		{name: "active legacy update keeps selections and history", configure: func(pcs *grovecorev1beta1.PodCliqueSet) {
			pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
				TargetSchedulingConfigurationHash: *pcs.Status.AppliedSchedulingConfigurationHash,
				UpdateStartedAt:                   startedAt,
				CurrentlyUpdating:                 []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, UpdateStartedAt: startedAt, InFlightEpochs: []string{"epoch"}, Message: new("waiting")}},
			}
		}},
		{name: "completed legacy update is not converted later", configure: func(pcs *grovecorev1beta1.PodCliqueSet) {
			pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
				TargetSchedulingConfigurationHash: *pcs.Status.AppliedSchedulingConfigurationHash,
				UpdateStartedAt:                   startedAt, UpdateEndedAt: &startedAt,
			}
		}},
		{name: "OnDelete recreation compares its active target", configure: func(pcs *grovecorev1beta1.PodCliqueSet) {
			pcs.Spec.UpdateStrategy.Type = grovecorev1beta1.OnDeleteStrategy
			pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true, TargetSchedulingConfigurationHash: *pcs.Status.AppliedSchedulingConfigurationHash,
				UpdateStartedAt: startedAt,
			}
			pcs.Status.AppliedSchedulingConfigurationHash = new("previous-scheduling")
		}},
		{name: "workload edit establishes a combined target", wantUpdate: true, configure: func(pcs *grovecorev1beta1.PodCliqueSet) {
			pcs.Spec.Template.Cliques[0].Annotations = map[string]string{"revision": "new"}
		}},
		{name: "scheduling edit starts recreation", wantUpdate: true, wantRecreate: true, configure: func(pcs *grovecorev1beta1.PodCliqueSet) {
			pcs.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(1))
		}},
		{name: "OnDelete workload edit is not orchestrated", wantUpdate: true, wantEnded: true, configure: func(pcs *grovecorev1beta1.PodCliqueSet) {
			pcs.Spec.UpdateStrategy.Type = grovecorev1beta1.OnDeleteStrategy
			pcs.Spec.Template.Cliques[0].Annotations = map[string]string{"revision": "new"}
		}},
		{name: "OnDelete scheduling edit is orchestrated", wantUpdate: true, wantRecreate: true, configure: func(pcs *grovecorev1beta1.PodCliqueSet) {
			pcs.Spec.UpdateStrategy.Type = grovecorev1beta1.OnDeleteStrategy
			pcs.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(1))
		}},
		{name: "combined workload and scheduling edit starts recreation", wantUpdate: true, wantRecreate: true, configure: func(pcs *grovecorev1beta1.PodCliqueSet) {
			pcs.Spec.Template.Cliques[0].Annotations = map[string]string{"revision": "new"}
			pcs.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(1))
		}},
		{name: "recreation remains required on scheduling rollback", wantUpdate: true, wantRecreate: true, configure: func(pcs *grovecorev1beta1.PodCliqueSet) {
			pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true, TargetSchedulingConfigurationHash: "superseded-scheduling", UpdateStartedAt: startedAt,
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := generationTestPCS()
			legacyHash, schedulingHash, _ := computePCSHashes(pcs)
			pcs.Status.CurrentGenerationHash = new(legacyHash)
			pcs.Status.AppliedSchedulingConfigurationHash = new(schedulingHash)
			pcs.Status.UpdatedReplicas = 2
			if tc.configure != nil {
				tc.configure(pcs)
			}
			before := pcs.DeepCopy()
			r := &_resource{}

			baselineInitialized, err := r.preparePCSGenerationChange(t.Context(), pcs, reconcileTime)

			require.NoError(t, err)
			assert.False(t, baselineInitialized, "a genuine update must not be mistaken for baseline initialization")
			if !tc.wantUpdate {
				assert.Equal(t, before, pcs)
				return
			}
			_, targetSchedulingHash, combinedHash := computePCSHashes(pcs)
			assert.Equal(t, new(combinedHash), pcs.Status.CurrentGenerationHash)
			assert.Zero(t, pcs.Status.UpdatedReplicas)
			assert.Equal(t, before.Status.AppliedSchedulingConfigurationHash, pcs.Status.AppliedSchedulingConfigurationHash)
			require.NotNil(t, pcs.Status.UpdateProgress)
			assert.Equal(t, before.Status.CurrentGenerationHash, pcs.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash)
			assert.Equal(t, targetSchedulingHash, pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash)
			assert.Equal(t, tc.wantRecreate, pcs.Status.UpdateProgress.RequiresReplicaRecreation)
			assert.Equal(t, reconcileTime, pcs.Status.UpdateProgress.UpdateStartedAt)
			assert.Equal(t, tc.wantEnded, pcs.Status.UpdateProgress.UpdateEndedAt != nil)
			after := pcs.DeepCopy()
			initializedAgain, err := r.preparePCSGenerationChange(t.Context(), pcs, metav1.NewTime(reconcileTime.Add(time.Minute)))
			require.NoError(t, err)
			assert.False(t, initializedAgain)
			assert.Equal(t, after, pcs, "the same configuration must not reset progress")
		})
	}
}

func TestBaselineDoesNotSwallowNewWorkloadEdit(t *testing.T) {
	t.Parallel()
	pcs := generationTestPCS()
	legacyHash, _, _ := computePCSHashes(pcs)
	pcs.Status.CurrentGenerationHash = new(legacyHash)
	pcs.Spec.Template.Cliques[0].Annotations = map[string]string{"revision": "new"}
	r := &_resource{}
	reconcileTime := metav1.NewTime(time.Unix(200, 0).UTC())

	initialized, err := r.preparePCSGenerationChange(t.Context(), pcs, reconcileTime)
	require.NoError(t, err)
	require.True(t, initialized)
	assert.Equal(t, new(legacyHash), pcs.Status.CurrentGenerationHash)
	assert.Nil(t, pcs.Status.UpdateProgress)

	initialized, err = r.preparePCSGenerationChange(t.Context(), pcs, reconcileTime)
	require.NoError(t, err)
	assert.False(t, initialized)
	require.NotNil(t, pcs.Status.UpdateProgress)
	assert.Equal(t, new(legacyHash), pcs.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash)
	assert.NotEqual(t, new(legacyHash), pcs.Status.CurrentGenerationHash)
}

func TestPreparePCSGenerationChangeLeavesStatusUnchangedOnScopeReadFailure(t *testing.T) {
	t.Parallel()
	pcs := generationTestPCS()
	_, schedulingHash, generationHash := computePCSHashes(pcs)
	pcs.Status.CurrentGenerationHash = new(generationHash)
	pcs.Status.AppliedSchedulingConfigurationHash = new(schedulingHash)
	pcs.Spec.UpdateStrategy.Type = grovecorev1beta1.CoherentStrategy
	pcs.Spec.Template.Cliques[0].Annotations = map[string]string{"revision": "new"}
	readError := apierrors.NewInternalError(errors.New("scope read failed"))
	cl := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).
		RecordErrorForObjectsMatchingLabels(testutils.ClientMethodList, client.ObjectKey{Namespace: pcs.Namespace},
			grovecorev1beta1.SchemeGroupVersion.WithKind("PodCliqueList"), apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name), readError).Build()
	r := &_resource{client: cl}
	before := pcs.DeepCopy()

	initialized, err := r.preparePCSGenerationChange(t.Context(), pcs, metav1.Now())

	var groveError *groveerr.GroveError
	require.ErrorAs(t, err, &groveError)
	assert.Equal(t, errCodeComputeCoherentUpdateScope, groveError.Code)
	assert.ErrorContains(t, err, "scope read failed")
	assert.False(t, initialized)
	assert.Equal(t, before, pcs, "scope must be calculated before preparing the new identity or update progress")
	stored := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), stored))
	assert.Equal(t, before.Status, stored.Status)
}

func generationTestPCS() *grovecorev1beta1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithReplicas(2).WithTerminationDelay(time.Minute).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.RollingRecreateStrategy}).Build()
}
