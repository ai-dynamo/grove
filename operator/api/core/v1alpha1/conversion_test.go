/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

func availabilityTermination(minReplicas int32, breachTolerance *metav1.Duration) *v1beta1.TerminationSpec {
	return &v1beta1.TerminationSpec{Resource: &v1beta1.ResourceTerminationSpec{
		Availability: &v1beta1.AvailabilityPolicy{MinReplicas: ptr.To(minReplicas), BreachTolerance: breachTolerance},
	}}
}

func toBeta(t *testing.T, in *PodCliqueSet) *v1beta1.PodCliqueSet {
	t.Helper()
	out := &v1beta1.PodCliqueSet{}
	require.NoError(t, Convert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet(in, out, nil))
	return out
}

func toAlpha(t *testing.T, in *v1beta1.PodCliqueSet) *PodCliqueSet {
	t.Helper()
	out := &PodCliqueSet{}
	require.NoError(t, Convert_v1beta1_PodCliqueSet_To_v1alpha1_PodCliqueSet(in, out, nil))
	return out
}

func TestConvertPodCliqueSetToBetaDerivesFields(t *testing.T) {
	oneHour := &metav1.Duration{Duration: time.Hour}
	defaultTolerance := &metav1.Duration{Duration: defaultTerminationDelay}
	testCases := []struct {
		description         string
		strategy            UpdateStrategyType
		minAvailable        *int32
		terminationDelay    *metav1.Duration
		rollingUpdate       *RollingUpdateConfiguration
		wantInit            int32
		wantBreachTolerance *metav1.Duration
		wantRollingUpdate   *v1beta1.RollingUpdateConfiguration
	}{
		{
			description:         "minAvailable and terminationDelay become init, availability and breachTolerance",
			strategy:            RollingRecreateStrategy,
			minAvailable:        ptr.To[int32](2),
			terminationDelay:    oneHour,
			wantInit:            2,
			wantBreachTolerance: oneHour,
		},
		{
			description:         "unset values take the v1alpha1 defaults",
			strategy:            RollingRecreateStrategy,
			wantInit:            1,
			wantBreachTolerance: defaultTolerance,
		},
		{
			description:         "Coherent sets rollingUpdate.minReplicas to minAvailable",
			strategy:            CoherentStrategy,
			minAvailable:        ptr.To[int32](3),
			rollingUpdate:       &RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](3)},
			wantInit:            3,
			wantBreachTolerance: defaultTolerance,
			wantRollingUpdate:   &v1beta1.RollingUpdateConfiguration{MaxUnavailable: ptr.To(intstr.FromInt32(3)), MinReplicas: ptr.To[int32](3)},
		},
		{
			description:         "RollingRecreate converts maxUnavailable to an integer IntOrString",
			strategy:            RollingRecreateStrategy,
			minAvailable:        ptr.To[int32](2),
			rollingUpdate:       &RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](1)},
			wantInit:            2,
			wantBreachTolerance: defaultTolerance,
			wantRollingUpdate:   &v1beta1.RollingUpdateConfiguration{MaxUnavailable: ptr.To(intstr.FromInt32(1))},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			in := &PodCliqueSet{Spec: PodCliqueSetSpec{
				UpdateStrategy: &PodCliqueSetUpdateStrategy{Type: tc.strategy},
				Template: PodCliqueSetTemplateSpec{
					TerminationDelay: tc.terminationDelay,
					Cliques: []*PodCliqueTemplateSpec{
						{Name: "leader", Spec: PodCliqueSpec{Replicas: 4, MinAvailable: tc.minAvailable}, RollingUpdate: tc.rollingUpdate},
						{Name: "worker", Spec: PodCliqueSpec{Replicas: 4, MinAvailable: tc.minAvailable, StartsAfter: []string{"leader"}}},
					},
				},
			}}

			out := toBeta(t, in)

			leader := out.Spec.Template.Cliques[0]
			assert.Equal(t, &v1beta1.InitSpec{MinReplicas: ptr.To(tc.wantInit)}, leader.Spec.Init)
			assert.Equal(t, availabilityTermination(tc.wantInit, tc.wantBreachTolerance), leader.Spec.Termination)
			assert.Equal(t, tc.wantRollingUpdate, leader.RollingUpdate)
			assert.Equal(t, []v1beta1.StartupDependency{{Name: "leader", ConditionType: string(corev1.PodReady)}},
				out.Spec.Template.Cliques[1].Spec.StartsAfter)
			assert.NotContains(t, out.Annotations, ConversionDataAnnotation)
		})
	}
}

