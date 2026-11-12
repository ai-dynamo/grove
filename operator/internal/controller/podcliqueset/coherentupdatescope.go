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
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// updateScope names the components whose Pods an update replaces, split into standalone PodCliques and
// PodCliqueScalingGroups. It backs the in-scope set recorded on Status.UpdateProgress.
type updateScope struct {
	standalonePCLQs        sets.Set[string]
	podCliqueScalingGroups sets.Set[string]
}

// newUpdateScope returns an empty updateScope.
func newUpdateScope() updateScope {
	return updateScope{standalonePCLQs: sets.New[string](), podCliqueScalingGroups: sets.New[string]()}
}

// isEmpty reports whether the scope names no component.
func (s updateScope) isEmpty() bool {
	return s.standalonePCLQs.Len() == 0 && s.podCliqueScalingGroups.Len() == 0
}

// computeChangedScope returns the components with a PodClique whose pod-template-hash changes once it is written
// from updateRevision, so its Pods must be replaced. A standalone PodClique adds its clique name, and a
// PodCliqueScalingGroup-owned PodClique adds its PodCliqueScalingGroup config name. It uses the rule the PodClique
// writers use, so the update starts exactly when a writer changes a hash. Terminating PodCliques are skipped, since
// they are recreated from the current template.
func (r *Reconciler) computeChangedScope(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, updateRevision string) (updateScope, error) {
	scope := newUpdateScope()
	pclqs, err := r.listPCSPodCliques(ctx, pcs)
	if err != nil {
		return scope, err
	}
	for i := range pclqs {
		pclq := &pclqs[i]
		if k8sutils.IsResourceTerminating(pclq.ObjectMeta) {
			continue
		}
		cliqueName, err := componentutils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
		if err != nil {
			return scope, err
		}
		if componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName) == nil {
			continue
		}
		podTemplateHash, err := componentutils.ResolvePodTemplateHash(ctx, r.client, pcs, updateRevision, cliqueName, pclq.Labels)
		if err != nil {
			return scope, err
		}
		if podTemplateHash == pclq.Labels[apicommon.LabelPodTemplateHash] {
			continue
		}
		if pcsgConfig := componentutils.FindScalingGroupConfigForClique(pcs.Spec.Template.PodCliqueScalingGroupConfigs, cliqueName); pcsgConfig != nil {
			scope.podCliqueScalingGroups.Insert(pcsgConfig.Name)
		} else {
			scope.standalonePCLQs.Insert(cliqueName)
		}
	}
	return scope, nil
}

// listPCSPodCliques returns the standalone PodCliques of the PodCliqueSet and the PodCliques of its
// PodCliqueScalingGroups. PodCliques left by an older PodCliqueSet of the same name are excluded.
func (r *Reconciler) listPCSPodCliques(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet) ([]grovecorev1beta1.PodClique, error) {
	pclqs, err := componentutils.ListPCLQsMatchingLabels(ctx, r.client, pcs.Namespace, apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))
	if err != nil {
		return nil, err
	}
	pcsgs, err := componentutils.GetPCSGsForPCS(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, err
	}
	ownerUIDs := sets.New(pcs.UID)
	for i := range pcsgs {
		ownerUIDs.Insert(pcsgs[i].UID)
	}
	return lo.Filter(pclqs, func(pclq grovecorev1beta1.PodClique, _ int) bool {
		controllerRef := metav1.GetControllerOfNoCopy(&pclq)
		return controllerRef != nil && ownerUIDs.Has(controllerRef.UID)
	}), nil
}

// withPreviousPendingScope merges the components that an in-flight coherent update has not finished rolling into
// scope, so a half-rolled component is finished rather than frozen. It reads the current generation hash from
// status, so it must run before that hash is overwritten with the new one.
func (r *Reconciler) withPreviousPendingScope(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, scope updateScope) (updateScope, error) {
	if !componentutils.IsCoherentUpdateInProgress(pcs) || pcs.Status.CurrentGenerationHash == nil {
		return scope, nil
	}
	pgms, err := componentutils.ListPodGangMapsForPCS(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return scope, err
	}
	return mergeInScopes(scope, previousPendingInScope(pcs, pgms, *pcs.Status.CurrentGenerationHash)), nil
}

// previousPendingInScope returns the components from the in-flight update's scope that have not finished
// rolling, so a fresh update carries them forward. A component is still pending when it has PodGangMap
// entries at a generation hash other than the current one. Restricting to the previous scope keeps a
// pending replica's untouched components, which also sit on old entries, from being pulled in.
func previousPendingInScope(pcs *grovecorev1beta1.PodCliqueSet, pgms []grovecorev1beta1.PodGangMap, currentGenerationHash string) updateScope {
	previousStandalone := sets.New(pcs.Status.UpdateProgress.InScopeStandalonePodCliques...)
	previousPCSG := sets.New(pcs.Status.UpdateProgress.InScopePodCliqueScalingGroups...)
	pending := newUpdateScope()
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
