// /*
// Copyright 2024 The Grove Authors.
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

package defaulting

import (
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// defaultPodCliqueSet adds defaults to a PodCliqueSet.
func defaultPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) {
	if utils.IsEmptyStringType(pcs.Namespace) {
		pcs.Namespace = "default"
	}
	defaultPodCliqueSetSpec(&pcs.Spec)
}

// defaultPodCliqueSetSpec adds defaults to the specification of a PodCliqueSet.
func defaultPodCliqueSetSpec(spec *grovecorev1alpha1.PodCliqueSetSpec) {
	defaultPodCliqueSetTemplateSpec(&spec.Template)
}

// defaultPodCliqueSetTemplateSpec applies defaults to the template specification including cliques, scaling groups, and service configuration.
// Note: TerminationDelay is intentionally not defaulted - when nil, gang termination is disabled.
func defaultPodCliqueSetTemplateSpec(spec *grovecorev1alpha1.PodCliqueSetTemplateSpec) {
	spec.Cliques = defaultPodCliqueTemplateSpecs(spec.Cliques)
	spec.PodCliqueScalingGroupConfigs = defaultPodCliqueScalingGroupConfigs(spec.PodCliqueScalingGroupConfigs)
	spec.HeadlessServiceConfig = defaultHeadlessServiceConfig(spec.HeadlessServiceConfig)
}

// defaultHeadlessServiceConfig applies defaults to the headless service configuration.
func defaultHeadlessServiceConfig(headlessServiceConfig *grovecorev1alpha1.HeadlessServiceConfig) *grovecorev1alpha1.HeadlessServiceConfig {
	if headlessServiceConfig == nil {
		headlessServiceConfig = &grovecorev1alpha1.HeadlessServiceConfig{
			PublishNotReadyAddresses: true,
		}
	}
	return headlessServiceConfig
}

// defaultPodCliqueTemplateSpecs applies defaults to each PodClique template including replicas, minAvailable, and autoscaling configuration.
func defaultPodCliqueTemplateSpecs(cliqueSpecs []*grovecorev1alpha1.PodCliqueTemplateSpec) []*grovecorev1alpha1.PodCliqueTemplateSpec {
	defaultedCliqueSpecs := make([]*grovecorev1alpha1.PodCliqueTemplateSpec, 0, len(cliqueSpecs))
	for _, cliqueSpec := range cliqueSpecs {
		defaultedCliqueSpec := cliqueSpec.DeepCopy()
		defaultedCliqueSpec.Spec.PodSpec = *defaultPodSpec(&cliqueSpec.Spec.PodSpec)
		if defaultedCliqueSpec.Spec.Replicas == 0 {
			defaultedCliqueSpec.Spec.Replicas = 1
		}
		if cliqueSpec.Spec.MinAvailable == nil {
			defaultedCliqueSpec.Spec.MinAvailable = ptr.To(cliqueSpec.Spec.Replicas)
		}
		if cliqueSpec.Spec.ScaleConfig != nil {
			if cliqueSpec.Spec.ScaleConfig.MinReplicas == nil {
				defaultedCliqueSpec.Spec.ScaleConfig.MinReplicas = ptr.To(cliqueSpec.Spec.Replicas)
			}
		}
		defaultedCliqueSpecs = append(defaultedCliqueSpecs, defaultedCliqueSpec)
	}
	return defaultedCliqueSpecs
}

// defaultPodCliqueScalingGroupConfigs applies defaults to scaling group configurations.
// Note: Replicas field is already set by kubebuilder defaults before the webhook runs.
func defaultPodCliqueScalingGroupConfigs(scalingGroupConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig) []grovecorev1alpha1.PodCliqueScalingGroupConfig {
	defaultedScalingGroupConfigs := make([]grovecorev1alpha1.PodCliqueScalingGroupConfig, 0, len(scalingGroupConfigs))
	for _, scalingGroupConfig := range scalingGroupConfigs {
		defaultedScalingGroupConfig := scalingGroupConfig.DeepCopy()
		// Replicas is already set by kubebuilder default (API server runs before defaulting webhook)
		if scalingGroupConfig.ScaleConfig != nil {
			if scalingGroupConfig.ScaleConfig.MinReplicas == nil {
				defaultedScalingGroupConfig.ScaleConfig.MinReplicas = ptr.To(*defaultedScalingGroupConfig.Replicas)
			}
		}
		defaultedScalingGroupConfigs = append(defaultedScalingGroupConfigs, *defaultedScalingGroupConfig)
	}
	return defaultedScalingGroupConfigs
}

// defaultPodSpec adds defaults to PodSpec.
func defaultPodSpec(spec *corev1.PodSpec) *corev1.PodSpec {
	defaultedPodSpec := spec.DeepCopy()
	if utils.IsEmptyStringType(defaultedPodSpec.RestartPolicy) {
		defaultedPodSpec.RestartPolicy = corev1.RestartPolicyAlways
	}
	if defaultedPodSpec.TerminationGracePeriodSeconds == nil {
		defaultedPodSpec.TerminationGracePeriodSeconds = ptr.To[int64](30)
	}
	return defaultedPodSpec
}
