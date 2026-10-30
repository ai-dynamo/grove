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

package crdinstaller_test

import (
	"context"
	"testing"

	operatorcrds "github.com/ai-dynamo/grove/operator/api/core/crds"
	"github.com/ai-dynamo/grove/operator/internal/crdinstaller"

	schedulercrds "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1/crds"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

const (
	testNamespace = "grove-test"
	// testServiceName differs from the name in the generated CRDs, so that the tests catch a name that is not set.
	testServiceName = "my-grove"
	testServicePort = 8443
)

var testConversion = crdinstaller.ConversionWebhookConfig{
	ServiceName:      testServiceName,
	ServiceNamespace: testNamespace,
	ServicePort:      testServicePort,
}

// buildFakeClient creates a fake client with apiextensionsv1 scheme registered.
func buildFakeClient(objects ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	_ = apiextensionsv1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
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

// TestInstallCRDs_AllApplied verifies that InstallCRDs applies all 6 Grove CRDs
// (operator and scheduler) in a single call with no errors.
//
// Flow:
//  1. Call InstallCRDs against a fresh fake client.
//  2. For each of the 6 expected CRD names, fetch the object from the API.
//  3. Assert every fetch succeeds, confirming all CRDs were created.
func TestInstallCRDs_AllApplied(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()

	allCRDs := []string{
		operatorcrds.PodCliqueCRD(),
		operatorcrds.PodCliqueSetCRD(),
		operatorcrds.PodCliqueScalingGroupCRD(),
		operatorcrds.ClusterTopologyCRD(),
		operatorcrds.PodGangMapCRD(),
		schedulercrds.PodGangCRD(),
	}
	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), allCRDs, testConversion)
	require.NoError(t, err)

	// Verify all 6 CRDs exist and only PodCliqueSet uses webhook conversion.
	for _, crdName := range []string{
		"podcliques.grove.io",
		"podcliquesets.grove.io",
		"podcliquescalinggroups.grove.io",
		"clustertopologybindings.grove.io",
		"podgangmaps.grove.io",
		"podgangs.scheduler.grove.io",
	} {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		err := cl.Get(ctx, client.ObjectKey{Name: crdName}, crd)
		assert.NoError(t, err, "CRD %q should exist after InstallCRDs", crdName)
		if crdName == "podcliquesets.grove.io" {
			require.NotNil(t, crd.Spec.Conversion)
			assert.Equal(t, apiextensionsv1.WebhookConverter, crd.Spec.Conversion.Strategy)
			continue
		}
		assert.True(t, crd.Spec.Conversion == nil || crd.Spec.Conversion.Strategy == apiextensionsv1.NoneConverter, crdName)
	}
}

// TestInstallCRDs_ConversionWebhook verifies that converted CRDs use the configured Service and CA.
func TestInstallCRDs_ConversionWebhook(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()

	crds := map[string]string{
		"podcliquesets.grove.io": operatorcrds.PodCliqueSetCRD(),
	}
	for name, crdYAML := range crds {
		require.NoError(t, crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{crdYAML}, testConversion))

		crd := &apiextensionsv1.CustomResourceDefinition{}
		require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: name}, crd))
		require.NotNil(t, crd.Spec.Conversion, name)
		assert.Equal(t, apiextensionsv1.WebhookConverter, crd.Spec.Conversion.Strategy, name)
		require.NotNil(t, crd.Spec.Conversion.Webhook, name)
		require.NotNil(t, crd.Spec.Conversion.Webhook.ClientConfig, name)
		service := crd.Spec.Conversion.Webhook.ClientConfig.Service
		require.NotNil(t, service, name)
		assert.Equal(t, testServiceName, service.Name, name)
		assert.Equal(t, testNamespace, service.Namespace, name)
		assert.Equal(t, "/convert", *service.Path, name)
		assert.EqualValues(t, testServicePort, *service.Port, name)
		assert.Empty(t, crd.Spec.Conversion.Webhook.ClientConfig.CABundle, name)

		withCA := testConversion
		withCA.CABundle = []byte("manual-ca")
		require.NoError(t, crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{crdYAML}, withCA))
		require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: name}, crd))
		assert.Equal(t, []byte("manual-ca"), crd.Spec.Conversion.Webhook.ClientConfig.CABundle, name)
	}

	invalid := testConversion
	invalid.ServiceNamespace = ""
	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{operatorcrds.PodCliqueSetCRD()}, invalid)
	assert.Error(t, err, "a Service-addressed conversion webhook requires a Service namespace")
	invalid = testConversion
	invalid.ServicePort = 0
	err = crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{operatorcrds.PodCliqueSetCRD()}, invalid)
	assert.Error(t, err, "a Service-addressed conversion webhook requires a valid Service port")
}

