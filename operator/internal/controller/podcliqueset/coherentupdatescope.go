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

package podcliqueset

import (
	"context"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"k8s.io/apimachinery/pkg/util/sets"
)

// updateScope names the components a coherent update targets, split into standalone PodCliques and
// PodCliqueScalingGroups. It backs the in-scope set recorded on Status.UpdateProgress.
type updateScope struct {
	standalonePCLQs        sets.Set[string]
	podCliqueScalingGroups sets.Set[string]
}

// computeCoherentUpdateScope determines which components a coherent update must roll for the given
// PodCliqueSet. A component is in scope when its pod template changed in this edit. When a coherent update
// is already in flight, the previous scope's still-pending components are merged in so a half-rolled
// component is finished rather than frozen. It reads the current generation hash from status, so it must
// run before that hash is overwritten with the new one.
func (r *Reconciler) computeCoherentUpdateScope(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (updateScope, error) {
	deployedHashByClique, err := r.deployedPodTemplateHashByClique(ctx, pcs)
	if err != nil {
		return updateScope{}, err
	}
	newInScope := newlyChangedInScope(pcs, deployedHashByClique)
	if !componentutils.IsCoherentUpdateInProgress(pcs) {
		return newInScope, nil
	}
	pgms, err := componentutils.ListPodGangMapsForPCS(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return updateScope{}, err
	}
	previousInScope := previousPendingInScope(pcs, pgms, *pcs.Status.CurrentGenerationHash)
	return mergeInScopes(newInScope, previousInScope), nil
}

// deployedPodTemplateHashByClique returns the currently deployed pod template hash of each PodClique keyed
// by clique name. It reads the desired-hash label set by the owner sync, which is what the cluster last
// converged toward, and covers standalone and PodCliqueScalingGroup-owned PodCliques alike.
func (r *Reconciler) deployedPodTemplateHashByClique(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[string]string, error) {
	pclqs, err := componentutils.ListPCLQsMatchingLabels(ctx, r.client, pcs.Namespace, apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))
	if err != nil {
		return nil, err
	}
	hashByClique := make(map[string]string)
	for _, pclq := range pclqs {
		cliqueName, err := componentutils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
		if err != nil {
			return nil, err
		}
		hashByClique[cliqueName] = pclq.Labels[apicommon.LabelPodTemplateHash]
	}
	return hashByClique, nil
}

// newlyChangedInScope returns the components whose newly computed pod template hash differs from the
// deployed hash, so this edit must roll them. A changed clique that is not part of any
// PodCliqueScalingGroup is a standalone entry, and a PodCliqueScalingGroup is in scope when any of its
// constituent cliques changed. Standalone changes go straight to the scope, so only changed
// PodCliqueScalingGroup cliques are collected to resolve group membership in a second pass.
func newlyChangedInScope(pcs *grovecorev1alpha1.PodCliqueSet, deployedHashByClique map[string]string) updateScope {
	scope := updateScope{standalonePCLQs: sets.New[string](), podCliqueScalingGroups: sets.New[string]()}
	changedPCSGPCLQs := sets.New[string]()
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		newHash := componentutils.ComputePCLQPodTemplateHash(cliqueTemplate, pcs.Spec.Template.PriorityClassName)
		if deployedHashByClique[cliqueTemplate.Name] == newHash {
			continue
		}
		if componentutils.IsStandalonePCLQ(pcs, cliqueTemplate.Name) {
			scope.standalonePCLQs.Insert(cliqueTemplate.Name)
		} else {
			changedPCSGPCLQs.Insert(cliqueTemplate.Name)
		}
	}
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if changedPCSGPCLQs.HasAny(pcsgConfig.CliqueNames...) {
			scope.podCliqueScalingGroups.Insert(pcsgConfig.Name)
		}
	}
	return scope
}

// previousPendingInScope returns the components from the in-flight update's scope that have not finished
// rolling, so a fresh update carries them forward. A component is still pending when it has PodGangMap
// entries at a generation hash other than the current one. Restricting to the previous scope keeps a
// pending replica's untouched components, which also sit on old entries, from being pulled in.
func previousPendingInScope(pcs *grovecorev1alpha1.PodCliqueSet, pgms []grovecorev1alpha1.PodGangMap, currentGenerationHash string) updateScope {
	previousStandalone := sets.New(pcs.Status.UpdateProgress.InScopeStandalonePodCliques...)
	previousPCSG := sets.New(pcs.Status.UpdateProgress.InScopePodCliqueScalingGroups...)
	pending := updateScope{standalonePCLQs: sets.New[string](), podCliqueScalingGroups: sets.New[string]()}
	for _, pgm := range pgms {
		for _, entry := range pgm.Spec.Entries {
			if entry.PodCliqueSetGenerationHash == currentGenerationHash {
				continue
			}
			for cliqueName := range entry.PodCliques {
				if previousStandalone.Has(cliqueName) {
					pending.standalonePCLQs.Insert(cliqueName)
				}
			}
			for pcsgName := range entry.PCSGReplicaIndices {
				if previousPCSG.Has(pcsgName) {
					pending.podCliqueScalingGroups.Insert(pcsgName)
				}
			}
		}
	}
	return pending
}

// mergeInScopes unions the newly changed components with the previous update's still-pending components,
// so a mid-flight edit finishes a half-rolled component instead of abandoning it.
func mergeInScopes(newInScope, previousInScope updateScope) updateScope {
	return updateScope{
		standalonePCLQs:        newInScope.standalonePCLQs.Union(previousInScope.standalonePCLQs),
		podCliqueScalingGroups: newInScope.podCliqueScalingGroups.Union(previousInScope.podCliqueScalingGroups),
	}
}
