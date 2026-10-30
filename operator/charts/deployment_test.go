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
	corev1 "k8s.io/api/core/v1"
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

func TestOperatorImagePullSecrets(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]interface{}
		want   []corev1.LocalObjectReference
	}{
		{
			name:   "none by default",
			values: nil,
			want:   nil,
		},
		{
			name: "set from values",
			values: map[string]interface{}{
				"imagePullSecrets": []interface{}{
					map[string]interface{}{"name": "registry-creds"},
				},
			},
			want: []corev1.LocalObjectReference{{Name: "registry-creds"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployment := renderOperatorDeployment(t, tt.values)
			assert.Equal(t, tt.want, deployment.Spec.Template.Spec.ImagePullSecrets)
		})
	}
}

// TestDeploymentServiceName verifies that the operator and the CRD installer get the name of the operator Service,
// which the webhook certificates and the conversion webhook of the CRDs refer to.
func TestDeploymentServiceName(t *testing.T) {
	deployment := renderOperatorDeployment(t, map[string]interface{}{
		"service": map[string]interface{}{"name": "my-grove"},
		"config": map[string]interface{}{
			"server": map[string]interface{}{
				"webhooks": map[string]interface{}{"port": 8443},
			},
		},
	})
	podSpec := deployment.Spec.Template.Spec
	containers := append(append([]corev1.Container{}, podSpec.InitContainers...), podSpec.Containers...)
	require.Len(t, containers, 2)
	for _, container := range containers {
		assert.Contains(t, container.Env, corev1.EnvVar{Name: "GROVE_OPERATOR_SERVICE_NAME", Value: "my-grove"}, container.Name)
	}
	require.Len(t, podSpec.InitContainers, 1)
	assert.Contains(t, podSpec.InitContainers[0].Args, "--conversion-webhook-port=8443")
}

func TestCRDInstallerAlwaysRuns(t *testing.T) {
	for _, tc := range []struct {
		name        string
		values      map[string]interface{}
		wantUpgrade string
	}{
		{name: "unset", wantUpgrade: "--upgrade-crds=true"},
		{name: "disabled", values: map[string]interface{}{"crdInstaller": map[string]interface{}{"upgradeCRDs": false}}, wantUpgrade: "--upgrade-crds=false"},
		{name: "enabled", values: map[string]interface{}{"crdInstaller": map[string]interface{}{"upgradeCRDs": true}}, wantUpgrade: "--upgrade-crds=true"},
		{
			name: "legacy installer toggle ignored",
			values: map[string]interface{}{
				"crdInstaller": map[string]interface{}{"enabled": false},
			},
			wantUpgrade: "--upgrade-crds=true",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := renderOperatorDeployment(t, tc.values)

			require.Len(t, deployment.Spec.Template.Spec.InitContainers, 1)
			installer := deployment.Spec.Template.Spec.InitContainers[0]
			assert.Equal(t, "crd-installer", installer.Name)
			assert.Contains(t, installer.Args, tc.wantUpgrade)
		})
	}
}

func TestCRDInstallerManualCA(t *testing.T) {
	deployment := renderOperatorDeployment(t, map[string]interface{}{
		"config": map[string]interface{}{
			"server": map[string]interface{}{
				"webhooks": map[string]interface{}{
					"certProvisionMode": "manual",
					"serverCertDir":     "/custom/webhook-certs",
				},
			},
		},
	})

	require.Len(t, deployment.Spec.Template.Spec.InitContainers, 1)
	installer := deployment.Spec.Template.Spec.InitContainers[0]
	assert.Contains(t, installer.Args, "--conversion-webhook-ca-file=/custom/webhook-certs/ca.crt")
	assert.Contains(t, installer.VolumeMounts, corev1.VolumeMount{
		Name:      "grove-webhook-server-cert",
		MountPath: "/custom/webhook-certs",
		ReadOnly:  true,
	})
}

func renderOperatorDeployment(t *testing.T, values map[string]interface{}) *appsv1.Deployment {
	t.Helper()

	manifests := renderChart(t, values)
	deploymentYAML, ok := manifests["grove-charts/templates/deployment.yaml"]
	require.True(t, ok)

	deployment := &appsv1.Deployment{}
	require.NoError(t, yaml.UnmarshalStrict([]byte(deploymentYAML), deployment))
	return deployment
}

func renderChart(t *testing.T, values map[string]interface{}) map[string]string {
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
	return manifests
}
