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

package clustertopology

import (
	"context"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ctrllogger "sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler reconciles ClusterTopology resources.
type Reconciler struct {
	config                  configv1alpha1.ClusterTopologyConfiguration
	client                  ctrlclient.Client
	eventRecorder           record.EventRecorder
	reconcileStatusRecorder ctrlcommon.ReconcileErrorRecorder
}

// NewReconciler creates a new reconciler for ClusterTopology.
func NewReconciler(mgr ctrl.Manager, topologyCfg configv1alpha1.ClusterTopologyConfiguration) *Reconciler {
	return &Reconciler{
		config:                  topologyCfg,
		client:                  mgr.GetClient(),
		eventRecorder:           mgr.GetEventRecorderFor(controllerName),
		reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(mgr.GetClient()),
	}
}

// Reconcile reconciles a ClusterTopology resource.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrllogger.FromContext(ctx).WithName(controllerName)

	ct := &grovecorev1alpha1.ClusterTopology{}
	if err := r.client.Get(ctx, req.NamespacedName, ct); err != nil {
		return ctrl.Result{}, ctrlclient.IgnoreNotFound(err)
	}

	// Handle deletion
	if !ct.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(ct, constants.FinalizerClusterTopology) {
			logger.V(1).Info("ClusterTopology marked for deletion but finalizer already removed", "clusterTopologyName", ct.Name)
			return ctrl.Result{}, nil
		}
		log := logger.WithValues("operation", "delete")
		return r.triggerDeletionFlow(ctx, log, ct).Result()
	}

	// Handle normal reconciliation
	return r.reconcileSpec(ctx, logger, ct).Result()
}
