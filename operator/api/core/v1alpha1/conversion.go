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

package v1alpha1

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// ConversionDataAnnotation holds the v1beta1 values that the v1alpha1 representation of an object cannot express.
// It is written only into v1alpha1 representations, so that a v1alpha1 read-modify-write keeps them, and it is
// removed again when the object is converted to v1beta1.
const ConversionDataAnnotation = "grove.io/v1beta1-conversion-data"

// defaultTerminationDelay is the v1alpha1 default of template.terminationDelay.
const defaultTerminationDelay = 4 * time.Hour

// conversionData is the content of ConversionDataAnnotation.
type conversionData struct {
	// TerminationDelay is the template.terminationDelay that the conversion to v1alpha1 wrote.
	TerminationDelay *metav1.Duration `json:"terminationDelay,omitempty"`
	// Cliques holds the clique templates of a PodCliqueSet, by name.
	Cliques map[string]*componentData `json:"cliques,omitempty"`
	// ScalingGroups holds the PodCliqueScalingGroup configs of a PodCliqueSet, by name.
	ScalingGroups map[string]*componentData `json:"podCliqueScalingGroups,omitempty"`
	// Self holds the values of a PodClique or PodCliqueScalingGroup object.
	Self *componentData `json:"self,omitempty"`
}

// componentData holds the lost values of one component. Each group is restored only while the v1alpha1 values it
// was written for are unchanged.
type componentData struct {
	MinReplicas           *minReplicasData   `json:"minReplicas,omitempty"`
	RollingUpdate         *rollingUpdateData `json:"rollingUpdate,omitempty"`
	StartsAfterConditions map[string]string  `json:"startsAfterConditions,omitempty"`
}

// minReplicasData holds init and termination, which v1alpha1 expresses as minAvailable and terminationDelay.
type minReplicasData struct {
	MinAvailable int32                    `json:"minAvailable"`
	Init         *v1beta1.InitSpec        `json:"init,omitempty"`
	Termination  *v1beta1.TerminationSpec `json:"termination,omitempty"`
}

// rollingUpdateData holds the v1beta1 rolling update configuration, which depends on minAvailable, the update
// strategy and the v1alpha1 rolling update configuration.
type rollingUpdateData struct {
	Strategy     UpdateStrategyType                  `json:"strategy"`
	MinAvailable int32                               `json:"minAvailable"`
	Written      *RollingUpdateConfiguration         `json:"written,omitempty"`
	Original     *v1beta1.RollingUpdateConfiguration `json:"original,omitempty"`
}

func (d *componentData) empty() bool {
	return d.MinReplicas == nil && d.RollingUpdate == nil && len(d.StartsAfterConditions) == 0
}

// componentContext is what the conversion of a component needs from the object that holds it.
type componentContext struct {
	// template is true for clique templates and PCSG configs, which carry a rolling update configuration and get
	// their breach tolerance from the PodCliqueSet's terminationDelay.
	template bool
	strategy UpdateStrategyType
	// breachTolerance is the v1alpha1 terminationDelay, resolved, for template components.
	breachTolerance *metav1.Duration
	// toleranceChanged is true when terminationDelay differs from what the conversion to v1alpha1 wrote.
	toleranceChanged bool
	// replicas is the template's replica count, which a percentage maxUnavailable is resolved against.
	replicas int32
}

// alphaComponent and betaComponent point at the fields of one component that need context to convert.
type alphaComponent struct {
	minAvailable  **int32
	rollingUpdate **RollingUpdateConfiguration
	startsAfter   []string
}

type betaComponent struct {
	init          **v1beta1.InitSpec
	termination   **v1beta1.TerminationSpec
	rollingUpdate **v1beta1.RollingUpdateConfiguration
	startsAfter   []v1beta1.StartupDependency
}

