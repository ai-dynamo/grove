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

package utils

import (
	"sort"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ComputePCSGenerationHash calculates the canonical PodCliqueSet generation
// hash. AnyOrder and Explicit startup modes sort cliques by name because the
// slice represents a name-keyed map-list. InOrder preserves clique order and
// includes the clique names as order keys because the sequence is part of the
// startup contract.
func ComputePCSGenerationHash(pcs *grovecorev1alpha1.PodCliqueSet) string {
	preserveCliqueOrder := isInOrderStartup(pcs)
	cliquesForHash := append([]*grovecorev1alpha1.PodCliqueTemplateSpec(nil), pcs.Spec.Template.Cliques...)
	if !preserveCliqueOrder {
		sort.SliceStable(cliquesForHash, func(i, j int) bool {
			return cliquesForHash[i].Name < cliquesForHash[j].Name
		})
	}

	podTemplateSpecs := podTemplateSpecsForPCLQTemplates(cliquesForHash, pcs.Spec.Template.PriorityClassName)
	if preserveCliqueOrder {
		orderKeys := lo.Map(cliquesForHash, func(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, _ int) string {
			return pclqTemplateSpec.Name
		})
		return k8sutils.ComputeHashWithOrderKeys(orderKeys, podTemplateSpecs...)
	}

	return k8sutils.ComputeHash(podTemplateSpecs...)
}

func podTemplateSpecsForPCLQTemplates(pclqTemplateSpecs []*grovecorev1alpha1.PodCliqueTemplateSpec, priorityClassName string) []*corev1.PodTemplateSpec {
	return lo.Map(pclqTemplateSpecs, func(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, _ int) *corev1.PodTemplateSpec {
		return newPCLQPodTemplateSpecForHash(pclqTemplateSpec, priorityClassName)
	})
}

func newPCLQPodTemplateSpecForHash(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, priorityClassName string) *corev1.PodTemplateSpec {
	podTemplateSpec := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      pclqTemplateSpec.Labels,
			Annotations: pclqTemplateSpec.Annotations,
		},
		Spec: pclqTemplateSpec.Spec.PodSpec,
	}
	podTemplateSpec.Spec.PriorityClassName = priorityClassName
	return podTemplateSpec
}

// isInOrderStartup returns true when the original clique slice order is part
// of the desired state. AnyOrder and Explicit are order-independent here.
func isInOrderStartup(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	st := grovecorev1alpha1.CliqueStartupTypeAnyOrder
	if pcs.Spec.Template.StartupType != nil {
		st = *pcs.Spec.Template.StartupType
	}
	return st == grovecorev1alpha1.CliqueStartupTypeInOrder
}
