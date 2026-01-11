//go:build e2e

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

package tests

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	certManagerIssuerYAML = `
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: selfsigned-issuer
spec:
  selfSigned: {}
`
	certManagerCertificateYAML = `
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: grove-webhook-server-cert
  namespace: grove-system
spec:
  secretName: grove-webhook-server-cert
  duration: 2160h # 90d
  renewBefore: 360h # 15d
  issuerRef:
    name: selfsigned-issuer
    kind: ClusterIssuer
  dnsNames:
  - grove-operator
  - grove-operator.grove-system
  - grove-operator.grove-system.svc
  - grove-operator.grove-system.svc.cluster.local
`
)

// Test_CM1_AutoProvisionToCertManager tests transitioning from auto-provisioned certs to cert-manager
// Scenario CM-1:
// 1. Initialize Grove with auto-provision mode
// 2. Install cert-manager and create Certificate
// 3. Upgrade Grove to use cert-manager (autoProvision=false)
// 4. Deploy and verify workload with cert-manager certs
func Test_CM1_AutoProvisionToCertManager(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize Grove with auto-provision mode")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 1)

	logger.Info("2. Install cert-manager and create Certificate")
	deps, _ := e2e.GetDependencies()
	installCertManager(t, ctx, restConfig, deps)
	defer uninstallCertManager(t, restConfig, deps)
	defer cleanup()

	// Create Issuer and Certificate
	if _, err := utils.ApplyYAMLData(ctx, []byte(certManagerIssuerYAML), "", restConfig, logger); err != nil {
		t.Fatalf("Failed to apply ClusterIssuer: %v", err)
	}
	waitForClusterIssuer(t, ctx, dynamicClient, "selfsigned-issuer")

	if _, err := utils.ApplyYAMLData(ctx, []byte(certManagerCertificateYAML), "", restConfig, logger); err != nil {
		t.Fatalf("Failed to apply Certificate: %v", err)
	}

	// Wait for Secret to be created by Cert-Manager
	waitForSecret(t, ctx, clientset, "grove-webhook-server-cert", true)

	logger.Info("3. Upgrade Grove to use cert-manager (autoProvision=false)")
	_, currentFile, _, _ := runtime.Caller(0)
	chartPath := filepath.Join(filepath.Dir(currentFile), "../../charts")
	upgradeGrove(t, ctx, clientset, chartPath, restConfig, false) // false = autoProvision off

	logger.Info("4. Deploy and verify workload with cert-manager certs")
	tc := createTestContext(t, ctx, clientset, dynamicClient, restConfig)
	if _, err := deployAndVerifyWorkload(tc); err != nil {
		t.Fatalf("Failed to verify workload in Cert-Manager mode: %v", err)
	}

	logger.Info("🎉 Auto-Provision to Cert-Manager transition test completed successfully!")
}

