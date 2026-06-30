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
				Type: grovecorev1alpha1.CoherentStrategy,
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
						MaxUnavailable: ptr.To[int32](2),
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

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := defaultPodCliqueTemplateSpecs(tc.input, grovecorev1alpha1.OnDeleteStrategy, sets.New[string]())
			tc.verify(t, result)
		})
	}
}

// TestDefaultPodCliqueTemplateSpecs_RollingUpdate covers the RollingUpdate defaulting path on standalone PodCliques.
func TestDefaultPodCliqueTemplateSpecs_RollingUpdate(t *testing.T) {
	clique := func(name string, replicas, minAvailable int32, ru *grovecorev1alpha1.RollingUpdateConfiguration) *grovecorev1alpha1.PodCliqueTemplateSpec {
		return &grovecorev1alpha1.PodCliqueTemplateSpec{
			Name: name,
			Spec: grovecorev1alpha1.PodCliqueSpec{
				Replicas:     replicas,
				MinAvailable: ptr.To(minAvailable),
				RoleName:     "r",
			},
			RollingUpdate: ru,
		}
	}
	tests := []struct {
		name                 string
		input                []*grovecorev1alpha1.PodCliqueTemplateSpec
		strategy             grovecorev1alpha1.UpdateStrategyType
		pcsgOwnedCliqueNames sets.Set[string]
		wantRollingUpdate    *grovecorev1alpha1.RollingUpdateConfiguration
	}{
		{
			name:                 "Coherent defaults standalone PCLQ MaxUnavailable to MinAvailable",
			input:                []*grovecorev1alpha1.PodCliqueTemplateSpec{clique("c", 5, 3, nil)},
			strategy:             grovecorev1alpha1.CoherentStrategy,
			pcsgOwnedCliqueNames: sets.New[string](),
			wantRollingUpdate:    &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](3)},
		},
		{
			name:                 "RollingRecreate defaults standalone PCLQ MaxUnavailable to 1",
			input:                []*grovecorev1alpha1.PodCliqueTemplateSpec{clique("c", 5, 3, nil)},
			strategy:             grovecorev1alpha1.RollingRecreateStrategy,
			pcsgOwnedCliqueNames: sets.New[string](),
			wantRollingUpdate:    &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](1)},
		},
		{
			name:                 "OnDelete leaves standalone PCLQ RollingUpdate nil",
			input:                []*grovecorev1alpha1.PodCliqueTemplateSpec{clique("c", 5, 3, nil)},
			strategy:             grovecorev1alpha1.OnDeleteStrategy,
			pcsgOwnedCliqueNames: sets.New[string](),
			wantRollingUpdate:    nil,
		},
		{
			name:                 "user-supplied MaxUnavailable is preserved under Coherent",
			input:                []*grovecorev1alpha1.PodCliqueTemplateSpec{clique("c", 5, 3, &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](2)})},
			strategy:             grovecorev1alpha1.CoherentStrategy,
			pcsgOwnedCliqueNames: sets.New[string](),
			wantRollingUpdate:    &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](2)},
		},
		{
			name:                 "PCSG-owned PCLQ is skipped even under Coherent",
			input:                []*grovecorev1alpha1.PodCliqueTemplateSpec{clique("c", 5, 3, nil)},
			strategy:             grovecorev1alpha1.CoherentStrategy,
			pcsgOwnedCliqueNames: sets.New("c"),
			wantRollingUpdate:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := defaultPodCliqueTemplateSpecs(tc.input, tc.strategy, tc.pcsgOwnedCliqueNames)
			require.Len(t, result, 1)
			assert.Equal(t, tc.wantRollingUpdate, result[0].RollingUpdate)
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

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := defaultPodCliqueScalingGroupConfigs(tc.input, grovecorev1alpha1.OnDeleteStrategy)
			tc.verify(t, result)
		})
	}
}

