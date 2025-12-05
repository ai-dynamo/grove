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

package controller

import (
	"context"
	"fmt"

	"github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// BackendReconciler reconciles PodGang objects and converts them to scheduler-specific CRs
type BackendReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Backend backend.SchedulerBackend
	Logger  logr.Logger
}

// Reconcile processes PodGang changes and synchronizes to backend-specific CRs
func (r *BackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	// 2. Check if this backend should handle this PodGang
	if !r.Backend.Matches(podGang) {
		logger.V(1).Info("PodGang does not match this backend, skipping")
		return ctrl.Result{}, nil
	}

	logger.Info("Processing PodGang with backend")

	// 3. Handle deletion
	if !podGang.DeletionTimestamp.IsZero() {
		logger.Info("PodGang is being deleted")
		if err := r.Backend.Delete(ctx, logger, podGang); err != nil {
			logger.Error(err, "Failed to delete backend resources")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 4. Sync PodGang to backend-specific CR
	if err := r.Backend.Sync(ctx, logger, podGang); err != nil {
		logger.Error(err, "Failed to sync PodGang to backend")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully synced PodGang to backend")

	// 5. Update PodGang status based on backend readiness
	if err := r.updatePodGangStatus(ctx, logger, podGang); err != nil {
		logger.Error(err, "Failed to update PodGang status")
		// Don't fail the reconciliation for status update errors
	}

	return ctrl.Result{}, nil
}

// updatePodGangStatus updates PodGang status based on backend CR readiness
func (r *BackendReconciler) updatePodGangStatus(ctx context.Context, logger logr.Logger, podGang *groveschedulerv1alpha1.PodGang) error {
	isReady, resourceName, err := r.Backend.CheckReady(ctx, logger, podGang)
	if err != nil {
		return fmt.Errorf("failed to check backend readiness: %w", err)
	}

	// Update status if changed
	if isReady {
		logger.Info("Backend resource is ready", "resource", resourceName)
		// TODO: Update PodGang.Status.Phase to Running
		// TODO: Add condition for backend resource readiness
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager
func (r *BackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&groveschedulerv1alpha1.PodGang{}).
		Named(fmt.Sprintf("backend-%s", r.Backend.Name())).
		Complete(r)
}
