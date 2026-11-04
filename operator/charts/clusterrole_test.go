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
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func TestComputeDomainRBAC(t *testing.T) {
	tests := []struct {
		name             string
		autoMNNVLEnabled bool
		wantVerbs        []string
	}{
		{
			name:      "cleanup permissions when AutoMNNVL is disabled",
			wantVerbs: []string{"delete", "get", "list", "watch"},
		},
		{
			name:             "synchronization permissions when AutoMNNVL is enabled",
			autoMNNVLEnabled: true,
			wantVerbs:        []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]interface{}{
				"config": map[string]interface{}{
					"network": map[string]interface{}{
						"autoMNNVLEnabled": test.autoMNNVLEnabled,
					},
				},
			}
			manifests := renderChart(t, values)
			clusterRoleYAML, ok := manifests["grove-charts/templates/clusterrole.yaml"]
			require.True(t, ok)

			clusterRole := &rbacv1.ClusterRole{}
			require.NoError(t, yaml.UnmarshalStrict([]byte(clusterRoleYAML), clusterRole))

			verbs := make(map[string]struct{})
			for _, rule := range clusterRole.Rules {
				if !contains(rule.APIGroups, "resource.nvidia.com") || !contains(rule.Resources, "computedomains") {
					continue
				}
				for _, verb := range rule.Verbs {
					verbs[verb] = struct{}{}
				}
			}
			assert.ElementsMatch(t, test.wantVerbs, mapKeys(verbs))
		})
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
