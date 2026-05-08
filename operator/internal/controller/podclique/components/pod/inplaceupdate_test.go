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
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInjectInPlaceReadinessGateForInPlaceStrategy(t *testing.T) {
	pcs := newInPlaceTestPCS(grovecorev1alpha1.InPlaceIfPossibleStrategy)
	pod := &corev1.Pod{}

	injectInPlaceReadinessGate(pcs, pod)

	assert.True(t, hasInPlaceReadinessGate(pod), "in-place update Pods should carry the readiness gate")
}

func TestInjectInPlaceReadinessGateSkipsRollingRecreate(t *testing.T) {
	pcs := newInPlaceTestPCS(grovecorev1alpha1.RollingRecreateStrategy)
	pod := &corev1.Pod{}

	injectInPlaceReadinessGate(pcs, pod)

	assert.False(t, hasInPlaceReadinessGate(pod), "rolling recreate Pods should preserve existing readiness gate behavior")
}

func TestComputeInPlaceUpdateSpecAllowsContainerImageOnlyChange(t *testing.T) {
	oldPod := newReadyInPlacePod("pod-1", testOldHash, "app:v1", "old-image-id")
	desiredPod := oldPod.DeepCopy()
	desiredPod.Spec.Containers[0].Image = "app:v2"

	spec, reason := computeInPlaceUpdateSpec(oldPod, desiredPod, testNewHash, "pcs-hash")

	require.NotNil(t, spec, "image-only changes should be eligible for in-place update: %s", reason)
	assert.Equal(t, map[string]string{"main": "app:v2"}, spec.ContainerImages)
	assert.Equal(t, testNewHash, spec.PodTemplateHash)
	assert.Equal(t, "pcs-hash", spec.PodCliqueSetGenerationHash)
}

func TestComputeInPlaceUpdateSpecRejectsEnvChange(t *testing.T) {
	oldPod := newReadyInPlacePod("pod-1", testOldHash, "app:v1", "old-image-id")
	desiredPod := oldPod.DeepCopy()
	desiredPod.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "NEW_ENV", Value: "true"}}

	spec, reason := computeInPlaceUpdateSpec(oldPod, desiredPod, testNewHash, "pcs-hash")

	require.Nil(t, spec)
	assert.Contains(t, reason, "container main has non-image changes")
}

func TestApplyInPlaceUpdateSpecPatchesImageAndDefersTemplateHash(t *testing.T) {
	pod := newReadyInPlacePod("pod-1", testOldHash, "app:v1", "old-image-id")
	spec := &inPlaceUpdateSpec{
		PodTemplateHash:            testNewHash,
		PodCliqueSetGenerationHash: "pcs-hash",
		ContainerImages:            map[string]string{"main": "app:v2"},
	}

	applyInPlaceUpdateSpec(pod, spec)

	assert.Equal(t, "app:v2", pod.Spec.Containers[0].Image)
	assert.Equal(t, testOldHash, pod.Labels[apicommon.LabelPodTemplateHash], "template hash should change only after kubelet reports completion")
	assert.Contains(t, pod.Annotations, annotationInPlaceUpdateState)
	assert.Equal(t, corev1.ConditionFalse, getInPlaceUpdateCondition(pod).Status)
}

func TestMarkInPlaceUpdateCompleteUpdatesTemplateHashAfterImageIDChanges(t *testing.T) {
	pod := newReadyInPlacePod("pod-1", testOldHash, "app:v1", "old-image-id")
	spec := &inPlaceUpdateSpec{
		PodTemplateHash:            testNewHash,
		PodCliqueSetGenerationHash: "pcs-hash",
		ContainerImages:            map[string]string{"main": "app:v2"},
	}
	applyInPlaceUpdateSpec(pod, spec)
	pod.Status.ContainerStatuses[0].ImageID = "new-image-id"
	pod.Status.ContainerStatuses[0].Image = "app:v2"

	completed, err := markInPlaceUpdateCompleteIfReady(pod)

	require.NoError(t, err)
	require.True(t, completed)
	assert.Equal(t, testNewHash, pod.Labels[apicommon.LabelPodTemplateHash])
	assert.Equal(t, corev1.ConditionTrue, getInPlaceUpdateCondition(pod).Status)
}

func TestMarkInPlaceUpdateReadyIfIdleSetsGateTrue(t *testing.T) {
	pod := newReadyInPlacePod("pod-1", testOldHash, "app:v1", "old-image-id")
	pod.Status.Conditions = nil

	changed, err := markInPlaceUpdateReadyIfIdle(pod)

	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, corev1.ConditionTrue, getInPlaceUpdateCondition(pod).Status)
}

func TestMarkInPlaceUpdateReadyIfIdleSkipsUpdatingPod(t *testing.T) {
	pod := newReadyInPlacePod("pod-1", testOldHash, "app:v1", "old-image-id")
	spec := &inPlaceUpdateSpec{
		PodTemplateHash:            testNewHash,
		PodCliqueSetGenerationHash: "pcs-hash",
		ContainerImages:            map[string]string{"main": "app:v2"},
	}
	applyInPlaceUpdateSpec(pod, spec)

	changed, err := markInPlaceUpdateReadyIfIdle(pod)

	require.NoError(t, err)
	require.False(t, changed)
	assert.Equal(t, corev1.ConditionFalse, getInPlaceUpdateCondition(pod).Status)
}

func newInPlaceTestPCS(strategy grovecorev1alpha1.UpdateStrategyType) *grovecorev1alpha1.PodCliqueSet {
	return &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: testNS},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: strategy},
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
					Name: "worker",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						RoleName: "worker",
						PodSpec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "main", Image: "app:v1"}},
						},
					},
				}},
			},
		},
	}
}

func newInPlaceTestPCLQ(name string) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNS,
			Labels: map[string]string{
				apicommon.LabelPartOfKey:       "pcs",
				apicommon.LabelPodTemplateHash: testOldHash,
			},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			RoleName: "worker",
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Image: "app:v1"}},
			},
		},
	}
}

func newReadyInPlacePod(name, templateHash, image, imageID string) *corev1.Pod {
	pod := newTestPod(name, templateHash,
		withPhase(corev1.PodRunning),
		withReadyCondition(),
		withContainerStatus(&[]bool{true}[0], true),
	)
	pod.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: conditionInPlaceUpdateReady}}
	pod.Spec.Containers = []corev1.Container{{Name: "main", Image: image}}
	pod.Status.ContainerStatuses[0].Name = "main"
	pod.Status.ContainerStatuses[0].Image = image
	pod.Status.ContainerStatuses[0].ImageID = imageID
	return pod
}
