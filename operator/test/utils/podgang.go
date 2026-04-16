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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// WithDeletionTimestamp sets the DeletionTimestamp on the PodGang to simulate a pending deletion.
func (b *PodGangBuilder) WithDeletionTimestamp() *PodGangBuilder {
	now := metav1.NewTime(time.Now())
	b.pg.DeletionTimestamp = &now
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
