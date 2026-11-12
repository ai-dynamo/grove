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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestEnsureUpdateRevisionDefersCleanupUntilUpdateEnds(t *testing.T) {
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

			require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(old), &appsv1.ControllerRevision{}))
			owned, err := r.listOwnedRevisions(t.Context(), pcs)
			require.NoError(t, err)
			require.Len(t, owned, 3, "current snapshot must still be created during an update")

			endedAt := metav1.Now()
			pcs.Status.UpdateProgress.UpdateEndedAt = &endedAt
			require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())
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
	previousHash := computeGenerationHash(&pcs.Spec.Template)
	pcs.Status.CurrentGenerationHash = &previousHash
	previous := newTestRevision(t, pcs, &pcs.Spec.Template, 14)
	pcs.Status.UpdateRevision = previous.Name
	pcs.Spec.Template.PriorityClassName = "new-priority"
	old := buildControllerRevision(pcs, []byte(`{}`), 1, 0)
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

func TestEnsureUpdateRevisionPrunesHistory(t *testing.T) {
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

			require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())
			// Cleanup-triggered reconciles must leave retained history unchanged.
			require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())
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

func TestEnsureUpdateRevisionPrunesAfterReuse(t *testing.T) {
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
			if _, ok := list.(*appsv1.ControllerRevisionList); ok {
				listCalls++
			}
			return cl.List(ctx, list, opts...)
		},
	})}
	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())
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

// TestEnsureUpdateRevisionCreatesMissingRevision verifies that a PCS with no owned
// ControllerRevision gets exactly one created, labelled and owned, carrying its current template.
func TestEnsureUpdateRevisionCreatesMissingRevision(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient}

	result := r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs)
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

// TestEnsureUpdateRevisionIsIdempotent verifies that reconciling twice with an unchanged
// template does not create a second ControllerRevision.
func TestEnsureUpdateRevisionIsIdempotent(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())
	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, fakeClient.List(t.Context(), revisions, client.InNamespace(pcs.Namespace)))
	assert.Len(t, revisions.Items, 1)
}

// TestEnsureUpdateRevisionCreatesRevisionPerTemplate verifies that a template change
// results in a second ControllerRevision at the next revision number.
func TestEnsureUpdateRevisionCreatesRevisionPerTemplate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	pcs.Spec.Template.PriorityClassName = "high-priority"
	require.NoError(t, fakeClient.Update(t.Context(), pcs))
	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, fakeClient.List(t.Context(), revisions, client.InNamespace(pcs.Namespace)))
	require.Len(t, revisions.Items, 2)
}

// TestEnsureUpdateRevisionReusesUnreferencedRevision verifies that an already-present
// equivalent revision is reused rather than duplicated.
func TestEnsureUpdateRevisionReusesUnreferencedRevision(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	data, err := controllerrevision.EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)
	existing := buildControllerRevision(pcs, data, 1, 0)
	fakeClient := testutils.SetupFakeClient(pcs, existing)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, fakeClient.List(t.Context(), revisions, client.InNamespace(pcs.Namespace)))
	require.Len(t, revisions.Items, 1)
	assert.Equal(t, existing.Name, revisions.Items[0].Name)
}

// TestEnsureUpdateRevisionRecoversFromNameCollision verifies that when a foreign
// ControllerRevision already holds the hashed name, the collision count is recomputed locally so a
// distinct name is chosen for the owned revision.
func TestEnsureUpdateRevisionRecoversFromNameCollision(t *testing.T) {
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

	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

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

// newTestRevision returns the ControllerRevision of the PodCliqueSet that stores template.
func newTestRevision(t *testing.T, pcs *grovecorev1beta1.PodCliqueSet, template *grovecorev1beta1.PodCliqueSetTemplateSpec, number int64) *appsv1.ControllerRevision {
	t.Helper()
	data, err := controllerrevision.EncodeTemplate(template)
	require.NoError(t, err)
	return buildControllerRevision(pcs, data, number, 0)
}

// newStandalonePCLQ returns a standalone PodClique of PodCliqueSet replica 0 with the given labels.
func newStandalonePCLQ(pcs *grovecorev1beta1.PodCliqueSet, cliqueName string, labels map[string]string) *grovecorev1beta1.PodClique {
	return testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, cliqueName, pcs.Namespace, 0).WithLabels(labels).Build()
}

