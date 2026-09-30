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
	"sync"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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