// deriveBeta sets the v1beta1 fields of a component from its v1alpha1 fields alone, as the migration table defines.
func deriveBeta(a alphaComponent, b betaComponent, ctx componentContext) {
	minAvailable := ptr.Deref(*a.minAvailable, 1)
	*b.init = &v1beta1.InitSpec{MinReplicas: ptr.To(minAvailable)}
	availability := &v1beta1.AvailabilityPolicy{MinReplicas: ptr.To(minAvailable)}
	if ctx.template {
		availability.BreachTolerance = ctx.breachTolerance.DeepCopy()
	}
	*b.termination = &v1beta1.TerminationSpec{Resource: &v1beta1.ResourceTerminationSpec{Availability: availability}}
	if b.rollingUpdate != nil {
		ru := convertRollingUpdateToBeta(*a.rollingUpdate)
		if ctx.strategy == CoherentStrategy {
			if ru == nil {
				ru = &v1beta1.RollingUpdateConfiguration{}
			}
			ru.MinReplicas = ptr.To(minAvailable)
		}
		*b.rollingUpdate = ru
	}
	for i := range b.startsAfter {
		b.startsAfter[i].ConditionType = string(corev1.PodReady)
	}
}

// restoreBeta derives the v1beta1 fields of a component and overlays the stashed values whose v1alpha1 inputs are
// unchanged.
func restoreBeta(a alphaComponent, b betaComponent, ctx componentContext, data *componentData) {
	deriveBeta(a, b, ctx)
	if data == nil {
		return
	}
	minAvailable := ptr.Deref(*a.minAvailable, 1)
	if d := data.MinReplicas; d != nil && d.MinAvailable == minAvailable {
		*b.init = d.Init.DeepCopy()
		termination := d.Termination.DeepCopy()
		if ctx.template && ctx.toleranceChanged && termination != nil && termination.Resource != nil && termination.Resource.Availability != nil {
			termination.Resource.Availability.BreachTolerance = ctx.breachTolerance.DeepCopy()
		}
		*b.termination = termination
	}
	if d := data.RollingUpdate; b.rollingUpdate != nil && d != nil && d.Strategy == ctx.strategy && d.MinAvailable == minAvailable &&
		equality.Semantic.DeepEqual(*a.rollingUpdate, d.Written) {
		*b.rollingUpdate = d.Original.DeepCopy()
	}
	for i := range b.startsAfter {
		if conditionType, ok := data.StartsAfterConditions[b.startsAfter[i].Name]; ok {
			b.startsAfter[i].ConditionType = conditionType
		}
	}
}

// convertComponentToAlpha sets the v1alpha1 fields of a component, and returns the values that the v1alpha1 form
// cannot express, or nil when there are none.
func convertComponentToAlpha(b betaComponent, a alphaComponent, ctx componentContext) *componentData {
	minAvailable := int32(1)
	if *b.init != nil && (*b.init).MinReplicas != nil {
		minAvailable = *(*b.init).MinReplicas
	}
	*a.minAvailable = ptr.To(minAvailable)
	if b.rollingUpdate != nil {
		*a.rollingUpdate = convertRollingUpdateToAlpha(*b.rollingUpdate, ctx, minAvailable)
	}

	// Derive the v1beta1 form back from the v1alpha1 form, and keep what differs.
	derived := struct {
		init          *v1beta1.InitSpec
		termination   *v1beta1.TerminationSpec
		rollingUpdate *v1beta1.RollingUpdateConfiguration
	}{}
	derivedStartsAfter := make([]v1beta1.StartupDependency, len(b.startsAfter))
	for i := range b.startsAfter {
		derivedStartsAfter[i].Name = b.startsAfter[i].Name
	}
	derivedComponent := betaComponent{init: &derived.init, termination: &derived.termination, startsAfter: derivedStartsAfter}
	if b.rollingUpdate != nil {
		derivedComponent.rollingUpdate = &derived.rollingUpdate
	}
	deriveBeta(a, derivedComponent, ctx)

	data := &componentData{}
	if !equality.Semantic.DeepEqual(*b.init, derived.init) || !equality.Semantic.DeepEqual(*b.termination, derived.termination) {
		data.MinReplicas = &minReplicasData{MinAvailable: minAvailable, Init: (*b.init).DeepCopy(), Termination: (*b.termination).DeepCopy()}
	}
	if b.rollingUpdate != nil && !equality.Semantic.DeepEqual(*b.rollingUpdate, derived.rollingUpdate) {
		data.RollingUpdate = &rollingUpdateData{
			Strategy:     ctx.strategy,
			MinAvailable: minAvailable,
			Written:      (*a.rollingUpdate).DeepCopy(),
			Original:     (*b.rollingUpdate).DeepCopy(),
		}
	}
	for _, dependency := range b.startsAfter {
		if dependency.ConditionType != string(corev1.PodReady) {
			if data.StartsAfterConditions == nil {
				data.StartsAfterConditions = map[string]string{}
			}
			data.StartsAfterConditions[dependency.Name] = dependency.ConditionType
		}
	}
	if data.empty() {
		return nil
	}
	return data
}

