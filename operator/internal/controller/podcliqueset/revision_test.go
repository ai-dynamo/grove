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
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	controllerrevision "github.com/ai-dynamo/grove/operator/internal/utils/controllerrevision"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestEnsureCurrentControllerRevisionDefersCleanupUntilUpdateEnds(t *testing.T) {
	for _, strategy := range []grovecorev1beta1.UpdateStrategyType{
		grovecorev1beta1.RollingRecreateStrategy, grovecorev1beta1.CoherentStrategy, grovecorev1beta1.OnDeleteStrategy,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
				WithPodCliqueParameters("worker", 1, nil).
				WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: strategy}).Build()
			pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
			old := buildControllerRevision(pcs, []byte(`{}`), 1, 0)
			previous := buildControllerRevision(pcs, []byte(`{"previous":true}`), 14, 0)
			fakeClient := testutils.SetupFakeClient(pcs, old, previous)
			r := &Reconciler{client: fakeClient}

			require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(old), &appsv1.ControllerRevision{}))
			owned, err := r.listOwnedRevisions(t.Context(), pcs)
			require.NoError(t, err)
			require.Len(t, owned, 3, "current snapshot must still be created during an update")

			endedAt := metav1.Now()
			pcs.Status.UpdateProgress.UpdateEndedAt = &endedAt
			require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())
			assert.True(t, apierrors.IsNotFound(fakeClient.Get(t.Context(), client.ObjectKeyFromObject(old), &appsv1.ControllerRevision{})))
			owned, err = r.listOwnedRevisions(t.Context(), pcs)
			require.NoError(t, err)
			assert.Len(t, owned, 2)
		})
	}
}

func TestReconcileSpecRetainsRevisionsWhenUpdateStarts(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).Build()
	previousHash := computeGenerationHash(pcs)
	pcs.Status.CurrentGenerationHash = &previousHash
	pcs.Spec.Template.PriorityClassName = "new-priority"
	old := buildControllerRevision(pcs, []byte(`{}`), 1, 0)
	previous := buildControllerRevision(pcs, []byte(`{"previous":true}`), 14, 0)
	fakeClient := testutils.SetupFakeClient(pcs, old, previous)
	r := &Reconciler{
		client:           fakeClient,
		operatorRegistry: component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result](),
	}

	require.False(t, r.reconcileSpec(t.Context(), logr.Discard(), pcs, &pcssync.State{}).HasErrors())
	require.NotNil(t, pcs.Status.UpdateProgress)
	assert.Nil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(old), &appsv1.ControllerRevision{}))
	owned, err := r.listOwnedRevisions(t.Context(), pcs)
	require.NoError(t, err)
	assert.Len(t, owned, 3)
}

func TestEnsureCurrentControllerRevisionPrunesHistory(t *testing.T) {
	for _, currentNumber := range []int64{10, 11, 12, 15} {
		t.Run(fmt.Sprint(currentNumber), func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
				WithPodCliqueParameters("worker", 1, nil).Build()
			objects := []client.Object{pcs}
			for number := int64(0); number < currentNumber; number++ {
				template := pcs.Spec.Template.DeepCopy()
				template.PriorityClassName = fmt.Sprintf("priority-%d", number)
				data, err := controllerrevision.EncodeTemplate(template)
				require.NoError(t, err)
				objects = append(objects, buildControllerRevision(pcs, data, number, 0))
			}
			// Matching labels alone must not allow deleting another owner's revision.
			foreign := buildControllerRevision(pcs, []byte(`{}`), 1, 0)
			foreign.OwnerReferences[0].UID = uuid.NewUUID()
			objects = append(objects, foreign)
			fakeClient := testutils.SetupFakeClient(objects...)
			r := &Reconciler{client: fakeClient}

			require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())
			// Cleanup-triggered reconciles must leave retained history unchanged.
			require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())
			owned, err := r.listOwnedRevisions(t.Context(), pcs)
			require.NoError(t, err)
			var actual, expected []int64
			for _, revision := range owned {
				actual = append(actual, revision.Revision)
			}
			for number := max(int64(0), currentNumber-10); number <= currentNumber; number++ {
				expected = append(expected, number)
			}
			slices.Sort(actual)
			assert.Equal(t, expected, actual)
			assert.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(foreign), &appsv1.ControllerRevision{}))
		})
	}
}

