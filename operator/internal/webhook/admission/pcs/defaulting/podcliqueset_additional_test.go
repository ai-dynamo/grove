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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

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
			result := defaultPodCliqueTemplateSpecs(tt.input)
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
			result := defaultPodCliqueScalingGroupConfigs(tt.input)
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
