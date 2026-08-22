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

package v1alpha1

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func TestReplicaSchemaDefaultsAndMinimums(t *testing.T) {
	pclq := loadCRD(t, "crds/grove.io_podcliques.yaml")
	pcsg := loadCRD(t, "crds/grove.io_podcliquescalinggroups.yaml")
	pcs := loadCRD(t, "crds/grove.io_podcliquesets.yaml")

	pclqSpec := pclq.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	pcsgSpec := pcsg.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	pcsTemplate := pcs.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["template"]

	schemas := map[string]apiextensionsv1.JSONSchemaProps{
		"PodClique":                    pclqSpec.Properties["replicas"],
		"PodCliqueScalingGroup":        pcsgSpec.Properties["replicas"],
		"PodClique template":           pcsTemplate.Properties["cliques"].Items.Schema.Properties["spec"].Properties["replicas"],
		"PodCliqueScalingGroup config": pcsTemplate.Properties["podCliqueScalingGroups"].Items.Schema.Properties["replicas"],
	}
	for name, schema := range schemas {
		t.Run(name, func(t *testing.T) {
			require.NotNil(t, schema.Default)
			assert.JSONEq(t, "1", string(schema.Default.Raw))
			require.NotNil(t, schema.Minimum)
			assert.Zero(t, *schema.Minimum)
		})
	}
}

func loadCRD(t *testing.T, path string) apiextensionsv1.CustomResourceDefinition {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(data, &crd))
	return crd
}
