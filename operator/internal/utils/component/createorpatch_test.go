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

package component

import (
	"context"
	"testing"

	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

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
}
