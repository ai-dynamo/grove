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

package podtemplatehash

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestCompute tests computing hash from pod template specs.
func TestComput(t *testing.T) {
	podSpec1 := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"app": "test"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "container-1",
					Image: "image:v1",
				},
			},
		},
	}

	podSpec2 := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"app": "test"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "container-1",
					Image: "image:v2",
				},
			},
		},
	}

	// Same spec should produce same hash
	hash1 := Compute(podSpec1)
	hash2 := Compute(podSpec1)
	assert.Equal(t, hash1, hash2)

	// Different specs should produce different hashes
	hash3 := Compute(podSpec2)
	assert.NotEqual(t, hash1, hash3)

	// Multiple specs should produce a consistent hash
	hash4 := Compute(podSpec1, podSpec2)
	hash5 := Compute(podSpec1, podSpec2)
	assert.Equal(t, hash4, hash5)
}
