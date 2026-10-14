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
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// These identities were produced by the released hash function in a separate
// module using k8s.io/api and k8s.io/apimachinery v0.34.3.
const (
	legacyFrontendHash = "b5f67c599b4bf686777"
	legacyWorkerHash   = "86855bdb95cdb7bcd4f"
	legacyPairHash     = "b778ffcd948b74876f"
	legacyPriorityHash = "8d55dffc84c84cf4997"
)

func TestProcessRevisionWithoutCurrentRevision(t *testing.T) {
	tests := []struct {
		name                string
		legacy              bool
		strategy            *grovecorev1alpha1.PodCliqueSetUpdateStrategy
		mutate              func(*grovecorev1alpha1.PodCliqueSet)
		wantLegacyData      bool
		wantUpdate          bool
		wantUpdateCompleted bool
		wantUpdatedReplicas int32
	}{
		{
			name:                "new PodCliqueSet initializes without an update",
			wantUpdatedReplicas: 0,
		},
		{
			name: "new PodCliqueSet with OnDelete initializes without an update",
			strategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{
				Type: grovecorev1alpha1.OnDeleteStrategy,
			},
			wantUpdatedReplicas: 0,
		},
		{
			name: "new PodCliqueSet with Coherent initializes without an update",
			strategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{
				Type: grovecorev1alpha1.CoherentStrategy,
			},
			wantUpdatedReplicas: 0,
		},
		{
			name:                "legacy PodCliqueSet is adopted without an update",
			legacy:              true,
			wantLegacyData:      true,
			wantUpdatedReplicas: 1,
		},
		{
			name:   "legacy PodCliqueSet with a template change before adoption starts an update",
			legacy: true,
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Generation++
				pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v2"
			},
			wantUpdate:          true,
			wantUpdatedReplicas: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-namespace", uuid.NewUUID()).
				WithReplicas(1).
				WithScalingGroup("group", []string{"worker"}).
				Build()
			pcs.Generation = 1
			pcs.Spec.UpdateStrategy = tt.strategy
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v1"}}

			initialData, err := commonrevision.PodCliqueSetData(pcs)
			require.NoError(t, err)

			objects := []client.Object{pcs}
			if tt.legacy {
				pcs.Status.ObservedGeneration = ptr.To(pcs.Generation)
				pcs.Status.CurrentGenerationHash = ptr.To(initialData.GenerationHash)
				pcs.Status.UpdatedReplicas = 1

				pcsgName := apicommon.GeneratePodCliqueScalingGroupName(
					apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0},
					"group",
				)
				pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:            pcsgName,
						Namespace:       pcs.Namespace,
						UID:             uuid.NewUUID(),
						Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
						OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet"))},
					},
				}
				pclqName := apicommon.GeneratePodCliqueName(
					apicommon.ResourceNameReplica{Name: pcsgName, Replica: 0},
					"worker",
				)
				pclq := testutils.NewPCSGPodCliqueBuilder(pclqName, pcs.Namespace, pcs.Name, pcsgName, 0, 0).
					WithLabels(map[string]string{apicommon.LabelPodTemplateHash: initialData.Cliques[0].Hash}).
					Build()
				pclq.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(pcsg, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueScalingGroup"))}
				objects = append(objects, pcsg, pclq)
			}

			if tt.mutate != nil {
				tt.mutate(pcs)
			}
			desiredData, err := commonrevision.PodCliqueSetData(pcs)
			require.NoError(t, err)

			fakeClient := testutils.SetupFakeClient(objects...)
			reconciler := &Reconciler{client: fakeClient, apiReader: fakeClient, pcsRevisionExpectations: sync.Map{}}

			result := reconciler.processRevision(ctx, logr.Discard(), pcs)
			require.False(t, result.HasErrors())
			assert.True(t, result.NeedsRequeue())

			updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(pcs), updatedPCS))
			require.NotNil(t, updatedPCS.Status.CurrentRevision)

			revisions := &appsv1.ControllerRevisionList{}
			require.NoError(t, fakeClient.List(ctx, revisions, client.InNamespace(pcs.Namespace)))
			require.Len(t, revisions.Items, 1)
			assert.Equal(t, revisions.Items[0].Name, *updatedPCS.Status.CurrentRevision)
			controller := metav1.GetControllerOf(&revisions.Items[0])
			require.NotNil(t, controller)
			assert.Equal(t, pcs.UID, controller.UID)

			storedData := commonrevision.Data{}
			require.NoError(t, json.Unmarshal(revisions.Items[0].Data.Raw, &storedData))
			assert.Equal(t, pcs.UID, storedData.UID)
			require.Len(t, storedData.Cliques, 1)
			assert.Equal(t, "worker", storedData.Cliques[0].Name)
			if tt.wantLegacyData {
				assert.Equal(t, initialData.GenerationHash, storedData.GenerationHash)
				assert.Equal(t, initialData.Cliques[0].Hash, storedData.Cliques[0].Hash)
			} else {
				assert.Equal(t, desiredData.GenerationHash, storedData.GenerationHash)
				assert.Equal(t, desiredData.Cliques[0].Hash, storedData.Cliques[0].Hash)
			}
			assert.Equal(t, storedData.GenerationHash, ptr.Deref(updatedPCS.Status.CurrentGenerationHash, ""))
			assert.Equal(t, tt.wantUpdatedReplicas, updatedPCS.Status.UpdatedReplicas)

			if !tt.wantUpdate {
				assert.Nil(t, updatedPCS.Status.UpdateProgress)
				return
			}
			require.NotNil(t, updatedPCS.Status.UpdateProgress)
			assert.False(t, updatedPCS.Status.UpdateProgress.UpdateStartedAt.IsZero())
			assert.Equal(t, tt.wantUpdateCompleted, updatedPCS.Status.UpdateProgress.UpdateEndedAt != nil)
		})
	}
}

