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

package podgang

import (
	"testing"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func pcsWithPriorities(templatePriority string, cliquePriorities ...string) *grovecorev1beta1.PodCliqueSet {
	pcs := &grovecorev1beta1.PodCliqueSet{}
	pcs.Spec.Template.PriorityClassName = templatePriority
	for _, p := range cliquePriorities {
		pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, &grovecorev1beta1.PodCliqueTemplateSpec{
			Spec: grovecorev1beta1.PodCliqueSpec{PodSpec: corev1.PodSpec{PriorityClassName: p}},
		})
	}
	return pcs
}

func TestPodGangPriorityClassName(t *testing.T) {
	tests := []struct {
		name string
		pcs  *grovecorev1beta1.PodCliqueSet
		want string
	}{
		{"set-level priority wins", pcsWithPriorities("batch", "inference", "inference"), "batch"},
		{"cliques agree: use their class", pcsWithPriorities("", "inference", "inference"), "inference"},
		{"one clique names it, the rest do not", pcsWithPriorities("", "inference", ""), "inference"},
		{"cliques disagree: no winner is picked", pcsWithPriorities("", "inference", "train"), ""},
		{"nobody names one", pcsWithPriorities("", "", ""), ""},
		{"no cliques", pcsWithPriorities(""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, podGangPriorityClassName(tt.pcs))
		})
	}
}

func TestPodGangPriorityClassNameIgnoresNilCliques(t *testing.T) {
	pcs := pcsWithPriorities("", "inference")
	pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, nil)
	assert.Equal(t, "inference", podGangPriorityClassName(pcs))
}