func TestConfigureConversionWebhook_OnlyUpdatesConversion(t *testing.T) {
	pcsCRD := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, yaml.Unmarshal([]byte(operatorcrds.PodCliqueSetCRD()), pcsCRD))
	pcsCRD.Labels = map[string]string{"preserve": "true"}
	pcsCRD.Spec.Conversion.Webhook.ClientConfig.CABundle = []byte("injected-ca")
	for _, version := range pcsCRD.Spec.Versions {
		if version.Name == "v1alpha1" {
			version.Storage = true
			pcsCRD.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{version}
			break
		}
	}
	require.Len(t, pcsCRD.Spec.Versions, 1)

	cl := buildFakeClient(pcsCRD)
	err := crdinstaller.ConfigureConversionWebhook(context.Background(), cl, logr.Discard(),
		operatorcrds.PodCliqueSetCRD(), testConversion)
	require.NoError(t, err)

	configured := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: pcsCRD.Name}, configured))
	assert.Equal(t, map[string]string{"preserve": "true"}, configured.Labels)
	require.Len(t, configured.Spec.Versions, 1)
	assert.Equal(t, "v1alpha1", configured.Spec.Versions[0].Name)
	require.NotNil(t, configured.Spec.Conversion)
	require.NotNil(t, configured.Spec.Conversion.Webhook)
	service := configured.Spec.Conversion.Webhook.ClientConfig.Service
	require.NotNil(t, service)
	assert.Equal(t, testServiceName, service.Name)
	assert.Equal(t, testNamespace, service.Namespace)
	assert.EqualValues(t, testServicePort, *service.Port)
	assert.Equal(t, []byte("injected-ca"), configured.Spec.Conversion.Webhook.ClientConfig.CABundle)

	manual := testConversion
	manual.CABundle = []byte("manual-ca")
	require.NoError(t, crdinstaller.ConfigureConversionWebhook(context.Background(), cl, logr.Discard(),
		operatorcrds.PodCliqueSetCRD(), manual))
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: pcsCRD.Name}, configured))
	assert.Equal(t, []byte("manual-ca"), configured.Spec.Conversion.Webhook.ClientConfig.CABundle)
}

func TestConfigureConversionWebhook_DoesNotCreateCRD(t *testing.T) {
	cl := buildFakeClient()
	err := crdinstaller.ConfigureConversionWebhook(context.Background(), cl, logr.Discard(),
		operatorcrds.PodCliqueSetCRD(), testConversion)
	require.ErrorContains(t, err, "failed to get CRD")

	err = cl.Get(context.Background(), client.ObjectKey{Name: "podcliquesets.grove.io"},
		&apiextensionsv1.CustomResourceDefinition{})
	assert.True(t, apierrors.IsNotFound(err))
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
		testConversion,
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

	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), crds, testConversion)
	require.NoError(t, err)

	err = crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), crds, testConversion)
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

	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{crdYAML("1")}, testConversion)
	require.NoError(t, err)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: crdName}, obj))
	assert.Equal(t, "1", obj.GetLabels()["test-version"], "initial label should be 1")

	err = crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{crdYAML("2")}, testConversion)
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

	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{"not: valid: yaml: [[["}, testConversion)
	assert.Error(t, err)
}

// TestInstallCRDs_ClusterTopologyBindingShortName verifies that the ClusterTopologyBinding CRD
// has the correct shortName "ctb" and not "ct" (which would conflict with the old ClusterTopology CRD).
//
// Flow:
//  1. Call InstallCRDs with the ClusterTopologyBinding CRD.
//  2. Fetch the CRD from the cluster.
//  3. Assert that spec.names.shortNames contains "ctb" and not "ct".
func TestInstallCRDs_ClusterTopologyBindingShortName(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()

	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard(), []string{operatorcrds.ClusterTopologyCRD()}, testConversion)
	require.NoError(t, err)

	crd := &apiextensionsv1.CustomResourceDefinition{}
	err = cl.Get(ctx, client.ObjectKey{Name: "clustertopologybindings.grove.io"}, crd)
	require.NoError(t, err, "ClusterTopologyBinding CRD should exist")

	shortNames := crd.Spec.Names.ShortNames
	assert.Contains(t, shortNames, "ctb", "ClusterTopologyBinding should have shortName 'ctb'")
	assert.NotContains(t, shortNames, "ct", "ClusterTopologyBinding should NOT have shortName 'ct' (conflicts with old ClusterTopology)")
}

func TestInstallCRDs_DeletesEmptyLegacyClusterTopologyCRD(t *testing.T) {
	legacyCRD := &apiextensionsv1.CustomResourceDefinition{}
	legacyCRD.Name = "clustertopologies.grove.io"
	cl := buildFakeClient(legacyCRD)

	err := crdinstaller.InstallCRDs(context.Background(), cl, logr.Discard(), nil, testConversion)
	require.NoError(t, err)

	err = cl.Get(context.Background(), client.ObjectKey{Name: legacyCRD.Name}, &apiextensionsv1.CustomResourceDefinition{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestInstallCRDs_RetainsLegacyClusterTopologyCRDWithResources(t *testing.T) {
	legacyCRD := &apiextensionsv1.CustomResourceDefinition{}
	legacyCRD.Name = "clustertopologies.grove.io"
	legacyTopology := &unstructured.Unstructured{}
	legacyTopology.SetGroupVersionKind(schema.GroupVersion{Group: "grove.io", Version: "v1alpha1"}.WithKind("ClusterTopology"))
	legacyTopology.SetName("existing-topology")
	cl := buildFakeClient(legacyCRD, legacyTopology)

	err := crdinstaller.InstallCRDs(context.Background(), cl, logr.Discard(), nil, testConversion)
	require.NoError(t, err)

	err = cl.Get(context.Background(), client.ObjectKey{Name: legacyCRD.Name}, &apiextensionsv1.CustomResourceDefinition{})
	assert.NoError(t, err)
}
