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
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
)

func TestDefaultPodCliqueSet(t *testing.T) {
	want := grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "PCS1",
			Namespace: "default",
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{
				Type: grovecorev1alpha1.RollingRecreateStrategy,
			},
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
					Name: "test",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas: 2,
						PodSpec: corev1.PodSpec{
							RestartPolicy:                 corev1.RestartPolicyAlways,
							TerminationGracePeriodSeconds: ptr.To[int64](30),
						},
						ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
							MinReplicas: ptr.To(int32(2)),
							MaxReplicas: 3,
						},
						MinAvailable: ptr.To[int32](2),
					},
					RollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{
						MaxUnavailable: ptr.To[int32](1),
					},
				}},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
				HeadlessServiceConfig: &grovecorev1alpha1.HeadlessServiceConfig{
					PublishNotReadyAddresses: true,
				},
				TerminationDelay: &metav1.Duration{Duration: 4 * time.Hour},
			},
		},
	}
	input := grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "PCS1",
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
					Name: "test",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas: 2,
						ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
							MinReplicas: ptr.To[int32](2),
							MaxReplicas: 3,
						},
					},
				}},
			},
		},
	}
	defaultPodCliqueSet(&input)
	assert.Equal(t, want, input)
}

// TestDefaultPodCliqueTemplateSpecs tests the defaulting logic for PodCliqueTemplateSpecs.
func TestDefaultPodCliqueTemplateSpecs(t *testing.T) {
	tests := []struct {
		// name identifies this test case
		name string
		// input is the slice of clique template specs to default
		input []*grovecorev1alpha1.PodCliqueTemplateSpec
		// verify function checks the defaulted output
		verify func(*testing.T, []*grovecorev1alpha1.PodCliqueTemplateSpec)
	}{
		{
			name: "replicas defaults to 1 when 0",
			input: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas: 0,
						RoleName: "role1",
						PodSpec:  corev1.PodSpec{},
					},
				},
			},
			verify: func(t *testing.T, result []*grovecorev1alpha1.PodCliqueTemplateSpec) {
				require.Len(t, result, 1)
				assert.Equal(t, int32(1), result[0].Spec.Replicas)
			},
		},
		{
			name: "minAvailable defaults to replicas when nil",
			input: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     5,
						RoleName:     "role1",
						MinAvailable: nil,
						PodSpec:      corev1.PodSpec{},
					},
				},
			},
			verify: func(t *testing.T, result []*grovecorev1alpha1.PodCliqueTemplateSpec) {
				require.Len(t, result, 1)
				require.NotNil(t, result[0].Spec.MinAvailable)
				assert.Equal(t, int32(5), *result[0].Spec.MinAvailable)
			},
		},
		{
			name: "minAvailable is not overridden when set",
			input: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     5,
						RoleName:     "role1",
						MinAvailable: ptr.To(int32(3)),
						PodSpec:      corev1.PodSpec{},
					},
				},
			},
			verify: func(t *testing.T, result []*grovecorev1alpha1.PodCliqueTemplateSpec) {
				require.Len(t, result, 1)
				require.NotNil(t, result[0].Spec.MinAvailable)
				assert.Equal(t, int32(3), *result[0].Spec.MinAvailable)
			},
		},
		{
			name: "scaleConfig minReplicas defaults to replicas when nil",
			input: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas: 5,
						RoleName: "role1",
						PodSpec:  corev1.PodSpec{},
						ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
							MinReplicas: nil,
							MaxReplicas: 10,
						},
					},
				},
			},
			verify: func(t *testing.T, result []*grovecorev1alpha1.PodCliqueTemplateSpec) {
				require.Len(t, result, 1)
				require.NotNil(t, result[0].Spec.ScaleConfig)
				require.NotNil(t, result[0].Spec.ScaleConfig.MinReplicas)
				assert.Equal(t, int32(5), *result[0].Spec.ScaleConfig.MinReplicas)
			},
		},
		{
			name: "scaleConfig minReplicas is not overridden when set",
			input: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas: 5,
						RoleName: "role1",
						PodSpec:  corev1.PodSpec{},
						ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
							MinReplicas: ptr.To(int32(2)),
							MaxReplicas: 10,
						},
					},
				},
			},
			verify: func(t *testing.T, result []*grovecorev1alpha1.PodCliqueTemplateSpec) {
				require.Len(t, result, 1)
				require.NotNil(t, result[0].Spec.ScaleConfig)
				require.NotNil(t, result[0].Spec.ScaleConfig.MinReplicas)
				assert.Equal(t, int32(2), *result[0].Spec.ScaleConfig.MinReplicas)
			},
		},
		{
			name: "nil scaleConfig does not cause panic",
			input: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:    5,
						RoleName:    "role1",
						PodSpec:     corev1.PodSpec{},
						ScaleConfig: nil,
					},
				},
			},
			verify: func(t *testing.T, result []*grovecorev1alpha1.PodCliqueTemplateSpec) {
				require.Len(t, result, 1)
				assert.Nil(t, result[0].Spec.ScaleConfig)
			},
		},
		{
			name: "pod spec defaults are applied",
			input: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "clique1",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas: 1,
						RoleName: "role1",
						PodSpec:  corev1.PodSpec{},
					},
				},
			},
			verify: func(t *testing.T, result []*grovecorev1alpha1.PodCliqueTemplateSpec) {
				require.Len(t, result, 1)
				assert.Equal(t, corev1.RestartPolicyAlways, result[0].Spec.PodSpec.RestartPolicy)
				require.NotNil(t, result[0].Spec.PodSpec.TerminationGracePeriodSeconds)
				assert.Equal(t, int64(30), *result[0].Spec.PodSpec.TerminationGracePeriodSeconds)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := defaultPodCliqueTemplateSpecs(tt.input, grovecorev1alpha1.RollingRecreateStrategy, sets.New[string]())
			tt.verify(t, result)
		})
	}
}

