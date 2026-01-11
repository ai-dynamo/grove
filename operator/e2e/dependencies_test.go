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

package e2e

import (
	"strings"
	"sync"
	"testing"
)

// TestGetDependencies verifies that dependencies can be loaded successfully
// and that all required fields are present and properly formatted.
func TestGetDependencies(t *testing.T) {
	deps, err := GetDependencies()
	if err != nil {
		t.Fatalf("Failed to load dependencies: %v", err)
	}

	// Verify we have images
	if len(deps.Images) == 0 {
		t.Error("Expected at least one image dependency")
	}

	// Verify image structure
	for _, img := range deps.Images {
		if img.Name == "" {
			t.Error("Image name should not be empty")
		}
		if img.Version == "" {
			t.Error("Image version should not be empty")
		}
		fullImage := img.FullImageName()
		if !strings.Contains(fullImage, ":") {
			t.Errorf("Full image name should contain ':' separator: %s", fullImage)
		}
	}

	// Verify Kai Scheduler helm chart
	if deps.HelmCharts.KaiScheduler.ReleaseName == "" {
		t.Error("Kai Scheduler release name should not be empty")
	}
	if deps.HelmCharts.KaiScheduler.ChartRef == "" {
		t.Error("Kai Scheduler chart ref should not be empty")
	}
	if deps.HelmCharts.KaiScheduler.Version == "" {
		t.Error("Kai Scheduler version should not be empty")
	}
	if deps.HelmCharts.KaiScheduler.Namespace == "" {
		t.Error("Kai Scheduler namespace should not be empty")
	}
}

// TestGetImagesToPrePull verifies that the GetImagesToPrePull method returns
// properly formatted image names in name:version format.
func TestGetImagesToPrePull(t *testing.T) {
	deps, err := GetDependencies()
	if err != nil {
		t.Fatalf("Failed to load dependencies: %v", err)
	}

	images := deps.GetImagesToPrePull()
	if len(images) == 0 {
		t.Error("Expected at least one image to prepull")
	}

	// Verify all images are in name:version format
	for _, img := range images {
		if !strings.Contains(img, ":") {
			t.Errorf("Image should be in name:version format: %s", img)
		}
	}
}

// TestImageDependencyFullImageName validates that the FullImageName method
// correctly combines image name and version with the ':' separator for various
// image formats (standard registry, GCR, simple names).
func TestImageDependencyFullImageName(t *testing.T) {
	tests := []struct {
		name        string
		image       ImageDependency
		expected    string
		description string
	}{
		{
			name:        "standard image",
			image:       ImageDependency{Name: "docker.io/library/nginx", Version: "1.21"},
			expected:    "docker.io/library/nginx:1.21",
			description: "Standard image with registry and namespace",
		},
		{
			name:        "gcr image",
			image:       ImageDependency{Name: "gcr.io/project/image", Version: "v2.3.4"},
			expected:    "gcr.io/project/image:v2.3.4",
			description: "GCR image with semantic version",
		},
		{
			name:        "simple image",
			image:       ImageDependency{Name: "ubuntu", Version: "latest"},
			expected:    "ubuntu:latest",
			description: "Simple image name without registry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.image.FullImageName()
			if result != tt.expected {
				t.Errorf("%s: expected %s, got %s", tt.description, tt.expected, result)
			}
		})
	}
}

// TestEmbeddedYAMLIsValid verifies that the go:embed directive successfully
// embedded the dependencies.yaml file and that it contains expected structure.
func TestEmbeddedYAMLIsValid(t *testing.T) {
	// Test that the embedded YAML data is valid and non-empty
	if len(dependenciesYAML) == 0 {
		t.Fatal("Embedded dependencies YAML is empty")
	}

	// Verify it contains expected YAML structure markers
	yamlContent := string(dependenciesYAML)
	if !strings.Contains(yamlContent, "images:") {
		t.Error("Embedded YAML should contain 'images:' section")
	}
	if !strings.Contains(yamlContent, "helmCharts:") {
		t.Error("Embedded YAML should contain 'helmCharts:' section")
	}
}

// TestGetDependenciesSingleton confirms that GetDependencies implements the
// singleton pattern correctly, returning the same instance on multiple calls.
func TestGetDependenciesSingleton(t *testing.T) {
	// Test that GetDependencies returns the same instance on multiple calls
	deps1, err1 := GetDependencies()
	if err1 != nil {
		t.Fatalf("Failed to load dependencies (first call): %v", err1)
	}

	deps2, err2 := GetDependencies()
	if err2 != nil {
		t.Fatalf("Failed to load dependencies (second call): %v", err2)
	}

	// Both should return the same pointer (singleton pattern)
	if deps1 != deps2 {
		t.Error("GetDependencies should return the same instance on multiple calls")
	}
}

