// /*
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
// */

package podcliqueset

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestControllerRevisionInitializesNewPodCliqueSet(t *testing.T) {
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("new", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}
	expectedGenerationHash := computeGenerationHash(pcs)
	expectedCliqueHashes := candidateCliqueHashes(pcs)

	result := r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.True(t, reconcileRequeues(result))
	require.NotNil(t, pcs.Status.CurrentGenerationHash)
	assert.Equal(t, expectedGenerationHash, *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, expectedCliqueHashes, selectedCliqueHashes(ctx, t, fakeClient, pcs))
	assert.Equal(t, 1, controllerRevisionCount(ctx, t, fakeClient))
	assert.Nil(t, pcs.Status.UpdateProgress)
}

func TestControllerRevisionExpectationDoesNotBlockRecreatedPodCliqueSet(t *testing.T) {
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("recreated", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}
	pcsObjectName := cache.NamespacedNameAsObjectName(client.ObjectKeyFromObject(pcs)).String()
	r.pcsRevisionExpectations.Store(pcsObjectName, revisionExpectation{
		uid:       uuid.NewUUID(),
		selection: revisionSelection{name: "deleted-revision", generationHash: "deleted-generation"},
	})

	result := r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.True(t, reconcileRequeues(result))
	require.NotNil(t, pcs.Status.CurrentRevision)
	assert.NotEqual(t, "deleted-revision", *pcs.Status.CurrentRevision)

	value, ok := r.pcsRevisionExpectations.Load(pcsObjectName)
	require.True(t, ok)
	assert.Equal(t, pcs.UID, value.(revisionExpectation).uid)
}

func TestControllerRevisionExpectationWaitsForSelectedRevisionCacheVisibility(t *testing.T) {
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("cache-lag", "default", uuid.NewUUID()).Build()
	desired, cliques, err := marshalDesiredRevision(pcs)
	require.NoError(t, err)
	generationHash := computeGenerationHash(pcs)
	raw, err := json.Marshal(commonrevision.Data{
		Version:        commonrevision.DataVersion,
		Desired:        desired,
		Cliques:        cliques,
		GenerationHash: generationHash,
		CliqueHashes:   candidateCliqueHashes(pcs),
	})
	require.NoError(t, err)
	revision := ownedControllerRevision(pcs, "cache-lag-revision", raw)
	pcs.Status.CurrentRevision = ptr.To(revision.Name)
	pcs.Status.CurrentGenerationHash = ptr.To(generationHash)

	baseClient := testutils.SetupFakeClient(pcs, revision)
	revisionVisible := false
	laggingClient := interceptor.NewClient(baseClient, interceptor.Funcs{
		Get: func(ctx context.Context, interceptedClient client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.ControllerRevision); ok && key.Name == revision.Name && !revisionVisible {
				return apierrors.NewNotFound(appsv1.Resource("controllerrevisions"), key.Name)
			}
			return interceptedClient.Get(ctx, key, obj, opts...)
		},
	})
	r := &Reconciler{client: laggingClient, pcsRevisionExpectations: sync.Map{}}
	pcsObjectName := cache.NamespacedNameAsObjectName(client.ObjectKeyFromObject(pcs)).String()
	r.pcsRevisionExpectations.Store(pcsObjectName, revisionExpectation{
		uid:       pcs.UID,
		selection: revisionSelection{name: revision.Name, generationHash: generationHash},
	})

	result := r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.True(t, reconcileRequeues(result))
	assert.Contains(t, result.GetDescription(), "is not yet visible")
	_, ok := r.pcsRevisionExpectations.Load(pcsObjectName)
	assert.True(t, ok)

	revisionVisible = true
	result = r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.False(t, reconcileRequeues(result))
	_, ok = r.pcsRevisionExpectations.Load(pcsObjectName)
	assert.False(t, ok)
}