// TestDefaultPodCliqueScalingGroupConfigs tests the defaulting logic for scaling group configurations.
func TestDefaultPodCliqueScalingGroupConfigs(t *testing.T) {
	tests := []struct {
		// name identifies this test case
		name string
		// input is the slice of scaling group configs to default
		input []grovecorev1alpha1.PodCliqueScalingGroupConfig
		// verify function checks the defaulted output
		verify func(*testing.T, []grovecorev1alpha1.PodCliqueScalingGroupConfig)
	}{
		{
			name: "scaleConfig minReplicas defaults to replicas when nil",
			input: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:        "sg1",
					CliqueNames: []string{"clique1", "clique2"},
					Replicas:    ptr.To(int32(3)),
					ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: nil,
						MaxReplicas: 10,
					},
				},
			},
			verify: func(t *testing.T, result []grovecorev1alpha1.PodCliqueScalingGroupConfig) {
				require.Len(t, result, 1)
				require.NotNil(t, result[0].ScaleConfig)
				require.NotNil(t, result[0].ScaleConfig.MinReplicas)
				assert.Equal(t, int32(3), *result[0].ScaleConfig.MinReplicas)
			},
		},
		{
			name: "scaleConfig minReplicas is not overridden when set",
			input: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:        "sg1",
					CliqueNames: []string{"clique1", "clique2"},
					Replicas:    ptr.To(int32(3)),
					ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: ptr.To(int32(1)),
						MaxReplicas: 10,
					},
				},
			},
			verify: func(t *testing.T, result []grovecorev1alpha1.PodCliqueScalingGroupConfig) {
				require.Len(t, result, 1)
				require.NotNil(t, result[0].ScaleConfig)
				require.NotNil(t, result[0].ScaleConfig.MinReplicas)
				assert.Equal(t, int32(1), *result[0].ScaleConfig.MinReplicas)
			},
		},
		{
			name: "nil scaleConfig does not cause panic",
			input: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:        "sg1",
					CliqueNames: []string{"clique1", "clique2"},
					Replicas:    ptr.To(int32(3)),
					ScaleConfig: nil,
				},
			},
			verify: func(t *testing.T, result []grovecorev1alpha1.PodCliqueScalingGroupConfig) {
				require.Len(t, result, 1)
				assert.Nil(t, result[0].ScaleConfig)
			},
		},
		{
			name:  "empty input returns empty output",
			input: []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
			verify: func(t *testing.T, result []grovecorev1alpha1.PodCliqueScalingGroupConfig) {
				assert.Empty(t, result)
			},
		},
		{
			name:  "nil input returns empty output",
			input: nil,
			verify: func(t *testing.T, result []grovecorev1alpha1.PodCliqueScalingGroupConfig) {
				assert.Empty(t, result)
			},
		},
		{
			name: "multiple scaling groups are all defaulted correctly",
			input: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:        "sg1",
					CliqueNames: []string{"clique1"},
					Replicas:    ptr.To(int32(2)),
					ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: nil,
						MaxReplicas: 5,
					},
				},
				{
					Name:        "sg2",
					CliqueNames: []string{"clique2"},
					Replicas:    ptr.To(int32(4)),
					ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: nil,
						MaxReplicas: 8,
					},
				},
			},
			verify: func(t *testing.T, result []grovecorev1alpha1.PodCliqueScalingGroupConfig) {
				require.Len(t, result, 2)
				require.NotNil(t, result[0].ScaleConfig.MinReplicas)
				assert.Equal(t, int32(2), *result[0].ScaleConfig.MinReplicas)
				require.NotNil(t, result[1].ScaleConfig.MinReplicas)
				assert.Equal(t, int32(4), *result[1].ScaleConfig.MinReplicas)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := defaultPodCliqueScalingGroupConfigs(tt.input, grovecorev1alpha1.RollingRecreateStrategy)
			tt.verify(t, result)
		})
	}
}

