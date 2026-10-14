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

// Package podtemplatehash owns the canonical hashing of Grove pod templates.
package podtemplatehash

import (
	"fmt"
	"hash/fnv"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/dump"
	"k8s.io/apimachinery/pkg/util/rand"
)

// Compute returns the canonical hash for one or more PodTemplateSpec.
func Compute(podTemplateSpecs ...*corev1.PodTemplateSpec) string {
	hasher := fnv.New64a()
	for _, podTemplateSpec := range podTemplateSpecs {
		_, _ = fmt.Fprintf(hasher, "%v", dump.ForHash(podTemplateSpec))
	}
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum64()))
}

// PodTemplateSpec constructs a corev1.PodTemplateSpec for the given PodCliqueTemplateSpec.
// Its primary purpose is for constructing the intended hash for rolling updates.
func PodTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet, podTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec) *corev1.PodTemplateSpec {
	template := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      podTemplateSpec.Labels,
			Annotations: podTemplateSpec.Annotations,
		},
		Spec: podTemplateSpec.Spec.PodSpec,
	}

	// If priorityClassName is unset, fallback to the PCS priorityClassName
	// so that any changes cause pods to be updated.
	if template.Spec.PriorityClassName == "" {
		template.Spec.PriorityClassName = pcs.Spec.Template.PriorityClassName
	}

	return template
}
