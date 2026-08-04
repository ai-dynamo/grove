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

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
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
	config configv1alpha1.PodCliqueControllerConfiguration
	client ctrlclient.Client
	// apiReader reads straight from the apiserver, bypassing the informer cache. It is used only to
	// fetch the PodClique being reconciled. See the Reconcile godoc for why that read cannot be
	// served from the cache.
	apiReader               ctrlclient.Reader
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
		apiReader:               mgr.GetAPIReader(),
		eventRecorder:           eventRecorder,
		reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(mgr.GetClient()),
		expectationsStore:       expectationsStore,
		operatorRegistry:        pclqcomponent.CreateOperatorRegistry(mgr, eventRecorder, expectationsStore, schedRegistry),
	}
}

// Reconcile reconciles the `PodClique` resource.
//
// The PodClique itself is read through apiReader rather than the informer cache. reconcileStatus
// skips its write when the recomputed status equals the status this object was loaded with, treating
// that as "already persisted". A cached read makes that claim unsound: the cache can still be serving
// a copy from before a write this controller already made, so the recomputed status can match a stale
// baseline and the skip drops a write the apiserver still needs. Nothing recovers from that - a status
// write does not change the generation, so it is filtered by this controller's own
// GenerationChangedPredicate and never re-enqueues, and once the Pods go quiet no other event arrives
// either. Reading the object live makes the baseline authoritative by construction, which is what the
// skip assumes.
//
// Child Pod reads and the parent PodCliqueSet lookup stay cache-backed; they are level-triggered by
// their own watches and a stale read of them is corrected by the next event.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrllogger.FromContext(ctx).WithName(controllerName)

	// Memoize lookups that happen multiple times within a single reconcile:
	//   * GetPCLQPods — reconcileSpec + reconcileStatus each list pods
	//   * GetPodCliqueSet — called 4× (spec, status, pod sync, resourceclaim)
	ctx = componentutils.WithPCLQPodsCache(ctx)
	ctx = componentutils.WithPodCliqueSetCache(ctx)

	pclq := &grovecorev1alpha1.PodClique{}
	if result := ctrlutils.GetPodClique(ctx, r.apiReader, logger, req.NamespacedName, pclq, true); ctrlcommon.ShortCircuitReconcileFlow(result) {
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
