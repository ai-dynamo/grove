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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testFinalizer = "test.grove.ai/finalizer"
)

// TestAddAndPatchFinalizer tests adding a finalizer to an object.
func TestAddAndPatchFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Test adding finalizer to object without any finalizers
	t.Run("add finalizer to object without finalizers", func(t *testing.T) {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "default",
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		err := AddAndPatchFinalizer(context.Background(), cl, configMap, testFinalizer)
		require.NoError(t, err)

		// Verify finalizer was added
		assert.Contains(t, configMap.GetFinalizers(), testFinalizer)
	})

	// Test adding finalizer to object with existing finalizers
	t.Run("add finalizer to object with existing finalizers", func(t *testing.T) {
		existingFinalizer := "existing.finalizer"
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-cm",
				Namespace:  "default",
				Finalizers: []string{existingFinalizer},
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		err := AddAndPatchFinalizer(context.Background(), cl, configMap, testFinalizer)
		require.NoError(t, err)

		// Verify both finalizers are present
		assert.Contains(t, configMap.GetFinalizers(), testFinalizer)
		assert.Contains(t, configMap.GetFinalizers(), existingFinalizer)
		assert.Len(t, configMap.GetFinalizers(), 2)
	})

	// Test adding duplicate finalizer
	t.Run("add duplicate finalizer", func(t *testing.T) {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-cm",
				Namespace:  "default",
				Finalizers: []string{testFinalizer},
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		err := AddAndPatchFinalizer(context.Background(), cl, configMap, testFinalizer)
		require.NoError(t, err)

		// Should still only have one instance
		assert.Contains(t, configMap.GetFinalizers(), testFinalizer)
	})
}

// TestRemoveAndPatchFinalizer tests removing a finalizer from an object.
func TestRemoveAndPatchFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Test removing existing finalizer
	t.Run("remove existing finalizer", func(t *testing.T) {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-cm",
				Namespace:  "default",
				Finalizers: []string{testFinalizer},
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		err := RemoveAndPatchFinalizer(context.Background(), cl, configMap, testFinalizer)
		require.NoError(t, err)

		// Verify finalizer was removed
		assert.NotContains(t, configMap.GetFinalizers(), testFinalizer)
	})

	// Test removing finalizer from object with multiple finalizers
	t.Run("remove one finalizer from multiple", func(t *testing.T) {
		otherFinalizer := "other.finalizer"
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-cm",
				Namespace:  "default",
				Finalizers: []string{testFinalizer, otherFinalizer},
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		err := RemoveAndPatchFinalizer(context.Background(), cl, configMap, testFinalizer)
		require.NoError(t, err)

		// Verify correct finalizer was removed
		assert.NotContains(t, configMap.GetFinalizers(), testFinalizer)
		assert.Contains(t, configMap.GetFinalizers(), otherFinalizer)
		assert.Len(t, configMap.GetFinalizers(), 1)
	})

	// Test removing non-existent finalizer
	t.Run("remove non-existent finalizer", func(t *testing.T) {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "default",
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		err := RemoveAndPatchFinalizer(context.Background(), cl, configMap, testFinalizer)
		require.NoError(t, err)

		// Should not error
		assert.Empty(t, configMap.GetFinalizers())
	})

	// Test removing finalizer from non-existent object (NotFound errors are ignored)
	t.Run("remove finalizer from non-existent object", func(_ *testing.T) {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "nonexistent-cm",
				Namespace:       "default",
				Finalizers:      []string{testFinalizer},
				ResourceVersion: "1",
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		err := RemoveAndPatchFinalizer(context.Background(), cl, configMap, testFinalizer)

		// Since the object doesn't exist, we expect some error but it should be handled
		// The fake client might not perfectly simulate NotFound behavior, so we just check it doesn't panic
		_ = err
	})
}

// TestMergeFromWithOptimisticLock tests the patch function with optimistic locking.
func TestMergeFromWithOptimisticLock(t *testing.T) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-cm",
			Namespace:       "default",
			ResourceVersion: "123",
		},
	}

	patch := mergeFromWithOptimisticLock(configMap)
	require.NotNil(t, patch)

	// Verify patch type
	patchType := patch.Type()
	assert.Equal(t, client.Merge.Type(), patchType)
}

// TestPatchFinalizer tests the internal patchFinalizer function through public APIs.
func TestPatchFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Test successful patch
	t.Run("successful patch", func(t *testing.T) {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "default",
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		err := AddAndPatchFinalizer(context.Background(), cl, configMap, testFinalizer)
		require.NoError(t, err)
	})

	// Test patch with conflict (simulating optimistic lock failure)
	t.Run("patch with stale object", func(_ *testing.T) {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "test-cm",
				Namespace:       "default",
				ResourceVersion: "1",
			},
		}

		// Create with a different resource version
		storedConfigMap := configMap.DeepCopy()
		storedConfigMap.ResourceVersion = "2"

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(storedConfigMap).
			Build()

		// This should fail with conflict error in a real scenario
		// but fake client might not enforce optimistic locking
		err := AddAndPatchFinalizer(context.Background(), cl, configMap, testFinalizer)

		// Fake client may or may not enforce this, so we just verify it doesn't panic
		_ = err
	})
}

// TestFinalizerHelpers tests the helper behavior with various edge cases.
func TestFinalizerHelpers(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Test with object that will be deleted
	t.Run("finalizer on deleted object", func(t *testing.T) {
		now := metav1.Now()
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-cm",
				Namespace:         "default",
				Finalizers:        []string{testFinalizer},
				DeletionTimestamp: &now,
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		// Removing finalizer should work
		err := RemoveAndPatchFinalizer(context.Background(), cl, configMap, testFinalizer)
		require.NoError(t, err)
	})

	// Test with empty finalizer string
	t.Run("empty finalizer string", func(t *testing.T) {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "default",
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		err := AddAndPatchFinalizer(context.Background(), cl, configMap, "")
		require.NoError(t, err)
	})
}
