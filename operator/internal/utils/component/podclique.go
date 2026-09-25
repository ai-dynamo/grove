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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetPCLQsByOwner retrieves PodClique objects that are owned by the specified owner kind and object key, and match the provided selector labels.
func GetPCLQsByOwner(ctx context.Context, cl client.Client, ownerKind string, ownerObjectKey client.ObjectKey, selectorLabels map[string]string) ([]grovecorev1alpha1.PodClique, error) {
	pclqs, err := ListPCLQsMatchingLabels(ctx, cl, ownerObjectKey.Namespace, selectorLabels)
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

// ListPCLQsMatchingLabels lists all the PodClique's in a given namespace matching selectorLabels.
func ListPCLQsMatchingLabels(ctx context.Context, cl client.Client, namespace string, selectorLabels map[string]string) ([]grovecorev1alpha1.PodClique, error) {
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

// GroupPCLQsByPCSReplicaIndex filters PCLQs that have a PodCliqueSetReplicaIndex label and groups them by the PCS replica index.
// A PodCliqueSetReplicaIndex label that is not a valid integer is a contract violation and returns an error.
func GroupPCLQsByPCSReplicaIndex(pclqs []grovecorev1alpha1.PodClique) (map[int][]grovecorev1alpha1.PodClique, error) {
	grouped := make(map[int][]grovecorev1alpha1.PodClique)
	for labelValue, pclqsForReplica := range groupPCLQsByLabel(pclqs, apicommon.LabelPodCliqueSetReplicaIndex) {
		replicaIndex, err := strconv.Atoi(labelValue)
		if err != nil {
			return nil, fmt.Errorf("%s label value %q is not a valid integer", apicommon.LabelPodCliqueSetReplicaIndex, labelValue)
		}
		grouped[replicaIndex] = pclqsForReplica
	}
	return grouped, nil
}

// InitialScheduleGrace is the small window after PodCliqueScalingGroup creation in which a flipped
// status condition is treated as the first-time-set rather than a transition from a different state.
// WasPCSGEverHealthy uses it to absorb the gap between the apiserver setting CreationTimestamp and
// the first reconcile that mutates the MinAvailableBreached condition.
const InitialScheduleGrace = 5 * time.Second

// WasPCLQEverScheduled reports whether the PodClique has ever reached the PodCliqueScheduled=True
// state. It reads Status.LastScheduled, a durable marker stamped in the same status reconcile that
// first sets PodCliqueScheduled to True and never reset once set. A nil value means no reconcile has
// yet observed the PodClique meet its scheduled count.
func WasPCLQEverScheduled(pclq *grovecorev1alpha1.PodClique) bool {
	return pclq.Status.LastScheduled != nil
}

// WasPCSGEverHealthy reports whether the PodCliqueScalingGroup has ever reached the
// MinAvailableBreached=False state since creation. Mirrors WasPCLQEverScheduled but reads the
// PCSG's own MinAvailableBreached condition (PCSGs have no PodCliqueScheduled equivalent).
// Used to gate gang-termination so an initial-startup PCSG that has not yet stabilized is left
// alone — only regressions from a previously-healthy state get recycled.
// Shares the observed-transitions-only limitation documented on WasPCLQEverScheduled.
func WasPCSGEverHealthy(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) bool {
	cond := meta.FindStatusCondition(pcsg.Status.Conditions, constants.ConditionTypeMinAvailableBreached)
	if cond == nil {
		return false
	}
	if cond.Status == metav1.ConditionFalse {
		return true
	}
	return cond.LastTransitionTime.After(pcsg.CreationTimestamp.Add(InitialScheduleGrace))
}

// GetMinAvailableBreachedPCLQInfo filters PodCliques that have grovecorev1alpha1.ConditionTypeMinAvailableBreached set to true.
// For each such PodClique it returns the name of the PodClique a duration to wait for before terminationDelay is breached.
//
// PodCliques that have never been scheduled (per WasPCLQEverScheduled) are excluded — the
// MinAvailableBreached condition is still True on them (operators can observe the state) but
// gang-termination would only churn-loop Pending pods against a cluster that already cannot
// schedule them. Recycling makes sense only after a workload has been healthy and then regressed.
func GetMinAvailableBreachedPCLQInfo(pclqs []grovecorev1alpha1.PodClique, terminationDelay time.Duration, since time.Time) ([]string, time.Duration) {
	pclqCandidateNames := make([]string, 0, len(pclqs))
	waitForDurations := make([]time.Duration, 0, len(pclqs))
	for _, pclq := range pclqs {
		cond := meta.FindStatusCondition(pclq.Status.Conditions, constants.ConditionTypeMinAvailableBreached)
		if cond == nil {
			continue
		}
		if cond.Status != metav1.ConditionTrue {
			continue
		}
		if !WasPCLQEverScheduled(&pclq) {
			continue
		}
		pclqCandidateNames = append(pclqCandidateNames, pclq.Name)
		waitFor := terminationDelay - since.Sub(cond.LastTransitionTime.Time)
		waitForDurations = append(waitForDurations, waitFor)
	}
	if len(pclqCandidateNames) == 0 {
		return nil, 0
	}
	slices.Sort(waitForDurations)
	return pclqCandidateNames, waitForDurations[0]
}

// GetPodCliquesWithParentPCS retrieves PodClique objects that are not part of any PodCliqueScalingGroup for the given PodCliqueSet.
// GetPodCliquesWithParentPCS retrieves PodClique objects that are not part of any PodCliqueScalingGroup for the given PodCliqueSet.
func GetPodCliquesWithParentPCS(ctx context.Context, cl client.Client, pcsObjMeta metav1.ObjectMeta) ([]grovecorev1alpha1.PodClique, error) {
	pclqList := &grovecorev1alpha1.PodCliqueList{}
	err := cl.List(ctx,
		pclqList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(lo.Assign(
			apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjMeta.Name),
			map[string]string{
				apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueSetPodClique,
			},
		)),
	)
	if err != nil {
		return nil, err
	}
	// Exclude PodCliques controlled by an older PodCliqueSet of the same name, so a recreated
	// PodCliqueSet does not ingest a deleted one's leftover children.
	return lo.Filter(pclqList.Items, func(pclq grovecorev1alpha1.PodClique, _ int) bool {
		return metav1.IsControlledBy(&pclq, &pcsObjMeta)
	}), nil
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

// ComputePCLQPodTemplateHash computes the pod template hash for the PCLQ pod spec.
func ComputePCLQPodTemplateHash(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, priorityClassName string) string {
	podTemplateSpec := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      pclqTemplateSpec.Labels,
			Annotations: pclqTemplateSpec.Annotations,
		},
		Spec: pclqTemplateSpec.Spec.PodSpec,
	}
	podTemplateSpec.Spec.PriorityClassName = priorityClassName
	return k8sutils.ComputeHash(&podTemplateSpec)
}

