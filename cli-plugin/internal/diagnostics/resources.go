// /*
// Copyright 2025 The Grove Authors.
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

package diagnostics

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// CollectGroveResources dumps all Grove resources as YAML.
func CollectGroveResources(dc *DiagnosticContext, output DiagnosticOutput) error {
	if dc.DynamicClient == nil {
		return fmt.Errorf("dynamic client is nil, cannot list Grove resources")
	}

	if err := output.WriteSection("GROVE RESOURCES"); err != nil {
		return err
	}

	for _, rt := range GroveResourceTypes {
		if err := collectResourceType(dc, output, rt); err != nil {
			// Log error but continue with other resource types
			_ = output.WriteLinef("[ERROR] Failed to collect %s: %v", rt.Name, err)
		}
	}

	return nil
}

// collectResourceType collects all resources of a specific type
func collectResourceType(dc *DiagnosticContext, output DiagnosticOutput, rt GroveResourceType) error {
	_ = output.WriteLinef("[INFO] Listing %s in namespace %s...", rt.Name, dc.Namespace)

	resources, err := dc.DynamicClient.Resource(rt.GVR).Namespace(dc.Namespace).List(dc.Ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list %s: %w", rt.Name, err)
	}

	if len(resources.Items) == 0 {
		_ = output.WriteLinef("[INFO] No %s found in namespace %s", rt.Name, dc.Namespace)
		return nil
	}

	_ = output.WriteLinef("[INFO] Found %d %s", len(resources.Items), rt.Name)

	for _, resource := range resources.Items {
		if err := output.WriteSubSection(fmt.Sprintf("%s: %s", rt.Singular, resource.GetName())); err != nil {
			return err
		}

		yamlBytes, err := yaml.Marshal(resource.Object)
		if err != nil {
			_ = output.WriteLinef("[ERROR] Failed to marshal %s %s: %v", rt.Singular, resource.GetName(), err)
			continue
		}

		if err := output.WriteYAML(rt.Filename, resource.GetName(), yamlBytes); err != nil {
			_ = output.WriteLinef("[ERROR] Failed to write YAML for %s %s: %v", rt.Singular, resource.GetName(), err)
		}
	}

	return nil
}
