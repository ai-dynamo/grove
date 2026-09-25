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
	"github.com/ai-dynamo/grove/operator/internal/utils"
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
				predicate.GenerationChangedPredicate{},
				podCliqueScalingGroupUpdatePredicate(),
			)),
		).
		Watches(&grovecorev1alpha1.PodCliqueSet{},
			handler.EnqueueRequestsFromMapFunc(mapPCSToPCSG()),
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

// mapPCSToPCSG maps PodCliqueSet rolling update events to PodCliqueScalingGroup reconcile requests for the currently updating replica
func mapPCSToPCSG() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		pcs, ok := obj.(*grovecorev1alpha1.PodCliqueSet)
		if !ok {
			return nil
		}
		if pcs.Status.UpdateProgress == nil {
			return nil
		}
		var pcsReplicaIndices []int32
		if componentutils.IsAutoUpdateStrategy(pcs) &&
			len(pcs.Status.UpdateProgress.CurrentlyUpdating) > 0 {
			// Rolling recreate needs to have a CurrentlyUpdating which is used to generate an event for the corresponding PCSG
			pcsReplicaIndices = lo.RangeFrom(pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex, 1)
		} else {
			// OnDelete will not have a specific CurrentlyUpdating, so PCSG resources of all PCS replicas are reconciled
			pcsReplicaIndices = lo.RangeFrom(int32(0), int(pcs.Spec.Replicas))
		}
		return pcsgReconcileRequestsForPCSReplicas(pcs, pcsReplicaIndices)
	}
}

func pcsgReconcileRequestsForPCSReplicas(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndices []int32) []reconcile.Request {
	pcsgConfigs := pcs.Spec.Template.PodCliqueScalingGroupConfigs
	if len(pcsgConfigs) == 0 {
		return nil
	}
	requests := make([]reconcile.Request, 0, int(pcs.Spec.Replicas)*len(pcsgConfigs))
	for _, pcsReplicaIndex := range pcsReplicaIndices {
		for _, pcsgConfig := range pcsgConfigs {
			pcsgName := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: int(pcsReplicaIndex)}, pcsgConfig.Name)
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKey{
					Name:      pcsgName,
					Namespace: pcs.Namespace,
				},
			})
		}
	}
	return requests
}

// podCliqueSetPredicate filters PodCliqueSet events to only process rolling update status changes
func podCliqueSetPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(_ event.DeleteEvent) bool { return false },
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return shouldEnqueueOnPCSUpdate(updateEvent)
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// shouldEnqueueOnPCSUpdate determines if a PodCliqueSet update event should trigger PodCliqueScalingGroup reconciliation based on rolling update progress changes
func shouldEnqueueOnPCSUpdate(event event.UpdateEvent) bool {
	oldPCS, okOld := event.ObjectOld.(*grovecorev1alpha1.PodCliqueSet)
	newPCS, okNew := event.ObjectNew.(*grovecorev1alpha1.PodCliqueSet)
	if !okOld || !okNew {
		return false
	}

	if oldPCS.Status.UpdateProgress != nil && newPCS.Status.UpdateProgress != nil {
		if utils.OnlyOneIsEmpty(oldPCS.Status.UpdateProgress.CurrentlyUpdating, newPCS.Status.UpdateProgress.CurrentlyUpdating) ||
			len(oldPCS.Status.UpdateProgress.CurrentlyUpdating) > 0 &&
				len(newPCS.Status.UpdateProgress.CurrentlyUpdating) > 0 &&
				oldPCS.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex != newPCS.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex {
			return true
		}
	}
	// Enqueue while using OnDelete since there is no CurrentlyUpdating
	if newPCS.Status.UpdateProgress != nil && !componentutils.IsAutoUpdateStrategy(newPCS) {
		return true
	}
	return false
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
