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

package role

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestNew tests creating a new Role operator.
func TestNew(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	operator := New(cl, scheme)

	assert.NotNil(t, operator)
}

// TestGetExistingResourceNames tests getting existing role names.
func TestGetExistingResourceNames(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	// Test with no existing roles
	t.Run("no existing roles", func(t *testing.T) {
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

	// Test with existing role owned by PCS
	t.Run("existing owned role", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GeneratePodRoleName("test-pcs"),
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
			WithObjects(role).
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
		assert.Equal(t, apicommon.GeneratePodRoleName("test-pcs"), names[0])
	})

	// Test with existing role not owned by this PCS
	t.Run("existing not owned role", func(t *testing.T) {
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GeneratePodRoleName("test-pcs"),
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
			WithObjects(role).
			Build()
		operator := New(cl, scheme)

		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			UID:       "pcs-uid-123",
		}

		names, err := operator.GetExistingResourceNames(context.Background(), logr.Discard(), pcsObjMeta)

		require.NoError(t, err)
		// Should not include the role owned by another PCS
		assert.Empty(t, names)
	})
}

// TestSync tests synchronizing roles.
func TestSync(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	// Test creating new role
	t.Run("creates new role", func(t *testing.T) {
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

		// Verify role was created
		role := &rbacv1.Role{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: apicommon.GeneratePodRoleName("test-pcs"), Namespace: "default"}, role)
		require.NoError(t, err)
		assert.Equal(t, apicommon.GeneratePodRoleName("test-pcs"), role.Name)
		// Verify labels
		assert.Equal(t, apicommon.LabelManagedByValue, role.Labels[apicommon.LabelManagedByKey])
		assert.Equal(t, "test-pcs", role.Labels[apicommon.LabelPartOfKey])
		// Verify rules
		require.Len(t, role.Rules, 1)
		assert.Equal(t, []string{""}, role.Rules[0].APIGroups)
		assert.Equal(t, []string{"pods", "pods/status"}, role.Rules[0].Resources)
		assert.Equal(t, []string{"get", "list", "watch"}, role.Rules[0].Verbs)
	})

	// Test when role already exists with correct owner
	t.Run("skips when role exists", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GeneratePodRoleName("test-pcs"),
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
			WithObjects(role).
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

		// Should not error when role already exists
		require.NoError(t, err)
	})
}

// TestDelete tests deleting roles.
func TestDelete(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	// Test deleting existing role
	t.Run("deletes existing role", func(t *testing.T) {
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GeneratePodRoleName("test-pcs"),
				Namespace: "default",
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(role).
			Build()
		operator := New(cl, scheme)

		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		}

		err := operator.Delete(context.Background(), logr.Discard(), pcsObjMeta)

		require.NoError(t, err)

		// Verify role was deleted
		role = &rbacv1.Role{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: apicommon.GeneratePodRoleName("test-pcs"), Namespace: "default"}, role)
		assert.Error(t, err)
		assert.True(t, client.IgnoreNotFound(err) == nil)
	})

	// Test deleting non-existent role
	t.Run("handles non-existent role", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		operator := New(cl, scheme)

		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		}

		err := operator.Delete(context.Background(), logr.Discard(), pcsObjMeta)

		// Should not error when role doesn't exist
		require.NoError(t, err)
	})
}
