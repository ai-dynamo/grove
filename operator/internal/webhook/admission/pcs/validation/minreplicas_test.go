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

package validation

import (
	"testing"
	"time"

	groveconfigv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
)

func newMinReplicasValidator(pcs *grovecorev1beta1.PodCliqueSet, operation admissionv1.Operation) *pcsValidator {
	return newPCSValidator(pcs, operation, defaultTASConfig(), groveconfigv1alpha1.SchedulerConfiguration{
		Profiles:           []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube}},
		DefaultProfileName: string(groveconfigv1alpha1.SchedulerNameKube),
	}, nil, testutils.NewDefaultFakeRegistry())
}

// newMinReplicasPCS builds a PodCliqueSet with one standalone clique "fe" (replicas 4) and the given strategy.
func newMinReplicasPCS(strategy grovecorev1beta1.UpdateStrategyType, mutate func(*grovecorev1beta1.PodCliqueTemplateSpec)) *grovecorev1beta1.PodCliqueSet {
	clique := testutils.NewPodCliqueTemplateSpecBuilder("fe").
		WithReplicas(4).
		WithRoleName("fe-role").
		WithInitMinReplicas(3).
		WithAvailabilityMinReplicas(2).
		Build()
	if strategy == grovecorev1beta1.CoherentStrategy {
		clique.RollingUpdate = &grovecorev1beta1.RollingUpdateConfiguration{MinReplicas: ptr.To[int32](2), MaxUnavailable: ptr.To(intstr.FromInt32(2))}
	}
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", uuid.NewUUID()).
		WithReplicas(1).
		WithBreachTolerance(4 * time.Hour).
		WithCliqueStartupType(ptr.To(grovecorev1beta1.CliqueStartupTypeAnyOrder)).
		WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: strategy}).
		WithPodCliqueTemplateSpec(clique).
		Build()
	if mutate != nil {
		mutate(pcs.Spec.Template.Cliques[0])
	}
	return pcs
}

