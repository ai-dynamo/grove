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

package storageversion

import (
	"context"
	"fmt"
	"testing"
	"time"

	operatorcrds "github.com/ai-dynamo/grove/operator/api/core/crds"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

const (
	alpha = "v1alpha1"
	beta  = "v1beta1"

	ctbCRD = "clustertopologybindings.grove.io"
	pgmCRD = "podgangmaps.grove.io"
)

var testCRDs = []string{ctbCRD, pgmCRD}

// TestMigrateUpgradeAndDowngrade runs the upgrade and the prepare-downgrade sequences against a real API
// server, starting from CRDs that store v1alpha1 as the CRDs of v0.1.x do. Both CRDs use the None conversion
// strategy, so no conversion webhook is needed.
func TestMigrateUpgradeAndDowngrade(t *testing.T) {
	ctx := context.Background()
	cl := startEnv(t)
	previousPageSize := listPageSize
	listPageSize = 2
	t.Cleanup(func() { listPageSize = previousPageSize })

	for _, data := range []string{operatorcrds.ClusterTopologyCRD(), operatorcrds.PodGangMapCRD()} {
		installCRD(ctx, t, cl, data, alpha)
	}
	for i := range 3 {
		createObject(ctx, t, cl, clusterTopologyBinding(fmt.Sprintf("topology-%d", i)))
		createObject(ctx, t, cl, podGangMap(fmt.Sprintf("pgm-%d", i)))
	}

	// The upgrade: the CRDs of this release store v1beta1, and the operator migrates the objects.
	require.NoError(t, SetStorageVersion(ctx, cl, logr.Discard(), testCRDs, beta))
	for _, name := range testCRDs {
		assert.ElementsMatch(t, []string{alpha, beta}, storedVersions(ctx, t, cl, name))
	}
	before := resourceVersions(ctx, t, cl)
	require.NoError(t, Migrate(ctx, cl, logr.Discard(), testCRDs, beta))
	after := resourceVersions(ctx, t, cl)
	for key, rv := range before {
		assert.NotEqual(t, rv, after[key], "object %s was not rewritten", key)
	}
	for _, name := range testCRDs {
		assert.Equal(t, []string{beta}, storedVersions(ctx, t, cl, name))
	}

	// A second run finds nothing to do and writes no object.
	require.NoError(t, Migrate(ctx, cl, logr.Discard(), testCRDs, beta))
	assert.Equal(t, after, resourceVersions(ctx, t, cl))

	// v1beta1 cannot be removed while objects are stored at it.
	require.ErrorContains(t, RemoveVersion(ctx, cl, logr.Discard(), testCRDs, beta), "still has objects stored")

	// The downgrade: store v1alpha1 again, migrate, then remove v1beta1.
	require.ErrorContains(t, Migrate(ctx, cl, logr.Discard(), testCRDs, alpha), `stores "v1beta1", not "v1alpha1"`)
	require.NoError(t, SetStorageVersion(ctx, cl, logr.Discard(), testCRDs, alpha))
	require.NoError(t, Migrate(ctx, cl, logr.Discard(), testCRDs, alpha))
	require.NoError(t, RemoveVersion(ctx, cl, logr.Discard(), testCRDs, beta))
	for _, name := range testCRDs {
		crd, err := getCRD(ctx, cl, name)
		require.NoError(t, err)
		assert.Equal(t, []string{alpha}, crd.Status.StoredVersions)
		require.Len(t, crd.Spec.Versions, 1)
		assert.Equal(t, alpha, crd.Spec.Versions[0].Name)
		assert.True(t, crd.Spec.Versions[0].Storage)
	}
	assert.Len(t, resourceVersions(ctx, t, cl), 6)
}

// TestMigrateKeepsStoredVersionsWhenObjectsAreRejected checks that a rejected object leaves the earlier
// version in status.storedVersions and that the error wraps ErrIncomplete.
func TestMigrateKeepsStoredVersionsWhenObjectsAreRejected(t *testing.T) {
	ctx := context.Background()
	cl := startEnv(t)
	installCRD(ctx, t, cl, operatorcrds.ClusterTopologyCRD(), alpha)
	createObject(ctx, t, cl, clusterTopologyBinding("accepted"))
	createObject(ctx, t, cl, clusterTopologyBinding("rejected"))
	require.NoError(t, SetStorageVersion(ctx, cl, logr.Discard(), []string{ctbCRD}, beta))

	rejecting := interceptor.NewClient(cl.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if obj.GetName() == "rejected" {
				return apierrors.NewInvalid(schema.GroupKind{Group: "grove.io", Kind: "ClusterTopologyBinding"}, obj.GetName(),
					field.ErrorList{field.Forbidden(field.NewPath("spec"), "rejected")})
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	err := Migrate(ctx, rejecting, logr.Discard(), []string{ctbCRD}, beta)
	require.ErrorIs(t, err, ErrIncomplete)
	assert.ErrorContains(t, err, "rejected")
	assert.ElementsMatch(t, []string{alpha, beta}, storedVersions(ctx, t, cl, ctbCRD))

	// Once the object is accepted, a later run completes the migration.
	require.NoError(t, Migrate(ctx, cl, logr.Discard(), []string{ctbCRD}, beta))
	assert.Equal(t, []string{beta}, storedVersions(ctx, t, cl, ctbCRD))
}

func TestMigrateFailsForMissingCRD(t *testing.T) {
	ctx := context.Background()
	cl := startEnv(t)
	err := Migrate(ctx, cl, logr.Discard(), []string{ctbCRD}, beta)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrIncomplete)
}

func startEnv(t *testing.T) client.Client {
	t.Helper()
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Skipf("Skipping test: kubebuilder test environment not available: %v", err)
	}
	t.Cleanup(func() { require.NoError(t, testEnv.Stop()) })
	cl, err := client.NewWithWatch(cfg, client.Options{Scheme: runtime.NewScheme()})
	require.NoError(t, err)
	return cl
}

// installCRD creates the CRD with storage as its storage version and waits until it is established.
func installCRD(ctx context.Context, t *testing.T, cl client.Client, data, storage string) {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, yaml.Unmarshal([]byte(data), crd))
	for i := range crd.Spec.Versions {
		crd.Spec.Versions[i].Storage = crd.Spec.Versions[i].Name == storage
	}
	u, err := toUnstructured(crd)
	require.NoError(t, err)
	require.NoError(t, cl.Create(ctx, u))
	require.NoError(t, wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		current, err := getCRD(ctx, cl, crd.Name)
		if err != nil {
			return false, err
		}
		for _, c := range current.Status.Conditions {
			if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	}))
}

