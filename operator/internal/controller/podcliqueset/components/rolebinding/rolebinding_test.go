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

package rolebinding

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestNew tests creating a new RoleBinding operator.
func TestNew(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	
	operator := New(cl, scheme)
	
	assert.NotNil(t, operator)
}

// TestGetExistingResourceNames tests getting existing rolebinding names.
func TestGetExistingResourceNames(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	
	// Test with no existing rolebindings
	t.Run("no existing rolebindings", func(t *testing.T) {
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
	
	// Test with existing rolebinding owned by PCS
	t.Run("existing owned rolebinding", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GeneratePodRoleBindingName("test-pcs"),
				Namespace: "default",
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
			WithObjects(rb).
			Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			UID:       pcsUID,
		}
		
		names, err := operator.GetExistingResourceNames(context.Background(), logr.Discard(), pcsObjMeta)
		
		require.NoError(t, err)
		assert.Len(t, names, 1)
		assert.Equal(t, apicommon.GeneratePodRoleBindingName("test-pcs"), names[0])
	})

	// Test with existing rolebinding not owned by this PCS
	t.Run("existing not owned rolebinding", func(t *testing.T) {
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GeneratePodRoleBindingName("test-pcs"),
				Namespace: "default",
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
			WithObjects(rb).
			Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			UID:       "pcs-uid-123",
		}
		
		names, err := operator.GetExistingResourceNames(context.Background(), logr.Discard(), pcsObjMeta)
		
		require.NoError(t, err)
		// Should not include the rolebinding owned by another PCS
		assert.Empty(t, names)
	})
}

// TestSync tests synchronizing rolebindings.
func TestSync(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	
	// Test creating new rolebinding
	t.Run("creates new rolebinding", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		operator := New(cl, scheme)
		
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs",
				Namespace: "default",
				UID:       types.UID("pcs-uid-123"),
			},
		}
		
		err := operator.Sync(context.Background(), logr.Discard(), pcs)
		
		require.NoError(t, err)
		
		// Verify rolebinding was created
		rb := &rbacv1.RoleBinding{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: apicommon.GeneratePodRoleBindingName("test-pcs"), Namespace: "default"}, rb)
		require.NoError(t, err)
		assert.Equal(t, apicommon.GeneratePodRoleBindingName("test-pcs"), rb.Name)
		// Verify labels
		assert.Equal(t, apicommon.LabelManagedByValue, rb.Labels[apicommon.LabelManagedByKey])
		assert.Equal(t, "test-pcs", rb.Labels[apicommon.LabelPartOfKey])
		// Verify RoleRef
		assert.Equal(t, "rbac.authorization.k8s.io", rb.RoleRef.APIGroup)
		assert.Equal(t, "Role", rb.RoleRef.Kind)
		assert.Equal(t, apicommon.GeneratePodRoleName("test-pcs"), rb.RoleRef.Name)
		// Verify Subjects
		require.Len(t, rb.Subjects, 1)
		assert.Equal(t, "ServiceAccount", rb.Subjects[0].Kind)
		assert.Equal(t, apicommon.GeneratePodServiceAccountName("test-pcs"), rb.Subjects[0].Name)
		assert.Equal(t, "default", rb.Subjects[0].Namespace)
	})

	// Test when rolebinding already exists with correct owner
	t.Run("skips when rolebinding exists", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GeneratePodRoleBindingName("test-pcs"),
				Namespace: "default",
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
			WithObjects(rb).
			Build()
		operator := New(cl, scheme)
		
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs",
				Namespace: "default",
				UID:       pcsUID,
			},
		}
		
		err := operator.Sync(context.Background(), logr.Discard(), pcs)
		
		// Should not error when rolebinding already exists
		require.NoError(t, err)
	})
}

// TestDelete tests deleting rolebindings.
func TestDelete(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	
	// Test deleting existing rolebinding
	t.Run("deletes existing rolebinding", func(t *testing.T) {
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GeneratePodRoleBindingName("test-pcs"),
				Namespace: "default",
			},
		}
		
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(rb).
			Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		}
		
		err := operator.Delete(context.Background(), logr.Discard(), pcsObjMeta)
		
		require.NoError(t, err)
		
		// Verify rolebinding was deleted
		rb = &rbacv1.RoleBinding{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: apicommon.GeneratePodRoleBindingName("test-pcs"), Namespace: "default"}, rb)
		assert.Error(t, err)
		assert.True(t, client.IgnoreNotFound(err) == nil)
	})

	// Test deleting non-existent rolebinding
	t.Run("handles non-existent rolebinding", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		}
		
		err := operator.Delete(context.Background(), logr.Discard(), pcsObjMeta)
		
		// Should not error when rolebinding doesn't exist
		require.NoError(t, err)
	})
}