func TestProcessRevisionWithCurrentRevision(t *testing.T) {
	tests := []struct {
		name                string
		strategy            *grovecorev1alpha1.PodCliqueSetUpdateStrategy
		mutate              func(*grovecorev1alpha1.PodCliqueSet)
		semanticRevision    bool
		missingRevision     bool
		wantNewRevision     bool
		wantUpdateCompleted bool
	}{
		{
			name: "unchanged template keeps the selected revision",
		},
		{
			name: "scale-only change keeps the selected revision",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Generation++
				pcs.Spec.Replicas = 2
			},
		},
		{
			name: "template change starts an update with a new revision",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Generation++
				pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v2"
			},
			wantNewRevision: true,
		},
		{
			name: "priority class change starts an update with a new revision",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Generation++
				pcs.Spec.Template.PriorityClassName = "high-priority"
			},
			wantNewRevision: true,
		},
		{
			name: "OnDelete template change completes the update with a new revision",
			strategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{
				Type: grovecorev1alpha1.OnDeleteStrategy,
			},
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Generation++
				pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v2"
			},
			wantNewRevision:     true,
			wantUpdateCompleted: true,
		},
		{
			name:             "semantically equal JSON keeps the selected revision",
			semanticRevision: true,
		},
		{
			name:            "missing selected revision returns an error",
			missingRevision: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-namespace", uuid.NewUUID()).
				WithReplicas(1).
				WithPodCliqueParameters("worker", 1, nil).
				Build()
			pcs.Generation = 1
			pcs.Spec.UpdateStrategy = tt.strategy
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v1"}}
			pcs.Status.UpdatedReplicas = 1

			oldRevision, err := testutils.NewPodCliqueSetControllerRevision(pcs)
			require.NoError(t, err)
			if tt.semanticRevision {
				storedData := commonrevision.Data{}
				require.NoError(t, json.Unmarshal(oldRevision.Data.Raw, &storedData))
				indentedTemplate, err := json.MarshalIndent(storedData.Cliques[0].Template, "", "  ")
				require.NoError(t, err)
				require.NotEqual(t, string(storedData.Cliques[0].Template), string(indentedTemplate))
				storedData.Cliques[0].Template = indentedTemplate
				storedData.Cliques[0].Hash = "stored-worker-hash"
				storedData.GenerationHash = "stored-generation-hash"
				oldRevision.Data.Raw, err = json.Marshal(storedData)
				require.NoError(t, err)
				pcs.Status.CurrentGenerationHash = ptr.To(storedData.GenerationHash)
			}

			if tt.missingRevision {
				pcs.Status.CurrentRevision = ptr.To("missing-revision")
				pcs.Status.CurrentGenerationHash = ptr.To("missing-generation-hash")
			}
			if tt.mutate != nil {
				tt.mutate(pcs)
			}
			desiredData, err := commonrevision.PodCliqueSetData(pcs)
			require.NoError(t, err)

			objects := []client.Object{pcs}
			if !tt.missingRevision {
				objects = append(objects, oldRevision)
			}
			fakeClient := testutils.SetupFakeClient(objects...)
			reconciler := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}

			result := reconciler.processRevision(ctx, logr.Discard(), pcs)
			if tt.missingRevision {
				require.True(t, result.HasErrors())
				assert.True(t, result.NeedsRequeue())
				_, err := result.Result()
				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err))

				updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
				require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(pcs), updatedPCS))
				assert.Equal(t, "missing-revision", ptr.Deref(updatedPCS.Status.CurrentRevision, ""))
				assert.Equal(t, "missing-generation-hash", ptr.Deref(updatedPCS.Status.CurrentGenerationHash, ""))

				revisions := &appsv1.ControllerRevisionList{}
				require.NoError(t, fakeClient.List(ctx, revisions, client.InNamespace(pcs.Namespace)))
				assert.Empty(t, revisions.Items)
				return
			}

			require.False(t, result.HasErrors())
			assert.Equal(t, tt.wantNewRevision, result.NeedsRequeue())

			updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(pcs), updatedPCS))
			require.NotNil(t, updatedPCS.Status.CurrentRevision)
			if tt.wantNewRevision {
				assert.NotEqual(t, oldRevision.Name, *updatedPCS.Status.CurrentRevision)
				assert.Equal(t, desiredData.GenerationHash, ptr.Deref(updatedPCS.Status.CurrentGenerationHash, ""))
				assert.Zero(t, updatedPCS.Status.UpdatedReplicas)
				require.NotNil(t, updatedPCS.Status.UpdateProgress)
				assert.False(t, updatedPCS.Status.UpdateProgress.UpdateStartedAt.IsZero())
				assert.Equal(t, tt.wantUpdateCompleted, updatedPCS.Status.UpdateProgress.UpdateEndedAt != nil)
			} else {
				assert.Equal(t, oldRevision.Name, *updatedPCS.Status.CurrentRevision)
				assert.Equal(t, int32(1), updatedPCS.Status.UpdatedReplicas)
				assert.Nil(t, updatedPCS.Status.UpdateProgress)
			}

			revisions := &appsv1.ControllerRevisionList{}
			require.NoError(t, fakeClient.List(ctx, revisions, client.InNamespace(pcs.Namespace)))
			if tt.wantNewRevision {
				require.Len(t, revisions.Items, 2)
			} else {
				require.Len(t, revisions.Items, 1)
			}

			selectedRevision := &appsv1.ControllerRevision{}
			require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: *updatedPCS.Status.CurrentRevision}, selectedRevision))
			controller := metav1.GetControllerOf(selectedRevision)
			require.NotNil(t, controller)
			assert.Equal(t, pcs.UID, controller.UID)

			selectedData := commonrevision.Data{}
			require.NoError(t, json.Unmarshal(selectedRevision.Data.Raw, &selectedData))
			wantData := commonrevision.Data{}
			if tt.wantNewRevision {
				wantData = desiredData
			} else {
				require.NoError(t, json.Unmarshal(oldRevision.Data.Raw, &wantData))
			}
			assert.Equal(t, wantData.UID, selectedData.UID)
			assert.Equal(t, wantData.GenerationHash, selectedData.GenerationHash)
			require.Len(t, selectedData.Cliques, len(wantData.Cliques))
			for i := range wantData.Cliques {
				assert.Equal(t, wantData.Cliques[i].Name, selectedData.Cliques[i].Name)
				assert.Equal(t, wantData.Cliques[i].Hash, selectedData.Cliques[i].Hash)
				assert.JSONEq(t, string(wantData.Cliques[i].Template), string(selectedData.Cliques[i].Template))
			}
		})
	}
}

