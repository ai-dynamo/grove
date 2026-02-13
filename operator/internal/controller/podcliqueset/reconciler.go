// /*
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
// */

package podcliqueset

import (
	"context"
	"sync"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/hash"
	pcscomponent "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"

	"github.com/go-logr/logr"
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
	reconcileStatusRecorder       ctrlcommon.ReconcileErrorRecorder
	operatorRegistry              component.OperatorRegistry[grovecorev1alpha1.PodCliqueSet]
	pcsGenerationHashExpectations sync.Map
}

// NewReconciler creates a new reconciler for PodCliqueSet.
func NewReconciler(appCtx context.Context, mgr ctrl.Manager, controllerCfg configv1alpha1.PodCliqueSetControllerConfiguration, topologyAwareSchedulingConfig configv1alpha1.TopologyAwareSchedulingConfiguration, networkConfig configv1alpha1.NetworkAcceleration, podTemplateSpecHashCache *hash.PodTemplateSpecHashCache) *Reconciler {
	eventRecorder := mgr.GetEventRecorderFor(controllerName)
	client := mgr.GetClient()
	return &Reconciler{
		config:                        controllerCfg,
		tasConfig:                     topologyAwareSchedulingConfig,
		client:                        client,
		reconcileStatusRecorder:       ctrlcommon.NewReconcileErrorRecorder(client),
		operatorRegistry:              pcscomponent.CreateOperatorRegistry(mgr, eventRecorder, topologyAwareSchedulingConfig, networkConfig, podTemplateSpecHashCache),
		pcsGenerationHashExpectations: sync.Map{},
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

	reconcileSpecFlowResult := r.reconcileSpec(ctx, logger, pcs)
	if statusReconcileResult := r.reconcileStatus(ctx, logger, pcs); ctrlcommon.ShortCircuitReconcileFlow(statusReconcileResult) {
		return statusReconcileResult.Result()
	}

	return reconcileSpecFlowResult.Result()
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