func TestEnsureCurrentControllerRevisionPrunesAfterReuse(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).Build()
	data, err := controllerrevision.EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)
	reused := buildControllerRevision(pcs, data, 1, 0)
	objects := []client.Object{pcs, reused}
	for _, number := range []int64{2, 4, 5, 14} {
		template := pcs.Spec.Template.DeepCopy()
		template.PriorityClassName = fmt.Sprintf("priority-%d", number)
		data, err := controllerrevision.EncodeTemplate(template)
		require.NoError(t, err)
		objects = append(objects, buildControllerRevision(pcs, data, number, 0))
	}
	fakeClient := testutils.SetupFakeClient(objects...)
	listCalls := 0
	r := &Reconciler{client: interceptor.NewClient(fakeClient, interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			listCalls++
			return cl.List(ctx, list, opts...)
		},
	})}
	require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())
	assert.Equal(t, 1, listCalls, "creation and cleanup must share one revision list")
	r.client = fakeClient
	owned, err := r.listOwnedRevisions(t.Context(), pcs)
	require.NoError(t, err)
	var numbers []int64
	for _, revision := range owned {
		numbers = append(numbers, revision.Revision)
	}
	slices.Sort(numbers)
	assert.Equal(t, []int64{5, 14, 15}, numbers)
	updated := &appsv1.ControllerRevision{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(reused), updated))
	assert.Equal(t, int64(15), updated.Revision)
}

func TestCleanupControllerRevisionsDeletionErrors(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("notFound=%t", missing), func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
			current := buildControllerRevision(pcs, []byte(`{}`), 15, 0)
			old := buildControllerRevision(pcs, []byte(`{"old":true}`), 1, 0)
			old.UID = uuid.NewUUID()
			other := buildControllerRevision(pcs, []byte(`{"other":true}`), 2, 0)
			fakeClient := testutils.SetupFakeClient(pcs, current, old, other)
			deleteErr := errors.New("delete failed")
			if missing {
				deleteErr = apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "controllerrevisions"}, old.Name)
			}
			r := &Reconciler{client: interceptor.NewClient(fakeClient, interceptor.Funcs{
				Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					if obj.GetName() == old.Name {
						options := &client.DeleteOptions{}
						options.ApplyOptions(opts)
						require.NotNil(t, options.Preconditions)
						assert.Equal(t, obj.GetUID(), *options.Preconditions.UID)
						assert.Equal(t, obj.GetResourceVersion(), *options.Preconditions.ResourceVersion)
						return deleteErr
					}
					return cl.Delete(ctx, obj, opts...)
				},
			})}
			revisions, err := r.listOwnedRevisions(t.Context(), pcs)
			require.NoError(t, err)
			err = r.cleanupControllerRevisions(t.Context(), pcs, revisions, current)
			if missing {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, deleteErr)
			}
			assert.True(t, apierrors.IsNotFound(fakeClient.Get(t.Context(), client.ObjectKeyFromObject(other), &appsv1.ControllerRevision{})))
		})
	}
}

