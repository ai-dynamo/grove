// /*
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
// */

package utils

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NewVolcanoPodGroupCRD returns a minimal Volcano PodGroup CRD for tests.
func NewVolcanoPodGroupCRD(includeSubGroupPolicy bool) *apiextensionsv1.CustomResourceDefinition {
	specProperties := map[string]apiextensionsv1.JSONSchemaProps{}
	if includeSubGroupPolicy {
		specProperties["subGroupPolicy"] = apiextensionsv1.JSONSchemaProps{
			Type: "array",
			Items: &apiextensionsv1.JSONSchemaPropsOrArray{
				Schema: &apiextensionsv1.JSONSchemaProps{Type: "object"},
			},
		}
	}

	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "podgroups.scheduling.volcano.sh"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "scheduling.volcano.sh",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "podgroups",
				Singular: "podgroup",
				Kind:     "PodGroup",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:   "v1beta1",
					Served: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"spec": {
									Type:       "object",
									Properties: specProperties,
								},
							},
						},
					},
				},
			},
		},
	}
}
