// /*
// Copyright 2025 The Grove Authors.
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

package aic

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

// Renderer generates Grove manifests from AIConfigurator plans.
type Renderer struct {
	// namespace is the Kubernetes namespace for generated resources
	namespace string

	// image is the container image for worker pods
	image string
}

// NewRenderer creates a new manifest renderer.
func NewRenderer(namespace, image string) *Renderer {
	return &Renderer{
		namespace: namespace,
		image:     image,
	}
}

// RenderPodCliqueSet generates a PodCliqueSet YAML manifest from a plan.
func (r *Renderer) RenderPodCliqueSet(plan GeneratorPlan, model, backend string) (string, error) {
	// Generate resource name from model and backend
	name := r.generateResourceName(model, backend, plan.Mode)

	// Create the PodCliqueSet
	pcs := &corev1alpha1.PodCliqueSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "grove.io/v1alpha1",
			Kind:       "PodCliqueSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       name,
				"app.kubernetes.io/component":  "inference",
				"app.kubernetes.io/managed-by": "kubectl-grove",
				"grove.io/model":               strings.ToLower(model),
				"grove.io/backend":             strings.ToLower(backend),
			},
		},
		Spec: corev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: corev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: r.generateCliques(plan, model, backend),
			},
		},
	}

	// Marshal to YAML
	yamlBytes, err := yaml.Marshal(pcs)
	if err != nil {
		return "", fmt.Errorf("failed to marshal PodCliqueSet: %w", err)
	}

	return string(yamlBytes), nil
}

// generateCliques generates the PodClique templates for a plan.
func (r *Renderer) generateCliques(plan GeneratorPlan, model, backend string) []*corev1alpha1.PodCliqueTemplateSpec {
	var cliques []*corev1alpha1.PodCliqueTemplateSpec

	if plan.Mode == "disaggregated" {
		// Disaggregated mode: separate prefill and decode cliques
		if plan.PrefillWorkers > 0 {
			prefillClique := r.createCliqueTemplate(
				"prefill",
				"prefill",
				plan.PrefillWorkers,
				plan.PrefillTP,
				model,
				backend,
				true,
			)
			cliques = append(cliques, prefillClique)
		}

		if plan.DecodeWorkers > 0 {
			decodeClique := r.createCliqueTemplate(
				"decode",
				"decode",
				plan.DecodeWorkers,
				plan.DecodeTP,
				model,
				backend,
				false,
			)
			cliques = append(cliques, decodeClique)
		}
	} else {
		// Aggregated mode: single worker clique
		if plan.AggregatedWorkers > 0 {
			workerClique := r.createCliqueTemplate(
				"worker",
				"worker",
				plan.AggregatedWorkers,
				plan.AggregatedTP,
				model,
				backend,
				false,
			)
			cliques = append(cliques, workerClique)
		}
	}

	return cliques
}

// createCliqueTemplate creates a PodClique template specification.
func (r *Renderer) createCliqueTemplate(
	name, roleName string,
	replicas, tp int,
	model, backend string,
	isPrefillWorker bool,
) *corev1alpha1.PodCliqueTemplateSpec {
	// Build container args based on backend
	args := r.buildContainerArgs(model, backend, isPrefillWorker)

	// Calculate GPU resources
	gpuQuantity := resource.MustParse(fmt.Sprintf("%d", tp))

	return &corev1alpha1.PodCliqueTemplateSpec{
		Name: name,
		Labels: map[string]string{
			"grove.io/role": roleName,
		},
		Spec: corev1alpha1.PodCliqueSpec{
			RoleName: roleName,
			Replicas: int32(replicas),
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:       "worker",
						Image:      r.image,
						WorkingDir: "/workspace/components/backends/vllm",
						Command:    []string{"python3", "-m", "dynamo.vllm"},
						Args:       args,
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								"nvidia.com/gpu": gpuQuantity,
							},
							Requests: corev1.ResourceList{
								"nvidia.com/gpu": gpuQuantity,
							},
						},
						Env: []corev1.EnvVar{
							{
								Name:  "TENSOR_PARALLEL_SIZE",
								Value: fmt.Sprintf("%d", tp),
							},
						},
					},
				},
				RestartPolicy: corev1.RestartPolicyAlways,
			},
		},
	}
}

// buildContainerArgs builds the container arguments based on backend and worker type.
func (r *Renderer) buildContainerArgs(model, backend string, isPrefillWorker bool) []string {
	args := []string{
		"--model", model,
	}

	// Add prefill worker flag if applicable
	if isPrefillWorker {
		args = append(args, "--is-prefill-worker")
	}

	return args
}

// generateResourceName generates a Kubernetes resource name from model and backend.
func (r *Renderer) generateResourceName(model, backend, mode string) string {
	// Normalize model name
	name := strings.ToLower(model)
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, "/", "-")

	// Add backend
	name = fmt.Sprintf("%s-%s", name, strings.ToLower(backend))

	// Add mode suffix
	if mode == "disaggregated" {
		name = fmt.Sprintf("%s-disagg", name)
	} else if mode == "aggregated" {
		name = fmt.Sprintf("%s-agg", name)
	}

	// Ensure name is valid Kubernetes name (max 63 chars, lowercase alphanumeric and hyphens)
	if len(name) > 63 {
		name = name[:63]
	}

	// Remove trailing hyphens
	name = strings.TrimRight(name, "-")

	return name
}