func TestTruncateRevisionHistory(t *testing.T) {
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-namespace", uuid.NewUUID()).Build()
	current, err := testutils.NewPodCliqueSetControllerRevision(pcs)
	require.NoError(t, err)

	obsolete := current.DeepCopy()
	obsolete.Name = "obsolete-revision"

	previousOwner := current.DeepCopy()
	previousOwner.Name = "previous-owner-revision"
	previousOwner.OwnerReferences[0].UID = uuid.NewUUID()

	unowned := current.DeepCopy()
	unowned.Name = "unowned-revision"
	unowned.OwnerReferences = nil

	nonControllerOwner := current.DeepCopy()
	nonControllerOwner.Name = "non-controller-owner-revision"
	nonControllerOwner.OwnerReferences[0].Controller = ptr.To(false)

	fakeClient := testutils.SetupFakeClient(pcs, current, obsolete, previousOwner, unowned, nonControllerOwner)
	reconciler := &Reconciler{client: fakeClient}

	result := reconciler.truncateRevisionHistory(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors(), "%v", result.GetErrors())
	assert.False(t, result.NeedsRequeue())

	err = fakeClient.Get(ctx, client.ObjectKeyFromObject(obsolete), &appsv1.ControllerRevision{})
	assert.True(t, apierrors.IsNotFound(err), "owned obsolete revision should be deleted: %v", err)

	for _, revision := range []*appsv1.ControllerRevision{current, previousOwner, unowned, nonControllerOwner} {
		err := fakeClient.Get(ctx, client.ObjectKeyFromObject(revision), &appsv1.ControllerRevision{})
		assert.NoError(t, err, "revision %s should be retained", revision.Name)
	}
}

func TestReconcileRevisionStatusConflict(t *testing.T) {
	for _, tt := range []struct {
		name           string
		recordConflict bool
	}{
		{name: "error recorded"},
		{name: "error recording also conflicts", recordConflict: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			// At zero replicas, status reconciliation can be a no-op. If both the selection
			// Update and the error-recording Patch fail, only memory refers to the new revision.
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-namespace", uuid.NewUUID()).
				WithReplicas(0).
				WithPodCliqueParameters("worker", 1, nil).
				Build()
			pcs.Finalizers = []string{apiconstants.FinalizerPodCliqueSet}
			pcs.Generation = 1
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v1"}}
			current, err := testutils.NewPodCliqueSetControllerRevision(pcs)
			require.NoError(t, err)
			require.NoError(t, mutateSelector(pcs))
			mutateUpdateInProgressCondition(pcs, updateInProgressCounts{})
			pcs.Generation++
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v2"

			storage := testutils.SetupFakeClient(pcs, current)
			conflict := apierrors.NewConflict(grovecorev1alpha1.Resource("podcliquesets"), pcs.Name, errors.New("concurrent status write"))
			failWrites := true
			updateAttempts, patchAttempts, deleteAttempts := 0, 0, 0
			cl := interceptor.NewClient(storage, interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, cl client.Client, subresource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					updateAttempts++
					if failWrites {
						return conflict
					}
					return cl.SubResource(subresource).Update(ctx, obj, opts...)
				},
				SubResourcePatch: func(ctx context.Context, cl client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					patchAttempts++
					if failWrites && tt.recordConflict {
						return conflict
					}
					return cl.SubResource(subresource).Patch(ctx, obj, patch, opts...)
				},
				Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deleteAttempts++
					return cl.Delete(ctx, obj, opts...)
				},
			})
			r := &Reconciler{
				client: cl, apiReader: storage,
				reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(cl),
				operatorRegistry:        component.NewOperatorRegistry[grovecorev1alpha1.PodCliqueSet](),
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pcs)}

			_, err = r.Reconcile(ctx, request)
			require.Error(t, err)
			assert.True(t, apierrors.IsConflict(err), "%v", err)
			assert.Equal(t, 1, updateAttempts)
			assert.Equal(t, 1, patchAttempts)
			assert.Zero(t, deleteAttempts, "failed spec reconciliation must not truncate history")
			persisted := &grovecorev1alpha1.PodCliqueSet{}
			require.NoError(t, storage.Get(ctx, request.NamespacedName, persisted))
			assert.Equal(t, current.Name, ptr.Deref(persisted.Status.CurrentRevision, ""))
			require.NoError(t, storage.Get(ctx, client.ObjectKeyFromObject(current), &appsv1.ControllerRevision{}),
				"the persisted selection must remain readable")

			failWrites = false
			// Restart without any in-memory expectations after the failed status writes.
			r = &Reconciler{
				client: cl, apiReader: storage,
				reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(cl),
				operatorRegistry:        component.NewOperatorRegistry[grovecorev1alpha1.PodCliqueSet](),
			}
			result, err := r.Reconcile(ctx, request)
			require.NoError(t, err)
			assert.Positive(t, result.RequeueAfter)
			require.NoError(t, storage.Get(ctx, request.NamespacedName, persisted))
			require.NotEqual(t, current.Name, ptr.Deref(persisted.Status.CurrentRevision, ""))
			selected := *persisted.Status.CurrentRevision
			result, err = r.Reconcile(ctx, request)
			require.NoError(t, err)
			assert.Zero(t, result.RequeueAfter)
			require.NoError(t, storage.Get(ctx, request.NamespacedName, persisted))
			assert.Equal(t, selected, *persisted.Status.CurrentRevision)
			assert.Equal(t, pcs.Generation, ptr.Deref(persisted.Status.ObservedGeneration, 0))
			require.NoError(t, storage.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: selected}, &appsv1.ControllerRevision{}))
			assert.True(t, apierrors.IsNotFound(storage.Get(ctx, client.ObjectKeyFromObject(current), &appsv1.ControllerRevision{})),
				"the old revision can be removed after the new selection is persisted")
		})
	}
}

