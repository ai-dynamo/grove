//go:build e2e

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

// Package setup provides internal testing utilities for configuring and managing
// Grove operator installations during e2e tests. This package is not intended for
// production use and its API may change without notice.
//
// Functions are exported to allow access from e2e test packages (e.g., operator/e2e/tests)
// which need to modify Grove configuration during test scenarios.
package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"

	"gopkg.in/yaml.v3"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

const webhookReadySuccessCount = 3

// GroveConfig holds typed configuration options for updating the Grove operator.
// This struct provides a user-friendly interface that gets translated to Helm values internally.
type GroveConfig struct {
	// Scheduler contains scheduler profile configuration.
	Scheduler *configv1beta1.SchedulerConfiguration
	// TopologyAwareScheduling controls topology constraint validation and translation.
	TopologyAwareScheduling *configv1beta1.TopologyAwareSchedulingConfiguration
	// Webhooks contains webhook-specific configuration.
	Webhooks WebhooksConfig
}

// WebhooksConfig holds configuration for Grove's webhook server.
type WebhooksConfig struct {
	// CertProvisionMode controls how webhook certificates are provisioned.
	// Use configv1beta1.CertProvisionModeAuto for automatic provisioning,
	// configv1beta1.CertProvisionModeManual for external cert management.
	CertProvisionMode configv1beta1.CertProvisionMode
	// SecretName is the name of the Kubernetes secret containing TLS certificates.
	SecretName string
	// Annotations to apply to webhook configurations (e.g., for cert-manager CA injection).
	// These annotations are applied to all webhook configurations.
	Annotations map[string]string
}

// helmValues mirrors the Helm values.yaml structure, using configv1beta1 types
// where the chart configuration matches the operator API.
type helmValues struct {
	Config   helmConfigValues  `json:"config"`
	Webhooks helmWebhookValues `json:"webhooks"`
}

type helmConfigValues struct {
	Server                  helmServerValues                                    `json:"server"`
	Scheduler               *configv1beta1.SchedulerConfiguration               `json:"scheduler,omitempty"`
	TopologyAwareScheduling *configv1beta1.TopologyAwareSchedulingConfiguration `json:"topologyAwareScheduling,omitempty"`
}

type helmServerValues struct {
	Webhooks     configv1beta1.WebhookServer `json:"webhooks"`
	HealthProbes helmHealthProbeValues       `json:"healthProbes"`
}

// helmHealthProbeValues includes the chart-only enable switch in addition to
// the operator's health probe server configuration.
type helmHealthProbeValues struct {
	Enable bool `json:"enable"`
	Port   int  `json:"port"`
}

type helmWebhookValues struct {
	PodCliqueSetValidationWebhook          helmWebhookAnnotations `json:"podCliqueSetValidationWebhook"`
	PodCliqueScalingGroupValidationWebhook helmWebhookAnnotations `json:"podCliqueScalingGroupValidationWebhook"`
	PodCliqueValidationWebhook             helmWebhookAnnotations `json:"podCliqueValidationWebhook"`
	PodCliqueSetDefaultingWebhook          helmWebhookAnnotations `json:"podCliqueSetDefaultingWebhook"`
	ClusterTopologyValidationWebhook       helmWebhookAnnotations `json:"clusterTopologyValidationWebhook"`
	AuthorizerWebhook                      helmWebhookAnnotations `json:"authorizerWebhook"`
}

type helmWebhookAnnotations struct {
	Annotations map[string]string `json:"annotations"`
}

