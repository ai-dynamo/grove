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
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// PodGangEntryBuilder provides a fluent interface for building test PodGangEntry values.
type PodGangEntryBuilder struct {
	entry grovecorev1alpha1.PodGangEntry
}

// NewPodGangEntryBuilder creates a PodGangEntryBuilder for an entry with the given generation hash
// and epoch. Epoch is the entry's identity.
func NewPodGangEntryBuilder(pcsGenerationHash, epoch string) *PodGangEntryBuilder {
	return &PodGangEntryBuilder{
		entry: grovecorev1alpha1.PodGangEntry{
			Epoch:                      epoch,
			PodCliqueSetGenerationHash: pcsGenerationHash,
		},
	}
}

// WithRole sets the Role on the entry.
func (b *PodGangEntryBuilder) WithRole(role grovecorev1alpha1.PodGangEntryRole) *PodGangEntryBuilder {
	b.entry.Role = role
	return b
}

// WithPodCliques sets the standalone PodClique pod counts on the entry.
func (b *PodGangEntryBuilder) WithPodCliques(podCliques map[string]int32) *PodGangEntryBuilder {
	b.entry.PodCliques = podCliques
	return b
}

// WithPCSGReplicaIndices sets the PodCliqueScalingGroup replica indices on the entry.
func (b *PodGangEntryBuilder) WithPCSGReplicaIndices(indices map[string][]int32) *PodGangEntryBuilder {
	b.entry.PCSGReplicaIndices = indices
	return b
}

// WithDependsOn sets the epochs this entry depends on.
func (b *PodGangEntryBuilder) WithDependsOn(epochs ...string) *PodGangEntryBuilder {
	b.entry.DependsOn = epochs
	return b
}

// Build returns the constructed PodGangEntry.
func (b *PodGangEntryBuilder) Build() grovecorev1alpha1.PodGangEntry {
	return b.entry
}

// NewPCSGPodGangEntry builds a PodGangEntry for a single PodCliqueScalingGroup, the common shape in
// tests. It sets the epoch, generation hash, role, and the PodCliqueScalingGroup replica indices the
// entry holds. Use the builder directly when an entry also needs DependsOn or standalone PodCliques.
func NewPCSGPodGangEntry(pcsGenerationHash, epoch string, role grovecorev1alpha1.PodGangEntryRole, pcsgName string, pcsgReplicaIndices ...int32) grovecorev1alpha1.PodGangEntry {
	return NewPodGangEntryBuilder(pcsGenerationHash, epoch).
		WithRole(role).
		WithPCSGReplicaIndices(map[string][]int32{pcsgName: pcsgReplicaIndices}).
		Build()
}

// PodGangMapBuilder provides a fluent interface for building test PodGangMap objects.
type PodGangMapBuilder struct {
	pgm *grovecorev1alpha1.PodGangMap
}

// NewPodGangMapBuilder creates a builder for the PodGangMap of a PodCliqueSet replica. The map is
// named <pcsName>-<replicaIndex>, carries the labels the PodGangMap component selects on, and is
// owned by the PodCliqueSet (controller reference), matching what the reconciler creates.
func NewPodGangMapBuilder(pcsName, namespace string, pcsUID types.UID, replicaIndex int) *PodGangMapBuilder {
	labels := lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelComponentKey:             apicommon.LabelComponentNamePodGangMap,
			apicommon.LabelAppNameKey:               fmt.Sprintf("%s-%d", pcsName, replicaIndex),
			apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(replicaIndex),
		},
	)
	return &PodGangMapBuilder{
		pgm: &grovecorev1alpha1.PodGangMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: pcsName, Replica: replicaIndex}),
				Namespace: namespace,
				Labels:    labels,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         grovecorev1alpha1.SchemeGroupVersion.String(),
						Kind:               "PodCliqueSet",
						Name:               pcsName,
						UID:                pcsUID,
						Controller:         ptr.To(true),
						BlockOwnerDeletion: ptr.To(true),
					},
				},
			},
			Spec: grovecorev1alpha1.PodGangMapSpec{PodCliqueSetReplicaIndex: int32(replicaIndex)},
		},
	}
}

// WithEntries sets the Spec.Entries on the PodGangMap.
func (b *PodGangMapBuilder) WithEntries(entries ...grovecorev1alpha1.PodGangEntry) *PodGangMapBuilder {
	b.pgm.Spec.Entries = entries
	return b
}

// Build returns the constructed PodGangMap.
func (b *PodGangMapBuilder) Build() *grovecorev1alpha1.PodGangMap {
	return b.pgm
}

// RolesOf returns the role of each entry in order.
func RolesOf(entries []grovecorev1alpha1.PodGangEntry) []grovecorev1alpha1.PodGangEntryRole {
	roles := make([]grovecorev1alpha1.PodGangEntryRole, 0, len(entries))
	for i := range entries {
		roles = append(roles, entries[i].Role)
	}
	return roles
}

// EntryByRole returns the first entry with the given role, or a zero entry when none matches.
func EntryByRole(entries []grovecorev1alpha1.PodGangEntry, role grovecorev1alpha1.PodGangEntryRole) grovecorev1alpha1.PodGangEntry {
	for i := range entries {
		if entries[i].Role == role {
			return entries[i]
		}
	}
	return grovecorev1alpha1.PodGangEntry{}
}