func getPCS(t *testing.T, cl client.Client, pcs *grovecorev1beta1.PodCliqueSet) *grovecorev1beta1.PodCliqueSet {
	t.Helper()
	stored := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), stored))
	return stored
}

// TestEnsureUpdateRevisionInitializesNewPodCliqueSet verifies that the first reconcile records the revision and the
// generation hash without starting an update.
func TestEnsureUpdateRevisionInitializesNewPodCliqueSet(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	stored := getPCS(t, fakeClient, pcs)
	owned, err := r.listOwnedRevisions(t.Context(), pcs)
	require.NoError(t, err)
	require.Len(t, owned, 1)
	assert.Equal(t, owned[0].Name, stored.Status.UpdateRevision)
	assert.Empty(t, stored.Status.CurrentRevision, "the current revision moves only once children reach it")
	require.NotNil(t, stored.Status.CurrentGenerationHash)
	assert.Equal(t, computeGenerationHash(&pcs.Spec.Template), *stored.Status.CurrentGenerationHash)
	assert.Nil(t, stored.Status.UpdateProgress)
}

// TestEnsureUpdateRevisionWithoutPodTemplateChange verifies that an edit outside the pod templates records a new
// revision but starts no update and keeps the generation hash.
func TestEnsureUpdateRevisionWithoutPodTemplateChange(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		WithPodCliqueSetGenerationHash(ptr.To("generation-1")).Build()
	previous := newTestRevision(t, pcs, &pcs.Spec.Template, 1)
	pcs.Status.UpdateRevision = previous.Name
	pcs.Spec.Template.Cliques[0].Spec.Replicas = 4
	fakeClient := testutils.SetupFakeClient(pcs, previous)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	stored := getPCS(t, fakeClient, pcs)
	assert.NotEqual(t, previous.Name, stored.Status.UpdateRevision)
	assert.Nil(t, stored.Status.UpdateProgress)
	assert.Equal(t, ptr.To("generation-1"), stored.Status.CurrentGenerationHash)
}

// TestEnsureUpdateRevisionKeepsInFlightUpdate verifies that an edit outside the pod templates during an update keeps
// the update going instead of restarting it.
func TestEnsureUpdateRevisionKeepsInFlightUpdate(t *testing.T) {
	startedAt := metav1.NewTime(metav1.Now().Add(-time.Hour)).Rfc3339Copy()
	progress := &grovecorev1beta1.PodCliqueSetUpdateProgress{
		UpdateStartedAt:   startedAt,
		CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1, UpdateStartedAt: startedAt}},
	}
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithReplicas(2).
		WithPodCliqueParameters("worker", 1, nil).
		WithPodCliqueSetGenerationHash(ptr.To("generation-2")).
		WithUpdateProgress(progress).Build()
	previous := newTestRevision(t, pcs, &pcs.Spec.Template, 2)
	pcs.Status.UpdateRevision = previous.Name
	pcs.Spec.Template.Cliques[0].Spec.Replicas = 4
	// The replica not yet updated is still on the template before the in-flight update.
	outdated := newStandalonePCLQ(pcs, "worker", map[string]string{
		apicommon.LabelPodTemplateHash: "old-hash", apicommon.LabelPodCliqueSetRevision: "revision-before-update",
	})
	fakeClient := testutils.SetupFakeClient(pcs, previous, outdated)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	stored := getPCS(t, fakeClient, pcs)
	assert.NotEqual(t, previous.Name, stored.Status.UpdateRevision)
	require.NotNil(t, stored.Status.UpdateProgress)
	assert.True(t, startedAt.Equal(&stored.Status.UpdateProgress.UpdateStartedAt), "the in-flight update must not restart")
	assert.Equal(t, progress.CurrentlyUpdating, stored.Status.UpdateProgress.CurrentlyUpdating)
	assert.Equal(t, ptr.To("generation-2"), stored.Status.CurrentGenerationHash)
}