func TestReconcileRevisionCacheLag(t *testing.T) {
	for _, tt := range []struct {
		name             string
		existingRevision bool
	}{
		{name: "initial revision"},
		{name: "template update", existingRevision: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-namespace", uuid.NewUUID()).
				WithReplicas(0).
				WithPodCliqueParameters("worker", 1, nil).
				Build()
			pcs.Finalizers = []string{apiconstants.FinalizerPodCliqueSet}
			pcs.Generation = 1
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v1"}}
			objects := []client.Object{pcs}
			if tt.existingRevision {
				revision, err := testutils.NewPodCliqueSetControllerRevision(pcs)
				require.NoError(t, err)
				objects = append(objects, revision)
				pcs.Generation++
				pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v2"
			}
			stalePCS := pcs.DeepCopy()
			storage := testutils.SetupFakeClient(objects...)
			revisionVisible, staleStatus := false, false
			createdRevision := ""
			revisionWrites := 0
			cl := interceptor.NewClient(storage, interceptor.Funcs{
				Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*appsv1.ControllerRevision); ok {
						createdRevision = obj.GetName()
						revisionWrites++
					}
					return cl.Create(ctx, obj, opts...)
				},
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if cachedPCS, ok := obj.(*grovecorev1alpha1.PodCliqueSet); ok && staleStatus {
						stalePCS.DeepCopyInto(cachedPCS)
						return nil
					}
					if _, ok := obj.(*appsv1.ControllerRevision); ok && key.Name == createdRevision && !revisionVisible {
						return apierrors.NewNotFound(appsv1.Resource("controllerrevisions"), key.Name)
					}
					return cl.Get(ctx, key, obj, opts...)
				},
			})
			r := &Reconciler{
				client: cl, apiReader: storage,
				reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(cl),
				operatorRegistry:        component.NewOperatorRegistry[grovecorev1alpha1.PodCliqueSet](),
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pcs)}

			result, err := r.Reconcile(ctx, request)
			require.NoError(t, err, "a newly created revision need not be visible in the informer yet")
			assert.Positive(t, result.RequeueAfter)
			persisted := &grovecorev1alpha1.PodCliqueSet{}
			require.NoError(t, storage.Get(ctx, request.NamespacedName, persisted))
			require.NotEmpty(t, createdRevision)
			require.Equal(t, createdRevision, ptr.Deref(persisted.Status.CurrentRevision, ""))
			progress := persisted.Status.UpdateProgress.DeepCopy()

			staleStatus = true
			result, err = r.Reconcile(ctx, request)
			require.NoError(t, err, "stale PCS status must wait for the successful revision write")
			assert.Positive(t, result.RequeueAfter)
			assert.Equal(t, 1, revisionWrites, "cache lag must not reinitialize the revision")
			_, pending := r.pcsRevisionExpectations.Load(pcs.UID)
			assert.True(t, pending)
			require.NoError(t, storage.Get(ctx, request.NamespacedName, persisted))
			assert.Equal(t, progress, persisted.Status.UpdateProgress)
			assert.Empty(t, persisted.Status.LastErrors)

			stalePCS.Status.CurrentRevision = persisted.Status.CurrentRevision
			result, err = r.Reconcile(ctx, request)
			require.NoError(t, err, "a mismatched generation hash must also preserve the expectation")
			assert.Positive(t, result.RequeueAfter)
			assert.Equal(t, 1, revisionWrites)

			staleStatus = false
			_, err = r.Reconcile(ctx, request)
			require.NoError(t, err, "observing PCS status may precede observing its revision")
			assert.Equal(t, 1, revisionWrites)

			revisionVisible = true
			r.pcsRevisionExpectations.Clear()
			result, err = r.Reconcile(ctx, request)
			require.NoError(t, err, "reconciliation must also converge after an operator restart")
			assert.Zero(t, result.RequeueAfter)
			require.NoError(t, storage.Get(ctx, request.NamespacedName, persisted))
			assert.Equal(t, createdRevision, *persisted.Status.CurrentRevision)
			assert.Equal(t, pcs.Generation, ptr.Deref(persisted.Status.ObservedGeneration, 0))
			assert.Equal(t, progress, persisted.Status.UpdateProgress)
			assert.Equal(t, 1, revisionWrites)
		})
	}
}

func TestProcessRevisionAdoptsLegacyIdentity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*grovecorev1alpha1.PodCliqueSet)
	}{
		{name: "unchanged workload"},
		{name: "scale before adoption", mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
			pcs.Spec.Replicas = 2
			pcs.Generation++
		}},
		{name: "scale to zero before adoption", mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
			pcs.Spec.Replicas = 0
			pcs.Generation++
		}},
		{name: "missing observed generation", mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
			pcs.Status.ObservedGeneration = nil
		}},
		{name: "existing update progress", mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
			pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.NewTime(time.Unix(1000, 0).UTC())}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pcs, _ := legacyRevisionFixture("worker")
			if tt.mutate != nil {
				tt.mutate(pcs)
			}
			progress := pcs.Status.UpdateProgress.DeepCopy()
			// No children are needed when the generation hash proves equivalence.
			cl := testutils.SetupFakeClient(pcs)
			r := &Reconciler{client: cl, apiReader: cl}
			data := processAndReadRevision(t, r, pcs)
			assert.Equal(t, legacyWorkerHash, data.Cliques[0].Hash)
			assert.Equal(t, legacyWorkerHash, data.GenerationHash)
			assert.True(t, equality.Semantic.DeepEqual(progress, pcs.Status.UpdateProgress), "adoption must preserve existing update progress")
			assert.EqualValues(t, 1, pcs.Status.UpdatedReplicas)

			selected := *pcs.Status.CurrentRevision
			// Restarting the controller must reload and retain the same identity.
			processAndReadRevision(t, &Reconciler{client: cl, apiReader: cl}, pcs)
			assert.Equal(t, selected, *pcs.Status.CurrentRevision)
		})
	}
}

