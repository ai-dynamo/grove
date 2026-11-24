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

package podcliquesetreplica

import (
	"testing"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
)

func TestComputeGenerationHash(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		Build()
	legacyHash := computeLegacyGenerationHash(pcs)
	generationHash := computeGenerationHash(pcs)

	schedulingChange := pcs.DeepCopy()
	schedulingChange.Spec.Template.Cliques[0].Spec.MinAvailable = ptr.To[int32](1)
	assert.Equal(t, legacyHash, computeLegacyGenerationHash(schedulingChange))
	assert.NotEqual(t, generationHash, computeGenerationHash(schedulingChange))

	contentChange := pcs.DeepCopy()
	contentChange.Spec.Template.Cliques[0].Annotations = map[string]string{"revision": "new"}
	assert.NotEqual(t, legacyHash, computeLegacyGenerationHash(contentChange))
	assert.NotEqual(t, generationHash, computeGenerationHash(contentChange))

	pcsgChange := pcs.DeepCopy()
	pcsgChange.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1beta1.PodCliqueScalingGroupConfig{{
		Name:         "workers",
		MinAvailable: ptr.To[int32](2),
	}}
	assert.NotEqual(t, generationHash, computeGenerationHash(pcsgChange))
}

func TestBuildPCSSchedulingConfig(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker-b").WithReplicas(3).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker-a").WithReplicas(4).WithMinAvailable(2).Build()).
		WithPodCliqueScalingGroupConfig(grovecorev1beta1.PodCliqueScalingGroupConfig{Name: "group-b", MinAvailable: ptr.To[int32](2)}).
		WithPodCliqueScalingGroupConfig(grovecorev1beta1.PodCliqueScalingGroupConfig{Name: "group-a"}).
		Build()

	want := pcsSchedulingConfig{
		podCliques: []resourceSchedulingInfo{
			{name: "worker-a", minAvailable: 2},
			{name: "worker-b", minAvailable: 3},
		},
		podCliqueScalingGroups: []resourceSchedulingInfo{
			{name: "group-a", minAvailable: 1},
			{name: "group-b", minAvailable: 2},
		},
	}
	assert.Equal(t, want, buildPCSSchedulingConfig(pcs))

	reordered := pcs.DeepCopy()
	reordered.Spec.Template.Cliques[0], reordered.Spec.Template.Cliques[1] = reordered.Spec.Template.Cliques[1], reordered.Spec.Template.Cliques[0]
	reordered.Spec.Template.PodCliqueScalingGroupConfigs[0], reordered.Spec.Template.PodCliqueScalingGroupConfigs[1] = reordered.Spec.Template.PodCliqueScalingGroupConfigs[1], reordered.Spec.Template.PodCliqueScalingGroupConfigs[0]
	assert.Equal(t, want, buildPCSSchedulingConfig(reordered))
	assert.Equal(t, computeSchedulingConfigurationHash(want), computeSchedulingConfigurationHash(buildPCSSchedulingConfig(reordered)))

	explicitDefaults := pcs.DeepCopy()
	explicitDefaults.Spec.Template.Cliques[0].Spec.MinAvailable = ptr.To(explicitDefaults.Spec.Template.Cliques[0].Spec.Replicas)
	explicitDefaults.Spec.Template.PodCliqueScalingGroupConfigs[1].MinAvailable = ptr.To[int32](1)
	assert.Equal(t, want, buildPCSSchedulingConfig(explicitDefaults))
	assert.Equal(t, computeGenerationHash(pcs), computeGenerationHash(explicitDefaults))
}

// TestComputePCSHashesStableEncoding pins the released legacy hash and the value-only new hash formats.
func TestComputePCSHashesStableEncoding(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("hash-contract", "default", "uid").
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		WithPodCliqueScalingGroupConfig(grovecorev1beta1.PodCliqueScalingGroupConfig{Name: "workers"}).Build()
	legacyHash, schedulingHash, generationHash := computePCSHashes(pcs)
	assert.Equal(t, "58fdfc6fb659d4f6c595", legacyHash)
	assert.Equal(t, "57dcdc4d75ddcfb49bcf", schedulingHash)
	assert.Equal(t, "56cd9fc675f8b959f78", generationHash)
}

func TestComputePCSHashesIgnoresPCSReplicaCount(t *testing.T) {
	t.Parallel()
	pcs := generationTestPCS()
	legacyHash, schedulingHash, generationHash := computePCSHashes(pcs)
	pcs.Spec.Replicas = 0
	scaledLegacyHash, scaledSchedulingHash, scaledGenerationHash := computePCSHashes(pcs)
	assert.Equal(t, legacyHash, scaledLegacyHash)
	assert.Equal(t, schedulingHash, scaledSchedulingHash)
	assert.Equal(t, generationHash, scaledGenerationHash)
}

func TestComputePCSConfigurationHashDistinguishesValueBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		left  []string
		right []string
	}{
		{name: "adjacent values", left: []string{"ab", "c"}, right: []string{"a", "bc"}},
		{name: "empty value", left: []string{"a"}, right: []string{"", "a"}},
		{name: "embedded separators", left: []string{"a:1", "b"}, right: []string{"a", "1:b"}},
		{name: "hash domains", left: []string{"scheduling-v1", "value"}, right: []string{"generation-v1", "value"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.NotEqual(t, computePCSConfigurationHash(tc.left...), computePCSConfigurationHash(tc.right...))
		})
	}
}
