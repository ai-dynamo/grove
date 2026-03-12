// /*
// Copyright 2024 The Grove Authors.
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
	"reflect"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	grovectrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	controllerName = "podcliqueset-controller"
)

// RegisterWithManager registers the PodCliqueSet Reconciler with the manager.
func (r *Reconciler) RegisterWithManager(mgr manager.Manager) error {
	return builder.ControllerManagedBy(mgr).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: *r.config.ConcurrentSyncs,
		}).
		For(&grovecorev1alpha1.PodCliqueSet{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&grovecorev1alpha1.PodClique{},
			handler.EnqueueRequestsFromMapFunc(mapPodCliqueToPodCliqueSet()),
			builder.WithPredicates(podCliquePredicate()),
		).
		Watches(
			&grovecorev1alpha1.PodCliqueScalingGroup{},
			handler.EnqueueRequestsFromMapFunc(mapPodCliqueScaleGroupToPodCliqueSet()),
			builder.WithPredicates(podCliqueScalingGroupPredicate()),
		).
		Complete(r)
}

// mapPodCliqueToPodCliqueSet returns a function that maps PodClique events to their parent PodCliqueSet.
func mapPodCliqueToPodCliqueSet() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		pclq, ok := obj.(*grovecorev1alpha1.PodClique)
		if !ok {
			return nil
		}
		pcsName := componentutils.GetPodCliqueSetName(pclq.ObjectMeta)
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: pcsName, Namespace: pclq.Namespace}}}
	}
}

// mapPodCliqueScaleGroupToPodCliqueSet returns a function that maps PCSG events to their parent PodCliqueSet.
func mapPodCliqueScaleGroupToPodCliqueSet() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		pcsg, ok := obj.(*grovecorev1alpha1.PodCliqueScalingGroup)
		if !ok {
			return nil
		}
		pcsName := componentutils.GetPodCliqueSetName(pcsg.ObjectMeta)
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: pcsName, Namespace: pcsg.Namespace}}}
	}
}

// podCliquePredicate returns a predicate that filters PodClique events based on ownership and changes.
func podCliquePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
			return grovectrlutils.IsManagedPodClique(deleteEvent.Object, constants.KindPodCliqueSet)
		},
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return grovectrlutils.IsManagedPodClique(updateEvent.ObjectOld, constants.KindPodCliqueSet, constants.KindPodCliqueScalingGroup) &&
				(hasSpecChanged(updateEvent) || hasStatusChanged(updateEvent))
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// podCliqueScalingGroupPredicate returns a predicate that filters PCSG events for relevant status changes.
func podCliqueScalingGroupPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(_ event.DeleteEvent) bool { return false },
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			oldPCSG, okOld := updateEvent.ObjectOld.(*grovecorev1alpha1.PodCliqueScalingGroup)
			newPCSG, okNew := updateEvent.ObjectNew.(*grovecorev1alpha1.PodCliqueScalingGroup)
			if !okOld || !okNew {
				return false
			}
			return hasMinAvailableBreachedConditionChanged(oldPCSG.Status.Conditions, newPCSG.Status.Conditions) || hasUpdateStatusChanged(&oldPCSG.Status, &newPCSG.Status)
		},
		GenericFunc: func(_ event.TypedGenericEvent[client.Object]) bool { return false },
	}
}

// hasSpecChanged checks if the resource generation has changed.
func hasSpecChanged(updateEvent event.UpdateEvent) bool {
	return updateEvent.ObjectOld.GetGeneration() != updateEvent.ObjectNew.GetGeneration()
}

// hasStatusChanged checks if PodClique status fields have changed.
func hasStatusChanged(updateEvent event.UpdateEvent) bool {
	oldPCLQ, okOld := updateEvent.ObjectOld.(*grovecorev1alpha1.PodClique)
	newPCLQ, okNew := updateEvent.ObjectNew.(*grovecorev1alpha1.PodClique)
	if !okOld || !okNew {
		return false
	}
	return hasAnyStatusReplicasChanged(oldPCLQ.Status, newPCLQ.Status) ||
		hasMinAvailableBreachedConditionChanged(oldPCLQ.Status.Conditions, newPCLQ.Status.Conditions)
}

// hasAnyStatusReplicasChanged checks if any replica count fields have changed.
func hasAnyStatusReplicasChanged(oldPCLQStatus, newPCLQStatus grovecorev1alpha1.PodCliqueStatus) bool {
	return oldPCLQStatus.Replicas != newPCLQStatus.Replicas ||
		oldPCLQStatus.ReadyReplicas != newPCLQStatus.ReadyReplicas ||
		oldPCLQStatus.ScheduleGatedReplicas != newPCLQStatus.ScheduleGatedReplicas
}

// hasMinAvailableBreachedConditionChanged checks if the MinAvailableBreached condition has changed.
func hasMinAvailableBreachedConditionChanged(oldConditions, newConditions []metav1.Condition) bool {
	oldMinAvailableBreachedCond := meta.FindStatusCondition(oldConditions, constants.ConditionTypeMinAvailableBreached)
	newMinAvailableBreachedCond := meta.FindStatusCondition(newConditions, constants.ConditionTypeMinAvailableBreached)
	if utils.OnlyOneIsNil(oldMinAvailableBreachedCond, newMinAvailableBreachedCond) {
		return true
	}
	if oldMinAvailableBreachedCond != nil && newMinAvailableBreachedCond != nil {
		return oldMinAvailableBreachedCond.Status != newMinAvailableBreachedCond.Status
	}
	return false
}

// hasUpdateStatusChanged checks if PCSG update progress has changed.
func hasUpdateStatusChanged(oldPCSGStatus, newPCSGStatus *grovecorev1alpha1.PodCliqueScalingGroupStatus) bool {
	return !reflect.DeepEqual(oldPCSGStatus.UpdateProgress, newPCSGStatus.UpdateProgress)
}
