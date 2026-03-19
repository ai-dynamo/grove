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

package podclique

import (
	"context"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/hash"
	pclqcomponent "github.com/ai-dynamo/grove/operator/internal/controller/podclique/components"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ctrllogger "sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler reconciles PodClique objects.
type Reconciler struct {
	config                   configv1alpha1.PodCliqueControllerConfiguration
	client                   ctrlclient.Client
	eventRecorder            record.EventRecorder
	reconcileStatusRecorder  ctrlcommon.ReconcileErrorRecorder
	expectationsStore        *expect.ExpectationsStore
	operatorRegistry         component.OperatorRegistry[grovecorev1alpha1.PodClique]
	podTemplateSpecHashCache *hash.PodTemplateSpecHashCache
}

// NewReconciler creates a new instance of the PodClique Reconciler.
func NewReconciler(mgr ctrl.Manager, controllerCfg configv1alpha1.PodCliqueControllerConfiguration, podTemplateSpecHashCache *hash.PodTemplateSpecHashCache) *Reconciler {
	eventRecorder := mgr.GetEventRecorderFor(controllerName)
	expectationsStore := expect.NewExpectationsStore()
	return &Reconciler{
		config:                   controllerCfg,
		client:                   mgr.GetClient(),
		eventRecorder:            eventRecorder,
		reconcileStatusRecorder:  ctrlcommon.NewReconcileErrorRecorder(mgr.GetClient()),
		expectationsStore:        expectationsStore,
		operatorRegistry:         pclqcomponent.CreateOperatorRegistry(mgr, eventRecorder, expectationsStore, podTemplateSpecHashCache),
		podTemplateSpecHashCache: podTemplateSpecHashCache,
	}
}

// Reconcile reconciles the `PodClique` resource.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrllogger.FromContext(ctx).WithName(controllerName)

	pclq := &grovecorev1alpha1.PodClique{}
	if result := ctrlutils.GetPodClique(ctx, r.client, logger, req.NamespacedName, pclq, true); ctrlcommon.ShortCircuitReconcileFlow(result) {
		return result.Result()
	}

	if !pclq.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(pclq, constants.FinalizerPodClique) {
			return ctrlcommon.DoNotRequeue().Result()
		}
		return r.triggerDeletionFlow(ctx, logger, pclq).Result()
	}

	reconcileSpecFlowResult := r.reconcileSpec(ctx, logger, pclq)
	if statusReconcileResult := r.reconcileStatus(ctx, logger, pclq); ctrlcommon.ShortCircuitReconcileFlow(statusReconcileResult) {
		return statusReconcileResult.Result()
	}

	return reconcileSpecFlowResult.Result()
}
