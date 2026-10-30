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

package pod

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	pclqexp "github.com/ai-dynamo/grove/operator/internal/controller/podclique/expectations"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDeleteTerminalPodsFreesTheReplicaSlot(t *testing.T) {
	const podGangName = "test-pcs-0-w-0"
	newPod := func(name string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name),
				Labels: map[string]string{apicommon.LabelPodGang: podGangName}},
			Status: corev1.PodStatus{Phase: phase},
		}
	}
	failed := newPod("failed", corev1.PodFailed)
	succeeded := newPod("succeeded", corev1.PodSucceeded)
	running := newPod("running", corev1.PodRunning)

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(failed, succeeded, running).Build()

	r := _resource{client: fakeClient, scheme: scheme, eventRecorder: record.NewFakeRecorder(10), expectationsStore: expect.NewExpectationsStore()}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-0-w-0-w", Namespace: "default"},
		Spec:       grovecorev1alpha1.PodCliqueSpec{Replicas: 3},
	}
	ss := &syncSnapshot{pclq: pclq, existingPCLQPods: []*corev1.Pod{failed, succeeded, running}, pcsgReplicaPodGangName: podGangName}

	require.NoError(t, r.deleteTerminalPods(context.Background(), logr.Discard(), ss))

	// Only the terminal Pods are deleted; the running one is untouched.
	assert.True(t, apierrors.IsNotFound(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(failed), &corev1.Pod{})))
	assert.True(t, apierrors.IsNotFound(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(succeeded), &corev1.Pod{})))
	assert.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(running), &corev1.Pod{}))

	key, err := pclqexp.PodGangScopedExpectationsStoreKey(pclq.ObjectMeta, podGangName)
	require.NoError(t, err)
	assert.ElementsMatch(t, []types.UID{"failed", "succeeded"}, r.expectationsStore.GetDeleteExpectations(key))

	// In the same reconcile the snapshot still lists the deleted Pods, and the delete expectations net
	// them out, so the PodCliqueScalingGroup delta asks for two replacements.
	delta, err := r.computePodCountDelta(ss)
	require.NoError(t, err)
	assert.Equal(t, 2, delta)
}
