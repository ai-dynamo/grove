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
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
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
		if IsStandalonePCLQ(pcs, pclqTemplateSpec.Name) {
			pclqNames = append(pclqNames, apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}, pclqTemplateSpec.Name))
		}
	}
	return pclqNames
}

// IsStandalonePCLQ reports whether the named PodClique is standalone, that is not a member of any
// PodCliqueScalingGroup of the PodCliqueSet.
func IsStandalonePCLQ(pcs *grovecorev1alpha1.PodCliqueSet, pclqName string) bool {
	return !lo.SomeBy(pcs.Spec.Template.PodCliqueScalingGroupConfigs, func(pcsgConfig grovecorev1alpha1.PodCliqueScalingGroupConfig) bool {
		return slices.Contains(pcsgConfig.CliqueNames, pclqName)
	})
}

// pcsCacheKey is a context key for per-reconcile memoization of GetPodCliqueSet results.
// In the PodClique reconcile flow alone we Get the same PCS up to 4 times (reconcileSpec,
// reconcileStatus, pod.prepareSyncFlow, resourceClaim) — each DeepCopying the full template.
type pcsCacheKey struct{}

// pcsCache holds one slot keyed by "namespace/name".
type pcsCache struct {
	byKey map[string]*grovecorev1alpha1.PodCliqueSet
}

// WithPodCliqueSetCache returns a context that memoizes GetPodCliqueSet results for the
// lifetime of one reconcile. Call this once at the top of Reconcile and propagate the ctx.
func WithPodCliqueSetCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, pcsCacheKey{}, &pcsCache{byKey: make(map[string]*grovecorev1alpha1.PodCliqueSet, 1)})
}

// GetPodCliqueSet gets the owner PodCliqueSet object. When the context carries a cache from
// WithPodCliqueSetCache, the first lookup populates it and subsequent calls skip the Get.
// The returned pointer is the cached instance — callers must not mutate it in place.
func GetPodCliqueSet(ctx context.Context, cl client.Client, objectMeta metav1.ObjectMeta) (*grovecorev1alpha1.PodCliqueSet, error) {
	pcsName := GetPodCliqueSetName(objectMeta)
	key := objectMeta.Namespace + "/" + pcsName
	cache, _ := ctx.Value(pcsCacheKey{}).(*pcsCache)
	if cache != nil {
		if pcs, ok := cache.byKey[key]; ok {
			return pcs, nil
		}
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pcsName,
			Namespace: objectMeta.Namespace,
		},
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(pcs), pcs); err != nil {
		return pcs, err
	}
	if cache != nil {
		cache.byKey[key] = pcs
	}
	return pcs, nil
}

// GetPodCliqueSetName retrieves the PodCliqueSet name from the labels of the given ObjectMeta.
// NOTE: It is assumed that all managed objects like PCSG, PCLQ and Pods will always have PCS name as value for grovecorev1alpha1.LabelPartOfKey label.
// It should be ensured that labels that are set by the operator are never removed.
func GetPodCliqueSetName(objectMeta metav1.ObjectMeta) string {
	pcsName := objectMeta.GetLabels()[apicommon.LabelPartOfKey]
	return pcsName
}

// ResolveUpdateStrategyType returns the effective update strategy type for the PodCliqueSet. It
// tolerates a nil UpdateStrategy or an empty Type by resolving to the Coherent default. This matches
// the value the defaulting webhook persists, so an object that predates the UpdateStrategy field and
// reconciles without re-admission resolves to the same strategy as a re-admitted object. Callers must
// pass a non-nil PodCliqueSet.
func ResolveUpdateStrategyType(pcs *grovecorev1alpha1.PodCliqueSet) grovecorev1alpha1.UpdateStrategyType {
	if pcs.Spec.UpdateStrategy == nil || pcs.Spec.UpdateStrategy.Type == "" {
		return grovecorev1alpha1.CoherentStrategy
	}
	return pcs.Spec.UpdateStrategy.Type
}

// IsRollingUpdateStrategy returns true when PodCliqueSet update strategy is orchestrated by Grove.
// Only the OnDelete update strategy is not a rolling update strategy.
func IsRollingUpdateStrategy(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	if pcs == nil {
		return false
	}
	return ResolveUpdateStrategyType(pcs) != grovecorev1alpha1.OnDeleteStrategy
}

// IsRollingUpdateInProgress returns true when the PodCliqueSet uses a rolling update strategy and an
// update has started but has not yet ended.
func IsRollingUpdateInProgress(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	return IsRollingUpdateStrategy(pcs) && updateInProgress(pcs)
}

// updateInProgress reports whether the PodCliqueSet has an update that has started but not yet ended.
func updateInProgress(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	return pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt == nil
}

// IsCoherentUpdateInProgress returns true when the PodCliqueSet uses the Coherent update strategy
// and an update has started but has not yet ended.
func IsCoherentUpdateInProgress(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	return IsCoherentStrategy(pcs) && updateInProgress(pcs)
}

