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
	"slices"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/samber/lo"
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
func GetPCSGsForPCS(ctx context.Context, cl client.Client, pcsObjKey client.ObjectKey) ([]grovecorev1alpha1.PodCliqueScalingGroup, error) {
	pcsgList, err := doGetPCSGsForPCS(ctx, cl, pcsObjKey, nil)
	if err != nil {
		return nil, err
	}
	return pcsgList.Items, nil
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

// GenerateDependencyNamesForBasePodGang generates the FQNs of all PodCliques that would qualify as a dependency.
func GenerateDependencyNamesForBasePodGang(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, parentCliqueName string) []string {
	parentPCLQNames := make([]string, 0)
	pcsgConfig := FindScalingGroupConfigForClique(pcs.Spec.Template.PodCliqueScalingGroupConfigs, parentCliqueName)
	if pcsgConfig != nil {
		// Generate FQNs of minAvailable number of PodCliques that belong to a PodCliueScalingGroup.
		pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}, pcsgConfig.Name)
		for pcsgReplicaIndex := range int(*pcsgConfig.MinAvailable) {
			parentPCLQNames = append(parentPCLQNames, apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgFQN, Replica: pcsgReplicaIndex}, parentCliqueName))
		}
	} else {
		parentPCLQNames = append(parentPCLQNames, apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}, parentCliqueName))
	}
	return parentPCLQNames
}

// GroupPCSGsByPCSReplicaIndex filters PCSGs that have a PodCliqueSetReplicaIndex label and groups them by the PCS replica.
func GroupPCSGsByPCSReplicaIndex(pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) map[string][]grovecorev1alpha1.PodCliqueScalingGroup {
	return groupPCSGsByLabel(pcsgs, apicommon.LabelPodCliqueSetReplicaIndex)
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
			pclqFQNsPendingUpdate = append(pclqFQNsPendingUpdate, expectedPodTemplateHash)
		}
	}
	return pclqFQNsPendingUpdate
}

// IsPCSGUpdateInProgress checks if PCSG is under rolling update.
func IsPCSGUpdateInProgress(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) bool {
	return pcsg.Status.RollingUpdateProgress != nil && pcsg.Status.RollingUpdateProgress.UpdateEndedAt == nil
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