// TestEnsureUpdateRevisionIgnoresHashDrift reproduces https://github.com/ai-dynamo/grove/issues/709. After an operator
// upgrade the same template hashes differently, but the template still matches the recorded revision, so nothing
// changes.
func TestEnsureUpdateRevisionIgnoresHashDrift(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		WithPodCliqueSetGenerationHash(ptr.To("generation-from-older-build")).Build()
	revision := newTestRevision(t, pcs, &pcs.Spec.Template, 1)
	pcs.Status.UpdateRevision = revision.Name
	pclq := newStandalonePCLQ(pcs, "worker", map[string]string{
		apicommon.LabelPodTemplateHash: "hash-from-older-build", apicommon.LabelPodCliqueSetRevision: revision.Name,
	})
	fakeClient := testutils.SetupFakeClient(pcs, revision, pclq)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	stored := getPCS(t, fakeClient, pcs)
	assert.Equal(t, revision.Name, stored.Status.UpdateRevision)
	assert.Nil(t, stored.Status.UpdateProgress)
	assert.Equal(t, ptr.To("generation-from-older-build"), stored.Status.CurrentGenerationHash)
}

// TestEnsureUpdateRevisionAdoptsLegacyPodCliqueSet covers the first reconcile after an upgrade from an operator
// without revisions. The PodCliques carry no revision label, so their hashes decide.
func TestEnsureUpdateRevisionAdoptsLegacyPodCliqueSet(t *testing.T) {
	newPCS := func() *grovecorev1beta1.PodCliqueSet {
		return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "pcs-uid").
			WithReplicas(1).
			WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.CoherentStrategy}).
			WithPodCliqueParameters("worker", 1, nil).
			WithPodCliqueSetGenerationHash(ptr.To("legacy-generation")).Build()
	}

	t.Run("converged children start no update", func(t *testing.T) {
		pcs := newPCS()
		hash := componentutils.ComputePCLQPodTemplateHash(pcs.Spec.Template.Cliques[0], pcs.Spec.Template.PriorityClassName)
		fakeClient := testutils.SetupFakeClient(pcs, newStandalonePCLQ(pcs, "worker", map[string]string{apicommon.LabelPodTemplateHash: hash}))
		r := &Reconciler{client: fakeClient}

		require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

		stored := getPCS(t, fakeClient, pcs)
		assert.NotEmpty(t, stored.Status.UpdateRevision)
		assert.Nil(t, stored.Status.UpdateProgress)
		assert.Equal(t, ptr.To("legacy-generation"), stored.Status.CurrentGenerationHash)
	})

	t.Run("a child on an older template starts an update", func(t *testing.T) {
		pcs := newPCS()
		fakeClient := testutils.SetupFakeClient(pcs, newStandalonePCLQ(pcs, "worker", map[string]string{apicommon.LabelPodTemplateHash: "older-template"}))
		r := &Reconciler{client: fakeClient}

		require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

		stored := getPCS(t, fakeClient, pcs)
		require.NotNil(t, stored.Status.UpdateProgress)
		assert.Equal(t, []string{"worker"}, stored.Status.UpdateProgress.InScopeStandalonePodCliques)
		assert.Equal(t, ptr.To(computeGenerationHash(&pcs.Spec.Template)), stored.Status.CurrentGenerationHash)
	})
}