func convertRollingUpdateToBeta(in *RollingUpdateConfiguration) *v1beta1.RollingUpdateConfiguration {
	if in == nil {
		return nil
	}
	out := &v1beta1.RollingUpdateConfiguration{ProgressDeadline: in.ProgressDeadline.DeepCopy()}
	if in.MaxUnavailable != nil {
		out.MaxUnavailable = ptr.To(intstr.FromInt32(*in.MaxUnavailable))
	}
	return out
}

// convertRollingUpdateToAlpha resolves a percentage maxUnavailable against the template's replicas, rounding down
// with a minimum of 1. Under Coherent the result is raised to minAvailable, which v1alpha1 validation requires.
func convertRollingUpdateToAlpha(in *v1beta1.RollingUpdateConfiguration, ctx componentContext, minAvailable int32) *RollingUpdateConfiguration {
	if in == nil {
		return nil
	}
	out := &RollingUpdateConfiguration{ProgressDeadline: in.ProgressDeadline.DeepCopy()}
	if in.MaxUnavailable != nil {
		maxUnavailable, err := intstr.GetScaledValueFromIntOrPercent(in.MaxUnavailable, int(ctx.replicas), false)
		if err != nil {
			maxUnavailable = 1
		}
		maxUnavailable = max(maxUnavailable, 1)
		if ctx.strategy == CoherentStrategy {
			maxUnavailable = max(maxUnavailable, int(minAvailable))
		}
		out.MaxUnavailable = ptr.To(int32(maxUnavailable))
	}
	if out.MaxUnavailable == nil && out.ProgressDeadline == nil {
		return nil
	}
	return out
}

func readConversionData(annotations map[string]string) (*conversionData, error) {
	raw, ok := annotations[ConversionDataAnnotation]
	if !ok {
		return &conversionData{}, nil
	}
	data := &conversionData{}
	if err := json.Unmarshal([]byte(raw), data); err != nil {
		return nil, fmt.Errorf("failed to decode annotation %s: %w", ConversionDataAnnotation, err)
	}
	return data, nil
}

