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

package podgangmap

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
)

func TestComputeMVUTemplate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("fe").WithReplicas(5).WithMinAvailable(2).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("other").WithReplicas(4).WithMinAvailable(1).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("pf-leader").WithReplicas(1).WithMinAvailable(1).Build()).
		WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{
			Name:         "pf",
			CliqueNames:  []string{"pf-leader"},
			MinAvailable: ptr.To[int32](3),
		}).
		WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
			InScopeStandalonePodCliques:   []string{"fe"},
			InScopePodCliqueScalingGroups: []string{"pf"},
		}).
		Build()

	mvuTmpl := computeMVUTemplate(pcs)
	assert.Equal(t, map[string]int32{"fe": 2}, mvuTmpl.standalonePCLQs)
	assert.Equal(t, map[string]int32{"pf": 3}, mvuTmpl.pcsgs)
}
