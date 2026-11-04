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

package defaulting

import (
	"time"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	defaultTerminationDelay                    = 4 * time.Hour
	defaultReplicas                      int32 = 1
	defaultTerminationGracePeriodSeconds int64 = 30
)

// defaultPodCliqueSet adds defaults to a PodCliqueSet.
func defaultPodCliqueSet(pcs *grovecorev1beta1.PodCliqueSet) {
	if utils.IsEmptyStringType(pcs.Namespace) {
		pcs.Namespace = metav1.NamespaceDefault
	}
	defaultPodCliqueSetSpec(&pcs.Spec)
}

// defaultPodCliqueSetSpec adds defaults to the specification of a PodCliqueSet.
func defaultPodCliqueSetSpec(spec *grovecorev1beta1.PodCliqueSetSpec) {
	defaultUpdateStrategy(spec)
	defaultPodCliqueSetTemplateSpec(&spec.Template)
}

// defaultUpdateStrategy populates Spec.UpdateStrategy when it is unset and fills in its Type. The
// strategy is behind a pointer, so the kubebuilder default on Type only fires when the pointer is
// already non-nil. This guarantees a non-nil UpdateStrategy with a concrete Type so that downstream
// defaulting and validation always observe the active strategy. The default Type is RollingRecreate.
func defaultUpdateStrategy(pcsSpec *grovecorev1beta1.PodCliqueSetSpec) {
	if pcsSpec.UpdateStrategy == nil {
		pcsSpec.UpdateStrategy = &grovecorev1beta1.PodCliqueSetUpdateStrategy{}
	}
	if pcsSpec.UpdateStrategy.Type == "" {
		pcsSpec.UpdateStrategy.Type = grovecorev1beta1.RollingRecreateStrategy
	}
}

// defaultPodCliqueSetTemplateSpec applies defaults to the template specification including cliques, scaling groups, and service configuration.
func defaultPodCliqueSetTemplateSpec(spec *grovecorev1beta1.PodCliqueSetTemplateSpec) {
	spec.Cliques = defaultPodCliqueTemplateSpecs(spec.Cliques)
	spec.PodCliqueScalingGroupConfigs = defaultPodCliqueScalingGroupConfigs(spec.PodCliqueScalingGroupConfigs)
	if spec.TerminationDelay == nil {
		spec.TerminationDelay = &metav1.Duration{Duration: defaultTerminationDelay}
	}

	spec.HeadlessServiceConfig = defaultHeadlessServiceConfig(spec.HeadlessServiceConfig)
}

// defaultHeadlessServiceConfig applies defaults to the headless service configuration.
func defaultHeadlessServiceConfig(headlessServiceConfig *grovecorev1beta1.HeadlessServiceConfig) *grovecorev1beta1.HeadlessServiceConfig {
	if headlessServiceConfig == nil {
		headlessServiceConfig = &grovecorev1beta1.HeadlessServiceConfig{
			PublishNotReadyAddresses: true,
		}
	}
	return headlessServiceConfig
}

// defaultPodCliqueTemplateSpecs applies defaults to each PodClique template including replicas, minAvailable, and autoscaling configuration.
func defaultPodCliqueTemplateSpecs(cliqueSpecs []*grovecorev1beta1.PodCliqueTemplateSpec) []*grovecorev1beta1.PodCliqueTemplateSpec {
	defaultedCliqueSpecs := make([]*grovecorev1beta1.PodCliqueTemplateSpec, 0, len(cliqueSpecs))
	for _, cliqueSpec := range cliqueSpecs {
		defaultedCliqueSpec := cliqueSpec.DeepCopy()
		defaultedCliqueSpec.Spec.PodSpec = *defaultPodSpec(&cliqueSpec.Spec.PodSpec)
		if defaultedCliqueSpec.Spec.Replicas == 0 {
			defaultedCliqueSpec.Spec.Replicas = defaultReplicas
		}
		if cliqueSpec.Spec.ScaleConfig != nil {
			if cliqueSpec.Spec.ScaleConfig.MinReplicas == nil {
				defaultedCliqueSpec.Spec.ScaleConfig.MinReplicas = ptr.To(ptr.Deref(defaultedCliqueSpec.Spec.MinAvailable, 0))
			}
		}
		defaultedCliqueSpecs = append(defaultedCliqueSpecs, defaultedCliqueSpec)
	}
	return defaultedCliqueSpecs
}

func defaultPodCliqueScalingGroupConfigs(scalingGroupConfigs []grovecorev1beta1.PodCliqueScalingGroupConfig) []grovecorev1beta1.PodCliqueScalingGroupConfig {
	defaultedScalingGroupConfigs := make([]grovecorev1beta1.PodCliqueScalingGroupConfig, 0, len(scalingGroupConfigs))
	for _, scalingGroupConfig := range scalingGroupConfigs {
		defaultedScalingGroupConfig := scalingGroupConfig.DeepCopy()
		// Replicas is already set by kubebuilder default (API server runs before defaulting webhook)
		if scalingGroupConfig.ScaleConfig != nil {
			if scalingGroupConfig.ScaleConfig.MinReplicas == nil {
				defaultedScalingGroupConfig.ScaleConfig.MinReplicas = ptr.To(ptr.Deref(defaultedScalingGroupConfig.MinAvailable, 0))
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
		defaultedPodSpec.TerminationGracePeriodSeconds = ptr.To(defaultTerminationGracePeriodSeconds)
	}
	return defaultedPodSpec
}