// toHelmValues converts GroveConfig to a Helm values map.
// Note: We must include all required fields for webhooks (port, serverCertDir) because
// Helm's merge behavior replaces entire nested objects rather than deep-merging.
// Without these, the port would be set to 0, causing Service validation errors.
func (c *GroveConfig) toHelmValues() (map[string]interface{}, error) {
	anns := helmWebhookAnnotations{Annotations: c.Webhooks.Annotations}
	hv := helmValues{
		Config: helmConfigValues{
			Server: helmServerValues{
				Webhooks: configv1beta1.WebhookServer{
					Server: configv1beta1.Server{
						Port: DefaultWebhookPort,
					},
					ServerCertDir:     DefaultWebhookServerCertDir,
					CertProvisionMode: c.Webhooks.CertProvisionMode,
					SecretName:        c.Webhooks.SecretName,
				},
				HealthProbes: helmHealthProbeValues{
					Enable: true,
					Port:   DefaultHealthProbePort,
				},
			},
			Scheduler:               c.Scheduler,
			TopologyAwareScheduling: c.TopologyAwareScheduling,
		},
		Webhooks: helmWebhookValues{
			PodCliqueSetValidationWebhook:          anns,
			PodCliqueScalingGroupValidationWebhook: anns,
			PodCliqueValidationWebhook:             anns,
			PodCliqueSetDefaultingWebhook:          anns,
			ClusterTopologyValidationWebhook:       anns,
			AuthorizerWebhook:                      anns,
		},
	}

	return k8s.ConvertTypedToUnstructured(hv)
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
func UpdateGroveConfiguration(ctx context.Context, restConfig *rest.Config, chartDir string, config *GroveConfig, logger *log.Logger) error {
	chartVersion, err := GetGroveChartVersion(chartDir)
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
		Wait:           true,
		Timeout:        defaultPollTimeout,
		Values:         helmValues,
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	logger.Debug("Updating Grove operator configuration")

	if _, err := UpgradeHelmChart(helmConfig); err != nil {
		return fmt.Errorf("helm upgrade failed: %w", err)
	}

	// Wait for Grove operator pod to be ready after upgrade
	k8sClient, err := k8sclient.New(restConfig)
	if err != nil {
		return fmt.Errorf("create k8s client for pod wait: %w", err)
	}
	podsManager := pods.NewPodManager(k8sClient, logger)
	if err := podsManager.WaitForReadyInNamespace(ctx, OperatorNamespace, 1, defaultPollTimeout, defaultPollInterval); err != nil {
		return fmt.Errorf("grove operator pod not ready after upgrade: %w", err)
	}
	if err := waitForWebhookReady(ctx, restConfig, defaultPollTimeout, time.Second, logger); err != nil {
		return fmt.Errorf("grove webhook not ready after upgrade: %w", err)
	}

	logger.Debug("Grove configuration update completed successfully")
	return nil
}

func waitForWebhookReady(ctx context.Context, restConfig *rest.Config, timeout, interval time.Duration, logger *log.Logger) error {
	httpClient, err := rest.HTTPClientFor(restConfig)
	if err != nil {
		return fmt.Errorf("create Kubernetes HTTP client: %w", err)
	}

	// Probe defaulting directly without creating a Kubernetes resource.
	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: admissionv1.SchemeGroupVersion.String(), Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:       uuid.NewUUID(),
			Kind:      metav1.GroupVersionKind{Group: grovecorev1beta1.SchemeGroupVersion.Group, Version: grovecorev1beta1.SchemeGroupVersion.Version, Kind: "PodCliqueSet"},
			Resource:  metav1.GroupVersionResource{Group: grovecorev1beta1.SchemeGroupVersion.Group, Version: grovecorev1beta1.SchemeGroupVersion.Version, Resource: "podcliquesets"},
			Operation: admissionv1.Create,
			Name:      "webhook-readiness-probe",
			Namespace: OperatorNamespace,
			DryRun:    ptr.To(true),
			Object: k8sruntime.RawExtension{Object: &grovecorev1beta1.PodCliqueSet{
				TypeMeta:   metav1.TypeMeta{APIVersion: grovecorev1beta1.SchemeGroupVersion.String(), Kind: "PodCliqueSet"},
				ObjectMeta: metav1.ObjectMeta{Name: "webhook-readiness-probe", Namespace: OperatorNamespace},
			}},
		},
	}
	body, err := json.Marshal(review)
	if err != nil {
		return fmt.Errorf("encode webhook probe request: %w", err)
	}

	proxyURL := strings.TrimRight(restConfig.Host, "/") +
		"/api/v1/namespaces/" + OperatorNamespace +
		"/services/https:" + OperatorDeploymentName + ":webhooks/proxy/webhooks/default-podcliqueset"
	consecutiveSuccesses := 0
	fetch := waiter.FetchFunc[bool](func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL, bytes.NewReader(body))
		if err != nil {
			return false, fmt.Errorf("create webhook probe request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := httpClient.Do(req)
		if err != nil {
			if logger != nil {
				logger.Debugf("Webhook readiness probe request failed: %v", err)
			}
			return false, nil
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			if logger != nil {
				logger.Debugf("Webhook readiness probe returned HTTP status %d", response.StatusCode)
			}
			return false, nil
		}
		var result admissionv1.AdmissionReview
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			if logger != nil {
				logger.Debugf("Failed to decode webhook readiness probe response: %v", err)
			}
			return false, nil
		}
		if result.Response == nil {
			if logger != nil {
				logger.Debug("Webhook readiness probe returned no admission response")
			}
			return false, nil
		}
		if result.Response.UID != review.Request.UID {
			if logger != nil {
				logger.Debugf("Webhook readiness probe returned UID %q, expected %q", result.Response.UID, review.Request.UID)
			}
			return false, nil
		}
		if !result.Response.Allowed && logger != nil {
			logger.Debugf("Webhook readiness probe was denied: %+v", result.Response.Result)
		}
		return result.Response.Allowed, nil
	})

	return waiter.New[bool]().
		WithTimeout(timeout).
		WithInterval(interval).
		WithLogger(logger).
		WaitUntil(ctx, fetch, func(ready bool) bool {
			if !ready {
				consecutiveSuccesses = 0
				return false
			}
			consecutiveSuccesses++
			return consecutiveSuccesses >= webhookReadySuccessCount
		})
}

// chartYAML represents the structure of Chart.yaml for version extraction
type chartYAML struct {
	Version string `yaml:"version"`
}

// GetGroveChartVersion reads the version from Chart.yaml in the given chart directory.
// The chartDir parameter should be the path to a Helm chart directory. Chart.yaml is
// a required file per the Helm chart specification and will always exist for valid charts.
// We read from Chart.yaml rather than hardcoding the version to maintain a single source
// of truth, avoiding configuration drift between the chart definition and the e2e test code.
func GetGroveChartVersion(chartDir string) (string, error) {
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
	rootDir, err := GetOperatorRootDir()
	if err != nil {
		return "", err
	}
	// This file is at operator/e2e/setup/grove.go
	// Chart directory is at operator/charts
	return filepath.Join(rootDir, "charts"), nil
}

// GetOperatorRootDir returns the absolute path to the Grove operator directory.
// It uses runtime.Caller to find the path relative to this source file.
func GetOperatorRootDir() (string, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("failed to get current file path")
	}
	// This file is at operator/e2e/setup/grove.go
	rootDir := filepath.Join(filepath.Dir(currentFile), "../../")
	return filepath.Abs(rootDir)
}
