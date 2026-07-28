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

package satokensecret

import (
	"context"
	"errors"
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
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

	t.Run("existing legacy owned secret", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GenerateLegacyInitContainerSATokenSecretName("test-pcs"),
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
		assert.Equal(t, apicommon.GenerateLegacyInitContainerSATokenSecretName("test-pcs"), names[0])
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

		assertRequeueAfter(t, err)

		// Verify secret was created
		secret := &corev1.Secret{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: apicommon.GenerateInitContainerSATokenSecretName("test-pcs"), Namespace: "default"}, secret)
		require.NoError(t, err)
		assert.Equal(t, apicommon.GenerateInitContainerSATokenSecretName("test-pcs"), secret.Name)
		// Verify labels
		assert.Equal(t, apicommon.LabelManagedByValue, secret.Labels[apicommon.LabelManagedByKey])
		assert.Equal(t, "test-pcs", secret.Labels[apicommon.LabelPartOfKey])
		assert.Equal(t, apicommon.GenerateInitContainerSATokenSecretName("test-pcs"), secret.Labels[apicommon.LabelAppNameKey])
		// Verify type and annotations
		assert.Equal(t, corev1.SecretTypeServiceAccountToken, secret.Type)
		assert.Equal(t, apicommon.GeneratePodServiceAccountName("test-pcs"), secret.Annotations[corev1.ServiceAccountNameKey])
	})

	t.Run("creates secret with valid labels for long admitted pcs name", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		operator := New(cl, scheme)
		pcsName := strings.Repeat("a", 44)

		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pcsName,
				Namespace: "default",
				UID:       types.UID("pcs-uid-123"),
			},
		}

		err := operator.Sync(context.Background(), logr.Discard(), pcs)

		assertRequeueAfter(t, err)

		secret := &corev1.Secret{}
		secretName := apicommon.GenerateInitContainerSATokenSecretName(pcsName)
		err = cl.Get(context.Background(), client.ObjectKey{Name: secretName, Namespace: "default"}, secret)
		require.NoError(t, err)
		require.Len(t, pcsName, 44)
		require.LessOrEqual(t, len(secretName), 63)
		assert.Equal(t, pcsName, secret.Labels[apicommon.LabelPartOfKey])
		assert.Equal(t, secretName, secret.Labels[apicommon.LabelAppNameKey])
		for _, value := range secret.Labels {
			assert.Empty(t, k8svalidation.IsValidLabelValue(value))
		}
	})

	t.Run("does not copy owned legacy secret data when creating new secret", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		legacySecretData := map[string][]byte{
			corev1.ServiceAccountTokenKey:  []byte("legacy-token"),
			corev1.ServiceAccountRootCAKey: []byte("legacy-ca"),
			"migration-marker":             []byte("migrated"),
		}
		legacySecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GenerateLegacyInitContainerSATokenSecretName("test-pcs"),
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
			Data: legacySecretData,
		}
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(legacySecret).
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

		assertRequeueAfter(t, err)

		secret := &corev1.Secret{}
		secretName := apicommon.GenerateInitContainerSATokenSecretName("test-pcs")
		err = cl.Get(context.Background(), client.ObjectKey{Name: secretName, Namespace: "default"}, secret)
		require.NoError(t, err)
		assert.Empty(t, secret.Data)
		assert.Equal(t, secretName, secret.Labels[apicommon.LabelAppNameKey])

		remainingLegacySecret := &corev1.Secret{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: apicommon.GenerateLegacyInitContainerSATokenSecretName("test-pcs"), Namespace: "default"}, remainingLegacySecret)
		require.NoError(t, err)
	})

	t.Run("does not copy legacy secret data without PCS ownership", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		legacySecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GenerateLegacyInitContainerSATokenSecretName("test-pcs"),
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
			Data: map[string][]byte{
				"migration-marker": []byte("do-not-copy"),
			},
		}
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(legacySecret).
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

		assertRequeueAfter(t, err)

		secret := &corev1.Secret{}
		secretName := apicommon.GenerateInitContainerSATokenSecretName("test-pcs")
		err = cl.Get(context.Background(), client.ObjectKey{Name: secretName, Namespace: "default"}, secret)
		require.NoError(t, err)
		assert.Empty(t, secret.Data)
		assert.Equal(t, secretName, secret.Labels[apicommon.LabelAppNameKey])
	})

	t.Run("errors when new secret already exists without PCS ownership", func(t *testing.T) {
		pcsUID := types.UID("pcs-uid-123")
		secretName := apicommon.GenerateInitContainerSATokenSecretName("test-pcs")
		secretData := map[string][]byte{
			"token": []byte("wrong-token"),
		}
		existingSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: "default",
			},
			Data: secretData,
		}
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingSecret).
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

		require.Error(t, err)
		var groveErr *groveerr.GroveError
		require.True(t, errors.As(err, &groveErr))
		assert.Equal(t, errCodeSecretConflict, groveErr.Code)
		assert.Contains(t, err.Error(), "already exists but is not controlled")
		assert.Contains(t, err.Error(), secretName)

		secret := &corev1.Secret{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: secretName, Namespace: "default"}, secret)
		require.NoError(t, err)
		assert.Equal(t, secretData, secret.Data)
		assert.Empty(t, secret.OwnerReferences)
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
			Data: map[string][]byte{
				corev1.ServiceAccountTokenKey: []byte("ready-token"),
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

	for _, tc := range []struct {
		name                       string
		tokenReady                 bool
		referenceLegacy            bool
		expectRequeue              bool
		expectLegacySecret         bool
		retryAfterReferenceRemoval bool
	}{
		{
			name:       "deletes owned legacy secret after new secret has token",
			tokenReady: true,
		},
		{
			name:               "keeps owned legacy secret while new secret waits for token",
			expectRequeue:      true,
			expectLegacySecret: true,
		},
		{
			name:                       "keeps owned legacy secret without requeue while pods still reference it",
			tokenReady:                 true,
			referenceLegacy:            true,
			expectLegacySecret:         true,
			retryAfterReferenceRemoval: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("pcs-uid-123"),
				},
			}
			secret := newOwnedSecret(apicommon.GenerateInitContainerSATokenSecretName(pcs.Name), pcs)
			if tc.tokenReady {
				secret.Data = map[string][]byte{
					corev1.ServiceAccountTokenKey: []byte("ready-token"),
				}
			}
			legacySecret := newOwnedSecret(apicommon.GenerateLegacyInitContainerSATokenSecretName(pcs.Name), pcs)

			objects := []client.Object{secret, legacySecret}
			var referencingPod *corev1.Pod
			if tc.referenceLegacy {
				referencingPod = newPodReferencingSecret("test-pod", pcs, legacySecret.Name)
				objects = append(objects, referencingPod)
			}
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()
			operator := New(cl, scheme)

			err := operator.Sync(context.Background(), logr.Discard(), pcs)

			if tc.expectRequeue {
				assertRequeueAfter(t, err)
			} else {
				require.NoError(t, err)
			}

			remainingSecret := &corev1.Secret{}
			err = cl.Get(context.Background(), client.ObjectKeyFromObject(secret), remainingSecret)
			require.NoError(t, err)

			remainingLegacySecret := &corev1.Secret{}
			err = cl.Get(context.Background(), client.ObjectKeyFromObject(legacySecret), remainingLegacySecret)
			if tc.expectLegacySecret {
				require.NoError(t, err)
			} else {
				assert.True(t, apierrors.IsNotFound(err))
			}

			if tc.retryAfterReferenceRemoval {
				require.NoError(t, cl.Delete(context.Background(), referencingPod))
				require.NoError(t, operator.Sync(context.Background(), logr.Discard(), pcs))

				err = cl.Get(context.Background(), client.ObjectKeyFromObject(legacySecret), &corev1.Secret{})
				assert.True(t, apierrors.IsNotFound(err))
			}
		})
	}
}

