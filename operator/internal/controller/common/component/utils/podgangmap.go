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

// IsPodGangEntryEmpty reports whether an entry has active members.
func IsPodGangEntryEmpty(entry grovecorev1alpha1.PodGangEntry) bool {
	for _, count := range entry.PodCliques {
		if count > 0 {
			return false
		}
	}
	for _, indices := range entry.PCSGReplicaIndices {
		if len(indices) > 0 {
			return false
		}
	}
	return true
}

// StandalonePCLQMembership resolves membership, optionally within one generation.
func StandalonePCLQMembership(entries []grovecorev1alpha1.PodGangEntry, generationHash, cliqueName string) (*grovecorev1alpha1.PodGangEntry, int32, error) {
	var found *grovecorev1alpha1.PodGangEntry
	var replicas int32
	for i := range entries {
		entry := &entries[i]
		if entry.Role != grovecorev1alpha1.PodGangEntryRoleAnchor ||
			(generationHash != "" && entry.PodCliqueSetGenerationHash != generationHash) {
			continue
		}
		count, ok := entry.PodCliques[cliqueName]
		if !ok {
			continue
		}
		if found != nil {
			return nil, 0, fmt.Errorf("standalone PodClique %q belongs to multiple anchor entries", cliqueName)
		}
		found = entry
		replicas = count
	}
	return found, replicas, nil
}

// FindPodGangEntryForStandalonePCLQ resolves active membership in one generation.
func FindPodGangEntryForStandalonePCLQ(entries []grovecorev1alpha1.PodGangEntry, generationHash, cliqueName string) (*grovecorev1alpha1.PodGangEntry, error) {
	entry, replicas, err := StandalonePCLQMembership(entries, generationHash, cliqueName)
	if err != nil || replicas <= 0 {
		return nil, err
	}
	return entry, nil
}

// PodGangNameForStandalonePCLQ returns the PodGang owning an active standalone PodClique.
func PodGangNameForStandalonePCLQ(pgm *grovecorev1alpha1.PodGangMap, rnr apicommon.ResourceNameReplica, generationHash, cliqueName string) (string, error) {
	entry, err := FindPodGangEntryForStandalonePCLQ(pgm.Spec.Entries, generationHash, cliqueName)
	if err != nil {
		return "", err
	}
	if entry != nil {
		return apicommon.GenerateAnchorPodGangName(rnr, entry.Epoch), nil
	}
	return "", fmt.Errorf("no anchor entry owns active standalone PodClique %q in PodGangMap %s", cliqueName, pgm.Name)
}

// ActivePodCliqueNamesForPodGang returns the PodClique FQNs materialized in podGangName.
func ActivePodCliqueNamesForPodGang(pcs *grovecorev1alpha1.PodCliqueSet, pgm *grovecorev1alpha1.PodGangMap, rnr apicommon.ResourceNameReplica, podGangName string) (Set[string], error) {
	for i := range pgm.Spec.Entries {
		entry := &pgm.Spec.Entries[i]
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor {
			if apicommon.GenerateAnchorPodGangName(rnr, entry.Epoch) == podGangName {
				return ActivePodCliqueNamesForEntry(pcs, rnr, entry)
			}
			continue
		}
		for pcsgName, indices := range entry.PCSGReplicaIndices {
			for _, index := range indices {
				if apicommon.GenerateNonAnchorPodGangName(rnr, entry.Epoch, pcsgName, index) != podGangName {
					continue
				}
				pcsgConfig, ok := lo.Find(pcs.Spec.Template.PodCliqueScalingGroupConfigs, func(config grovecorev1alpha1.PodCliqueScalingGroupConfig) bool {
					return config.Name == pcsgName
				})
				if !ok {
					return nil, fmt.Errorf("PodGangMap %s references unknown PodCliqueScalingGroup %q", pgm.Name, pcsgName)
				}
				return podCliqueNamesForPCSGReplica(rnr, pcsgConfig, index), nil
			}
		}
	}
	return nil, fmt.Errorf("PodGang %q is not materialized by PodGangMap %s", podGangName, pgm.Name)
}

