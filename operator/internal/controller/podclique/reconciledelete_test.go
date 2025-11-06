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
	"fmt"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestTriggerDeletionFlow tests the deletion flow for PodClique
func TestTriggerDeletionFlow(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pclq is the PodClique being deleted
		pclq *grovecorev1alpha1.PodClique
		// existingResources are resources that exist in the cluster
		existingResources []client.Object
		// mockSetup sets up mock behavior
		mockSetup func(*mockOperatorRegistry)
		// expectedResult is the expected reconcile result
		expectedResult ctrlcommon.ReconcileStepResult
	}{
		{
			// Tests successful deletion flow
			name: "successful_deletion",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pclq",
					Namespace:  "default",
					Finalizers: []string{constants.FinalizerPodClique},
				},
			},
			mockSetup: func(registry *mockOperatorRegistry) {
				// Mock pod operator
				podOp := &mockOperator{
					deleteFunc: func(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) error {
						return nil
					},
					getExistingResourceNamesFunc: func(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) ([]string, error) {
						return []string{}, nil // No resources left
					},
				}
				registry.operators[component.KindPod] = podOp
			},
			expectedResult: ctrlcommon.DoNotRequeue(),
		},
		{
			// Tests deletion with resources still present
			name: "deletion_with_resources_awaiting_cleanup",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pclq",
					Namespace:  "default",
					Finalizers: []string{constants.FinalizerPodClique},
				},
			},
			mockSetup: func(registry *mockOperatorRegistry) {
				// Mock pod operator
				podOp := &mockOperator{
					deleteFunc: func(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) error {
						return nil
					},
					getExistingResourceNamesFunc: func(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) ([]string, error) {
						return []string{"pod-1", "pod-2"}, nil // Resources still exist
					},
				}
				registry.operators[component.KindPod] = podOp
			},
			expectedResult: ctrlcommon.ReconcileAfter(5*time.Second, "Resources are still awaiting cleanup. Skipping removal of finalizer"),
		},
		{
			// Tests deletion with error during resource deletion
			name: "deletion_with_error",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pclq",
					Namespace:  "default",
					Finalizers: []string{constants.FinalizerPodClique},
				},
			},
			mockSetup: func(registry *mockOperatorRegistry) {
				// Mock pod operator
				podOp := &mockOperator{
					deleteFunc: func(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) error {
						return fmt.Errorf("failed to delete pod")
					},
				}
				registry.operators[component.KindPod] = podOp
			},
			expectedResult: ctrlcommon.ReconcileWithErrors("error deleting managed resources", fmt.Errorf("failed to delete pod")),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup scheme
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			// Build runtime objects
			runtimeObjs := []client.Object{tc.pclq}
			runtimeObjs = append(runtimeObjs, tc.existingResources...)

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(runtimeObjs...).
				Build()

			// Create mock registry
			mockRegistry := &mockOperatorRegistry{
				operators: make(map[component.Kind]component.Operator[grovecorev1alpha1.PodClique]),
			}
			if tc.mockSetup != nil {
				tc.mockSetup(mockRegistry)
			}

			// Create reconciler
			r := &Reconciler{
				client:           fakeClient,
				operatorRegistry: mockRegistry,
			}

			// Execute deletion flow
			ctx := context.Background()
			logger := logr.Discard()
			result := r.triggerDeletionFlow(ctx, logger, tc.pclq)

			// Verify result
			assert.Equal(t, tc.expectedResult.NeedsRequeue(), result.NeedsRequeue())
			if tc.expectedResult.HasErrors() {
				assert.True(t, result.HasErrors())
			}
		})
	}
}

// mockOperator is a mock implementation of component.Operator
type mockOperator struct {
	deleteFunc                   func(ctx context.Context, logger logr.Logger, objMeta metav1.ObjectMeta) error
	syncFunc                     func(ctx context.Context, logger logr.Logger, obj *grovecorev1alpha1.PodClique) error
	getExistingResourceNamesFunc func(ctx context.Context, logger logr.Logger, objMeta metav1.ObjectMeta) ([]string, error)
}

func (m *mockOperator) Delete(ctx context.Context, logger logr.Logger, objMeta metav1.ObjectMeta) error {
	if m.deleteFunc != nil {
		return m.deleteFunc(ctx, logger, objMeta)
	}
	return nil
}

func (m *mockOperator) Sync(ctx context.Context, logger logr.Logger, obj *grovecorev1alpha1.PodClique) error {
	if m.syncFunc != nil {
		return m.syncFunc(ctx, logger, obj)
	}
	return nil
}

func (m *mockOperator) GetExistingResourceNames(ctx context.Context, logger logr.Logger, objMeta metav1.ObjectMeta) ([]string, error) {
	if m.getExistingResourceNamesFunc != nil {
		return m.getExistingResourceNamesFunc(ctx, logger, objMeta)
	}
	return []string{}, nil
}

// mockOperatorRegistry is a mock implementation of OperatorRegistry
type mockOperatorRegistry struct {
	operators map[component.Kind]component.Operator[grovecorev1alpha1.PodClique]
}

func (m *mockOperatorRegistry) Register(kind component.Kind, operator component.Operator[grovecorev1alpha1.PodClique]) {
	m.operators[kind] = operator
}

func (m *mockOperatorRegistry) GetOperator(kind component.Kind) (component.Operator[grovecorev1alpha1.PodClique], error) {
	op, ok := m.operators[kind]
	if !ok {
		return nil, fmt.Errorf("operator not found for kind: %s", kind)
	}
	return op, nil
}

func (m *mockOperatorRegistry) GetAllOperators() map[component.Kind]component.Operator[grovecorev1alpha1.PodClique] {
	return m.operators
}