// TestDefaultPodSpec tests the defaulting logic for PodSpec.
func TestDefaultPodSpec(t *testing.T) {
	tests := []struct {
		// name identifies this test case
		name string
		// input is the PodSpec to default
		input *corev1.PodSpec
		// verify function checks the defaulted output
		verify func(*testing.T, *corev1.PodSpec)
	}{
		{
			name:  "restartPolicy defaults to Always when empty",
			input: &corev1.PodSpec{},
			verify: func(t *testing.T, result *corev1.PodSpec) {
				assert.Equal(t, corev1.RestartPolicyAlways, result.RestartPolicy)
			},
		},
		{
			name: "restartPolicy is not overridden when set",
			input: &corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
			},
			verify: func(t *testing.T, result *corev1.PodSpec) {
				assert.Equal(t, corev1.RestartPolicyNever, result.RestartPolicy)
			},
		},
		{
			name:  "terminationGracePeriodSeconds defaults to 30 when nil",
			input: &corev1.PodSpec{},
			verify: func(t *testing.T, result *corev1.PodSpec) {
				require.NotNil(t, result.TerminationGracePeriodSeconds)
				assert.Equal(t, int64(30), *result.TerminationGracePeriodSeconds)
			},
		},
		{
			name: "terminationGracePeriodSeconds is not overridden when set",
			input: &corev1.PodSpec{
				TerminationGracePeriodSeconds: ptr.To[int64](60),
			},
			verify: func(t *testing.T, result *corev1.PodSpec) {
				require.NotNil(t, result.TerminationGracePeriodSeconds)
				assert.Equal(t, int64(60), *result.TerminationGracePeriodSeconds)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := defaultPodSpec(tt.input)
			tt.verify(t, result)
		})
	}
}

func TestDefaultUpdateStrategy(t *testing.T) {
	testCases := []struct {
		description  string
		input        *grovecorev1alpha1.PodCliqueSetUpdateStrategy
		wantStrategy grovecorev1alpha1.UpdateStrategyType
	}{
		{
			description:  "nil UpdateStrategy defaults to RollingRecreate",
			input:        nil,
			wantStrategy: grovecorev1alpha1.RollingRecreateStrategy,
		},
		{
			description:  "empty Type defaults to RollingRecreate",
			input:        &grovecorev1alpha1.PodCliqueSetUpdateStrategy{},
			wantStrategy: grovecorev1alpha1.RollingRecreateStrategy,
		},
		{
			description:  "existing Type is preserved",
			input:        &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy},
			wantStrategy: grovecorev1alpha1.OnDeleteStrategy,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{UpdateStrategy: tc.input},
			}
			defaultUpdateStrategy(&pcs.Spec)
			require.NotNil(t, pcs.Spec.UpdateStrategy)
			assert.Equal(t, tc.wantStrategy, pcs.Spec.UpdateStrategy.Type)
		})
	}
}

func TestDefaultRollingUpdateConfiguration(t *testing.T) {
	testCases := []struct {
		description        string
		existing           *grovecorev1alpha1.RollingUpdateConfiguration
		updateStrategy     grovecorev1alpha1.UpdateStrategyType
		wantNil            bool
		wantMaxUnavailable int32
	}{
		{
			description:        "rollingRecreate defaults MaxUnavailable to 1",
			existing:           nil,
			updateStrategy:     grovecorev1alpha1.RollingRecreateStrategy,
			wantMaxUnavailable: 1,
		},
		{
			description:    "onDelete leaves a nil configuration nil",
			existing:       nil,
			updateStrategy: grovecorev1alpha1.OnDeleteStrategy,
			wantNil:        true,
		},
		{
			description:        "onDelete leaves an existing configuration untouched for the validating webhook to reject",
			existing:           &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](7)},
			updateStrategy:     grovecorev1alpha1.OnDeleteStrategy,
			wantMaxUnavailable: 7,
		},
		{
			description:        "existing MaxUnavailable is preserved",
			existing:           &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](7)},
			updateStrategy:     grovecorev1alpha1.RollingRecreateStrategy,
			wantMaxUnavailable: 7,
		},
		{
			description:        "existing configuration with nil MaxUnavailable is populated",
			existing:           &grovecorev1alpha1.RollingUpdateConfiguration{},
			updateStrategy:     grovecorev1alpha1.RollingRecreateStrategy,
			wantMaxUnavailable: 1,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := defaultRollingUpdateConfiguration(tc.existing, tc.updateStrategy)
			if tc.wantNil {
				assert.Nil(t, result)
				return
			}
			require.NotNil(t, result)
			require.NotNil(t, result.MaxUnavailable)
			assert.Equal(t, tc.wantMaxUnavailable, *result.MaxUnavailable)
		})
	}
}

