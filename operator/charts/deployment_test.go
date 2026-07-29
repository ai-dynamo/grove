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
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

func TestOperatorHealthProbes(t *testing.T) {
	tests := []struct {
		name       string
		values     map[string]interface{}
		wantProbes bool
	}{
		{
			name:       "enabled by default",
			values:     nil,
			wantProbes: true,
		},
		{
			name: "explicitly disabled",
			values: map[string]interface{}{
				"config": map[string]interface{}{
					"server": map[string]interface{}{
						"healthProbes": map[string]interface{}{
							"enable": false,
						},
					},
				},
			},
			wantProbes: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployment := renderOperatorDeployment(t, tt.values)
			require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
			container := deployment.Spec.Template.Spec.Containers[0]

			if !tt.wantProbes {
				assert.Nil(t, container.LivenessProbe)
				assert.Nil(t, container.ReadinessProbe)
				return
			}

			require.NotNil(t, container.LivenessProbe)
			require.NotNil(t, container.LivenessProbe.HTTPGet)
			assert.Equal(t, "/healthz", container.LivenessProbe.HTTPGet.Path)
			assert.EqualValues(t, 9444, container.LivenessProbe.HTTPGet.Port.IntVal)

			require.NotNil(t, container.ReadinessProbe)
			require.NotNil(t, container.ReadinessProbe.HTTPGet)
			assert.Equal(t, "/readyz", container.ReadinessProbe.HTTPGet.Path)
			assert.EqualValues(t, 9444, container.ReadinessProbe.HTTPGet.Port.IntVal)
		})
	}
}

func renderOperatorDeployment(t *testing.T, values map[string]interface{}) *appsv1.Deployment {
	t.Helper()

	chart, err := loader.Load(".")
	require.NoError(t, err)

	renderValues, err := chartutil.ToRenderValues(
		chart,
		values,
		chartutil.ReleaseOptions{Name: "grove", Namespace: "default", IsInstall: true},
		chartutil.DefaultCapabilities,
	)
	require.NoError(t, err)

	manifests, err := engine.Render(chart, renderValues)
	require.NoError(t, err)

	deploymentYAML, ok := manifests["grove-charts/templates/deployment.yaml"]
	require.True(t, ok)

	deployment := &appsv1.Deployment{}
	require.NoError(t, yaml.UnmarshalStrict([]byte(deploymentYAML), deployment))
	return deployment
}