// TestValidateMinReplicasOrdering pins the ordering 1 <= availability.minReplicas <= init.minReplicas <=
// replicas and the autoscaling floor on create.
func TestValidateMinReplicasOrdering(t *testing.T) {
	cliquePath := "spec.template.cliques[0].spec"
	testCases := []struct {
		description   string
		mutate        func(*grovecorev1beta1.PodCliqueTemplateSpec)
		errorMatchers []testutils.ErrorMatcher
	}{
		{description: "valid ordering"},
		{
			description: "init.minReplicas above replicas rejected",
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.Spec.Init = testutils.NewInitSpec(5)
			},
			errorMatchers: []testutils.ErrorMatcher{{ErrorType: field.ErrorTypeInvalid, Field: cliquePath + ".init.minReplicas"}},
		},
		{
			description: "availability.minReplicas above init.minReplicas rejected",
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.Spec.Termination = testutils.NewAvailabilityTermination(4)
			},
			errorMatchers: []testutils.ErrorMatcher{{ErrorType: field.ErrorTypeInvalid, Field: cliquePath + ".termination.resource.availability.minReplicas"}},
		},
		{
			description: "non-positive breachTolerance rejected",
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.Spec.Termination.Resource.Availability.BreachTolerance = &metav1.Duration{}
			},
			errorMatchers: []testutils.ErrorMatcher{{ErrorType: field.ErrorTypeInvalid, Field: cliquePath + ".termination.resource.availability.breachTolerance"}},
		},
		{
			description: "autoscaling floor below availability.minReplicas rejected",
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.Spec.ScaleConfig = &grovecorev1beta1.AutoScalingConfig{MinReplicas: ptr.To[int32](1), MaxReplicas: 8}
			},
			errorMatchers: []testutils.ErrorMatcher{{ErrorType: field.ErrorTypeInvalid, Field: cliquePath + ".autoScalingConfig.minReplicas"}},
		},
		{
			description: "autoscaling floor at availability.minReplicas, below init.minReplicas, accepted",
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.Spec.ScaleConfig = &grovecorev1beta1.AutoScalingConfig{MinReplicas: ptr.To[int32](2), MaxReplicas: 8}
			},
		},
		{
			description: "autoscaling floor of 1 accepted without availability",
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.Spec.Termination = nil
				c.Spec.ScaleConfig = &grovecorev1beta1.AutoScalingConfig{MinReplicas: ptr.To[int32](1), MaxReplicas: 8}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := newMinReplicasPCS(grovecorev1beta1.RollingRecreateStrategy, tc.mutate)
			_, errs := newMinReplicasValidator(pcs, admissionv1.Create).validate()
			if tc.errorMatchers == nil {
				assert.Empty(t, errs)
				return
			}
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

// TestValidateMinReplicasUpdate pins the update rules of the min-replica fields.
func TestValidateMinReplicasUpdate(t *testing.T) {
	inProgress := &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
	testCases := []struct {
		description    string
		oldStrategy    grovecorev1beta1.UpdateStrategyType
		newStrategy    grovecorev1beta1.UpdateStrategyType
		oldProgress    *grovecorev1beta1.PodCliqueSetUpdateProgress
		mutate         func(*grovecorev1beta1.PodCliqueTemplateSpec)
		alphaRequest   bool
		wantErrContain string
	}{
		{
			description: "init.minReplicas can change in a v1beta1 request",
			oldStrategy: grovecorev1beta1.RollingRecreateStrategy,
			mutate:      func(c *grovecorev1beta1.PodCliqueTemplateSpec) { c.Spec.Init = testutils.NewInitSpec(4) },
		},
		{
			description: "availability.minReplicas can change in a v1beta1 request",
			oldStrategy: grovecorev1beta1.RollingRecreateStrategy,
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.Spec.Termination.Resource.Availability.MinReplicas = ptr.To[int32](3)
			},
		},
		{
			description:    "init.minReplicas cannot change in a v1alpha1 request",
			oldStrategy:    grovecorev1beta1.RollingRecreateStrategy,
			mutate:         func(c *grovecorev1beta1.PodCliqueTemplateSpec) { c.Spec.Init = testutils.NewInitSpec(4) },
			alphaRequest:   true,
			wantErrContain: "immutable for grove.io/v1alpha1 requests",
		},
		{
			description: "availability.minReplicas cannot change in a v1alpha1 request",
			oldStrategy: grovecorev1beta1.RollingRecreateStrategy,
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.Spec.Termination.Resource.Availability.MinReplicas = ptr.To[int32](3)
			},
			alphaRequest:   true,
			wantErrContain: "immutable for grove.io/v1alpha1 requests",
		},
		{
			description:    "removing availability rejected",
			oldStrategy:    grovecorev1beta1.RollingRecreateStrategy,
			mutate:         func(c *grovecorev1beta1.PodCliqueTemplateSpec) { c.Spec.Termination = nil },
			wantErrContain: "adding or removing availability is not allowed",
		},
		{
			description: "rollingUpdate.minReplicas can change when no Coherent update is in progress",
			oldStrategy: grovecorev1beta1.CoherentStrategy,
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.RollingUpdate.MinReplicas = ptr.To[int32](3)
				c.RollingUpdate.MaxUnavailable = ptr.To(intstr.FromInt32(3))
			},
		},
		{
			description: "rollingUpdate.minReplicas cannot change while a Coherent update is in progress",
			oldStrategy: grovecorev1beta1.CoherentStrategy,
			oldProgress: inProgress,
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.RollingUpdate.MinReplicas = ptr.To[int32](3)
				c.RollingUpdate.MaxUnavailable = ptr.To(intstr.FromInt32(3))
			},
			wantErrContain: "must not change while a Coherent update is in progress",
		},
		{
			description: "rollingUpdate.minReplicas cannot change in a v1alpha1 request",
			oldStrategy: grovecorev1beta1.CoherentStrategy,
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.RollingUpdate.MinReplicas = ptr.To[int32](3)
				c.RollingUpdate.MaxUnavailable = ptr.To(intstr.FromInt32(3))
			},
			alphaRequest:   true,
			wantErrContain: "immutable for grove.io/v1alpha1 requests",
		},
		{
			description: "a strategy switch that drops rollingUpdate.minReplicas is not a change of the value",
			oldStrategy: grovecorev1beta1.CoherentStrategy,
			newStrategy: grovecorev1beta1.RollingRecreateStrategy,
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.RollingUpdate = &grovecorev1beta1.RollingUpdateConfiguration{MaxUnavailable: ptr.To(intstr.FromInt32(1))}
			},
			alphaRequest: true,
		},
		{
			description: "switching away from Coherent during a Coherent update rejected",
			oldStrategy: grovecorev1beta1.CoherentStrategy,
			newStrategy: grovecorev1beta1.RollingRecreateStrategy,
			oldProgress: inProgress,
			mutate: func(c *grovecorev1beta1.PodCliqueTemplateSpec) {
				c.RollingUpdate = &grovecorev1beta1.RollingUpdateConfiguration{MaxUnavailable: ptr.To(intstr.FromInt32(1))}
			},
			wantErrContain: "must not change away from Coherent while a Coherent update is in progress",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			newStrategy := tc.newStrategy
			if newStrategy == "" {
				newStrategy = tc.oldStrategy
			}
			oldPCS := newMinReplicasPCS(tc.oldStrategy, nil)
			oldPCS.Status.UpdateProgress = tc.oldProgress
			newPCS := newMinReplicasPCS(newStrategy, tc.mutate)

			validator := newMinReplicasValidator(newPCS, admissionv1.Update)
			validator.alphaRequest = tc.alphaRequest
			err := validator.validateUpdate(oldPCS)
			if tc.wantErrContain == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrContain)
		})
	}
}
