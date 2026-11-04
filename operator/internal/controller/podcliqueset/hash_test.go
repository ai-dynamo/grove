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

package podcliqueset

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
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
	pcsgChange.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{{
		Name:         "workers",
		MinAvailable: ptr.To[int32](2),
	}}
	assert.NotEqual(t, generationHash, computeGenerationHash(pcsgChange))
}

func TestBuildPCSSchedulingConfig(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker-b").WithReplicas(3).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker-a").WithReplicas(4).WithMinAvailable(2).Build()).
		WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{Name: "group-b", MinAvailable: ptr.To[int32](2)}).
		WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{Name: "group-a"}).
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

	explicitDefaults := pcs.DeepCopy()
	explicitDefaults.Spec.Template.Cliques[0].Spec.MinAvailable = ptr.To(explicitDefaults.Spec.Template.Cliques[0].Spec.Replicas)
	explicitDefaults.Spec.Template.PodCliqueScalingGroupConfigs[1].MinAvailable = ptr.To[int32](1)
	assert.Equal(t, want, buildPCSSchedulingConfig(explicitDefaults))
}
