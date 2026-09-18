// Copyright 2026 The Grove Authors.
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
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestCreateOrPatchSpec(t *testing.T) {
	ctx := context.Background()

	t.Run("creates the object when it does not exist", func(t *testing.T) {
		counter := &callCounter{}
		cl := fakeClientWithCounter(counter)

		cm := newConfigMap(nil)
		result, err := CreateOrPatchSpec(ctx, cl, cm, func() error {
			cm.Data = map[string]string{"key": "value"}
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, controllerutil.OperationResultCreated, result)
		assert.Equal(t, 1, counter.creates)
		assert.Equal(t, 0, counter.patches)

		fetched := &corev1.ConfigMap{}
		require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(cm), fetched))
		assert.Equal(t, "value", fetched.Data["key"])
	})

	t.Run("does not patch when the mutate function makes no change", func(t *testing.T) {
		counter := &callCounter{}
		existing := newConfigMap(map[string]string{"key": "value"})
		cl := fakeClientWithCounter(counter, existing)

		cm := newConfigMap(nil)
		result, err := CreateOrPatchSpec(ctx, cl, cm, func() error {
			// Build the same desired state that already exists on the server.
			cm.Data = map[string]string{"key": "value"}
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, controllerutil.OperationResultNone, result)
		assert.Equal(t, 0, counter.creates)
		assert.Equal(t, 0, counter.patches, "no patch must be issued when nothing changed")
	})

	t.Run("patches when the mutate function changes the object", func(t *testing.T) {
		counter := &callCounter{}
		existing := newConfigMap(map[string]string{"key": "old"})
		cl := fakeClientWithCounter(counter, existing)

		cm := newConfigMap(nil)
		result, err := CreateOrPatchSpec(ctx, cl, cm, func() error {
			cm.Data = map[string]string{"key": "new"}
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, controllerutil.OperationResultUpdated, result)
		assert.Equal(t, 1, counter.patches)

		fetched := &corev1.ConfigMap{}
		require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(cm), fetched))
		assert.Equal(t, "new", fetched.Data["key"])
	})

	t.Run("returns an error when the mutate function fails", func(t *testing.T) {
		counter := &callCounter{}
		existing := newConfigMap(map[string]string{"key": "value"})
		cl := fakeClientWithCounter(counter, existing)

		cm := newConfigMap(nil)
		_, err := CreateOrPatchSpec(ctx, cl, cm, func() error {
			return assert.AnError
		})

		require.Error(t, err)
		assert.Equal(t, 0, counter.patches)
	})

	t.Run("rejects a mutate function that changes the object name", func(t *testing.T) {
		counter := &callCounter{}
		existing := newConfigMap(map[string]string{"key": "value"})
		cl := fakeClientWithCounter(counter, existing)

		cm := newConfigMap(nil)
		_, err := CreateOrPatchSpec(ctx, cl, cm, func() error {
			cm.Name = "renamed"
			return nil
		})

		require.Error(t, err)
		assert.Equal(t, 0, counter.patches)
	})

	t.Run("creates the object as-is when the mutate function is nil", func(t *testing.T) {
		counter := &callCounter{}
		cl := fakeClientWithCounter(counter)

		cm := newConfigMap(map[string]string{"key": "value"})
		result, err := CreateOrPatchSpec(ctx, cl, cm, nil)

		require.NoError(t, err)
		assert.Equal(t, controllerutil.OperationResultCreated, result)
		assert.Equal(t, 1, counter.creates)
	})

	t.Run("does not patch an existing object when the mutate function is nil", func(t *testing.T) {
		counter := &callCounter{}
		existing := newConfigMap(map[string]string{"key": "value"})
		cl := fakeClientWithCounter(counter, existing)

		cm := newConfigMap(nil)
		result, err := CreateOrPatchSpec(ctx, cl, cm, nil)

		require.NoError(t, err)
		assert.Equal(t, controllerutil.OperationResultNone, result)
		assert.Equal(t, 0, counter.patches, "a nil mutate function cannot change the object, so no patch must be issued")
	})

	t.Run("patches the main resource but drops a status-only mutation", func(t *testing.T) {
		// CreateOrPatchSpec does not reconcile the Status subresource (see its doc comment). A
		// mutateFn that changes only Status is observed as a change and triggers a Patch against
		// the main resource, which the API server ignores for the status subresource: the caller
		// sees OperationResultUpdated but the status change is silently lost. This test pins that
		// contract so callers are not tempted to mutate Status through this helper.
		existing := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: "pclq", Namespace: "default"},
		}
		// PodClique must be registered as a status subresource for the fake client to reproduce the
		// real API server's behavior of ignoring status on a non-status Patch.
		cl := fake.NewClientBuilder().
			WithScheme(groveclientscheme.Scheme).
			WithObjects(existing.DeepCopy()).
			WithStatusSubresource(&grovecorev1alpha1.PodClique{}).
			Build()

		pclq := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: "pclq", Namespace: "default"},
		}
		result, err := CreateOrPatchSpec(ctx, cl, pclq, func() error {
			pclq.Status.Replicas = 5
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, controllerutil.OperationResultUpdated, result, "a status change is observed as a change and reported as an update")

		fetched := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(pclq), fetched))
		assert.Equal(t, int32(0), fetched.Status.Replicas, "the status-only mutation must not be persisted by the main-resource patch")
	})
}

