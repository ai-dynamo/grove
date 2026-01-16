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

package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"gopkg.in/yaml.v3"
	"k8s.io/client-go/rest"
)

// UpgradeGrove upgrades the Grove operator with the specified Helm values.
//
// This uses Helm upgrade (rather than Skaffold) because:
// 1. Grove is initially installed via Skaffold, which uses Helm under the hood
// 2. For config-only changes (like switching cert modes), rebuilding images is unnecessary
// 3. Helm upgrade with ReuseValues preserves the image configuration that Skaffold set
//
// This approach avoids wasteful rebuilds while staying compatible with the Skaffold installation.
func UpgradeGrove(ctx context.Context, restConfig *rest.Config, values map[string]interface{}, logger *utils.Logger) error {
	chartPath, err := getGroveChartPath()
	if err != nil {
		return fmt.Errorf("failed to get Grove chart path: %w", err)
	}

	chartVersion, err := getChartVersion(chartPath)
	if err != nil {
		return fmt.Errorf("failed to get chart version: %w", err)
	}

	// Configure Helm upgrade using shared constants to stay in sync with Skaffold installation.
	// - ReleaseName: Uses OperatorDeploymentName from constants.go (matches skaffold.yaml's deploy.helm.releases[0].name)
	// - ChartVersion: Read from Chart.yaml to avoid version string duplication
	// - ReuseValues: Preserves image configuration that Skaffold set during initial install
	helmConfig := &HelmInstallConfig{
		RestConfig:     restConfig,
		ReleaseName:    OperatorDeploymentName,
		ChartRef:       chartPath,
		ChartVersion:   chartVersion,
		Namespace:      OperatorNamespace,
		ReuseValues:    true,
		Values:         values,
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	logger.Debug("Upgrading Grove operator")

	if _, err := UpgradeHelmChart(helmConfig); err != nil {
		return fmt.Errorf("helm upgrade failed: %w", err)
	}

	// Wait for Grove operator pod to be ready after upgrade
	if err := utils.WaitForPodsInNamespace(ctx, OperatorNamespace, restConfig, 1, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		return fmt.Errorf("grove operator pod not ready after upgrade: %w", err)
	}

	logger.Debug("Grove upgrade completed successfully")
	return nil
}

// chartYAML represents the structure of Chart.yaml for version extraction
type chartYAML struct {
	Version string `yaml:"version"`
}

// getChartVersion reads the version from Chart.yaml in the given chart directory.
// We read from Chart.yaml rather than hardcoding the version to maintain a single source
// of truth. This ensures the upgrade uses the same version as the chart itself, avoiding
// configuration drift between the chart definition and the e2e test code.
func getChartVersion(chartPath string) (string, error) {
	chartFile := filepath.Join(chartPath, "Chart.yaml")
	data, err := os.ReadFile(chartFile)
	if err != nil {
		return "", fmt.Errorf("failed to read Chart.yaml: %w", err)
	}

	var chart chartYAML
	if err := yaml.Unmarshal(data, &chart); err != nil {
		return "", fmt.Errorf("failed to parse Chart.yaml: %w", err)
	}

	if chart.Version == "" {
		return "", fmt.Errorf("version not found in Chart.yaml")
	}

	return chart.Version, nil
}

// getGroveChartPath returns the absolute path to the Grove Helm chart.
// It uses runtime.Caller to find the path relative to this source file.
func getGroveChartPath() (string, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("failed to get current file path")
	}
	// This file is at operator/e2e/setup/grove.go
	// Chart is at operator/charts
	chartPath := filepath.Join(filepath.Dir(currentFile), "../../charts")
	return filepath.Abs(chartPath)
}
