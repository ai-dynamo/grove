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
	"slices"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/hash"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetPCLQsByOwner retrieves PodClique objects that are owned by the specified owner kind and object key, and match the provided selector labels.
func GetPCLQsByOwner(ctx context.Context, cl client.Client, ownerKind string, ownerObjectKey client.ObjectKey, selectorLabels map[string]string) ([]grovecorev1alpha1.PodClique, error) {
	pclqs, err := GetPCLQsMatchingLabels(ctx, cl, ownerObjectKey.Namespace, selectorLabels)
	if err != nil {
		return pclqs, err
	}
	filteredPCLQs := lo.Filter(pclqs, func(pclq grovecorev1alpha1.PodClique, _ int) bool {
		if len(pclq.OwnerReferences) == 0 {
			return false
		}
		return pclq.OwnerReferences[0].Kind == ownerKind && pclq.OwnerReferences[0].Name == ownerObjectKey.Name
	})
	return filteredPCLQs, nil
}

// GetPCLQsByOwnerReplicaIndex retrieves PodClique objects per replica of the owner resource matching provided selector labels.
func GetPCLQsByOwnerReplicaIndex(ctx context.Context, cl client.Client, ownerKind string, ownerObjectKey client.ObjectKey, selectorLabels map[string]string) (map[string][]grovecorev1alpha1.PodClique, error) {
	pclqs, err := GetPCLQsByOwner(ctx, cl, ownerKind, ownerObjectKey, selectorLabels)
	if err != nil {
		return nil, err
	}
	return groupPCLQsByLabel(pclqs, apicommon.LabelPodCliqueSetReplicaIndex), nil
}

// GetPCLQsMatchingLabels gets all the PodClique's in a given namespace matching selectorLabels.
func GetPCLQsMatchingLabels(ctx context.Context, cl client.Client, namespace string, selectorLabels map[string]string) ([]grovecorev1alpha1.PodClique, error) {
	podCliqueList := &grovecorev1alpha1.PodCliqueList{}
	if err := cl.List(ctx,
		podCliqueList,
		client.InNamespace(namespace),
		client.MatchingLabels(selectorLabels)); err != nil {
		return nil, err
	}
	return podCliqueList.Items, nil
}

// GroupPCLQsByPodGangName filters PCLQs that have a PodGang label and groups them by the PodGang name.
func GroupPCLQsByPodGangName(pclqs []grovecorev1alpha1.PodClique) map[string][]grovecorev1alpha1.PodClique {
	return groupPCLQsByLabel(pclqs, apicommon.LabelPodGang)
}

// GroupPCLQsByPCSGReplicaIndex filters PCLQs that have a PodCliqueScalingGroupReplicaIndex label and groups them by the PCSG replica.
func GroupPCLQsByPCSGReplicaIndex(pclqs []grovecorev1alpha1.PodClique) map[string][]grovecorev1alpha1.PodClique {
	return groupPCLQsByLabel(pclqs, apicommon.LabelPodCliqueScalingGroupReplicaIndex)
}

// GroupPCLQsByPCSReplicaIndex filters PCLQs that have a PodCliqueSetReplicaIndex label and groups them by the PCS replica.
func GroupPCLQsByPCSReplicaIndex(pclqs []grovecorev1alpha1.PodClique) map[string][]grovecorev1alpha1.PodClique {
	return groupPCLQsByLabel(pclqs, apicommon.LabelPodCliqueSetReplicaIndex)
}

// GetMinAvailableBreachedPCLQInfo filters PodCliques that have grovecorev1alpha1.ConditionTypeMinAvailableBreached set to true.
// For each such PodClique it returns the name of the PodClique a duration to wait for before terminationDelay is breached.
func GetMinAvailableBreachedPCLQInfo(pclqs []grovecorev1alpha1.PodClique, terminationDelay time.Duration, since time.Time) ([]string, time.Duration) {
	pclqCandidateNames := make([]string, 0, len(pclqs))
	waitForDurations := make([]time.Duration, 0, len(pclqs))
	for _, pclq := range pclqs {
		cond := meta.FindStatusCondition(pclq.Status.Conditions, constants.ConditionTypeMinAvailableBreached)
		if cond == nil {
			continue
		}
		if cond.Status == metav1.ConditionTrue {
			pclqCandidateNames = append(pclqCandidateNames, pclq.Name)
			waitFor := terminationDelay - since.Sub(cond.LastTransitionTime.Time)
			waitForDurations = append(waitForDurations, waitFor)
		}
	}
	if len(pclqCandidateNames) == 0 {
		return nil, 0
	}
	slices.Sort(waitForDurations)
	return pclqCandidateNames, waitForDurations[0]
}