func TestConvertPodCliqueSetRoundTripKeepsBetaValues(t *testing.T) {
	const modelLoaded = "example.com/model-loaded"
	halfHour := &metav1.Duration{Duration: 30 * time.Minute}
	beta := &v1beta1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"team": "platform"}},
		Spec: v1beta1.PodCliqueSetSpec{
			UpdateStrategy: &v1beta1.PodCliqueSetUpdateStrategy{Type: v1beta1.CoherentStrategy},
			Template: v1beta1.PodCliqueSetTemplateSpec{
				Cliques: []*v1beta1.PodCliqueTemplateSpec{
					{
						Name: "model",
						Spec: v1beta1.PodCliqueSpec{
							Replicas:    4,
							Init:        &v1beta1.InitSpec{MinReplicas: ptr.To[int32](3)},
							Termination: availabilityTermination(2, halfHour),
						},
						RollingUpdate: &v1beta1.RollingUpdateConfiguration{
							MaxUnavailable: ptr.To(intstr.FromString("50%")),
							MinReplicas:    ptr.To[int32](2),
						},
					},
					{
						Name: "worker",
						Spec: v1beta1.PodCliqueSpec{
							Replicas:    2,
							Init:        &v1beta1.InitSpec{MinReplicas: ptr.To[int32](2)},
							Termination: availabilityTermination(2, halfHour),
							StartsAfter: []v1beta1.StartupDependency{{Name: "model", ConditionType: modelLoaded}},
						},
					},
				},
			},
		},
	}

	alpha := toAlpha(t, beta)

	model := alpha.Spec.Template.Cliques[0]
	assert.Equal(t, ptr.To[int32](3), model.Spec.MinAvailable, "minAvailable takes init.minReplicas")
	// 50% of 4 is 2, which Coherent raises to minAvailable 3.
	assert.Equal(t, &RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](3)}, model.RollingUpdate)
	assert.Equal(t, halfHour, alpha.Spec.Template.TerminationDelay)
	assert.Equal(t, []string{"model"}, alpha.Spec.Template.Cliques[1].Spec.StartsAfter)
	assert.Contains(t, alpha.Annotations, ConversionDataAnnotation)
	assert.Equal(t, "platform", alpha.Annotations["team"])

	roundTripped := toBeta(t, alpha)
	assert.Equal(t, beta.Spec, roundTripped.Spec)
	assert.Equal(t, beta.Annotations, roundTripped.Annotations)
}

func TestConvertPodCliqueSetStashIgnoredAfterAlphaEdit(t *testing.T) {
	beta := &v1beta1.PodCliqueSet{Spec: v1beta1.PodCliqueSetSpec{Template: v1beta1.PodCliqueSetTemplateSpec{
		Cliques: []*v1beta1.PodCliqueTemplateSpec{{
			Name: "model",
			Spec: v1beta1.PodCliqueSpec{
				Replicas:    4,
				Init:        &v1beta1.InitSpec{MinReplicas: ptr.To[int32](3)},
				Termination: availabilityTermination(2, nil),
			},
		}},
	}}}

	alpha := toAlpha(t, beta)
	require.Contains(t, alpha.Annotations, ConversionDataAnnotation)
	alpha.Spec.Template.Cliques[0].Spec.MinAvailable = ptr.To[int32](1)

	out := toBeta(t, alpha)
	model := out.Spec.Template.Cliques[0]
	assert.Equal(t, &v1beta1.InitSpec{MinReplicas: ptr.To[int32](1)}, model.Spec.Init)
	assert.Equal(t, availabilityTermination(1, &metav1.Duration{Duration: defaultTerminationDelay}), model.Spec.Termination)
}

func TestConvertPodCliqueSetWithoutLostValuesWritesNoStash(t *testing.T) {
	alpha := &PodCliqueSet{Spec: PodCliqueSetSpec{Template: PodCliqueSetTemplateSpec{
		Cliques: []*PodCliqueTemplateSpec{{Name: "model", Spec: PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To[int32](2)}}},
	}}}

	back := toAlpha(t, toBeta(t, alpha))
	assert.NotContains(t, back.Annotations, ConversionDataAnnotation)
	assert.Equal(t, alpha.Spec.Template.Cliques[0].Spec.MinAvailable, back.Spec.Template.Cliques[0].Spec.MinAvailable)
}