// ActivePodCliqueNamesForEntry resolves and validates every active member in an entry.
func ActivePodCliqueNamesForEntry(pcs *grovecorev1alpha1.PodCliqueSet, rnr apicommon.ResourceNameReplica, entry *grovecorev1alpha1.PodGangEntry) (Set[string], error) {
	names := make(Set[string])
	standaloneNames, _ := GetExpectedPCLQNamesGroupByOwner(pcs)
	standaloneSet := NewSet(standaloneNames)
	for cliqueName, replicas := range entry.PodCliques {
		if replicas <= 0 {
			continue
		}
		if !standaloneSet.Has(cliqueName) {
			return nil, fmt.Errorf("PodGangMap entry %q references unknown standalone PodClique %q", entry.Epoch, cliqueName)
		}
		names[apicommon.GeneratePodCliqueName(rnr, cliqueName)] = struct{}{}
	}
	for pcsgName, indices := range entry.PCSGReplicaIndices {
		config, ok := lo.Find(pcs.Spec.Template.PodCliqueScalingGroupConfigs, func(config grovecorev1alpha1.PodCliqueScalingGroupConfig) bool {
			return config.Name == pcsgName
		})
		if !ok {
			return nil, fmt.Errorf("PodGangMap entry %q references unknown PodCliqueScalingGroup %q", entry.Epoch, pcsgName)
		}
		for _, index := range indices {
			for name := range podCliqueNamesForPCSGReplica(rnr, config, index) {
				names[name] = struct{}{}
			}
		}
	}
	return names, nil
}

func podCliqueNamesForPCSGReplica(rnr apicommon.ResourceNameReplica, config grovecorev1alpha1.PodCliqueScalingGroupConfig, index int32) Set[string] {
	pcsgName := apicommon.GeneratePodCliqueScalingGroupName(rnr, config.Name)
	names := make(Set[string], len(config.CliqueNames))
	for _, cliqueName := range config.CliqueNames {
		names[apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgName, Replica: int(index)}, cliqueName)] = struct{}{}
	}
	return names
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
	entry, err := FindPodGangEntryForPCSGReplica(pgm.Spec.Entries, "", pcsgName, pcsgReplicaIndex)
	if err != nil || entry != nil {
		return entry, err
	}
	var scaleOut *grovecorev1alpha1.PodGangEntry
	for i := range pgm.Spec.Entries {
		entry := &pgm.Spec.Entries[i]
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut {
			if scaleOut != nil {
				return nil, fmt.Errorf("PodGangMap %s has multiple ScaleOut entries", pgm.Name)
			}
			scaleOut = entry
		}
	}
	if scaleOut != nil {
		return scaleOut, nil
	}
	return nil, fmt.Errorf("no PodGangMap entry owns replica index %d of PodCliqueScalingGroup %q and no ScaleOut entry exists in PodGangMap %s", pcsgReplicaIndex, pcsgName, pgm.Name)
}

// FindPodGangEntryForPCSGReplica resolves exact membership, optionally within one generation.
func FindPodGangEntryForPCSGReplica(entries []grovecorev1alpha1.PodGangEntry, generationHash, pcsgName string, replicaIndex int32) (*grovecorev1alpha1.PodGangEntry, error) {
	var found *grovecorev1alpha1.PodGangEntry
	for i := range entries {
		entry := &entries[i]
		if generationHash != "" && entry.PodCliqueSetGenerationHash != generationHash {
			continue
		}
		if !lo.Contains(entry.PCSGReplicaIndices[pcsgName], replicaIndex) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("PodCliqueScalingGroup %q replica index %d belongs to multiple PodGangMap entries", pcsgName, replicaIndex)
		}
		found = entry
	}
	return found, nil
}

// AnchorPodGangEpoch returns the epoch of the AnchorIndex 0 anchor entry of the PodGangMap. It is
// used as the placeholder label for a newly created idle standalone PodClique, which has no active
// membership to resolve. Active standalone PodCliques use PodGangNameForStandalonePCLQ instead.
func AnchorPodGangEpoch(pgm *grovecorev1alpha1.PodGangMap) (string, error) {
	for i := range pgm.Spec.Entries {
		entry := &pgm.Spec.Entries[i]
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor && entry.AnchorIndex != nil && *entry.AnchorIndex == 0 {
			return entry.Epoch, nil
		}
	}
	return "", fmt.Errorf("no AnchorIndex 0 anchor entry exists in PodGangMap %s", pgm.Name)
}