// GetPodCliquesWithParentPCS retrieves PodClique objects that are not part of any PodCliqueScalingGroup for the given PodCliqueSet.
func GetPodCliquesWithParentPCS(ctx context.Context, cl client.Client, pcsObjKey client.ObjectKey) ([]grovecorev1alpha1.PodClique, error) {
	pclqList := &grovecorev1alpha1.PodCliqueList{}
	err := cl.List(ctx,
		pclqList,
		client.InNamespace(pcsObjKey.Namespace),
		client.MatchingLabels(lo.Assign(
			apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjKey.Name),
			map[string]string{
				apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueSetPodClique,
			},
		)),
	)
	if err != nil {
		return nil, err
	}
	return pclqList.Items, nil
}

// groupPCLQsByLabel groups PodCliques by the value of the specified label key
func groupPCLQsByLabel(pclqs []grovecorev1alpha1.PodClique, labelKey string) map[string][]grovecorev1alpha1.PodClique {
	grouped := make(map[string][]grovecorev1alpha1.PodClique)
	for _, pclq := range pclqs {
		labelValue, exists := pclq.Labels[labelKey]
		if !exists {
			continue
		}
		grouped[labelValue] = append(grouped[labelValue], pclq)
	}
	return grouped
}

// IsPCLQUpdateInProgress checks if PodClique is under rolling update.
func IsPCLQUpdateInProgress(pclq *grovecorev1alpha1.PodClique) bool {
	return pclq.Status.RollingUpdateProgress != nil && pclq.Status.RollingUpdateProgress.UpdateEndedAt == nil
}

// IsLastPCLQUpdateCompleted checks if the last rolling update of PodClique is completed.
func IsLastPCLQUpdateCompleted(pclq *grovecorev1alpha1.PodClique) bool {
	return pclq.Status.RollingUpdateProgress != nil && pclq.Status.RollingUpdateProgress.UpdateEndedAt != nil
}

// FindPodCliqueTemplateSpecByName retrieves the PodCliqueTemplateSpec from the PodCliqueSet by its name.
// If there is no matching PodCliqueTemplateSpec, it returns nil.
func FindPodCliqueTemplateSpecByName(pcs *grovecorev1alpha1.PodCliqueSet, pclqName string) *grovecorev1alpha1.PodCliqueTemplateSpec {
	matchingPCLQTemplateSpec, ok := lo.Find(pcs.Spec.Template.Cliques, func(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec) bool {
		return pclqName == pclqTemplateSpec.Name
	})
	if !ok {
		return nil
	}
	return matchingPCLQTemplateSpec
}

// GetExpectedPCLQPodTemplateHash gets or computes the PodTemplateSpec hash for PodClique in a PodCliqueSet.
func GetExpectedPCLQPodTemplateHash(podTemplateSpecHashCache *hash.PodTemplateSpecHashCache, pcs *grovecorev1alpha1.PodCliqueSet, pclqObjectMeta metav1.ObjectMeta) (string, error) {
	cliqueName, err := utils.GetPodCliqueNameFromPodCliqueFQN(pclqObjectMeta)
	if err != nil {
		return "", fmt.Errorf("failed to find PodClique name from FQN in PCS: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	pclqTemplateSpec := FindPodCliqueTemplateSpecByName(pcs, cliqueName)
	if pclqTemplateSpec == nil {
		return "", fmt.Errorf("failed to find matching PodCliqueTemplateSpec for PodClique %v in PodCliqueSet %v", k8sutils.GetObjectKeyFromObjectMeta(pclqObjectMeta), client.ObjectKeyFromObject(pcs))
	}
	pclqPodHash, err := podTemplateSpecHashCache.GetOrCompute(pcs.Name, pcs.Generation, pclqTemplateSpec, pcs.Spec.Template.PriorityClassName)
	if err != nil {
		return "", fmt.Errorf("failed to compute pod template spec hash for PodClique %v: %w", k8sutils.GetObjectKeyFromObjectMeta(pclqObjectMeta), err)
	}
	return pclqPodHash, nil
}
