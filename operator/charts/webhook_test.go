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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

func TestPodCliqueValidationWebhook(t *testing.T) {
	chart, err := loader.Load(".")
	require.NoError(t, err)
	values, err := chartutil.ToRenderValues(
		chart,
		nil,
		chartutil.ReleaseOptions{Name: "grove", Namespace: "default", IsInstall: true},
		chartutil.DefaultCapabilities,
	)
	require.NoError(t, err)
	manifests, err := engine.Render(chart, values)
	require.NoError(t, err)

	rendered, ok := manifests["grove-charts/templates/pcs-validating-webhook-config.yaml"]
	require.True(t, ok)
	config := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	require.NoError(t, yaml.UnmarshalStrict([]byte(rendered), config))

	require.Len(t, config.Webhooks, 2)
	webhook := config.Webhooks[1]
	assert.Equal(t, "pclq.validating.webhooks.grove.io", webhook.Name)
	require.NotNil(t, webhook.ClientConfig.Service)
	require.NotNil(t, webhook.ClientConfig.Service.Path)
	assert.Equal(t, "/webhooks/validate-podclique", *webhook.ClientConfig.Service.Path)
	require.Len(t, webhook.Rules, 1)
	assert.Equal(t, []string{"podcliques", "podcliques/scale"}, webhook.Rules[0].Resources)
	assert.Equal(t, []admissionregistrationv1.OperationType{admissionregistrationv1.Update}, webhook.Rules[0].Operations)
}
