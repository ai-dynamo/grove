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
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	controllerrevision "github.com/ai-dynamo/grove/operator/internal/utils/controllerrevision"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReconcileRevisionBootstrapNewPodCliqueSet(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient}

	result := r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	assert.True(t, result.NeedsRequeue())
	updated := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), updated))
	require.NotEmpty(t, updated.Status.CurrentRevision)
	assert.Equal(t, updated.Status.CurrentRevision, updated.Status.UpdateRevision)
	assert.Nil(t, updated.Status.CurrentGenerationHash)

	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, fakeClient.List(t.Context(), revisions, client.InNamespace(pcs.Namespace)))
	require.Len(t, revisions.Items, 1)
	revision := &revisions.Items[0]
	assert.Equal(t, updated.Status.CurrentRevision, revision.Name)
	assert.Equal(t, int64(1), revision.Revision)
	assert.Equal(t, apicommon.LabelManagedByValue, revision.Labels[apicommon.LabelManagedByKey])
	assert.Equal(t, pcs.Name, revision.Labels[apicommon.LabelPartOfKey])
	assert.NotEmpty(t, revision.Labels[apicommon.LabelControllerRevisionDataHash])
	assert.True(t, metav1.IsControlledBy(revision, pcs))

	template, err := controllerrevision.DecodeTemplate(revision.Data.Raw)
	require.NoError(t, err)
	assert.Equal(t, pcs.Spec.Template, *template)
}

func TestEnsureCurrentControllerRevisionPreservesCurrentRevisionOnUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient}

	result := r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	stable := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), stable))
	currentRevision := stable.Status.CurrentRevision

	stable.Spec.Template.PriorityClassName = "high-priority"
	require.NoError(t, fakeClient.Update(t.Context(), stable))
	result = r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), stable)
	require.False(t, result.HasErrors())

	updated := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), updated))
	assert.Equal(t, currentRevision, updated.Status.CurrentRevision)
	assert.NotEqual(t, currentRevision, updated.Status.UpdateRevision)
}

func TestReconcileRevisionBootstrapPreservesLegacyGenerationHash(t *testing.T) {
	legacyHash := "legacy-hash"
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		WithPodCliqueSetGenerationHash(&legacyHash).
		Build()
	pcs.Generation = 7
	pcs.Status.ObservedGeneration = new(int64(7))
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient}

	result := r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	assert.True(t, result.NeedsRequeue())
	updated := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), updated))
	require.NotNil(t, updated.Status.CurrentGenerationHash)
	assert.Equal(t, legacyHash, *updated.Status.CurrentGenerationHash)
	assert.Equal(t, updated.Status.CurrentRevision, updated.Status.UpdateRevision)
}

func TestEnsureCurrentControllerRevisionIgnoresLegacyRolloutState(t *testing.T) {
	legacyHash := "legacy-hash"
	tests := []struct {
		name   string
		mutate func(*grovecorev1alpha1.PodCliqueSet)
	}{
		{
			name: "generation not observed",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Status.ObservedGeneration = new(int64(3))
			},
		},
		{
			name: "update in progress",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Status.ObservedGeneration = new(int64(4))
				pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
				WithPodCliqueParameters("worker", 1, nil).
				WithPodCliqueSetGenerationHash(&legacyHash).
				Build()
			pcs.Generation = 4
			tt.mutate(pcs)
			fakeClient := testutils.SetupFakeClient(pcs)
			r := &Reconciler{client: fakeClient}

			result := r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs)

			require.False(t, result.HasErrors())
			assert.True(t, result.NeedsRequeue())
			revisions := &appsv1.ControllerRevisionList{}
			require.NoError(t, fakeClient.List(t.Context(), revisions))
			require.Len(t, revisions.Items, 1)
			updated := &grovecorev1alpha1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), updated))
			assert.Equal(t, revisions.Items[0].Name, updated.Status.CurrentRevision)
			assert.Equal(t, updated.Status.CurrentRevision, updated.Status.UpdateRevision)
		})
	}
}