func TestProcessRevisionAdoptsMixedLegacyReplicas(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("mixed", "test", "pcs-uid").
		WithReplicas(1).
		WithScalingGroupConfig("group", []string{"worker"}, 2, 1).
		Build()
	pcs.Generation = 2
	pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy}
	pcs.Status.ObservedGeneration = ptr.To(pcs.Generation)
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v2"}}
	desired, err := commonrevision.PodCliqueSetData(pcs)
	require.NoError(t, err)
	pcs.Status.CurrentGenerationHash = ptr.To(desired.GenerationHash)
	// OnDelete PCS sync can finish before the independent PCSG controller.
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		UpdateStartedAt: metav1.Now(),
		UpdateEndedAt:   ptr.To(metav1.Now()),
	}
	oldPCS := pcs.DeepCopy()
	oldPCS.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v1"
	pcsgName := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, "group")
	pcsg := testutils.NewPodCliqueScalingGroupBuilder(pcsgName, pcs.Namespace, pcs.Name, 0).
		WithReplicas(2).
		WithCliqueNames([]string{"worker"}).
		WithOwnerReference("PodCliqueSet", pcs.Name, pcs.UID).
		Build()
	pcsg.UID = "pcsg-uid"
	objects := []client.Object{pcs, pcsg}
	var children []grovecorev1alpha1.PodClique
	for i, source := range []*grovecorev1alpha1.PodCliqueSet{pcs, oldPCS} {
		name := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgName, Replica: i}, "worker")
		data, err := commonrevision.PodCliqueSetData(source)
		require.NoError(t, err)
		pclq := testutils.NewPCSGPodCliqueBuilder(name, pcs.Namespace, pcs.Name, pcsgName, 0, i).
			WithLabels(map[string]string{apicommon.LabelPodTemplateHash: data.Cliques[0].Hash}).
			WithOwnerReference("PodCliqueScalingGroup", pcsg.Name, pcsg.UID).
			Build()
		pclq.Spec = *source.Spec.Template.Cliques[0].Spec.DeepCopy()
		objects = append(objects, pclq)
		children = append(children, *pclq)
	}
	cl := testutils.SetupFakeClient(objects...)
	data := processAndReadRevision(t, &Reconciler{client: cl, apiReader: cl}, pcs)
	assert.Equal(t, desired.Cliques[0].Hash, data.Cliques[0].Hash)
	assert.Equal(t, desired.GenerationHash, data.GenerationHash)
	revision, err := componentutils.GetPodCliqueSetRevision(context.Background(), cl, pcs)
	require.NoError(t, err)
	assert.Equal(t, []string{children[1].Name}, componentutils.GetPCLQsInPCSGPendingUpdate(revision, pcs, pcsg, children))
}

func TestProcessRevisionRetainsUnchangedCliqueIdentity(t *testing.T) {
	for _, adoptFirst := range []bool{false, true} {
		name := "edit before adoption"
		if adoptFirst {
			name = "edit after adoption"
		}
		t.Run(name, func(t *testing.T) {
			pcs, objects := legacyRevisionFixture("frontend", "worker")
			cl := testutils.SetupFakeClient(objects...)
			r := &Reconciler{client: cl, apiReader: cl}
			if adoptFirst {
				processAndReadRevision(t, r, pcs)
			}
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "frontend:v2"
			pcs.Generation++
			require.NoError(t, cl.Update(context.Background(), pcs))
			after := processAndReadRevision(t, r, pcs)
			assert.Equal(t, legacyWorkerHash, after.Cliques[1].Hash)
			assert.NotEqual(t, legacyFrontendHash, after.Cliques[0].Hash)
			assert.NotEqual(t, legacyPairHash, after.GenerationHash)
			require.NotNil(t, pcs.Status.UpdateProgress)
			assert.Nil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
			assert.Zero(t, pcs.Status.UpdatedReplicas)
		})
	}
}

func TestProcessRevisionAdoptsExplicitPodPriority(t *testing.T) {
	pcs, objects := legacyRevisionFixture("worker")
	pcs.Spec.Template.PriorityClassName = "gang-priority"
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.PriorityClassName = "pod-priority"
	pcs.Status.CurrentGenerationHash = ptr.To(legacyPriorityHash)
	objects[1].(*grovecorev1alpha1.PodClique).Spec.PodSpec.PriorityClassName = "pod-priority"
	objects[1].(*grovecorev1alpha1.PodClique).Labels[apicommon.LabelPodTemplateHash] = legacyPriorityHash
	cl := testutils.SetupFakeClient(objects...)
	r := &Reconciler{client: cl, apiReader: cl}
	before := processAndReadRevision(t, r, pcs)
	selected := *pcs.Status.CurrentRevision
	var template corev1.PodTemplateSpec
	require.NoError(t, json.Unmarshal(before.Cliques[0].Template, &template))
	assert.Equal(t, "pod-priority", template.Spec.PriorityClassName)
	assert.Equal(t, legacyPriorityHash, before.Cliques[0].Hash)

	after := processAndReadRevision(t, r, pcs)
	assert.Equal(t, selected, *pcs.Status.CurrentRevision)
	assert.Equal(t, before.Cliques[0].Hash, after.Cliques[0].Hash)
	assert.Nil(t, pcs.Status.UpdateProgress)
}

