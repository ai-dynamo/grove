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
	"sigs.k8s.io/yaml"
)

func TestServiceMonitorUserReleaseLabelOverwritesChartDefault(t *testing.T) {
	values := map[string]interface{}{
		"metrics": map[string]interface{}{
			"serviceMonitor": map[string]interface{}{
				"enabled": true,
				"name":    "grove-operator",
				"labels": map[string]interface{}{
					"app.kubernetes.io/component": "operator-service-monitor",
					"app.kubernetes.io/name":      "grove-operator",
					"app.kubernetes.io/part-of":   "grove",
					"release":                     "kube-prometheus-stack",
				},
			},
		},
	}

	manifests := renderChart(t, values)
	smYAML, ok := manifests["grove-charts/templates/servicemonitor.yaml"]
	require.True(t, ok, "ServiceMonitor manifest missing")

	// UnmarshalStrict rejects duplicate YAML keys — the bug this covers.
	var sm map[string]interface{}
	require.NoError(t, yaml.UnmarshalStrict([]byte(smYAML), &sm))

	metadata, ok := sm["metadata"].(map[string]interface{})
	require.True(t, ok)
	labels, ok := metadata["labels"].(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "kube-prometheus-stack", labels["release"])
	assert.Equal(t, "grove-operator", labels["app.kubernetes.io/name"])
	assert.Contains(t, labels, "chart")
}
