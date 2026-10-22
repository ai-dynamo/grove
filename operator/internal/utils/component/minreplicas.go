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
	"strconv"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"k8s.io/utils/ptr"
)

// The accessors in this file name the purpose a min-replica, update-unit or breach-tolerance value is read
// for: gang scheduling (InitMinReplicas), the breach threshold (AvailabilityMinReplicas), "available"
// counting in status (ReadyThreshold), the minimum update unit (UpdateMinReplicas) and the time a breach is
// tolerated (BreachTolerance). Call sites read these instead of the API fields. Each accessor resolves an
// unset field the way the defaulting webhook would, so objects that have not been re-admitted read the same
// values.

// DefaultBreachTolerance is the breach tolerance applied when none is configured.
const DefaultBreachTolerance = 4 * time.Hour

// defaultMinReplicas is the default of the min-replica fields when no other field determines it.
const defaultMinReplicas int32 = 1

// Availability returns the availability policy in termination, or nil when it is not set.
func Availability(termination *grovecorev1beta1.TerminationSpec) *grovecorev1beta1.AvailabilityPolicy {
	if termination == nil || termination.Resource == nil {
		return nil
	}
	return termination.Resource.Availability
}

// availabilityMinReplicas returns availability.minReplicas, and false when availability is not set.
func availabilityMinReplicas(termination *grovecorev1beta1.TerminationSpec) (int32, bool) {
	availability := Availability(termination)
	if availability == nil {
		return 0, false
	}
	return ptr.Deref(availability.MinReplicas, defaultMinReplicas), true
}

// initMinReplicas returns init.minReplicas, defaulting to availability.minReplicas when availability is set and to
// 1 otherwise.
func initMinReplicas(init *grovecorev1beta1.InitSpec, termination *grovecorev1beta1.TerminationSpec) int32 {
	if init != nil && init.MinReplicas != nil {
		return *init.MinReplicas
	}
	if minReplicas, ok := availabilityMinReplicas(termination); ok {
		return minReplicas
	}
	return defaultMinReplicas
}

// DefaultUpdateMinReplicas returns the default rollingUpdate.minReplicas of a component under the Coherent
// strategy: availability.minReplicas when availability is set, and 1 otherwise.
func DefaultUpdateMinReplicas(termination *grovecorev1beta1.TerminationSpec) int32 {
	if minReplicas, ok := availabilityMinReplicas(termination); ok {
		return minReplicas
	}
	return defaultMinReplicas
}

// ScaleFloor returns the lowest non-zero replica count of a component, which autoscaling and the scale guard
// enforce: availability.minReplicas when availability is set, and 1 otherwise.
func ScaleFloor(termination *grovecorev1beta1.TerminationSpec) int32 {
	return DefaultUpdateMinReplicas(termination)
}

// PCLQInitMinReplicas returns the number of pods of a PodClique that must be scheduled together as a gang.
func PCLQInitMinReplicas(spec *grovecorev1beta1.PodCliqueSpec) int32 {
	return initMinReplicas(spec.Init, spec.Termination)
}

// PCSGInitMinReplicas returns the number of replicas of a PodCliqueScalingGroup that must be scheduled
// together as a gang.
func PCSGInitMinReplicas(spec *grovecorev1beta1.PodCliqueScalingGroupSpec) int32 {
	return initMinReplicas(spec.Init, spec.Termination)
}

// PCSGConfigInitMinReplicas returns the gang-scheduled replica count of a PodCliqueScalingGroup config in the
// PodCliqueSet template.
func PCSGConfigInitMinReplicas(cfg *grovecorev1beta1.PodCliqueScalingGroupConfig) int32 {
	return initMinReplicas(cfg.Init, cfg.Termination)
}

// PCLQAvailabilityMinReplicas returns the minimum number of available pods below which the PodClique is in
// breach. The second result is false when the PodClique has no availability requirement, in which case it is
// never in breach.
func PCLQAvailabilityMinReplicas(spec *grovecorev1beta1.PodCliqueSpec) (int32, bool) {
	return availabilityMinReplicas(spec.Termination)
}

