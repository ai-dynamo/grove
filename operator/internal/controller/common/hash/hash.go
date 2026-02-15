// /*
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
// */

package hash

import (
	"encoding/json"
	"fmt"
	"hash/fnv"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

// Compute computes a hash for the given PodTemplateSpecs.
// This uses FNV-64a hash with JSON marshaling for optimal performance.
// This is faster than using dump.ForHash() which uses deep reflection.
func Compute(specs ...*corev1.PodTemplateSpec) (string, error) {
	if len(specs) == 0 {
		return "", fmt.Errorf("must provide at least one PodTemplateSpec for hashing")
	}
	hasher := fnv.New64a()
	for _, spec := range specs {
		if spec == nil {
			return "", fmt.Errorf("must provide non-nil PodTemplateSpec for hashing")
		}
		specBytes, err := json.Marshal(spec)
		if err != nil {
			return "", err
		}
		if _, err = hasher.Write(specBytes); err != nil {
			return "", err
		}
	}
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum64())), nil
}

// ComputePCLQPodTemplateHash computes the pod template hash for a PodClique template spec.
// This wraps the PodCliqueTemplateSpec into a standard PodTemplateSpec and computes its hash.
// The hash includes:
// - Labels and annotations from the template
// - The entire PodSpec
// - The priority class name
// Any change to these fields will result in a different hash.
func ComputePCLQPodTemplateHash(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, priorityClassName string) (string, error) {
	if pclqTemplateSpec == nil {
		return "", fmt.Errorf("pclqTemplateSpec is nil")
	}
	podTemplateSpec := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      pclqTemplateSpec.Labels,
			Annotations: pclqTemplateSpec.Annotations,
		},
		Spec: pclqTemplateSpec.Spec.PodSpec,
	}
	podTemplateSpec.Spec.PriorityClassName = priorityClassName

	return Compute(&podTemplateSpec)
}