// IsPCLQAutoUpdateInProgress checks if PodClique is under an auto-orchestrated update.
func IsPCLQAutoUpdateInProgress(pclq *grovecorev1alpha1.PodClique) bool {
	return pclq.Status.UpdateProgress != nil && pclq.Status.UpdateProgress.UpdateEndedAt == nil
}

// IsLastPCLQUpdateCompleted checks if the last update of PodClique is completed.
// For rolling update strategies, it returns if all Pods of the PodClique have been updated with the new specification.
// For the OnDelete strategy, it returns whether the PodClique controller has processed the update by refreshing all hash fields in the PodCliqueStatus, based on which PodCliqueStatus.UpdatedReplicas are calculated.
func IsLastPCLQUpdateCompleted(pclq *grovecorev1alpha1.PodClique) bool {
	return pclq.Status.UpdateProgress != nil && pclq.Status.UpdateProgress.UpdateEndedAt != nil
}

// GetExpectedPCLQPodTemplateHash finds the matching PodCliqueTemplateSpec from the PodCliqueSet and computes the pod template hash for the PCLQ pod spec.
func GetExpectedPCLQPodTemplateHash(pcs *grovecorev1alpha1.PodCliqueSet, pclqObjectMeta metav1.ObjectMeta) (string, error) {
	cliqueName, err := GetPodCliqueNameFromPodCliqueFQN(pclqObjectMeta)
	if err != nil {
		return "", err
	}
	matchingPCLQTemplateSpec := FindPodCliqueTemplateSpecByName(pcs, cliqueName)
	if matchingPCLQTemplateSpec == nil {
		return "", fmt.Errorf("pod clique template not found for cliqueName: %s", cliqueName)
	}
	return ComputePCLQPodTemplateHash(matchingPCLQTemplateSpec, pcs.Spec.Template.PriorityClassName), nil
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

// GetPodCliqueNameFromPodCliqueFQN extracts the unqualified PodClique name from a fully qualified name.
func GetPodCliqueNameFromPodCliqueFQN(pclqObjectMeta metav1.ObjectMeta) (string, error) {
	pclqObjectKey := client.ObjectKey{Name: pclqObjectMeta.Name, Namespace: pclqObjectMeta.Namespace}
	pcsgName, ok := pclqObjectMeta.Labels[apicommon.LabelPodCliqueScalingGroup]
	if ok {
		// get the pcsg replica index
		pcsgReplicaIndex, replicaIndexLabelFound := pclqObjectMeta.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex]
		if !replicaIndexLabelFound {
			return "", fmt.Errorf("missing label %s on PodClique: %v", apicommon.LabelPodCliqueScalingGroupReplicaIndex, pclqObjectKey)
		}
		pcsgReplicaIndexInt, err := strconv.Atoi(pcsgReplicaIndex)
		if err != nil {
			return "", fmt.Errorf("invalid label %s on PodClique: %v: %w", apicommon.LabelPodCliqueScalingGroupReplicaIndex, pclqObjectKey, err)
		}
		return apicommon.ExtractScalingGroupNameFromPCSGFQN(pclqObjectMeta.Name, apicommon.ResourceNameReplica{Name: pcsgName, Replica: pcsgReplicaIndexInt})
	}

	pcsName, ok := pclqObjectMeta.Labels[apicommon.LabelPartOfKey]
	if !ok {
		return "", fmt.Errorf("missing label %s on PodClique: %v", apicommon.LabelPartOfKey, pclqObjectKey)
	}
	// Get the PCS replica index
	pcsReplicaIndex, ok := pclqObjectMeta.Labels[apicommon.LabelPodCliqueSetReplicaIndex]
	if !ok {
		return "", fmt.Errorf("missing label %s on PodClique: %v", apicommon.LabelPodCliqueSetReplicaIndex, pclqObjectKey)
	}
	pcsReplicaIndexInt, err := strconv.Atoi(pcsReplicaIndex)
	if err != nil {
		return "", fmt.Errorf("invalid label %s on PodClique: %v: %w", apicommon.LabelPodCliqueSetReplicaIndex, pclqObjectKey, err)
	}
	return apicommon.ExtractScalingGroupNameFromPCSGFQN(pclqObjectMeta.Name, apicommon.ResourceNameReplica{Name: pcsName, Replica: pcsReplicaIndexInt})
}
