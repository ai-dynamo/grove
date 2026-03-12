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

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetExpectedPCSGFQNsForPCS computes the FQNs for all PodCliqueScalingGroups defined in PCS for the given replica.
func GetExpectedPCSGFQNsForPCS(pcs *grovecorev1alpha1.PodCliqueSet) []string {
	pcsgFQNsPerPCSReplica := GetExpectedPCSGFQNsPerPCSReplica(pcs)
	return lo.Flatten(lo.Values(pcsgFQNsPerPCSReplica))
}

// GetPodCliqueFQNsForPCSNotInPCSG computes the FQNs for all PodCliques for all PCS replicas which are not part of any PCSG.
func GetPodCliqueFQNsForPCSNotInPCSG(pcs *grovecorev1alpha1.PodCliqueSet) []string {
	pclqFQNs := make([]string, 0, int(pcs.Spec.Replicas)*len(pcs.Spec.Template.Cliques))
	for pcsReplicaIndex := range int(pcs.Spec.Replicas) {
		pclqFQNs = append(pclqFQNs, GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pcsReplicaIndex)...)
	}
	return pclqFQNs
}

// GetPodCliqueFQNsForPCSReplicaNotInPCSG computes the FQNs for all PodCliques for a PCS replica which are not part of any PCSG.
func GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int) []string {
	pclqNames := make([]string, 0, len(pcs.Spec.Template.Cliques))
	for _, pclqTemplateSpec := range pcs.Spec.Template.Cliques {
		if isStandalonePCLQ(pcs, pclqTemplateSpec.Name) {
			pclqNames = append(pclqNames, common.GeneratePodCliqueName(common.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}, pclqTemplateSpec.Name))
		}
	}
	return pclqNames
}

// isStandalonePCLQ checks if the PodClique is managed by PodCliqueSet or not
func isStandalonePCLQ(pcs *grovecorev1alpha1.PodCliqueSet, pclqName string) bool {
	return !lo.Reduce(pcs.Spec.Template.PodCliqueScalingGroupConfigs, func(agg bool, pcsgConfig grovecorev1alpha1.PodCliqueScalingGroupConfig, _ int) bool {
		return agg || slices.Contains(pcsgConfig.CliqueNames, pclqName)
	}, false)
}

// GetPodCliqueSet gets the owner PodCliqueSet object.
func GetPodCliqueSet(ctx context.Context, cl client.Client, objectMeta metav1.ObjectMeta) (*grovecorev1alpha1.PodCliqueSet, error) {
	pcsName := GetPodCliqueSetName(objectMeta)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pcsName,
			Namespace: objectMeta.Namespace,
		},
	}
	err := cl.Get(ctx, client.ObjectKeyFromObject(pcs), pcs)
	return pcs, err
}

// GetPodCliqueSetName retrieves the PodCliqueSet name from the labels of the given ObjectMeta.
// NOTE: It is assumed that all managed objects like PCSG, PCLQ and Pods will always have PCS name as value for grovecorev1alpha1.LabelPartOfKey label.
// It should be ensured that labels that are set by the operator are never removed.
func GetPodCliqueSetName(objectMeta metav1.ObjectMeta) string {
	pcsName := objectMeta.GetLabels()[common.LabelPartOfKey]
	return pcsName
}

// IsAutoUpdateStrategy returns true when PodCliqueSet update strategy is automatically orchestrated by Grove.
// Only the OnDelete update strategy is not an auto update strategy.
func IsAutoUpdateStrategy(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	if pcs == nil {
		return false
	}
	return pcs.Spec.UpdateStrategy == nil || pcs.Spec.UpdateStrategy.Type != grovecorev1alpha1.OnDeleteStrategy
}

// GetExpectedPCLQNamesGroupByOwner returns the expected unqualified PodClique names which are either owned by PodCliqueSet or PodCliqueScalingGroup.
func GetExpectedPCLQNamesGroupByOwner(pcs *grovecorev1alpha1.PodCliqueSet) (expectedPCLQNamesForPCS []string, expectedPCLQNamesForPCSG []string) {
	pcsgConfigs := pcs.Spec.Template.PodCliqueScalingGroupConfigs
	for _, pcsgConfig := range pcsgConfigs {
		expectedPCLQNamesForPCSG = append(expectedPCLQNamesForPCSG, pcsgConfig.CliqueNames...)
	}
	pcsCliqueNames := lo.Map(pcs.Spec.Template.Cliques, func(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, _ int) string {
		return pclqTemplateSpec.Name
	})
	expectedPCLQNamesForPCS, _ = lo.Difference(pcsCliqueNames, expectedPCLQNamesForPCSG)
	return
}

// GetExpectedPCSGFQNsPerPCSReplica computes the FQNs for all PodCliqueScalingGroups defined in PCS for each replica.
func GetExpectedPCSGFQNsPerPCSReplica(pcs *grovecorev1alpha1.PodCliqueSet) map[int][]string {
	pcsgFQNsByPCSReplica := make(map[int][]string)
	for pcsReplicaIndex := range int(pcs.Spec.Replicas) {
		for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
			pcsgName := common.GeneratePodCliqueScalingGroupName(common.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}, pcsgConfig.Name)
			pcsgFQNsByPCSReplica[pcsReplicaIndex] = append(pcsgFQNsByPCSReplica[pcsReplicaIndex], pcsgName)
		}
	}
	return pcsgFQNsByPCSReplica
}

// GetExpectedStandAlonePCLQFQNsPerPCSReplica computes the FQNs for all standalone PodCliques defined in PCS for each replica.
func GetExpectedStandAlonePCLQFQNsPerPCSReplica(pcs *grovecorev1alpha1.PodCliqueSet) map[int][]string {
	pclqFQNsByPCSReplica := make(map[int][]string)
	for pcsReplicaIndex := range int(pcs.Spec.Replicas) {
		pclqFQNsByPCSReplica[pcsReplicaIndex] = GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pcsReplicaIndex)
	}
	return pclqFQNsByPCSReplica
}
