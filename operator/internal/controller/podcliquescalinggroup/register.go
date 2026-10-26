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

package podcliquescalinggroup

import (
	"context"
	"maps"
	"slices"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	pcsgexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliquescalinggroup/expectations"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	ctrllogger "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	controllerName = "podcliquescalinggroup-controller"
)

// RegisterWithManager registers the PodCliqueScalingGroup Reconciler with the manager.
// This reconciler will only be called when the PodCliqueScalingGroup resource is updated. The resource can either be
// updated by an HPA or an equivalent external components.
func (r *Reconciler) RegisterWithManager(mgr manager.Manager) error {
	return builder.ControllerManagedBy(mgr).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: *r.config.ConcurrentSyncs,
		}).
		For(&grovecorev1alpha1.PodCliqueScalingGroup{}, builder.WithPredicates(
			predicate.And(
				podCliqueScalingGroupReconcilePredicate(),
				podCliqueScalingGroupUpdatePredicate(),
			)),
		).
		Watches(&grovecorev1alpha1.PodCliqueSet{},
			handler.EnqueueRequestsFromMapFunc(mapPCSToPCSG(r.client)),
			builder.WithPredicates(podCliqueSetPredicate()),
		).
		Watches(&grovecorev1alpha1.PodClique{},
			handler.EnqueueRequestsFromMapFunc(mapPCLQToPCSG()),
			builder.WithPredicates(r.podCliquePredicate()),
		).
		Watches(&grovecorev1alpha1.PodGangMap{},
			handler.EnqueueRequestsFromMapFunc(mapPodGangMapToPCSGs()),
			builder.WithPredicates(podGangMapPredicate()),
		).
		Complete(r)
}

// podCliqueScalingGroupReconcilePredicate admits creates, spec changes, and changes to the parent
// PodCliqueSet generation annotation used to trigger OnDelete template propagation.
func podCliqueScalingGroupReconcilePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return true },
		DeleteFunc: func(_ event.DeleteEvent) bool { return false },
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			oldPCSG, oldOK := updateEvent.ObjectOld.(*grovecorev1alpha1.PodCliqueScalingGroup)
			newPCSG, newOK := updateEvent.ObjectNew.(*grovecorev1alpha1.PodCliqueScalingGroup)
			if !oldOK || !newOK {
				return false
			}
			return oldPCSG.Generation != newPCSG.Generation ||
				oldPCSG.Annotations[constants.AnnotationOnDeletePodCliqueSetGenerationHash] != newPCSG.Annotations[constants.AnnotationOnDeletePodCliqueSetGenerationHash]
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// podCliqueScalingGroupUpdatePredicate filters PodCliqueScalingGroup events to only process Grove-managed resources owned by PodCliqueSet
func podCliqueScalingGroupUpdatePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(createEvent event.CreateEvent) bool {
			return ctrlutils.IsManagedByGrove(createEvent.Object.GetLabels()) &&
				ctrlutils.HasExpectedOwner(constants.KindPodCliqueSet, createEvent.Object.GetOwnerReferences())
		},
		DeleteFunc: func(_ event.DeleteEvent) bool { return false },
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return ctrlutils.IsManagedByGrove(updateEvent.ObjectOld.GetLabels()) &&
				ctrlutils.HasExpectedOwner(constants.KindPodCliqueSet, updateEvent.ObjectOld.GetOwnerReferences())
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// mapPCSToPCSG maps a PCS revision change to its actual PodCliqueScalingGroups. During an automatic
// update, it limits the requests to the selected replica. Listing the actual resources preserves
// sparse replica identities.
func mapPCSToPCSG(cl client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		pcs, ok := obj.(*grovecorev1alpha1.PodCliqueSet)
		if !ok {
			return nil
		}
		pcsgsByReplicaIndex, err := componentutils.GetPCSGsByPCSReplicaIndex(ctx, cl, pcs.ObjectMeta)
		if err != nil {
			ctrllogger.FromContext(ctx).Error(err, "Failed to list PodCliqueScalingGroups for PodCliqueSet", "podCliqueSet", client.ObjectKeyFromObject(pcs))
			return nil
		}

		pcsgs := lo.Flatten(lo.Values(pcsgsByReplicaIndex))
		if replicaIndex, selected := currentPCSReplicaInUpdate(pcs.Status.UpdateProgress); componentutils.IsRollingUpdateStrategy(pcs) && selected {
			pcsgs = pcsgsByReplicaIndex[int(replicaIndex)]
		}
		return lo.Map(pcsgs, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup, _ int) reconcile.Request {
			return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&pcsg)}
		})
	}
}

