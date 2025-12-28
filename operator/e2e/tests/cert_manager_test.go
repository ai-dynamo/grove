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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func TestCertManagerIntegration(t *testing.T) {
	// 1. Prepare cluster
	ctx := context.Background()
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 1)
	defer cleanup()

	// 2. Install cert-manager
	deps, err := e2e.GetDependencies()
	if err != nil {
		t.Fatalf("Failed to get dependencies: %v", err)
	}

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

	logger.Info("Installing cert-manager...")
	if _, err := setup.InstallHelmChart(cmConfig); err != nil {
		t.Fatalf("Failed to install cert-manager: %v", err)
	}

	// Wait for cert-manager pods to be ready
	if err := utils.WaitForPodsInNamespace(ctx, cmConfig.Namespace, restConfig, 3, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("cert-manager pods not ready: %v", err)
	}

	// 3. Create Issuer and Certificate
	logger.Info("Creating ClusterIssuer and Certificate...")
	if _, err := utils.ApplyYAMLData(ctx, []byte(certManagerIssuerYAML), "", restConfig, logger); err != nil {
		t.Fatalf("Failed to apply ClusterIssuer: %v", err)
	}

	// Wait a bit for ClusterIssuer
	time.Sleep(5 * time.Second)

	if _, err := utils.ApplyYAMLData(ctx, []byte(certManagerCertificateYAML), "", restConfig, logger); err != nil {
		t.Fatalf("Failed to apply Certificate: %v", err)
	}

	// 4. Wait for Secret to be created
	logger.Info("Waiting for certificate secret to be created...")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		_, err := clientset.CoreV1().Secrets("grove-system").Get(ctx, "grove-webhook-server-cert", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("Certificate secret was not created: %v", err)
	}

	// 5. Update Grove to use the external secret via Helm upgrade
	logger.Info("Upgrading Grove to use cert-manager secret...")

	// Get the path to the charts directory relative to this source file
	_, currentFile, _, _ := runtime.Caller(0)
	chartPath := filepath.Join(filepath.Dir(currentFile), "../../charts")
	logger.Infof("Using chart path: %s", chartPath)

	// Delete the old auto-provisioned webhook secret if it exists
	logger.Info("Deleting old webhook secret if exists...")
	_ = clientset.CoreV1().Secrets("grove-system").Delete(ctx, "grove-webhook-server-cert", metav1.DeleteOptions{})

	groveUpgradeConfig := &setup.HelmInstallConfig{
		RestConfig:   restConfig,
		ReleaseName:  "grove-operator",
		ChartRef:     chartPath,
		ChartVersion: "0.1.0-dev",
		Namespace:    "grove-system",
		ReuseValues:  true, // Reuse existing values (like image config)
		Values: map[string]interface{}{
			"config": map[string]interface{}{
				"server": map[string]interface{}{
					"webhooks": map[string]interface{}{
						"autoProvision": false,
						"secretName":    "grove-webhook-server-cert",
					},
				},
			},
			"webhooks": map[string]interface{}{
				"podCliqueSetValidationWebhook": map[string]interface{}{
					"annotations": map[string]interface{}{
						"cert-manager.io/inject-ca-from": "grove-system/grove-webhook-server-cert",
					},
				},
				"podCliqueSetDefaultingWebhook": map[string]interface{}{
					"annotations": map[string]interface{}{
						"cert-manager.io/inject-ca-from": "grove-system/grove-webhook-server-cert",
					},
				},
				"clusterTopologyValidationWebhook": map[string]interface{}{
					"annotations": map[string]interface{}{
						"cert-manager.io/inject-ca-from": "grove-system/grove-webhook-server-cert",
					},
				},
				"authorizerWebhook": map[string]interface{}{
					"annotations": map[string]interface{}{
						"cert-manager.io/inject-ca-from": "grove-system/grove-webhook-server-cert",
					},
				},
			},
		},
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	logger.Info("Starting Helm upgrade...")
	if _, err := setup.UpgradeHelmChart(groveUpgradeConfig); err != nil {
		pods, podErr := clientset.CoreV1().Pods("grove-system").List(ctx, metav1.ListOptions{})
		if podErr == nil {
			logger.Infof("Found %d pods in grove-system:", len(pods.Items))
			for _, pod := range pods.Items {
				logger.Debugf("  - %s: %s (Ready: %v)", pod.Name, pod.Status.Phase, pod.Status.Conditions)
			}
		}
		t.Fatalf("Failed to upgrade Grove: %v", err)
	}

	// Wait for Grove operator pod to be ready after restart
	if err := utils.WaitForPodsInNamespace(ctx, "grove-system", restConfig, 1, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Grove operator pod not ready after upgrade: %v", err)
	}
	logger.Info("✅ Grove upgraded successfully")

	// 6. Deploy a workload to verify webhooks
	logger.Info("Deploying workload to verify webhooks...")

	// Get the path to the workload YAML file relative to this source file
	workloadPath := filepath.Join(filepath.Dir(currentFile), "../yaml/workload1.yaml")
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload1", // Matches metadata.name in yaml
			YAMLPath:     workloadPath,
			Namespace:    "default",
			ExpectedPods: 10, // 2+2+6
		},
	}

	if _, err := deployAndVerifyWorkload(tc); err != nil {
		t.Fatalf("Failed to verify workload: %v", err)
	}

	logger.Info("✅ Auto-Provision to cert-manager test passed!")

	logger.Info("🔄 Starting transition back to Auto-Provision mode...")

	// 7. Delete the Cert-Manager Secret and Certificate to force re-generation
	logger.Info("Deleting cert-manager assets to force operator fallback...")
	_ = clientset.CoreV1().Secrets("grove-system").Delete(ctx, "grove-webhook-server-cert", metav1.DeleteOptions{})

	certGVR := schema.GroupVersionResource{
		Group:    "cert-manager.io",
		Version:  "v1",
		Resource: "certificates",
	}
	_ = dynamicClient.Resource(certGVR).Namespace("grove-system").Delete(ctx, "grove-webhook-server-cert", metav1.DeleteOptions{})

	// 8. Delete the Issuer
	issuerGVR := schema.GroupVersionResource{
		Group:    "cert-manager.io",
		Version:  "v1",
		Resource: "clusterissuers",
	}
	_ = dynamicClient.Resource(issuerGVR).Delete(ctx, "selfsigned-issuer", metav1.DeleteOptions{})

	logger.Info("Waiting for secret to be fully deleted from the API server...")
	err = utils.PollForCondition(ctx, 30*time.Second, 1*time.Second, func() (bool, error) {
		_, err := clientset.CoreV1().Secrets("grove-system").Get(ctx, "grove-webhook-server-cert", metav1.GetOptions{})
		if err != nil {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Timeout waiting for secret deletion: %v", err)
	}

	logger.Info("Secret is gone. Proceeding with Helm upgrade...")

	// 9. Upgrade Grove to turn Auto-Provision back ON
	groveAutoProvisionConfig := &setup.HelmInstallConfig{
		RestConfig:   restConfig,
		ReleaseName:  "grove-operator",
		ChartRef:     chartPath,
		ChartVersion: "0.1.0-dev",
		Namespace:    "grove-system",
		ReuseValues:  true,
		Values: map[string]interface{}{
			"installCRDs": true,
			"config": map[string]interface{}{
				"server": map[string]interface{}{
					"webhooks": map[string]interface{}{
						"autoProvision": true,
						"secretName":    "grove-webhook-server-cert",
					},
				},
			},
			"webhooks": map[string]interface{}{
				"podCliqueSetValidationWebhook":    map[string]interface{}{"annotations": map[string]interface{}{"cert-manager.io/inject-ca-from": nil}},
				"podCliqueSetDefaultingWebhook":    map[string]interface{}{"annotations": map[string]interface{}{"cert-manager.io/inject-ca-from": nil}},
				"clusterTopologyValidationWebhook": map[string]interface{}{"annotations": map[string]interface{}{"cert-manager.io/inject-ca-from": nil}},
				"authorizerWebhook":                map[string]interface{}{"annotations": map[string]interface{}{"cert-manager.io/inject-ca-from": nil}},
			},
		},
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	logger.Info("Upgrading Grove to restore auto-provisioning...")
	if _, err := setup.UpgradeHelmChart(groveAutoProvisionConfig); err != nil {
		t.Fatalf("Failed to revert to auto-provision: %v", err)
	}

	// 10. Wait for Grove to provision its own secret
	logger.Info("Waiting for Grove to auto-provision a new secret...")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		s, err := clientset.CoreV1().Secrets("grove-system").Get(ctx, "grove-webhook-server-cert", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		// Verify it has actual data
		return len(s.Data["tls.crt"]) > 0, nil
	})
	if err != nil {
		t.Fatalf("Grove failed to recover and auto-provision secret: %v", err)
	}

	// 11. Delete old workload
	logger.Info("Cleaning up old workload...")
	deleteGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	_ = dynamicClient.Resource(deleteGVR).Namespace("default").Delete(ctx, "workload1", metav1.DeleteOptions{})

	utils.PollForCondition(ctx, 20*time.Second, 1*time.Second, func() (bool, error) {
		_, err := dynamicClient.Resource(deleteGVR).Namespace("default").Get(ctx, "workload1", metav1.GetOptions{})
		return err != nil, nil // Success if error (NotFound)
	})

	// 12. Redeploy workload to verify everything works end-to-end
	logger.Info("Deploying new workload for cert-manager to autoprovision flow")
	if _, err := deployAndVerifyWorkload(tc); err != nil {
		t.Fatalf("Failed to verify workload: %v", err)
	}

	logger.Info("✅ Full round-trip cert integration test passed!")
}
