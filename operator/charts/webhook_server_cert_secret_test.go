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

package charts_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

const webhookSecretManifest = "grove-charts/templates/webhook-server-cert-secret.yaml"

func TestWebhookServerSecret(t *testing.T) {
	emptyData := map[string][]byte{corev1.TLSCertKey: {}, corev1.TLSPrivateKeyKey: {}}
	certificate := []byte("operator-generated certificate")
	privateKey := []byte("operator-generated private key")
	tests := []struct {
		name     string
		secret   *corev1.Secret
		values   map[string]interface{}
		wantData map[string][]byte
		wantOmit bool
	}{
		{name: "fresh install", wantData: emptyData},
		{name: "secret without data", secret: &corev1.Secret{}, wantData: emptyData},
		{
			name: "keep existing certificate and key",
			secret: &corev1.Secret{Data: map[string][]byte{
				corev1.TLSCertKey: certificate, corev1.TLSPrivateKeyKey: privateKey,
				"ca.crt": []byte("operator-managed CA"),
			}},
			wantData: map[string][]byte{corev1.TLSCertKey: certificate, corev1.TLSPrivateKeyKey: privateKey},
		},
		{
			name:     "missing key defaults to empty",
			secret:   &corev1.Secret{Data: map[string][]byte{corev1.TLSCertKey: certificate}},
			wantData: map[string][]byte{corev1.TLSCertKey: certificate, corev1.TLSPrivateKeyKey: {}},
		},
		{
			name:     "missing certificate defaults to empty",
			secret:   &corev1.Secret{Data: map[string][]byte{corev1.TLSPrivateKeyKey: privateKey}},
			wantData: map[string][]byte{corev1.TLSCertKey: {}, corev1.TLSPrivateKeyKey: privateKey},
		},
		{
			name: "secret in another namespace is ignored",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: "another-release"},
				Data:       map[string][]byte{corev1.TLSCertKey: certificate, corev1.TLSPrivateKeyKey: privateKey},
			},
			wantData: emptyData,
		},
		{
			name:     "disabled secret does not query cluster",
			values:   map[string]interface{}{"webhookServerSecret": map[string]interface{}{"enabled": false}},
			wantOmit: true,
		},
		{
			name: "manual certificates do not query cluster",
			values: map[string]interface{}{"config": map[string]interface{}{
				"server": map[string]interface{}{"webhooks": map[string]interface{}{"certProvisionMode": "manual"}},
			}},
			wantOmit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newWebhookSecretClientProvider(t, tt.secret)
			manifests, err := renderChartWithSecretClient(t, tt.values, provider)
			require.NoError(t, err)
			if tt.wantOmit {
				assert.Empty(t, strings.TrimSpace(manifests[webhookSecretManifest]))
				assert.Empty(t, provider.client.Actions())
				return
			}
			require.NotEmpty(t, manifests[webhookSecretManifest])
			var secret corev1.Secret
			require.NoError(t, yaml.Unmarshal([]byte(manifests[webhookSecretManifest]), &secret))
			assert.Equal(t, corev1.SecretTypeTLS, secret.Type)
			assert.Equal(t, "grove-webhook-server-cert", secret.Name)
			assert.Equal(t, "grove-system", secret.Namespace)
			assert.Equal(t, tt.wantData, secret.Data)
			require.Len(t, provider.client.Actions(), 1)
			action := provider.client.Actions()[0]
			assert.Equal(t, "get", action.GetVerb())
			assert.Equal(t, "grove-system", action.GetNamespace())
			assert.Equal(t, "grove-webhook-server-cert", action.(clienttesting.GetAction).GetName())
		})
	}
}

func TestWebhookServerSecretLookupError(t *testing.T) {
	provider := newWebhookSecretClientProvider(t, nil)
	provider.client.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("secret lookup denied")
	})
	_, err := renderChartWithSecretClient(t, nil, provider)
	require.ErrorContains(t, err, "secret lookup denied")
}

func TestWebhookServerSecretOfflineRender(t *testing.T) {
	manifests := renderChart(t, nil)
	var secret corev1.Secret
	require.NoError(t, yaml.Unmarshal([]byte(manifests[webhookSecretManifest]), &secret))
	assert.Equal(t, map[string][]byte{corev1.TLSCertKey: {}, corev1.TLSPrivateKeyKey: {}}, secret.Data)
}

type webhookSecretClientProvider struct {
	t      *testing.T
	client *fake.FakeDynamicClient
}

func newWebhookSecretClientProvider(t *testing.T, secret *corev1.Secret) *webhookSecretClientProvider {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	var objects []runtime.Object
	if secret != nil {
		secret.Name = "grove-webhook-server-cert"
		if secret.Namespace == "" {
			secret.Namespace = "grove-system"
		}
		objects = append(objects, secret)
	}
	return &webhookSecretClientProvider{t: t, client: fake.NewSimpleDynamicClient(scheme, objects...)}
}

func (p *webhookSecretClientProvider) GetClientFor(apiVersion, kind string) (dynamic.NamespaceableResourceInterface, bool, error) {
	require.Equal(p.t, "v1", apiVersion)
	require.Equal(p.t, "Secret", kind)
	return p.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}), true, nil
}

func renderChartWithSecretClient(t *testing.T, values map[string]interface{}, provider engine.ClientProvider) (map[string]string, error) {
	t.Helper()
	chart, err := loader.Load(".")
	require.NoError(t, err)
	renderValues, err := chartutil.ToRenderValues(chart, values,
		chartutil.ReleaseOptions{Name: "grove", Namespace: "grove-system", IsUpgrade: true},
		chartutil.DefaultCapabilities)
	require.NoError(t, err)
	return engine.RenderWithClientProvider(chart, renderValues, provider)
}
