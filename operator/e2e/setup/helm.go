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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

// HelmInstallConfig holds configuration for Helm chart installations.
type HelmInstallConfig struct {
	// RestConfig is the Kubernetes REST configuration. If nil, uses default kubeconfig.
	RestConfig *rest.Config
	// ReleaseName is the name of the Helm release. Required unless GenerateName is true.
	ReleaseName string
	// ChartRef is the chart reference (path, URL, or chart name). Required.
	ChartRef string
	// ChartVersion is the version of the chart to install. Required.
	ChartVersion string
	// Namespace is the Kubernetes namespace to install into. Required.
	Namespace string
	// CreateNamespace creates the namespace if it doesn't exist.
	CreateNamespace bool
	// Wait blocks until all resources are ready.
	Wait bool
	// GenerateName generates a random release name with ReleaseName as prefix.
	GenerateName bool
	// Values are the chart values to use for the installation.
	Values map[string]interface{}
	// HelmLoggerFunc is called for Helm operation logging.
	HelmLoggerFunc func(format string, v ...interface{})
	// Logger is the full logger for component operations.
	Logger *utils.Logger
	// RepoURL is the base URL of the Helm repository (optional, for direct chart downloads).
	RepoURL string
	// ReuseValues reuses the last release's values and merges in the new values.
	ReuseValues bool
}

// Validate validates and sets defaults for the configuration.
func (c *HelmInstallConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("config cannot be nil")
	}

	var missing []string
	if c.ReleaseName == "" && !c.GenerateName {
		missing = append(missing, "release name (or enable GenerateName)")
	}
	if c.ChartRef == "" {
		missing = append(missing, "chart reference")
	}
	if c.ChartVersion == "" {
		missing = append(missing, "chart version")
	}
	if c.Namespace == "" {
		missing = append(missing, "namespace")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}

	// Set defaults
	if c.Values == nil {
		c.Values = make(map[string]interface{})
	}
	if c.HelmLoggerFunc == nil {
		c.HelmLoggerFunc = func(_ string, _ ...interface{}) {}
	}
	return nil
}

// InstallHelmChart installs a Helm chart with the given configuration.
func InstallHelmChart(config *HelmInstallConfig) (*release.Release, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	// Initialize Helm action configuration
	config.HelmLoggerFunc("Setting up Helm configuration for %s...", config.ReleaseName)
	actionConfig, err := setupHelmAction(config)
	if err != nil {
		return nil, err
	}

	// Resolve chart location (download from HTTP or locate via Helm)
	chartPath, err := resolveChart(actionConfig, config)
	if err != nil {
		return nil, err
	}

	// Load and validate the chart
	config.HelmLoggerFunc("Loading chart from %s...", chartPath)
	chart, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load chart: %w", err)
	}

	// Install the chart
	config.HelmLoggerFunc("Installing chart %s in namespace %s...", config.ChartRef, config.Namespace)
	installClient := newInstallClient(actionConfig, config)
	rel, err := installClient.Run(chart, config.Values)
	if err != nil {
		return nil, fmt.Errorf("helm install failed: %w", err)
	}

	config.HelmLoggerFunc("✅ Release '%s' installed successfully. Status: %s", rel.Name, rel.Info.Status)
	return rel, nil
}

// UpgradeHelmChart upgrades a Helm chart with the given configuration.
func UpgradeHelmChart(config *HelmInstallConfig) (*release.Release, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	// Initialize Helm action configuration
	config.HelmLoggerFunc("Setting up Helm configuration for upgrade of %s...", config.ReleaseName)
	actionConfig, err := setupHelmAction(config)
	if err != nil {
		return nil, err
	}

	// Resolve chart location (download from HTTP or locate via Helm)
	chartPath, err := resolveChart(actionConfig, config)
	if err != nil {
		return nil, err
	}

	// Load and validate the chart
	config.HelmLoggerFunc("Loading chart from %s...", chartPath)
	chart, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load chart: %w", err)
	}

	// Upgrade the chart
	config.HelmLoggerFunc("Upgrading release %s in namespace %s...", config.ReleaseName, config.Namespace)
	upgradeClient := newUpgradeClient(actionConfig, config)
	rel, err := upgradeClient.Run(config.ReleaseName, chart, config.Values)
	if err != nil {
		return nil, fmt.Errorf("helm upgrade failed: %w", err)
	}

	config.HelmLoggerFunc("✅ Release '%s' upgraded successfully. Status: %s", rel.Name, rel.Info.Status)
	return rel, nil
}

