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

package component

import (
	"context"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
func ListPodGangMapsForPCS(ctx context.Context, cl client.Client, pcsObjMeta metav1.ObjectMeta) ([]grovecorev1alpha1.PodGangMap, error) {
	pgmList := &grovecorev1alpha1.PodGangMapList{}
	if err := cl.List(ctx, pgmList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(lo.Assign(
			apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjMeta.Name),
			map[string]string{apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGangMap},
		))); err != nil {
		return nil, err
	}
	// Exclude PodGangMaps controlled by an older PodCliqueSet of the same name, so a recreated
	// PodCliqueSet does not pick up a deleted one's stale PodGangMap.
	return lo.Filter(pgmList.Items, func(pgm grovecorev1alpha1.PodGangMap, _ int) bool {
		return metav1.IsControlledBy(&pgm, &pcsObjMeta)
	}), nil
}

// PodGangMapByPCSReplicaIndex groups PodGangMaps by their PCS replica index.
// A PodCliqueSetReplicaIndex label that is missing or not a valid integer is a contract violation and returns an error.
func PodGangMapByPCSReplicaIndex(pgms []grovecorev1alpha1.PodGangMap) (map[int]*grovecorev1alpha1.PodGangMap, error) {
	pgmByReplicaIndex := make(map[int]*grovecorev1alpha1.PodGangMap, len(pgms))
	for i := range pgms {
		labelValue, ok := pgms[i].Labels[apicommon.LabelPodCliqueSetReplicaIndex]
		if !ok {
			return nil, fmt.Errorf("PodGangMap %s has no label %s", pgms[i].Name, apicommon.LabelPodCliqueSetReplicaIndex)
		}
		pcsReplicaIndex, err := strconv.Atoi(labelValue)
		if err != nil {
			return nil, fmt.Errorf("%s label on PodGangMap %s is not a valid integer: %q", apicommon.LabelPodCliqueSetReplicaIndex, pgms[i].Name, labelValue)
		}
		pgmByReplicaIndex[pcsReplicaIndex] = &pgms[i]
	}
	return pgmByReplicaIndex, nil
}

// PodGangNameForPCSGReplica returns the epoch-based PodGang name that a PodCliqueScalingGroup replica
// index belongs to, reading its entry from the PodGangMap. An Anchor entry yields the anchor PodGang
// name. A Tail or ScaleOut entry yields the non-anchor name. It reads the role from the entry, so it
// agrees with how the PodGang materializer names the PodGang.
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

// AnchorPodGangEpoch returns the epoch of the MinAvailable anchor of the PodGangMap, which carries the
// MinAvailable replicas that standalone Pods key off. See MinAvailableAnchorEpoch.
func AnchorPodGangEpoch(pgm *grovecorev1alpha1.PodGangMap) (string, error) {
	epoch, found, err := MinAvailableAnchorEpoch(pgm.Spec.Entries, nil)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("no anchor entry exists in PodGangMap %s", pgm.Name)
	}
	return epoch, nil
}

// MinAvailableAnchorEpoch returns the epoch of the MinAvailable anchor, considering only entries at
// pcsGenerationHash when it is non-nil, or all entries when it is nil. It returns found false when no
// such anchor exists, and an error when an anchor epoch is not numeric.
//
// The MinAvailable anchor is the anchor entry that holds a PodCliqueSet replica's guaranteed
// MinAvailable floor for its standalone PodCliques. It is the lowest-epoch anchor of the generation in
// question. Steady-state standalone scale-in drains from the highest-epoch anchor downward, so the
// MinAvailable anchor is drained last and always retains the final MinAvailable pods. Its standalone
// PodGroups keep MinReplicas at the template MinAvailable. Every other anchor clamps MinReplicas to its
// per-anchor count.
func MinAvailableAnchorEpoch(entries []grovecorev1alpha1.PodGangEntry, pcsGenerationHash *string) (string, bool, error) {
	var (
		lowestEpoch      string
		found            bool
		lowestEpochNanos int64
	)
	for i := range entries {
		entry := entries[i]
		if entry.Role != grovecorev1alpha1.PodGangEntryRoleAnchor {
			continue
		}
		if pcsGenerationHash != nil && entry.PodCliqueSetGenerationHash != *pcsGenerationHash {
			continue
		}
		epochNanos, err := strconv.ParseInt(entry.Epoch, 10, 64)
		if err != nil {
			return "", false, fmt.Errorf("anchor entry has a non-numeric epoch %q: %w", entry.Epoch, err)
		}
		if !found || epochNanos < lowestEpochNanos {
			found, lowestEpochNanos, lowestEpoch = true, epochNanos, entry.Epoch
		}
	}
	return lowestEpoch, found, nil
}

// IndexPodGangEntriesByEpoch returns a map of the PodGangMap entries keyed by their epoch. Epoch is
// unique per entry, so each key maps to a single entry.
func IndexPodGangEntriesByEpoch(entries []grovecorev1alpha1.PodGangEntry) map[string]grovecorev1alpha1.PodGangEntry {
	byEpoch := make(map[string]grovecorev1alpha1.PodGangEntry, len(entries))
	for _, entry := range entries {
		byEpoch[entry.Epoch] = entry
	}
	return byEpoch
}

// LatestEpochForGenerationHash returns the largest epoch among entries carrying pcsGenerationHash, or
// nil when no entry carries it. Filtering by hash makes it safe across generations, the coherent flow
// queries it with the current generation hash while older or mid-flight generations coexist as drain
// targets. Epochs are monotonic unix-nano decimals, so the largest is the newest.
func LatestEpochForGenerationHash(entries []grovecorev1alpha1.PodGangEntry, pcsGenerationHash string) (*string, error) {
	var (
		latestEpoch   string
		maxEpochValue int64
		found         bool
	)
	for i := range entries {
		if entries[i].PodCliqueSetGenerationHash != pcsGenerationHash {
			continue
		}
		epochValue, err := strconv.ParseInt(entries[i].Epoch, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("PodGangMap entry with epoch %q has a non-numeric epoch: %w", entries[i].Epoch, err)
		}
		if !found || epochValue > maxEpochValue {
			latestEpoch, maxEpochValue, found = entries[i].Epoch, epochValue, true
		}
	}
	if !found {
		return nil, nil
	}
	return &latestEpoch, nil
}
