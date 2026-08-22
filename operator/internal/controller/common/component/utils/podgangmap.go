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
	"context"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetPodGangMap fetches a PodGangMap for a given PCS objectKey and replica index.
func GetPodGangMap(ctx context.Context, cl client.Client, pcsObjectKey client.ObjectKey, pcsReplicaIndex int) (*grovecorev1alpha1.PodGangMap, error) {
	pgm := &grovecorev1alpha1.PodGangMap{}
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: pcsObjectKey.Name, Replica: pcsReplicaIndex})
	if err := cl.Get(ctx, client.ObjectKey{Namespace: pcsObjectKey.Namespace, Name: pgmName}, pgm); err != nil {
		return nil, err
	}
	return pgm, nil
}

// ListPodGangMapsForPCS fetches all PodGangMaps owned by a PodCliqueSet.
func ListPodGangMapsForPCS(ctx context.Context, cl client.Client, pcsObjectKey client.ObjectKey) ([]grovecorev1alpha1.PodGangMap, error) {
	pgmList := &grovecorev1alpha1.PodGangMapList{}
	if err := cl.List(ctx, pgmList,
		client.InNamespace(pcsObjectKey.Namespace),
		client.MatchingLabels(lo.Assign(
			apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjectKey.Name),
			map[string]string{apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGangMap},
		))); err != nil {
		return nil, err
	}
	return pgmList.Items, nil
}

// PodGangMapByPCSReplicaIndex groups PodGangMaps by their PCS replica index.
// A PodCliqueSetReplicaIndex label that is missing or not a valid integer is a contract violation and returns an error.
func PodGangMapByPCSReplicaIndex(pgms []grovecorev1alpha1.PodGangMap) (map[int]grovecorev1alpha1.PodGangMap, error) {
	pgmByReplicaIndex := make(map[int]grovecorev1alpha1.PodGangMap, len(pgms))
	for i := range pgms {
		labelValue, ok := pgms[i].Labels[apicommon.LabelPodCliqueSetReplicaIndex]
		if !ok {
			return nil, fmt.Errorf("PodGangMap %s has no label %s", pgms[i].Name, apicommon.LabelPodCliqueSetReplicaIndex)
		}
		pcsReplicaIndex, err := strconv.Atoi(labelValue)
		if err != nil {
			return nil, fmt.Errorf("%s label on PodGangMap %s is not a valid integer: %q", apicommon.LabelPodCliqueSetReplicaIndex, pgms[i].Name, labelValue)
		}
		pgmByReplicaIndex[pcsReplicaIndex] = pgms[i]
	}
	return pgmByReplicaIndex, nil
}

// EpochForPCSGReplica returns the epoch of the PodGangMap entry a PodCliqueScalingGroup replica index
// belongs to. Callers use it to build the PodGang name for that replica.
func EpochForPCSGReplica(pgm *grovecorev1alpha1.PodGangMap, pcsgName string, pcsgReplicaIndex int32) (string, error) {
	entry, err := podGangEntryForPCSGReplica(pgm, pcsgName, pcsgReplicaIndex)
	if err != nil {
		return "", err
	}
	return entry.Epoch, nil
}

// PodGangNameForPCSGReplica returns the epoch-based PodGang name that a PodCliqueScalingGroup replica
// index belongs to, reading its entry from the PodGangMap. An Anchor entry yields the anchor PodGang
// name. A Tail or ScaleOut entry yields the non-anchor name. It uses the same entry lookup as
// EpochForPCSGReplica, so it agrees with how the PodGang materializer names the PodGang.
func PodGangNameForPCSGReplica(pgm *grovecorev1alpha1.PodGangMap, rnr apicommon.ResourceNameReplica, pcsgName string, pcsgReplicaIndex int32) (string, error) {
	entry, err := podGangEntryForPCSGReplica(pgm, pcsgName, pcsgReplicaIndex)
	if err != nil {
		return "", err
	}
	if entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor {
		return apicommon.GenerateAnchorPodGangName(rnr, entry.Epoch), nil
	}
	return apicommon.GenerateNonAnchorPodGangName(rnr, entry.Epoch, pcsgName, pcsgReplicaIndex), nil
}

// DependsOnForEpoch returns the epochs that the PodGangMap entry with the given epoch depends on
// before its pods may be scheduled. An empty result means the entry has no scheduling dependency. It
// returns an error when no entry carries the epoch, which the caller treats as requeue-worthy rather
// than proceeding with an unknown dependency.
func DependsOnForEpoch(pgm *grovecorev1alpha1.PodGangMap, epoch string) ([]string, error) {
	for i := range pgm.Spec.Entries {
		if pgm.Spec.Entries[i].Epoch == epoch {
			return pgm.Spec.Entries[i].DependsOn, nil
		}
	}
	return nil, fmt.Errorf("no entry with epoch %q exists in PodGangMap %s", epoch, pgm.Name)
}

// podGangEntryForPCSGReplica returns the PodGangMap entry that a PodCliqueScalingGroup replica index
// belongs to. It first returns the entry whose PCSGReplicaIndices for pcsgName already contains the
// index. When no entry has placed the index yet — the case for a scale-out replica whose index the
// PodGangMap component has not appended to the ScaleOut entry in this reconcile pass — it returns the
// pre-created ScaleOut entry, whose epoch every scale-out replica shares. It returns an error when
// neither an owning entry nor a ScaleOut entry exists, which is a contract violation for a
// PodCliqueScalingGroup-owned PodClique and must be requeued rather than resolved to an empty name.
// It does not filter by generation hash: a replica's PodGangMap holds a single generation's entries,
// and during a rolling update only the under-update replica's entries advance, so a lagging replica
// is resolved against its own entries.
func podGangEntryForPCSGReplica(pgm *grovecorev1alpha1.PodGangMap, pcsgName string, pcsgReplicaIndex int32) (*grovecorev1alpha1.PodGangEntry, error) {
	var scaleOut *grovecorev1alpha1.PodGangEntry
	for i := range pgm.Spec.Entries {
		entry := &pgm.Spec.Entries[i]
		if lo.Contains(entry.PCSGReplicaIndices[pcsgName], pcsgReplicaIndex) {
			return entry, nil
		}
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut {
			scaleOut = entry
		}
	}
	if scaleOut != nil {
		return scaleOut, nil
	}
	return nil, fmt.Errorf("no PodGangMap entry owns replica index %d of PodCliqueScalingGroup %q and no ScaleOut entry exists in PodGangMap %s", pcsgReplicaIndex, pcsgName, pgm.Name)
}

// AnchorPodGangEpoch returns the epoch of the AnchorIndex 0 anchor entry of the PodGangMap. Standalone
// PodCliques always belong to this entry. It returns an error when no such anchor entry exists, a
// contract violation that must be re-queued.
// NOTE: When coherent-updates update strategy (GREP-393) is introduced then post coherent update it is possible
// that there are more than one anchor entry. This function will have to be adapted to support that.
func AnchorPodGangEpoch(pgm *grovecorev1alpha1.PodGangMap) (string, error) {
	for i := range pgm.Spec.Entries {
		entry := &pgm.Spec.Entries[i]
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor && entry.AnchorIndex != nil && *entry.AnchorIndex == 0 {
			return entry.Epoch, nil
		}
	}
	return "", fmt.Errorf("no AnchorIndex 0 anchor entry exists in PodGangMap %s", pgm.Name)
}