// podCliqueSetPredicate filters PodCliqueSet events to revision changes and updates that select a new PCS replica.
func podCliqueSetPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(_ event.DeleteEvent) bool { return false },
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			oldPCS, okOld := updateEvent.ObjectOld.(*grovecorev1alpha1.PodCliqueSet)
			newPCS, okNew := updateEvent.ObjectNew.(*grovecorev1alpha1.PodCliqueSet)
			if !okOld || !okNew {
				return false
			}
			return oldPCS.Status.UpdateRevision != newPCS.Status.UpdateRevision ||
				componentutils.IsRollingUpdateStrategy(newPCS) &&
					hasNewPCSReplicaBeenSelectedForUpdate(oldPCS.Status.UpdateProgress, newPCS.Status.UpdateProgress)
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// hasNewPCSReplicaBeenSelectedForUpdate reports whether the PCS selected a new replica for update.
func hasNewPCSReplicaBeenSelectedForUpdate(oldProgress, newProgress *grovecorev1alpha1.PodCliqueSetUpdateProgress) bool {
	oldReplicaIndex, oldOK := currentPCSReplicaInUpdate(oldProgress)
	newReplicaIndex, newOK := currentPCSReplicaInUpdate(newProgress)
	if !newOK {
		return false
	}
	return !oldOK || oldReplicaIndex != newReplicaIndex
}

// currentPCSReplicaInUpdate returns the PCS replica index currently being updated, if any.
func currentPCSReplicaInUpdate(progress *grovecorev1alpha1.PodCliqueSetUpdateProgress) (int32, bool) {
	if progress == nil || len(progress.CurrentlyUpdating) == 0 {
		return 0, false
	}
	return progress.CurrentlyUpdating[0].ReplicaIndex, true
}

// mapPCLQToPCSG maps PodClique events to their owning PodCliqueScalingGroup reconcile requests
func mapPCLQToPCSG() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		pclq, ok := obj.(*grovecorev1alpha1.PodClique)
		if !ok {
			return nil
		}
		pcsgName, ok := pclq.GetLabels()[apicommon.LabelPodCliqueScalingGroup]
		if !ok || lo.IsEmpty(pcsgName) {
			return nil
		}
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: pcsgName, Namespace: pclq.Namespace}}}
	}
}

// podCliquePredicate filters PodClique events to only process those managed by PodCliqueScalingGroup
func (r *Reconciler) podCliquePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
			if !ctrlutils.IsManagedPodClique(deleteEvent.Object, constants.KindPodCliqueScalingGroup) {
				return false
			}
			r.observeMemberPodCliqueDeletion(deleteEvent.Object)
			return true
		},
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return ctrlutils.IsManagedPodClique(updateEvent.ObjectOld, constants.KindPodCliqueScalingGroup)
		},
		GenericFunc: func(_ event.TypedGenericEvent[client.Object]) bool { return false },
	}
}

