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

package component

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FindScalingGroupConfigForClique searches through the scaling group configurations to find
// the one that contains the specified clique name in its CliqueNames list.
//
// Returns the matching PodCliqueScalingGroupConfig and true if found, or an empty config and false if not found.
func FindScalingGroupConfigForClique(scalingGroupConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig, cliqueName string) *grovecorev1alpha1.PodCliqueScalingGroupConfig {
	pcsgConfig, ok := lo.Find(scalingGroupConfigs, func(pcsgConfig grovecorev1alpha1.PodCliqueScalingGroupConfig) bool {
		return slices.Contains(pcsgConfig.CliqueNames, cliqueName)
	})
	if !ok {
		return nil
	}
	return &pcsgConfig
}

// GetPCSGsForPCS fetches all PodCliqueScalingGroups for a PodCliqueSet.
func GetPCSGsForPCS(ctx context.Context, cl client.Client, pcsObjMeta metav1.ObjectMeta) ([]grovecorev1alpha1.PodCliqueScalingGroup, error) {
	pcsgList, err := doGetPCSGsForPCS(ctx, cl, client.ObjectKey{Namespace: pcsObjMeta.Namespace, Name: pcsObjMeta.Name}, nil)
	if err != nil {
		return nil, err
	}
	// Exclude PodCliqueScalingGroups controlled by an older PodCliqueSet of the same name, so a
	// recreated PodCliqueSet does not ingest a deleted one's leftover children.
	return lo.Filter(pcsgList.Items, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup, _ int) bool {
		return metav1.IsControlledBy(&pcsg, &pcsObjMeta)
	}), nil
}

// doGetPCSGsForPCS is a helper function that fetches PodCliqueScalingGroups with optional additional label filtering
func doGetPCSGsForPCS(ctx context.Context, cl client.Client, pcsObjKey client.ObjectKey, matchingLabels map[string]string) (*grovecorev1alpha1.PodCliqueScalingGroupList, error) {
	pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
	if err := cl.List(ctx,
		pcsgList,
		client.InNamespace(pcsObjKey.Namespace),
		client.MatchingLabels(lo.Assign(
			apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjKey.Name),
			matchingLabels,
		)),
	); err != nil {
		return nil, err
	}
	return pcsgList, nil
}

// GroupPCSGsByPCSReplicaIndex filters PCSGs that have a PodCliqueSetReplicaIndex label and groups them by the PCS replica index.
// A PodCliqueSetReplicaIndex label that is not a valid integer is a contract violation and returns an error.
func GroupPCSGsByPCSReplicaIndex(pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) (map[int][]grovecorev1alpha1.PodCliqueScalingGroup, error) {
	grouped := make(map[int][]grovecorev1alpha1.PodCliqueScalingGroup)
	for labelValue, pcsgsForReplica := range groupPCSGsByLabel(pcsgs, apicommon.LabelPodCliqueSetReplicaIndex) {
		replicaIndex, err := strconv.Atoi(labelValue)
		if err != nil {
			return nil, fmt.Errorf("%s label value %q is not a valid integer", apicommon.LabelPodCliqueSetReplicaIndex, labelValue)
		}
		grouped[replicaIndex] = pcsgsForReplica
	}
	return grouped, nil
}

// groupPCSGsByLabel groups PodCliqueScalingGroups by the value of the specified label key
func groupPCSGsByLabel(pcsgs []grovecorev1alpha1.PodCliqueScalingGroup, label string) map[string][]grovecorev1alpha1.PodCliqueScalingGroup {
	result := make(map[string][]grovecorev1alpha1.PodCliqueScalingGroup)
	for _, pcsg := range pcsgs {
		labelValue, exists := pcsg.Labels[label]
		if !exists {
			continue
		}
		result[labelValue] = append(result[labelValue], pcsg)
	}
	return result
}