func TestProcessRevisionRejectsUnverifiableLegacyIdentity(t *testing.T) {
	pcs, objects := legacyRevisionFixture("worker")
	pcs.Status.CurrentGenerationHash = ptr.To("unknown-legacy-generation")
	cl := testutils.SetupFakeClient(objects...)
	r := &Reconciler{client: cl, apiReader: cl}
	result := r.processRevision(context.Background(), logr.Discard(), pcs)
	require.True(t, result.HasErrors())
	_, err := result.Result()
	require.ErrorContains(t, err, "cannot verify legacy generation hash")
	assert.Nil(t, pcs.Status.CurrentRevision)
	revisions := &appsv1.ControllerRevisionList{}
	require.NoError(t, cl.List(context.Background(), revisions))
	assert.Empty(t, revisions.Items)
}

func TestProcessRevisionRejectsAmbiguousLegacyCliqueIdentity(t *testing.T) {
	pcs, objects := legacyRevisionFixture("frontend", "worker")
	current, err := commonrevision.PodCliqueSetData(pcs)
	require.NoError(t, err)
	duplicate := objects[2].(*grovecorev1alpha1.PodClique).DeepCopy()
	duplicate.Name = pcs.Name + "-1-worker"
	duplicate.Labels[apicommon.LabelPodTemplateHash] = current.Cliques[1].Hash
	objects = append(objects, duplicate)
	pcs.Generation++
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "frontend:v2"
	cl := testutils.SetupFakeClient(objects...)
	r := &Reconciler{client: cl, apiReader: cl}
	result := r.processRevision(context.Background(), logr.Discard(), pcs)
	require.True(t, result.HasErrors())
	_, err = result.Result()
	require.ErrorContains(t, err, "multiple legacy identities for clique worker")
	assert.Nil(t, pcs.Status.CurrentRevision)
}

func TestProcessRevisionAdoptionUsesUncachedReader(t *testing.T) {
	pcs, objects := legacyRevisionFixture("frontend", "worker")
	pcs.Generation++
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "frontend:v2"
	cl := testutils.SetupFakeClient(pcs)
	apiReader := testutils.SetupFakeClient(objects...)
	after := processAndReadRevision(t, &Reconciler{client: cl, apiReader: apiReader}, pcs)
	assert.Equal(t, legacyWorkerHash, after.Cliques[1].Hash)
}

func TestProcessRevisionAdoptionPropagatesReadErrors(t *testing.T) {
	pcs, objects := legacyRevisionFixture("frontend", "worker")
	pcs.Generation++
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "frontend:v2"
	cl := testutils.SetupFakeClient(objects...)
	readError := assert.AnError
	apiReader := interceptor.NewClient(cl, interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return readError
		},
	})
	result := (&Reconciler{client: cl, apiReader: apiReader}).processRevision(context.Background(), logr.Discard(), pcs)
	require.True(t, result.HasErrors())
	_, err := result.Result()
	require.ErrorIs(t, err, readError)
	assert.Nil(t, pcs.Status.CurrentRevision)
}

