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

package crdinstaller_test

import (
	"context"
	"testing"

	operatorcrds "github.com/ai-dynamo/grove/operator/api/core/v1alpha1/crds"
	"github.com/ai-dynamo/grove/operator/internal/crdinstaller"

	schedulercrds "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1/crds"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// buildFakeClient creates a fake client with apiextensionsv1 scheme registered.
func buildFakeClient() client.Client {
	scheme := runtime.NewScheme()
	_ = apiextensionsv1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

// minimalCRDYAML returns a minimal valid CRD yaml for testing.
func minimalCRDYAML(name, group, plural, kind string) string {
	singular := plural[:len(plural)-1]
	return `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: ` + name + `
spec:
  group: ` + group + `
  names:
    kind: ` + kind + `
    listKind: ` + kind + `List
    plural: ` + plural + `
    singular: ` + singular + `
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`
}

// TestInstallCRDs_AllApplied verifies that InstallCRDs applies all 5 Grove CRDs
// (operator and scheduler) in a single call with no errors.
//
// Flow:
//  1. Call InstallCRDs against a fresh fake client.
//  2. For each of the 5 expected CRD names, fetch the object from the API.
//  3. Assert every fetch succeeds, confirming all CRDs were created.
func TestInstallCRDs_AllApplied(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()

	allCRDs := []string{
		operatorcrds.PodCliqueCRD(),
		operatorcrds.PodCliqueSetCRD(),
		operatorcrds.PodCliqueScalingGroupCRD(),
		operatorcrds.ClusterTopologyCRD(),
		schedulercrds.PodGangCRD(),
	}
	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), allCRDs)
	require.NoError(t, err)

	// Verify all 5 CRDs exist by name.
	for _, crdName := range []string{
		"podcliques.grove.io",
		"podcliquesets.grove.io",
		"podcliquescalinggroups.grove.io",
		"clustertopologies.grove.io",
		"podgangs.scheduler.grove.io",
	} {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
		err := cl.Get(ctx, client.ObjectKey{Name: crdName}, obj)
		assert.NoError(t, err, "CRD %q should exist after InstallCRDs", crdName)
	}
}

// TestInstallCRDs_CreatesByName verifies that InstallCRDs creates the CRD with
// the exact metadata.name present in the provided YAML.
//
// Flow:
//  1. Call InstallCRDs with a single minimal CRD YAML.
//  2. Fetch the object by its expected name from the fake client.
//  3. Assert the fetch succeeds, confirming the name was correctly parsed and applied.
func TestInstallCRDs_CreatesByName(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()

	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(),
		[]string{minimalCRDYAML("testthings.test.io", "test.io", "testthings", "TestThing")},
	)
	require.NoError(t, err)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	err = cl.Get(ctx, client.ObjectKey{Name: "testthings.test.io"}, obj)
	assert.NoError(t, err, "CRD should exist under the name declared in the YAML")
}

// TestInstallCRDs_Idempotent verifies that calling InstallCRDs twice with the
// same CRD YAML does not produce an error. This is important because the installer
// runs on every operator startup and must be safe to re-run against an
// already-current cluster.
//
// Flow:
//  1. Call InstallCRDs with a single CRD YAML — first call creates it.
//  2. Call InstallCRDs again with the identical YAML — second call is a no-op server-side apply.
//  3. Assert neither call returns an error.
func TestInstallCRDs_Idempotent(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()
	crds := []string{minimalCRDYAML("testthings.test.io", "test.io", "testthings", "TestThing")}

	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), crds)
	require.NoError(t, err)

	err = crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), crds)
	require.NoError(t, err)
}

// TestInstallCRDs_UpdatesExistingContent verifies that calling InstallCRDs with
// changed CRD content actually mutates the existing object in the cluster, not
// just leaves it unchanged because it already exists. This covers the upgrade
// path where a new operator version ships updated CRD schemas.
//
// Flow:
//  1. Call InstallCRDs with a CRD YAML containing label test-version="1".
//  2. Fetch the object and confirm the label is "1".
//  3. Call InstallCRDs again with the same CRD name but label test-version="2".
//  4. Fetch the object again and assert the label was updated to "2".
func TestInstallCRDs_UpdatesExistingContent(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()

	const crdName = "testthings.test.io"

	crdYAML := func(labelValue string) string {
		return `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: ` + crdName + `
  labels:
    test-version: "` + labelValue + `"
spec:
  group: test.io
  names:
    kind: TestThing
    listKind: TestThingList
    plural: testthings
    singular: testthing
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`
	}

	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{crdYAML("1")})
	require.NoError(t, err)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: crdName}, obj))
	assert.Equal(t, "1", obj.GetLabels()["test-version"], "initial label should be 1")

	err = crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{crdYAML("2")})
	require.NoError(t, err)

	obj2 := &unstructured.Unstructured{}
	obj2.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: crdName}, obj2))
	assert.Equal(t, "2", obj2.GetLabels()["test-version"], "label should be updated to 2 after second apply")
}

// TestInstallCRDs_ReturnsErrorOnInvalidYAML verifies that InstallCRDs fails fast
// and returns an error when given YAML that cannot be parsed, rather than
// silently sending garbage to the API server.
//
// Flow:
//  1. Call InstallCRDs with a syntactically invalid YAML string.
//  2. Assert an error is returned.
func TestInstallCRDs_ReturnsErrorOnInvalidYAML(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()

	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{"not: valid: yaml: [[["})
	assert.Error(t, err)
}
