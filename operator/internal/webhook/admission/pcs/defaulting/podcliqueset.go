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
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
)

const (
	defaultReplicas                      int32 = 1
	defaultMinReplicas                   int32 = 1
	defaultTerminationGracePeriodSeconds int64 = 30
)

// defaultingContext carries the request-wide inputs of defaulting.
type defaultingContext struct {
	updateStrategy       grovecorev1beta1.UpdateStrategyType
	pcsgOwnedCliqueNames sets.Set[string]
	// alphaRequest is true when the request was sent as v1alpha1. Such requests keep the v1alpha1
	// default of autoScalingConfig.minReplicas, which is replicas.
	alphaRequest bool
}

// defaultPodCliqueSet adds defaults to a PodCliqueSet. alphaRequest is true when the request was sent as
// v1alpha1.
func defaultPodCliqueSet(pcs *grovecorev1beta1.PodCliqueSet, alphaRequest bool) {
	if utils.IsEmptyStringType(pcs.Namespace) {
		pcs.Namespace = metav1.NamespaceDefault
	}
	_, pcsgOwnedCliqueNames := componentutils.GetExpectedPCLQNamesGroupByOwner(pcs)
	defaultUpdateStrategy(&pcs.Spec)
	dc := defaultingContext{
		updateStrategy:       pcs.Spec.UpdateStrategy.Type,
		pcsgOwnedCliqueNames: pcsgOwnedCliqueNames,
		alphaRequest:         alphaRequest,
	}
	defaultPodCliqueSetTemplateSpec(&pcs.Spec.Template, dc)
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
func defaultPodCliqueSetTemplateSpec(spec *grovecorev1beta1.PodCliqueSetTemplateSpec, dc defaultingContext) {
	spec.Cliques = defaultPodCliqueTemplateSpecs(spec.Cliques, dc)
	spec.PodCliqueScalingGroupConfigs = defaultPodCliqueScalingGroupConfigs(spec.PodCliqueScalingGroupConfigs, dc)
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

// defaultPodCliqueTemplateSpecs applies defaults to each PodClique template including replicas, the
// min-replica fields, and autoscaling configuration.
func defaultPodCliqueTemplateSpecs(cliqueSpecs []*grovecorev1beta1.PodCliqueTemplateSpec, dc defaultingContext) []*grovecorev1beta1.PodCliqueTemplateSpec {
	defaultedCliqueSpecs := make([]*grovecorev1beta1.PodCliqueTemplateSpec, 0, len(cliqueSpecs))
	for _, cliqueSpec := range cliqueSpecs {
		defaultedCliqueSpec := cliqueSpec.DeepCopy()
		defaultedCliqueSpec.Spec.PodSpec = *defaultPodSpec(&cliqueSpec.Spec.PodSpec)
		if defaultedCliqueSpec.Spec.Replicas == 0 {
			defaultedCliqueSpec.Spec.Replicas = defaultReplicas
		}
		defaultedCliqueSpec.Spec.Termination = defaultTermination(defaultedCliqueSpec.Spec.Termination)
		defaultedCliqueSpec.Spec.Init = defaultInit(defaultedCliqueSpec.Spec.Init, defaultedCliqueSpec.Spec.Termination)
		defaultAutoScalingMinReplicas(defaultedCliqueSpec.Spec.ScaleConfig, defaultedCliqueSpec.Spec.Termination, defaultedCliqueSpec.Spec.Replicas, dc.alphaRequest)
		// A standalone PodClique carries its own RollingUpdate. A PCSG-owned PodClique is governed by
		// its PodCliqueScalingGroup, and the validating webhook rejects a RollingUpdate set on it, so
		// it is skipped here.
		if !dc.pcsgOwnedCliqueNames.Has(defaultedCliqueSpec.Name) {
			defaultedCliqueSpec.RollingUpdate = defaultRollingUpdateConfiguration(defaultedCliqueSpec.RollingUpdate, dc.updateStrategy, defaultedCliqueSpec.Spec.Termination)
		}
		defaultedCliqueSpecs = append(defaultedCliqueSpecs, defaultedCliqueSpec)
	}
	return defaultedCliqueSpecs
}

func defaultPodCliqueScalingGroupConfigs(scalingGroupConfigs []grovecorev1beta1.PodCliqueScalingGroupConfig, dc defaultingContext) []grovecorev1beta1.PodCliqueScalingGroupConfig {
	defaultedScalingGroupConfigs := make([]grovecorev1beta1.PodCliqueScalingGroupConfig, 0, len(scalingGroupConfigs))
	for _, scalingGroupConfig := range scalingGroupConfigs {
		defaultedScalingGroupConfig := scalingGroupConfig.DeepCopy()
		// Replicas is already set by kubebuilder default (API server runs before defaulting webhook)
		defaultedScalingGroupConfig.Termination = defaultTermination(defaultedScalingGroupConfig.Termination)
		defaultedScalingGroupConfig.Init = defaultInit(defaultedScalingGroupConfig.Init, defaultedScalingGroupConfig.Termination)
		defaultAutoScalingMinReplicas(defaultedScalingGroupConfig.ScaleConfig, defaultedScalingGroupConfig.Termination, ptr.Deref(defaultedScalingGroupConfig.Replicas, defaultReplicas), dc.alphaRequest)
		defaultedScalingGroupConfig.RollingUpdate = defaultRollingUpdateConfiguration(defaultedScalingGroupConfig.RollingUpdate, dc.updateStrategy, defaultedScalingGroupConfig.Termination)
		defaultedScalingGroupConfigs = append(defaultedScalingGroupConfigs, *defaultedScalingGroupConfig)
	}
	return defaultedScalingGroupConfigs
}

// defaultTermination fills availability.minReplicas (1) and availability.breachTolerance (4h) when
// availability is set. A nil availability stays nil, because it means the component is never in breach.
func defaultTermination(termination *grovecorev1beta1.TerminationSpec) *grovecorev1beta1.TerminationSpec {
	availability := componentutils.Availability(termination)
	if availability == nil {
		return termination
	}
	if availability.MinReplicas == nil {
		availability.MinReplicas = ptr.To(defaultMinReplicas)
	}
	if availability.BreachTolerance == nil {
		availability.BreachTolerance = &metav1.Duration{Duration: componentutils.DefaultBreachTolerance}
	}
	return termination
}

// defaultInit fills init.minReplicas with availability.minReplicas when availability is set, and with 1
// otherwise.
func defaultInit(init *grovecorev1beta1.InitSpec, termination *grovecorev1beta1.TerminationSpec) *grovecorev1beta1.InitSpec {
	if init == nil {
		init = &grovecorev1beta1.InitSpec{}
	}
	if init.MinReplicas == nil {
		init.MinReplicas = ptr.To(componentutils.DefaultUpdateMinReplicas(termination))
	}
	return init
}

// defaultAutoScalingMinReplicas fills autoScalingConfig.minReplicas with the scale floor:
// availability.minReplicas when availability is set, and 1 otherwise. A request sent as v1alpha1 keeps the
// v1alpha1 default of replicas.
func defaultAutoScalingMinReplicas(scaleConfig *grovecorev1beta1.AutoScalingConfig, termination *grovecorev1beta1.TerminationSpec, replicas int32, alphaRequest bool) {
	if scaleConfig == nil || scaleConfig.MinReplicas != nil {
		return
	}
	if alphaRequest {
		scaleConfig.MinReplicas = ptr.To(replicas)
		return
	}
	scaleConfig.MinReplicas = ptr.To(componentutils.ScaleFloor(termination))
}

// defaultRollingUpdateConfiguration fills the missing rollingUpdate fields for the rolling update
// strategies (Coherent, RollingRecreate). It never clears a consumer-populated RollingUpdate. For OnDelete
// the validating webhook rejects a set RollingUpdate, so removing it is left to the consumer. Under
// Coherent, minReplicas defaults to availability.minReplicas when availability is set and to 1 otherwise,
// and MaxUnavailable defaults to minReplicas, since the MVU sub-step takes down minReplicas of the
// component at once and that is the smallest internally consistent budget. RollingRecreate defaults
// MaxUnavailable to 1 and never sets minReplicas. MaxUnavailable is always written as an integer.
// ProgressDeadline is never defaulted, a nil value opts out of the deadline.
func defaultRollingUpdateConfiguration(existing *grovecorev1beta1.RollingUpdateConfiguration, updateStrategy grovecorev1beta1.UpdateStrategyType, termination *grovecorev1beta1.TerminationSpec) *grovecorev1beta1.RollingUpdateConfiguration {
	switch updateStrategy {
	case grovecorev1beta1.CoherentStrategy, grovecorev1beta1.RollingRecreateStrategy:
	default: // OnDelete or unknown, no defaulting
		return existing
	}
	if existing == nil {
		existing = &grovecorev1beta1.RollingUpdateConfiguration{}
	}
	if updateStrategy == grovecorev1beta1.CoherentStrategy {
		if existing.MinReplicas == nil {
			existing.MinReplicas = ptr.To(componentutils.DefaultUpdateMinReplicas(termination))
		}
		if existing.MaxUnavailable == nil {
			existing.MaxUnavailable = ptr.To(intstr.FromInt32(*existing.MinReplicas))
		}
		return existing
	}
	if existing.MaxUnavailable == nil {
		existing.MaxUnavailable = ptr.To(intstr.FromInt32(componentutils.DefaultRollingRecreateMaxUnavailable))
	}
	return existing
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