func assertRequeueAfter(t *testing.T, err error) {
	t.Helper()

	var groveErr *groveerr.GroveError
	require.True(t, errors.As(err, &groveErr))
	assert.Equal(t, groveerr.ErrCodeRequeueAfter, groveErr.Code)
}

func newOwnedSecret(name string, pcs *grovecorev1alpha1.PodCliqueSet) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pcs.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
					Kind:       "PodCliqueSet",
					Name:       pcs.Name,
					UID:        pcs.UID,
					Controller: ptr.To(true),
				},
			},
		},
	}
}

func newPodReferencingSecret(name string, pcs *grovecorev1alpha1.PodCliqueSet, secretName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pcs.Namespace,
			Labels:    apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{
					Name: "sa-token-secret-vol",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: secretName,
						},
					},
				},
			},
		},
	}
}

// TestDelete tests deleting secrets.
func TestDelete(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Test deleting existing secrets
	t.Run("deletes existing secrets", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GenerateInitContainerSATokenSecretName("test-pcs"),
				Namespace: "default",
			},
		}
		legacySecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GenerateLegacyInitContainerSATokenSecretName("test-pcs"),
				Namespace: "default",
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret, legacySecret).
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

		legacySecret = &corev1.Secret{}
		err = cl.Get(context.Background(), client.ObjectKey{Name: apicommon.GenerateLegacyInitContainerSATokenSecretName("test-pcs"), Namespace: "default"}, legacySecret)
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
