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

package clustertopology

import (
	"context"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	controllerName = "clustertopology-controller"
)

// RegisterWithManager registers the ClusterTopology Reconciler with the manager.
func (r *Reconciler) RegisterWithManager(mgr manager.Manager) error {
	return builder.ControllerManagedBy(mgr).
		Named(controllerName).
		For(&grovecorev1alpha1.ClusterTopology{}).
		Watches(
			&grovecorev1alpha1.PodCliqueSet{},
			handler.EnqueueRequestsFromMapFunc(mapPodCliqueSetToClusterTopology()),
			builder.WithPredicates(podCliqueSetDeletionPredicate()),
		).
		Complete(r)
}

// mapPodCliqueSetToClusterTopology maps PodCliqueSet events to ClusterTopology reconcile requests
// based on the topology label.
func mapPodCliqueSetToClusterTopology() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		pcs, ok := obj.(*grovecorev1alpha1.PodCliqueSet)
		if !ok {
			return nil
		}

		// Get the cluster topology name from the label
		topologyName, exists := pcs.Labels[apicommon.LabelClusterTopologyName]
		if !exists || topologyName == "" {
			return nil
		}

		// ClusterTopology is cluster-scoped, so no namespace
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{Name: topologyName},
		}}
	}
}

// podCliqueSetDeletionPredicate filters PodCliqueSet events to only trigger reconciliation
// when a PCS with a topology label is deleted or its topology label changes.
func podCliqueSetDeletionPredicate() predicate.Predicate {
	hasTopologyLabel := func(obj client.Object) bool {
		if obj == nil {
			return false
		}
		value, exists := obj.GetLabels()[apicommon.LabelClusterTopologyName]
		return exists && value != ""
	}

	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			// Don't reconcile on create - topology isn't blocked when PCS is created
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Reconcile when a PCS with topology label is deleted
			return hasTopologyLabel(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Reconcile if topology label was removed or changed
			// Empty values are treated as non-existent
			oldLabel, oldExists := e.ObjectOld.GetLabels()[apicommon.LabelClusterTopologyName]
			oldHasValue := oldExists && oldLabel != ""
			newLabel, newExists := e.ObjectNew.GetLabels()[apicommon.LabelClusterTopologyName]
			newHasValue := newExists && newLabel != ""

			// Label removed (had value, now doesn't) or changed (both have values but different)
			return (oldHasValue && !newHasValue) || (oldHasValue && newHasValue && oldLabel != newLabel)
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
	}
}
