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

package utils

import (
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// PodCliqueTemplateSpecBuilder is a builder for creating PodCliqueTemplateSpec objects.
type PodCliqueTemplateSpecBuilder struct {
	pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec
}

// NewPodCliqueTemplateSpecBuilder creates a new PodCliqueTemplateSpecBuilder.
func NewPodCliqueTemplateSpecBuilder(name string) *PodCliqueTemplateSpecBuilder {
	return &PodCliqueTemplateSpecBuilder{
		pclqTemplateSpec: createDefaultPodCliqueTemplateSpec(name),
	}
}

// NewBasicPodCliqueTemplateSpec creates a basic PodClique template without topology constraints.
// This is a convenience function for tests that need a simple PodClique with default configuration.
func NewBasicPodCliqueTemplateSpec(name string) *grovecorev1alpha1.PodCliqueTemplateSpec {
	return &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: name,
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas: 1,
			RoleName: name + "-role",
		},
	}
}

// Build creates a PodCliqueTemplateSpec object.
func (b *PodCliqueTemplateSpecBuilder) Build() *grovecorev1alpha1.PodCliqueTemplateSpec {
	// Only apply default PodSpec if no containers were configured
	if len(b.pclqTemplateSpec.Spec.PodSpec.Containers) == 0 && len(b.pclqTemplateSpec.Spec.PodSpec.InitContainers) == 0 {
		b.withDefaultPodSpec()
	}
	return b.pclqTemplateSpec
}

// WithReplicas sets the number of replicas for the PodCliqueTemplateSpec.
func (b *PodCliqueTemplateSpecBuilder) WithReplicas(replicas int32) *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.Spec.Replicas = replicas
	return b
}

// WithLabels sets the labels for the PodCliqueTemplateSpec.
func (b *PodCliqueTemplateSpecBuilder) WithLabels(labels map[string]string) *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.Labels = labels
	return b
}

// WithStartsAfter sets the StartsAfter field for the PodCliqueTemplateSpec.
func (b *PodCliqueTemplateSpecBuilder) WithStartsAfter(startsAfter []string) *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.Spec.StartsAfter = startsAfter
	return b
}

// WithMinAvailable sets the MinAvailable field for the PodCliqueTemplateSpec.
func (b *PodCliqueTemplateSpecBuilder) WithMinAvailable(minAvailable int32) *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.Spec.MinAvailable = &minAvailable
	return b
}

// WithScaleConfig sets the complete ScaleConfig for the PodCliqueTemplateSpec.
func (b *PodCliqueTemplateSpecBuilder) WithScaleConfig(minReplicas *int32, maxReplicas int32) *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.Spec.ScaleConfig = &grovecorev1alpha1.AutoScalingConfig{
		MinReplicas: minReplicas,
		MaxReplicas: maxReplicas,
	}
	return b
}

// WithAutoScaleMinReplicas sets the minimum replicas in ScaleConfig for the PodClique.
func (b *PodCliqueTemplateSpecBuilder) WithAutoScaleMinReplicas(minimum int32) *PodCliqueTemplateSpecBuilder {
	if b.pclqTemplateSpec.Spec.ScaleConfig == nil {
		b.pclqTemplateSpec.Spec.ScaleConfig = &grovecorev1alpha1.AutoScalingConfig{}
	}
	b.pclqTemplateSpec.Spec.ScaleConfig.MinReplicas = &minimum
	return b
}

// WithAutoScaleMaxReplicas sets the maximum replicas in ScaleConfig for the PodClique.
func (b *PodCliqueTemplateSpecBuilder) WithAutoScaleMaxReplicas(maximum int32) *PodCliqueTemplateSpecBuilder {
	if b.pclqTemplateSpec.Spec.ScaleConfig == nil {
		b.pclqTemplateSpec.Spec.ScaleConfig = &grovecorev1alpha1.AutoScalingConfig{}
	}
	b.pclqTemplateSpec.Spec.ScaleConfig.MaxReplicas = maximum
	return b
}

// WithRoleName sets the RoleName for the PodCliqueTemplateSpec.
func (b *PodCliqueTemplateSpecBuilder) WithRoleName(roleName string) *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.Spec.RoleName = roleName
	return b
}

// WithPodSpec sets a custom PodSpec for the PodCliqueTemplateSpec.
func (b *PodCliqueTemplateSpecBuilder) WithPodSpec(podSpec corev1.PodSpec) *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.Spec.PodSpec = podSpec
	return b
}

// WithTopologyConstraint sets the TopologyConstraint for the PodCliqueTemplateSpec.
func (b *PodCliqueTemplateSpecBuilder) WithTopologyConstraint(constraint *grovecorev1alpha1.TopologyConstraint) *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.TopologyConstraint = constraint
	return b
}

// WithContainer adds a container to the PodSpec.
func (b *PodCliqueTemplateSpecBuilder) WithContainer(container corev1.Container) *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.Spec.PodSpec.Containers = append(b.pclqTemplateSpec.Spec.PodSpec.Containers, container)
	return b
}

// WithInitContainer adds an init container to the PodSpec.
func (b *PodCliqueTemplateSpecBuilder) WithInitContainer(container corev1.Container) *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.Spec.PodSpec.InitContainers = append(b.pclqTemplateSpec.Spec.PodSpec.InitContainers, container)
	return b
}

// NewGPUContainer creates a container with GPU resources.
func NewGPUContainer(name, image string, gpuCount int64) corev1.Container {
	return corev1.Container{
		Name:  name,
		Image: image,
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				constants.GPUResourceName: *resource.NewQuantity(gpuCount, resource.DecimalSI),
			},
		},
	}
}

// NewContainer creates a simple container without GPU resources.
func NewContainer(name, image string) corev1.Container {
	return corev1.Container{
		Name:  name,
		Image: image,
	}
}

func (b *PodCliqueTemplateSpecBuilder) withDefaultPodSpec() *PodCliqueTemplateSpecBuilder {
	b.pclqTemplateSpec.Spec.PodSpec = NewPodWithBuilderWithDefaultSpec("test-name", "test-ns").Build().Spec
	return b
}

func createDefaultPodCliqueTemplateSpec(name string) *grovecorev1alpha1.PodCliqueTemplateSpec {
	return &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: name,
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas: 1,
		},
	}
}
