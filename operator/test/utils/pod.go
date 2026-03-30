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
	"maps"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
)

// PodBuilder is a builder for Pod objects.
type PodBuilder struct {
	pod *corev1.Pod
}

// NewPodBuilder creates a new PodBuilder with the given name and namespace.
func NewPodBuilder(name, namespace string) *PodBuilder {
	return &PodBuilder{
		pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{},
			},
		},
	}
}

// NewPodWithBuilderWithDefaultSpec creates a new PodBuilder with a default spec.
func NewPodWithBuilderWithDefaultSpec(name, namespace string) *PodBuilder {
	return &PodBuilder{
		pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: createDefaultPodSpec(),
		},
	}
}

// Build builds and returns the Pod.
func (b *PodBuilder) Build() *corev1.Pod {
	return b.pod
}

// WithOwner adds an owner reference to the Pod.
func (b *PodBuilder) WithOwner(ownerName string) *PodBuilder {
	ownerRef := metav1.OwnerReference{
		APIVersion:         grovecorev1alpha1.SchemeGroupVersion.String(),
		Kind:               constants.KindPodClique,
		Name:               ownerName,
		UID:                uuid.NewUUID(),
		BlockOwnerDeletion: ptr.To(true),
		Controller:         ptr.To(true),
	}
	b.pod.OwnerReferences = append(b.pod.OwnerReferences, ownerRef)
	return b
}

// WithSchedulerName sets the scheduler name on the Pod spec.
func (b *PodBuilder) WithSchedulerName(name string) *PodBuilder {
	b.pod.Spec.SchedulerName = name
	return b
}

// WithLabels adds labels to the Pod.
func (b *PodBuilder) WithLabels(labels map[string]string) *PodBuilder {
	if b.pod.Labels == nil {
		b.pod.Labels = make(map[string]string)
	}
	maps.Copy(b.pod.Labels, labels)
	return b
}

// MarkForTermination sets the DeletionTimestamp on the Pod.
func (b *PodBuilder) MarkForTermination() *PodBuilder {
	b.pod.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	return b
}

// WithCondition adds a condition to the Pod's status.
func (b *PodBuilder) WithCondition(cond corev1.PodCondition) *PodBuilder {
	b.pod.Status.Conditions = append(b.pod.Status.Conditions, cond)
	return b
}

// WithPhase adds a pod phase to the Pod's status.
func (b *PodBuilder) WithPhase(phase corev1.PodPhase) *PodBuilder {
	b.pod.Status.Phase = phase
	return b
}

func createDefaultPodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:    "test-container",
				Image:   "alpine:3.21",
				Command: []string{"/bin/sh", "-c", "sleep 2m"},
			},
		},
		TerminationGracePeriodSeconds: ptr.To[int64](30),
	}
}
