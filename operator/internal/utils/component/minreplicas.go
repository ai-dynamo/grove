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

package component

import (
	"time"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"k8s.io/utils/ptr"
)

// The accessors in this file name the purpose a min-replica, update-unit or breach-tolerance value is read
// for: gang scheduling (InitMinReplicas), the breach threshold (AvailabilityMinReplicas), "available"
// counting in status (ReadyThreshold), the minimum update unit (UpdateMinReplicas) and the time a breach is
// tolerated (BreachTolerance). Call sites read these instead of the API fields, so the API fields can change
// without changing the call sites.

// DefaultBreachTolerance is the breach tolerance applied when none is configured.
const DefaultBreachTolerance = 4 * time.Hour

// defaultMinReplicas is the API default of the min-replica fields. Objects persisted under an older CRD
// schema can read back nil until their next write.
const defaultMinReplicas int32 = 1

// PCLQInitMinReplicas returns the number of pods of a PodClique that must be scheduled together as a gang.
func PCLQInitMinReplicas(spec *grovecorev1beta1.PodCliqueSpec) int32 {
	return ptr.Deref(spec.MinAvailable, defaultMinReplicas)
}

// PCSGInitMinReplicas returns the number of replicas of a PodCliqueScalingGroup that must be scheduled
// together as a gang.
func PCSGInitMinReplicas(spec *grovecorev1beta1.PodCliqueScalingGroupSpec) int32 {
	return ptr.Deref(spec.MinAvailable, defaultMinReplicas)
}

// PCSGConfigInitMinReplicas returns the gang-scheduled replica count of a PodCliqueScalingGroup config in the
// PodCliqueSet template.
func PCSGConfigInitMinReplicas(cfg *grovecorev1beta1.PodCliqueScalingGroupConfig) int32 {
	return ptr.Deref(cfg.MinAvailable, defaultMinReplicas)
}

// PCLQAvailabilityMinReplicas returns the minimum number of available pods below which the PodClique is in
// breach. The second result is false when the PodClique has no availability requirement, in which case it is
// never in breach.
func PCLQAvailabilityMinReplicas(spec *grovecorev1beta1.PodCliqueSpec) (int32, bool) {
	return ptr.Deref(spec.MinAvailable, defaultMinReplicas), true
}

// PCSGAvailabilityMinReplicas returns the minimum number of available replicas below which the
// PodCliqueScalingGroup is in breach. The second result is false when the PodCliqueScalingGroup has no
// availability requirement, in which case it is never in breach.
func PCSGAvailabilityMinReplicas(spec *grovecorev1beta1.PodCliqueScalingGroupSpec) (int32, bool) {
	return ptr.Deref(spec.MinAvailable, defaultMinReplicas), true
}

// PCLQReadyThreshold returns the number of ready pods a PodClique needs to count as available in its
// parent's status: the availability minimum when set, else the gang-scheduled minimum.
func PCLQReadyThreshold(spec *grovecorev1beta1.PodCliqueSpec) int32 {
	if minReplicas, ok := PCLQAvailabilityMinReplicas(spec); ok {
		return minReplicas
	}
	return PCLQInitMinReplicas(spec)
}

// PCSGReadyThreshold returns the number of available replicas a PodCliqueScalingGroup needs to count as
// available in its parent's status: the availability minimum when set, else the gang-scheduled minimum.
func PCSGReadyThreshold(spec *grovecorev1beta1.PodCliqueScalingGroupSpec) int32 {
	if minReplicas, ok := PCSGAvailabilityMinReplicas(spec); ok {
		return minReplicas
	}
	return PCSGInitMinReplicas(spec)
}

// CliqueTemplateUpdateMinReplicas returns the minimum update unit of a clique template in the PodCliqueSet:
// the number of its pods that are updated, and must be updated and ready, together.
func CliqueTemplateUpdateMinReplicas(_ *grovecorev1beta1.PodCliqueSet, cliqueTemplate *grovecorev1beta1.PodCliqueTemplateSpec) int32 {
	return ptr.Deref(cliqueTemplate.Spec.MinAvailable, defaultMinReplicas)
}

// PCSGConfigUpdateMinReplicas returns the minimum update unit of a PodCliqueScalingGroup config in the
// PodCliqueSet template, in replicas.
func PCSGConfigUpdateMinReplicas(_ *grovecorev1beta1.PodCliqueSet, cfg *grovecorev1beta1.PodCliqueScalingGroupConfig) int32 {
	return ptr.Deref(cfg.MinAvailable, defaultMinReplicas)
}

// PCLQUpdateMinReplicas returns the minimum update unit of a PodClique object, in pods. It also is the number
// of updated and ready pods that completes the PodClique's update. For a PCSG member, it is the member's
// gang-scheduled minimum.
func PCLQUpdateMinReplicas(_ *grovecorev1beta1.PodCliqueSet, pclq *grovecorev1beta1.PodClique) int32 {
	return ptr.Deref(pclq.Spec.MinAvailable, defaultMinReplicas)
}

// PCSGUpdateMinReplicas returns the minimum update unit of a PodCliqueScalingGroup object, in replicas.
func PCSGUpdateMinReplicas(_ *grovecorev1beta1.PodCliqueSet, pcsg *grovecorev1beta1.PodCliqueScalingGroup) int32 {
	return ptr.Deref(pcsg.Spec.MinAvailable, defaultMinReplicas)
}

// PCLQBreachTolerance returns how long the PodClique may stay in breach before its PodCliqueSet replica (or,
// for a PCSG member, its PCSG replica) is gang-terminated.
func PCLQBreachTolerance(pcs *grovecorev1beta1.PodCliqueSet, _ *grovecorev1beta1.PodClique) time.Duration {
	return pcsTerminationDelay(pcs)
}

// PCSGBreachTolerance returns how long the PodCliqueScalingGroup may stay in breach before its PodCliqueSet
// replica is gang-terminated.
func PCSGBreachTolerance(pcs *grovecorev1beta1.PodCliqueSet, _ *grovecorev1beta1.PodCliqueScalingGroup) time.Duration {
	return pcsTerminationDelay(pcs)
}

func pcsTerminationDelay(pcs *grovecorev1beta1.PodCliqueSet) time.Duration {
	if pcs.Spec.Template.TerminationDelay == nil {
		return DefaultBreachTolerance
	}
	return pcs.Spec.Template.TerminationDelay.Duration
}
