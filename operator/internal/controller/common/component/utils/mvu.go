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
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"k8s.io/utils/clock"
)

// MVUTemplate describes the composition of one Minimum Viable Unit (MVU) PodGang.
// It specifies the minimum number of pods per standalone PodClique and the minimum
// number of replicas per PodCliqueScalingGroup that must be gang-scheduled together.
type MVUTemplate struct {
	// StandalonePCLQs maps standalone PodClique name to the minAvailable pod count.
	StandalonePCLQs map[string]int32
	// PCSGs maps PodCliqueScalingGroup name to the minAvailable replica count.
	PCSGs map[string]int32
}

// PodGangEntryBuilder is a function that creates a PodGangEntry given standalone PCLQ pod counts
// and PCSG replica indices for the PodGang, and the names of PodGangs this entry depends on.
type PodGangEntryBuilder func(standalonePCLQReplicas map[string]int32, pcsgReplicaIndices map[string][]int32, dependsOn []string) grovecorev1alpha1.PodGangEntry

// ComputeMVUTemplateFromPCSTemplateSpec computes the MVU template for a PCS from its spec.
func ComputeMVUTemplateFromPCSTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet) MVUTemplate {
	return MVUTemplate{
		StandalonePCLQs: GetStandalonePCLQMinAvailableFromPCSTemplateSpec(pcs),
		PCSGs:           GetPCSGMinAvailableFromPCSTemplateSpec(pcs),
	}
}

// GetStandalonePCLQMinAvailableFromPCSTemplateSpec returns the minAvailable pod count per standalone PCLQ from the PCS spec.
func GetStandalonePCLQMinAvailableFromPCSTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet) map[string]int32 {
	result := make(map[string]int32)
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		pcsgConfig := FindScalingGroupConfigForClique(pcs.Spec.Template.PodCliqueScalingGroupConfigs, cliqueTemplate.Name)
		if pcsgConfig == nil {
			result[cliqueTemplate.Name] = *cliqueTemplate.Spec.MinAvailable
		}
	}
	return result
}

// GetStandalonePCLQReplicasFromPCSTemplateSpec returns the total replica count per standalone PCLQ from the PCS spec.
func GetStandalonePCLQReplicasFromPCSTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet) map[string]int32 {
	result := make(map[string]int32)
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		pcsgConfig := FindScalingGroupConfigForClique(pcs.Spec.Template.PodCliqueScalingGroupConfigs, cliqueTemplate.Name)
		if pcsgConfig == nil {
			result[cliqueTemplate.Name] = cliqueTemplate.Spec.Replicas
		}
	}
	return result
}

// GetPCSGMinAvailableFromPCSTemplateSpec returns the minAvailable replica count per PCSG from the PCS spec.
func GetPCSGMinAvailableFromPCSTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet) map[string]int32 {
	result := make(map[string]int32)
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		result[pcsgConfig.Name] = *pcsgConfig.MinAvailable
	}
	return result
}

// GetPCSGReplicasFromPCSTemplateSpec returns the total replica count per PCSG from the PCS spec.
func GetPCSGReplicasFromPCSTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet) map[string]int32 {
	result := make(map[string]int32)
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		result[pcsgConfig.Name] = *pcsgConfig.Replicas
	}
	return result
}

// NewPodGangEntryBuilder returns a closure that creates PodGangEntry values with names
// generated under the unified PodGang naming convention.
//
// clk.Now() is invoked on every entry generation, so each name's base timestamp reflects
// the actual moment of generation. A monotonically-increasing intra-builder counter is
// added to the nanosecond value on every invocation to guarantee within-builder uniqueness
// without depending on the host clock's nanosecond resolution advancing between successive
// reads.
//
// pcsGenerationHash is no longer part of the PodGang name under the unified scheme; it is
// still written into PodGangEntry.PodCliqueSetGenerationHash so consumers can identify the
// cohort each entry belongs to.
//
// In production callers should pass clock.RealClock{}; tests can pass
// clocktesting.FakeClock for deterministic name generation.
func NewPodGangEntryBuilder(pcsName string, pcsReplicaIndex int32, pcsGenerationHash string, clock clock.Clock) PodGangEntryBuilder {
	var i int64
	return func(standalonePCLQReplicas map[string]int32, pcsgReplicaIndices map[string][]int32, dependsOn []string) grovecorev1alpha1.PodGangEntry {
		suffix := strconv.FormatInt(clock.Now().UnixNano()+i, 10)
		i++
		return grovecorev1alpha1.PodGangEntry{
			Name:                       apicommon.GeneratePodGangName(pcsName, pcsReplicaIndex, suffix),
			PodCliqueSetGenerationHash: pcsGenerationHash,
			PodCliques:                 standalonePCLQReplicas,
			PCSGReplicaIndices:         pcsgReplicaIndices,
			DependsOn:                  dependsOn,
		}
	}
}
