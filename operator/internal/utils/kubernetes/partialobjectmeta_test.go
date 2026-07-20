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

package kubernetes

import (
	"context"
	"fmt"
	"testing"

	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestListExistingPartialObjectMetadata tests listing partial object metadata for a GVK.
// It verifies listing with labels and namespace filtering works correctly.
func TestListExistingPartialObjectMetadata(t *testing.T) {
	podGVK := schema.GroupVersionKind{
		Version: "v1",
		Kind:    "Pod",
	}

	testCases := []struct {
		// name identifies the test case
		name string
		// existingObjects contains objects that exist in the cluster
		existingObjects []client.Object
		// gvk is the GroupVersionKind to list
		gvk schema.GroupVersionKind
		// ownerObjMeta is the owner object metadata used for namespace
		ownerObjMeta metav1.ObjectMeta
		// selectorLabels are the labels to match
		selectorLabels map[string]string
		// expectedCount is the expected number of items returned
		expectedCount int
		// expectError indicates if an error is expected
		expectError bool
	}{
		{
			// Successfully list pods matching labels in namespace
			name: "list-pods-with-matching-labels",
			existingObjects: []client.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: testNamespace,
						Labels:    map[string]string{"app": "test", "env": "prod"},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-2",
						Namespace: testNamespace,
						Labels:    map[string]string{"app": "test", "env": "dev"},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-3",
						Namespace: "other-namespace",
						Labels:    map[string]string{"app": "test"},
					},
				},
			},
			gvk:            podGVK,
			ownerObjMeta:   metav1.ObjectMeta{Namespace: testNamespace},
			selectorLabels: map[string]string{"app": "test"},
			expectedCount:  2,
			expectError:    false,
		},
		{
			// Empty list when no objects match labels
			name: "list-pods-no-matching-labels",
			existingObjects: []client.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: testNamespace,
						Labels:    map[string]string{"app": "other"},
					},
				},
			},
			gvk:            podGVK,
			ownerObjMeta:   metav1.ObjectMeta{Namespace: testNamespace},
			selectorLabels: map[string]string{"app": "test"},
			expectedCount:  0,
			expectError:    false,
		},
		{
			// Empty list when no objects in namespace
			name:            "list-pods-empty-namespace",
			existingObjects: []client.Object{},
			gvk:             podGVK,
			ownerObjMeta:    metav1.ObjectMeta{Namespace: testNamespace},
			selectorLabels:  map[string]string{"app": "test"},
			expectedCount:   0,
			expectError:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := testutils.CreateDefaultFakeClient(tc.existingObjects)
			items, err := ListExistingPartialObjectMetadata(context.Background(), fakeClient, tc.gvk, tc.ownerObjMeta, tc.selectorLabels)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedCount, len(items))
			}
		})
	}

	// Test error case with a client that returns errors
	t.Run("list-error", func(t *testing.T) {
		listErr := apierrors.NewInternalError(fmt.Errorf("simulated list error"))
		fakeClient := testutils.CreateFakeClientForObjectsMatchingLabels(nil, listErr, testNamespace, podGVK, map[string]string{"app": "test"})
		items, err := ListExistingPartialObjectMetadata(context.Background(), fakeClient, podGVK, metav1.ObjectMeta{Namespace: testNamespace}, map[string]string{"app": "test"})
		assert.Error(t, err)
		assert.Nil(t, items)
	})
}

// TestGetExistingPartialObjectMetadata tests getting partial object metadata for a specific object.
// It verifies getting objects by GVK and ObjectKey works correctly.
func TestGetExistingPartialObjectMetadata(t *testing.T) {
	podGVK := schema.GroupVersionKind{
		Version: "v1",
		Kind:    "Pod",
	}

	testCases := []struct {
		// name identifies the test case
		name string
		// existingObjects contains objects that exist in the cluster
		existingObjects []client.Object
		// gvk is the GroupVersionKind to get
		gvk schema.GroupVersionKind
		// objKey is the object key to get
		objKey client.ObjectKey
		// expectError indicates if an error is expected
		expectError bool
		// expectedName is the expected name of the retrieved object
		expectedName string
	}{
		{
			// Successfully get an existing pod
			name: "get-existing-pod",
			existingObjects: []client.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testResourceName,
						Namespace: testNamespace,
						Labels:    map[string]string{"app": "test"},
					},
				},
			},
			gvk:          podGVK,
			objKey:       client.ObjectKey{Name: testResourceName, Namespace: testNamespace},
			expectError:  false,
			expectedName: testResourceName,
		},
		{
			// Error when object doesn't exist
			name:            "get-non-existent-pod",
			existingObjects: []client.Object{},
			gvk:             podGVK,
			objKey:          client.ObjectKey{Name: testResourceName, Namespace: testNamespace},
			expectError:     true,
		},
		{
			// Error when object exists in different namespace
			name: "get-pod-wrong-namespace",
			existingObjects: []client.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testResourceName,
						Namespace: "other-namespace",
					},
				},
			},
			gvk:         podGVK,
			objKey:      client.ObjectKey{Name: testResourceName, Namespace: testNamespace},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := testutils.CreateDefaultFakeClient(tc.existingObjects)
			obj, err := GetExistingPartialObjectMetadata(context.Background(), fakeClient, tc.gvk, tc.objKey)

			if tc.expectError {
				assert.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err))
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, obj)
				assert.Equal(t, tc.expectedName, obj.Name)
				assert.Equal(t, tc.objKey.Namespace, obj.Namespace)
			}
		})
	}
}
