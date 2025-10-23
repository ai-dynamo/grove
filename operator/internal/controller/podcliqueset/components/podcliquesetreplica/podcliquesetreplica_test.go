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

package podcliquesetreplica

import (
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestNew tests creating a new PodCliqueSetReplica operator
func TestNew(t *testing.T) {
	// Tests creating a new operator instance
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	eventRecorder := &record.FakeRecorder{}
	
	operator := New(client, eventRecorder)
	
	assert.NotNil(t, operator)
	resource, ok := operator.(*_resource)
	assert.True(t, ok)
	assert.Equal(t, client, resource.client)
	assert.Equal(t, eventRecorder, resource.eventRecorder)
}

// TestGetExistingResourceNames tests the GetExistingResourceNames function
func TestGetExistingResourceNames(t *testing.T) {
	// Tests that GetExistingResourceNames always returns empty slice
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	
	r := &_resource{
		client: client,
	}
	
	names, err := r.GetExistingResourceNames(context.Background(), logr.Discard(), metav1.ObjectMeta{})
	
	assert.NoError(t, err)
	assert.Empty(t, names)
}

// TestDelete tests the Delete function
func TestDelete(t *testing.T) {
	// Tests that Delete is a no-op
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	
	r := &_resource{
		client: client,
	}
	
	err := r.Delete(context.Background(), logr.Discard(), metav1.ObjectMeta{})
	
	assert.NoError(t, err)
}

// TestSync tests the Sync function
func TestSync(t *testing.T) {
	tests := []struct {
		name         string
		// pcs is the PodCliqueSet to sync
		pcs          *grovecorev1alpha1.PodCliqueSet
		// existingObjs are the existing objects in the cluster
		existingObjs []runtime.Object
		// expectError indicates if an error is expected
		expectError  bool
	}{
		{
			// Tests sync with no replicas
			name: "no_replicas",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 0,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{},
					},
				},
			},
			existingObjs: []runtime.Object{},
			expectError:  false,
		},
		{
			// Tests sync with replicas
			name: "with_replicas",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 2,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{},
					},
				},
			},
			existingObjs: []runtime.Object{},
			expectError:  false,
		},
	}

	ctx := context.Background()
	logger := logr.Discard()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjs...).
				WithStatusSubresource(tc.pcs).
				Build()

			r := &_resource{
				client:        fakeClient,
				eventRecorder: &record.FakeRecorder{},
			}

			err := r.Sync(ctx, logger, tc.pcs)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				// May return a requeue error which is expected
				if err != nil {
					groveErr, ok := err.(*groveerr.GroveError)
					if ok && groveErr.Code == groveerr.ErrCodeContinueReconcileAndRequeue {
						// This is expected
					} else {
						t.Errorf("Unexpected error: %v", err)
					}
				}
			}
		})
	}
}

// TestIsRollingUpdateInProgress tests the isRollingUpdateInProgress function
func TestIsRollingUpdateInProgress(t *testing.T) {
	tests := []struct {
		name     string
		// pcs is the PodCliqueSet to check
		pcs      *grovecorev1alpha1.PodCliqueSet
		// expected is the expected result
		expected bool
	}{
		{
			// Tests when rolling update is not in progress
			name: "no_rolling_update",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					RollingUpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			// Tests when rolling update is in progress
			name: "rolling_update_in_progress",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueSetRollingUpdateProgress{
						UpdateEndedAt: nil,
					},
				},
			},
			expected: true,
		},
		{
			// Tests when rolling update has ended
			name: "rolling_update_ended",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueSetRollingUpdateProgress{
						UpdateEndedAt: &metav1.Time{},
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isRollingUpdateInProgress(tc.pcs)
			assert.Equal(t, tc.expected, result)
		})
	}
}