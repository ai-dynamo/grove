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
	"errors"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type statusProviderBackend struct {
	scheduler.Backend
	condition *scheduler.PodGangSchedulingBackendCondition
	err       error
}

func (b *statusProviderBackend) GetPodGangSchedulingBackendCondition(_ context.Context, _ *groveschedulerv1alpha1.PodGang) (*scheduler.PodGangSchedulingBackendCondition, error) {
	return b.condition, b.err
}

func TestGetScheduledCondition(t *testing.T) {
	const namespace = "default"
	podGang := testutils.NewPodGangBuilder("test-podgang", namespace).
		WithGeneration(2).
		WithManaged(true).
		Build()
	podGang.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{{
		Name:        "workers",
		MinReplicas: 2,
		PodReferences: []groveschedulerv1alpha1.NamespacedName{
			{Namespace: namespace, Name: "worker-0"},
			{Namespace: namespace, Name: "worker-1"},
		},
	}}
	t.Run("unknown before initialization", func(t *testing.T) {
		r := &Reconciler{Client: testutils.CreateDefaultFakeClient(nil)}
		condition := scheduledConditionForTest(t, r, podGang.DeepCopy())
		assert.Equal(t, metav1.ConditionUnknown, condition.Status)
		assert.Equal(t, groveschedulerv1alpha1.ConditionReasonPodGangNotInitialized, condition.Reason)
		assert.Equal(t, int64(2), condition.ObservedGeneration)
	})

	initializedPodGang := podGang.DeepCopy()
	initializedPodGang.Status.Conditions = []metav1.Condition{{
		Type:   string(groveschedulerv1alpha1.PodGangConditionTypeInitialized),
		Status: metav1.ConditionTrue,
	}}

	t.Run("false when a PodGroup has insufficient scheduled Pods", func(t *testing.T) {
		r := &Reconciler{Client: testutils.CreateDefaultFakeClient([]client.Object{
			scheduledPod("worker-0", namespace, podGang.Name),
			unscheduledPod("worker-1", namespace, podGang.Name),
		})}
		condition := scheduledConditionForTest(t, r, initializedPodGang.DeepCopy())
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Equal(t, groveschedulerv1alpha1.ConditionReasonInsufficientScheduledPods, condition.Reason)
		assert.Equal(t, `PodGroup "workers" has 1 of 2 required Pods scheduled`, condition.Message)
	})

	t.Run("true when every PodGroup satisfies MinReplicas", func(t *testing.T) {
		r := &Reconciler{Client: testutils.CreateDefaultFakeClient([]client.Object{
			scheduledPod("worker-0", namespace, podGang.Name),
			scheduledPod("worker-1", namespace, podGang.Name),
		})}
		condition := scheduledConditionForTest(t, r, initializedPodGang.DeepCopy())
		assert.Equal(t, metav1.ConditionTrue, condition.Status)
		assert.Equal(t, groveschedulerv1alpha1.ConditionReasonSufficientScheduledPods, condition.Reason)
	})
}

func TestGetSchedulingBackendReadyCondition(t *testing.T) {
	podGang := testutils.NewPodGangBuilder("test-podgang", "default").
		WithGeneration(4).
		WithManaged(true).
		Build()
	backend := &statusProviderBackend{
		Backend: testutils.NewFakeSchedulerBackend("test-scheduler"),
		condition: &scheduler.PodGangSchedulingBackendCondition{
			Status:  metav1.ConditionFalse,
			Reason:  "QueueDoesNotExist",
			Message: "queue is missing",
		},
	}

	condition, err := getSchedulingBackendReadyCondition(t.Context(), podGang, backend)
	require.NoError(t, err)
	require.NotNil(t, condition)
	assert.Equal(t, string(groveschedulerv1alpha1.PodGangConditionTypeSchedulingBackendReady), condition.Type)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, "QueueDoesNotExist", condition.Reason)
	assert.Equal(t, "queue is missing", condition.Message)
	assert.Equal(t, int64(4), condition.ObservedGeneration)

	condition, err = getSchedulingBackendReadyCondition(t.Context(), podGang, testutils.NewFakeSchedulerBackend("test-scheduler"))
	require.NoError(t, err)
	assert.Nil(t, condition)
}

