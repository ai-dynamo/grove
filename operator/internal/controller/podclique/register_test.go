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

package podclique

import (
	"context"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TestControllerConstants tests the controller constants
func TestControllerConstants(t *testing.T) {
	// Verifies that controller name is set correctly
	assert.Equal(t, "podclique-controller", controllerName)
}

// mockInnerHandler records whether Delete was called and forwards to no-op implementations.
type mockInnerHandler struct {
	deleteCalled bool
	deleteObject client.Object
}

func (m *mockInnerHandler) Create(_ context.Context, _ event.TypedCreateEvent[client.Object], _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (m *mockInnerHandler) Update(_ context.Context, _ event.TypedUpdateEvent[client.Object], _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (m *mockInnerHandler) Delete(_ context.Context, e event.TypedDeleteEvent[client.Object], _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	m.deleteCalled = true
	m.deleteObject = e.Object
}

func (m *mockInnerHandler) Generic(_ context.Context, _ event.TypedGenericEvent[client.Object], _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

// managedPodWithPodCliqueOwner returns a Pod that isManagedPod() and has a PodClique owner (so the
// pod delete handler will call ObserveDeletions for it). Used to simulate the issue #457 scenario:
// a pending pod is manually deleted; the handler must lower create expectations so the next reconcile recreates it.
func managedPodWithPodCliqueOwner(namespace, podName, pclqName string, podUID types.UID) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      podName,
			UID:       podUID,
			Labels: map[string]string{
				common.LabelManagedByKey: common.LabelManagedByValue,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: grovecorev1alpha1.SchemeGroupVersion.Group + "/" + grovecorev1alpha1.SchemeGroupVersion.Version,
					Kind:       constants.KindPodClique,
					Name:       pclqName,
					UID:        types.UID("pclq-uid"),
					Controller: ptr.To(true),
				},
			},
		},
		Spec:   corev1.PodSpec{},
		Status: corev1.PodStatus{},
	}
}

// TestPodWatchHandler_Delete tests the pod watch handler's Delete path for the scenario in issue #457:
// when a managed pod (e.g. pending) is manually deleted, the informer sees a Delete event before the next reconcile.
// The handler must call ObserveDeletions so the pod's UID is removed from create expectations (uidsToAdd),
// allowing the controller to recreate the pod on the next reconcile instead of treating it as "informer slow".
func TestPodWatchHandler_Delete(t *testing.T) {
	const ns, pclqName, podName = "default", "pclq-1", "pclq-1-0"
	pclqKey, err := expect.ControlleeKeyFunc(&grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: pclqName}})
	require.NoError(t, err)

	t.Run("managed pod with PodClique owner: ObserveDeletions removes UID from create expectations so pod can be recreated", func(t *testing.T) {
		store := expect.NewExpectationsStore()
		podUID := types.UID("pod-deleted-manually")
		require.NoError(t, store.ExpectCreations(logr.Discard(), pclqKey, podUID))

		createExpectations := store.GetCreateExpectations(pclqKey)
		require.Contains(t, createExpectations, podUID, "setup: create expectation should contain pod UID")

		inner := &mockInnerHandler{}
		h := &podWatchHandler{expectationsStore: store, inner: inner}
		pod := managedPodWithPodCliqueOwner(ns, podName, pclqName, podUID)
		q := workqueue.NewTypedRateLimitingQueue[reconcile.Request](workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		ctx := context.Background()

		h.Delete(ctx, event.TypedDeleteEvent[client.Object]{Object: pod}, q)

		createExpectationsAfter := store.GetCreateExpectations(pclqKey)
		assert.NotContains(t, createExpectationsAfter, podUID,
			"ObserveDeletions should remove the deleted pod UID from uidsToAdd so next reconcile sees diff < 0 and recreates the pod (issue #457)")
		assert.True(t, inner.deleteCalled, "inner handler Delete should still be called to enqueue reconcile")
		assert.Equal(t, pod, inner.deleteObject)
	})
}
