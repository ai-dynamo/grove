// /*
// Copyright 2026 The Grove Authors.
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
	"errors"
	"fmt"
	"sort"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctutils "github.com/ai-dynamo/grove/operator/internal/clustertopology"
	internalconstants "github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler reconciles ClusterTopology resources by synchronizing
// scheduler backend topology resources and reporting drift status.
type Reconciler struct {
	client.Client
	backends map[string]scheduler.Backend
	recorder record.EventRecorder
}

// Reconcile reconciles a ClusterTopology resource.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("clusterTopology", req.Name)

	ct := &grovecorev1alpha1.ClusterTopology{}
	if err := r.Get(ctx, req.NamespacedName, ct); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var statuses []grovecorev1alpha1.SchedulerTopologyStatus
	var reconcileErr error
	var topologyNotFound bool

	schedulerRefMap := ctutils.BuildSchedulerReferenceMap(ct.Spec.SchedulerTopologyReferences)

	for _, backendName := range sortedBackendNames(r.backends) {
		b := r.backends[backendName]
		tasBackend, ok := b.(scheduler.TopologyAwareSchedBackend)
		if !ok {
			continue
		}

		ref := schedulerRefMap[b.Name()]
		if ref == nil {
			// Auto-managed: create/update via SyncTopology
			if err := tasBackend.SyncTopology(ctx, nil, ct); err != nil {
				reconcileErr = errors.Join(reconcileErr, err)
				statuses = append(statuses, grovecorev1alpha1.SchedulerTopologyStatus{
					SchedulerTopologyReference: grovecorev1alpha1.SchedulerTopologyReference{
						SchedulerName:     b.Name(),
						TopologyReference: tasBackend.TopologyResourceName(ct),
					},
					InSync:  false,
					Message: err.Error(),
				})
				logger.Error(err, "Failed to sync topology for backend", "backend", b.Name())
			} else {
				statuses = append(statuses, grovecorev1alpha1.SchedulerTopologyStatus{
					SchedulerTopologyReference: grovecorev1alpha1.SchedulerTopologyReference{
						SchedulerName:     b.Name(),
						TopologyReference: tasBackend.TopologyResourceName(ct),
					},
					InSync: true,
				})
			}
		} else {
			// Externally managed: drift detection only
			inSync, msg, gen, err := tasBackend.CheckTopologyDrift(ctx, ct, *ref)
			if err != nil {
				reconcileErr = errors.Join(reconcileErr, err)
				msg = err.Error()
				logger.Error(err, "Failed to check topology drift for backend", "backend", b.Name())
			}
			statuses = append(statuses, grovecorev1alpha1.SchedulerTopologyStatus{
				SchedulerTopologyReference: grovecorev1alpha1.SchedulerTopologyReference{
					SchedulerName:     b.Name(),
					TopologyReference: ref.TopologyReference,
				},
				InSync: inSync,
				SchedulerBackendTopologyObservedGeneration: gen,
				Message: msg,
			})
		}
	}

	for _, ref := range ct.Spec.SchedulerTopologyReferences {
		backend, exists := r.backends[ref.SchedulerName]
		if !exists {
			topologyNotFound = true
			statuses = append(statuses, grovecorev1alpha1.SchedulerTopologyStatus{
				SchedulerTopologyReference: ref,
				InSync:                     false,
				Message:                    fmt.Sprintf("scheduler backend %q is not enabled", ref.SchedulerName),
			})
			continue
		}

		if _, ok := backend.(scheduler.TopologyAwareSchedBackend); !ok {
			topologyNotFound = true
			statuses = append(statuses, grovecorev1alpha1.SchedulerTopologyStatus{
				SchedulerTopologyReference: ref,
				InSync:                     false,
				Message:                    fmt.Sprintf("scheduler backend %q does not support topology management", ref.SchedulerName),
			})
		}
	}

	if reconcileErr == nil {
		ct.Status.ObservedGeneration = ct.Generation
	}
	ct.Status.SchedulerTopologyStatuses = statuses
	var prevStatus metav1.ConditionStatus
	if c := meta.FindStatusCondition(ct.Status.Conditions, apicommonconstants.ConditionSchedulerTopologyDrift); c != nil {
		prevStatus = c.Status
	}
	setSchedulerTopologyDriftCondition(ct, statuses, topologyNotFound)
	newCondition := meta.FindStatusCondition(ct.Status.Conditions, apicommonconstants.ConditionSchedulerTopologyDrift)
	r.emitDriftTransitionEvent(ct, prevStatus, newCondition)

	if err := r.Status().Update(ctx, ct); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ClusterTopology status: %w", err)
	}
	return ctrl.Result{}, reconcileErr
}

// setSchedulerTopologyDriftCondition sets the SchedulerTopologyDrift condition on the ClusterTopology
// based on the aggregated scheduler topology statuses.
func setSchedulerTopologyDriftCondition(ct *grovecorev1alpha1.ClusterTopology, statuses []grovecorev1alpha1.SchedulerTopologyStatus, topologyNotFound bool) {
	if len(statuses) == 0 {
		meta.RemoveStatusCondition(&ct.Status.Conditions, apicommonconstants.ConditionSchedulerTopologyDrift)
		return
	}

	if topologyNotFound {
		meta.SetStatusCondition(&ct.Status.Conditions, metav1.Condition{
			Type:               apicommonconstants.ConditionSchedulerTopologyDrift,
			Status:             metav1.ConditionUnknown,
			Reason:             apicommonconstants.ConditionReasonTopologyNotFound,
			Message:            "One or more referenced scheduler backends are unavailable for topology management",
			ObservedGeneration: ct.Generation,
		})
		return
	}

	allInSync := true
	for _, s := range statuses {
		if !s.InSync {
			allInSync = false
			break
		}
	}

	if allInSync {
		meta.SetStatusCondition(&ct.Status.Conditions, metav1.Condition{
			Type:               apicommonconstants.ConditionSchedulerTopologyDrift,
			Status:             metav1.ConditionFalse,
			Reason:             apicommonconstants.ConditionReasonInSync,
			Message:            "All scheduler backend topologies are in sync",
			ObservedGeneration: ct.Generation,
		})
	} else {
		meta.SetStatusCondition(&ct.Status.Conditions, metav1.Condition{
			Type:               apicommonconstants.ConditionSchedulerTopologyDrift,
			Status:             metav1.ConditionTrue,
			Reason:             apicommonconstants.ConditionReasonDrift,
			Message:            "One or more scheduler backend topologies have drifted",
			ObservedGeneration: ct.Generation,
		})
	}
}

// emitDriftTransitionEvent emits a Kubernetes event when the SchedulerTopologyDrift
// condition transitions between statuses.
func (r *Reconciler) emitDriftTransitionEvent(ct *grovecorev1alpha1.ClusterTopology, prevStatus metav1.ConditionStatus, next *metav1.Condition) {
	if next == nil || prevStatus == next.Status {
		return
	}
	switch next.Status {
	case metav1.ConditionTrue:
		r.recorder.Eventf(ct, corev1.EventTypeWarning, internalconstants.ReasonTopologyDriftDetected,
			"One or more scheduler backend topologies have drifted")
	case metav1.ConditionFalse:
		r.recorder.Eventf(ct, corev1.EventTypeNormal, internalconstants.ReasonTopologyInSync,
			"All scheduler backend topologies are in sync")
	}
}

// sortedBackendNames returns backend names in sorted order for deterministic reconciliation.
func sortedBackendNames(backends map[string]scheduler.Backend) []string {
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
