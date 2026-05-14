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

package utils

import (
	"time"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ============================================================================
// PodCliqueScalingGroup Option Functions
// ============================================================================

// PCSGOption is a function that modifies a PodCliqueScalingGroup for testing.
type PCSGOption func(*grovecorev1alpha1.PodCliqueScalingGroup)

// WithPCSGMinAvailableBreached sets the PCSG to have MinAvailableBreached=True.
func WithPCSGMinAvailableBreached() PCSGOption {
	return func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
		pcsg.Status.Conditions = []metav1.Condition{
			{
				Type:   constants.ConditionTypeMinAvailableBreached,
				Status: metav1.ConditionTrue,
				Reason: constants.ConditionReasonInsufficientAvailablePCSGReplicas,
			},
		}
		// Set AvailableReplicas to be less than MinAvailable to simulate breach
		minAvailable := pcsg.Spec.Replicas
		if pcsg.Spec.MinAvailable != nil {
			minAvailable = *pcsg.Spec.MinAvailable
		}
		if minAvailable > 0 {
			pcsg.Status.AvailableReplicas = minAvailable - 1
		} else {
			pcsg.Status.AvailableReplicas = 0
		}
	}
}

// WithPCSGUnknownCondition sets the PCSG to have MinAvailableBreached=Unknown.
func WithPCSGUnknownCondition() PCSGOption {
	return func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
		pcsg.Status.Conditions = []metav1.Condition{
			{
				Type:   constants.ConditionTypeMinAvailableBreached,
				Status: metav1.ConditionUnknown,
				Reason: "UnknownState",
			},
		}
		// For unknown condition, set AvailableReplicas to 0 to simulate unavailable state
		pcsg.Status.AvailableReplicas = 0
	}
}

// WithPCSGObservedGeneration sets the PCSG ObservedGeneration to enable status mutations.
func WithPCSGObservedGeneration(generation int64) PCSGOption {
	return func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
		pcsg.Status.ObservedGeneration = &generation
	}
}

// ============================================================================
// PodClique Option Functions
// ============================================================================

// PCLQOption is a function that modifies a PodClique for testing.
type PCLQOption func(*grovecorev1alpha1.PodClique)

// WithPCLQAvailable sets the PodClique to a healthy state with MinAvailableBreached=False and PodCliqueScheduled=True.
func WithPCLQAvailable() PCLQOption {
	return func(pclq *grovecorev1alpha1.PodClique) {
		pclq.Status.Conditions = []metav1.Condition{
			{
				Type:   constants.ConditionTypeMinAvailableBreached,
				Status: metav1.ConditionFalse,
				Reason: "SufficientReadyReplicas",
			},
			{
				Type:   constants.ConditionTypePodCliqueScheduled,
				Status: metav1.ConditionTrue,
				Reason: "ScheduledSuccessfully",
			},
		}
		pclq.Status.ReadyReplicas = pclq.Spec.Replicas
	}
}

// WithPCLQTerminating marks the PodClique for termination with a DeletionTimestamp.
func WithPCLQTerminating() PCLQOption {
	return func(pclq *grovecorev1alpha1.PodClique) {
		now := metav1.NewTime(time.Now())
		pclq.DeletionTimestamp = &now
		pclq.Finalizers = []string{"test-finalizer"}
	}
}

// WithPCLQMinAvailableBreached sets the PodClique to have MinAvailableBreached=True but scheduled.
func WithPCLQMinAvailableBreached() PCLQOption {
	return func(pclq *grovecorev1alpha1.PodClique) {
		pclq.Status.Conditions = []metav1.Condition{
			{
				Type:   constants.ConditionTypeMinAvailableBreached,
				Status: metav1.ConditionTrue,
				Reason: "InsufficientReadyReplicas",
			},
			{
				Type:   constants.ConditionTypePodCliqueScheduled,
				Status: metav1.ConditionTrue,
				Reason: "ScheduledSuccessfully",
			},
		}
	}
}

// WithPCLQNotScheduled sets the PodClique to be not scheduled. A PodClique that is not
// scheduled also has scheduledReplicas < minAvailable, so under the always-breach rule the
// MinAvailableBreached condition is True as well — this helper sets both.
func WithPCLQNotScheduled() PCLQOption {
	return func(pclq *grovecorev1alpha1.PodClique) {
		pclq.Status.Conditions = []metav1.Condition{
			{
				Type:   constants.ConditionTypePodCliqueScheduled,
				Status: metav1.ConditionFalse,
				Reason: "SchedulingFailed",
			},
			{
				Type:   constants.ConditionTypeMinAvailableBreached,
				Status: metav1.ConditionTrue,
				Reason: constants.ConditionReasonScheduledReplicasBelowMinAvailable,
			},
		}
	}
}

// WithPCLQScheduledAndAvailable sets the PodClique to be both scheduled and available.
func WithPCLQScheduledAndAvailable() PCLQOption {
	return WithPCLQAvailable()
}

// WithPCLQScheduledButBreached sets the PodClique to be scheduled but with breached availability.
func WithPCLQScheduledButBreached() PCLQOption {
	return WithPCLQMinAvailableBreached()
}

// WithPCLQNoConditions removes all conditions from the PodClique status.
func WithPCLQNoConditions() PCLQOption {
	return func(pclq *grovecorev1alpha1.PodClique) {
		pclq.Status.Conditions = []metav1.Condition{}
	}
}

// WithPCSGAvailableReplicas sets specific AvailableReplicas count for PCSG without touching conditions.
func WithPCSGAvailableReplicas(available int32) PCSGOption {
	return func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
		pcsg.Status.AvailableReplicas = available
	}
}

// WithPCLQReplicaReadyStatus sets specific ReadyReplicas count for PodClique without touching conditions.
func WithPCLQReplicaReadyStatus(ready int32) PCLQOption {
	return func(pclq *grovecorev1alpha1.PodClique) {
		pclq.Status.ReadyReplicas = ready
	}
}

// WithPCLQCurrentPCSGenerationHash sets the CurrentPodCliqueSetGenerationHash in the PodClique status.
func WithPCLQCurrentPCSGenerationHash(pcsGenerationHash string) PCLQOption {
	return func(pclq *grovecorev1alpha1.PodClique) {
		pclq.Status.CurrentPodCliqueSetGenerationHash = &pcsGenerationHash
	}
}

// WithPCSGCurrentPCSGenerationHash sets the CurrentPodCliqueSetGenerationHash in the PCSG status.
func WithPCSGCurrentPCSGenerationHash(pcsGenerationHash string) PCSGOption {
	return func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
		pcsg.Status.CurrentPodCliqueSetGenerationHash = &pcsGenerationHash
	}
}

// ============================================================================
// PodCliqueSet Option Functions
// ============================================================================

// PCSOption is a function that modifies a PodCliqueSet for testing.
type PCSOption func(*grovecorev1alpha1.PodCliqueSet)
