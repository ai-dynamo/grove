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

// Package setup provides internal testing utilities for configuring and managing
// Grove operator installations during e2e tests. This package is not intended for
// production use and its API may change without notice.
//
// Functions are exported to allow access from e2e test packages (e.g., operator/e2e/tests)
// which need to modify Grove configuration during test scenarios.
package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"gopkg.in/yaml.v3"
	"k8s.io/client-go/rest"
)

// GroveConfig holds typed configuration options for updating the Grove operator.
// This struct provides a user-friendly interface that gets translated to Helm values internally.
type GroveConfig struct {
	// InstallCRDs controls whether CRDs should be installed/updated.
	InstallCRDs bool
	// Webhooks contains webhook-specific configuration.
	Webhooks WebhooksConfig
}

// WebhooksConfig holds configuration for Grove's webhook server.
type WebhooksConfig struct {
	// CertProvisionMode controls how webhook certificates are provisioned.
	// Use configv1alpha1.CertProvisionModeAuto for automatic provisioning,
	// configv1alpha1.CertProvisionModeManual for external cert management.
	CertProvisionMode configv1alpha1.CertProvisionMode
	// SecretName is the name of the Kubernetes secret containing TLS certificates.
	SecretName string
	// Annotations to apply to webhook configurations (e.g., for cert-manager CA injection).
	// These annotations are applied to all webhook configurations.
	Annotations map[string]string
}

// helmValues mirrors the Helm values.yaml structure, using configv1alpha1 types
// for config.server to ensure JSON field names stay synchronized with the API.
type helmValues struct {
	InstallCRDs bool              `json:"installCRDs"`
	Config      helmConfigValues  `json:"config"`
	Webhooks    helmWebhookValues `json:"webhooks"`
}

type helmConfigValues struct {
	Server configv1alpha1.ServerConfiguration `json:"server"`
}

type helmWebhookValues struct {
	PodCliqueSetValidationWebhook helmWebhookAnnotations `json:"podCliqueSetValidationWebhook"`
	PodCliqueSetDefaultingWebhook helmWebhookAnnotations `json:"podCliqueSetDefaultingWebhook"`
	AuthorizerWebhook             helmWebhookAnnotations `json:"authorizerWebhook"`
}

type helmWebhookAnnotations struct {
	Annotations map[string]string `json:"annotations"`
}

// toHelmValues converts GroveConfig to a Helm values map.
func (c *GroveConfig) toHelmValues() (map[string]interface{}, error) {
	anns := helmWebhookAnnotations{Annotations: c.Webhooks.Annotations}
	hv := helmValues{
		InstallCRDs: c.InstallCRDs,
		Config: helmConfigValues{
			Server: configv1alpha1.ServerConfiguration{
				Webhooks: configv1alpha1.WebhookServer{
					CertProvisionMode: c.Webhooks.CertProvisionMode,
					SecretName:        c.Webhooks.SecretName,
				},
			},
		},
		Webhooks: helmWebhookValues{
			PodCliqueSetValidationWebhook: anns,
			PodCliqueSetDefaultingWebhook: anns,
			AuthorizerWebhook:             anns,
		},
	}

	return utils.ConvertTypedToUnstructured(hv)
}

// UpdateGroveConfiguration updates the Grove operator configuration.
//
// This uses Helm upgrade (rather than Skaffold) because:
// 1. Grove is initially installed via Skaffold, which uses Helm under the hood
// 2. For config-only changes (like switching cert modes), rebuilding images is unnecessary
// 3. Helm upgrade with ReuseValues preserves the image configuration that Skaffold set
//
// The chartDir parameter should be the path to the Grove Helm chart directory.
// Use GetGroveChartDir() to obtain the default chart directory path.
//
// This approach avoids wasteful rebuilds while staying compatible with the Skaffold installation.
func UpdateGroveConfiguration(ctx context.Context, restConfig *rest.Config, chartDir string, config *GroveConfig, logger *utils.Logger) error {
	chartVersion, err := getChartVersion(chartDir)
	if err != nil {
		return fmt.Errorf("failed to get chart version: %w", err)
	}

	helmValues, err := config.toHelmValues()
	if err != nil {
		return fmt.Errorf("failed to convert config to helm values: %w", err)
	}

	// Configure Helm upgrade using shared constants to stay in sync with Skaffold installation.
	// - ReleaseName: Uses OperatorDeploymentName from constants.go (matches skaffold.yaml's deploy.helm.releases[0].name)
	// - ChartVersion: Read from Chart.yaml to avoid version string duplication
	// - ReuseValues: Preserves image configuration that Skaffold set during initial install
	helmConfig := &HelmInstallConfig{
		RestConfig:     restConfig,
		ReleaseName:    OperatorDeploymentName,
		ChartRef:       chartDir,
		ChartVersion:   chartVersion,
		Namespace:      OperatorNamespace,
		ReuseValues:    true,
		Values:         helmValues,
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	logger.Debug("Updating Grove operator configuration")

	if _, err := UpgradeHelmChart(helmConfig); err != nil {
		return fmt.Errorf("helm upgrade failed: %w", err)
	}

	// Wait for Grove operator pod to be ready after upgrade
	if err := utils.WaitForPodsInNamespace(ctx, OperatorNamespace, restConfig, 1, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		return fmt.Errorf("grove operator pod not ready after upgrade: %w", err)
	}

	logger.Debug("Grove configuration update completed successfully")
	return nil
}

// chartYAML represents the structure of Chart.yaml for version extraction
type chartYAML struct {
	Version string `yaml:"version"`
}

// getChartVersion reads the version from Chart.yaml in the given chart directory.
// The chartDir parameter should be the path to a Helm chart directory. Chart.yaml is
// a required file per the Helm chart specification and will always exist for valid charts.
// We read from Chart.yaml rather than hardcoding the version to maintain a single source
// of truth, avoiding configuration drift between the chart definition and the e2e test code.
func getChartVersion(chartDir string) (string, error) {
	chartFile := filepath.Join(chartDir, "Chart.yaml")
	data, err := os.ReadFile(chartFile)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", chartFile, err)
	}

	var chart chartYAML
	if err := yaml.Unmarshal(data, &chart); err != nil {
		return "", fmt.Errorf("failed to parse %s: %w", chartFile, err)
	}

	if chart.Version == "" {
		return "", fmt.Errorf("version not found in %s", chartFile)
	}

	return chart.Version, nil
}

// GetGroveChartDir returns the absolute path to the Grove Helm chart directory.
// It uses runtime.Caller to find the path relative to this source file.
// This function is exported for use by callers of UpdateGroveConfiguration.
func GetGroveChartDir() (string, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("failed to get current file path")
	}
	// This file is at operator/e2e/setup/grove.go
	// Chart directory is at operator/charts
	chartDir := filepath.Join(filepath.Dir(currentFile), "../../charts")
	return filepath.Abs(chartDir)
}
