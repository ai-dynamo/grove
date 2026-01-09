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
	_ "embed"
	"fmt"
	"sync"

	"sigs.k8s.io/yaml"
)

//go:embed dependencies.yaml
var dependenciesYAML []byte

// ImageDependency represents a container image with its version
type ImageDependency struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// FullImageName returns the complete image reference (name:version)
func (i ImageDependency) FullImageName() string {
	return fmt.Sprintf("%s:%s", i.Name, i.Version)
}

// HelmChartDependency represents a Helm chart configuration
type HelmChartDependency struct {
	ReleaseName string `yaml:"releaseName"`
	ChartRef    string `yaml:"chartRef"`
	Version     string `yaml:"version"`
	Namespace   string `yaml:"namespace"`
	RepoURL     string `yaml:"repoURL,omitempty"`
}

// HelmCharts contains all Helm chart configurations
type HelmCharts struct {
	KaiScheduler HelmChartDependency `yaml:"kaiScheduler"`
	GPUOperator  HelmChartDependency `yaml:"gpuOperator"`
}

// Dependencies represents all E2E test dependencies
type Dependencies struct {
	Images     []ImageDependency `yaml:"images"`
	HelmCharts HelmCharts        `yaml:"helmCharts"`
}

var (
	// deps holds the loaded dependencies
	deps *Dependencies
	// loadOnce ensures dependencies are loaded only once
	loadOnce sync.Once
	// loadErr stores any error from loading
	loadErr error
)

// GetDependencies loads and returns the E2E test dependencies from dependencies.yaml
// The file is embedded at compile time using go:embed, so it works
// regardless of where the binary is run from
func GetDependencies() (*Dependencies, error) {
	loadOnce.Do(func() {
		deps, loadErr = loadDependencies()
	})
	return deps, loadErr
}

// loadDependencies reads and parses the embedded dependencies.yaml file
func loadDependencies() (*Dependencies, error) {
	var deps Dependencies
	if err := yaml.Unmarshal(dependenciesYAML, &deps); err != nil {
		return nil, fmt.Errorf("failed to parse embedded dependencies.yaml: %w", err)
	}

	// Validate that we have required dependencies
	if len(deps.Images) == 0 {
		return nil, fmt.Errorf("no images defined in dependencies.yaml")
	}

	return &deps, nil
}

// GetImagesToPrePull returns a slice of full image names (name:version) for pre-pulling
func (d *Dependencies) GetImagesToPrePull() []string {
	images := make([]string, 0, len(d.Images))
	for _, img := range d.Images {
		images = append(images, img.FullImageName())
	}
	return images
}
