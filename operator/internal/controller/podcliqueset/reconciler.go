// Copyright 2024 The Grove Authors.
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

package podcliqueset

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcscomponent "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	"github.com/go-logr/logr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ctrllogger "sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler reconciles PodCliqueSet resources.
type Reconciler struct {
	config                        configv1alpha1.PodCliqueSetControllerConfiguration
	tasConfig                     configv1alpha1.TopologyAwareSchedulingConfiguration
	client                        ctrlclient.Client
	eventRecorder                 record.EventRecorder
	reconcileStatusRecorder       ctrlcommon.ReconcileErrorRecorder
	operatorRegistry              component.OperatorRegistry[grovecorev1alpha1.PodCliqueSet, pcssync.State, pcssync.Result]
	pcsGenerationHashExpectations sync.Map
	scalingProgressExpectations   *pcssync.ScalingProgressExpectations
}

// NewReconciler creates a new reconciler for PodCliqueSet.
func NewReconciler(mgr ctrl.Manager, controllerCfg configv1alpha1.PodCliqueSetControllerConfiguration, topologyAwareSchedulingConfig configv1alpha1.TopologyAwareSchedulingConfiguration, networkConfig configv1alpha1.NetworkAcceleration, schedRegistry scheduler.Registry) *Reconciler {
	eventRecorder := mgr.GetEventRecorderFor(controllerName)
	client := mgr.GetClient()
	scalingProgressExpectations := &pcssync.ScalingProgressExpectations{}
	return &Reconciler{
		config:                        controllerCfg,
		tasConfig:                     topologyAwareSchedulingConfig,
		client:                        client,
		eventRecorder:                 eventRecorder,
		reconcileStatusRecorder:       ctrlcommon.NewReconcileErrorRecorder(client),
		operatorRegistry:              pcscomponent.CreateOperatorRegistry(mgr, eventRecorder, topologyAwareSchedulingConfig, networkConfig, schedRegistry, scalingProgressExpectations),
		pcsGenerationHashExpectations: sync.Map{},
		scalingProgressExpectations:   scalingProgressExpectations,
	}
}

// Reconcile reconciles a PodCliqueSet resource.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrllogger.FromContext(ctx).WithName(controllerName)

	pcs := &grovecorev1alpha1.PodCliqueSet{}
	if result := ctrlutils.GetPodCliqueSet(ctx, r.client, logger, req.NamespacedName, pcs); ctrlcommon.ShortCircuitReconcileFlow(result) {
		return result.Result()
	}

	if result := r.reconcileDelete(ctx, logger, pcs); ctrlcommon.ShortCircuitReconcileFlow(result) {
		return result.Result()
	}
	if result := r.ensureScalingProgressExpectationSatisfied(pcs); ctrlcommon.ShortCircuitReconcileFlow(result) {
		return result.Result()
	}

	state := &pcssync.State{}
	reconcileSpecFlowResult := r.reconcileSpec(ctx, logger, pcs, state)
	if len(state.ExpectedReplicaIndices) != int(pcs.Spec.Replicas) {
		return reconcileSpecFlowResult.Result()
	}
	if statusReconcileResult := r.reconcileStatus(ctx, logger, pcs, state); ctrlcommon.ShortCircuitReconcileFlow(statusReconcileResult) {
		return statusReconcileResult.Result()
	}

	return reconcileSpecFlowResult.Result()
}

const scalingProgressCacheSyncRetryInterval = 500 * time.Millisecond

// ensureScalingProgressExpectationSatisfied waits until the informer cache contains the last persisted scaling progress.
func (r *Reconciler) ensureScalingProgressExpectationSatisfied(pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	if r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, pcs.Status.ScalingProgress) {
		return ctrlcommon.ContinueReconcile()
	}
	return ctrlcommon.ReconcileAfter(
		scalingProgressCacheSyncRetryInterval,
		fmt.Sprintf("PodCliqueSet scaling progress is not yet visible in the informer cache: %v", ctrlclient.ObjectKeyFromObject(pcs)),
	)
}

// reconcileDelete handles PodCliqueSet deletion when a deletion timestamp is set.
func (r *Reconciler) reconcileDelete(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	if !pcs.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(pcs, constants.FinalizerPodCliqueSet) {
			return ctrlcommon.DoNotRequeue()
		}
		dLog := logger.WithValues("operation", "delete")
		return r.triggerDeletionFlow(ctx, dLog, pcs)
	}
	return ctrlcommon.ContinueReconcile()
}
