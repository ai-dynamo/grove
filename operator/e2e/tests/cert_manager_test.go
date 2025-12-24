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

	logger.Info("✅ Cert-manager integration test passed!")
}