// TestGetDependenciesConcurrent verifies thread-safety of GetDependencies
// by calling it from 100 concurrent goroutines, ensuring sync.Once works correctly.
func TestGetDependenciesConcurrent(t *testing.T) {
	// Test concurrent access to GetDependencies (should be thread-safe with sync.Once)
	const numGoroutines = 100
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	results := make([]*Dependencies, numGoroutines)
	errors := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(index int) {
			defer wg.Done()
			results[index], errors[index] = GetDependencies()
		}(i)
	}

	wg.Wait()

	// Check that all calls succeeded
	for i, err := range errors {
		if err != nil {
			t.Errorf("Goroutine %d failed: %v", i, err)
		}
	}

	// Check that all calls returned the same instance
	firstResult := results[0]
	for i, result := range results {
		if result != firstResult {
			t.Errorf("Goroutine %d got different instance", i)
		}
	}
}

// TestSpecificExpectedImages validates that critical images required for E2E tests
// are present in the dependencies (NFD, GPU Operator, Kai Scheduler components).
func TestSpecificExpectedImages(t *testing.T) {
	deps, err := GetDependencies()
	if err != nil {
		t.Fatalf("Failed to load dependencies: %v", err)
	}

	// Test that specific expected images are present
	expectedImages := []string{
		"registry.k8s.io/nfd/node-feature-discovery",
		"ghcr.io/nvidia/kai-scheduler/admission",
		"ghcr.io/nvidia/kai-scheduler/scheduler",
	}

	imageMap := make(map[string]bool)
	for _, img := range deps.Images {
		imageMap[img.Name] = true
	}

	for _, expected := range expectedImages {
		if !imageMap[expected] {
			t.Errorf("Expected image not found: %s", expected)
		}
	}
}

// TestHelmChartsRequiredFields ensures all Helm chart configurations have
// required fields populated and versions follow expected format (start with 'v').
func TestHelmChartsRequiredFields(t *testing.T) {
	deps, err := GetDependencies()
	if err != nil {
		t.Fatalf("Failed to load dependencies: %v", err)
	}

	tests := []struct {
		name  string
		chart HelmChartDependency
	}{
		{"KaiScheduler", deps.HelmCharts.KaiScheduler},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.chart.ReleaseName == "" {
				t.Errorf("%s: ReleaseName is empty", tt.name)
			}
			if tt.chart.ChartRef == "" {
				t.Errorf("%s: ChartRef is empty", tt.name)
			}
			if tt.chart.Version == "" {
				t.Errorf("%s: Version is empty", tt.name)
			}
			if tt.chart.Namespace == "" {
				t.Errorf("%s: Namespace is empty", tt.name)
			}
			// Validate version format (should start with 'v' for these charts)
			if !strings.HasPrefix(tt.chart.Version, "v") {
				t.Errorf("%s: Version should start with 'v', got: %s", tt.name, tt.chart.Version)
			}
		})
	}
}

// TestImageVersionFormats validates that all image versions are properly formatted:
// non-empty, no colons, and result in parseable name:version strings.
func TestImageVersionFormats(t *testing.T) {
	deps, err := GetDependencies()
	if err != nil {
		t.Fatalf("Failed to load dependencies: %v", err)
	}

	for _, img := range deps.Images {
		// Versions should not be empty
		if img.Version == "" {
			t.Errorf("Image %s has empty version", img.Name)
		}

		// Versions should not contain colons (that would break the image:tag format)
		if strings.Contains(img.Version, ":") {
			t.Errorf("Image %s version contains colon: %s", img.Name, img.Version)
		}

		// Full image name should be parseable (no double colons, etc.)
		fullName := img.FullImageName()
		parts := strings.Split(fullName, ":")
		if len(parts) != 2 {
			t.Errorf("Image %s full name has unexpected format: %s", img.Name, fullName)
		}
	}
}

// TestNoDuplicateImages ensures that each image appears only once in the
// dependencies list, preventing accidental duplicate entries.
func TestNoDuplicateImages(t *testing.T) {
	deps, err := GetDependencies()
	if err != nil {
		t.Fatalf("Failed to load dependencies: %v", err)
	}

	seen := make(map[string]bool)
	for _, img := range deps.Images {
		if seen[img.Name] {
			t.Errorf("Duplicate image found: %s", img.Name)
		}
		seen[img.Name] = true
	}
}
