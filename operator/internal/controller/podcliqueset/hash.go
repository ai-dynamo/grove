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

package podcliqueset

import (
	"sort"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// resourceSchedulingInfo captures scheduling fields associated with one named
// PodClique or PodCliqueScalingGroup. Future resource-level scheduling fields,
// such as topology constraints, can be added here.
type resourceSchedulingInfo struct {
	name         string
	minAvailable int32
}

// pcsSchedulingConfig captures scheduling fields that contribute to a
// PodCliqueSet's scheduling-configuration hash. Future PCS-level scheduling
// fields can be added here.
type pcsSchedulingConfig struct {
	podCliques             []resourceSchedulingInfo
	podCliqueScalingGroups []resourceSchedulingInfo
}

// pcsGenerationConfig combines workload content and scheduling configuration
// into the PodCliqueSet generation identity.
type pcsGenerationConfig struct {
	podTemplateHash             string
	schedulingConfigurationHash string
}

// computeGenerationHash calculates the combined content and scheduling hash of a PodCliqueSet.
func computeGenerationHash(pcs *grovecorev1alpha1.PodCliqueSet) string {
	_, _, generationHash := computePCSHashes(pcs)
	return generationHash
}

// computePCSHashes computes the legacy generation, scheduling configuration,
// and combined generation hashes from the current PodCliqueSet specification.
func computePCSHashes(pcs *grovecorev1alpha1.PodCliqueSet) (
	legacyGenerationHash string,
	schedulingConfigurationHash string,
	generationHash string,
) {
	legacyGenerationHash = computeLegacyGenerationHash(pcs)
	schedulingConfig := buildPCSSchedulingConfig(pcs)
	schedulingConfigurationHash = computeSchedulingConfigurationHash(schedulingConfig)
	generationHash = k8sutils.ComputeHash(pcsGenerationConfig{
		podTemplateHash:             legacyGenerationHash,
		schedulingConfigurationHash: schedulingConfigurationHash,
	})
	return
}

// buildPCSSchedulingConfig creates a stable, name-ordered representation of the scheduling configuration.
func buildPCSSchedulingConfig(pcs *grovecorev1alpha1.PodCliqueSet) pcsSchedulingConfig {
	podCliques := make([]resourceSchedulingInfo, 0, len(pcs.Spec.Template.Cliques))
	for _, clique := range pcs.Spec.Template.Cliques {
		podCliques = append(podCliques, resourceSchedulingInfo{
			name:         clique.Name,
			minAvailable: ptr.Deref(clique.Spec.MinAvailable, clique.Spec.Replicas),
		})
	}
	sort.Slice(podCliques, func(i, j int) bool {
		return podCliques[i].less(podCliques[j])
	})

	podCliqueScalingGroups := make([]resourceSchedulingInfo, 0, len(pcs.Spec.Template.PodCliqueScalingGroupConfigs))
	for _, scalingGroup := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		podCliqueScalingGroups = append(podCliqueScalingGroups, resourceSchedulingInfo{
			name:         scalingGroup.Name,
			minAvailable: ptr.Deref(scalingGroup.MinAvailable, int32(1)),
		})
	}
	sort.Slice(podCliqueScalingGroups, func(i, j int) bool {
		return podCliqueScalingGroups[i].less(podCliqueScalingGroups[j])
	})

	return pcsSchedulingConfig{
		podCliques:             podCliques,
		podCliqueScalingGroups: podCliqueScalingGroups,
	}
}

func (r resourceSchedulingInfo) less(other resourceSchedulingInfo) bool {
	return r.name < other.name
}

// computeLegacyGenerationHash calculates the generation hash used before scheduling configuration hashing was introduced.
func computeLegacyGenerationHash(pcs *grovecorev1alpha1.PodCliqueSet) string {
	podTemplateSpecs := lo.Map(pcs.Spec.Template.Cliques, func(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, _ int) *corev1.PodTemplateSpec {
		podTemplateSpec := &corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      pclqTemplateSpec.Labels,
				Annotations: pclqTemplateSpec.Annotations,
			},
			Spec: pclqTemplateSpec.Spec.PodSpec,
		}
		podTemplateSpec.Spec.PriorityClassName = pcs.Spec.Template.PriorityClassName
		return podTemplateSpec
	})
	return k8sutils.ComputeHash(podTemplateSpecs...)
}

// computeSchedulingConfigurationHash calculates a hash of the scheduling configuration.
func computeSchedulingConfigurationHash(config pcsSchedulingConfig) string {
	return k8sutils.ComputeHash(config)
}
