// /*
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
// */

package conversion

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	operatorcrds "github.com/ai-dynamo/grove/operator/api/core/crds"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

const (
	testNamespace = "grove-test"
	testPort      = 8443
)

func TestConfigureCRDs(t *testing.T) {
	ctx := context.Background()

	t.Run("points the generated CRDs at the operator", func(t *testing.T) {
		cl := newCRDClient(t, generatedCRDs(t)...)
		setCABundle(t, cl, []byte("existing-ca"))

		require.NoError(t, ConfigureCRDs(ctx, cl, logr.Discard(), testNamespace, testPort))

		for _, name := range ConvertedCRDNames() {
			conversion := getConversion(t, cl, name)
			assert.Equal(t, apiextensionsv1.WebhookConverter, conversion.Strategy, name)
			service := conversion.Webhook.ClientConfig.Service
			require.NotNil(t, service, name)
			assert.Equal(t, ServiceName, service.Name, name)
			assert.Equal(t, testNamespace, service.Namespace, name)
			assert.Equal(t, WebhookPath, *service.Path, name)
			assert.Equal(t, int32(testPort), *service.Port, name)
			assert.Equal(t, []string{"v1"}, conversion.Webhook.ConversionReviewVersions, name)
			assert.Equal(t, []byte("existing-ca"), conversion.Webhook.ClientConfig.CABundle, "the caBundle is kept: %s", name)
		}
	})

	t.Run("sets the Webhook strategy on CRDs that have none", func(t *testing.T) {
		crds := generatedCRDs(t)
		for _, crd := range crds {
			crd.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{Strategy: apiextensionsv1.NoneConverter}
		}
		cl := newCRDClient(t, crds...)

		require.NoError(t, ConfigureCRDs(ctx, cl, logr.Discard(), testNamespace, testPort))

		for _, name := range ConvertedCRDNames() {
			conversion := getConversion(t, cl, name)
			assert.Equal(t, apiextensionsv1.WebhookConverter, conversion.Strategy, name)
			assert.Equal(t, testNamespace, conversion.Webhook.ClientConfig.Service.Namespace, name)
		}
	})

	t.Run("fails when a CRD does not serve the hub version", func(t *testing.T) {
		crds := generatedCRDs(t)
		var versions []apiextensionsv1.CustomResourceDefinitionVersion
		for _, v := range crds[0].Spec.Versions {
			if v.Name != "v1beta1" {
				versions = append(versions, v)
			}
		}
		crds[0].Spec.Versions = versions
		cl := newCRDClient(t, crds...)

		err := ConfigureCRDs(ctx, cl, logr.Discard(), testNamespace, testPort)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not serve grove.io/v1beta1")
	})

	t.Run("fails when a CRD is not installed", func(t *testing.T) {
		cl := newCRDClient(t, generatedCRDs(t)[1:]...)

		err := ConfigureCRDs(ctx, cl, logr.Discard(), testNamespace, testPort)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not installed")
	})
}