// TestEnsureCurrentControllerRevisionCreatesMissingRevision verifies that a PCS with no owned
// ControllerRevision gets exactly one created, labelled and owned, carrying its current template.
func TestEnsureCurrentControllerRevisionCreatesMissingRevision(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient}

	result := r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs)
	require.False(t, result.HasErrors())

	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, fakeClient.List(t.Context(), revisions, client.InNamespace(pcs.Namespace)))
	require.Len(t, revisions.Items, 1)
	revision := &revisions.Items[0]
	assert.Equal(t, int64(1), revision.Revision)
	assert.Equal(t, apicommon.LabelManagedByValue, revision.Labels[apicommon.LabelManagedByKey])
	assert.Equal(t, pcs.Name, revision.Labels[apicommon.LabelPartOfKey])
	assert.NotEmpty(t, revision.Labels[apicommon.LabelControllerRevisionDataHash])
	assert.True(t, metav1.IsControlledBy(revision, pcs))

	template, err := controllerrevision.DecodeTemplate(revision.Data.Raw)
	require.NoError(t, err)
	assert.Equal(t, pcs.Spec.Template, *template)
}

// TestEnsureCurrentControllerRevisionIsIdempotent verifies that reconciling twice with an unchanged
// template does not create a second ControllerRevision.
func TestEnsureCurrentControllerRevisionIsIdempotent(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())
	require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, fakeClient.List(t.Context(), revisions, client.InNamespace(pcs.Namespace)))
	assert.Len(t, revisions.Items, 1)
}

// TestEnsureCurrentControllerRevisionCreatesRevisionPerTemplate verifies that a template change
// results in a second ControllerRevision at the next revision number.
func TestEnsureCurrentControllerRevisionCreatesRevisionPerTemplate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	pcs.Spec.Template.PriorityClassName = "high-priority"
	require.NoError(t, fakeClient.Update(t.Context(), pcs))
	require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, fakeClient.List(t.Context(), revisions, client.InNamespace(pcs.Namespace)))
	require.Len(t, revisions.Items, 2)
}

// TestEnsureCurrentControllerRevisionReusesUnreferencedRevision verifies that an already-present
// equivalent revision is reused rather than duplicated.
func TestEnsureCurrentControllerRevisionReusesUnreferencedRevision(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	data, err := controllerrevision.EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)
	existing := buildControllerRevision(pcs, data, 1, 0)
	fakeClient := testutils.SetupFakeClient(pcs, existing)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, fakeClient.List(t.Context(), revisions, client.InNamespace(pcs.Namespace)))
	require.Len(t, revisions.Items, 1)
	assert.Equal(t, existing.Name, revisions.Items[0].Name)
}

// TestEnsureCurrentControllerRevisionRecoversFromNameCollision verifies that when a foreign
// ControllerRevision already holds the hashed name, the collision count is recomputed locally so a
// distinct name is chosen for the owned revision.
func TestEnsureCurrentControllerRevisionRecoversFromNameCollision(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	data, err := controllerrevision.EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)
	// A revision occupying the collisionCount=0 name the PCS would pick, but carrying different data
	// so it is treated as a genuine collision rather than a reusable equivalent.
	collidingName := controllerRevisionName(pcs.Name, hashRevisionData(data, 0))
	colliding := buildControllerRevision(pcs, []byte(`{"apiVersion":"grove.io/v1beta1","podCliqueSetTemplateSpec":{"priorityClassName":"other"}}`), 1, 0)
	colliding.Name = collidingName
	colliding.OwnerReferences[0].UID = uuid.NewUUID()
	fakeClient := testutils.SetupFakeClient(pcs, colliding)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureCurrentControllerRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	owned, err := r.listOwnedRevisions(t.Context(), pcs)
	require.NoError(t, err)
	require.Len(t, owned, 1)
	assert.NotEqual(t, colliding.Name, owned[0].Name)
	// The owned revision stores the PCS's actual template.
	template, err := controllerrevision.DecodeTemplate(owned[0].Data.Raw)
	require.NoError(t, err)
	assert.Equal(t, pcs.Spec.Template, *template)
}

func TestControllerRevisionNameIsLabelSafe(t *testing.T) {
	name := controllerRevisionName(strings.Repeat("a", 63), strings.Repeat("b", 20))
	assert.LessOrEqual(t, len(name), maxControllerRevisionNameLength)
	assert.True(t, strings.HasSuffix(name, "-"+strings.Repeat("b", 20)))
}