// Test_CM2_CertManagerToAutoProvision tests transitioning from cert-manager back to auto-provision
// Scenario CM-2:
// 1. Initialize Grove with cert-manager
// 2. Upgrade Grove to cert-manager mode and verify
// 3. Remove cert-manager resources
// 4. Upgrade Grove back to auto-provision mode
// 5. Deploy and verify workload with auto-provisioned certs
func Test_CM2_CertManagerToAutoProvision(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize Grove with cert-manager")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 1)

	// Install cert-manager
	deps, _ := e2e.GetDependencies()
	_, currentFile, _, _ := runtime.Caller(0)
	chartPath := filepath.Join(filepath.Dir(currentFile), "../../charts")

	installCertManager(t, ctx, restConfig, deps)
	defer uninstallCertManager(t, restConfig, deps)
	defer cleanup()

	// Create Issuer and Certificate
	if _, err := utils.ApplyYAMLData(ctx, []byte(certManagerIssuerYAML), "", restConfig, logger); err != nil {
		t.Fatalf("Failed to apply ClusterIssuer: %v", err)
	}
	waitForClusterIssuer(t, ctx, dynamicClient, "selfsigned-issuer")

	if _, err := utils.ApplyYAMLData(ctx, []byte(certManagerCertificateYAML), "", restConfig, logger); err != nil {
		t.Fatalf("Failed to apply Certificate: %v", err)
	}

	logger.Info("2. Upgrade Grove to cert-manager mode and verify")
	upgradeGrove(t, ctx, clientset, chartPath, restConfig, false) // autoProvision = false
	waitForSecret(t, ctx, clientset, "grove-webhook-server-cert", true)

	logger.Info("3. Remove cert-manager resources")
	deleteCertManagerResources(ctx, clientset, dynamicClient)
	waitForSecret(t, ctx, clientset, "grove-webhook-server-cert", false)

	logger.Info("4. Upgrade Grove back to auto-provision mode")
	upgradeGrove(t, ctx, clientset, chartPath, restConfig, true)
	waitForSecret(t, ctx, clientset, "grove-webhook-server-cert", true)

	logger.Info("5. Deploy and verify workload with auto-provisioned certs")
	tc := createTestContext(t, ctx, clientset, dynamicClient, restConfig)
	if _, err := deployAndVerifyWorkload(tc); err != nil {
		t.Fatalf("Failed to verify workload after reverting to Auto-Provision: %v", err)
	}

	logger.Info("🎉 Cert-Manager to Auto-Provision transition test completed successfully!")
}

// upgradeGrove handles the Helm upgrade for both tests
func upgradeGrove(t *testing.T, ctx context.Context, clientset *kubernetes.Clientset, chartPath string, restConfig *rest.Config, autoProvision bool) {
	t.Helper()

	annotations := map[string]interface{}{"cert-manager.io/inject-ca-from": nil}
	if !autoProvision {
		annotations = map[string]interface{}{"cert-manager.io/inject-ca-from": "grove-system/grove-webhook-server-cert"}
	}

	config := &setup.HelmInstallConfig{
		RestConfig:   restConfig,
		ReleaseName:  "grove-operator",
		ChartRef:     chartPath,
		ChartVersion: "0.1.0-dev",
		Namespace:    "grove-system",
		ReuseValues:  true, // Reuse existing values (like image config)
		Values: map[string]interface{}{
			"installCRDs": true,
			"config": map[string]interface{}{
				"server": map[string]interface{}{
					"webhooks": map[string]interface{}{
						"autoProvision": autoProvision,
						"secretName":    "grove-webhook-server-cert",
					},
				},
			},
			"webhooks": map[string]interface{}{
				"podCliqueSetValidationWebhook":    map[string]interface{}{"annotations": annotations},
				"podCliqueSetDefaultingWebhook":    map[string]interface{}{"annotations": annotations},
				"clusterTopologyValidationWebhook": map[string]interface{}{"annotations": annotations},
				"authorizerWebhook":                map[string]interface{}{"annotations": annotations},
			},
		},
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	if _, err := setup.UpgradeHelmChart(config); err != nil {
		pods, podErr := clientset.CoreV1().Pods("grove-system").List(ctx, metav1.ListOptions{})
		if podErr == nil {
			logger.Infof("Found %d pods in grove-system:", len(pods.Items))
			for _, pod := range pods.Items {
				logger.Debugf("  - %s: %s (Ready: %v)", pod.Name, pod.Status.Phase, pod.Status.Conditions)
			}
		}
		t.Fatalf("Failed to upgrade Grove: %v", err)
	}

	// Wait for Grove operator pod to be ready after upgrade
	if err := utils.WaitForPodsInNamespace(ctx, "grove-system", restConfig, 1, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Grove operator pod not ready after upgrade: %v", err)
	}
}

func waitForSecret(t *testing.T, ctx context.Context, clientset *kubernetes.Clientset, name string, shouldExist bool) {
	t.Helper()

	err := utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		_, err := clientset.CoreV1().Secrets("grove-system").Get(ctx, name, metav1.GetOptions{})
		if shouldExist {
			return err == nil, nil
		}
		return errors.IsNotFound(err), nil
	})
	if err != nil {
		t.Fatalf("Timeout waiting for secret %s (shouldExist=%v): %v", name, shouldExist, err)
	}
}