func TestCRDConversionKeeper(t *testing.T) {
	ctx := context.Background()

	t.Run("restores a reverted service address", func(t *testing.T) {
		cl := newCRDClient(t, generatedCRDs(t)...)
		keeper := NewCRDConversionKeeper(cl, logr.Discard(), testNamespace, testPort, "").(*crdConversionKeeper)
		require.NoError(t, keeper.keep(ctx))
		setCABundle(t, cl, []byte("injected-ca"))

		// Re-applying the CRD manifests resets the address to the generated default.
		for _, crd := range generatedCRDs(t) {
			live := &apiextensionsv1.CustomResourceDefinition{}
			require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: crd.Name}, live))
			live.Spec.Conversion.Webhook.ClientConfig.Service = crd.Spec.Conversion.Webhook.ClientConfig.Service
			require.NoError(t, cl.Update(ctx, live))
		}

		require.NoError(t, keeper.keep(ctx))
		for _, name := range ConvertedCRDNames() {
			conversion := getConversion(t, cl, name)
			assert.Equal(t, testNamespace, conversion.Webhook.ClientConfig.Service.Namespace, name)
			assert.Equal(t, int32(testPort), *conversion.Webhook.ClientConfig.Service.Port, name)
			assert.Equal(t, []byte("injected-ca"), conversion.Webhook.ClientConfig.CABundle, "without a CA file the caBundle is kept: %s", name)
		}
	})

	t.Run("injects and rotates the CA from the CA file", func(t *testing.T) {
		cl := newCRDClient(t, generatedCRDs(t)...)
		caFile := filepath.Join(t.TempDir(), "ca.crt")
		keeper := NewCRDConversionKeeper(cl, logr.Discard(), testNamespace, testPort, caFile).(*crdConversionKeeper)
		assert.False(t, keeper.NeedLeaderElection())

		assert.Error(t, keeper.keep(ctx), "a missing CA file is reported")

		require.NoError(t, os.WriteFile(caFile, []byte("ca-1"), 0o600))
		require.NoError(t, keeper.keep(ctx))
		for _, name := range ConvertedCRDNames() {
			conversion := getConversion(t, cl, name)
			assert.Equal(t, []byte("ca-1"), conversion.Webhook.ClientConfig.CABundle, name)
			assert.Equal(t, testNamespace, conversion.Webhook.ClientConfig.Service.Namespace, name)
		}

		require.NoError(t, os.WriteFile(caFile, []byte("ca-2"), 0o600))
		require.NoError(t, keeper.keep(ctx))
		for _, name := range ConvertedCRDNames() {
			assert.Equal(t, []byte("ca-2"), getConversion(t, cl, name).Webhook.ClientConfig.CABundle, "a rotated CA is injected: %s", name)
		}
	})
}

func generatedCRDs(t *testing.T) []*apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	var crds []*apiextensionsv1.CustomResourceDefinition
	for _, crdYAML := range []string{
		operatorcrds.PodCliqueSetCRD(),
	} {
		crds = append(crds, unmarshalCRD(t, crdYAML))
	}
	return crds
}

func unmarshalCRD(t *testing.T, crdYAML string) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, yaml.Unmarshal([]byte(crdYAML), crd))
	return crd
}

func TestGeneratedCRDsConversionStrategy(t *testing.T) {
	for _, crd := range generatedCRDs(t) {
		require.NotNil(t, crd.Spec.Conversion, crd.Name)
		assert.Equal(t, apiextensionsv1.WebhookConverter, crd.Spec.Conversion.Strategy, crd.Name)
	}
	for _, crdYAML := range []string{
		operatorcrds.PodCliqueCRD(),
		operatorcrds.PodCliqueScalingGroupCRD(),
		operatorcrds.PodGangMapCRD(),
		operatorcrds.ClusterTopologyCRD(),
	} {
		crd := unmarshalCRD(t, crdYAML)
		assert.True(t, crd.Spec.Conversion == nil || crd.Spec.Conversion.Strategy == apiextensionsv1.NoneConverter, crd.Name)
	}
}

func newCRDClient(t *testing.T, crds ...*apiextensionsv1.CustomResourceDefinition) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, crd := range crds {
		builder = builder.WithObjects(crd)
	}
	return builder.Build()
}

func setCABundle(t *testing.T, cl client.Client, caBundle []byte) {
	t.Helper()
	for _, name := range ConvertedCRDNames() {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: name}, crd))
		crd.Spec.Conversion.Webhook.ClientConfig.CABundle = caBundle
		require.NoError(t, cl.Update(context.Background(), crd))
	}
}

func getConversion(t *testing.T, cl client.Client, name string) *apiextensionsv1.CustomResourceConversion {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: name}, crd))
	require.NotNil(t, crd.Spec.Conversion, name)
	require.NotNil(t, crd.Spec.Conversion.Webhook, name)
	require.NotNil(t, crd.Spec.Conversion.Webhook.ClientConfig, name)
	return crd.Spec.Conversion
}
