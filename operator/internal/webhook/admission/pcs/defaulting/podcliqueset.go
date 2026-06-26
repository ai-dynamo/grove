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
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
)

const (
	defaultTerminationDelay = 4 * time.Hour
)

// defaultPodCliqueSet adds defaults to a PodCliqueSet.
func defaultPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) {
	if utils.IsEmptyStringType(pcs.Namespace) {
		pcs.Namespace = "default"
	}
	defaultUpdateStrategy(pcs)
	defaultPodCliqueSetTemplateSpec(pcs)
}

// defaultUpdateStrategy populates Spec.UpdateStrategy when unset and fills in its Type.
// Coherent is the default per GREP-393. Downstream defaulting and validation rely on a
// non-nil strategy pointer with a non-empty Type.
func defaultUpdateStrategy(pcs *grovecorev1alpha1.PodCliqueSet) {
	if pcs.Spec.UpdateStrategy == nil {
		pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{}
	}
	if pcs.Spec.UpdateStrategy.Type == "" {
		pcs.Spec.UpdateStrategy.Type = grovecorev1alpha1.CoherentStrategy
	}
}

// defaultPodCliqueSetTemplateSpec applies defaults to the template specification including cliques, scaling groups, and service configuration.
func defaultPodCliqueSetTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet) {
	spec := &pcs.Spec.Template
	pcsgOwnedCliqueNames := componentutils.GetPCSGOwnedCliqueNames(pcs)
	spec.Cliques = defaultPodCliqueTemplateSpecs(spec.Cliques, pcs.Spec.UpdateStrategy.Type, pcsgOwnedCliqueNames)
	spec.PodCliqueScalingGroupConfigs = defaultPodCliqueScalingGroupConfigs(spec.PodCliqueScalingGroupConfigs, pcs.Spec.UpdateStrategy.Type)
	if spec.TerminationDelay == nil {
		spec.TerminationDelay = &metav1.Duration{Duration: defaultTerminationDelay}
	}

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

// defaultPodCliqueTemplateSpecs applies defaults to each PodClique template including replicas, minAvailable, autoscaling configuration, and (for standalone PodCliques) rollingUpdate.
// PCSG-owned templates are skipped for RollingUpdate defaulting because the validating webhook rejects RollingUpdate on those templates.
func defaultPodCliqueTemplateSpecs(cliqueSpecs []*grovecorev1alpha1.PodCliqueTemplateSpec, strategy grovecorev1alpha1.UpdateStrategyType, pcsgOwnedCliqueNames sets.Set[string]) []*grovecorev1alpha1.PodCliqueTemplateSpec {
	defaultedCliqueSpecs := make([]*grovecorev1alpha1.PodCliqueTemplateSpec, 0, len(cliqueSpecs))
	for _, cliqueSpec := range cliqueSpecs {
		defaultedCliqueSpec := cliqueSpec.DeepCopy()
		defaultedCliqueSpec.Spec.PodSpec = *defaultPodSpec(&cliqueSpec.Spec.PodSpec)
		if defaultedCliqueSpec.Spec.Replicas == 0 {
			defaultedCliqueSpec.Spec.Replicas = 1
		}
		if cliqueSpec.Spec.MinAvailable == nil {
			defaultedCliqueSpec.Spec.MinAvailable = ptr.To(defaultedCliqueSpec.Spec.Replicas)
		}
		if cliqueSpec.Spec.ScaleConfig != nil {
			if cliqueSpec.Spec.ScaleConfig.MinReplicas == nil {
				defaultedCliqueSpec.Spec.ScaleConfig.MinReplicas = ptr.To(cliqueSpec.Spec.Replicas)
			}
		}
		if !pcsgOwnedCliqueNames.Has(defaultedCliqueSpec.Name) && defaultedCliqueSpec.Spec.MinAvailable != nil {
			defaultedCliqueSpec.RollingUpdate = defaultRollingUpdateConfiguration(defaultedCliqueSpec.RollingUpdate, strategy, *defaultedCliqueSpec.Spec.MinAvailable)
		}
		defaultedCliqueSpecs = append(defaultedCliqueSpecs, defaultedCliqueSpec)
	}
	return defaultedCliqueSpecs
}

// defaultPodCliqueScalingGroupConfigs applies defaults to scaling group configurations including rollingUpdate.
// Note: Replicas field is already set by kubebuilder defaults before the webhook runs.
func defaultPodCliqueScalingGroupConfigs(scalingGroupConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig, strategy grovecorev1alpha1.UpdateStrategyType) []grovecorev1alpha1.PodCliqueScalingGroupConfig {
	defaultedScalingGroupConfigs := make([]grovecorev1alpha1.PodCliqueScalingGroupConfig, 0, len(scalingGroupConfigs))
	for _, scalingGroupConfig := range scalingGroupConfigs {
		defaultedScalingGroupConfig := scalingGroupConfig.DeepCopy()
		// Replicas is already set by kubebuilder default (API server runs before defaulting webhook)
		if scalingGroupConfig.ScaleConfig != nil {
			if scalingGroupConfig.ScaleConfig.MinReplicas == nil {
				defaultedScalingGroupConfig.ScaleConfig.MinReplicas = ptr.To(*defaultedScalingGroupConfig.Replicas)
			}
		}
		if defaultedScalingGroupConfig.MinAvailable != nil {
			defaultedScalingGroupConfig.RollingUpdate = defaultRollingUpdateConfiguration(defaultedScalingGroupConfig.RollingUpdate, strategy, *defaultedScalingGroupConfig.MinAvailable)
		}
		defaultedScalingGroupConfigs = append(defaultedScalingGroupConfigs, *defaultedScalingGroupConfig)
	}
	return defaultedScalingGroupConfigs
}

// defaultRollingUpdateConfiguration returns a RollingUpdateConfiguration with MaxUnavailable
// defaulted per the active update strategy. An existing non-nil MaxUnavailable is preserved.
// Coherent defaults MaxUnavailable to minAvailable; RollingRecreate defaults it to 1; other
// strategies (including OnDelete) are returned unchanged.
func defaultRollingUpdateConfiguration(existing *grovecorev1alpha1.RollingUpdateConfiguration, strategy grovecorev1alpha1.UpdateStrategyType, minAvailable int32) *grovecorev1alpha1.RollingUpdateConfiguration {
	if existing != nil && existing.MaxUnavailable != nil {
		return existing
	}
	if strategy != grovecorev1alpha1.CoherentStrategy && strategy != grovecorev1alpha1.RollingRecreateStrategy {
		return existing
	}
	defaultedUpdateConfig := existing.DeepCopy()
	if defaultedUpdateConfig == nil {
		defaultedUpdateConfig = &grovecorev1alpha1.RollingUpdateConfiguration{}
	}
	switch strategy {
	case grovecorev1alpha1.CoherentStrategy:
		defaultedUpdateConfig.MaxUnavailable = ptr.To(minAvailable)
	case grovecorev1alpha1.RollingRecreateStrategy:
		defaultedUpdateConfig.MaxUnavailable = ptr.To[int32](1)
	}
	return defaultedUpdateConfig
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