// TestEnsureUpdateRevisionRevert verifies that reverting a template brings back the earlier revision and starts an
// update that moves the children back.
func TestEnsureUpdateRevisionRevert(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).Build()
	original := newTestRevision(t, pcs, &pcs.Spec.Template, 1)
	edited := pcs.Spec.Template.DeepCopy()
	edited.Cliques[0].Spec.PodSpec.Containers[0].Image = "edited"
	editedRevision := newTestRevision(t, pcs, edited, 2)
	pcs.Status.UpdateRevision = editedRevision.Name
	pcs.Status.CurrentGenerationHash = ptr.To(computeGenerationHash(edited))
	pclq := newStandalonePCLQ(pcs, "worker", map[string]string{
		apicommon.LabelPodTemplateHash: "edited-hash", apicommon.LabelPodCliqueSetRevision: editedRevision.Name,
	})
	fakeClient := testutils.SetupFakeClient(pcs, original, editedRevision, pclq)
	r := &Reconciler{client: fakeClient}

	require.False(t, r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs).HasErrors())

	stored := getPCS(t, fakeClient, pcs)
	assert.Equal(t, original.Name, stored.Status.UpdateRevision, "the earlier revision is reused")
	require.NotNil(t, stored.Status.UpdateProgress)
	assert.Equal(t, ptr.To(computeGenerationHash(&pcs.Spec.Template)), stored.Status.CurrentGenerationHash)
	reused := &appsv1.ControllerRevision{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(original), reused))
	assert.Equal(t, int64(3), reused.Revision, "the reused revision is fast-forwarded")
}

// TestEnsureUpdateRevisionRequeuesWhileCacheIsStale verifies that no decision is taken on a status older than the
// last one written.
func TestEnsureUpdateRevisionRequeuesWhileCacheIsStale(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		WithUpdateRevision("revision-1").Build()
	r := &Reconciler{client: testutils.SetupFakeClient(pcs)}
	r.updateRevisionExpectations.Store(updateRevisionExpectationKey(pcs), "revision-2")

	result := r.ensureUpdateRevision(t.Context(), logr.Discard(), pcs)

	assert.False(t, result.HasErrors())
	assert.True(t, result.NeedsRequeue())
	owned, err := r.listOwnedRevisions(t.Context(), pcs)
	require.NoError(t, err)
	assert.Empty(t, owned)
}

// TestCleanupKeepsReferencedRevisions verifies that history cleanup never deletes the current revision or a revision
// a PodClique or PodCliqueScalingGroup references.
func TestCleanupKeepsReferencedRevisions(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).Build()
	revisions := make([]*appsv1.ControllerRevision, 0, 16)
	objects := []client.Object{pcs}
	for number := int64(1); number <= 15; number++ {
		revision := buildControllerRevision(pcs, []byte(fmt.Sprintf(`{"n":%d}`, number)), number, 0)
		revisions = append(revisions, revision)
		objects = append(objects, revision)
	}
	pcs.Status.CurrentRevision = revisions[0].Name
	pclq := newStandalonePCLQ(pcs, "worker", map[string]string{apicommon.LabelPodCliqueSetRevision: revisions[1].Name})
	pclq.Status.CurrentPodCliqueSetRevision = revisions[2].Name
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-sg", testNamespace, testPCSName, 0).
		WithOwnerReference(apiconstants.KindPodCliqueSet, testPCSName, pcs.UID).
		WithOptions(func(pcsg *grovecorev1beta1.PodCliqueScalingGroup) {
			pcsg.Status.UpdateProgress = &grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{PodCliqueSetRevision: revisions[3].Name}
		}).Build()
	objects = append(objects, pclq, pcsg)
	fakeClient := testutils.SetupFakeClient(objects...)
	r := &Reconciler{client: fakeClient}

	owned, err := r.listOwnedRevisions(t.Context(), pcs)
	require.NoError(t, err)
	require.NoError(t, r.cleanupControllerRevisions(t.Context(), pcs, owned, revisions[14]))

	exists := func(revision *appsv1.ControllerRevision) bool {
		return fakeClient.Get(t.Context(), client.ObjectKeyFromObject(revision), &appsv1.ControllerRevision{}) == nil
	}
	for i, revision := range revisions {
		switch {
		case i <= 3:
			assert.True(t, exists(revision), "referenced revision %d must be kept", revision.Revision)
		case revision.Revision < 5:
			assert.False(t, exists(revision), "unreferenced revision %d must be deleted", revision.Revision)
		default:
			assert.True(t, exists(revision), "recent revision %d must be kept", revision.Revision)
		}
	}
}