func TestControllerRevisionExpectationClearedOnDeletion(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("deleting", "default", uuid.NewUUID()).Build()
	now := metav1.Now()
	pcs.DeletionTimestamp = &now
	pcsObjectName := cache.NamespacedNameAsObjectName(client.ObjectKeyFromObject(pcs)).String()
	r := &Reconciler{pcsRevisionExpectations: sync.Map{}}
	r.pcsRevisionExpectations.Store(pcsObjectName, revisionExpectation{
		uid:       pcs.UID,
		selection: revisionSelection{name: "selected", generationHash: "generation"},
	})

	result := r.reconcileDelete(context.Background(), logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	_, ok := r.pcsRevisionExpectations.Load(pcsObjectName)
	assert.False(t, ok)
}

func TestControllerRevisionExpectationClearedWhenPodCliqueSetIsGone(t *testing.T) {
	req := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "default", Name: "deleted"}}
	pcsObjectName := cache.NamespacedNameAsObjectName(req.NamespacedName).String()
	r := &Reconciler{client: testutils.SetupFakeClient(), pcsRevisionExpectations: sync.Map{}}
	r.pcsRevisionExpectations.Store(pcsObjectName, revisionExpectation{
		uid:       uuid.NewUUID(),
		selection: revisionSelection{name: "selected", generationHash: "generation"},
	})

	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.Requeue)
	_, ok := r.pcsRevisionExpectations.Load(pcsObjectName)
	assert.False(t, ok)
}

func TestControllerRevisionFailsClosedWhenSelectedRevisionIsMissing(t *testing.T) {
	tests := []struct {
		name                  string
		currentGenerationHash func(*grovecorev1alpha1.PodCliqueSet) string
	}{
		{
			name: "desired state is unchanged",
			currentGenerationHash: func(pcs *grovecorev1alpha1.PodCliqueSet) string {
				return computeGenerationHash(pcs)
			},
		},
		{
			name: "desired state has advanced",
			currentGenerationHash: func(*grovecorev1alpha1.PodCliqueSet) string {
				return "selected-generation"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pcs := testutils.NewPodCliqueSetBuilder("missing", "default", uuid.NewUUID()).
				WithPodCliqueParameters("worker", 1, nil).
				Build()
			pcs.Status.CurrentRevision = ptr.To("missing-revision")
			pcs.Status.CurrentGenerationHash = ptr.To(tt.currentGenerationHash(pcs))
			originalStatus := pcs.Status.DeepCopy()
			fakeClient := testutils.SetupFakeClient(pcs)
			r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}

			result := r.processRevision(ctx, logr.Discard(), pcs)
			require.True(t, result.HasErrors())
			assert.True(t, result.NeedsRequeue())
			assert.Equal(t, "error loading current ControllerRevision", result.GetDescription())
			require.Len(t, result.GetErrors(), 1)
			assert.True(t, apierrors.IsNotFound(result.GetErrors()[0]))
			assert.True(t, equality.Semantic.DeepEqual(*originalStatus, pcs.Status))
			persistedPCS := getPCS(ctx, t, fakeClient, pcs)
			assert.True(t, equality.Semantic.DeepEqual(*originalStatus, persistedPCS.Status))
			assert.Equal(t, 0, controllerRevisionCount(ctx, t, fakeClient))
		})
	}
}

