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

package controller

import (
	"context"
	"testing"

	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kai"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kube"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestReconcile tests the Reconcile method.
func TestReconcile(t *testing.T) {
	tests := []struct {
		name           string
		podGang        *groveschedulerv1alpha1.PodGang
		schedulerName  string
		expectError    bool
		setupMock      func(*groveschedulerv1alpha1.PodGang)
		verifyBehavior func(*testing.T, ctrl.Result, error)
	}{
		{
			name: "reconcile new podgang with kai backend",
			podGang: &groveschedulerv1alpha1.PodGang{
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
			},
			schedulerName: "kai-scheduler",
			expectError:   false,
		},
		{
			name: "reconcile new podgang with kube backend",
			podGang: &groveschedulerv1alpha1.PodGang{
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
			},
			schedulerName: "default-scheduler",
			expectError:   false,
		},
		{
			name: "reconcile deleted podgang",
			podGang: &groveschedulerv1alpha1.PodGang{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-podgang",
					Namespace:         "default",
					DeletionTimestamp: &metav1.Time{},
					Finalizers:        []string{"test-finalizer"},
				},
				Spec: groveschedulerv1alpha1.PodGangSpec{
					PodGroups: []groveschedulerv1alpha1.PodGroup{
						{
							Name:        "group-0",
							MinReplicas: 3,
						},
					},
				},
			},
			schedulerName: "kai-scheduler",
			expectError:   false,
		},
		{
			name: "reconcile updated podgang",
			podGang: &groveschedulerv1alpha1.PodGang{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-podgang",
					Namespace:  "default",
					Generation: 2,
				},
				Spec: groveschedulerv1alpha1.PodGangSpec{
					PodGroups: []groveschedulerv1alpha1.PodGroup{
						{
							Name:        "group-0",
							MinReplicas: 5,
						},
					},
				},
			},
			schedulerName: "kai-scheduler",
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup client with podgang
			cl := testutils.CreateDefaultFakeClient([]client.Object{tt.podGang})
			recorder := record.NewFakeRecorder(10)

			// Create appropriate backend
			var backend schedulerbackend.SchedulerBackend
			if tt.schedulerName == "kai-scheduler" {
				backend = kai.New(cl, cl.Scheme(), recorder)
			} else {
				backend = kube.New(cl, cl.Scheme(), recorder)
			}

			reconciler := &BackendReconciler{
				Client:  cl,
				Scheme:  cl.Scheme(),
				Backend: backend,
				Logger:  logr.Discard(),
			}

			// Execute reconcile
			ctx := context.Background()
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.podGang.Name,
					Namespace: tt.podGang.Namespace,
				},
			}

			result, err := reconciler.Reconcile(ctx, req)

			// Verify results
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, ctrl.Result{}, result)
			}

			// Custom verification if provided
			if tt.verifyBehavior != nil {
				tt.verifyBehavior(t, result, err)
			}
		})
	}
}

// TestReconcilePodGangNotFound tests reconciling a non-existent PodGang.
func TestReconcilePodGangNotFound(t *testing.T) {
	// Setup client without any podgang
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	backend := kai.New(cl, cl.Scheme(), recorder)

	reconciler := &BackendReconciler{
		Client:  cl,
		Scheme:  cl.Scheme(),
		Backend: backend,
		Logger:  logr.Discard(),
	}

	ctx := context.Background()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(ctx, req)

	// Should not error when PodGang is not found (likely deleted)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// TestReconcilePodGangWithDeletionTimestamp tests reconciling a PodGang being deleted.
func TestReconcilePodGangWithDeletionTimestamp(t *testing.T) {
	now := metav1.Now()
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-podgang",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{"test-finalizer"},
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

	cl := testutils.CreateDefaultFakeClient([]client.Object{podGang})
	recorder := record.NewFakeRecorder(10)
	backend := kai.New(cl, cl.Scheme(), recorder)

	reconciler := &BackendReconciler{
		Client:  cl,
		Scheme:  cl.Scheme(),
		Backend: backend,
		Logger:  logr.Discard(),
	}

	ctx := context.Background()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      podGang.Name,
			Namespace: podGang.Namespace,
		},
	}

	result, err := reconciler.Reconcile(ctx, req)

	// Should handle deletion without error
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// TestNewReconcilerWithBackend tests creating a reconciler with explicit backend.
func TestNewReconcilerWithBackend(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	backend := kai.New(cl, cl.Scheme(), recorder)

	reconciler := NewReconcilerWithBackend(cl, cl.Scheme(), backend)

	require.NotNil(t, reconciler)
	assert.Equal(t, cl, reconciler.Client)
	assert.Equal(t, cl.Scheme(), reconciler.Scheme)
	assert.Equal(t, backend, reconciler.Backend)
}

// TestPodGangSpecChangePredicate tests the event filter for PodGang changes.
func TestPodGangSpecChangePredicate(t *testing.T) {
	predicate := podGangSpecChangePredicate()

	// Verify that predicate is not nil
	require.NotNil(t, predicate)

	// The predicate is tested indirectly through controller integration
	// Direct testing of event filters requires complex event setup
	// This test ensures the predicate creation doesn't panic
	assert.NotNil(t, predicate)
}