func deleteCertManagerResources(ctx context.Context, clientset *kubernetes.Clientset, dynamicClient dynamic.Interface) {
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	issuerGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}

	// Delete Certificate first (cert-manager resource)
	if err := dynamicClient.Resource(certGVR).Namespace("grove-system").Delete(ctx, "grove-webhook-server-cert", metav1.DeleteOptions{}); err != nil {
		logger.Warnf("Failed to delete Certificate (may not exist): %v", err)
	}

	// Delete the Secret managed by cert-manager
	if err := clientset.CoreV1().Secrets("grove-system").Delete(ctx, "grove-webhook-server-cert", metav1.DeleteOptions{}); err != nil {
		logger.Warnf("Failed to delete Secret (may not exist): %v", err)
	}

	// Delete ClusterIssuer last
	if err := dynamicClient.Resource(issuerGVR).Delete(ctx, "selfsigned-issuer", metav1.DeleteOptions{}); err != nil {
		logger.Warnf("Failed to delete ClusterIssuer (may not exist): %v", err)
	}
}

func installCertManager(t *testing.T, ctx context.Context, restConfig *rest.Config, deps *e2e.Dependencies) {
	t.Helper()

	cmConfig := &setup.HelmInstallConfig{
		RestConfig:      restConfig,
		ReleaseName:     deps.HelmCharts.CertManager.ReleaseName,
		ChartRef:        deps.HelmCharts.CertManager.ChartRef,
		ChartVersion:    deps.HelmCharts.CertManager.Version,
		Namespace:       deps.HelmCharts.CertManager.Namespace,
		CreateNamespace: true,
		RepoURL:         deps.HelmCharts.CertManager.RepoURL,
		Values: map[string]interface{}{
			"installCRDs": true,
		},
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	if _, err := setup.InstallHelmChart(cmConfig); err != nil {
		t.Fatalf("Failed to install cert-manager: %v", err)
	}

	// Ensure cert-manager is actually up and running before returning
	if err := utils.WaitForPodsInNamespace(ctx, cmConfig.Namespace, restConfig, 3, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("cert-manager pods failed to become ready: %v", err)
	}
}

func createTestContext(t *testing.T, ctx context.Context, clientset *kubernetes.Clientset, dynamicClient dynamic.Interface, restConfig *rest.Config) TestContext {
	t.Helper()

	_, currentFile, _, _ := runtime.Caller(0)
	workloadPath := filepath.Join(filepath.Dir(currentFile), "../yaml/workload1.yaml")

	return TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     workloadPath,
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}
}

func waitForClusterIssuer(t *testing.T, ctx context.Context, dynamicClient dynamic.Interface, name string) {
	t.Helper()

	issuerGVR := schema.GroupVersionResource{
		Group:    "cert-manager.io",
		Version:  "v1",
		Resource: "clusterissuers",
	}

	err := utils.PollForCondition(ctx, 30*time.Second, 1*time.Second, func() (bool, error) {
		issuer, err := dynamicClient.Resource(issuerGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		return checkReadyStatus(issuer), nil
	})

	if err != nil {
		t.Fatalf("ClusterIssuer %s failed to become Ready: %v", name, err)
	}
}

func checkReadyStatus(obj *unstructured.Unstructured) bool {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}

	for _, c := range conditions {
		condition, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		if condition["type"] == "Ready" && condition["status"] == "True" {
			return true
		}
	}
	return false
}

func uninstallCertManager(t *testing.T, restConfig *rest.Config, deps *e2e.Dependencies) {
	t.Helper()

	cmConfig := &setup.HelmInstallConfig{
		RestConfig:     restConfig,
		ReleaseName:    deps.HelmCharts.CertManager.ReleaseName,
		Namespace:      deps.HelmCharts.CertManager.Namespace,
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	if err := setup.UninstallHelmChart(cmConfig); err != nil {
		logger.Warnf("Failed to uninstall cert-manager (may not exist): %v", err)
	}
}
