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

package pod

import (
	"encoding/json"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	annotationInPlaceUpdateState = "grove.io/in-place-update-state"
	annotationInPlaceUpdateGrace = "grove.io/in-place-update-grace"
)

const conditionInPlaceUpdateReady corev1.PodConditionType = "grove.io/InPlaceUpdateReady"

type inPlaceUpdateSpec struct {
	PodTemplateHash            string            `json:"podTemplateHash"`
	PodCliqueSetGenerationHash string            `json:"podCliqueSetGenerationHash"`
	ContainerImages            map[string]string `json:"containerImages,omitempty"`
}

type inPlaceUpdateState struct {
	PodTemplateHash            string                                  `json:"podTemplateHash"`
	PodCliqueSetGenerationHash string                                  `json:"podCliqueSetGenerationHash"`
	UpdateStartedAt            metav1.Time                             `json:"updateStartedAt,omitempty"`
	LastContainerStatuses      map[string]inPlaceUpdateContainerStatus `json:"lastContainerStatuses,omitempty"`
	ContainerImages            map[string]string                       `json:"containerImages,omitempty"`
}

type inPlaceUpdateContainerStatus struct {
	ImageID string `json:"imageID,omitempty"`
}

func injectInPlaceReadinessGate(pcs *grovecorev1alpha1.PodCliqueSet, pod *corev1.Pod) {
	if !componentutils.IsInPlaceUpdateStrategy(pcs) || hasInPlaceReadinessGate(pod) {
		return
	}
	pod.Spec.ReadinessGates = append(pod.Spec.ReadinessGates, corev1.PodReadinessGate{ConditionType: conditionInPlaceUpdateReady})
}

func hasInPlaceReadinessGate(pod *corev1.Pod) bool {
	for _, gate := range pod.Spec.ReadinessGates {
		if gate.ConditionType == conditionInPlaceUpdateReady {
			return true
		}
	}
	return false
}

func computeInPlaceUpdateSpec(existingPod, desiredPod *corev1.Pod, podTemplateHash, pcsGenerationHash string) (*inPlaceUpdateSpec, string) {
	if !hasInPlaceReadinessGate(existingPod) {
		return nil, "missing Grove in-place readiness gate"
	}
	if len(existingPod.Spec.InitContainers) != len(desiredPod.Spec.InitContainers) ||
		!equality.Semantic.DeepEqual(existingPod.Spec.InitContainers, desiredPod.Spec.InitContainers) {
		return nil, "initContainers changed"
	}
	if len(existingPod.Spec.Containers) != len(desiredPod.Spec.Containers) {
		return nil, "container set changed"
	}

	normalizedExisting := existingPod.DeepCopy()
	normalizedDesired := desiredPod.DeepCopy()
	normalizedDesired.Spec.SchedulingGates = normalizedExisting.Spec.SchedulingGates
	normalizedDesired.Spec.NodeName = normalizedExisting.Spec.NodeName

	containerImages := make(map[string]string)
	existingContainersByName := make(map[string]corev1.Container, len(normalizedExisting.Spec.Containers))
	for _, container := range normalizedExisting.Spec.Containers {
		existingContainersByName[container.Name] = container
	}
	for i := range normalizedDesired.Spec.Containers {
		desiredContainer := &normalizedDesired.Spec.Containers[i]
		existingContainer, ok := existingContainersByName[desiredContainer.Name]
		if !ok {
			return nil, fmt.Sprintf("container %s is new", desiredContainer.Name)
		}
		if desiredContainer.Image != existingContainer.Image {
			containerImages[desiredContainer.Name] = desiredContainer.Image
			desiredContainer.Image = existingContainer.Image
		}
	}

	if !equality.Semantic.DeepEqual(normalizedExisting.Spec, normalizedDesired.Spec) {
		for _, desiredContainer := range normalizedDesired.Spec.Containers {
			existingContainer := existingContainersByName[desiredContainer.Name]
			if !equality.Semantic.DeepEqual(existingContainer, desiredContainer) {
				return nil, fmt.Sprintf("container %s has non-image changes", desiredContainer.Name)
			}
		}
		return nil, "pod spec has non-image changes"
	}
	if len(containerImages) == 0 {
		return nil, "no container image changes"
	}
	return &inPlaceUpdateSpec{
		PodTemplateHash:            podTemplateHash,
		PodCliqueSetGenerationHash: pcsGenerationHash,
		ContainerImages:            containerImages,
	}, ""
}