func TestReconcileStatus(t *testing.T) {
	podGang, cl := createInitializedPodGangWithScheduledPod(t)
	r := &Reconciler{Client: cl}
	current := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(podGang), current))
	backend := &statusProviderBackend{
		Backend: testutils.NewFakeSchedulerBackend("test-scheduler"),
		condition: &scheduler.PodGangSchedulingBackendCondition{
			Status:  metav1.ConditionFalse,
			Reason:  "QueueDoesNotExist",
			Message: "queue is missing",
		},
	}

	require.NoError(t, r.reconcileStatus(t.Context(), current, backend))
	updated := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(podGang), updated))
	scheduledCondition := apimeta.FindStatusCondition(updated.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeScheduled))
	require.NotNil(t, scheduledCondition)
	assert.Equal(t, metav1.ConditionTrue, scheduledCondition.Status)
	schedulingBackendReadyCondition := apimeta.FindStatusCondition(updated.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeSchedulingBackendReady))
	require.NotNil(t, schedulingBackendReadyCondition)
	assert.Equal(t, metav1.ConditionFalse, schedulingBackendReadyCondition.Status)
	assert.Equal(t, "QueueDoesNotExist", schedulingBackendReadyCondition.Reason)
}

func TestReconcileStatusPersistsGroveConditionOnBackendError(t *testing.T) {
	podGang, cl := createInitializedPodGangWithScheduledPod(t)
	r := &Reconciler{Client: cl}
	current := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(podGang), current))
	backendErr := errors.New("backend status unavailable")
	backend := &statusProviderBackend{
		Backend: testutils.NewFakeSchedulerBackend("test-scheduler"),
		err:     backendErr,
	}

	require.ErrorIs(t, r.reconcileStatus(t.Context(), current, backend), backendErr)
	updated := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(podGang), updated))
	scheduledCondition := apimeta.FindStatusCondition(updated.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeScheduled))
	require.NotNil(t, scheduledCondition)
	assert.Equal(t, metav1.ConditionTrue, scheduledCondition.Status)
	schedulingBackendReadyCondition := apimeta.FindStatusCondition(updated.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeSchedulingBackendReady))
	require.NotNil(t, schedulingBackendReadyCondition)
	assert.Equal(t, metav1.ConditionUnknown, schedulingBackendReadyCondition.Status)
	assert.Equal(t, groveschedulerv1alpha1.ConditionReasonSchedulingBackendStatusUnavailable, schedulingBackendReadyCondition.Reason)
}

func scheduledConditionForTest(t *testing.T, r *Reconciler, podGang *groveschedulerv1alpha1.PodGang) metav1.Condition {
	t.Helper()
	var pods map[types.NamespacedName]*corev1.Pod
	if isPodGangInitialized(podGang) {
		var err error
		pods, err = r.listReferencedPods(t.Context(), podGang)
		require.NoError(t, err)
	}
	return getScheduledCondition(podGang, pods)
}

func createInitializedPodGangWithScheduledPod(t *testing.T) (*groveschedulerv1alpha1.PodGang, client.Client) {
	t.Helper()
	const namespace = "default"
	podGang := testutils.NewPodGangBuilder("test-podgang", namespace).
		WithGeneration(1).
		WithManaged(true).
		Build()
	podGang.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{{
		Name:        "workers",
		MinReplicas: 1,
		PodReferences: []groveschedulerv1alpha1.NamespacedName{{
			Namespace: namespace,
			Name:      "worker-0",
		}},
	}}
	podGang.Status.Conditions = []metav1.Condition{{
		Type:   string(groveschedulerv1alpha1.PodGangConditionTypeInitialized),
		Status: metav1.ConditionTrue,
	}}
	return podGang, testutils.NewTestClientBuilder().
		WithObjects(podGang, scheduledPod("worker-0", namespace, podGang.Name)).
		WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
		Build()
}

func scheduledPod(name, namespace, podGangName string) *corev1.Pod {
	pod := unscheduledPod(name, namespace, podGangName)
	pod.Status.Conditions = []corev1.PodCondition{{
		Type:   corev1.PodScheduled,
		Status: corev1.ConditionTrue,
	}}
	return pod
}

func unscheduledPod(name, namespace, podGangName string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		Labels:    map[string]string{apicommon.LabelPodGang: podGangName},
	}}
}