// withoutConversionData returns a copy of annotations without ConversionDataAnnotation.
func withoutConversionData(annotations map[string]string) map[string]string {
	if _, ok := annotations[ConversionDataAnnotation]; !ok {
		return annotations
	}
	out := make(map[string]string, len(annotations)-1)
	for k, v := range annotations {
		if k != ConversionDataAnnotation {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// withConversionData returns a copy of annotations with ConversionDataAnnotation set to data, or without it when
// data holds nothing.
func withConversionData(annotations map[string]string, data *conversionData) (map[string]string, error) {
	annotations = withoutConversionData(annotations)
	if data.TerminationDelay == nil && len(data.Cliques) == 0 && len(data.ScalingGroups) == 0 && data.Self == nil {
		return annotations, nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to encode annotation %s: %w", ConversionDataAnnotation, err)
	}
	out := make(map[string]string, len(annotations)+1)
	for k, v := range annotations {
		out[k] = v
	}
	out[ConversionDataAnnotation] = string(raw)
	return out, nil
}

func strategyOf(updateStrategy *PodCliqueSetUpdateStrategy) UpdateStrategyType {
	if updateStrategy == nil || updateStrategy.Type == "" {
		return RollingRecreateStrategy
	}
	return updateStrategy.Type
}

// Convert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet converts a PodCliqueSet to v1beta1.
func Convert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet(in *PodCliqueSet, out *v1beta1.PodCliqueSet, s conversion.Scope) error {
	if err := autoConvert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet(in, out, s); err != nil {
		return err
	}
	data, err := readConversionData(in.Annotations)
	if err != nil {
		return err
	}
	out.Annotations = withoutConversionData(in.Annotations)

	breachTolerance := in.Spec.Template.TerminationDelay.DeepCopy()
	if breachTolerance == nil {
		breachTolerance = &metav1.Duration{Duration: defaultTerminationDelay}
	}
	ctx := componentContext{
		template:         true,
		strategy:         strategyOf(in.Spec.UpdateStrategy),
		breachTolerance:  breachTolerance,
		toleranceChanged: !equality.Semantic.DeepEqual(in.Spec.Template.TerminationDelay, data.TerminationDelay),
	}
	for i, clique := range in.Spec.Template.Cliques {
		if clique == nil {
			continue
		}
		outClique := out.Spec.Template.Cliques[i]
		restoreBeta(
			alphaComponent{minAvailable: &clique.Spec.MinAvailable, rollingUpdate: &clique.RollingUpdate, startsAfter: clique.Spec.StartsAfter},
			betaComponent{init: &outClique.Spec.Init, termination: &outClique.Spec.Termination, rollingUpdate: &outClique.RollingUpdate, startsAfter: outClique.Spec.StartsAfter},
			ctx, data.Cliques[clique.Name])
	}
	for i := range in.Spec.Template.PodCliqueScalingGroupConfigs {
		config := &in.Spec.Template.PodCliqueScalingGroupConfigs[i]
		outConfig := &out.Spec.Template.PodCliqueScalingGroupConfigs[i]
		restoreBeta(
			alphaComponent{minAvailable: &config.MinAvailable, rollingUpdate: &config.RollingUpdate},
			betaComponent{init: &outConfig.Init, termination: &outConfig.Termination, rollingUpdate: &outConfig.RollingUpdate},
			ctx, data.ScalingGroups[config.Name])
	}
	return nil
}

// Convert_v1beta1_PodCliqueSet_To_v1alpha1_PodCliqueSet converts a PodCliqueSet to v1alpha1.
func Convert_v1beta1_PodCliqueSet_To_v1alpha1_PodCliqueSet(in *v1beta1.PodCliqueSet, out *PodCliqueSet, s conversion.Scope) error {
	if err := autoConvert_v1beta1_PodCliqueSet_To_v1alpha1_PodCliqueSet(in, out, s); err != nil {
		return err
	}
	data := &conversionData{TerminationDelay: commonBreachTolerance(&in.Spec.Template)}
	out.Spec.Template.TerminationDelay = data.TerminationDelay.DeepCopy()

	breachTolerance := data.TerminationDelay.DeepCopy()
	if breachTolerance == nil {
		breachTolerance = &metav1.Duration{Duration: defaultTerminationDelay}
	}
	strategy := strategyOf(out.Spec.UpdateStrategy)
	for i, clique := range in.Spec.Template.Cliques {
		if clique == nil {
			continue
		}
		outClique := out.Spec.Template.Cliques[i]
		ctx := componentContext{template: true, strategy: strategy, breachTolerance: breachTolerance, replicas: clique.Spec.Replicas}
		if d := convertComponentToAlpha(
			betaComponent{init: &clique.Spec.Init, termination: &clique.Spec.Termination, rollingUpdate: &clique.RollingUpdate, startsAfter: clique.Spec.StartsAfter},
			alphaComponent{minAvailable: &outClique.Spec.MinAvailable, rollingUpdate: &outClique.RollingUpdate, startsAfter: outClique.Spec.StartsAfter},
			ctx); d != nil {
			if data.Cliques == nil {
				data.Cliques = map[string]*componentData{}
			}
			data.Cliques[clique.Name] = d
		}
	}
	for i := range in.Spec.Template.PodCliqueScalingGroupConfigs {
		config := &in.Spec.Template.PodCliqueScalingGroupConfigs[i]
		outConfig := &out.Spec.Template.PodCliqueScalingGroupConfigs[i]
		ctx := componentContext{template: true, strategy: strategy, breachTolerance: breachTolerance, replicas: ptr.Deref(config.Replicas, 1)}
		if d := convertComponentToAlpha(
			betaComponent{init: &config.Init, termination: &config.Termination, rollingUpdate: &config.RollingUpdate},
			alphaComponent{minAvailable: &outConfig.MinAvailable, rollingUpdate: &outConfig.RollingUpdate},
			ctx); d != nil {
			if data.ScalingGroups == nil {
				data.ScalingGroups = map[string]*componentData{}
			}
			data.ScalingGroups[config.Name] = d
		}
	}
	if len(data.Cliques) == 0 && len(data.ScalingGroups) == 0 {
		data.TerminationDelay = nil
	}
	annotations, err := withConversionData(in.Annotations, data)
	if err != nil {
		return err
	}
	out.Annotations = annotations
	return nil
}

// commonBreachTolerance returns the breach tolerance that the components with an availability policy share, or the
// maximum when they differ. An unset tolerance counts as the default. It returns nil when no component sets one.
func commonBreachTolerance(template *v1beta1.PodCliqueSetTemplateSpec) *metav1.Duration {
	var tolerances []*metav1.Duration
	collect := func(termination *v1beta1.TerminationSpec) {
		if termination != nil && termination.Resource != nil && termination.Resource.Availability != nil {
			tolerances = append(tolerances, termination.Resource.Availability.BreachTolerance)
		}
	}
	for _, clique := range template.Cliques {
		if clique != nil {
			collect(clique.Spec.Termination)
		}
	}
	for i := range template.PodCliqueScalingGroupConfigs {
		collect(template.PodCliqueScalingGroupConfigs[i].Termination)
	}
	var result *metav1.Duration
	for _, tolerance := range tolerances {
		if tolerance == nil {
			tolerance = &metav1.Duration{Duration: defaultTerminationDelay}
		}
		if result == nil || tolerance.Duration > result.Duration {
			result = tolerance.DeepCopy()
		}
	}
	allUnset := true
	for _, tolerance := range tolerances {
		if tolerance != nil {
			allUnset = false
		}
	}
	if allUnset {
		return nil
	}
	return result
}

// Convert_v1alpha1_PodClique_To_v1beta1_PodClique converts a PodClique to v1beta1.
func Convert_v1alpha1_PodClique_To_v1beta1_PodClique(in *PodClique, out *v1beta1.PodClique, s conversion.Scope) error {
	if err := autoConvert_v1alpha1_PodClique_To_v1beta1_PodClique(in, out, s); err != nil {
		return err
	}
	data, err := readConversionData(in.Annotations)
	if err != nil {
		return err
	}
	out.Annotations = withoutConversionData(in.Annotations)
	restoreBeta(
		alphaComponent{minAvailable: &in.Spec.MinAvailable, startsAfter: in.Spec.StartsAfter},
		betaComponent{init: &out.Spec.Init, termination: &out.Spec.Termination, startsAfter: out.Spec.StartsAfter},
		componentContext{}, data.Self)
	return nil
}

// Convert_v1beta1_PodClique_To_v1alpha1_PodClique converts a PodClique to v1alpha1.
func Convert_v1beta1_PodClique_To_v1alpha1_PodClique(in *v1beta1.PodClique, out *PodClique, s conversion.Scope) error {
	if err := autoConvert_v1beta1_PodClique_To_v1alpha1_PodClique(in, out, s); err != nil {
		return err
	}
	data := &conversionData{Self: convertComponentToAlpha(
		betaComponent{init: &in.Spec.Init, termination: &in.Spec.Termination, startsAfter: in.Spec.StartsAfter},
		alphaComponent{minAvailable: &out.Spec.MinAvailable, startsAfter: out.Spec.StartsAfter},
		componentContext{})}
	annotations, err := withConversionData(in.Annotations, data)
	if err != nil {
		return err
	}
	out.Annotations = annotations
	return nil
}

// Convert_v1alpha1_PodCliqueScalingGroup_To_v1beta1_PodCliqueScalingGroup converts a PodCliqueScalingGroup to v1beta1.
func Convert_v1alpha1_PodCliqueScalingGroup_To_v1beta1_PodCliqueScalingGroup(in *PodCliqueScalingGroup, out *v1beta1.PodCliqueScalingGroup, s conversion.Scope) error {
	if err := autoConvert_v1alpha1_PodCliqueScalingGroup_To_v1beta1_PodCliqueScalingGroup(in, out, s); err != nil {
		return err
	}
	data, err := readConversionData(in.Annotations)
	if err != nil {
		return err
	}
	out.Annotations = withoutConversionData(in.Annotations)
	restoreBeta(
		alphaComponent{minAvailable: &in.Spec.MinAvailable},
		betaComponent{init: &out.Spec.Init, termination: &out.Spec.Termination},
		componentContext{}, data.Self)
	return nil
}

// Convert_v1beta1_PodCliqueScalingGroup_To_v1alpha1_PodCliqueScalingGroup converts a PodCliqueScalingGroup to v1alpha1.
func Convert_v1beta1_PodCliqueScalingGroup_To_v1alpha1_PodCliqueScalingGroup(in *v1beta1.PodCliqueScalingGroup, out *PodCliqueScalingGroup, s conversion.Scope) error {
	if err := autoConvert_v1beta1_PodCliqueScalingGroup_To_v1alpha1_PodCliqueScalingGroup(in, out, s); err != nil {
		return err
	}
	data := &conversionData{Self: convertComponentToAlpha(
		betaComponent{init: &in.Spec.Init, termination: &in.Spec.Termination},
		alphaComponent{minAvailable: &out.Spec.MinAvailable},
		componentContext{})}
	annotations, err := withConversionData(in.Annotations, data)
	if err != nil {
		return err
	}
	out.Annotations = annotations
	return nil
}

// The conversions below are context-free. The object-level conversions above set the fields that need context.

// Convert_v1alpha1_PodCliqueSpec_To_v1beta1_PodCliqueSpec converts a PodCliqueSpec to v1beta1.
func Convert_v1alpha1_PodCliqueSpec_To_v1beta1_PodCliqueSpec(in *PodCliqueSpec, out *v1beta1.PodCliqueSpec, s conversion.Scope) error {
	if err := autoConvert_v1alpha1_PodCliqueSpec_To_v1beta1_PodCliqueSpec(in, out, s); err != nil {
		return err
	}
	out.StartsAfter = nil
	if in.StartsAfter != nil {
		out.StartsAfter = make([]v1beta1.StartupDependency, len(in.StartsAfter))
		for i, name := range in.StartsAfter {
			out.StartsAfter[i] = v1beta1.StartupDependency{Name: name, ConditionType: string(corev1.PodReady)}
		}
	}
	deriveBeta(alphaComponent{minAvailable: &in.MinAvailable}, betaComponent{init: &out.Init, termination: &out.Termination}, componentContext{})
	return nil
}

// Convert_v1beta1_PodCliqueSpec_To_v1alpha1_PodCliqueSpec converts a PodCliqueSpec to v1alpha1.
func Convert_v1beta1_PodCliqueSpec_To_v1alpha1_PodCliqueSpec(in *v1beta1.PodCliqueSpec, out *PodCliqueSpec, s conversion.Scope) error {
	if err := autoConvert_v1beta1_PodCliqueSpec_To_v1alpha1_PodCliqueSpec(in, out, s); err != nil {
		return err
	}
	out.StartsAfter = nil
	if in.StartsAfter != nil {
		out.StartsAfter = make([]string, len(in.StartsAfter))
		for i, dependency := range in.StartsAfter {
			out.StartsAfter[i] = dependency.Name
		}
	}
	out.MinAvailable = ptr.To(initMinReplicasOf(in.Init))
	return nil
}

// Convert_v1alpha1_PodCliqueSetTemplateSpec_To_v1beta1_PodCliqueSetTemplateSpec drops terminationDelay, which the
// PodCliqueSet conversion moves into every component's breach tolerance.
func Convert_v1alpha1_PodCliqueSetTemplateSpec_To_v1beta1_PodCliqueSetTemplateSpec(in *PodCliqueSetTemplateSpec, out *v1beta1.PodCliqueSetTemplateSpec, s conversion.Scope) error {
	return autoConvert_v1alpha1_PodCliqueSetTemplateSpec_To_v1beta1_PodCliqueSetTemplateSpec(in, out, s)
}

// Convert_v1alpha1_PodCliqueScalingGroupConfig_To_v1beta1_PodCliqueScalingGroupConfig converts a
// PodCliqueScalingGroupConfig to v1beta1.
func Convert_v1alpha1_PodCliqueScalingGroupConfig_To_v1beta1_PodCliqueScalingGroupConfig(in *PodCliqueScalingGroupConfig, out *v1beta1.PodCliqueScalingGroupConfig, s conversion.Scope) error {
	if err := autoConvert_v1alpha1_PodCliqueScalingGroupConfig_To_v1beta1_PodCliqueScalingGroupConfig(in, out, s); err != nil {
		return err
	}
	deriveBeta(alphaComponent{minAvailable: &in.MinAvailable}, betaComponent{init: &out.Init, termination: &out.Termination}, componentContext{})
	return nil
}

// Convert_v1beta1_PodCliqueScalingGroupConfig_To_v1alpha1_PodCliqueScalingGroupConfig converts a
// PodCliqueScalingGroupConfig to v1alpha1.
func Convert_v1beta1_PodCliqueScalingGroupConfig_To_v1alpha1_PodCliqueScalingGroupConfig(in *v1beta1.PodCliqueScalingGroupConfig, out *PodCliqueScalingGroupConfig, s conversion.Scope) error {
	if err := autoConvert_v1beta1_PodCliqueScalingGroupConfig_To_v1alpha1_PodCliqueScalingGroupConfig(in, out, s); err != nil {
		return err
	}
	out.MinAvailable = ptr.To(initMinReplicasOf(in.Init))
	return nil
}

// Convert_v1alpha1_PodCliqueScalingGroupSpec_To_v1beta1_PodCliqueScalingGroupSpec converts a
// PodCliqueScalingGroupSpec to v1beta1.
func Convert_v1alpha1_PodCliqueScalingGroupSpec_To_v1beta1_PodCliqueScalingGroupSpec(in *PodCliqueScalingGroupSpec, out *v1beta1.PodCliqueScalingGroupSpec, s conversion.Scope) error {
	if err := autoConvert_v1alpha1_PodCliqueScalingGroupSpec_To_v1beta1_PodCliqueScalingGroupSpec(in, out, s); err != nil {
		return err
	}
	deriveBeta(alphaComponent{minAvailable: &in.MinAvailable}, betaComponent{init: &out.Init, termination: &out.Termination}, componentContext{})
	return nil
}

// Convert_v1beta1_PodCliqueScalingGroupSpec_To_v1alpha1_PodCliqueScalingGroupSpec converts a
// PodCliqueScalingGroupSpec to v1alpha1.
func Convert_v1beta1_PodCliqueScalingGroupSpec_To_v1alpha1_PodCliqueScalingGroupSpec(in *v1beta1.PodCliqueScalingGroupSpec, out *PodCliqueScalingGroupSpec, s conversion.Scope) error {
	if err := autoConvert_v1beta1_PodCliqueScalingGroupSpec_To_v1alpha1_PodCliqueScalingGroupSpec(in, out, s); err != nil {
		return err
	}
	out.MinAvailable = ptr.To(initMinReplicasOf(in.Init))
	return nil
}

// Convert_v1alpha1_RollingUpdateConfiguration_To_v1beta1_RollingUpdateConfiguration converts a
// RollingUpdateConfiguration to v1beta1. minReplicas is set by the PodCliqueSet conversion.
func Convert_v1alpha1_RollingUpdateConfiguration_To_v1beta1_RollingUpdateConfiguration(in *RollingUpdateConfiguration, out *v1beta1.RollingUpdateConfiguration, _ conversion.Scope) error {
	*out = *convertRollingUpdateToBeta(in)
	return nil
}

// Convert_v1beta1_RollingUpdateConfiguration_To_v1alpha1_RollingUpdateConfiguration converts a
// RollingUpdateConfiguration to v1alpha1. A percentage maxUnavailable is resolved by the PodCliqueSet conversion.
func Convert_v1beta1_RollingUpdateConfiguration_To_v1alpha1_RollingUpdateConfiguration(in *v1beta1.RollingUpdateConfiguration, out *RollingUpdateConfiguration, _ conversion.Scope) error {
	out.ProgressDeadline = in.ProgressDeadline.DeepCopy()
	out.MaxUnavailable = nil
	if in.MaxUnavailable != nil && in.MaxUnavailable.Type == intstr.Int {
		out.MaxUnavailable = ptr.To(in.MaxUnavailable.IntVal)
	}
	return nil
}

// Convert_v1alpha1_TopologyConstraint_To_v1beta1_TopologyConstraint converts a TopologyConstraint to v1beta1, moving
// the deprecated packDomain to pack.required.
func Convert_v1alpha1_TopologyConstraint_To_v1beta1_TopologyConstraint(in *TopologyConstraint, out *v1beta1.TopologyConstraint, s conversion.Scope) error {
	if err := autoConvert_v1alpha1_TopologyConstraint_To_v1beta1_TopologyConstraint(in, out, s); err != nil {
		return err
	}
	if in.PackDomain == "" {
		return nil
	}
	if out.Pack == nil {
		out.Pack = &v1beta1.TopologyPackConstraint{}
	} else {
		out.Pack = out.Pack.DeepCopy()
	}
	if out.Pack.RequiredDomain == "" {
		out.Pack.RequiredDomain = v1beta1.TopologyDomain(in.PackDomain)
	}
	return nil
}

// Convert_Pointer_v1alpha1_PodCliqueTemplateSpec_To_Pointer_v1beta1_PodCliqueTemplateSpec converts an element of
// the template's clique list.
func Convert_Pointer_v1alpha1_PodCliqueTemplateSpec_To_Pointer_v1beta1_PodCliqueTemplateSpec(in **PodCliqueTemplateSpec, out **v1beta1.PodCliqueTemplateSpec, s conversion.Scope) error {
	if *in == nil {
		*out = nil
		return nil
	}
	*out = new(v1beta1.PodCliqueTemplateSpec)
	return Convert_v1alpha1_PodCliqueTemplateSpec_To_v1beta1_PodCliqueTemplateSpec(*in, *out, s)
}

// Convert_Pointer_v1beta1_PodCliqueTemplateSpec_To_Pointer_v1alpha1_PodCliqueTemplateSpec converts an element of
// the template's clique list.
func Convert_Pointer_v1beta1_PodCliqueTemplateSpec_To_Pointer_v1alpha1_PodCliqueTemplateSpec(in **v1beta1.PodCliqueTemplateSpec, out **PodCliqueTemplateSpec, s conversion.Scope) error {
	if *in == nil {
		*out = nil
		return nil
	}
	*out = new(PodCliqueTemplateSpec)
	return Convert_v1beta1_PodCliqueTemplateSpec_To_v1alpha1_PodCliqueTemplateSpec(*in, *out, s)
}

// Convert_v1beta1_StartupDependency_To_string converts a v1beta1 startsAfter entry to its name.
func Convert_v1beta1_StartupDependency_To_string(in *v1beta1.StartupDependency, out *string, _ conversion.Scope) error {
	*out = in.Name
	return nil
}

// Convert_intstr_IntOrString_To_int32 converts an absolute maxUnavailable. A percentage is resolved by the
// PodCliqueSet conversion, which knows the template's replicas.
func Convert_intstr_IntOrString_To_int32(in *intstr.IntOrString, out *int32, _ conversion.Scope) error {
	*out = 0
	if in.Type == intstr.Int {
		*out = in.IntVal
	}
	return nil
}

func initMinReplicasOf(init *v1beta1.InitSpec) int32 {
	if init == nil || init.MinReplicas == nil {
		return 1
	}
	return *init.MinReplicas
}
