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

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/ai-dynamo/grove/operator/internal/crdinstaller"
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

func TestInstallCRDs_AllApplied(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()

	err := crdinstaller.InstallCRDs(ctx, cl, logr.Discard())
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

func TestApplyCRD_ReturnsName(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()

	name, err := crdinstaller.ApplyCRD(ctx, cl, []byte(minimalCRDYAML("testthings.test.io", "test.io", "testthings", "TestThing")))
	require.NoError(t, err)
	assert.Equal(t, "testthings.test.io", name)
}

func TestApplyCRD_Idempotent(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()
	yaml := []byte(minimalCRDYAML("testthings.test.io", "test.io", "testthings", "TestThing"))

	_, err := crdinstaller.ApplyCRD(ctx, cl, yaml)
	require.NoError(t, err)

	// Second apply of the same yaml must not error.
	_, err = crdinstaller.ApplyCRD(ctx, cl, yaml)
	require.NoError(t, err)
}

func TestApplyCRD_ReturnsErrorOnInvalidYAML(t *testing.T) {
	cl := buildFakeClient()
	ctx := context.Background()

	_, err := crdinstaller.ApplyCRD(ctx, cl, []byte("not: valid: yaml: [[["))
	assert.Error(t, err)
}
