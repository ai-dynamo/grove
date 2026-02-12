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

package schedulerbackend

import (
	"context"
	"sync"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

// TestPreparePod tests the global PreparePod function.
func TestPreparePod(t *testing.T) {
	tests := []struct {
		name          string
		schedulerName configv1alpha1.SchedulerName
		inputPod      *corev1.Pod
		expectError   bool
		expectName    string
	}{
		{
			name:          "prepare pod with kai backend",
			schedulerName: configv1alpha1.SchedulerNameKai,
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{},
			},
			expectError: false,
			expectName:  "kai-scheduler",
		},
		{
			name:          "prepare pod with kube backend",
			schedulerName: configv1alpha1.SchedulerNameKube,
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{},
			},
			expectError: false,
			expectName:  "default-scheduler",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset global state
			globalBackend = nil
			initOnce = sync.Once{}

			// Initialize backend
			cl := testutils.CreateDefaultFakeClient(nil)
			recorder := record.NewFakeRecorder(10)
			err := Initialize(cl, cl.Scheme(), recorder, configv1alpha1.SchedulerConfiguration{Name: tt.schedulerName})
			require.NoError(t, err)

			// Test PreparePod
			err = PreparePod(tt.inputPod)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestPreparePodWhenNotInitialized tests PreparePod when backend is not initialized.
func TestPreparePodWhenNotInitialized(t *testing.T) {
	// Reset global state to ensure backend is not initialized
	globalBackend = nil
	initOnce = sync.Once{}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{},
	}

	err := PreparePod(pod)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend not initialized")
}

// mockBackend is a mock implementation of SchedulerBackend for testing.
type mockBackend struct {
	name              string
	initCalled        bool
	syncCalled        bool
	deleteCalled      bool
	prepareCalled     bool
	returnError       error
	lastPreparedPod   *corev1.Pod
	lastSyncedPodGang *groveschedulerv1alpha1.PodGang
}

func (m *mockBackend) Name() string {
	return m.name
}

func (m *mockBackend) Init() error {
	m.initCalled = true
	return m.returnError
}

func (m *mockBackend) SyncPodGang(_ context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	m.syncCalled = true
	m.lastSyncedPodGang = podGang
	return m.returnError
}

func (m *mockBackend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	m.deleteCalled = true
	return m.returnError
}

func (m *mockBackend) PreparePod(pod *corev1.Pod) {
	m.prepareCalled = true
	m.lastPreparedPod = pod
	pod.Spec.SchedulerName = m.name
}

// TestSchedulerBackendInterface tests that backends implement the interface correctly.
func TestSchedulerBackendInterface(t *testing.T) {
	mock := &mockBackend{name: "mock-backend"}

	// Test Name
	assert.Equal(t, "mock-backend", mock.Name())

	// Test Init
	err := mock.Init()
	require.NoError(t, err)
	assert.True(t, mock.initCalled)

	// Test SyncPodGang
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-podgang",
			Namespace: "default",
		},
	}
	ctx := context.Background()
	err = mock.SyncPodGang(ctx, podGang)
	require.NoError(t, err)
	assert.True(t, mock.syncCalled)
	assert.Equal(t, podGang, mock.lastSyncedPodGang)

	// Test OnPodGangDelete
	err = mock.OnPodGangDelete(ctx, podGang)
	require.NoError(t, err)
	assert.True(t, mock.deleteCalled)

	// Test PreparePod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{},
	}
	mock.PreparePod(pod)
	assert.True(t, mock.prepareCalled)
	assert.Equal(t, pod, mock.lastPreparedPod)
	assert.Equal(t, "mock-backend", pod.Spec.SchedulerName)
}