func TestControllerRevisionUpgrade(t *testing.T) {
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("legacy", "default", uuid.NewUUID()).
		WithReplicas(1).
		WithPodCliqueParameters("worker", 1, nil).
		WithPodCliqueParameters("sidecar", 1, nil).
		Build()
	pcs.Generation = 7
	pcs.Status.ObservedGeneration = ptr.To(int64(7))
	pcs.Status.CurrentGenerationHash = ptr.To("alpha8-generation")
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v1"}}
	pcs.Spec.Template.Cliques[1].Spec.PodSpec.Containers = []corev1.Container{{Name: "sidecar", Image: "sidecar:v1"}}

	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: metav1.ObjectMeta{
		Name:            "legacy-0-group",
		Namespace:       pcs.Namespace,
		UID:             uuid.NewUUID(),
		Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet"))},
	}}
	worker := legacyPCLQ(pcs, "legacy-0-group-0-worker", "alpha8-worker", pcsg)
	sidecar := legacyPCLQ(pcs, "legacy-0-sidecar", "alpha8-sidecar", pcs)
	fakeClient := testutils.SetupFakeClient(pcs, pcsg, worker, sidecar)
	originalWorker := getPCLQ(ctx, t, fakeClient, worker)
	originalSidecar := getPCLQ(ctx, t, fakeClient, sidecar)
	r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}

	result := r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.True(t, reconcileRequeues(result))
	require.NotNil(t, pcs.Status.CurrentGenerationHash)
	assert.Equal(t, "alpha8-generation", *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, map[string]string{
		"sidecar": "alpha8-sidecar",
		"worker":  "alpha8-worker",
	}, selectedCliqueHashes(ctx, t, fakeClient, pcs))
	assert.Equal(t, 1, controllerRevisionCount(ctx, t, fakeClient))
	assert.Nil(t, pcs.Status.UpdateProgress)
	assert.True(t, childrenEqual(ctx, t, fakeClient, originalWorker, originalSidecar))

	pcs = getPCS(ctx, t, fakeClient, pcs)
	result = r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.False(t, reconcileRequeues(result))
	assert.Equal(t, "alpha8-generation", *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, map[string]string{
		"sidecar": "alpha8-sidecar",
		"worker":  "alpha8-worker",
	}, selectedCliqueHashes(ctx, t, fakeClient, pcs))
	assert.Equal(t, 1, controllerRevisionCount(ctx, t, fakeClient))
	assert.Nil(t, pcs.Status.UpdateProgress)

	pcs.Generation++
	pcs.Spec.Replicas = 2
	result = r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.False(t, reconcileRequeues(result))
	assert.Equal(t, "alpha8-generation", *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, map[string]string{
		"sidecar": "alpha8-sidecar",
		"worker":  "alpha8-worker",
	}, selectedCliqueHashes(ctx, t, fakeClient, pcs))
	assert.Equal(t, 1, controllerRevisionCount(ctx, t, fakeClient))
	assert.Nil(t, pcs.Status.UpdateProgress)

	pcs.Generation++
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v2"
	expectedGenerationHash := computeGenerationHash(pcs)
	expectedWorkerHash := candidateCliqueHashes(pcs)["worker"]
	result = r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.True(t, reconcileRequeues(result))
	assert.Equal(t, expectedGenerationHash, *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, map[string]string{
		"sidecar": "alpha8-sidecar",
		"worker":  expectedWorkerHash,
	}, selectedCliqueHashes(ctx, t, fakeClient, pcs))
	assert.Equal(t, 2, controllerRevisionCount(ctx, t, fakeClient))
	assert.NotNil(t, pcs.Status.UpdateProgress)
}

func TestControllerRevisionUpgradeRejectsActiveLegacyUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("legacy", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	pcs.Generation = 3
	pcs.Status.ObservedGeneration = ptr.To(int64(3))
	pcs.Status.CurrentGenerationHash = ptr.To("alpha8-generation")
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}

	result := r.processRevision(context.Background(), logr.Discard(), pcs)
	require.True(t, result.HasErrors())
	assert.False(t, reconcileRequeues(result))
	assert.Equal(t, "alpha8-generation", *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, 0, controllerRevisionCount(context.Background(), t, fakeClient))
	assert.NotNil(t, pcs.Status.UpdateProgress)
}

func TestSemanticallyEqualJSON(t *testing.T) {
	tests := []struct {
		name      string
		left      json.RawMessage
		right     json.RawMessage
		wantEqual bool
		wantError bool
	}{
		{
			name:      "normalizes representation differences",
			left:      json.RawMessage(`[{"metadata":{"labels":{"app":"worker"},"creationTimestamp":null},"spec":{"containers":[{"name":"worker","image":"v1","resources":{}}]}}]`),
			right:     json.RawMessage(`[{"spec":{"containers":[{"resources":{},"image":"v1","name":"worker"}]},"metadata":{"labels":{"app":"worker"}}}]`),
			wantEqual: true,
		},
		{
			name:      "detects semantic changes",
			left:      json.RawMessage(`[{"spec":{"containers":[{"name":"worker","image":"v1"}]}}]`),
			right:     json.RawMessage(`[{"spec":{"containers":[{"name":"worker","image":"v2"}]}}]`),
			wantEqual: false,
		},
		{
			name:      "fails closed for malformed stored content",
			left:      json.RawMessage(`[{`),
			right:     json.RawMessage(`[]`),
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			equal, err := semanticallyEqualJSON[[]corev1.PodTemplateSpec](tt.left, tt.right)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEqual, equal)
		})
	}
}