// TestDefaultPodCliqueScalingGroupConfigs_RollingUpdate covers the RollingUpdate defaulting path on PCSGs.
func TestDefaultPodCliqueScalingGroupConfigs_RollingUpdate(t *testing.T) {
	pcsg := func(minAvailable int32, ru *grovecorev1alpha1.RollingUpdateConfiguration) grovecorev1alpha1.PodCliqueScalingGroupConfig {
		return grovecorev1alpha1.PodCliqueScalingGroupConfig{
			Name:          "sg",
			Replicas:      ptr.To[int32](5),
			MinAvailable:  ptr.To(minAvailable),
			RollingUpdate: ru,
		}
	}
	tests := []struct {
		name              string
		input             []grovecorev1alpha1.PodCliqueScalingGroupConfig
		strategy          grovecorev1alpha1.UpdateStrategyType
		wantRollingUpdate *grovecorev1alpha1.RollingUpdateConfiguration
	}{
		{
			name:              "Coherent defaults PCSG MaxUnavailable to MinAvailable",
			input:             []grovecorev1alpha1.PodCliqueScalingGroupConfig{pcsg(3, nil)},
			strategy:          grovecorev1alpha1.CoherentStrategy,
			wantRollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](3)},
		},
		{
			name:              "RollingRecreate defaults PCSG MaxUnavailable to 1",
			input:             []grovecorev1alpha1.PodCliqueScalingGroupConfig{pcsg(3, nil)},
			strategy:          grovecorev1alpha1.RollingRecreateStrategy,
			wantRollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](1)},
		},
		{
			name:              "OnDelete leaves PCSG RollingUpdate nil",
			input:             []grovecorev1alpha1.PodCliqueScalingGroupConfig{pcsg(3, nil)},
			strategy:          grovecorev1alpha1.OnDeleteStrategy,
			wantRollingUpdate: nil,
		},
		{
			name:              "user-supplied MaxUnavailable is preserved under Coherent",
			input:             []grovecorev1alpha1.PodCliqueScalingGroupConfig{pcsg(3, &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](2)})},
			strategy:          grovecorev1alpha1.CoherentStrategy,
			wantRollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](2)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := defaultPodCliqueScalingGroupConfigs(tc.input, tc.strategy)
			require.Len(t, result, 1)
			assert.Equal(t, tc.wantRollingUpdate, result[0].RollingUpdate)
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

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := defaultPodSpec(tc.input)
			tc.verify(t, result)
		})
	}
}

func TestDefaultUpdateStrategy(t *testing.T) {
	tests := []struct {
		name string
		in   *grovecorev1alpha1.PodCliqueSet
		want grovecorev1alpha1.PodCliqueSetUpdateStrategy
	}{
		{
			name: "nil UpdateStrategy is populated with Coherent",
			in:   &grovecorev1alpha1.PodCliqueSet{},
			want: grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
		},
		{
			name: "empty Type is filled with Coherent",
			in: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{},
				},
			},
			want: grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
		},
		{
			name: "explicit RollingRecreate is preserved",
			in: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy},
				},
			},
			want: grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy},
		},
		{
			name: "explicit OnDelete is preserved",
			in: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy},
				},
			},
			want: grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defaultUpdateStrategy(tc.in)
			require.NotNil(t, tc.in.Spec.UpdateStrategy)
			assert.Equal(t, tc.want, *tc.in.Spec.UpdateStrategy)
		})
	}
}

func TestDefaultRollingUpdateConfiguration(t *testing.T) {
	tests := []struct {
		name         string
		existing     *grovecorev1alpha1.RollingUpdateConfiguration
		strategy     grovecorev1alpha1.UpdateStrategyType
		minAvailable int32
		want         *grovecorev1alpha1.RollingUpdateConfiguration
	}{
		{
			name:         "Coherent defaults nil RollingUpdate to MaxUnavailable=MinAvailable",
			existing:     nil,
			strategy:     grovecorev1alpha1.CoherentStrategy,
			minAvailable: 3,
			want:         &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](3)},
		},
		{
			name:         "RollingRecreate defaults nil RollingUpdate to MaxUnavailable=1",
			existing:     nil,
			strategy:     grovecorev1alpha1.RollingRecreateStrategy,
			minAvailable: 3,
			want:         &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](1)},
		},
		{
			name:         "OnDelete leaves nil RollingUpdate unchanged",
			existing:     nil,
			strategy:     grovecorev1alpha1.OnDeleteStrategy,
			minAvailable: 3,
			want:         nil,
		},
		{
			name:         "user-supplied MaxUnavailable is preserved under Coherent",
			existing:     &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](7)},
			strategy:     grovecorev1alpha1.CoherentStrategy,
			minAvailable: 3,
			want:         &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](7)},
		},
		{
			name:         "user-supplied MaxUnavailable is preserved under RollingRecreate",
			existing:     &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](5)},
			strategy:     grovecorev1alpha1.RollingRecreateStrategy,
			minAvailable: 3,
			want:         &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](5)},
		},
		{
			name:         "Coherent fills MaxUnavailable on an existing struct with nil MaxUnavailable",
			existing:     &grovecorev1alpha1.RollingUpdateConfiguration{},
			strategy:     grovecorev1alpha1.CoherentStrategy,
			minAvailable: 4,
			want:         &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](4)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := defaultRollingUpdateConfiguration(tc.existing, tc.strategy, tc.minAvailable)
			assert.Equal(t, tc.want, got)
		})
	}
}