func TestDefaultRollingUpdateForTemplateSpecsPerStrategy(t *testing.T) {
	testCases := []struct {
		description          string
		updateStrategy       grovecorev1alpha1.UpdateStrategyType
		pcsgOwnedCliqueNames sets.Set[string]
		input                *grovecorev1alpha1.PodCliqueTemplateSpec
		wantRollingUpdateNil bool
		wantMaxUnavailable   int32
	}{
		{
			description:        "rollingRecreate defaults standalone MaxUnavailable to 1",
			updateStrategy:     grovecorev1alpha1.RollingRecreateStrategy,
			input:              &grovecorev1alpha1.PodCliqueTemplateSpec{Name: "standalone", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](3)}},
			wantMaxUnavailable: 1,
		},
		{
			description:          "onDelete leaves standalone RollingUpdate nil",
			updateStrategy:       grovecorev1alpha1.OnDeleteStrategy,
			input:                &grovecorev1alpha1.PodCliqueTemplateSpec{Name: "standalone", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](3)}},
			wantRollingUpdateNil: true,
		},
		{
			description:        "onDelete leaves an existing standalone RollingUpdate untouched for the validating webhook to reject",
			updateStrategy:     grovecorev1alpha1.OnDeleteStrategy,
			input:              &grovecorev1alpha1.PodCliqueTemplateSpec{Name: "standalone", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](3)}, RollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](2)}},
			wantMaxUnavailable: 2,
		},
		{
			description:          "PCSG-owned clique is skipped",
			updateStrategy:       grovecorev1alpha1.RollingRecreateStrategy,
			pcsgOwnedCliqueNames: sets.New("member"),
			input:                &grovecorev1alpha1.PodCliqueTemplateSpec{Name: "member", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To[int32](3)}},
			wantRollingUpdateNil: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := defaultPodCliqueTemplateSpecs([]*grovecorev1alpha1.PodCliqueTemplateSpec{tc.input}, tc.updateStrategy, tc.pcsgOwnedCliqueNames)
			require.Len(t, result, 1)
			if tc.wantRollingUpdateNil {
				assert.Nil(t, result[0].RollingUpdate)
				return
			}
			require.NotNil(t, result[0].RollingUpdate)
			require.NotNil(t, result[0].RollingUpdate.MaxUnavailable)
			assert.Equal(t, tc.wantMaxUnavailable, *result[0].RollingUpdate.MaxUnavailable)
		})
	}
}

func TestDefaultRollingUpdateForScalingGroupConfigsPerStrategy(t *testing.T) {
	testCases := []struct {
		description          string
		updateStrategy       grovecorev1alpha1.UpdateStrategyType
		input                grovecorev1alpha1.PodCliqueScalingGroupConfig
		wantRollingUpdateNil bool
		wantMaxUnavailable   int32
	}{
		{
			description:        "rollingRecreate defaults MaxUnavailable to 1",
			updateStrategy:     grovecorev1alpha1.RollingRecreateStrategy,
			input:              grovecorev1alpha1.PodCliqueScalingGroupConfig{Name: "sg", CliqueNames: []string{"c"}, Replicas: ptr.To[int32](4), MinAvailable: ptr.To[int32](2)},
			wantMaxUnavailable: 1,
		},
		{
			description:          "onDelete leaves RollingUpdate nil",
			updateStrategy:       grovecorev1alpha1.OnDeleteStrategy,
			input:                grovecorev1alpha1.PodCliqueScalingGroupConfig{Name: "sg", CliqueNames: []string{"c"}, Replicas: ptr.To[int32](4), MinAvailable: ptr.To[int32](2)},
			wantRollingUpdateNil: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := defaultPodCliqueScalingGroupConfigs([]grovecorev1alpha1.PodCliqueScalingGroupConfig{tc.input}, tc.updateStrategy)
			require.Len(t, result, 1)
			if tc.wantRollingUpdateNil {
				assert.Nil(t, result[0].RollingUpdate)
				return
			}
			require.NotNil(t, result[0].RollingUpdate)
			require.NotNil(t, result[0].RollingUpdate.MaxUnavailable)
			assert.Equal(t, tc.wantMaxUnavailable, *result[0].RollingUpdate.MaxUnavailable)
		})
	}
}
