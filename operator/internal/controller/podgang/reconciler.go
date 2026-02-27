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
	"context"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/common"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler reconciles PodGang objects and converts them to scheduler-specific CRs
type Reconciler struct {
	client.Client
	scheme *runtime.Scheme
}

// NewReconciler creates a new Reconciler. Backend is resolved per PodGang from the grove.io/scheduler-name label or default.
func NewReconciler(mgr ctrl.Manager) *Reconciler {
	return &Reconciler{
		Client: mgr.GetClient(),
		scheme: mgr.GetScheme(),
	}
}

func resolveBackend(podGang *groveschedulerv1alpha1.PodGang) common.SchedBackend {
	if name := podGang.Labels[apicommon.LabelSchedulerName]; name != "" {
		if b := schedulerbackend.Get(name); b != nil {
			return b
		}
	}
	return schedulerbackend.GetDefault()
}

// Reconcile processes PodGang changes and synchronizes to backend-specific CRs
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	podGang := &groveschedulerv1alpha1.PodGang{}
	if err := r.Get(ctx, req.NamespacedName, podGang); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	backend := resolveBackend(podGang)
	if backend == nil {
		log.FromContext(ctx).Error(nil, "No scheduler backend available for PodGang", "podgang", req.NamespacedName)
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx).WithValues("scheduler", backend.Name(), "podGang", req.NamespacedName)
	if !podGang.DeletionTimestamp.IsZero() {
		logger.Info("PodGang is being deleted")
		if err := backend.OnPodGangDelete(ctx, podGang); err != nil {
			logger.Error(err, "Failed to delete scheduler backend resources on-delete of PodGang")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if err := backend.SyncPodGang(ctx, podGang); err != nil {
		logger.Error(err, "Failed to SyncPodGang on spec change")
		return ctrl.Result{}, err
	}
	logger.Info("Successfully synced PodGang")
	return ctrl.Result{}, nil
}
