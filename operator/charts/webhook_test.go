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
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// TestWebhookRulesMatchHubVersion verifies that every grove.io admission rule matches only the hub version
// with matchPolicy Equivalent, so that requests for any served version reach the handlers as v1beta1 objects.
func TestWebhookRulesMatchHubVersion(t *testing.T) {
	manifests := renderChart(t, map[string]interface{}{
		"config": map[string]interface{}{
			"authorizer": map[string]interface{}{"enabled": true},
		},
	})

	groveRules := 0
	for path, manifest := range manifests {
		if !strings.HasSuffix(path, "-webhook-config.yaml") || strings.TrimSpace(manifest) == "" {
			continue
		}
		// The webhooks of mutating and validating configurations share the fields checked here.
		config := &admissionregistrationv1.ValidatingWebhookConfiguration{}
		require.NoError(t, yaml.Unmarshal([]byte(manifest), config), path)
		require.NotEmpty(t, config.Webhooks, path)

		for _, webhook := range config.Webhooks {
			require.NotNil(t, webhook.ClientConfig.Service, webhook.Name)
			require.NotNil(t, webhook.ClientConfig.Service.Path, webhook.Name)
			assert.True(t, strings.HasPrefix(*webhook.ClientConfig.Service.Path, "/webhooks/"), "%s: %s", webhook.Name, *webhook.ClientConfig.Service.Path)
			require.NotNil(t, webhook.MatchPolicy, webhook.Name)
			assert.Equal(t, admissionregistrationv1.Equivalent, *webhook.MatchPolicy, webhook.Name)
			for _, rule := range webhook.Rules {
				if !slices.Contains(rule.APIGroups, "grove.io") {
					continue
				}
				groveRules++
				assert.Equal(t, []string{"v1beta1"}, rule.APIVersions, "%s: %v", webhook.Name, rule.Resources)
			}
		}
	}
	// PodCliqueSet defaulting and validation, PodClique, PodCliqueScalingGroup, ClusterTopologyBinding and the
	// authorizer.
	assert.Equal(t, 6, groveRules)
}

// TestClusterRoleConversionCRDAccess verifies that the installer can patch or upgrade the CRDs and cert-controller
// can inject the conversion CA.
func TestClusterRoleConversionCRDAccess(t *testing.T) {
	manifests := renderChart(t, map[string]interface{}{
		"crdInstaller": map[string]interface{}{"upgradeCRDs": false},
	})
	clusterRoleYAML, ok := manifests["grove-charts/templates/clusterrole.yaml"]
	require.True(t, ok)
	clusterRole := &rbacv1.ClusterRole{}
	require.NoError(t, yaml.UnmarshalStrict([]byte(clusterRoleYAML), clusterRole))

	for _, crd := range []string{"podcliquesets.grove.io", "podcliques.grove.io", "podcliquescalinggroups.grove.io",
		"clustertopologybindings.grove.io", "podgangmaps.grove.io", "podgangs.scheduler.grove.io"} {
		for _, verb := range []string{"get", "create", "update", "patch", "delete"} {
			assert.True(t, allowsCRD(clusterRole, "customresourcedefinitions", crd, verb),
				"%s on %s", verb, crd)
		}
	}
	for _, verb := range []string{"list", "watch"} {
		assert.True(t, allowsCRD(clusterRole, "customresourcedefinitions", "podcliquesets.grove.io", verb), verb)
	}
}

func allowsCRD(clusterRole *rbacv1.ClusterRole, resource, name, verb string) bool {
	for _, rule := range clusterRole.Rules {
		if slices.Contains(rule.APIGroups, "apiextensions.k8s.io") &&
			slices.Contains(rule.Resources, resource) &&
			slices.Contains(rule.Verbs, verb) &&
			(len(rule.ResourceNames) == 0 || slices.Contains(rule.ResourceNames, name)) {
			return true
		}
	}
	return false
}
