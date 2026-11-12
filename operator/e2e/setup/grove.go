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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GroveConfig holds typed configuration options for updating the Grove operator.
// This struct provides a user-friendly interface that gets translated to Helm values internally.
type GroveConfig struct {
	// Webhooks contains webhook-specific configuration.
	Webhooks WebhooksConfig
	// ScaleIn names the operator's scale-in cost source. Empty leaves the chart default ("none") in place.
	ScaleIn configv1beta1.ScaleInConfiguration
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
	Config         helmConfigValues  `json:"config"`
	Webhooks       helmWebhookValues `json:"webhooks"`
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`
}

type helmConfigValues struct {
	Server  helmServerValues                   `json:"server"`
	ScaleIn configv1beta1.ScaleInConfiguration `json:"scaleIn"`
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

// toHelmValues converts GroveConfig to a Helm values map. podAnnotations lands on the operator pod template.
// Note: We must include all required fields for webhooks (port, serverCertDir) because
// Helm's merge behavior replaces entire nested objects rather than deep-merging.
// Without these, the port would be set to 0, causing Service validation errors.
func (c *GroveConfig) toHelmValues(podAnnotations map[string]string) (map[string]interface{}, error) {
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
			ScaleIn: c.ScaleIn,
		},
		Webhooks: helmWebhookValues{
			PodCliqueSetValidationWebhook:          anns,
			PodCliqueScalingGroupValidationWebhook: anns,
			PodCliqueValidationWebhook:             anns,
			PodCliqueSetDefaultingWebhook:          anns,
			ClusterTopologyValidationWebhook:       anns,
			AuthorizerWebhook:                      anns,
		},
		PodAnnotations: podAnnotations,
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
// The operator reads its configuration once, at startup, and a change to its ConfigMap alone does not roll
// the Deployment. Every update therefore stamps the operator pod template with the time of the update, so the
// upgrade rolls the operator pods, and returns only once no pod of the previous template is left and, when
// leader election is on, the leader lease is held by a pod of the new template. The next request a test sends
// is then handled by an operator running the new configuration and by no other.
//
// This approach avoids wasteful rebuilds while staying compatible with the Skaffold installation.
func UpdateGroveConfiguration(ctx context.Context, restConfig *rest.Config, chartDir string, config *GroveConfig, logger *log.Logger) error {
	chartVersion, err := GetGroveChartVersion(chartDir)
	if err != nil {
		return fmt.Errorf("failed to get chart version: %w", err)
	}

	configuredAt := time.Now().UTC().Format(time.RFC3339Nano)
	helmValues, err := config.toHelmValues(map[string]string{OperatorConfiguredAtAnnotation: configuredAt})
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

	k8sClient, err := k8sclient.New(restConfig)
	if err != nil {
		return fmt.Errorf("create k8s client for rollout wait: %w", err)
	}
	if err := waitForOperatorRollout(ctx, k8sClient, configuredAt); err != nil {
		return fmt.Errorf("grove operator not rolled onto the new configuration: %w", err)
	}

	logger.Debug("Grove configuration update completed successfully")
	return nil
}

// waitForOperatorRollout waits until the operator Deployment reports its rollout complete, every operator pod,
// terminating ones included, carries the configuredAt stamp, and the leader lease, when leader election is
// on, is held by one of those pods. A terminating pod of the previous template still runs its controllers
// until it is gone, so the wait does not end while one exists.
func waitForOperatorRollout(ctx context.Context, cl client.Client, configuredAt string) error {
	var lastState string
	err := wait.PollUntilContextTimeout(ctx, defaultPollInterval, defaultPollTimeout, true, func(ctx context.Context) (bool, error) {
		deployment := &appsv1.Deployment{}
		if err := cl.Get(ctx, client.ObjectKey{Namespace: OperatorNamespace, Name: OperatorDeploymentName}, deployment); err != nil {
			return false, err
		}
		if deployment.Spec.Template.Annotations[OperatorConfiguredAtAnnotation] != configuredAt {
			lastState = "the operator pod template does not carry the new stamp"
			return false, nil
		}
		want := ptr.Deref(deployment.Spec.Replicas, 1)
		status := deployment.Status
		if status.ObservedGeneration < deployment.Generation || status.UpdatedReplicas != want || status.Replicas != want || status.AvailableReplicas != want {
			lastState = fmt.Sprintf("rollout in progress: generation %d observed %d, %d of %d replicas updated, %d available",
				deployment.Generation, status.ObservedGeneration, status.UpdatedReplicas, want, status.AvailableReplicas)
			return false, nil
		}

		podList := &corev1.PodList{}
		if err := cl.List(ctx, podList, client.InNamespace(OperatorNamespace), OperatorPodLabels); err != nil {
			return false, err
		}
		currentPods := sets.New[string]()
		for i := range podList.Items {
			pod := &podList.Items[i]
			if pod.Annotations[OperatorConfiguredAtAnnotation] != configuredAt {
				lastState = fmt.Sprintf("operator pod %s of the previous template still exists", pod.Name)
				return false, nil
			}
			currentPods.Insert(pod.Name)
		}
		if currentPods.Len() == 0 {
			lastState = "no operator pod of the new template yet"
			return false, nil
		}

		lease := &coordinationv1.Lease{}
		if err := cl.Get(ctx, client.ObjectKey{Namespace: OperatorNamespace, Name: OperatorLeaderElectionLeaseName}, lease); err != nil {
			if apierrors.IsNotFound(err) {
				// Leader election is off: every operator pod runs its controllers, and only pods of the new template are left.
				return true, nil
			}
			return false, err
		}
		// controller-runtime names the holder "<pod name>_<random id>".
		holderPod, _, _ := strings.Cut(ptr.Deref(lease.Spec.HolderIdentity, ""), "_")
		if !currentPods.Has(holderPod) {
			lastState = fmt.Sprintf("leader lease held by %q, not by a pod of the new template", holderPod)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w", lastState, err)
	}
	return nil
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