func TestReconcileRevisionBootstrapMigratesCompletedLegacyUpdate(t *testing.T) {
	legacyHash := "legacy-hash"
	endedAt := metav1.Now()
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		WithPodCliqueSetGenerationHash(&legacyHash).
		WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
			UpdateStartedAt: metav1.Now(),
			UpdateEndedAt:   &endedAt,
		}).
		Build()
	pcs.Generation = 4
	pcs.Status.ObservedGeneration = new(int64(4))
	pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "worker", pcs.Namespace, 0).Build()
	pclq.Status.CurrentPodCliqueSetGenerationHash = &legacyHash
	fakeClient := testutils.SetupFakeClient(pcs, pclq)
	r := &Reconciler{client: fakeClient}

	result := r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	assert.True(t, result.NeedsRequeue())
	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, fakeClient.List(t.Context(), revisions))
	assert.Len(t, revisions.Items, 1)
}

func TestReconcileRevisionBootstrapReusesUnreferencedRevision(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	data, err := controllerrevision.EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)
	existing := buildControllerRevision(pcs, data, 1, 0)
	fakeClient := testutils.SetupFakeClient(pcs, existing)
	r := &Reconciler{client: fakeClient}

	result := r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	updated := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), updated))
	assert.Equal(t, existing.Name, updated.Status.CurrentRevision)
	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, fakeClient.List(t.Context(), revisions))
	assert.Len(t, revisions.Items, 1)
}

func TestDecodeRevisionTemplateRejectsUnknownFieldsAndVersions(t *testing.T) {
	_, err := controllerrevision.DecodeTemplate([]byte(`{"apiVersion":"grove.io/v1alpha1","podCliqueSetTemplateSpec":{},"unknown":true}`))
	require.ErrorContains(t, err, "unknown field")

	_, err = controllerrevision.DecodeTemplate([]byte(`{"apiVersion":"grove.io/v2","podCliqueSetTemplateSpec":{}}`))
	require.ErrorContains(t, err, "unsupported PodCliqueSet revision API version")
}

func TestRevisionDataMatchesTypedTemplateIgnoringJSONRepresentation(t *testing.T) {
	formatted := []byte("{\n  \"podCliqueSetTemplateSpec\": {},\n  \"apiVersion\": \"grove.io/v1alpha1\"\n}")
	desiredTemplate := controllerrevision.NormalizeTemplate(&grovecorev1alpha1.PodCliqueSetTemplateSpec{})

	equal, err := controllerrevision.DataMatchesTemplate(formatted, desiredTemplate)

	require.NoError(t, err)
	assert.True(t, equal)
}

func TestReconcileRevisionBootstrapHandlesNameCollision(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
	data, err := controllerrevision.EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)
	colliding := buildControllerRevision(pcs, data, 1, 0)
	colliding.OwnerReferences[0].UID = uuid.NewUUID()
	fakeClient := testutils.SetupFakeClient(pcs, colliding)
	r := &Reconciler{client: fakeClient}

	firstResult := r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs)

	require.True(t, firstResult.HasErrors())
	assert.True(t, firstResult.NeedsRequeue())
	updated := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), updated))
	require.NotNil(t, updated.Status.CollisionCount)
	assert.Equal(t, int32(1), *updated.Status.CollisionCount)
	assert.Empty(t, updated.Status.CurrentRevision)
	assert.Empty(t, updated.Status.UpdateRevision)

	secondResult := r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), updated)

	require.False(t, secondResult.HasErrors())
	assert.True(t, secondResult.NeedsRequeue())
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), updated))
	assert.NotEqual(t, colliding.Name, updated.Status.CurrentRevision)
	assert.Equal(t, updated.Status.CurrentRevision, updated.Status.UpdateRevision)
	assert.Equal(t, int32(1), *updated.Status.CollisionCount)
}

func TestControllerRevisionNameIsLabelSafe(t *testing.T) {
	name := controllerRevisionName(strings.Repeat("a", 63), strings.Repeat("b", 20))
	assert.LessOrEqual(t, len(name), maxControllerRevisionNameLength)
	assert.True(t, strings.HasSuffix(name, "-"+strings.Repeat("b", 20)))
}
