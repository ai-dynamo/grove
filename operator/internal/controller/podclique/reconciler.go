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

package podclique

import (
	"context"
	"time"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	pclqcomponent "github.com/ai-dynamo/grove/operator/internal/controller/podclique/components"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/ai-dynamo/grove/operator/internal/expect"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ctrllogger "sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler reconciles PodClique objects.
type Reconciler struct {
	config                  configv1alpha1.PodCliqueControllerConfiguration
	client                  ctrlclient.Client
	eventRecorder           record.EventRecorder
	reconcileStatusRecorder ctrlcommon.ReconcileErrorRecorder
	expectationsStore       *expect.ExpectationsStore
	operatorRegistry        component.OperatorRegistry[grovecorev1alpha1.PodClique]
}

// NewReconciler creates a new instance of the PodClique Reconciler.
func NewReconciler(mgr ctrl.Manager, controllerCfg configv1alpha1.PodCliqueControllerConfiguration, schedRegistry scheduler.Registry) *Reconciler {
	eventRecorder := mgr.GetEventRecorderFor(controllerName)
	expectationsStore := expect.NewExpectationsStore()
	return &Reconciler{
		config:                  controllerCfg,
		client:                  mgr.GetClient(),
		eventRecorder:           eventRecorder,
		reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(mgr.GetClient()),
		expectationsStore:       expectationsStore,
		operatorRegistry:        pclqcomponent.CreateOperatorRegistry(mgr, eventRecorder, expectationsStore, schedRegistry),
	}
}

// Reconcile reconciles the `PodClique` resource.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrllogger.FromContext(ctx).WithName(controllerName)

	// Memoize lookups that happen multiple times within a single reconcile:
	//   * GetPCLQPods — reconcileSpec + reconcileStatus each list pods
	//   * GetPodCliqueSet — called 4× (spec, status, pod sync, resourceclaim)
	ctx = componentutils.WithPCLQPodsCache(ctx)
	ctx = componentutils.WithPodCliqueSetCache(ctx)

	pclq := &grovecorev1alpha1.PodClique{}
	if result := ctrlutils.GetPodClique(ctx, r.client, logger, req.NamespacedName, pclq, true); ctrlcommon.ShortCircuitReconcileFlow(result) {
		return result.Result()
	}

	if !pclq.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(pclq, apicommonconstants.FinalizerPodClique) {
			return ctrlcommon.DoNotRequeue().Result()
		}
		return r.triggerDeletionFlow(ctx, logger, pclq).Result()
	}

	reconcileSpecFlowResult := r.reconcileSpec(ctx, logger, pclq)
	statusReconcileResult := r.reconcileStatus(ctx, logger, pclq)
	reconcileResult := ctrlcommon.MergeStepResults(reconcileSpecFlowResult, statusReconcileResult)

	result, err := reconcileResult.Result()
	// The spec and status reconciles both succeeded without asking for a requeue. Schedule a periodic
	// status resync so a stale status is corrected even when no watch event arrives. A lost update can
	// leave the status wrong with nothing left to trigger a recompute. This is applied here, not in
	// reconcileStatus, so it cannot mask a shorter requeue from the spec flow that drives
	// rolling-update progression.
	// See https://github.com/ai-dynamo/grove/issues/775.
	if err == nil && result.RequeueAfter == 0 {
		result.RequeueAfter = r.getStatusResyncInterval(ctx, pclq)
	}
	return result, err
}

// getStatusResyncInterval returns the requeue interval for a successful reconcile. It is the periodic
// status resync bounded by the owner PodCliqueSet's TerminationDelay. Gang termination is armed off
// the MinAvailableBreached condition this reconciler writes. A stale condition must be recomputed
// before the delay expires, else the breach goes undetected past its termination deadline. It falls
// back to the plain resync interval when the owner PodCliqueSet cannot be read or sets no
// TerminationDelay.
func (r *Reconciler) getStatusResyncInterval(ctx context.Context, pclq *grovecorev1alpha1.PodClique) time.Duration {
	defaultResyncInterval := constants.PodCliqueStatusResyncInterval
	pcs, err := componentutils.GetPodCliqueSet(ctx, r.client, pclq.ObjectMeta)
	if err != nil || pcs.Spec.Template.TerminationDelay == nil {
		return defaultResyncInterval
	}
	return min(pcs.Spec.Template.TerminationDelay.Duration, defaultResyncInterval)
}
