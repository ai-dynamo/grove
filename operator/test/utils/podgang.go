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

package utils

import (
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// PodGangBuilder is a builder for PodGang objects (scheduler API).
type PodGangBuilder struct {
	pg *groveschedulerv1alpha1.PodGang
}

// NewPodGangBuilder creates a new PodGangBuilder.
func NewPodGangBuilder(name, namespace string) *PodGangBuilder {
	return &PodGangBuilder{
		pg: createEmptyPodGang(name, namespace),
	}
}

// WithGeneration sets the Generation on the PodGang.
func (b *PodGangBuilder) WithGeneration(generation int64) *PodGangBuilder {
	b.pg.SetGeneration(generation)
	return b
}

// WithManaged sets the managed-by label so the PodGang is considered operator-managed.
func (b *PodGangBuilder) WithManaged(managed bool) *PodGangBuilder {
	if b.pg.Labels == nil {
		b.pg.Labels = make(map[string]string)
	}
	if managed {
		b.pg.Labels[apicommon.LabelManagedByKey] = apicommon.LabelManagedByValue
	} else {
		delete(b.pg.Labels, apicommon.LabelManagedByKey)
	}
	return b
}

// WithPodGroups sets the Spec.PodGroups slice.
func (b *PodGangBuilder) WithPodGroups(groups []groveschedulerv1alpha1.PodGroup) *PodGangBuilder {
	b.pg.Spec.PodGroups = groups
	return b
}

// WithPodGroup adds a single PodGroup (convenience for tests that need one group).
func (b *PodGangBuilder) WithPodGroup(name string, minReplicas int32) *PodGangBuilder {
	b.pg.Spec.PodGroups = append(b.pg.Spec.PodGroups, groveschedulerv1alpha1.PodGroup{
		Name:        name,
		MinReplicas: minReplicas,
	})
	return b
}

// WithSchedulerName sets the grove.io/scheduler-name label on the PodGang.
func (b *PodGangBuilder) WithSchedulerName(name string) *PodGangBuilder {
	if b.pg.Labels == nil {
		b.pg.Labels = make(map[string]string)
	}
	b.pg.Labels[apicommon.LabelSchedulerName] = name
	return b
}

// WithLabels merges the given labels onto the PodGang.
func (b *PodGangBuilder) WithLabels(labels map[string]string) *PodGangBuilder {
	if b.pg.Labels == nil {
		b.pg.Labels = make(map[string]string)
	}
	for k, v := range labels {
		b.pg.Labels[k] = v
	}
	return b
}

// WithDeletionTimestamp sets the DeletionTimestamp on the PodGang to simulate a pending deletion.
func (b *PodGangBuilder) WithDeletionTimestamp() *PodGangBuilder {
	now := metav1.NewTime(time.Now())
	b.pg.DeletionTimestamp = &now
	return b
}

// WithOwnerReference sets a controller owner reference of the given kind, name and uid on the
// PodGang. It lets a test model a PodGang owned by a specific controller instance, so ownership by
// UID (not just name) can be exercised.
func (b *PodGangBuilder) WithOwnerReference(kind, name string, uid types.UID) *PodGangBuilder {
	b.pg.OwnerReferences = append(b.pg.OwnerReferences, metav1.OwnerReference{
		APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
		Kind:       kind,
		Name:       name,
		UID:        uid,
		Controller: ptr.To(true),
	})
	return b
}

// WithLastScheduled sets Status.LastScheduled to mark the PodGang as having been scheduled at least once.
func (b *PodGangBuilder) WithLastScheduled() *PodGangBuilder {
	now := metav1.Now()
	b.pg.Status.LastScheduled = &now
	return b
}

// WithPodGroupPods adds a PodGroup named name whose PodReferences list the given pods, all in the
// PodGang's namespace.
func (b *PodGangBuilder) WithPodGroupPods(name string, podNames ...string) *PodGangBuilder {
	refs := lo.Map(podNames, func(podName string, _ int) groveschedulerv1alpha1.NamespacedName {
		return groveschedulerv1alpha1.NamespacedName{Namespace: b.pg.Namespace, Name: podName}
	})
	b.pg.Spec.PodGroups = append(b.pg.Spec.PodGroups, groveschedulerv1alpha1.PodGroup{Name: name, PodReferences: refs})
	return b
}

// Build returns the PodGang.
func (b *PodGangBuilder) Build() *groveschedulerv1alpha1.PodGang {
	return b.pg
}

func createEmptyPodGang(name, namespace string) *groveschedulerv1alpha1.PodGang {
	return &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{},
	}
}
