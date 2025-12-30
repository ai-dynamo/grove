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

//go:build e2e

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

func TestAutoProvisionToCertManager(t *testing.T) {
	// 1. Prepare cluster
	ctx := context.Background()
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 1)
	defer cleanup()

	// 2. Install cert-manager
	deps, _ := e2e.GetDependencies()

	logger.Info("Installing cert-manager...")
	installCertManager(t, ctx, restConfig, deps)

	// 3. Create Issuer and Certificate
	logger.Info("Creating ClusterIssuer and Certificate...")
	if _, err := utils.ApplyYAMLData(ctx, []byte(certManagerIssuerYAML), "", restConfig, logger); err != nil {
		t.Fatalf("Failed to apply ClusterIssuer: %v", err)
	}

	waitForClusterIssuer(t, ctx, dynamicClient, "selfsigned-issuer")

	if _, err := utils.ApplyYAMLData(ctx, []byte(certManagerCertificateYAML), "", restConfig, logger); err != nil {
		t.Fatalf("Failed to apply Certificate: %v", err)
	}

	// 4. Wait for Secret to be created by Cert-Manager
	waitForSecret(t, ctx, clientset, "grove-webhook-server-cert", true)

	// Delete the old auto-provisioned webhook secret if it exists
	_ = clientset.CoreV1().Secrets("grove-system").Delete(ctx, "grove-webhook-server-cert", metav1.DeleteOptions{})

	// 5. Upgrade Grove to External Mode
	logger.Info("Upgrading Grove to use external certs...")
	_, currentFile, _, _ := runtime.Caller(0)

	// Get the path to the charts directory relative to this source file
	chartPath := filepath.Join(filepath.Dir(currentFile), "../../charts")
	upgradeGrove(t, ctx, clientset, chartPath, restConfig, false) // false = autoProvision off

	// Wait for Grove operator pod to be ready after restart
	if err := utils.WaitForPodsInNamespace(ctx, "grove-system", restConfig, 1, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Grove operator pod not ready after upgrade: %v", err)
	}
	logger.Info("✅ Grove upgraded successfully")

	// 6. Deploy and Verify Workload
	tc := createTestContext(t, ctx, clientset, dynamicClient, restConfig)
	if _, err := deployAndVerifyWorkload(tc); err != nil {
		t.Fatalf("Failed to verify workload in Cert-Manager mode: %v", err)
	}
	logger.Info("✅ Auto-Provision to Cert-Manager transition successful")
}

func TestCertManagerToAutoProvision(t *testing.T) {
	// 1. Prepare cluster
	ctx := context.Background()
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 1)
	defer cleanup()

	// 2. Install cert-manager
	deps, _ := e2e.GetDependencies()
	_, currentFile, _, _ := runtime.Caller(0)
	chartPath := filepath.Join(filepath.Dir(currentFile), "../../charts")

	logger.Info("Installing cert-manager...")
	installCertManager(t, ctx, restConfig, deps)

	// 3. Create Issuer and Certificate
	logger.Info("Creating ClusterIssuer and Certificate...")
	if _, err := utils.ApplyYAMLData(ctx, []byte(certManagerIssuerYAML), "", restConfig, logger); err != nil {
		t.Fatalf("Failed to apply ClusterIssuer: %v", err)
	}

	waitForClusterIssuer(t, ctx, dynamicClient, "selfsigned-issuer")

	if _, err := utils.ApplyYAMLData(ctx, []byte(certManagerCertificateYAML), "", restConfig, logger); err != nil {
		t.Fatalf("Failed to apply Certificate: %v", err)
	}

	// 4. Wait for Secret to be created by Cert-Manager
	waitForSecret(t, ctx, clientset, "grove-webhook-server-cert", true)

	// 5. Upgrade Grove to External Mode
	upgradeGrove(t, ctx, clientset, chartPath, restConfig, false) // autoProvision = false
	waitForSecret(t, ctx, clientset, "grove-webhook-server-cert", true)

	// 6. Remove Cert-Manager assets before switching
	logger.Info("Purging Cert-Manager assets...")
	deleteCertManagerResources(ctx, clientset, dynamicClient)

	// 7. Wait for secret deletion to avoid Helm ownership errors
	waitForSecret(t, ctx, clientset, "grove-webhook-server-cert", false)

	// 8. Downgrade Grove to Auto-Provision Mode
	logger.Info("Upgrading Grove back to Auto-Provision mode...")
	upgradeGrove(t, ctx, clientset, chartPath, restConfig, true)

	// 9. Wait for Operator to generate its own secret
	waitForSecret(t, ctx, clientset, "grove-webhook-server-cert", true)

	// 10. Deploy and Verify via Workload
	tc := createTestContext(t, ctx, clientset, dynamicClient, restConfig)
	if _, err := deployAndVerifyWorkload(tc); err != nil {
		t.Fatalf("Failed to verify workload after reverting to Auto-Provision: %v", err)
	}

	logger.Info("✅ Cert-Manager to Auto-Provision transition successful")
}

// upgradeGrove handles the Helm upgrade for both tests
func upgradeGrove(t *testing.T, ctx context.Context, clientset *kubernetes.Clientset, chartPath string, restConfig *rest.Config, autoProvision bool) {
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

	logger.Info("Starting Helm upgrade...")
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
}

func waitForSecret(t *testing.T, ctx context.Context, clientset *kubernetes.Clientset, name string, shouldExist bool) {
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

	_ = dynamicClient.Resource(certGVR).Namespace("grove-system").Delete(ctx, "grove-webhook-server-cert", metav1.DeleteOptions{})
	_ = dynamicClient.Resource(issuerGVR).Delete(ctx, "selfsigned-issuer", metav1.DeleteOptions{})
	_ = clientset.CoreV1().Secrets("grove-system").Delete(ctx, "grove-webhook-server-cert", metav1.DeleteOptions{})
}

func installCertManager(t *testing.T, ctx context.Context, restConfig *rest.Config, deps *e2e.Dependencies) {
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

	logger.Info("Installing cert-manager via Helm...")
	if _, err := setup.InstallHelmChart(cmConfig); err != nil {
		t.Fatalf("Failed to install cert-manager: %v", err)
	}

	// Ensure cert-manager is actually up and running before returning
	logger.Info("Waiting for cert-manager pods to be ready...")
	if err := utils.WaitForPodsInNamespace(ctx, cmConfig.Namespace, restConfig, 3, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("cert-manager pods failed to become ready: %v", err)
	}
}

func createTestContext(t *testing.T, ctx context.Context, clientset *kubernetes.Clientset, dynamicClient dynamic.Interface, restConfig *rest.Config) TestContext {
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
	logger.Infof("Waiting for ClusterIssuer %s to be Ready...", name)

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
