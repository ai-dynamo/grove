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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/strvals"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// TestUserLabelsOverrideChartLabels verifies that a user-provided release label replaces the chart's default one
// instead of rendering a duplicate key, which strict YAML parsers and Kustomize reject.
func TestUserLabelsOverrideChartLabels(t *testing.T) {
	tests := []struct {
		template string
		// values is the values key holding the template's labels map. It is also used as that map's release label
		// value, so a template that reads another key's labels fails.
		values string
		// podTemplate also checks the labels of the pod template.
		podTemplate bool
	}{
		{template: "serviceaccount.yaml", values: "serviceAccount"},
		{template: "configmap-operator.yaml", values: "configMap"},
		{template: "deployment.yaml", values: "deployment", podTemplate: true},
		{template: "service.yaml", values: "service"},
		{template: "servicemonitor.yaml", values: "metrics.serviceMonitor"},
		{template: "clusterrole.yaml", values: "clusterRole"},
		{template: "clusterrolebinding.yaml", values: "clusterRoleBinding"},
		{template: "leaderelection-role.yaml", values: "leaseRole"},
		{template: "leaderelection-rolebinding.yaml", values: "leaseRoleBinding"},
		{template: "webhook-server-cert-secret.yaml", values: "webhookServerSecret"},
		{template: "pcs-validating-webhook-config.yaml", values: "webhooks.podCliqueSetValidationWebhook"},
		{template: "pclq-validating-webhook-config.yaml", values: "webhooks.podCliqueValidationWebhook"},
		{template: "pcsg-validating-webhook-config.yaml", values: "webhooks.podCliqueScalingGroupValidationWebhook"},
		{template: "pcs-defaulting-webhook-config.yaml", values: "webhooks.podCliqueSetDefaultingWebhook"},
		{template: "clustertopology-validating-webhook-config.yaml", values: "webhooks.clusterTopologyValidationWebhook"},
		{template: "authorizer-webhook-config.yaml", values: "webhooks.authorizerWebhook"},
	}

	set := []string{"config.authorizer.enabled=true", "metrics.serviceMonitor.enabled=true"}
	for _, tt := range tests {
		set = append(set, tt.values+".labels.release="+tt.values)
	}
	values := map[string]interface{}{}
	require.NoError(t, strvals.ParseInto(strings.Join(set, ","), values))
	manifests := renderChart(t, values)

	for _, tt := range tests {
		t.Run(tt.template, func(t *testing.T) {
			manifest := manifests["grove-charts/templates/"+tt.template]
			require.NotEmpty(t, strings.TrimSpace(manifest))

			obj := &unstructured.Unstructured{}
			// Strict decoding fails on duplicate keys.
			require.NoError(t, yaml.UnmarshalStrict([]byte(manifest), &obj.Object))

			labelPaths := [][]string{{"metadata", "labels"}}
			if tt.podTemplate {
				labelPaths = append(labelPaths, []string{"spec", "template", "metadata", "labels"})
			}
			for _, path := range labelPaths {
				labels, found, err := unstructured.NestedStringMap(obj.Object, path...)
				require.NoError(t, err, path)
				require.True(t, found, path)
				assert.Equal(t, tt.values, labels["release"], path)
				assert.NotEmpty(t, labels["chart"], path)
			}
		})
	}
}