func createObject(ctx context.Context, t *testing.T, cl client.Client, obj *unstructured.Unstructured) {
	t.Helper()
	require.NoError(t, cl.Create(ctx, obj))
}

func clusterTopologyBinding(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"levels": []any{map[string]any{"domain": "zone", "key": "topology.kubernetes.io/zone"}},
		},
	}}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "grove.io", Version: alpha, Kind: "ClusterTopologyBinding"})
	u.SetName(name)
	return u
}

func podGangMap(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"entries": []any{}, "podCliqueSetReplicaIndex": int64(0)},
	}}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "grove.io", Version: alpha, Kind: "PodGangMap"})
	u.SetNamespace("default")
	u.SetName(name)
	return u
}

func storedVersions(ctx context.Context, t *testing.T, cl client.Client, name string) []string {
	t.Helper()
	crd, err := getCRD(ctx, cl, name)
	require.NoError(t, err)
	return crd.Status.StoredVersions
}

// resourceVersions returns the resourceVersion of every test object, keyed by kind and name.
func resourceVersions(ctx context.Context, t *testing.T, cl client.Client) map[string]string {
	t.Helper()
	versions := map[string]string{}
	for _, kind := range []string{"ClusterTopologyBinding", "PodGangMap"} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{Group: "grove.io", Version: alpha, Kind: kind + "List"})
		if err := cl.List(ctx, list); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			require.NoError(t, err)
		}
		for _, item := range list.Items {
			versions[kind+"/"+item.GetName()] = item.GetResourceVersion()
		}
	}
	return versions
}