// setupHelmAction sets up Helm action configuration.
func setupHelmAction(config *HelmInstallConfig) (*action.Configuration, error) {
	actionConfig := new(action.Configuration)

	// Create a RESTClientGetter that can handle both a custom rest.Config and the default kubeconfig path.
	restClientGetter := newRESTClientGetter(config.Namespace, config.RestConfig)

	// Initialize the action configuration with the REST client, namespace, driver, and logger.
	if err := actionConfig.Init(restClientGetter, config.Namespace, os.Getenv("HELM_DRIVER"), config.HelmLoggerFunc); err != nil {
		return nil, fmt.Errorf("failed to initialize Helm action configuration: %w", err)
	}

	// Initialize the OCI registry client for pulling charts from OCI registries.
	regClient, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create Helm registry client: %w", err)
	}
	actionConfig.RegistryClient = regClient

	return actionConfig, nil
}

// resolveChart determines how to obtain the chart and returns its local path.
func resolveChart(actionConfig *action.Configuration, config *HelmInstallConfig) (string, error) {
	// If RepoURL is provided, download the chart directly via HTTP
	if config.RepoURL != "" {
		config.HelmLoggerFunc("Downloading chart %s version %s...", config.ChartRef, config.ChartVersion)
		return downloadChart(config)
	}

	// Otherwise, use Helm's LocateChart (for OCI registries or local paths)
	config.HelmLoggerFunc("Locating chart %s...", config.ChartRef)
	installClient := newInstallClient(actionConfig, config)
	chartPath, err := installClient.LocateChart(config.ChartRef, cli.New())
	if err != nil {
		return "", fmt.Errorf("failed to locate chart: %w", err)
	}
	return chartPath, nil
}

// downloadChart downloads a Helm chart tarball directly via HTTP.
func downloadChart(config *HelmInstallConfig) (string, error) {
	// Create a temporary directory for the downloaded chart
	tempDir, err := os.MkdirTemp("", "helm-chart-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Construct the chart URL: <repoURL>/charts/<chartName>-<version>.tgz
	chartURL := fmt.Sprintf("%s/charts/%s-%s.tgz", config.RepoURL, config.ChartRef, config.ChartVersion)
	config.HelmLoggerFunc("Chart URL: %s", chartURL)

	// Download the chart using HTTP
	resp, err := http.Get(chartURL)
	if err != nil {
		return "", fmt.Errorf("HTTP GET failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP request failed with status %d", resp.StatusCode)
	}

	// Save the chart to a file
	chartFileName := fmt.Sprintf("%s-%s.tgz", config.ChartRef, config.ChartVersion)
	chartPath := filepath.Join(tempDir, chartFileName)

	outFile, err := os.Create(chartPath)
	if err != nil {
		return "", fmt.Errorf("failed to create chart file: %w", err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, resp.Body); err != nil {
		return "", fmt.Errorf("failed to write chart file: %w", err)
	}

	config.HelmLoggerFunc("Chart downloaded to: %s", chartPath)
	return chartPath, nil
}

// newInstallClient creates and configures a Helm install action client from the provided configuration.
func newInstallClient(actionConfig *action.Configuration, config *HelmInstallConfig) *action.Install {
	client := action.NewInstall(actionConfig)

	client.ReleaseName = config.ReleaseName
	client.GenerateName = config.GenerateName
	client.Namespace = config.Namespace
	client.CreateNamespace = config.CreateNamespace
	client.Wait = config.Wait
	client.Version = config.ChartVersion
	client.Replace = true // Allow replacing failed releases on retry

	return client
}

// newUpgradeClient creates and configures a Helm upgrade action client from the provided configuration.
func newUpgradeClient(actionConfig *action.Configuration, config *HelmInstallConfig) *action.Upgrade {
	client := action.NewUpgrade(actionConfig)
	client.Namespace = config.Namespace
	client.Wait = config.Wait
	client.Version = config.ChartVersion
	client.ReuseValues = config.ReuseValues

	return client
}

// newRESTClientGetter creates a RESTClientGetter for Helm actions
func newRESTClientGetter(namespace string, restConfig *rest.Config) genericclioptions.RESTClientGetter {
	flags := genericclioptions.NewConfigFlags(true)
	flags.Namespace = &namespace

	// Inject custom REST config if provided, otherwise use default kubeconfig
	flags.WrapConfigFn = func(c *rest.Config) *rest.Config {
		if restConfig != nil {
			return restConfig
		}
		return c
	}

	return flags
}
