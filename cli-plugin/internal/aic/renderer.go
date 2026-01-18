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
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

// Renderer generates Grove manifests from AIConfigurator output.
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

// RenderDeploymentYAML generates PodCliqueSet YAML from a deployment plan.
func (r *Renderer) RenderDeploymentYAML(plan *DeploymentPlan) error {
	yamlContent, err := r.RenderPodCliqueSet(plan)
	if err != nil {
		return fmt.Errorf("failed to render %s YAML: %w", plan.Mode, err)
	}

	// Write YAML to file
	if err := os.WriteFile(plan.OutputPath, []byte(yamlContent), 0644); err != nil {
		return fmt.Errorf("failed to write YAML to %s: %w", plan.OutputPath, err)
	}

	return nil
}

// RenderPodCliqueSet generates a PodCliqueSet YAML manifest from a deployment plan.
func (r *Renderer) RenderPodCliqueSet(plan *DeploymentPlan) (string, error) {
	// Use namespace and image from plan if provided, otherwise use renderer defaults
	namespace := r.namespace
	if plan.Namespace != "" {
		namespace = plan.Namespace
	}

	// Image priority: plan.Image > r.image > config.K8s.K8sImage > default
	image := r.image
	if plan.Image != "" {
		image = plan.Image
	}
	if image == "" && plan.Config != nil && plan.Config.K8s.K8sImage != "" {
		image = plan.Config.K8s.K8sImage
	}
	if image == "" {
		// Final fallback to a sensible default
		image = "nvcr.io/nvidia/ai-dynamo/vllm-runtime:latest"
	}

	// Generate resource name
	name := r.generateResourceName(plan.ModelName, plan.BackendName, plan.Mode)

	// Create the PodCliqueSet
	pcs := &corev1alpha1.PodCliqueSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "grove.io/v1alpha1",
			Kind:       "PodCliqueSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       name,
				"app.kubernetes.io/component":  "inference",
				"app.kubernetes.io/managed-by": "kubectl-grove",
				"grove.io/model":               strings.ToLower(plan.ModelName),
				"grove.io/backend":             strings.ToLower(plan.BackendName),
			},
		},
		Spec: corev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: corev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: r.generateCliques(plan, image),
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

// generateCliques generates the PodClique templates from a deployment plan.
func (r *Renderer) generateCliques(plan *DeploymentPlan, image string) []*corev1alpha1.PodCliqueTemplateSpec {
	var cliques []*corev1alpha1.PodCliqueTemplateSpec
	config := plan.Config

	if plan.Mode == DeploymentModeDisagg {
		// Disaggregated mode: separate prefill and decode cliques
		if config.Workers.PrefillWorkers > 0 {
			prefillParams := GetWorkerParams(config.Params.Prefill)
			gpusPerWorker := config.Workers.PrefillGPUsPerWorker
			if gpusPerWorker == 0 {
				gpusPerWorker = prefillParams.TensorParallelSize
			}

			prefillClique := r.createCliqueTemplate(
				"prefill",
				"prefill",
				config.Workers.PrefillWorkers,
				gpusPerWorker,
				prefillParams,
				plan.ModelName,
				plan.BackendName,
				image,
				true,
			)
			cliques = append(cliques, prefillClique)
		}

		if config.Workers.DecodeWorkers > 0 {
			decodeParams := GetWorkerParams(config.Params.Decode)
			gpusPerWorker := config.Workers.DecodeGPUsPerWorker
			if gpusPerWorker == 0 {
				gpusPerWorker = decodeParams.TensorParallelSize
			}

			decodeClique := r.createCliqueTemplate(
				"decode",
				"decode",
				config.Workers.DecodeWorkers,
				gpusPerWorker,
				decodeParams,
				plan.ModelName,
				plan.BackendName,
				image,
				false,
			)
			cliques = append(cliques, decodeClique)
		}
	} else {
		// Aggregated mode: single worker clique
		if config.Workers.AggWorkers > 0 {
			aggParams := GetWorkerParams(config.Params.Agg)
			gpusPerWorker := config.Workers.AggGPUsPerWorker
			if gpusPerWorker == 0 {
				gpusPerWorker = aggParams.TensorParallelSize
			}

			workerClique := r.createCliqueTemplate(
				"worker",
				"worker",
				config.Workers.AggWorkers,
				gpusPerWorker,
				aggParams,
				plan.ModelName,
				plan.BackendName,
				image,
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
	replicas, gpusPerWorker int,
	params WorkerParams,
	model, backend, image string,
	isPrefillWorker bool,
) *corev1alpha1.PodCliqueTemplateSpec {
	// Build container args based on backend
	args := r.buildContainerArgs(model, backend, params, isPrefillWorker)

	// Calculate GPU resources
	gpuQuantity := resource.MustParse(fmt.Sprintf("%d", gpusPerWorker))

	// Build environment variables
	envVars := []corev1.EnvVar{
		{
			Name:  "TENSOR_PARALLEL_SIZE",
			Value: fmt.Sprintf("%d", params.TensorParallelSize),
		},
	}
	if params.PipelineParallelSize > 0 {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "PIPELINE_PARALLEL_SIZE",
			Value: fmt.Sprintf("%d", params.PipelineParallelSize),
		})
	}

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
						Image:      image,
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
						Env: envVars,
					},
				},
				RestartPolicy: corev1.RestartPolicyAlways,
			},
		},
	}
}

// buildContainerArgs builds the container arguments based on backend and worker type.
func (r *Renderer) buildContainerArgs(model, backend string, params WorkerParams, isPrefillWorker bool) []string {
	args := []string{
		"--model", model,
	}

	// Add tensor parallelism
	if params.TensorParallelSize > 0 {
		args = append(args, "--tensor-parallel-size", fmt.Sprintf("%d", params.TensorParallelSize))
	}

	// Add pipeline parallelism
	if params.PipelineParallelSize > 0 {
		args = append(args, "--pipeline-parallel-size", fmt.Sprintf("%d", params.PipelineParallelSize))
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
	if mode == DeploymentModeDisagg {
		name = fmt.Sprintf("%s-disagg", name)
	} else if mode == DeploymentModeAgg {
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
