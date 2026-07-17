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

// NewPodGangEntryBuilder creates a PodGangEntryBuilder for an entry with the given name, generation
// hash, and grove.io/epoch label.
func NewPodGangEntryBuilder(name, pcsGenerationHash, epoch string) *PodGangEntryBuilder {
	return &PodGangEntryBuilder{
		entry: grovecorev1alpha1.PodGangEntry{
			Name:                       name,
			PodCliqueSetGenerationHash: pcsGenerationHash,
			Labels:                     map[string]string{apicommon.LabelEpoch: epoch},
		},
	}
}

// WithEpochAnchor sets the IsEpochAnchor flag on the entry.
func (b *PodGangEntryBuilder) WithEpochAnchor(isAnchor bool) *PodGangEntryBuilder {
	b.entry.IsEpochAnchor = isAnchor
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

// WithUpdateProgress sets Status.UpdateProgress on the PodGangMap.
func (b *PodGangMapBuilder) WithUpdateProgress(progress *grovecorev1alpha1.PodGangMapUpdateProgress) *PodGangMapBuilder {
	b.pgm.Status.UpdateProgress = progress
	return b
}

// Build returns the constructed PodGangMap.
func (b *PodGangMapBuilder) Build() *grovecorev1alpha1.PodGangMap {
	return b.pgm
}