// observeMemberPodCliqueDeletion lowers the PodCliqueScalingGroup's delete expectation for a deleted
// member PodClique so a disrupted replica stops counting as unavailable once its PodCliques are gone.
func (r *Reconciler) observeMemberPodCliqueDeletion(obj client.Object) {
	pclq, ok := obj.(*grovecorev1alpha1.PodClique)
	if !ok {
		return
	}
	if key, ok := pcsgexpectations.PCSGScopedExpectationsStoreKeyForMemberPodClique(pclq); ok {
		r.expectationStore.ObserveDeletions(ctrllogger.Log.WithName(controllerName), key, pclq.GetUID())
	}
}

// mapPodGangMapToPCSGs maps a PodGangMap to reconcile.Request(s) for the PodCliqueScalingGroups whose
// per-PodGang replica-index distribution it carries. A coherent-update sub-step can move a
// PodCliqueScalingGroup replica index from one entry to another without changing the total, which
// changes the PodGang a PodCliqueScalingGroup-owned PodClique must be stamped with. No
// PodCliqueScalingGroup, PodClique or PodCliqueSet watch observes that move, so the PodGangMap is the
// only signal. The requests cover every PodCliqueScalingGroup config named across the entries.
func mapPodGangMapToPCSGs() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		pgm, ok := obj.(*grovecorev1alpha1.PodGangMap)
		if !ok {
			return nil
		}
		pcsName := componentutils.GetPodCliqueSetName(pgm.ObjectMeta)
		rnr := apicommon.ResourceNameReplica{Name: pcsName, Replica: int(pgm.Spec.PodCliqueSetReplicaIndex)}
		pcsgConfigNames := make(map[string]struct{})
		for _, entry := range pgm.Spec.Entries {
			for pcsgConfigName := range entry.PCSGReplicaIndices {
				pcsgConfigNames[pcsgConfigName] = struct{}{}
			}
		}
		requests := make([]reconcile.Request, 0, len(pcsgConfigNames))
		for pcsgConfigName := range pcsgConfigNames {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{
				Namespace: pgm.Namespace,
				Name:      apicommon.GeneratePodCliqueScalingGroupName(rnr, pcsgConfigName),
			}})
		}
		return requests
	}
}

// podGangMapPredicate triggers the PodCliqueScalingGroup controller on a PodGangMap create and on an
// update that changes the per-entry PodCliqueScalingGroup replica-index placement. It does not fire
// on other spec churn such as standalone PodClique count moves, which this controller does not
// consume.
func podGangMapPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(_ event.CreateEvent) bool { return true },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPGM, okOld := e.ObjectOld.(*grovecorev1alpha1.PodGangMap)
			newPGM, okNew := e.ObjectNew.(*grovecorev1alpha1.PodGangMap)
			if !okOld || !okNew {
				return false
			}
			return pcsgPlacementChanged(oldPGM, newPGM)
		},
	}
}

// pcsgPlacementChanged reports whether the PodCliqueScalingGroup replica indices differ between the
// two PodGangMaps. It compares the maps returned by pcsgPlacement. Because those maps key the indices
// by both the entry epoch and the PodCliqueScalingGroup config name, moving a replica index from one
// entry to another with the same total still counts as a change.
func pcsgPlacementChanged(oldPGM, newPGM *grovecorev1alpha1.PodGangMap) bool {
	return !maps.EqualFunc(pcsgPlacement(oldPGM), pcsgPlacement(newPGM), slices.Equal[[]int32])
}

// pcsgPlacement returns the replica indices of every PodCliqueScalingGroup config. The key of each
// index slice is the entry epoch and the config name joined together, so the same config under two
// different epochs yields two distinct keys. The indices are written in a stable sorted order by the
// PodGangMap writer, so the slices are compared directly.
func pcsgPlacement(pgm *grovecorev1alpha1.PodGangMap) map[string][]int32 {
	placement := make(map[string][]int32)
	for _, entry := range pgm.Spec.Entries {
		for pcsgConfigName, indices := range entry.PCSGReplicaIndices {
			placement[entry.Epoch+"/"+pcsgConfigName] = indices
		}
	}
	return placement
}
