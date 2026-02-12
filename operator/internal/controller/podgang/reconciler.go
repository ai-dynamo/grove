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
	"fmt"

	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler reconciles PodGang objects and converts them to scheduler-specific CRs
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Backend schedulerbackend.SchedulerBackend
	Logger  logr.Logger
}

// NewReconciler creates a new Reconciler
func NewReconciler(mgr ctrl.Manager) (*Reconciler, error) {
	b := schedulerbackend.Get()
	if b == nil {
		return nil, fmt.Errorf("backend not initialized")
	}
	return &Reconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Backend: b,
	}, nil
}

// Reconcile processes PodGang changes and synchronizes to backend-specific CRs
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("backend", r.Backend.Name(), "podgang", req.NamespacedName)

	// 1. Fetch the PodGang
	podGang := &groveschedulerv1alpha1.PodGang{}
	if err := r.Get(ctx, req.NamespacedName, podGang); err != nil {
		if client.IgnoreNotFound(err) != nil {
			logger.Error(err, "Failed to get PodGang")
			return ctrl.Result{}, err
		}
		// PodGang was deleted, nothing to do (ownerReference handles cleanup)
		logger.Info("PodGang not found, likely deleted")
		return ctrl.Result{}, nil
	}

	logger.Info("Processing PodGang with backend")

	// 2. Handle deletion
	if !podGang.DeletionTimestamp.IsZero() {
		logger.Info("PodGang is being deleted")
		if err := r.Backend.OnPodGangDelete(ctx, podGang); err != nil {
			logger.Error(err, "Failed to delete backend resources")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 3. Sync PodGang to backend-specific CR
	if err := r.Backend.SyncPodGang(ctx, podGang); err != nil {
		logger.Error(err, "Failed to sync PodGang to backend")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully synced PodGang to backend")
	return ctrl.Result{}, nil
}
