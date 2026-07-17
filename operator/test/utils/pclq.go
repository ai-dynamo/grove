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
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// PodCliqueBuilder is a builder for creating PodClique objects.
// This should primarily be used for tests.
type PodCliqueBuilder struct {
	pcsName         string
	pcsReplicaIndex int32
	pclq            *grovecorev1alpha1.PodClique
}

// NewPodCliqueBuilder creates a new PodCliqueBuilder.
func NewPodCliqueBuilder(pcsName string, pcsUID types.UID, pclqTemplateName, namespace string, pcsReplicaIndex int32) *PodCliqueBuilder {
	return &PodCliqueBuilder{
		pcsName:         pcsName,
		pcsReplicaIndex: pcsReplicaIndex,
		pclq:            createDefaultPodCliqueWithoutPodSpec(pcsName, pcsUID, pclqTemplateName, namespace, pcsReplicaIndex),
	}
}

// NewPCSGPodCliqueBuilder creates a PodClique that belongs to a PodCliqueScalingGroup.
func NewPCSGPodCliqueBuilder(name, namespace, pcsName, pcsgName string, pcsReplicaIndex, pcsgReplicaIndex int) *PodCliqueBuilder {
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				apicommon.LabelManagedByKey:                      apicommon.LabelManagedByValue,
				apicommon.LabelPartOfKey:                         pcsName,
				apicommon.LabelPodCliqueScalingGroup:             pcsgName,
				apicommon.LabelComponentKey:                      apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
				apicommon.LabelPodCliqueSetReplicaIndex:          strconv.Itoa(pcsReplicaIndex),
				apicommon.LabelPodCliqueScalingGroupReplicaIndex: strconv.Itoa(pcsgReplicaIndex),
			},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas:     1,
			MinAvailable: ptr.To(int32(1)),
		},
		Status: grovecorev1alpha1.PodCliqueStatus{},
	}

	return &PodCliqueBuilder{
		pcsName:         pcsName,
		pcsReplicaIndex: int32(pcsReplicaIndex),
		pclq:            pclq,
	}
}

// WithLabels merges the passed labels with default labels.
// Passed in labels will overwrite default labels with the same keys.
func (b *PodCliqueBuilder) WithLabels(labels map[string]string) *PodCliqueBuilder {
	b.pclq.Labels = lo.Assign(b.pclq.Labels, labels)
	return b
}

// WithReplicas sets the number of replicas for the PodClique.
// Default is set to 1.
func (b *PodCliqueBuilder) WithReplicas(replicas int32) *PodCliqueBuilder {
	b.pclq.Spec.Replicas = replicas
	return b
}

// WithStartsAfter sets the StartsAfter field for the PodClique.
func (b *PodCliqueBuilder) WithStartsAfter(pclqTemplateNames []string) *PodCliqueBuilder {
	pclqDependencies := lo.Map(pclqTemplateNames, func(pclqTemplateName string, _ int) string {
		return apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: b.pcsName, Replica: int(b.pcsReplicaIndex)}, pclqTemplateName)
	})
	b.pclq.Spec.StartsAfter = pclqDependencies
	return b
}

// WithAutoScaleMaxReplicas sets the maximum replicas in ScaleConfig for the PodClique.
func (b *PodCliqueBuilder) WithAutoScaleMaxReplicas(maximum int32) *PodCliqueBuilder {
	b.pclq.Spec.ScaleConfig = &grovecorev1alpha1.AutoScalingConfig{
		MaxReplicas: maximum,
	}
	return b
}

// WithOwnerReference sets the owner reference for the PodClique from individual values.
func (b *PodCliqueBuilder) WithOwnerReference(kind, name string, uid types.UID) *PodCliqueBuilder {
	ownerRef := metav1.OwnerReference{
		Kind: kind,
		Name: name,
		UID:  types.UID("test-uid"),
	}
	if uid != "" {
		ownerRef.UID = uid
	}
	b.pclq.OwnerReferences = append(b.pclq.OwnerReferences, ownerRef)
	return b
}

// WithPodGangMapping sets the Status.PodGangMapping for the PodClique.
func (b *PodCliqueBuilder) WithPodGangMapping(mapping map[string]int32) *PodCliqueBuilder {
	b.pclq.Status.PodGangMapping = mapping
	return b
}

// WithOptions applies option functions to customize the PodClique.
func (b *PodCliqueBuilder) WithOptions(opts ...PCLQOption) *PodCliqueBuilder {
	for _, opt := range opts {
		opt(b.pclq)
	}
	return b
}

// Build creates a PodClique object.
func (b *PodCliqueBuilder) Build() *grovecorev1alpha1.PodClique {
	_ = b.withDefaultPodSpec()
	return b.pclq
}

func (b *PodCliqueBuilder) withDefaultPodSpec() *PodCliqueBuilder {
	b.pclq.Spec.PodSpec = NewPodWithBuilderWithDefaultSpec("test-name", "test-ns").Build().Spec
	return b
}

func createDefaultPodCliqueWithoutPodSpec(pcsName string, pcsUID types.UID, pclqTemplateName, namespace string, pcsReplicaIndex int32) *grovecorev1alpha1.PodClique {
	pclqName := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsName, Replica: int(pcsReplicaIndex)}, pclqTemplateName)
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pclqName,
			Namespace: namespace,
			Labels:    getDefaultLabels(pcsName, pclqName, pcsReplicaIndex),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         grovecorev1alpha1.SchemeGroupVersion.String(),
					Kind:               constants.KindPodCliqueSet,
					Name:               pcsName,
					UID:                pcsUID,
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas:     1,
			MinAvailable: ptr.To(int32(1)),
		},
	}
}

func getDefaultLabels(pcsName, pclqName string, pcsReplicaIndex int32) map[string]string {
	pclqComponentLabels := map[string]string{
		apicommon.LabelAppNameKey:               pclqName,
		apicommon.LabelComponentKey:             apicommon.LabelComponentNamePodCliqueSetPodClique,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(int(pcsReplicaIndex)),
	}
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		pclqComponentLabels,
	)
}
