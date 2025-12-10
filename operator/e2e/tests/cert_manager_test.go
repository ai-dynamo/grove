//go:build e2e

package tests

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		Wait:            true,
		Timeout:         10 * time.Minute, // cert-manager needs time to download images and start
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

	// Wait for cert-manager webhook to be ready (InstallHelmChart with Wait=true should handle it, but to be safe)
	if err := utils.WaitForPodsInNamespace(ctx, cmConfig.Namespace, restConfig, 3, 2*time.Minute, 5*time.Second, logger); err != nil {
		t.Fatalf("cert-manager pods not ready: %v", err)
	}

	// 3. Create Issuer and Certificate
	logger.Info("Creating ClusterIssuer and Certificate...")
	if err := applyYAMLContent(ctx, restConfig, certManagerIssuerYAML); err != nil {
		t.Fatalf("Failed to apply ClusterIssuer: %v", err)
	}

	// Wait a bit for ClusterIssuer
	time.Sleep(5 * time.Second)

	if err := applyYAMLContent(ctx, restConfig, certManagerCertificateYAML); err != nil {
		t.Fatalf("Failed to apply Certificate: %v", err)
	}

	// 4. Wait for Secret to be created
	logger.Info("Waiting for certificate secret to be created...")
	err = utils.PollForCondition(ctx, 1*time.Minute, 2*time.Second, func() (bool, error) {
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

	chartPath := "../../charts"
	logger.Infof("Using chart path: %s", chartPath)

	// Delete the old auto-provisioned webhook secret if it exists
	logger.Info("Deleting old webhook secret if exists...")
	_ = clientset.CoreV1().Secrets("grove-system").Delete(ctx, "grove-webhook-server-cert", metav1.DeleteOptions{})
	time.Sleep(30 * time.Second)

	groveUpgradeConfig := &setup.HelmInstallConfig{
		RestConfig:   restConfig,
		ReleaseName:  "grove-operator",
		ChartRef:     chartPath,
		ChartVersion: "0.1.0-dev",
		Namespace:    "grove-system",
		Wait:         true,
		Timeout:      10 * time.Minute, // Allow time for pod restart
		ReuseValues:  true,             // Reuse existing values (like image config)
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

	logger.Info("Starting Helm upgrade (this may take several minutes)...")
	if _, err := setup.UpgradeHelmChart(groveUpgradeConfig); err != nil {
		// Get more info about what went wrong
		logger.Error("Helm upgrade failed, checking Grove pods status...")
		pods, podErr := clientset.CoreV1().Pods("grove-system").List(ctx, metav1.ListOptions{})
		if podErr == nil {
			logger.Infof("Found %d pods in grove-system:", len(pods.Items))
			for _, pod := range pods.Items {
				logger.Infof("  - %s: %s (Ready: %v)", pod.Name, pod.Status.Phase, pod.Status.Conditions)
			}
		}
		t.Fatalf("Failed to upgrade Grove: %v", err)
	}

	time.Sleep(30 * time.Second)
	logger.Info("✅ Grove upgraded successfully")

	// 6. Deploy a workload to verify webhooks
	logger.Info("Deploying workload to verify webhooks...")

	workloadPath := "../yaml/workload1.yaml"
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       2 * time.Minute,
		Interval:      5 * time.Second,
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

// helper to apply YAML string
func applyYAMLContent(ctx context.Context, restConfig *rest.Config, content string) error {
	tmpfile, err := os.CreateTemp("", "manifest-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.WriteString(content); err != nil {
		return err
	}
	if err := tmpfile.Close(); err != nil {
		return err
	}

	_, err = utils.ApplyYAMLFile(ctx, tmpfile.Name(), "", restConfig, logger)
	return err
}
