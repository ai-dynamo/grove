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

package utils

import (
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// PodCliqueScalingGroupBuilder provides a fluent interface for building test PodCliqueScalingGroup objects.
type PodCliqueScalingGroupBuilder struct {
	pcsg *grovecorev1alpha1.PodCliqueScalingGroup
}

// NewPodCliqueScalingGroupBuilder creates a new PodCliqueScalingGroupBuilder with basic configuration.
func NewPodCliqueScalingGroupBuilder(name, namespace, pcsName string, pcsReplicaIndex int) *PodCliqueScalingGroupBuilder {
	return &PodCliqueScalingGroupBuilder{
		pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					apicommon.LabelManagedByKey:             apicommon.LabelManagedByValue,
					apicommon.LabelPartOfKey:                pcsName,
					apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
				},
			},
			Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
				Replicas:     1,
				MinAvailable: ptr.To(int32(1)),
			},
			Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{},
		},
	}
}

// WithReplicas sets the number of replicas for the PodCliqueScalingGroup.
func (b *PodCliqueScalingGroupBuilder) WithReplicas(replicas int32) *PodCliqueScalingGroupBuilder {
	b.pcsg.Spec.Replicas = replicas
	return b
}

// WithMinAvailable sets the MinAvailable field for the PodCliqueScalingGroup.
func (b *PodCliqueScalingGroupBuilder) WithMinAvailable(minAvailable int32) *PodCliqueScalingGroupBuilder {
	b.pcsg.Spec.MinAvailable = ptr.To(minAvailable)
	return b
}

// WithCliqueNames sets the CliqueNames field for the PodCliqueScalingGroup.
func (b *PodCliqueScalingGroupBuilder) WithCliqueNames(cliqueNames []string) *PodCliqueScalingGroupBuilder {
	b.pcsg.Spec.CliqueNames = cliqueNames
	return b
}

// WithLabels adds labels to the PodCliqueScalingGroup.
func (b *PodCliqueScalingGroupBuilder) WithLabels(labels map[string]string) *PodCliqueScalingGroupBuilder {
	if b.pcsg.Labels == nil {
		b.pcsg.Labels = make(map[string]string)
	}
	for k, v := range labels {
		b.pcsg.Labels[k] = v
	}
	return b
}

// WithOwnerReference adds an owner reference to the PodCliqueScalingGroup.
func (b *PodCliqueScalingGroupBuilder) WithOwnerReference(kind, name, uid string) *PodCliqueScalingGroupBuilder {
	ownerRef := metav1.OwnerReference{
		Kind: kind,
		Name: name,
		UID:  types.UID("test-uid"),
	}
	if uid != "" {
		ownerRef.UID = types.UID(uid)
	}
	b.pcsg.OwnerReferences = append(b.pcsg.OwnerReferences, ownerRef)
	return b
}

// WithOptions applies option functions to customize the PodCliqueScalingGroup.
func (b *PodCliqueScalingGroupBuilder) WithOptions(opts ...PCSGOption) *PodCliqueScalingGroupBuilder {
	for _, opt := range opts {
		opt(b.pcsg)
	}
	return b
}

// Build returns the constructed PodCliqueScalingGroup.
func (b *PodCliqueScalingGroupBuilder) Build() *grovecorev1alpha1.PodCliqueScalingGroup {
	return b.pcsg
}
