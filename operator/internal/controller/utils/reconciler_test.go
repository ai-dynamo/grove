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

package utils

import (
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestGetPodCliqueSet tests the GetPodCliqueSet function
func TestGetPodCliqueSet(t *testing.T) {
	tests := []struct {
		name string
		// namespacedName is the object key
		namespacedName types.NamespacedName
		// existingPCS is the existing PodCliqueSet
		existingPCS *grovecorev1alpha1.PodCliqueSet
		// notFound indicates if the object should not be found
		notFound bool
		// expectedResult is the expected ReconcileStepResult
		expectedNeedsRequeue bool
		// expectLogError indicates if we expect an error to be logged
		expectLogError bool
	}{
		{
			// Tests successful retrieval of PodCliqueSet
			name: "successful_retrieval",
			namespacedName: types.NamespacedName{
				Name:      "test-pcs",
				Namespace: "default",
			},
			existingPCS: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
			},
			notFound:             false,
			expectedNeedsRequeue: false,
			expectLogError:       false,
		},
		{
			// Tests when PodCliqueSet is not found
			name: "not_found",
			namespacedName: types.NamespacedName{
				Name:      "test-pcs",
				Namespace: "default",
			},
			existingPCS:          nil,
			notFound:             true,
			expectedNeedsRequeue: false, // DoNotRequeue() returns false for NeedsRequeue
			expectLogError:       false,
		},
		{
			// Tests when PodCliqueSet is being deleted
			name: "being_deleted",
			namespacedName: types.NamespacedName{
				Name:      "test-pcs",
				Namespace: "default",
			},
			existingPCS: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-pcs",
					Namespace:         "default",
					DeletionTimestamp: &metav1.Time{},
					Finalizers:        []string{"test-finalizer"},
				},
			},
			notFound:             false,
			expectedNeedsRequeue: false, // GetPodCliqueSet doesn't check deletion timestamp, just returns ContinueReconcile()
			expectLogError:       false,
		},
	}

	ctx := context.Background()
	logger := logr.Discard()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tc.existingPCS != nil {
				builder = builder.WithRuntimeObjects(tc.existingPCS)
			}
			fakeClient := builder.Build()

			pcs := &grovecorev1alpha1.PodCliqueSet{}
			result := GetPodCliqueSet(ctx, fakeClient, logger, tc.namespacedName, pcs)

			assert.Equal(t, tc.expectedNeedsRequeue, result.NeedsRequeue())

			if !tc.notFound {
				assert.Equal(t, tc.namespacedName.Name, pcs.Name)
				assert.Equal(t, tc.namespacedName.Namespace, pcs.Namespace)
			}
		})
	}
}

// TestGetPodClique tests the GetPodClique function
func TestGetPodClique(t *testing.T) {
	tests := []struct {
		name string
		// namespacedName is the object key
		namespacedName types.NamespacedName
		// existingPCLQ is the existing PodClique
		existingPCLQ *grovecorev1alpha1.PodClique
		// notFound indicates if the object should not be found
		notFound bool
		// expectedResult is the expected ReconcileStepResult
		expectedNeedsRequeue bool
	}{
		{
			// Tests successful retrieval of PodClique
			name: "successful_retrieval",
			namespacedName: types.NamespacedName{
				Name:      "test-pclq",
				Namespace: "default",
			},
			existingPCLQ: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "default",
				},
			},
			notFound:             false,
			expectedNeedsRequeue: false,
		},
		{
			// Tests when PodClique is not found
			name: "not_found",
			namespacedName: types.NamespacedName{
				Name:      "test-pclq",
				Namespace: "default",
			},
			existingPCLQ:         nil,
			notFound:             true,
			expectedNeedsRequeue: false, // When ignoreNotFound is true and not found, returns DoNotRequeue()
		},
	}

	ctx := context.Background()
	logger := logr.Discard()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tc.existingPCLQ != nil {
				builder = builder.WithRuntimeObjects(tc.existingPCLQ)
			}
			fakeClient := builder.Build()

			pclq := &grovecorev1alpha1.PodClique{}
			result := GetPodClique(ctx, fakeClient, logger, tc.namespacedName, pclq, true)

			assert.Equal(t, tc.expectedNeedsRequeue, result.NeedsRequeue())

			if !tc.notFound {
				assert.Equal(t, tc.namespacedName.Name, pclq.Name)
				assert.Equal(t, tc.namespacedName.Namespace, pclq.Namespace)
			}
		})
	}
}

// TestGetPodCliqueScalingGroup tests the GetPodCliqueScalingGroup function
func TestGetPodCliqueScalingGroup(t *testing.T) {
	tests := []struct {
		name string
		// namespacedName is the object key
		namespacedName types.NamespacedName
		// existingPCSG is the existing PodCliqueScalingGroup
		existingPCSG *grovecorev1alpha1.PodCliqueScalingGroup
		// notFound indicates if the object should not be found
		notFound bool
		// expectedResult is the expected ReconcileStepResult
		expectedNeedsRequeue bool
	}{
		{
			// Tests successful retrieval of PodCliqueScalingGroup
			name: "successful_retrieval",
			namespacedName: types.NamespacedName{
				Name:      "test-pcsg",
				Namespace: "default",
			},
			existingPCSG: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcsg",
					Namespace: "default",
				},
			},
			notFound:             false,
			expectedNeedsRequeue: false,
		},
		{
			// Tests when PodCliqueScalingGroup is not found
			name: "not_found",
			namespacedName: types.NamespacedName{
				Name:      "test-pcsg",
				Namespace: "default",
			},
			existingPCSG:         nil,
			notFound:             true,
			expectedNeedsRequeue: false, // When not found, returns DoNotRequeue()
		},
	}

	ctx := context.Background()
	logger := logr.Discard()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tc.existingPCSG != nil {
				builder = builder.WithRuntimeObjects(tc.existingPCSG)
			}
			fakeClient := builder.Build()

			pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{}
			result := GetPodCliqueScalingGroup(ctx, fakeClient, logger, tc.namespacedName, pcsg)

			assert.Equal(t, tc.expectedNeedsRequeue, result.NeedsRequeue())

			if !tc.notFound {
				assert.Equal(t, tc.namespacedName.Name, pcsg.Name)
				assert.Equal(t, tc.namespacedName.Namespace, pcsg.Namespace)
			}
		})
	}
}