func applyInPlaceUpdateSpec(pod *corev1.Pod, spec *inPlaceUpdateSpec) {
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	statuses := make(map[string]inPlaceUpdateContainerStatus, len(spec.ContainerImages))
	for _, status := range pod.Status.ContainerStatuses {
		if _, ok := spec.ContainerImages[status.Name]; ok {
			statuses[status.Name] = inPlaceUpdateContainerStatus{ImageID: status.ImageID}
		}
	}
	state := inPlaceUpdateState{
		PodTemplateHash:            spec.PodTemplateHash,
		PodCliqueSetGenerationHash: spec.PodCliqueSetGenerationHash,
		UpdateStartedAt:            metav1.Now(),
		LastContainerStatuses:      statuses,
		ContainerImages:            spec.ContainerImages,
	}
	stateJSON, _ := json.Marshal(state)
	pod.Annotations[annotationInPlaceUpdateState] = string(stateJSON)

	for i := range pod.Spec.Containers {
		if image, ok := spec.ContainerImages[pod.Spec.Containers[i].Name]; ok {
			pod.Spec.Containers[i].Image = image
		}
	}
	setInPlaceUpdateCondition(pod, corev1.ConditionFalse, "StartInPlaceUpdate")
}

func markInPlaceUpdateCompleteIfReady(pod *corev1.Pod) (bool, error) {
	state, ok, err := getInPlaceUpdateState(pod)
	if err != nil || !ok {
		return false, err
	}
	for containerName, oldStatus := range state.LastContainerStatuses {
		currentStatus := getContainerStatus(pod, containerName)
		if currentStatus == nil {
			return false, nil
		}
		if oldStatus.ImageID != "" && currentStatus.ImageID == oldStatus.ImageID {
			return false, nil
		}
		if !currentStatus.Ready {
			return false, nil
		}
	}
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[apicommon.LabelPodTemplateHash] = state.PodTemplateHash
	delete(pod.Annotations, annotationInPlaceUpdateGrace)
	delete(pod.Annotations, annotationInPlaceUpdateState)
	setInPlaceUpdateCondition(pod, corev1.ConditionTrue, "InPlaceUpdateComplete")
	return true, nil
}

func markInPlaceUpdateReadyIfIdle(pod *corev1.Pod) (bool, error) {
	if !hasInPlaceReadinessGate(pod) {
		return false, nil
	}
	if _, ok, err := getInPlaceUpdateState(pod); err != nil || ok {
		return false, err
	}
	condition := getInPlaceUpdateCondition(pod)
	if condition != nil && condition.Status == corev1.ConditionTrue {
		return false, nil
	}
	setInPlaceUpdateCondition(pod, corev1.ConditionTrue, "NoInPlaceUpdate")
	return true, nil
}

func getInPlaceUpdateState(pod *corev1.Pod) (*inPlaceUpdateState, bool, error) {
	if pod.Annotations == nil {
		return nil, false, nil
	}
	rawState, ok := pod.Annotations[annotationInPlaceUpdateState]
	if !ok {
		return nil, false, nil
	}
	state := &inPlaceUpdateState{}
	if err := json.Unmarshal([]byte(rawState), state); err != nil {
		return nil, false, err
	}
	return state, true, nil
}

func getContainerStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == name {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

func getInPlaceUpdateCondition(pod *corev1.Pod) *corev1.PodCondition {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == conditionInPlaceUpdateReady {
			return &pod.Status.Conditions[i]
		}
	}
	return nil
}

func setInPlaceUpdateCondition(pod *corev1.Pod, status corev1.ConditionStatus, reason string) {
	condition := corev1.PodCondition{
		Type:               conditionInPlaceUpdateReady,
		Status:             status,
		Reason:             reason,
		LastTransitionTime: metav1.Now(),
	}
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == conditionInPlaceUpdateReady {
			pod.Status.Conditions[i] = condition
			return
		}
	}
	pod.Status.Conditions = append(pod.Status.Conditions, condition)
}
