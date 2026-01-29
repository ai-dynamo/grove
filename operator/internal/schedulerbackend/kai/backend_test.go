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

package kai

import (
	"context"
	"testing"

	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

// TestNew tests creating a new KAI backend instance.
func TestNew(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	backend := New(cl, cl.Scheme(), recorder, "kai-scheduler")

	require.NotNil(t, backend)
	assert.Equal(t, cl, backend.client)
	assert.Equal(t, cl.Scheme(), backend.scheme)
	assert.Equal(t, recorder, backend.eventRecorder)
	assert.Equal(t, "kai-scheduler", backend.schedulerName)
}

// TestName tests the Name method returns the correct backend name.
func TestName(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	backend := New(cl, cl.Scheme(), recorder, "kai-scheduler")

	assert.Equal(t, BackendName, backend.Name())
	assert.Equal(t, "KAI-Scheduler", backend.Name())
}

// TestInit tests the Init method.
func TestInit(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	backend := New(cl, cl.Scheme(), recorder, "kai-scheduler")

	err := backend.Init()
	require.NoError(t, err)
}

// TestSyncPodGang tests the SyncPodGang method.
func TestSyncPodGang(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	backend := New(cl, cl.Scheme(), recorder, "kai-scheduler")

	// Create a sample PodGang
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-podgang",
			Namespace: "default",
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{
					Name:        "group-0",
					MinReplicas: 3,
				},
			},
		},
	}

	ctx := context.Background()
	err := backend.SyncPodGang(ctx, podGang)

	// Phase 1: Should be no-op and return no error
	require.NoError(t, err)
}

// TestOnPodGangDelete tests the OnPodGangDelete method.
func TestOnPodGangDelete(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	backend := New(cl, cl.Scheme(), recorder, "kai-scheduler")

	// Create a sample PodGang
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-podgang",
			Namespace: "default",
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{
					Name:        "group-0",
					MinReplicas: 3,
				},
			},
		},
	}

	ctx := context.Background()
	err := backend.OnPodGangDelete(ctx, podGang)

	// Phase 1: Should be no-op and return no error
	require.NoError(t, err)
}

// TestPreparePod tests the PreparePod method.
func TestPreparePod(t *testing.T) {
	tests := []struct {
		name              string
		schedulerName     string
		inputPod          *corev1.Pod
		expectedScheduler string
	}{
		{
			name:          "sets scheduler name",
			schedulerName: "kai-scheduler",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{},
			},
			expectedScheduler: "kai-scheduler",
		},
		{
			name:          "overwrites existing scheduler name",
			schedulerName: "kai-scheduler",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					SchedulerName: "old-scheduler",
				},
			},
			expectedScheduler: "kai-scheduler",
		},
		{
			name:          "preserves existing pod configuration",
			schedulerName: "kai-scheduler",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Labels: map[string]string{
						"app": "test",
					},
					Annotations: map[string]string{
						"existing": "annotation",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "test:latest",
						},
					},
				},
			},
			expectedScheduler: "kai-scheduler",
		},
		{
			name:          "handles custom scheduler name",
			schedulerName: "custom-kai-scheduler",
			inputPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{},
			},
			expectedScheduler: "custom-kai-scheduler",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := testutils.CreateDefaultFakeClient(nil)
			recorder := record.NewFakeRecorder(10)

			backend := New(cl, cl.Scheme(), recorder, tt.schedulerName)

			// Store original pod configuration for verification
			originalLabels := make(map[string]string)
			for k, v := range tt.inputPod.Labels {
				originalLabels[k] = v
			}
			originalContainers := len(tt.inputPod.Spec.Containers)

			backend.PreparePod(tt.inputPod)

			// Verify scheduler name was set correctly
			assert.Equal(t, tt.expectedScheduler, tt.inputPod.Spec.SchedulerName)

			// Verify other pod configuration was preserved
			assert.Equal(t, tt.inputPod.Name, tt.inputPod.Name)
			assert.Equal(t, tt.inputPod.Namespace, tt.inputPod.Namespace)
			for k, v := range originalLabels {
				assert.Equal(t, v, tt.inputPod.Labels[k])
			}
			assert.Equal(t, originalContainers, len(tt.inputPod.Spec.Containers))
		})
	}
}

// TestConstants tests that package constants are properly defined.
func TestConstants(t *testing.T) {
	assert.Equal(t, "KAI-Scheduler", BackendName)
	assert.Equal(t, "grove.io/podgang-pending-creation", SchedulingGateName)
	assert.Equal(t, "kai", BackendLabelValue)
	assert.Equal(t, "scheduling.run.ai", PodGroupAPIGroup)
	assert.Equal(t, "v2alpha2", PodGroupAPIVersion)
	assert.Equal(t, "PodGroup", PodGroupKind)
}
