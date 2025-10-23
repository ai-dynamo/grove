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

package satokensecret

import (
	"context"
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

// TestNew tests creating a new Secret operator.
func TestNew(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	
	operator := New(cl, scheme)
	
	assert.NotNil(t, operator)
}

// TestGetExistingResourceNames tests getting existing secret names.
func TestGetExistingResourceNames(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	
	// Test with no existing secrets
	t.Run("no existing secrets", func(t *testing.T) {
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
	
	// Test with existing secret owned by PCS
	t.Run("existing owned secret", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GenerateInitContainerSATokenSecretName("test-pcs"),
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
			WithObjects(secret).
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
		assert.Equal(t, apicommon.GenerateInitContainerSATokenSecretName("test-pcs"), names[0])
	})

	// Test with existing secret not owned by this PCS
	t.Run("existing not owned secret", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GenerateInitContainerSATokenSecretName("test-pcs"),
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
			WithObjects(secret).
			Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			UID:       "pcs-uid-123",
		}
		
		names, err := operator.GetExistingResourceNames(context.Background(), logr.Discard(), pcsObjMeta)
		
		require.NoError(t, err)
		// Should not include the secret owned by another PCS
		assert.Empty(t, names)
	})
}

// TestSync tests synchronizing secrets.
func TestSync(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	
	// Test creating new secret
	t.Run("creates new secret", func(t *testing.T) {
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
		
		// Verify secret was created
		secret := &corev1.Secret{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: apicommon.GenerateInitContainerSATokenSecretName("test-pcs"), Namespace: "default"}, secret)
		require.NoError(t, err)
		assert.Equal(t, apicommon.GenerateInitContainerSATokenSecretName("test-pcs"), secret.Name)
		// Verify labels
		assert.Equal(t, apicommon.LabelManagedByValue, secret.Labels[apicommon.LabelManagedByKey])
		assert.Equal(t, "test-pcs", secret.Labels[apicommon.LabelPartOfKey])
		// Verify type and annotations
		assert.Equal(t, corev1.SecretTypeServiceAccountToken, secret.Type)
		assert.Equal(t, apicommon.GeneratePodServiceAccountName("test-pcs"), secret.Annotations[corev1.ServiceAccountNameKey])
	})

	// Test when secret already exists with correct owner
	t.Run("skips when secret exists", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GenerateInitContainerSATokenSecretName("test-pcs"),
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
			WithObjects(secret).
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
		
		// Should not error when secret already exists
		require.NoError(t, err)
	})
}

// TestDelete tests deleting secrets.
func TestDelete(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	
	// Test deleting existing secret
	t.Run("deletes existing secret", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GenerateInitContainerSATokenSecretName("test-pcs"),
				Namespace: "default",
			},
		}
		
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret).
			Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		}
		
		err := operator.Delete(context.Background(), logr.Discard(), pcsObjMeta)
		
		require.NoError(t, err)
		
		// Verify secret was deleted
		secret = &corev1.Secret{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: apicommon.GenerateInitContainerSATokenSecretName("test-pcs"), Namespace: "default"}, secret)
		assert.Error(t, err)
		assert.True(t, client.IgnoreNotFound(err) == nil)
	})

	// Test deleting non-existent secret
	t.Run("handles non-existent secret", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		operator := New(cl, scheme)
		
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		}
		
		err := operator.Delete(context.Background(), logr.Discard(), pcsObjMeta)
		
		// Should not error when secret doesn't exist
		require.NoError(t, err)
	})
}
