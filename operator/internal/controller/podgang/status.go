// /*
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
// */

package podgang

import (
	"context"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *Reconciler) reconcileStatus(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang, backend scheduler.Backend) error {
	var pods map[types.NamespacedName]*corev1.Pod
	if isPodGangInitialized(podGang) {
		var err error
		pods, err = r.listReferencedPods(ctx, podGang)
		if err != nil {
			return err
		}
	}
	scheduledCondition := getScheduledCondition(podGang, pods)
	schedulingBackendReadyCondition, backendStatusErr := getSchedulingBackendReadyCondition(ctx, podGang, backend)
	if backendStatusErr != nil {
		schedulingBackendReadyCondition = &metav1.Condition{
			Type:               string(groveschedulerv1alpha1.PodGangConditionTypeSchedulingBackendReady),
			Status:             metav1.ConditionUnknown,
			Reason:             groveschedulerv1alpha1.ConditionReasonSchedulingBackendStatusUnavailable,
			Message:            backendStatusErr.Error(),
			ObservedGeneration: podGang.Generation,
		}
	}

	original := podGang.DeepCopy()
	scheduledChanged := meta.SetStatusCondition(&podGang.Status.Conditions, scheduledCondition)
	var schedulingBackendReadyChanged bool
	if schedulingBackendReadyCondition == nil {
		schedulingBackendReadyChanged = meta.RemoveStatusCondition(
			&podGang.Status.Conditions,
			string(groveschedulerv1alpha1.PodGangConditionTypeSchedulingBackendReady),
		)
	} else {
		schedulingBackendReadyChanged = meta.SetStatusCondition(&podGang.Status.Conditions, *schedulingBackendReadyCondition)
	}
	if !scheduledChanged && !schedulingBackendReadyChanged {
		return backendStatusErr
	}
	if err := r.Status().Patch(ctx, podGang, client.MergeFrom(original)); err != nil {
		return err
	}
	return backendStatusErr
}

func getScheduledCondition(podGang *groveschedulerv1alpha1.PodGang, pods map[types.NamespacedName]*corev1.Pod) metav1.Condition {
	condition := metav1.Condition{
		Type:               string(groveschedulerv1alpha1.PodGangConditionTypeScheduled),
		ObservedGeneration: podGang.Generation,
	}
	if !isPodGangInitialized(podGang) {
		condition.Status = metav1.ConditionUnknown
		condition.Reason = groveschedulerv1alpha1.ConditionReasonPodGangNotInitialized
		condition.Message = "PodGang scheduling cannot be determined before initialization completes"
		return condition
	}

	for _, podGroup := range podGang.Spec.PodGroups {
		scheduled := countPodsWithCondition(podGroup.PodReferences, pods, corev1.PodScheduled)
		if scheduled >= podGroup.MinReplicas {
			continue
		}
		condition.Status = metav1.ConditionFalse
		condition.Reason = groveschedulerv1alpha1.ConditionReasonInsufficientScheduledPods
		condition.Message = fmt.Sprintf("PodGroup %q has %d of %d required Pods scheduled", podGroup.Name, scheduled, podGroup.MinReplicas)
		return condition
	}

	condition.Status = metav1.ConditionTrue
	condition.Reason = groveschedulerv1alpha1.ConditionReasonSufficientScheduledPods
	condition.Message = "All PodGroups satisfy MinReplicas"
	return condition
}

func getSchedulingBackendReadyCondition(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang, backend scheduler.Backend) (*metav1.Condition, error) {
	provider, ok := backend.(scheduler.PodGangStatusProvider)
	if !ok {
		return nil, nil
	}
	backendCondition, err := provider.GetPodGangSchedulingBackendCondition(ctx, podGang)
	if err != nil || backendCondition == nil {
		return nil, err
	}
	return &metav1.Condition{
		Type:               string(groveschedulerv1alpha1.PodGangConditionTypeSchedulingBackendReady),
		Status:             backendCondition.Status,
		Reason:             backendCondition.Reason,
		Message:            backendCondition.Message,
		ObservedGeneration: podGang.Generation,
	}, nil
}

func isPodGangInitialized(podGang *groveschedulerv1alpha1.PodGang) bool {
	return meta.IsStatusConditionTrue(podGang.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized))
}

func (r *Reconciler) listReferencedPods(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) (map[types.NamespacedName]*corev1.Pod, error) {
	namespaces := make(map[string]struct{})
	for _, podGroup := range podGang.Spec.PodGroups {
		for _, ref := range podGroup.PodReferences {
			namespaces[ref.Namespace] = struct{}{}
		}
	}

	pods := make(map[types.NamespacedName]*corev1.Pod)
	for namespace := range namespaces {
		podList := &corev1.PodList{}
		if err := r.List(
			ctx,
			podList,
			client.InNamespace(namespace),
			client.MatchingLabels{apicommon.LabelPodGang: podGang.Name},
		); err != nil {
			return nil, fmt.Errorf("failed to list Pods for PodGang %s/%s: %w", podGang.Namespace, podGang.Name, err)
		}
		for i := range podList.Items {
			pod := &podList.Items[i]
			pods[client.ObjectKeyFromObject(pod)] = pod
		}
	}
	return pods, nil
}

func countPodsWithCondition(references []groveschedulerv1alpha1.NamespacedName, pods map[types.NamespacedName]*corev1.Pod, conditionType corev1.PodConditionType) int32 {
	var count int32
	for _, ref := range references {
		pod := pods[types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}]
		if pod == nil || !pod.DeletionTimestamp.IsZero() {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == conditionType && condition.Status == corev1.ConditionTrue {
				count++
				break
			}
		}
	}
	return count
}
