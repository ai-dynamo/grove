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
	"context"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetPodGangMap fetches a PodGangMap by name and namespace.
func GetPodGangMap(ctx context.Context, cl client.Client, podGangMapName, namespace string) (*grovecorev1alpha1.PodGangMap, error) {
	pgm := &grovecorev1alpha1.PodGangMap{}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podGangMapName}, pgm); err != nil {
		return nil, err
	}
	return pgm, nil
}

// GetPodGangMapForPCSReplica fetches the PodGangMap for a given PCS replica.
func GetPodGangMapForPCSReplica(ctx context.Context, cl client.Client, pcsName, namespace string, pcsReplicaIndex int) (*grovecorev1alpha1.PodGangMap, error) {
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: pcsName, Replica: pcsReplicaIndex})
	return GetPodGangMap(ctx, cl, pgmName, namespace)
}

// ListPodGangMapsForPCS fetches all PodGangMap's for a PCS.
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

func PodGangMapByPCSReplicaIndex(pgms []grovecorev1alpha1.PodGangMap) (map[int]grovecorev1alpha1.PodGangMap, error) {
	pgmByReplicaIndex := make(map[int]grovecorev1alpha1.PodGangMap, len(pgms))
	for i := range pgms {
		labelValue, ok := pgms[i].Labels[apicommon.LabelPodCliqueSetReplicaIndex]
		if !ok {
			return nil, fmt.Errorf("PodGangMap resource %s has no label %s", pgms[i].Name, apicommon.LabelPodCliqueSetReplicaIndex)
		}
		pcsReplicaIndex, err := strconv.Atoi(labelValue)
		if err != nil {
			return nil, fmt.Errorf("%s label on PodGangMap %s is not a valid integer: %q", apicommon.LabelPodCliqueSetReplicaIndex, pgms[i].Name, labelValue)
		}
		pgmByReplicaIndex[pcsReplicaIndex] = pgms[i]
	}
	return pgmByReplicaIndex, nil
}

// FilterPodGangMapEntriesByGenerationHash returns entries that match the given PodCliqueSetGenerationHash.
func FilterPodGangMapEntriesByGenerationHash(entries []grovecorev1alpha1.PodGangEntry, hash string) []grovecorev1alpha1.PodGangEntry {
	result := make([]grovecorev1alpha1.PodGangEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.PodCliqueSetGenerationHash == hash {
			result = append(result, entry)
		}
	}
	return result
}

// GetPodGangMapEntriesForPCLQ returns all entries that reference the given standalone PodClique name.
func GetPodGangMapEntriesForPCLQ(entries []grovecorev1alpha1.PodGangEntry, pclqName string) []grovecorev1alpha1.PodGangEntry {
	result := make([]grovecorev1alpha1.PodGangEntry, 0, len(entries))
	for _, entry := range entries {
		if _, ok := entry.PodCliques[pclqName]; ok {
			result = append(result, entry)
		}
	}
	return result
}

// GetPodGangMapEntriesForPCSG returns all entries that reference the given PodCliqueScalingGroup name.
func GetPodGangMapEntriesForPCSG(entries []grovecorev1alpha1.PodGangEntry, pcsgName string) []grovecorev1alpha1.PodGangEntry {
	result := make([]grovecorev1alpha1.PodGangEntry, 0, len(entries))
	for _, entry := range entries {
		if _, ok := entry.PCSGReplicaIndices[pcsgName]; ok {
			result = append(result, entry)
		}
	}
	return result
}
