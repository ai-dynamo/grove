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

package kubernetes

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testPCSName      = "test-pcs"
	testNamespace    = "test-ns"
	testResourceName = "test-resource"
	version          = "v1alpha1"
)

// Test helper functions
func newTestObjectMetaWithOwnerRefs(name, namespace string, ownerRefs ...metav1.OwnerReference) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:            name,
		Namespace:       namespace,
		OwnerReferences: ownerRefs,
	}
}

func newTestOwnerReference(name string, uid types.UID, isController bool) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: version,
		Kind:       constants.KindPodCliqueSet,
		Name:       name,
		UID:        uid,
		Controller: ptr.To(isController),
	}
}

func newTestOwnerReferenceSimple(name string, isController bool) metav1.OwnerReference {
	return newTestOwnerReference(name, uuid.NewUUID(), isController)
}

func TestGetDefaultLabelsForPodCliqueSetManagedResources(t *testing.T) {
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(testPCSName)
	assert.Equal(t, labels, map[string]string{
		"app.kubernetes.io/managed-by": "grove-operator",
		"app.kubernetes.io/part-of":    testPCSName,
	})
}

func TestFilterMapOwnedResourceNames(t *testing.T) {
	testOwnerObjMeta := metav1.ObjectMeta{
		Name:      testPCSName,
		Namespace: testNamespace,
		UID:       uuid.NewUUID(),
	}
	testCases := []struct {
		description           string
		ownerObjMeta          metav1.ObjectMeta
		candidateResources    []metav1.PartialObjectMetadata
		expectedResourceNames []string
	}{
		{
			description:  "no resources owned by the owner object",
			ownerObjMeta: testOwnerObjMeta,
			candidateResources: []metav1.PartialObjectMetadata{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "resource1",
						Namespace: testNamespace,
						UID:       uuid.NewUUID(),
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "v1",
								Kind:       "PodCliqueSet",
								Name:       "other-pcs",
								UID:        uuid.NewUUID(),
								Controller: ptr.To(true),
							},
						},
					},
				},
			},
			expectedResourceNames: []string{},
		},
		{
			description:  "mixed ownership - some resources owned, some not",
			ownerObjMeta: testOwnerObjMeta,
			candidateResources: []metav1.PartialObjectMetadata{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "owned-resource",
						Namespace: testNamespace,
						UID:       uuid.NewUUID(),
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "v1",
								Kind:       "PodCliqueSet",
								Name:       testPCSName,
								UID:        testOwnerObjMeta.UID,
								Controller: ptr.To(true),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unowned-resource",
						Namespace: testNamespace,
						UID:       uuid.NewUUID(),
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "v1",
								Kind:       "PodCliqueSet",
								Name:       "other-pcs",
								UID:        uuid.NewUUID(),
								Controller: ptr.To(true),
							},
						},
					},
				},
			},
			expectedResourceNames: []string{"owned-resource"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			resourceNames := FilterMapOwnedResourceNames(tc.ownerObjMeta, tc.candidateResources)
			assert.Equal(t, tc.expectedResourceNames, resourceNames)
		})
	}
}

func TestGetFirstOwnerName(t *testing.T) {
	testCases := []struct {
		description  string
		resourceMeta metav1.ObjectMeta
		expectedName string
	}{
		{
			description: "should return first owner name when owner references exist",
			resourceMeta: newTestObjectMetaWithOwnerRefs(testResourceName, testNamespace,
				newTestOwnerReferenceSimple("first-owner", true),
				newTestOwnerReferenceSimple("second-owner", false)),
			expectedName: "first-owner",
		},
		{
			description: "should return single owner name when only one owner exists",
			resourceMeta: newTestObjectMetaWithOwnerRefs(testResourceName, testNamespace,
				newTestOwnerReferenceSimple("only-owner", true)),
			expectedName: "only-owner",
		},
		{
			description:  "should return empty string when no owner references exist",
			resourceMeta: newTestObjectMetaWithOwnerRefs(testResourceName, testNamespace),
			expectedName: "",
		},
		{
			description: "should return empty string when owner references is nil",
			resourceMeta: metav1.ObjectMeta{
				Name:      testResourceName,
				Namespace: testNamespace,
			},
			expectedName: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := GetFirstOwnerName(tc.resourceMeta)
			assert.Equal(t, tc.expectedName, result)
		})
	}
}

