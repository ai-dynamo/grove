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
	"fmt"
	"reflect"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	kaitopologyv1alpha1 "github.com/NVIDIA/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/samber/lo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Reconciler reconciles ClusterTopology resources and manages corresponding KAI Topology resources.
type Reconciler struct {
	client     client.Client
	scheme     *runtime.Scheme
	tasEnabled bool
}

// NewReconciler creates a new reconciler for ClusterTopology.
func NewReconciler(mgr ctrl.Manager, tasEnabled bool) *Reconciler {
	return &Reconciler{
		client:     mgr.GetClient(),
		scheme:     mgr.GetScheme(),
		tasEnabled: tasEnabled,
	}
}

// Reconcile reconciles a ClusterTopology resource.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	ct := &grovecorev1alpha1.ClusterTopology{}
	if err := r.client.Get(ctx, req.NamespacedName, ct); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ClusterTopology not found, skipping reconciliation")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get ClusterTopology %s: %w", req.Name, err)
	}

	if !r.tasEnabled {
		log.Info("Topology-aware scheduling is disabled, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	if len(ct.Spec.SchedulerReferences) == 0 {
		if err := r.reconcileAutoManaged(ctx, ct); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		r.reconcileDriftDetection(ctx, ct)
	}

	ct.Status.ObservedGeneration = ct.Generation
	if err := r.client.Status().Update(ctx, ct); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ClusterTopology %s status: %w", ct.Name, err)
	}

	return ctrl.Result{}, nil
}

// reconcileAutoManaged handles the auto-managed path where the operator creates and owns the KAI Topology.
func (r *Reconciler) reconcileAutoManaged(ctx context.Context, ct *grovecorev1alpha1.ClusterTopology) error {
	log := ctrl.LoggerFrom(ctx)

	desiredTopology, err := buildKAITopology(ct, r.scheme)
	if err != nil {
		return fmt.Errorf("failed to build KAI Topology for ClusterTopology %s: %w", ct.Name, err)
	}

	existingTopology := &kaitopologyv1alpha1.Topology{}
	err = r.client.Get(ctx, client.ObjectKey{Name: ct.Name}, existingTopology)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if createErr := r.client.Create(ctx, desiredTopology); createErr != nil {
				return fmt.Errorf("failed to create KAI Topology %s: %w", ct.Name, createErr)
			}
			log.Info("Created KAI Topology", "name", ct.Name)
			setSchedulerTopologyDriftCondition(ct, metav1.ConditionFalse, apicommonconstants.ConditionReasonInSync, "KAI Topology is in sync with ClusterTopology")
			ct.Status.SchedulerTopologyStatuses = nil
			return nil
		}
		return fmt.Errorf("failed to get KAI Topology %s: %w", ct.Name, err)
	}

	// The KAI Topology exists. Check if this CT owns it.
	if !metav1.IsControlledBy(existingTopology, ct) {
		msg := fmt.Sprintf("KAI Topology %s exists but is not owned by ClusterTopology %s", ct.Name, ct.Name)
		log.Error(nil, msg)
		setSchedulerTopologyDriftCondition(ct, metav1.ConditionTrue, apicommonconstants.ConditionReasonDrift, msg)
		return nil
	}

	// Owned by this CT. Check if levels changed.
	if isKAITopologyChanged(existingTopology, desiredTopology) {
		// KAI Topology has immutable levels, so delete and recreate.
		if deleteErr := r.client.Delete(ctx, existingTopology); deleteErr != nil {
			return fmt.Errorf("failed to recreate (action: delete) existing KAI Topology %s: %w", ct.Name, deleteErr)
		}
		if createErr := r.client.Create(ctx, desiredTopology); createErr != nil {
			return fmt.Errorf("failed to recreate (action: create) KAI Topology %s: %w", ct.Name, createErr)
		}
		log.Info("Recreated KAI Topology with updated levels", "name", ct.Name)
	}

	setSchedulerTopologyDriftCondition(ct, metav1.ConditionFalse, apicommonconstants.ConditionReasonInSync, "KAI Topology is in sync with ClusterTopology")
	ct.Status.SchedulerTopologyStatuses = nil
	return nil
}

