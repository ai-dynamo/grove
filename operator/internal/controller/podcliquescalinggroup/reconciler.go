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

package podcliquescalinggroup

import (
	"context"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	groveconfigv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	pcsgcomponent "github.com/ai-dynamo/grove/operator/internal/controller/podcliquescalinggroup/components"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ctrllogger "sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler reconciles PodCliqueScalingGroup objects.
type Reconciler struct {
	config groveconfigv1alpha1.PodCliqueScalingGroupControllerConfiguration
	client ctrlclient.Client
	// apiReader reads straight from the apiserver, bypassing the informer cache. It is used only to
	// fetch the PodCliqueScalingGroup being reconciled. See the Reconcile godoc for why that read
	// cannot be served from the cache.
	apiReader               ctrlclient.Reader
	eventRecorder           record.EventRecorder
	reconcileStatusRecorder ctrlcommon.ReconcileErrorRecorder
	operatorRegistry        component.OperatorRegistry[grovecorev1alpha1.PodCliqueScalingGroup]
}

// NewReconciler creates a new instance of the PodClique Reconciler.
func NewReconciler(mgr ctrl.Manager, controllerCfg groveconfigv1alpha1.PodCliqueScalingGroupControllerConfiguration) *Reconciler {
	eventRecorder := mgr.GetEventRecorderFor(controllerName)
	client := mgr.GetClient()
	return &Reconciler{
		config:                  controllerCfg,
		client:                  client,
		apiReader:               mgr.GetAPIReader(),
		eventRecorder:           eventRecorder,
		reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(client),
		operatorRegistry:        pcsgcomponent.CreateOperatorRegistry(mgr, eventRecorder),
	}
}

// Reconcile reconciles a PodCliqueScalingGroup resource.
//
// The PodCliqueScalingGroup itself is read through apiReader rather than the informer cache.
// reconcileStatus skips its write when the recomputed status equals the status this object was loaded
// with, treating that as "already persisted". A cached read makes that claim unsound: the cache can
// still be serving a copy from before a write this controller already made, so the recomputed status
// can match a stale baseline and the skip drops a write the apiserver still needs. Nothing recovers
// from that - a status write does not change the generation, so it is filtered by this controller's
// own GenerationChangedPredicate and never re-enqueues, and once the PodCliques go quiet no other
// event arrives either. Reading the object live makes the baseline authoritative by construction,
// which is what the skip assumes.
//
// Child PodClique reads and the parent PodCliqueSet lookup stay cache-backed; they are level-triggered
// by their own watches and a stale read of them is corrected by the next event.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrllogger.FromContext(ctx).WithName(controllerName)

	// GetPodCliqueSet is called 3× per reconcile (spec, status, podclique sync) — memoize.
	ctx = componentutils.WithPodCliqueSetCache(ctx)

	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{}
	if result := ctrlutils.GetPodCliqueScalingGroup(ctx, r.apiReader, logger, req.NamespacedName, pcsg); ctrlcommon.ShortCircuitReconcileFlow(result) {
		return result.Result()
	}

	// Check if the deletion timestamp has not been set, do not handle if it is
	var deletionOrSpecReconcileFlowResult ctrlcommon.ReconcileStepResult
	if !pcsg.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(pcsg, constants.FinalizerPodCliqueScalingGroup) {
			return ctrlcommon.DoNotRequeue().Result()
		}
		dLog := logger.WithValues("operation", "delete")
		deletionOrSpecReconcileFlowResult = r.triggerDeletionFlow(ctx, dLog, pcsg)
	} else {
		specLog := logger.WithValues("operation", "specReconcile")
		deletionOrSpecReconcileFlowResult = r.reconcileSpec(ctx, specLog, pcsg)
	}

	if statusReconcileResult := r.reconcileStatus(ctx, logger, ctrlclient.ObjectKeyFromObject(pcsg)); ctrlcommon.ShortCircuitReconcileFlow(statusReconcileResult) {
		return statusReconcileResult.Result()
	}

	if ctrlcommon.ShortCircuitReconcileFlow(deletionOrSpecReconcileFlowResult) {
		return deletionOrSpecReconcileFlowResult.Result()
	}

	return ctrlcommon.DoNotRequeue().Result()
}
