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
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// PodCliqueSetBuilder is a builder for PodCliqueSet objects.
type PodCliqueSetBuilder struct {
	pcs *grovecorev1alpha1.PodCliqueSet
}

// NewPodCliqueSetBuilder creates a new PodCliqueSetBuilder.
func NewPodCliqueSetBuilder(name, namespace string, uid types.UID) *PodCliqueSetBuilder {
	return &PodCliqueSetBuilder{
		pcs: createEmptyPodCliqueSet(name, namespace, uid),
	}
}

// WithCliqueStartupType sets the StartupType for the PodCliqueSet.
func (b *PodCliqueSetBuilder) WithCliqueStartupType(startupType *grovecorev1alpha1.CliqueStartupType) *PodCliqueSetBuilder {
	b.pcs.Spec.Template.StartupType = startupType
	return b
}

// WithReplicas sets the number of replicas for the PodCliqueSet.
func (b *PodCliqueSetBuilder) WithReplicas(replicas int32) *PodCliqueSetBuilder {
	b.pcs.Spec.Replicas = replicas
	return b
}

// WithPodCliqueParameters is a convenience function that creates a PodCliqueTemplateSpec given the parameters and adds it to the PodCliqueSet.
func (b *PodCliqueSetBuilder) WithPodCliqueParameters(name string, replicas int32, startsAfter []string) *PodCliqueSetBuilder {
	pclqTemplateSpec := NewPodCliqueTemplateSpecBuilder(name).
		WithReplicas(replicas).
		WithStartsAfter(startsAfter).
		Build()
	return b.WithPodCliqueTemplateSpec(pclqTemplateSpec)
}

// WithPodCliqueTemplateSpec sets the PodCliqueTemplateSpec for the PodCliqueSet.
// Consumers can use PodCliqueBuilder to create a PodCliqueTemplateSpec and then use this method to add it to the PodCliqueSet.
func (b *PodCliqueSetBuilder) WithPodCliqueTemplateSpec(pclq *grovecorev1alpha1.PodCliqueTemplateSpec) *PodCliqueSetBuilder {
	b.pcs.Spec.Template.Cliques = append(b.pcs.Spec.Template.Cliques, pclq)
	return b
}

// WithPodCliqueScalingGroupConfig adds a PodCliqueScalingGroupConfig to the PodCliqueSet.
func (b *PodCliqueSetBuilder) WithPodCliqueScalingGroupConfig(config grovecorev1alpha1.PodCliqueScalingGroupConfig) *PodCliqueSetBuilder {
	b.pcs.Spec.Template.PodCliqueScalingGroupConfigs = append(b.pcs.Spec.Template.PodCliqueScalingGroupConfigs, config)
	return b
}

// WithPriorityClassName sets the PriorityClassName for the PodCliqueSet.
func (b *PodCliqueSetBuilder) WithPriorityClassName(priorityClassName string) *PodCliqueSetBuilder {
	b.pcs.Spec.Template.PriorityClassName = priorityClassName
	return b
}

// WithTerminationDelay sets the TerminationDelay for the PodCliqueSet.
func (b *PodCliqueSetBuilder) WithTerminationDelay(duration time.Duration) *PodCliqueSetBuilder {
	b.pcs.Spec.Template.TerminationDelay = &metav1.Duration{Duration: duration}
	return b
}

// WithStandaloneClique adds a standalone clique (not part of any scaling group).
func (b *PodCliqueSetBuilder) WithStandaloneClique(name string) *PodCliqueSetBuilder {
	cliqueSpec := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: name,
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas: 1,
		},
	}
	b.pcs.Spec.Template.Cliques = append(b.pcs.Spec.Template.Cliques, cliqueSpec)
	return b
}

// WithStandaloneCliqueReplicas adds a standalone clique with specific replica count.
func (b *PodCliqueSetBuilder) WithStandaloneCliqueReplicas(name string, replicas int32) *PodCliqueSetBuilder {
	cliqueSpec := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: name,
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas: replicas,
		},
	}
	b.pcs.Spec.Template.Cliques = append(b.pcs.Spec.Template.Cliques, cliqueSpec)
	return b
}

// WithScalingGroup adds a scaling group with the specified cliques.
func (b *PodCliqueSetBuilder) WithScalingGroup(name string, cliqueNames []string) *PodCliqueSetBuilder {
	return b.WithScalingGroupConfig(name, cliqueNames, 1, 1)
}

// WithScalingGroupConfig adds a scaling group with custom replicas and minAvailable.
func (b *PodCliqueSetBuilder) WithScalingGroupConfig(name string, cliqueNames []string, replicas, minAvailable int32) *PodCliqueSetBuilder {
	// Add cliques for the scaling group
	for _, cliqueName := range cliqueNames {
		cliqueSpec := &grovecorev1alpha1.PodCliqueTemplateSpec{
			Name: cliqueName,
			Spec: grovecorev1alpha1.PodCliqueSpec{
				Replicas: 1,
			},
		}
		b.pcs.Spec.Template.Cliques = append(b.pcs.Spec.Template.Cliques, cliqueSpec)
	}

	// Add scaling group config
	pcsgConfig := grovecorev1alpha1.PodCliqueScalingGroupConfig{
		Name:         name,
		CliqueNames:  cliqueNames,
		Replicas:     &replicas,
		MinAvailable: &minAvailable,
	}
	b.pcs.Spec.Template.PodCliqueScalingGroupConfigs = append(b.pcs.Spec.Template.PodCliqueScalingGroupConfigs, pcsgConfig)
	return b
}

// WithPodCliqueSetGenerationHash sets the CurrentGenerationHash in the PodCliqueSet status.
func (b *PodCliqueSetBuilder) WithPodCliqueSetGenerationHash(pcsGenerationHash *string) *PodCliqueSetBuilder {
	b.pcs.Status.CurrentGenerationHash = pcsGenerationHash
	return b
}

// WithUpdateStrategy sets the UpdateStrategy for the PodCliqueSet.
func (b *PodCliqueSetBuilder) WithUpdateStrategy(strategy *grovecorev1alpha1.PodCliqueSetUpdateStrategy) *PodCliqueSetBuilder {
	b.pcs.Spec.UpdateStrategy = strategy
	return b
}

// WithUpdateProgress sets the UpdateProgress in the PodCliqueSet status.
func (b *PodCliqueSetBuilder) WithUpdateProgress(progress *grovecorev1alpha1.PodCliqueSetUpdateProgress) *PodCliqueSetBuilder {
	b.pcs.Status.UpdateProgress = progress
	return b
}

// WithTopologyConstraint sets the TopologyConstraint for the PodCliqueSet template.
func (b *PodCliqueSetBuilder) WithTopologyConstraint(constraint *grovecorev1alpha1.TopologyConstraint) *PodCliqueSetBuilder {
	b.pcs.Spec.Template.TopologyConstraint = constraint
	return b
}

// WithAnnotations sets the annotations for the PodCliqueSet.
func (b *PodCliqueSetBuilder) WithAnnotations(annotations map[string]string) *PodCliqueSetBuilder {
	b.pcs.Annotations = annotations
	return b
}

// Build creates a PodCliqueSet object.
func (b *PodCliqueSetBuilder) Build() *grovecorev1alpha1.PodCliqueSet {
	return b.pcs
}

func createEmptyPodCliqueSet(name, namespace string, uid types.UID) *grovecorev1alpha1.PodCliqueSet {
	return &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       uid,
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
		},
	}
}
