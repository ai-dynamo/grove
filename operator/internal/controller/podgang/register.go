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
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovectrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// RegisterWithManager registers the backend controller with the manager
func (r *Reconciler) RegisterWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&groveschedulerv1alpha1.PodGang{}, builder.WithPredicates(podGangSpecChangePredicate())).
		// A backend may hold off on a PodGang until the PodGangs it depends on are scheduled, so resync it then.
		Watches(&groveschedulerv1alpha1.PodGang{},
			handler.EnqueueRequestsFromMapFunc(mapPodGangToDependentPodGangs(r.Client)),
			builder.WithPredicates(podGangFirstScheduledPredicate())).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: *r.config.ConcurrentSyncs,
		}).
		Named("podgang").
		Complete(r)
}

// podGangSpecChangePredicate filters PodGang events to only process spec changes
// Status-only updates (like Initialized condition) are ignored
func podGangSpecChangePredicate() predicate.Predicate {
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
				(e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration())
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// podGangFirstScheduledPredicate passes the update that first sets a managed PodGang's Status.LastScheduled.
func podGangFirstScheduledPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(_ event.DeleteEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPG, okOld := e.ObjectOld.(*groveschedulerv1alpha1.PodGang)
			newPG, okNew := e.ObjectNew.(*groveschedulerv1alpha1.PodGang)
			return okOld && okNew && grovectrlutils.IsManagedPodGang(newPG) &&
				oldPG.Status.LastScheduled == nil && newPG.Status.LastScheduled != nil
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// mapPodGangToDependentPodGangs maps a PodGang to the PodGangs of every PodGangMap entry that DependsOn its epoch.
func mapPodGangToDependentPodGangs(cl client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		podGang, ok := obj.(*groveschedulerv1alpha1.PodGang)
		if !ok {
			return nil
		}
		epoch := podGang.Labels[apicommon.LabelEpoch]
		pcsName := podGang.Labels[apicommon.LabelPartOfKey]
		pcsReplicaIndex, err := strconv.Atoi(podGang.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
		if epoch == "" || pcsName == "" || err != nil {
			return nil
		}
		pgm, err := componentutils.GetPodGangMap(ctx, cl, client.ObjectKey{Namespace: podGang.Namespace, Name: pcsName}, pcsReplicaIndex)
		if err != nil {
			log.FromContext(ctx).Error(err, "Failed to get the PodGangMap to resync the PodGangs that depend on a PodGang", "podGang", client.ObjectKeyFromObject(podGang))
			return nil
		}
		rnr := apicommon.ResourceNameReplica{Name: pcsName, Replica: pcsReplicaIndex}
		var requests []reconcile.Request
		for _, entry := range pgm.Spec.Entries {
			if !slices.Contains(entry.DependsOn, epoch) {
				continue
			}
			for _, name := range componentutils.ExpectedPodGangNamesForEntry(rnr, entry) {
				requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: podGang.Namespace, Name: name}})
			}
		}
		return requests
	}
}
