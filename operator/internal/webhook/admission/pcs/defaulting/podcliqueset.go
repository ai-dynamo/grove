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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
)

const (
	defaultTerminationDelay                    = 4 * time.Hour
	defaultReplicas                      int32 = 1
	defaultTerminationGracePeriodSeconds int64 = 30
)

// defaultPodCliqueSet adds defaults to a PodCliqueSet.
func defaultPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) {
	if utils.IsEmptyStringType(pcs.Namespace) {
		pcs.Namespace = metav1.NamespaceDefault
	}
	_, pcsgOwnedCliqueNames := componentutils.GetExpectedPCLQNamesGroupByOwner(pcs)
	defaultPodCliqueSetSpec(&pcs.Spec, pcsgOwnedCliqueNames)
}

// defaultPodCliqueSetSpec adds defaults to the specification of a PodCliqueSet.
func defaultPodCliqueSetSpec(spec *grovecorev1alpha1.PodCliqueSetSpec, pcsgOwnedCliqueNames sets.Set[string]) {
	defaultUpdateStrategy(spec)
	defaultPodCliqueSetTemplateSpec(&spec.Template, spec.UpdateStrategy.Type, pcsgOwnedCliqueNames)
}

// defaultUpdateStrategy populates Spec.UpdateStrategy when it is unset and fills in its Type. The
// strategy is behind a pointer, so the kubebuilder default on Type only fires when the pointer is
// already non-nil. This guarantees a non-nil UpdateStrategy with a concrete Type so that downstream
// defaulting and validation always observe the active strategy. The default Type is RollingRecreate.
func defaultUpdateStrategy(pcsSpec *grovecorev1alpha1.PodCliqueSetSpec) {
	if pcsSpec.UpdateStrategy == nil {
		pcsSpec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{}
	}
	if pcsSpec.UpdateStrategy.Type == "" {
		pcsSpec.UpdateStrategy.Type = grovecorev1alpha1.RollingRecreateStrategy
	}
}

// defaultPodCliqueSetTemplateSpec applies defaults to the template specification including cliques, scaling groups, and service configuration.
func defaultPodCliqueSetTemplateSpec(spec *grovecorev1alpha1.PodCliqueSetTemplateSpec, updateStrategy grovecorev1alpha1.UpdateStrategyType, pcsgOwnedCliqueNames sets.Set[string]) {
	spec.Cliques = defaultPodCliqueTemplateSpecs(spec.Cliques, updateStrategy, pcsgOwnedCliqueNames)
	spec.PodCliqueScalingGroupConfigs = defaultPodCliqueScalingGroupConfigs(spec.PodCliqueScalingGroupConfigs, updateStrategy)
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

// defaultPodCliqueTemplateSpecs applies defaults to each PodClique template including replicas, minAvailable, and autoscaling configuration.
func defaultPodCliqueTemplateSpecs(cliqueSpecs []*grovecorev1alpha1.PodCliqueTemplateSpec, updateStrategy grovecorev1alpha1.UpdateStrategyType, pcsgOwnedCliqueNames sets.Set[string]) []*grovecorev1alpha1.PodCliqueTemplateSpec {
	defaultedCliqueSpecs := make([]*grovecorev1alpha1.PodCliqueTemplateSpec, 0, len(cliqueSpecs))
	for _, cliqueSpec := range cliqueSpecs {
		defaultedCliqueSpec := cliqueSpec.DeepCopy()
		defaultedCliqueSpec.Spec.PodSpec = *defaultPodSpec(&cliqueSpec.Spec.PodSpec)
		if defaultedCliqueSpec.Spec.Replicas == 0 {
			defaultedCliqueSpec.Spec.Replicas = defaultReplicas
		}
		if cliqueSpec.Spec.MinAvailable == nil {
			defaultedCliqueSpec.Spec.MinAvailable = ptr.To(cliqueSpec.Spec.Replicas)
		}
		if cliqueSpec.Spec.ScaleConfig != nil {
			if cliqueSpec.Spec.ScaleConfig.MinReplicas == nil {
				defaultedCliqueSpec.Spec.ScaleConfig.MinReplicas = ptr.To(cliqueSpec.Spec.Replicas)
			}
		}
		// A standalone PodClique carries its own RollingUpdate. A PCSG-owned PodClique is governed by
		// its PodCliqueScalingGroup, and the validating webhook rejects a RollingUpdate set on it, so
		// it is skipped here.
		if !pcsgOwnedCliqueNames.Has(defaultedCliqueSpec.Name) {
			defaultedCliqueSpec.RollingUpdate = defaultRollingUpdateConfiguration(defaultedCliqueSpec.RollingUpdate, updateStrategy)
		}
		defaultedCliqueSpecs = append(defaultedCliqueSpecs, defaultedCliqueSpec)
	}
	return defaultedCliqueSpecs
}

func defaultPodCliqueScalingGroupConfigs(scalingGroupConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig, updateStrategy grovecorev1alpha1.UpdateStrategyType) []grovecorev1alpha1.PodCliqueScalingGroupConfig {
	defaultedScalingGroupConfigs := make([]grovecorev1alpha1.PodCliqueScalingGroupConfig, 0, len(scalingGroupConfigs))
	for _, scalingGroupConfig := range scalingGroupConfigs {
		defaultedScalingGroupConfig := scalingGroupConfig.DeepCopy()
		// Replicas is already set by kubebuilder default (API server runs before defaulting webhook)
		if scalingGroupConfig.ScaleConfig != nil {
			if scalingGroupConfig.ScaleConfig.MinReplicas == nil {
				defaultedScalingGroupConfig.ScaleConfig.MinReplicas = ptr.To(*defaultedScalingGroupConfig.Replicas)
			}
		}
		defaultedScalingGroupConfig.RollingUpdate = defaultRollingUpdateConfiguration(defaultedScalingGroupConfig.RollingUpdate, updateStrategy)
		defaultedScalingGroupConfigs = append(defaultedScalingGroupConfigs, *defaultedScalingGroupConfig)
	}
	return defaultedScalingGroupConfigs
}

// defaultRollingUpdateConfiguration returns the RollingUpdateConfiguration normalized for the active
// update strategy. RollingUpdate only applies to RollingRecreate, so it is cleared for any other
// strategy, which keeps a transition away from RollingRecreate from leaving a stale configuration that
// the validating webhook would then reject. For RollingRecreate an existing MaxUnavailable is
// preserved and a missing one is defaulted to 1. ProgressDeadline is never defaulted, a nil value opts
// out of the deadline.
func defaultRollingUpdateConfiguration(existing *grovecorev1alpha1.RollingUpdateConfiguration, updateStrategy grovecorev1alpha1.UpdateStrategyType) *grovecorev1alpha1.RollingUpdateConfiguration {
	// Defaulting only fills a hole for the rolling update strategies. It never clears a
	// consumer-populated RollingUpdate. For OnDelete the validating webhook rejects a RollingUpdate
	// that is set, so removing it is left to the consumer rather than silently dropped here.
	if updateStrategy != grovecorev1alpha1.RollingRecreateStrategy {
		return existing
	}
	if existing != nil && existing.MaxUnavailable != nil {
		return existing
	}
	defaulted := existing
	if defaulted == nil {
		defaulted = &grovecorev1alpha1.RollingUpdateConfiguration{}
	}
	defaulted.MaxUnavailable = ptr.To(componentutils.DefaultRollingRecreateMaxUnavailable)
	return defaulted
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
