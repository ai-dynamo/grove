// /*
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
// */

package clustertopology

import (
	"context"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	schedmanager "github.com/ai-dynamo/grove/operator/internal/scheduler/manager"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// NewReconciler creates a new ClusterTopology reconciler.
func NewReconciler(mgr ctrl.Manager) *Reconciler {
	return &Reconciler{
		Client:   mgr.GetClient(),
		backends: schedmanager.All(),
		recorder: mgr.GetEventRecorderFor("clustertopology-controller"),
	}
}

// RegisterWithManager registers the ClusterTopology controller with the manager.
func (r *Reconciler) RegisterWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("clustertopology").
		For(&grovecorev1alpha1.ClusterTopology{}, builder.WithPredicates(predicate.GenerationChangedPredicate{}))

	// Register a dynamic watch on each TAS backend's topology CRD.
	// When a backend topology resource is created/updated/deleted, map the event
	// back to its owning ClusterTopology and enqueue a reconciliation.
	for _, backend := range r.backends {
		tasBackend, ok := backend.(scheduler.TopologyAwareSchedBackend)
		if !ok {
			continue
		}
		gvr := tasBackend.TopologyGVR()
		gvk, err := mgr.GetRESTMapper().KindFor(gvr)
		if err != nil {
			// CRD not installed — skip watch; the controller still reconciles on CT spec changes.
			log.Log.Info("Skipping watch for backend topology CRD (not installed)", "gvr", gvr, "backend", backend.Name())
			continue
		}
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b = b.Watches(u, handler.EnqueueRequestsFromMapFunc(r.mapBackendTopologyToCT(backend.Name())))
	}

	return b.Complete(r)
}

// mapBackendTopologyToCT returns a MapFunc that maps a backend topology resource event
// to the ClusterTopology that should be reconciled.
//
// For auto-managed topologies: the backend topology has an OwnerReference to the CT.
// For externally-managed topologies: the CT's schedulerTopologyReferences names the backend topology.
func (r *Reconciler) mapBackendTopologyToCT(backendName string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []ctrl.Request {
		for _, ref := range obj.GetOwnerReferences() {
			if ref.Kind == apicommonconstants.KindClusterTopology && ref.APIVersion == grovecorev1alpha1.SchemeGroupVersion.String() {
				return []ctrl.Request{{
					NamespacedName: client.ObjectKey{Name: ref.Name},
				}}
			}
		}

		ctList := &grovecorev1alpha1.ClusterTopologyList{}
		if err := r.List(ctx, ctList); err != nil {
			log.FromContext(ctx).Error(err, "Failed to list ClusterTopologies for backend topology watch mapping")
			return nil
		}
		var requests []ctrl.Request
		for _, ct := range ctList.Items {
			for _, ref := range ct.Spec.SchedulerTopologyReferences {
				if ref.SchedulerName == backendName && ref.TopologyReference == obj.GetName() {
					requests = append(requests, ctrl.Request{
						NamespacedName: client.ObjectKey{Name: ct.Name},
					})
				}
			}
		}
		return requests
	}
}
