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

package service

import (
	"context"
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestNew tests creating a new Service operator.
func TestNew(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	
	operator := New(cl, scheme)
	
	assert.NotNil(t, operator)
}

// TestGetExistingResourceNames tests getting existing service names.
func TestGetExistingResourceNames(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	
	// Test with no existing services
	t.Run("no existing services", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			UID:       "pcs-uid-123",
		}
		
		names, err := operator.GetExistingResourceNames(context.Background(), logr.Discard(), pcsObjMeta)
		
		require.NoError(t, err)
		assert.Empty(t, names)
	})
	
	// Test with existing services owned by PCS
	t.Run("existing owned services", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		svc1 := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelManagedByKey:                          apicommon.LabelManagedByValue,
					apicommon.LabelPartOfKey:                             "test-pcs",
					apicommon.LabelComponentKey:                          apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService,
					apicommon.LabelPodCliqueSetReplicaIndex:              "0",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "grove.ai-dynamo.io/v1alpha1",
						Kind:       "PodCliqueSet",
						Name:       "test-pcs",
						UID:        pcsUID,
						Controller: ptr.To(true),
					},
				},
			},
		}
		svc2 := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-1",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelManagedByKey:                          apicommon.LabelManagedByValue,
					apicommon.LabelPartOfKey:                             "test-pcs",
					apicommon.LabelComponentKey:                          apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService,
					apicommon.LabelPodCliqueSetReplicaIndex:              "1",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "grove.ai-dynamo.io/v1alpha1",
						Kind:       "PodCliqueSet",
						Name:       "test-pcs",
						UID:        pcsUID,
						Controller: ptr.To(true),
					},
				},
			},
		}
		
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(svc1, svc2).
			Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			UID:       pcsUID,
		}
		
		names, err := operator.GetExistingResourceNames(context.Background(), logr.Discard(), pcsObjMeta)
		
		require.NoError(t, err)
		assert.Len(t, names, 2)
		assert.Contains(t, names, "test-pcs-0")
		assert.Contains(t, names, "test-pcs-1")
	})

	// Test with existing service not owned by this PCS
	t.Run("existing not owned services", func(t *testing.T) {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelManagedByKey:                          apicommon.LabelManagedByValue,
					apicommon.LabelPartOfKey:                             "test-pcs",
					apicommon.LabelComponentKey:                          apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService,
					apicommon.LabelPodCliqueSetReplicaIndex:              "0",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "grove.ai-dynamo.io/v1alpha1",
						Kind:       "PodCliqueSet",
						Name:       "other-pcs",
						UID:        types.UID("other-uid"),
						Controller: ptr.To(true),
					},
				},
			},
		}
		
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(svc).
			Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			UID:       "pcs-uid-123",
		}
		
		names, err := operator.GetExistingResourceNames(context.Background(), logr.Discard(), pcsObjMeta)
		
		require.NoError(t, err)
		// Should not include the service owned by another PCS
		assert.Empty(t, names)
	})
}

// TestSync tests synchronizing services.
func TestSync(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	
	// Test creating new services
	t.Run("creates new services", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		operator := New(cl, scheme)
		
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs",
				Namespace: "default",
				UID:       types.UID("pcs-uid-123"),
			},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 2,
			},
		}
		
		err := operator.Sync(context.Background(), logr.Discard(), pcs)
		
		require.NoError(t, err)
		
		// Verify services were created
		for i := 0; i < 2; i++ {
			svc := &corev1.Service{}
			serviceName := fmt.Sprintf("test-pcs-%d", i)
			err = cl.Get(context.Background(), client.ObjectKey{Name: serviceName, Namespace: "default"}, svc)
			require.NoError(t, err)
			assert.Equal(t, serviceName, svc.Name)
			// Verify labels
			assert.Equal(t, apicommon.LabelManagedByValue, svc.Labels[apicommon.LabelManagedByKey])
			assert.Equal(t, "test-pcs", svc.Labels[apicommon.LabelPartOfKey])
			assert.Equal(t, fmt.Sprintf("%d", i), svc.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
			// Verify service spec
			assert.Equal(t, "None", svc.Spec.ClusterIP)
			assert.False(t, svc.Spec.PublishNotReadyAddresses)
			// Verify selector
			assert.Equal(t, "test-pcs", svc.Spec.Selector[apicommon.LabelPartOfKey])
			assert.Equal(t, fmt.Sprintf("%d", i), svc.Spec.Selector[apicommon.LabelPodCliqueSetReplicaIndex])
		}
	})

	// Test creating services with PublishNotReadyAddresses
	t.Run("creates services with PublishNotReadyAddresses", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		operator := New(cl, scheme)
		
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs",
				Namespace: "default",
				UID:       types.UID("pcs-uid-123"),
			},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 1,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					HeadlessServiceConfig: &grovecorev1alpha1.HeadlessServiceConfig{
						PublishNotReadyAddresses: true,
					},
				},
			},
		}
		
		err := operator.Sync(context.Background(), logr.Discard(), pcs)
		
		require.NoError(t, err)
		
		// Verify service was created with PublishNotReadyAddresses
		svc := &corev1.Service{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: "test-pcs-0", Namespace: "default"}, svc)
		require.NoError(t, err)
		assert.True(t, svc.Spec.PublishNotReadyAddresses)
	})

	// Test updating existing service
	t.Run("updates existing service", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
					apicommon.LabelPartOfKey:    "test-pcs",
				},
			},
			Spec: corev1.ServiceSpec{
				ClusterIP: "None",
				PublishNotReadyAddresses: false,
			},
		}
		
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(svc).
			Build()
		operator := New(cl, scheme)
		
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs",
				Namespace: "default",
				UID:       pcsUID,
			},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 1,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					HeadlessServiceConfig: &grovecorev1alpha1.HeadlessServiceConfig{
						PublishNotReadyAddresses: true,
					},
				},
			},
		}
		
		err := operator.Sync(context.Background(), logr.Discard(), pcs)
		
		require.NoError(t, err)
		
		// Verify service was updated
		updatedSvc := &corev1.Service{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: "test-pcs-0", Namespace: "default"}, updatedSvc)
		require.NoError(t, err)
		assert.True(t, updatedSvc.Spec.PublishNotReadyAddresses)
	})
}

// TestDelete tests deleting services.
func TestDelete(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	
	// Test deleting existing services
	t.Run("deletes existing services", func(t *testing.T) {
		svc1 := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelManagedByKey:                          apicommon.LabelManagedByValue,
					apicommon.LabelPartOfKey:                             "test-pcs",
					apicommon.LabelComponentKey:                          apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService,
				},
			},
		}
		svc2 := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-1",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelManagedByKey:                          apicommon.LabelManagedByValue,
					apicommon.LabelPartOfKey:                             "test-pcs",
					apicommon.LabelComponentKey:                          apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService,
				},
			},
		}
		
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(svc1, svc2).
			Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		}
		
		err := operator.Delete(context.Background(), logr.Discard(), pcsObjMeta)
		
		require.NoError(t, err)
		
		// Verify services were deleted
		svcList := &corev1.ServiceList{}
		err = cl.List(context.Background(), svcList, client.InNamespace("default"))
		require.NoError(t, err)
		assert.Empty(t, svcList.Items)
	})

	// Test deleting non-existent services
	t.Run("handles non-existent services", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		}
		
		err := operator.Delete(context.Background(), logr.Discard(), pcsObjMeta)
		
		// Should not error when services don't exist
		require.NoError(t, err)
	})
}