// PCSGAvailabilityMinReplicas returns the minimum number of available replicas below which the
// PodCliqueScalingGroup is in breach. The second result is false when the PodCliqueScalingGroup has no
// availability requirement, in which case it is never in breach.
func PCSGAvailabilityMinReplicas(spec *grovecorev1beta1.PodCliqueScalingGroupSpec) (int32, bool) {
	return availabilityMinReplicas(spec.Termination)
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

// updateMinReplicas returns rollingUpdate.minReplicas under the Coherent strategy, defaulting as the webhook
// does, and init.minReplicas under the other strategies.
func updateMinReplicas(pcs *grovecorev1beta1.PodCliqueSet, rollingUpdate *grovecorev1beta1.RollingUpdateConfiguration, init *grovecorev1beta1.InitSpec, termination *grovecorev1beta1.TerminationSpec) int32 {
	if IsCoherentStrategy(pcs) {
		if rollingUpdate != nil && rollingUpdate.MinReplicas != nil {
			return *rollingUpdate.MinReplicas
		}
		return DefaultUpdateMinReplicas(termination)
	}
	return initMinReplicas(init, termination)
}

// CliqueTemplateUpdateMinReplicas returns the minimum update unit of a clique template in the PodCliqueSet:
// the number of its pods that are updated, and must be updated and ready, together.
func CliqueTemplateUpdateMinReplicas(pcs *grovecorev1beta1.PodCliqueSet, cliqueTemplate *grovecorev1beta1.PodCliqueTemplateSpec) int32 {
	return updateMinReplicas(pcs, cliqueTemplate.RollingUpdate, cliqueTemplate.Spec.Init, cliqueTemplate.Spec.Termination)
}

// PCSGConfigUpdateMinReplicas returns the minimum update unit of a PodCliqueScalingGroup config in the
// PodCliqueSet template, in replicas.
func PCSGConfigUpdateMinReplicas(pcs *grovecorev1beta1.PodCliqueSet, cfg *grovecorev1beta1.PodCliqueScalingGroupConfig) int32 {
	return updateMinReplicas(pcs, cfg.RollingUpdate, cfg.Init, cfg.Termination)
}

// PCLQUpdateMinReplicas returns the minimum update unit of a PodClique object, in pods. It also is the number
// of updated and ready pods that completes the PodClique's update. For a standalone PodClique it is read from
// its template, so it follows rollingUpdate.minReplicas under Coherent. For a PCSG member, it is the member's
// gang-scheduled minimum.
func PCLQUpdateMinReplicas(pcs *grovecorev1beta1.PodCliqueSet, pclq *grovecorev1beta1.PodClique) int32 {
	if _, isPCSGMember := pclq.Labels[apicommon.LabelPodCliqueScalingGroup]; !isPCSGMember {
		if cliqueTemplate := findCliqueTemplateForPCLQ(pcs, pclq); cliqueTemplate != nil {
			return updateMinReplicas(pcs, cliqueTemplate.RollingUpdate, pclq.Spec.Init, pclq.Spec.Termination)
		}
	}
	return PCLQInitMinReplicas(&pclq.Spec)
}

// PCSGUpdateMinReplicas returns the minimum update unit of a PodCliqueScalingGroup object, in replicas.
func PCSGUpdateMinReplicas(pcs *grovecorev1beta1.PodCliqueSet, pcsg *grovecorev1beta1.PodCliqueScalingGroup) int32 {
	if cfg := findConfigForPCSG(pcs, pcsg); cfg != nil {
		return updateMinReplicas(pcs, cfg.RollingUpdate, pcsg.Spec.Init, pcsg.Spec.Termination)
	}
	return PCSGInitMinReplicas(&pcsg.Spec)
}

// PCLQBreachTolerance returns how long the PodClique may stay in breach before its PodCliqueSet replica (or,
// for a PCSG member, its PCSG replica) is gang-terminated: its own breachTolerance, else its template's.
func PCLQBreachTolerance(pcs *grovecorev1beta1.PodCliqueSet, pclq *grovecorev1beta1.PodClique) time.Duration {
	if tolerance := breachToleranceOf(pclq.Spec.Termination); tolerance != nil {
		return *tolerance
	}
	if cliqueTemplate := findCliqueTemplateForPCLQ(pcs, pclq); cliqueTemplate != nil {
		if tolerance := breachToleranceOf(cliqueTemplate.Spec.Termination); tolerance != nil {
			return *tolerance
		}
	}
	return DefaultBreachTolerance
}

// PCSGBreachTolerance returns how long the PodCliqueScalingGroup may stay in breach before its PodCliqueSet
// replica is gang-terminated: its own breachTolerance, else its config's.
func PCSGBreachTolerance(pcs *grovecorev1beta1.PodCliqueSet, pcsg *grovecorev1beta1.PodCliqueScalingGroup) time.Duration {
	if tolerance := breachToleranceOf(pcsg.Spec.Termination); tolerance != nil {
		return *tolerance
	}
	if cfg := findConfigForPCSG(pcs, pcsg); cfg != nil {
		if tolerance := breachToleranceOf(cfg.Termination); tolerance != nil {
			return *tolerance
		}
	}
	return DefaultBreachTolerance
}

// ResolvedBreachTolerance returns the breachTolerance of a template component, defaulted.
func ResolvedBreachTolerance(termination *grovecorev1beta1.TerminationSpec) time.Duration {
	if tolerance := breachToleranceOf(termination); tolerance != nil {
		return *tolerance
	}
	return DefaultBreachTolerance
}

func breachToleranceOf(termination *grovecorev1beta1.TerminationSpec) *time.Duration {
	availability := Availability(termination)
	if availability == nil || availability.BreachTolerance == nil {
		return nil
	}
	return &availability.BreachTolerance.Duration
}

func findCliqueTemplateForPCLQ(pcs *grovecorev1beta1.PodCliqueSet, pclq *grovecorev1beta1.PodClique) *grovecorev1beta1.PodCliqueTemplateSpec {
	if pcs == nil {
		return nil
	}
	cliqueName, err := GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
	if err != nil {
		return nil
	}
	return FindPodCliqueTemplateSpecByName(pcs, cliqueName)
}

func findConfigForPCSG(pcs *grovecorev1beta1.PodCliqueSet, pcsg *grovecorev1beta1.PodCliqueScalingGroup) *grovecorev1beta1.PodCliqueScalingGroupConfig {
	if pcs == nil {
		return nil
	}
	replicaIndex, err := strconv.Atoi(pcsg.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
	if err != nil {
		return nil
	}
	pcsNameReplica := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex}
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		cfg := &pcs.Spec.Template.PodCliqueScalingGroupConfigs[i]
		if apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, cfg.Name) == pcsg.Name {
			return cfg
		}
	}
	return nil
}
