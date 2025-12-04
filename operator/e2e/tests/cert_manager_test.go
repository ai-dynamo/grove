//go:build e2e

package tests

import (
	"context"
	"os"
	"path/filepath"
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
	// We need enough nodes for workload1 (10 pods)
	ctx := context.Background()
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
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

	// 5. Update Grove to use the external secret
	logger.Info("Upgrading Grove to use cert-manager secret...")

	// Resolve chart path relative to this test file
	// We assume we are in operator/e2e/tests
	// Chart is in operator/charts -> ../../charts
	wd, _ := os.Getwd()
	logger.Debugf("Current working directory: %s", wd)
	
	// If running from root, path is operator/charts
	// If running from operator/e2e/tests, path is ../../charts
	// Let's try to find it.
	chartPath := "../../charts"
	if _, err := os.Stat(chartPath); os.IsNotExist(err) {
		// Try absolute path or other relative path if test running from elsewhere
		// Assuming standard structure
		chartPath = "../../../operator/charts" // If running from deep inside? No.
		// Try absolute path based on known structure
		// /root/kang_1/grove/operator/charts
		chartPath = "/root/kang_1/grove/operator/charts"
	}
	
	absChartPath, _ := filepath.Abs(chartPath)
	logger.Infof("Using chart path: %s", absChartPath)

	groveUpgradeConfig := &setup.HelmInstallConfig{
		RestConfig:   restConfig,
		ReleaseName:  "grove-operator",
		ChartRef:     absChartPath,
		ChartVersion: "0.1.0-dev", // Use dummy version or match current
		Namespace:    "grove-system",
		Wait:         true,
		ReuseValues:  true, // Keep existing values (image, etc.)
		Values: map[string]interface{}{
			"config": map[string]interface{}{
				"server": map[string]interface{}{
					"webhooks": map[string]interface{}{
						"autoProvision": false,
						"secretName":    "grove-webhook-server-cert",
					},
				},
			},
		},
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	if _, err := setup.UpgradeHelmChart(groveUpgradeConfig); err != nil {
		t.Fatalf("Failed to upgrade Grove: %v", err)
	}

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