func TestControllerRevisionPreservesSemanticallyEqualCliqueIdentity(t *testing.T) {
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("semantic", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		WithPodCliqueParameters("sidecar", 1, nil).
		Build()
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v1"}}
	pcs.Spec.Template.Cliques[1].Spec.PodSpec.Containers = []corev1.Container{{Name: "sidecar", Image: "sidecar:v1"}}
	desired, cliques, err := marshalDesiredRevision(pcs)
	require.NoError(t, err)
	oldHashes := map[string]string{"worker": "worker-old", "sidecar": "sidecar-old"}
	data := commonrevision.Data{
		Version:        commonrevision.DataVersion,
		Desired:        desired,
		Cliques:        cliques,
		GenerationHash: "generation-old",
		CliqueHashes:   oldHashes,
	}
	var indentedSidecar json.RawMessage
	indentedSidecar, err = json.MarshalIndent(data.Cliques["sidecar"], "", "  ")
	require.NoError(t, err)
	data.Cliques["sidecar"] = indentedSidecar
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	revision := ownedControllerRevision(pcs, "semantic-old", raw)
	pcs.Status.CurrentRevision = ptr.To(revision.Name)
	pcs.Status.CurrentGenerationHash = ptr.To(data.GenerationHash)

	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v2"
	fakeClient := testutils.SetupFakeClient(pcs, revision)
	r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}
	result := r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())

	selected := selectedCliqueHashes(ctx, t, fakeClient, pcs)
	assert.Equal(t, "sidecar-old", selected["sidecar"])
	assert.NotEqual(t, "worker-old", selected["worker"])
}

func TestControllerRevisionIgnoresSemanticallyEqualDesiredRepresentation(t *testing.T) {
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("semantic-noop", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v1"}}
	desired, cliques, err := marshalDesiredRevision(pcs)
	require.NoError(t, err)
	indentedDesired, err := json.MarshalIndent(desired, "", "  ")
	require.NoError(t, err)
	require.NotEqual(t, string(desired), string(indentedDesired))
	data := commonrevision.Data{
		Version:        commonrevision.DataVersion,
		Desired:        indentedDesired,
		Cliques:        cliques,
		GenerationHash: "generation-old",
		CliqueHashes:   map[string]string{"worker": "worker-old"},
	}
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	revision := ownedControllerRevision(pcs, "semantic-noop-old", raw)
	pcs.Status.CurrentRevision = ptr.To(revision.Name)
	pcs.Status.CurrentGenerationHash = ptr.To(data.GenerationHash)
	fakeClient := testutils.SetupFakeClient(pcs, revision)
	r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}

	result := r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.Equal(t, 1, controllerRevisionCount(ctx, t, fakeClient))
	assert.Nil(t, pcs.Status.UpdateProgress)
	assert.Equal(t, revision.Name, *pcs.Status.CurrentRevision)
}

func TestTruncateRevisionHistory(t *testing.T) {
	completedAt := metav1.Now()
	tests := []struct {
		name        string
		configure   func(*grovecorev1alpha1.PodCliqueSet)
		wantOld     bool
		wantForeign bool
	}{
		{
			name: "completed automatic update truncates owned history",
			configure: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateEndedAt: &completedAt}
			},
			wantOld:     false,
			wantForeign: true,
		},
		{
			name: "active automatic update retains history",
			configure: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
			},
			wantOld:     true,
			wantForeign: true,
		},
		{
			name: "on-delete update retains history",
			configure: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy}
				pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateEndedAt: &completedAt}
			},
			wantOld:     true,
			wantForeign: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pcs := testutils.NewPodCliqueSetBuilder("history", "default", uuid.NewUUID()).Build()
			pcs.Status.CurrentRevision = ptr.To("history-current")
			tt.configure(pcs)
			current := ownedControllerRevision(pcs, "history-current", json.RawMessage(`{}`))
			old := ownedControllerRevision(pcs, "history-old", json.RawMessage(`{}`))
			foreign := ownedControllerRevision(pcs, "history-foreign", json.RawMessage(`{}`))
			foreign.OwnerReferences[0].UID = uuid.NewUUID()
			fakeClient := testutils.SetupFakeClient(pcs, current, old, foreign)
			r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}

			result := r.truncateRevisionHistory(ctx, logr.Discard(), pcs)
			require.False(t, result.HasErrors())
			assertRevisionExists(ctx, t, fakeClient, current.Name, true)
			assertRevisionExists(ctx, t, fakeClient, old.Name, tt.wantOld)
			assertRevisionExists(ctx, t, fakeClient, foreign.Name, tt.wantForeign)
		})
	}
}