// IsRollingRecreateUpdateInProgress returns true when a rolling update is in progress under the
// RollingRecreate strategy, the default strategy that is neither Coherent nor OnDelete.
func IsRollingRecreateUpdateInProgress(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	return IsRollingUpdateInProgress(pcs) && !IsCoherentStrategy(pcs)
}

// IsCoherentStrategy returns true when the PodCliqueSet uses the Coherent update strategy (the
// UpdateStrategyType value "Coherent"). A nil or unset strategy resolves to the Coherent default.
func IsCoherentStrategy(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	if pcs == nil {
		return false
	}
	return ResolveUpdateStrategyType(pcs) == grovecorev1alpha1.CoherentStrategy
}

// IsPCSReplicaUnderCoherentUpdate reports whether the given PodCliqueSet replica is the one the
// orchestrator has selected for a coherent update and has not yet closed out.
func IsPCSReplicaUnderCoherentUpdate(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int) bool {
	if !IsCoherentUpdateInProgress(pcs) {
		return false
	}
	for _, replicaProgress := range pcs.Status.UpdateProgress.CurrentlyUpdating {
		if int(replicaProgress.ReplicaIndex) == pcsReplicaIndex && replicaProgress.UpdateEndedAt == nil {
			return true
		}
	}
	return false
}

// GetExpectedPCLQNamesGroupByOwner returns the expected unqualified PodClique names which are either owned by PodCliqueSet or PodCliqueScalingGroup.
func GetExpectedPCLQNamesGroupByOwner(pcs *grovecorev1alpha1.PodCliqueSet) (expectedPCLQNamesForPCS sets.Set[string], expectedPCLQNamesForPCSG sets.Set[string]) {
	expectedPCLQNamesForPCS = sets.New[string]()
	expectedPCLQNamesForPCSG = sets.New[string]()
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		expectedPCLQNamesForPCSG.Insert(pcsgConfig.CliqueNames...)
	}
	for _, pclqTemplateSpec := range pcs.Spec.Template.Cliques {
		if !expectedPCLQNamesForPCSG.Has(pclqTemplateSpec.Name) {
			expectedPCLQNamesForPCS.Insert(pclqTemplateSpec.Name)
		}
	}
	return
}

// GetExpectedPCSGFQNsPerPCSReplica computes the FQNs for all PodCliqueScalingGroups defined in PCS for each replica.
func GetExpectedPCSGFQNsPerPCSReplica(pcs *grovecorev1alpha1.PodCliqueSet) map[int][]string {
	pcsgFQNsByPCSReplica := make(map[int][]string)
	for pcsReplicaIndex := range int(pcs.Spec.Replicas) {
		for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
			pcsgName := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}, pcsgConfig.Name)
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

// CountStandalonePCLQs returns the number of standalone PodCliques declared in the PCS template.
func CountStandalonePCLQs(pcs *grovecorev1alpha1.PodCliqueSet) int {
	return lo.CountBy(pcs.Spec.Template.Cliques, func(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec) bool {
		return IsStandalonePCLQ(pcs, pclqTemplateSpec.Name)
	})
}

// GetStandalonePCLQReplicasFromPCSTemplateSpec returns the total replica count per standalone PodClique from the PCS template spec.
func GetStandalonePCLQReplicasFromPCSTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet) map[string]int32 {
	result := make(map[string]int32)
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		if IsStandalonePCLQ(pcs, cliqueTemplate.Name) {
			result[cliqueTemplate.Name] = cliqueTemplate.Spec.Replicas
		}
	}
	return result
}

// GetPCSGMinAvailableFromPCSTemplateSpec returns the MinAvailable replica count per PodCliqueScalingGroup from the PCS template spec.
func GetPCSGMinAvailableFromPCSTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet) map[string]int32 {
	result := make(map[string]int32)
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		result[pcsgConfig.Name] = *pcsgConfig.MinAvailable
	}
	return result
}

// GetPCSGReplicasFromPCSTemplateSpec returns the total replica count per PodCliqueScalingGroup from the PCS template spec.
func GetPCSGReplicasFromPCSTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet) map[string]int32 {
	result := make(map[string]int32)
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		result[pcsgConfig.Name] = *pcsgConfig.Replicas
	}
	return result
}

// GetPodCliqueSetReplicaIndexFromPodCliqueFQN extracts the PodCliqueSet replica index from a Pod Clique FQN name.
func GetPodCliqueSetReplicaIndexFromPodCliqueFQN(pcsName, pclqFQNName string) (int, error) {
	replicaStartIndex := len(pcsName) + 1 // +1 for the hyphen
	hyphenIndex := strings.Index(pclqFQNName[replicaStartIndex:], "-")
	if hyphenIndex == -1 {
		return -1, fmt.Errorf("PodClique FQN is not in the expected format of <pcs-name>-<pcs-replica-index>-<pclq-template-name>: %s", pclqFQNName)
	}
	replicaEndIndex := replicaStartIndex + hyphenIndex
	return strconv.Atoi(pclqFQNName[replicaStartIndex:replicaEndIndex])
}
