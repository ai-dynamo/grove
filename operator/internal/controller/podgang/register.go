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

package podgang

import (
	grovectrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// RegisterWithManager registers the backend controller with the manager
func (r *Reconciler) RegisterWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&groveschedulerv1alpha1.PodGang{}).
		WithEventFilter(podGangSpecChangePredicate()).
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
