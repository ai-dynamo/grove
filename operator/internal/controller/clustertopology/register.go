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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
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
			&podCliqueSetEventHandler{},
		).
		Complete(r)
}

// podCliqueSetEventHandler is a custom event handler for PodCliqueSet events that reconciles
// ClusterTopology resources when PodCliqueSets are deleted or their topology label changes.
// This handler ensures that both old and new topologies are reconciled on label changes.
// This is required in order to unblock the deletion of a PodCliqueSet when the topology label is removed.
type podCliqueSetEventHandler struct{}

// Create handles PodCliqueSet creation events.
// We don't reconcile on create since topology isn't blocked when PCS is created.
func (h *podCliqueSetEventHandler) Create(_ context.Context, _ event.TypedCreateEvent[client.Object], _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// No-op: don't reconcile on create
}

// Update handles PodCliqueSet update events.
// Reconciles both old and new topologies if the topology label was changed or removed.
func (h *podCliqueSetEventHandler) Update(_ context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldPCS, oldOk := e.ObjectOld.(*grovecorev1alpha1.PodCliqueSet)
	newPCS, newOk := e.ObjectNew.(*grovecorev1alpha1.PodCliqueSet)
	if !oldOk || !newOk {
		return
	}

	oldTopology := componentutils.GetTopologyName(oldPCS)
	newTopology := componentutils.GetTopologyName(newPCS)

	// Only reconcile if there was a change to the topology label
	if oldTopology == newTopology {
		// No change - don't enqueue anything
		return
	}

	// Collect unique topologies to reconcile
	topologies := make(map[string]struct{})

	// Add old topology if it had one and was removed or changed
	// (it may need unblocking now that this PCS no longer references it)
	if oldTopology != "" {
		topologies[oldTopology] = struct{}{}
	}

	// Add new topology if one was set (and it's different from old, which we already checked)
	if newTopology != "" {
		topologies[newTopology] = struct{}{}
	}

	// Enqueue reconcile requests for all affected topologies
	for topology := range topologies {
		q.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{Name: topology},
		})
	}
}

// Delete handles PodCliqueSet deletion events.
// Reconciles the topology that was referenced by the deleted PCS to potentially unblock its deletion.
func (h *podCliqueSetEventHandler) Delete(_ context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pcs, ok := e.Object.(*grovecorev1alpha1.PodCliqueSet)
	if !ok {
		return
	}

	topology := componentutils.GetTopologyName(pcs)
	if topology == "" {
		return
	}

	q.Add(reconcile.Request{
		NamespacedName: types.NamespacedName{Name: topology},
	})
}

// Generic handles generic events for PodCliqueSet.
// We don't reconcile on generic events.
func (h *podCliqueSetEventHandler) Generic(_ context.Context, _ event.TypedGenericEvent[client.Object], _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// No-op: don't reconcile on generic events
}