func TestFindOwnerRefByKind(t *testing.T) {
	podCliqueRef := metav1.OwnerReference{Kind: constants.KindPodClique, Name: "pclq-1"}
	podCliqueSetRef := metav1.OwnerReference{Kind: constants.KindPodCliqueSet, Name: "pcs-1"}
	testCases := []struct {
		description  string
		ownerRefs    []metav1.OwnerReference
		kind         string
		expectFound  bool
		expectedKind string
		expectedName string
	}{
		{
			description:  "finds matching kind",
			ownerRefs:    []metav1.OwnerReference{podCliqueSetRef, podCliqueRef},
			kind:         constants.KindPodClique,
			expectFound:  true,
			expectedKind: constants.KindPodClique,
			expectedName: "pclq-1",
		},
		{
			description:  "returns first match when multiple of same kind",
			ownerRefs:    []metav1.OwnerReference{podCliqueRef, {Kind: constants.KindPodClique, Name: "pclq-2"}},
			kind:         constants.KindPodClique,
			expectFound:  true,
			expectedKind: constants.KindPodClique,
			expectedName: "pclq-1",
		},
		{
			description: "returns nil when no match",
			ownerRefs:   []metav1.OwnerReference{podCliqueRef},
			kind:        constants.KindPodCliqueSet,
			expectFound: false,
		},
		{
			description: "returns nil when ownerRefs empty",
			ownerRefs:   nil,
			kind:        constants.KindPodClique,
			expectFound: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := FindOwnerRefByKind(tc.ownerRefs, tc.kind)
			if tc.expectFound {
				require.NotNil(t, result)
				assert.Equal(t, tc.expectedKind, result.Kind)
				assert.Equal(t, tc.expectedName, result.Name)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

func TestGetObjectKeyFromObjectMeta(t *testing.T) {
	testCases := []struct {
		description string
		objMeta     metav1.ObjectMeta
		expectedKey client.ObjectKey
	}{
		{
			description: "should create object key with namespace and name",
			objMeta: metav1.ObjectMeta{
				Name:      testResourceName,
				Namespace: testNamespace,
			},
			expectedKey: client.ObjectKey{
				Name:      testResourceName,
				Namespace: testNamespace,
			},
		},
		{
			description: "should create object key with empty namespace for cluster-scoped resources",
			objMeta: metav1.ObjectMeta{
				Name:      "test-cluster-resource",
				Namespace: "",
			},
			expectedKey: client.ObjectKey{
				Name:      "test-cluster-resource",
				Namespace: "",
			},
		},
		{
			description: "should create object key with empty name",
			objMeta: metav1.ObjectMeta{
				Name:      "",
				Namespace: testNamespace,
			},
			expectedKey: client.ObjectKey{
				Name:      "",
				Namespace: testNamespace,
			},
		},
		{
			description: "should create object key with both empty namespace and name",
			objMeta: metav1.ObjectMeta{
				Name:      "",
				Namespace: "",
			},
			expectedKey: client.ObjectKey{
				Name:      "",
				Namespace: "",
			},
		},
		{
			description: "should create object key ignoring other metadata fields",
			objMeta: metav1.ObjectMeta{
				Name:      "test-resource",
				Namespace: testNamespace,
				Labels: map[string]string{
					"app": "test",
				},
				Annotations: map[string]string{
					"note": "test annotation",
				},
				UID:               uuid.NewUUID(),
				ResourceVersion:   "123",
				Generation:        1,
				CreationTimestamp: metav1.Now(),
			},
			expectedKey: client.ObjectKey{
				Name:      "test-resource",
				Namespace: testNamespace,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := GetObjectKeyFromObjectMeta(tc.objMeta)
			assert.Equal(t, tc.expectedKey, result)
		})
	}
}

func TestIsResourceTerminating(t *testing.T) {
	now := metav1.Now()

	testCases := []struct {
		description    string
		objMeta        metav1.ObjectMeta
		expectedResult bool
	}{
		{
			description: "deletion timestamp set - resource terminating",
			objMeta: metav1.ObjectMeta{
				Name:              testResourceName,
				Namespace:         testNamespace,
				DeletionTimestamp: &now,
			},
			expectedResult: true,
		},
		{
			description: "deletion timestamp nil - resource not terminating",
			objMeta: metav1.ObjectMeta{
				Name:              testResourceName,
				Namespace:         testNamespace,
				DeletionTimestamp: nil,
			},
			expectedResult: false,
		},
		{
			description: "no deletion timestamp - resource not terminating",
			objMeta: metav1.ObjectMeta{
				Name:      testResourceName,
				Namespace: testNamespace,
			},
			expectedResult: false,
		},
		{
			description: "complex metadata with deletion timestamp and finalizers",
			objMeta: metav1.ObjectMeta{
				Name:      "test-resource",
				Namespace: testNamespace,
				Labels: map[string]string{
					"app": "test",
				},
				Annotations: map[string]string{
					"note": "test annotation",
				},
				Finalizers: []string{
					"test.finalizer/cleanup",
				},
				DeletionTimestamp:          &now,
				DeletionGracePeriodSeconds: ptr.To(int64(30)),
			},
			expectedResult: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := IsResourceTerminating(tc.objMeta)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}
