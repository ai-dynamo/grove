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

package podcliquescalinggroup

import (
	"context"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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
			builder.WithPredicates(podCliquePredicate()),
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
		pcsgConfigs := pcs.Spec.Template.PodCliqueScalingGroupConfigs
		if len(pcsgConfigs) == 0 {
			return nil
		}
		requests := make([]reconcile.Request, 0, int(pcs.Spec.Replicas)*len(pcsgConfigs))
		// We are only interested in PCS events during rolling update.
		if pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.CurrentlyUpdating != nil {
			pcsReplicaIndex := pcs.Status.UpdateProgress.CurrentlyUpdating.ReplicaIndex
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
		if utils.OnlyOneIsNil(oldPCS.Status.UpdateProgress.CurrentlyUpdating, newPCS.Status.UpdateProgress.CurrentlyUpdating) ||
			oldPCS.Status.UpdateProgress.CurrentlyUpdating != nil &&
				newPCS.Status.UpdateProgress.CurrentlyUpdating != nil &&
				oldPCS.Status.UpdateProgress.CurrentlyUpdating.ReplicaIndex != newPCS.Status.UpdateProgress.CurrentlyUpdating.ReplicaIndex {
			return true
		}
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
func podCliquePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
			return ctrlutils.IsManagedPodClique(deleteEvent.Object, constants.KindPodCliqueScalingGroup)
		},
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return ctrlutils.IsManagedPodClique(updateEvent.ObjectOld, constants.KindPodCliqueScalingGroup)
		},
	}
}
