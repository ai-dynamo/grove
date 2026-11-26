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
	"encoding/json"
	"testing"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func TestSchedulerProfileConfig(t *testing.T) {
	tests := []struct {
		name     string
		profiles []any
	}{
		{
			name: "omitted config",
			profiles: []any{
				map[string]any{"name": "default-scheduler"},
			},
		},
		{
			name: "empty config",
			profiles: []any{
				map[string]any{"name": "default-scheduler", "config": map[string]any{}},
			},
		},
		{
			name: "configured profile",
			profiles: []any{
				map[string]any{"name": "default-scheduler", "config": map[string]any{"gangScheduling": true}},
			},
		},
		{
			name: "false value followed by another profile",
			profiles: []any{
				map[string]any{"name": "default-scheduler", "config": map[string]any{"gangScheduling": false}},
				map[string]any{"name": "kai-scheduler"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := map[string]any{
				"config": map[string]any{
					"scheduler": map[string]any{"profiles": tt.profiles},
				},
			}
			manifests := renderChart(t, values)
			configMapYAML, ok := manifests["grove-charts/templates/configmap-operator.yaml"]
			require.True(t, ok)

			var configMap corev1.ConfigMap
			require.NoError(t, yaml.UnmarshalStrict([]byte(configMapYAML), &configMap))
			configYAML, ok := configMap.Data["config.yaml"]
			require.True(t, ok)

			var config configv1beta1.OperatorConfiguration
			require.NoError(t, yaml.UnmarshalStrict([]byte(configYAML), &config))
			assert.Equal(t, configv1beta1.SchemeGroupVersion.String(), config.APIVersion)
			require.Len(t, config.Scheduler.Profiles, len(tt.profiles))
			for i, profile := range config.Scheduler.Profiles {
				expected, ok := tt.profiles[i].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, expected["name"], string(profile.Name))
				expectedConfig, configured := expected["config"]
				if !configured {
					assert.Nil(t, profile.Config)
					continue
				}
				require.NotNil(t, profile.Config)
				var actualConfig map[string]any
				require.NoError(t, json.Unmarshal(profile.Config.Raw, &actualConfig))
				assert.Equal(t, expectedConfig, actualConfig)
			}
		})
	}
}