func TestExistingPodTemplateHashesChecksOwnership(t *testing.T) {
	pcs, objects := legacyRevisionFixture("worker")
	standalone := objects[1].(*grovecorev1alpha1.PodClique)
	standalone.UID = "standalone-uid"
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("group", pcs.Namespace, pcs.Name, 0).
		WithOwnerReference("PodCliqueSet", pcs.Name, pcs.UID).
		Build()
	pcsg.UID = "pcsg-uid"
	member := standalone.DeepCopy()
	member.Name, member.UID = "member", "member-uid"
	member.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(pcsg, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueScalingGroup"))}
	member.Labels[apicommon.LabelPodTemplateHash] = "member-hash"
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "pod", Namespace: pcs.Namespace,
		Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(member, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodClique"))},
	}}
	pod.Labels[apicommon.LabelPodTemplateHash] = "pod-hash"
	foreignGroup := pcsg.DeepCopy()
	foreignGroup.Name, foreignGroup.UID = "foreign-group", "foreign-group-uid"
	foreignGroup.OwnerReferences[0].UID = "another-pcs"
	foreignMember := member.DeepCopy()
	foreignMember.Name, foreignMember.UID = "foreign-member", "foreign-member-uid"
	foreignMember.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(foreignGroup, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueScalingGroup"))}
	foreignMember.Labels[apicommon.LabelPodTemplateHash] = "foreign-member-hash"
	foreignPod := pod.DeepCopy()
	foreignPod.Name = "foreign-pod"
	foreignPod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(foreignMember, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodClique"))}
	foreignPod.Labels[apicommon.LabelPodTemplateHash] = "foreign-pod-hash"
	orphan := standalone.DeepCopy()
	orphan.Name, orphan.UID = "orphan", "orphan-uid"
	orphan.OwnerReferences = nil
	orphan.Labels[apicommon.LabelPodTemplateHash] = "orphan-hash"
	objects = append(objects, pcsg, member, pod, foreignGroup, foreignMember, foreignPod, orphan)
	cl := testutils.SetupFakeClient(objects...)
	hashes, err := (&Reconciler{client: cl, apiReader: cl}).existingPodTemplateHashes(context.Background(), pcs)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{legacyWorkerHash, "member-hash", "pod-hash"}, hashes.UnsortedList())
}

func TestProcessRevisionPreservesCoherentUpdateScope(t *testing.T) {
	tests := []struct {
		name              string
		previousUpdate    bool
		pendingGeneration string
		wantScalingGroups []string
	}{
		{
			name: "unchanged components retain legacy identities outside the new scope",
		},
		{
			name:              "unfinished components from the previous update remain in scope",
			previousUpdate:    true,
			pendingGeneration: "older-generation",
			wantScalingGroups: []string{"decode"},
		},
		{
			name:              "completed components from the previous update leave the scope",
			previousUpdate:    true,
			pendingGeneration: "previous-generation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneClique("frontend").
				WithStandaloneClique("router").
				WithScalingGroupConfig("decode", []string{"decodeworker"}, 2, 1).
				WithScalingGroupConfig("prefill", []string{"prefillworker"}, 2, 1).
				WithPodCliqueSetGenerationHash(ptr.To("previous-generation")).
				WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}).
				Build()
			pcs.Generation = 1
			for _, clique := range pcs.Spec.Template.Cliques {
				clique.Spec.PodSpec.Containers = []corev1.Container{{Name: clique.Name, Image: clique.Name + ":v1"}}
			}
			oldRevision, err := testutils.NewPodCliqueSetControllerRevision(pcs)
			require.NoError(t, err)
			data := commonrevision.Data{}
			require.NoError(t, json.Unmarshal(oldRevision.Data.Raw, &data))
			for i := range data.Cliques {
				data.Cliques[i].Hash = "legacy-" + data.Cliques[i].Name
			}
			oldRevision.Data.Raw, err = json.Marshal(data)
			require.NoError(t, err)
			if tt.previousUpdate {
				pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
					UpdateStartedAt:               metav1.Now(),
					InScopePodCliqueScalingGroups: []string{"decode"},
				}
			}
			pgm := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 0).
				WithEntries(grovecorev1alpha1.PodGangEntry{
					PodCliqueSetGenerationHash: tt.pendingGeneration,
					PCSGReplicaIndices:         map[string][]int32{"decode": {0}},
				}).Build()
			objects := []client.Object{pcs, oldRevision, pgm}
			for _, name := range []string{"frontend", "router"} {
				objects = append(objects, testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, name, pcs.Namespace, 0).
					WithLabels(map[string]string{apicommon.LabelPodTemplateHash: "legacy-" + name}).Build())
			}
			for _, name := range []string{"decode", "prefill"} {
				pcsgName := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, name)
				cliqueName := name + "worker"
				pclqName := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgName, Replica: 0}, cliqueName)
				objects = append(objects, testutils.NewPCSGPodCliqueBuilder(pclqName, pcs.Namespace, pcs.Name, pcsgName, 0, 0).
					WithLabels(map[string]string{apicommon.LabelPodTemplateHash: "legacy-" + cliqueName}).Build())
			}

			pcs.Generation++
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "frontend:v2"
			cl := testutils.SetupFakeClient(objects...)
			r := &Reconciler{client: cl, apiReader: cl}
			result := r.processRevision(ctx, logr.Discard(), pcs)
			require.False(t, result.HasErrors(), "%v", result.GetErrors())
			assert.True(t, result.NeedsRequeue())

			updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
			require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(pcs), updatedPCS))
			require.NotNil(t, updatedPCS.Status.UpdateProgress)
			assert.Equal(t, []string{"frontend"}, updatedPCS.Status.UpdateProgress.InScopeStandalonePodCliques)
			assert.ElementsMatch(t, tt.wantScalingGroups, updatedPCS.Status.UpdateProgress.InScopePodCliqueScalingGroups)
			assert.Nil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt)
			assert.Empty(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating)
			assert.NotEqual(t, "previous-generation", ptr.Deref(updatedPCS.Status.CurrentGenerationHash, ""))

			revision, err := componentutils.GetPodCliqueSetRevision(ctx, cl, updatedPCS)
			require.NoError(t, err)
			for _, name := range []string{"router", "decodeworker", "prefillworker"} {
				hash, err := revision.CliqueHash(name)
				require.NoError(t, err)
				assert.Equal(t, "legacy-"+name, hash)
			}
		})
	}
}

