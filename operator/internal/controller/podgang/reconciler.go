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
	"errors"
	"fmt"
	"slices"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const errCodeSyncSchedulerBackend grovecorev1alpha1.ErrorCode = "ERR_SYNC_SCHEDULER_BACKEND"

// Reconciler reconciles PodGang objects and converts them to scheduler-specific CRs
type Reconciler struct {
	client.Client
	scheme        *runtime.Scheme
	config        configv1alpha1.PodGangControllerConfiguration
	schedRegistry scheduler.Registry
}

// NewReconciler creates a new Reconciler. Backend is resolved per PodGang from the grove.io/scheduler-name label or default.
func NewReconciler(mgr ctrl.Manager, config configv1alpha1.PodGangControllerConfiguration, schedRegistry scheduler.Registry) *Reconciler {
	return &Reconciler{
		Client:        mgr.GetClient(),
		scheme:        mgr.GetScheme(),
		config:        config,
		schedRegistry: schedRegistry,
	}
}

func (r *Reconciler) resolveSchedulerBackend(podGang *groveschedulerv1alpha1.PodGang) scheduler.Backend {
	if name := podGang.Labels[apicommon.LabelSchedulerName]; name != "" {
		if b := r.schedRegistry.Get(name); b != nil {
			return b
		}
	}
	return r.schedRegistry.GetDefault()
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

	backend := r.resolveSchedulerBackend(podGang)
	// This should ideally not happen. If you see this log then there is either something wrong with the defaulting or validation.
	if backend == nil {
		log.FromContext(ctx).Error(nil, "No scheduler backend available for PodGang", "podgang", req.NamespacedName)
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx).WithValues("scheduler", backend.Name(), "podGang", req.NamespacedName)
	if !podGang.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if err := backend.SyncPodGang(ctx, podGang); err != nil {
		logger.Error(err, "Failed to SyncPodGang on spec change")
		if statusErr := r.setBackendLastError(ctx, podGang, err); statusErr != nil {
			return ctrl.Result{}, errors.Join(err, statusErr)
		}
		return ctrl.Result{}, err
	}
	if err := r.setBackendLastError(ctx, podGang, nil); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("Successfully synced PodGang")
	return ctrl.Result{}, nil
}

// setBackendLastError records only scheduler-backend errors on the owning PodCliqueSet,
// preserving errors written by the PodCliqueSet controller and other components.
func (r *Reconciler) setBackendLastError(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang, syncErr error) error {
	owner := metav1.GetControllerOf(podGang)
	if owner == nil || owner.APIVersion != grovecorev1alpha1.SchemeGroupVersion.String() || owner.Kind != "PodCliqueSet" {
		return fmt.Errorf("podgang %s/%s has no controlling PodCliqueSet", podGang.Namespace, podGang.Name)
	}

	pcs := &grovecorev1alpha1.PodCliqueSet{}
	key := client.ObjectKey{Namespace: podGang.Namespace, Name: owner.Name}
	if err := r.Get(ctx, key, pcs); err != nil {
		if apierrors.IsNotFound(err) && syncErr == nil {
			return nil
		}
		return fmt.Errorf("get owning PodCliqueSet %s: %w", key, err)
	}

	descriptionPrefix := fmt.Sprintf("podgang %s/%s: ", podGang.Namespace, podGang.Name)
	desired := slices.DeleteFunc(slices.Clone(pcs.Status.LastErrors), func(lastErr grovecorev1alpha1.LastError) bool {
		return lastErr.Code == errCodeSyncSchedulerBackend && strings.HasPrefix(lastErr.Description, descriptionPrefix)
	})
	if syncErr != nil {
		desired = append(desired, grovecorev1alpha1.LastError{
			Code:        errCodeSyncSchedulerBackend,
			Description: descriptionPrefix + syncErr.Error(),
			ObservedAt:  metav1.Now(),
		})
	}
	if slices.EqualFunc(pcs.Status.LastErrors, desired, func(left, right grovecorev1alpha1.LastError) bool {
		return left.Code == right.Code && left.Description == right.Description && left.ObservedAt.Equal(&right.ObservedAt)
	}) {
		return nil
	}

	before := pcs.DeepCopy()
	pcs.Status.LastErrors = desired
	if err := r.Status().Patch(ctx, pcs, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("update scheduler backend error on PodCliqueSet %s: %w", key, err)
	}
	return nil
}