// GetPCSGsByPCSReplicaIndex groups the PodCliqueScalingGroups per PodCliqueSet replica index and returns a map with the key being the PodCliqueSet replica index and the value
// being the slice of PodCliqueScalingGroup objects.
func GetPCSGsByPCSReplicaIndex(ctx context.Context, cl client.Client, pcsObjKey client.ObjectKey) (map[string][]grovecorev1alpha1.PodCliqueScalingGroup, error) {
	pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
	if err := cl.List(ctx,
		pcsgList,
		client.InNamespace(pcsObjKey.Namespace),
		client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjKey.Name)),
	); err != nil {
		return nil, err
	}
	pcsgsByPCSReplicaIndex := make(map[string][]grovecorev1alpha1.PodCliqueScalingGroup)
	for _, pcsg := range pcsgList.Items {
		pcsReplicaIndex, ok := pcsg.Labels[apicommon.LabelPodCliqueSetReplicaIndex]
		if !ok {
			continue
		}
		pcsgsByPCSReplicaIndex[pcsReplicaIndex] = append(pcsgsByPCSReplicaIndex[pcsReplicaIndex], pcsg)
	}
	return pcsgsByPCSReplicaIndex, nil
}

// GetPCLQTemplateHashes generates the Pod template hash for all PCLQs in a PCSG. Returns a map of [PCLQ Name : PodTemplateHas]
func GetPCLQTemplateHashes(pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) map[string]string {
	pclqTemplateSpecs := make([]*grovecorev1alpha1.PodCliqueTemplateSpec, 0, len(pcsg.Spec.CliqueNames))
	for _, cliqueName := range pcsg.Spec.CliqueNames {
		pclqTemplateSpec := FindPodCliqueTemplateSpecByName(pcs, cliqueName)
		if pclqTemplateSpec == nil {
			continue
		}
		pclqTemplateSpecs = append(pclqTemplateSpecs, pclqTemplateSpec)
	}
	cliqueTemplateSpecHashes := make(map[string]string, len(pclqTemplateSpecs))
	for pcsgReplicaIndex := range int(pcsg.Spec.Replicas) {
		for _, pclqTemplateSpec := range pclqTemplateSpecs {
			pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: pcsgReplicaIndex}, pclqTemplateSpec.Name)
			cliqueTemplateSpecHashes[pclqFQN] = ComputePCLQPodTemplateHash(pclqTemplateSpec, pcs.Spec.Template.PriorityClassName)
		}
	}
	return cliqueTemplateSpecHashes
}

// GetPCLQsInPCSGPendingUpdate collects the PodClique FQNs that are pending updates.
// It identifies PCLQ pending update by comparing the current PodTemplateHash label on an existing PCLQ with that of
// a computed PodTemplateHash from the latest PodCliqueSet resource.
func GetPCLQsInPCSGPendingUpdate(pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, existingPCLQs []grovecorev1alpha1.PodClique) []string {
	pclqFQNsPendingUpdate := make([]string, 0, len(existingPCLQs))
	expectedPCLQPodTemplateHashes := GetPCLQTemplateHashes(pcs, pcsg)
	for _, existingPCLQ := range existingPCLQs {
		existingPodTemplateHash := existingPCLQ.Labels[apicommon.LabelPodTemplateHash]
		expectedPodTemplateHash := expectedPCLQPodTemplateHashes[existingPCLQ.Name]
		if existingPodTemplateHash != expectedPodTemplateHash {
			pclqFQNsPendingUpdate = append(pclqFQNsPendingUpdate, existingPCLQ.Name)
		}
	}
	return pclqFQNsPendingUpdate
}

// IsPCSGUpdateInProgress checks if PCSG is under rolling update.
func IsPCSGUpdateInProgress(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) bool {
	return pcsg.Status.UpdateProgress != nil && pcsg.Status.UpdateProgress.UpdateEndedAt == nil
}

// IsPCSGUpdateComplete returns whether the rolling update of the PodCliqueScalingGroup is complete.
func IsPCSGUpdateComplete(pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pcsGenerationHash string) bool {
	return pcsg.Status.CurrentPodCliqueSetGenerationHash != nil && *pcsg.Status.CurrentPodCliqueSetGenerationHash == pcsGenerationHash
}