func TestReconcileRevisionAfterCRDUpgrade(t *testing.T) {
	const (
		schemaUpdateTimeout = 15 * time.Second
		pollInterval        = 100 * time.Millisecond
	)
	options := envtest.CRDInstallOptions{
		Paths:              []string{"../../../api/core/v1alpha1/crds"},
		ErrorIfPathMissing: true,
	}
	require.NoError(t, envtest.ReadCRDFiles(&options))
	var currentCRD *apiextensionsv1.CustomResourceDefinition
	for _, crd := range options.CRDs {
		if crd.Name == "podcliquesets.grove.io" {
			currentCRD = crd.DeepCopy()
			for i := range crd.Spec.Versions {
				delete(crd.Spec.Versions[i].Schema.OpenAPIV3Schema.Properties["status"].Properties, "currentRevision")
			}
		}
	}
	require.NotNil(t, currentCRD)

	testEnv := &envtest.Environment{CRDs: options.CRDs}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Skipf("Skipping test: kubebuilder test environment not available: %v", err)
		return
	}
	t.Cleanup(func() { require.NoError(t, testEnv.Stop()) })

	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	ctx := t.Context()
	require.NoError(t, cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "revision-upgrade"}}))
	r := &Reconciler{
		client: cl, apiReader: cl,
		reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(cl),
		operatorRegistry:        component.NewOperatorRegistry[grovecorev1alpha1.PodCliqueSet](),
	}

	var workloads []*grovecorev1alpha1.PodCliqueSet
	selectedRevisions := make(map[string]string)
	for _, tt := range []struct {
		name           string
		legacy         bool
		templateChange bool
	}{
		{name: "new-workload"},
		{name: "legacy-workload", legacy: true},
		{name: "pending-update", legacy: true, templateChange: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder(tt.name, "revision-upgrade", "").
				WithReplicas(0).
				WithPodCliqueParameters("worker", 1, nil).
				Build()
			pcs.Finalizers = []string{apiconstants.FinalizerPodCliqueSet}
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v1"}}
			require.NoError(t, cl.Create(ctx, pcs))
			if tt.legacy {
				data, err := commonrevision.PodCliqueSetData(pcs)
				require.NoError(t, err)
				pcs.Status.CurrentGenerationHash = ptr.To(data.GenerationHash)
				pcs.Status.ObservedGeneration = ptr.To(pcs.Generation)
				require.NoError(t, cl.Status().Update(ctx, pcs))
			}
			if tt.templateChange {
				pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v2"
				require.NoError(t, cl.Update(ctx, pcs))
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pcs)}
			// A second attempt must reach revision processing instead of waiting on
			// an expectation for the field that the old schema never persisted.
			for range 2 {
				require.NotPanics(t, func() {
					_, err = r.Reconcile(ctx, request)
				})
				require.Error(t, err)
				revisions := &appsv1.ControllerRevisionList{}
				require.NoError(t, cl.List(ctx, revisions, client.InNamespace(pcs.Namespace),
					client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))))
				require.Len(t, revisions.Items, 1, "retries must reuse the persisted revision")
				selectedRevisions[pcs.Name] = revisions.Items[0].Name
				var groveErr *groveerr.GroveError
				require.ErrorAs(t, err, &groveErr)
				assert.Equal(t, errCodeRevisionStatusNotPersisted, groveErr.Code)
				assert.Equal(t, component.OperationSync, groveErr.Operation)
				assert.Equal(t, fmt.Sprintf(
					"could not persist status.currentRevision %q for PodCliqueSet %v: apply the updated PodCliqueSet CRD before upgrading the operator",
					revisions.Items[0].Name, request.NamespacedName), groveErr.Message)
				_, pending := r.pcsRevisionExpectations.Load(pcs.UID)
				assert.False(t, pending, "a pruned status field must not leave an expectation")
				require.NoError(t, cl.Get(ctx, request.NamespacedName, pcs))
				assert.Nil(t, pcs.Status.CurrentRevision)
				require.Len(t, pcs.Status.LastErrors, 1)
				assert.Equal(t, errCodeRevisionStatusNotPersisted, pcs.Status.LastErrors[0].Code)
				assert.Equal(t, err.Error(), pcs.Status.LastErrors[0].Description)
			}
			workloads = append(workloads, pcs)
		})
	}
	require.Len(t, workloads, 3)

	_, err = envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
		CRDs: []*apiextensionsv1.CustomResourceDefinition{currentCRD},
	})
	require.NoError(t, err)
	for _, pcs := range workloads {
		t.Run(pcs.Name+"-recovers", func(t *testing.T) {
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pcs)}
			err := wait.PollUntilContextTimeout(ctx, pollInterval, schemaUpdateTimeout, true, func(ctx context.Context) (bool, error) {
				if _, err := r.Reconcile(ctx, request); err != nil {
					var groveErr *groveerr.GroveError
					if errors.As(err, &groveErr) && groveErr.Code == errCodeRevisionStatusNotPersisted {
						return false, nil
					}
					return false, err
				}
				if err := cl.Get(ctx, request.NamespacedName, pcs); err != nil {
					return false, err
				}
				return pcs.Status.CurrentRevision != nil, nil
			})
			require.NoError(t, err, "updating the CRD must recover the same reconciler without a restart")
			require.Equal(t, selectedRevisions[pcs.Name], ptr.Deref(pcs.Status.CurrentRevision, ""))
			result, err := r.Reconcile(ctx, request)
			require.NoError(t, err)
			assert.Zero(t, result.RequeueAfter)
			require.NoError(t, cl.Get(ctx, request.NamespacedName, pcs))
			assert.Equal(t, pcs.Generation, ptr.Deref(pcs.Status.ObservedGeneration, 0))
			assert.Empty(t, pcs.Status.LastErrors)
		})
	}
}

func legacyRevisionFixture(names ...string) (*grovecorev1alpha1.PodCliqueSet, []client.Object) {
	builder := testutils.NewPodCliqueSetBuilder("survivor", "test", "pcs-uid").WithReplicas(1)
	for _, name := range names {
		builder.WithPodCliqueParameters(name, 1, nil)
	}
	pcs := builder.Build()
	pcs.Generation = 1
	pcs.Status.ObservedGeneration = ptr.To(pcs.Generation)
	pcs.Status.CurrentGenerationHash = ptr.To(legacyWorkerHash)
	if len(names) == 2 {
		pcs.Status.CurrentGenerationHash = ptr.To(legacyPairHash)
	}
	pcs.Status.UpdatedReplicas = 1
	objects := []client.Object{pcs}
	for _, clique := range pcs.Spec.Template.Cliques {
		clique.Spec.PodSpec = corev1.PodSpec{Containers: []corev1.Container{{Name: clique.Name, Image: clique.Name + ":v1"}}}
		hash := legacyWorkerHash
		if clique.Name == "frontend" {
			hash = legacyFrontendHash
		}
		pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, clique.Name, pcs.Namespace, 0).
			WithLabels(map[string]string{apicommon.LabelPodTemplateHash: hash}).
			Build()
		pclq.Spec = *clique.Spec.DeepCopy()
		objects = append(objects, pclq)
	}
	return pcs, objects
}

func processAndReadRevision(t *testing.T, r *Reconciler, pcs *grovecorev1alpha1.PodCliqueSet) commonrevision.Data {
	t.Helper()
	ctx := componentutils.WithPodCliqueSetRevisionCache(context.Background())
	result := r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors(), "processRevision failed: %v", result.GetErrors())
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(pcs), pcs))
	require.NotNil(t, pcs.Status.CurrentRevision)
	revision := &appsv1.ControllerRevision{}
	require.NoError(t, r.client.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: *pcs.Status.CurrentRevision}, revision))
	var data commonrevision.Data
	require.NoError(t, json.Unmarshal(revision.Data.Raw, &data))
	return data
}