// TestCreateOrPatchSpecMatchesControllerutil runs the same scenarios through
// CreateOrPatchSpec and controllerutil.CreateOrPatch and asserts they agree on the returned
// OperationResult, the error outcome, and the object left on the API server. This guards the
// claim that CreateOrPatchSpec is a drop-in replacement for controllerutil.CreateOrPatch on the
// scenarios it supports (create, no-op, spec change). Status subresource behavior is intentionally
// out of scope: CreateOrPatchSpec does not reconcile Status (see its doc comment).
func TestCreateOrPatchSpecMatchesControllerutil(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		existing *corev1.ConfigMap // seeded on the server before the call; nil means the object does not exist
		mutateFn func(cm *corev1.ConfigMap) controllerutil.MutateFn
	}{
		{
			name:     "creates when the object does not exist",
			existing: nil,
			mutateFn: func(cm *corev1.ConfigMap) controllerutil.MutateFn {
				return func() error {
					cm.Data = map[string]string{"key": "value"}
					return nil
				}
			},
		},
		{
			name:     "no change when the desired state equals the existing state",
			existing: newConfigMap(map[string]string{"key": "value"}),
			mutateFn: func(cm *corev1.ConfigMap) controllerutil.MutateFn {
				return func() error {
					cm.Data = map[string]string{"key": "value"}
					return nil
				}
			},
		},
		{
			name:     "updates when the mutate function changes the object",
			existing: newConfigMap(map[string]string{"key": "old"}),
			mutateFn: func(cm *corev1.ConfigMap) controllerutil.MutateFn {
				return func() error {
					cm.Data = map[string]string{"key": "new"}
					return nil
				}
			},
		},
		{
			name:     "returns an error when the mutate function fails",
			existing: newConfigMap(map[string]string{"key": "value"}),
			mutateFn: func(_ *corev1.ConfigMap) controllerutil.MutateFn {
				return func() error {
					return assert.AnError
				}
			},
		},
		{
			name:     "creates as-is when the mutate function is nil",
			existing: nil,
			mutateFn: func(_ *corev1.ConfigMap) controllerutil.MutateFn {
				return nil
			},
		},
		{
			name:     "no change on an existing object when the mutate function is nil",
			existing: newConfigMap(map[string]string{"key": "value"}),
			mutateFn: func(_ *corev1.ConfigMap) controllerutil.MutateFn {
				return nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The nil mutate function is created as-is, so both functions start from the same
			// non-empty object when tc.existing is nil and mutateFn is nil.
			newInput := func() *corev1.ConfigMap {
				if tc.existing == nil {
					return newConfigMap(map[string]string{"key": "value"})
				}
				return newConfigMap(nil)
			}

			var seed []client.Object
			if tc.existing != nil {
				seed = []client.Object{tc.existing.DeepCopy()}
			}

			specCl := fakeClientWithCounter(&callCounter{}, seed...)
			specInput := newInput()
			specResult, specErr := CreateOrPatchSpec(ctx, specCl, specInput, tc.mutateFn(specInput))

			refCl := fakeClientWithCounter(&callCounter{}, seed...)
			refInput := newInput()
			refResult, refErr := controllerutil.CreateOrPatch(ctx, refCl, refInput, tc.mutateFn(refInput))

			assert.Equal(t, refResult, specResult, "OperationResult must match controllerutil.CreateOrPatch")
			assert.Equal(t, refErr != nil, specErr != nil, "error outcome must match controllerutil.CreateOrPatch")

			specStored := &corev1.ConfigMap{}
			specGetErr := specCl.Get(ctx, client.ObjectKeyFromObject(specInput), specStored)
			refStored := &corev1.ConfigMap{}
			refGetErr := refCl.Get(ctx, client.ObjectKeyFromObject(refInput), refStored)

			assert.Equal(t, apierrors.IsNotFound(refGetErr), apierrors.IsNotFound(specGetErr), "object existence on the server must match")
			require.Equal(t, refGetErr == nil, specGetErr == nil)
			if refGetErr == nil {
				assert.Equal(t, refStored.Data, specStored.Data, "stored object data must match controllerutil.CreateOrPatch")
			}
		})
	}
}

// callCounter counts the write calls the fake client receives so tests can assert
// on real network behavior (did a Create/Patch actually fire?) rather than on mocks.
type callCounter struct {
	creates int
	patches int
}

func fakeClientWithCounter(counter *callCounter, existing ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(groveclientscheme.Scheme).
		WithObjects(existing...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				counter.creates++
				return c.Create(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				counter.patches++
				return c.Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()
}

func newConfigMap(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Data:       data,
	}
}
