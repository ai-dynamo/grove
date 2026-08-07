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

package podgang

import (
	"context"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovectrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// RegisterWithManager registers the backend controller with the manager
func (r *Reconciler) RegisterWithManager(mgr ctrl.Manager) error {
	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		For(&groveschedulerv1alpha1.PodGang{}, builder.WithPredicates(podGangChangePredicate())).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(mapPodToPodGang),
			builder.WithPredicates(podStatusChangePredicate()),
		).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: *r.config.ConcurrentSyncs,
		}).
		Named("podgang")
	for _, backend := range r.schedRegistry.All() {
		if eventSource, ok := backend.(scheduler.PodGangStatusEventSource); ok {
			controllerBuilder = eventSource.AddPodGangStatusWatches(controllerBuilder)
		}
	}
	return controllerBuilder.Complete(r)
}

func podGangChangePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return grovectrlutils.IsManagedPodGang(e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return grovectrlutils.IsManagedPodGang(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return grovectrlutils.IsManagedPodGang(e.ObjectOld) &&
				grovectrlutils.IsManagedPodGang(e.ObjectNew) &&
				(e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() ||
					hasInitializedConditionChanged(e))
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

func hasInitializedConditionChanged(updateEvent event.UpdateEvent) bool {
	oldPodGang, oldOK := updateEvent.ObjectOld.(*groveschedulerv1alpha1.PodGang)
	newPodGang, newOK := updateEvent.ObjectNew.(*groveschedulerv1alpha1.PodGang)
	if !oldOK || !newOK {
		return false
	}
	conditionType := string(groveschedulerv1alpha1.PodGangConditionTypeInitialized)
	return meta.IsStatusConditionTrue(oldPodGang.Status.Conditions, conditionType) !=
		meta.IsStatusConditionTrue(newPodGang.Status.Conditions, conditionType)
}

func podStatusChangePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return true },
		DeleteFunc: func(_ event.DeleteEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, oldOK := e.ObjectOld.(*corev1.Pod)
			newPod, newOK := e.ObjectNew.(*corev1.Pod)
			if !oldOK || !newOK {
				return false
			}
			return oldPod.Labels[apicommon.LabelPodGang] != newPod.Labels[apicommon.LabelPodGang] ||
				oldPod.DeletionTimestamp.IsZero() != newPod.DeletionTimestamp.IsZero() ||
				isPodConditionTrue(oldPod, corev1.PodScheduled) != isPodConditionTrue(newPod, corev1.PodScheduled) ||
				isPodConditionTrue(oldPod, corev1.PodReady) != isPodConditionTrue(newPod, corev1.PodReady)
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

func isPodConditionTrue(pod *corev1.Pod, conditionType corev1.PodConditionType) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func mapPodToPodGang(_ context.Context, obj client.Object) []reconcile.Request {
	podGangName := obj.GetLabels()[apicommon.LabelPodGang]
	if podGangName == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: obj.GetNamespace(),
			Name:      podGangName,
		},
	}}
}