func TestReconcileTruncatesRevisionHistoryAfterAggregateCompletion(t *testing.T) {
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("history", "default", uuid.NewUUID()).
		WithReplicas(0).
		WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}).
		Build()
	pcs.Generation = 1
	pcs.Status.ObservedGeneration = ptr.To(pcs.Generation)
	pcs.Finalizers = []string{apicommonconstants.FinalizerPodCliqueSet}
	current := testutils.NewPodCliqueSetControllerRevision(pcs, map[string]string{})
	old := ownedControllerRevision(pcs, "history-old", json.RawMessage(`{}`))
	fakeClient := testutils.SetupFakeClient(pcs, current, old)
	r := &Reconciler{
		client:                  fakeClient,
		operatorRegistry:        component.NewOperatorRegistry[grovecorev1alpha1.PodCliqueSet](),
		pcsRevisionExpectations: sync.Map{},
	}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pcs)})
	require.NoError(t, err)
	assert.False(t, result.Requeue)

	updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(pcs), updatedPCS))
	require.NotNil(t, updatedPCS.Status.UpdateProgress)
	assert.NotNil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt)
	assertRevisionExists(ctx, t, fakeClient, current.Name, true)
	assertRevisionExists(ctx, t, fakeClient, old.Name, false)
}

func ownedControllerRevision(pcs *grovecorev1alpha1.PodCliqueSet, name string, raw json.RawMessage) *appsv1.ControllerRevision {
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       pcs.Namespace,
			Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet"))},
		},
		Data: runtime.RawExtension{Raw: raw},
	}
}

func assertRevisionExists(ctx context.Context, t *testing.T, cl client.Client, name string, want bool) {
	t.Helper()
	err := cl.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &appsv1.ControllerRevision{})
	if want {
		require.NoError(t, err)
	} else {
		require.True(t, apierrors.IsNotFound(err), "expected ControllerRevision %s to be deleted, got %v", name, err)
	}
}

func legacyPCLQ(pcs *grovecorev1alpha1.PodCliqueSet, name, hash string, owner client.Object) *grovecorev1alpha1.PodClique {
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)
	labels[apicommon.LabelPodTemplateHash] = hash
	labels[apicommon.LabelPodCliqueSetReplicaIndex] = "0"
	if pcsg, ok := owner.(*grovecorev1alpha1.PodCliqueScalingGroup); ok {
		labels[apicommon.LabelPodCliqueScalingGroup] = pcsg.Name
		labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex] = "0"
	}
	gvk := grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet")
	if _, ok := owner.(*grovecorev1alpha1.PodCliqueScalingGroup); ok {
		gvk = grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueScalingGroup")
	}
	return &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Name:            name,
		Namespace:       pcs.Namespace,
		UID:             uuid.NewUUID(),
		Labels:          labels,
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(owner, gvk)},
	}}
}

func reconcileRequeues(result ctrlcommon.ReconcileStepResult) bool {
	ctrlResult, _ := result.Result()
	return ctrlResult.RequeueAfter > 0
}

func controllerRevisionCount(ctx context.Context, t *testing.T, cl client.Client) int {
	t.Helper()
	list := &appsv1.ControllerRevisionList{}
	if err := cl.List(ctx, list); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func selectedCliqueHashes(ctx context.Context, t *testing.T, cl client.Client, pcs *grovecorev1alpha1.PodCliqueSet) map[string]string {
	t.Helper()
	selectedRevision, err := componentutils.GetSelectedPodCliqueSetRevision(ctx, cl, pcs)
	if err != nil {
		t.Fatal(err)
	}
	hashes := make(map[string]string, len(pcs.Spec.Template.Cliques))
	for _, clique := range pcs.Spec.Template.Cliques {
		hash, err := selectedRevision.CliqueHash(clique.Name)
		if err != nil {
			t.Fatal(err)
		}
		hashes[clique.Name] = hash
	}
	return hashes
}

func childrenEqual(ctx context.Context, t *testing.T, cl client.Client, expected ...*grovecorev1alpha1.PodClique) bool {
	t.Helper()
	for _, want := range expected {
		got := &grovecorev1alpha1.PodClique{}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(want), got); err != nil || !equality.Semantic.DeepEqual(want, got) {
			return false
		}
	}
	return true
}

func getPCS(ctx context.Context, t *testing.T, cl client.Client, pcs *grovecorev1alpha1.PodCliqueSet) *grovecorev1alpha1.PodCliqueSet {
	t.Helper()
	updated := &grovecorev1alpha1.PodCliqueSet{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(pcs), updated); err != nil {
		t.Fatal(err)
	}
	return updated
}

func getPCLQ(ctx context.Context, t *testing.T, cl client.Client, pclq *grovecorev1alpha1.PodClique) *grovecorev1alpha1.PodClique {
	t.Helper()
	updated := &grovecorev1alpha1.PodClique{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(pclq), updated); err != nil {
		t.Fatal(err)
	}
	return updated
}