// GetPodCliqueFQNsForPCSG generates the PodClique FQNs for all PodCliques that are owned by a PodCliqueScalingGroup.
func GetPodCliqueFQNsForPCSG(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) []string {
	pclqFQNsInPCSG := make([]string, 0, len(pcsg.Spec.CliqueNames)*int(pcsg.Spec.Replicas))
	for replicaIndex := range int(pcsg.Spec.Replicas) {
		for _, cliqueName := range pcsg.Spec.CliqueNames {
			pclqFQNsInPCSG = append(pclqFQNsInPCSG, apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{
				Name:    pcsg.Name,
				Replica: replicaIndex,
			}, cliqueName))
		}
	}
	return pclqFQNsInPCSG
}

// PCSGReplicaState is the health of a PodCliqueScalingGroup replica derived from its member PodCliques.
type PCSGReplicaState int

const (
	// PCSGReplicaStatePending marks a replica with a member PodClique below MinAvailable scheduled replicas.
	PCSGReplicaStatePending PCSGReplicaState = iota
	// PCSGReplicaStateUnavailable marks a scheduled replica with a member PodClique below MinAvailable ready replicas.
	PCSGReplicaStateUnavailable
	// PCSGReplicaStateReady marks a replica whose every member PodClique has at least MinAvailable ready replicas.
	PCSGReplicaStateReady
)

// PCSGReplicaDisruptionInfo is the per-replica input to disruption ordering. It currently carries only the
// replica index and its health state. It is a struct rather than a bare state map so further ordering
// signals can be added later without changing the ordering function signature, for example a deletion
// cost to steer selection among equally healthy replicas. Callers populate only the fields they have and
// the ordering uses whatever is present.
type PCSGReplicaDisruptionInfo struct {
	// Index is the replica index.
	Index int
	// State is the replica health, the primary ordering key.
	State PCSGReplicaState
}

// ComputePCSGReplicaState classifies one PodCliqueScalingGroup replica from its member PodCliques and the
// number of member PodCliques the replica should have. A replica is pending when a member PodClique is
// absent or below MinAvailable scheduled, unavailable when a member is below MinAvailable ready, otherwise
// ready. A missing member is treated as below MinAvailable scheduled, so an incomplete replica is never
// classified ready and never counts as serving capacity. Terminating member PodCliques are ignored, so a
// replica mid-replacement reads as incomplete.
func ComputePCSGReplicaState(memberPCLQs []grovecorev1alpha1.PodClique, expectedMemberCount int) PCSGReplicaState {
	nonTerminating := lo.Filter(memberPCLQs, func(pclq grovecorev1alpha1.PodClique, _ int) bool {
		return !k8sutils.IsResourceTerminating(pclq.ObjectMeta)
	})
	if len(nonTerminating) < expectedMemberCount {
		return PCSGReplicaStatePending
	}
	for _, pclq := range nonTerminating {
		if pclq.Status.ScheduledReplicas < *pclq.Spec.MinAvailable {
			return PCSGReplicaStatePending
		}
		if pclq.Status.ReadyReplicas < *pclq.Spec.MinAvailable {
			return PCSGReplicaStateUnavailable
		}
	}
	return PCSGReplicaStateReady
}

// OrderPCSGReplicaIndicesForDisruption returns the replica indices ordered by disruption preference, worst-off
// health first (pending, then unavailable, then ready), and ascending by index within the same health
// state so the order is deterministic. As PCSGReplicaDisruptionInfo grows new ordering signals, this function
// applies them within a health state, keeping health the primary key.
func OrderPCSGReplicaIndicesForDisruption(infos []PCSGReplicaDisruptionInfo) []int {
	ordered := slices.Clone(infos)
	slices.SortFunc(ordered, func(a, b PCSGReplicaDisruptionInfo) int {
		if a.State != b.State {
			return int(a.State) - int(b.State)
		}
		return a.Index - b.Index
	})
	indices := make([]int, len(ordered))
	for i := range ordered {
		indices[i] = ordered[i].Index
	}
	return indices
}