// reconcileDriftDetection handles the drift detection path for externally-managed scheduler topology references.
func (r *Reconciler) reconcileDriftDetection(ctx context.Context, ct *grovecorev1alpha1.ClusterTopology) {
	log := ctrl.LoggerFrom(ctx)

	var (
		statuses     []grovecorev1alpha1.SchedulerTopologyStatus
		hasDrift     bool
		hasNotFound  bool
		driftDetails []string
	)

	for _, ref := range ct.Spec.SchedulerReferences {
		kaiTopology := &kaitopologyv1alpha1.Topology{}
		err := r.client.Get(ctx, client.ObjectKey{Name: ref.Reference}, kaiTopology)
		if err != nil {
			if apierrors.IsNotFound(err) {
				hasNotFound = true
				statuses = append(statuses, grovecorev1alpha1.SchedulerTopologyStatus{
					SchedulerName: ref.SchedulerName,
					Reference:     ref.Reference,
					InSync:        false,
					Message:       fmt.Sprintf("KAI Topology %s not found", ref.Reference),
				})
				driftDetails = append(driftDetails, fmt.Sprintf("topology %q not found", ref.Reference))
				continue
			}
			log.Error(err, "Failed to get referenced KAI Topology", "reference", ref.Reference)
			statuses = append(statuses, grovecorev1alpha1.SchedulerTopologyStatus{
				SchedulerName: ref.SchedulerName,
				Reference:     ref.Reference,
				InSync:        false,
				Message:       fmt.Sprintf("failed to get KAI Topology %s: %v", ref.Reference, err),
			})
			hasDrift = true
			driftDetails = append(driftDetails, fmt.Sprintf("error fetching topology %q", ref.Reference))
			continue
		}

		inSync, mismatchMsg := compareLevels(ct, kaiTopology)
		status := grovecorev1alpha1.SchedulerTopologyStatus{
			SchedulerName: ref.SchedulerName,
			Reference:     ref.Reference,
			InSync:        inSync,
			SchedulerBackendTopologyObservedGeneration: kaiTopology.Generation,
		}
		if !inSync {
			hasDrift = true
			status.Message = mismatchMsg
			driftDetails = append(driftDetails, fmt.Sprintf("topology %q: %s", ref.Reference, mismatchMsg))
		}
		statuses = append(statuses, status)
	}

	ct.Status.SchedulerTopologyStatuses = statuses

	switch {
	case hasNotFound:
		setSchedulerTopologyDriftCondition(ct, metav1.ConditionUnknown, apicommonconstants.ConditionReasonTopologyNotFound,
			fmt.Sprintf("one or more referenced topologies not found: %v", driftDetails))
	case hasDrift:
		setSchedulerTopologyDriftCondition(ct, metav1.ConditionTrue, apicommonconstants.ConditionReasonDrift,
			fmt.Sprintf("scheduler topology drift detected: %v", driftDetails))
	default:
		setSchedulerTopologyDriftCondition(ct, metav1.ConditionFalse, apicommonconstants.ConditionReasonInSync,
			"all scheduler backend topologies are in sync with ClusterTopology")
	}
}

// buildKAITopology constructs a KAI Topology resource from a ClusterTopology, setting an owner reference.
func buildKAITopology(ct *grovecorev1alpha1.ClusterTopology, scheme *runtime.Scheme) (*kaitopologyv1alpha1.Topology, error) {
	kaiLevels := lo.Map(ct.Spec.Levels, func(level grovecorev1alpha1.TopologyLevel, _ int) kaitopologyv1alpha1.TopologyLevel {
		return kaitopologyv1alpha1.TopologyLevel{
			NodeLabel: level.Key,
		}
	})
	kaiTopology := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: ct.Name,
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: kaiLevels,
		},
	}
	if err := controllerutil.SetControllerReference(ct, kaiTopology, scheme); err != nil {
		return nil, fmt.Errorf("failed to set owner reference for KAI Topology: %w", err)
	}
	return kaiTopology, nil
}

// isKAITopologyChanged compares the levels of two KAI Topology resources.
func isKAITopologyChanged(existing, desired *kaitopologyv1alpha1.Topology) bool {
	return !reflect.DeepEqual(existing.Spec.Levels, desired.Spec.Levels)
}

// compareLevels checks whether the KAI Topology levels match the ClusterTopology levels.
// Returns true if in sync, or false with a description of the mismatch.
func compareLevels(ct *grovecorev1alpha1.ClusterTopology, kaiTopology *kaitopologyv1alpha1.Topology) (bool, string) {
	ctLevels := ct.Spec.Levels
	kaiLevels := kaiTopology.Spec.Levels

	if len(ctLevels) != len(kaiLevels) {
		return false, fmt.Sprintf("level count mismatch: ClusterTopology has %d levels, KAI Topology has %d levels",
			len(ctLevels), len(kaiLevels))
	}
	for i := range ctLevels {
		if ctLevels[i].Key != kaiLevels[i].NodeLabel {
			return false, fmt.Sprintf("level %d mismatch: ClusterTopology key %q != KAI Topology nodeLabel %q",
				i, ctLevels[i].Key, kaiLevels[i].NodeLabel)
		}
	}
	return true, ""
}

// setSchedulerTopologyDriftCondition sets the SchedulerTopologyDrift condition on the ClusterTopology status.
func setSchedulerTopologyDriftCondition(ct *grovecorev1alpha1.ClusterTopology, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&ct.Status.Conditions, metav1.Condition{
		Type:               apicommonconstants.ConditionTypeSchedulerTopologyDrift,
		Status:             status,
		ObservedGeneration: ct.Generation,
		Reason:             reason,
		Message:            message,
	})
}
