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
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"testing"
	"time"


	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/resources"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// certManagerGroveConfig returns Grove configuration for cert-manager mode.
// This uses manual cert provisioning and configures webhook annotations for cert-manager CA injection.
func certManagerGroveConfig() *setup.GroveConfig {
	return &setup.GroveConfig{
		Webhooks: setup.WebhooksConfig{
			CertProvisionMode: configv1alpha1.CertProvisionModeManual,
			SecretName:        configv1alpha1.DefaultWebhookSecretName,
			Annotations: map[string]string{
				"cert-manager.io/inject-ca-from": "grove-system/" + configv1alpha1.DefaultWebhookSecretName,
			},
		},
	}
}

// autoProvisionGroveConfig returns Grove configuration for auto-provision mode (default).
// Note: Annotations are intentionally omitted - the Helm templates only include annotations
// when certProvisionMode is "manual", so any previous cert-manager annotations are automatically
// cleared when switching back to auto mode.
func autoProvisionGroveConfig() *setup.GroveConfig {
	return &setup.GroveConfig{
		Webhooks: setup.WebhooksConfig{
			CertProvisionMode: configv1alpha1.CertProvisionModeAuto,
			SecretName:        configv1alpha1.DefaultWebhookSecretName,
		},
	}
}

// updateGroveToCertManager updates Grove to use cert-manager for certificate management.
func updateGroveToCertManager(t *testing.T, ctx context.Context, restConfig *rest.Config) {
	t.Helper()
	chartDir, err := setup.GetGroveChartDir()
	if err != nil {
		t.Fatalf("Failed to get Grove chart directory: %v", err)
	}
	if err := setup.UpdateGroveConfiguration(ctx, restConfig, chartDir, certManagerGroveConfig(), Logger); err != nil {
		t.Fatalf("Failed to update Grove to cert-manager mode: %v", err)
	}
}

// updateGroveToAutoProvision updates Grove to use auto-provisioned certificates.
func updateGroveToAutoProvision(t *testing.T, ctx context.Context, restConfig *rest.Config) {
	t.Helper()
	chartDir, err := setup.GetGroveChartDir()
	if err != nil {
		t.Fatalf("Failed to get Grove chart directory: %v", err)
	}
	if err := setup.UpdateGroveConfiguration(ctx, restConfig, chartDir, autoProvisionGroveConfig(), Logger); err != nil {
		t.Fatalf("Failed to update Grove to auto-provision mode: %v", err)
	}
}

const (
	// Cert-manager Helm chart configuration
	// NOTE: This version must match the version in operator/hack/dependencies.yaml
	// to ensure image pre-pulling works correctly in E2E tests
	certManagerReleaseName = "cert-manager"
	certManagerChartRef    = "cert-manager"
	certManagerVersion     = "v1.14.4" // Keep in sync with dependencies.yaml
	certManagerNamespace   = "cert-manager"
	certManagerRepoURL     = "https://charts.jetstack.io"

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

// Test_CM1_CertManagementRoundTrip tests the full certificate management round-trip:
// auto-provision -> cert-manager -> auto-provision
// Scenario CM-1:
// 1. Initialize Grove with auto-provision mode
// 2. Install cert-manager and create Certificate
// 3. Upgrade Grove to use cert-manager (certProvisionMode=manual)
// 4. Verify cert-manager mode is active
// 5. Deploy and verify workload with cert-manager certs
// 6. Remove cert-manager resources
// 7. Upgrade Grove back to auto-provision mode
// 8. Verify auto-provision mode is active
// 9. Delete and redeploy workload to test webhooks with auto-provisioned certs
func Test_CM1_CertManagementRoundTrip(t *testing.T) {
	ctx := context.Background()

	_, currentFile, _, _ := runtime.Caller(0)
	workloadPath := filepath.Join(filepath.Dir(currentFile), "../yaml/workload1.yaml")

	Logger.Info("1. Initialize Grove with auto-provision mode (10 nodes for workload)")
	tc, cleanup := testctx.PrepareTest(ctx, t, 10,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     workloadPath,
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)

	Logger.Info("2. Install cert-manager and create Certificate")
	installCertManager(t, ctx, tc)
	defer uninstallCertManager(t, tc.Client.RestConfig)
	defer cleanup()

	// Create Issuer and Certificate using pre-created clients from prepareTest
	resources := resources.NewResourceManager(tc.Client, Logger)

	if _, err := resources.ApplyYAMLData(ctx, []byte(certManagerIssuerYAML), ""); err != nil {
		t.Fatalf("Failed to apply ClusterIssuer: %v", err)
	}
	waitForClusterIssuer(t, ctx, tc.Client, "selfsigned-issuer")

	if _, err := resources.ApplyYAMLData(ctx, []byte(certManagerCertificateYAML), ""); err != nil {
		t.Fatalf("Failed to apply Certificate: %v", err)
	}

	// Wait for cert-manager to actually take over the secret.
	// This is critical because the secret may already exist from auto-provision mode,
	// and we need to wait for cert-manager to update it (not just check existence).
	waitForSecretManagedByCertManager(t, ctx, tc.Client, configv1alpha1.DefaultWebhookSecretName)

	Logger.Info("3. Upgrade Grove to use cert-manager (certProvisionMode=manual)")
	updateGroveToCertManager(t, ctx, tc.Client.RestConfig)

	Logger.Info("4. Verify cert-manager mode is active")
	verifyCertManagerMode(t, ctx, tc.Client)
	verifyWebhookServingCertificate(t, ctx, tc.Client)

	Logger.Info("5. Deploy and verify workload with cert-manager certs")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload in Cert-Manager mode: %v", err)
	}

	// Wait for all pods to become ready to verify the webhook is working end-to-end
	if err := tc.WaitForPods(tc.Workload.ExpectedPods); err != nil {
		t.Fatalf("Failed to wait for workload pods to be ready: %v", err)
	}

	Logger.Info("6. Remove cert-manager resources")
	deleteCertManagerResources(ctx, tc.Client)
	waitForSecret(t, ctx, tc.Client, configv1alpha1.DefaultWebhookSecretName, false)

	Logger.Info("7. Upgrade Grove back to auto-provision mode")
	updateGroveToAutoProvision(t, ctx, tc.Client.RestConfig)
	waitForSecret(t, ctx, tc.Client, configv1alpha1.DefaultWebhookSecretName, true)

	Logger.Info("8. Verify auto-provision mode is active")
	verifyAutoProvisionMode(t, ctx, tc.Client)
	verifyWebhookServingCertificate(t, ctx, tc.Client)

	Logger.Info("9. Delete and redeploy workload to test webhooks with auto-provisioned certs")
	// Delete the existing workload to test that webhooks work with new certs
	deletePodCliqueSetAndWait(t, ctx, tc, tc.Workload.Name, tc.Namespace)

	// Redeploy workload - this will exercise the validating and mutating webhooks
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload with auto-provisioned certs: %v", err)
	}

	// Wait for all pods to become ready
	if err := tc.WaitForPods(tc.Workload.ExpectedPods); err != nil {
		t.Fatalf("Workload pods not ready after redeploying with auto-provisioned certs: %v", err)
	}

	Logger.Info("🎉 Certificate management round-trip test completed successfully")
}

func deletePodCliqueSetAndWait(t *testing.T, ctx context.Context, tc *testctx.TestContext, name, namespace string) {
	t.Helper()

	Logger.Debugf("Deleting PodCliqueSet %s/%s", namespace, name)
	workloads := workload.NewWorkloadManager(tc.Client, Logger)
	if err := workloads.DeletePCSAndWait(ctx, namespace, name, testctx.DefaultPollTimeout, testctx.DefaultPollInterval); err != nil {
		t.Fatalf("Failed to delete PodCliqueSet %s: %v", name, err)
	}
	Logger.Debugf("PodCliqueSet %s/%s deleted", namespace, name)
}

func waitForSecret(t *testing.T, ctx context.Context, k8sClient *k8sclient.Client, name string, shouldExist bool) {
	t.Helper()

	w := waiter.New[*corev1.Secret]().
		WithTimeout(testctx.DefaultPollTimeout).
		WithInterval(testctx.DefaultPollInterval)
	getFn := k8sclient.Getter[*corev1.Secret](k8sClient, groveNamespace)

	var err error
	if shouldExist {
		_, err = waiter.WaitForResource(ctx, w.WithRetryOnError(), name, getFn)
	} else {
		err = waiter.WaitForResourceDeletion(ctx, w, name, getFn)
	}
	if err != nil {
		t.Fatalf("Timeout waiting for secret %s (shouldExist=%v): %v", name, shouldExist, err)
	}
}

// waitForSecretManagedByCertManager waits for a secret to exist AND be managed by cert-manager.
// This is important because when transitioning from auto-provision to cert-manager mode,
// the secret may already exist from auto-provision, and we need to wait for cert-manager
// to actually update it (which is indicated by the cert-manager.io/certificate-name annotation).
func waitForSecretManagedByCertManager(t *testing.T, ctx context.Context, k8sClient *k8sclient.Client, name string) {
	t.Helper()

	Logger.Debugf("Waiting for secret %s to be managed by cert-manager...", name)

	fetchSecret := waiter.FetchByName(name, k8sclient.Getter[*corev1.Secret](k8sClient, groveNamespace))
	isManagedByCertManager := waiter.Predicate[*corev1.Secret](func(secret *corev1.Secret) bool {
		certName := secret.Annotations[certManagerCertNameAnnotation]
		if certName != "" {
			Logger.Debugf("Secret %s is now managed by cert-manager (certificate: %s)", name, certName)
			return true
		}
		return false
	})

	w := waiter.New[*corev1.Secret]().
		WithTimeout(testctx.DefaultPollTimeout).
		WithInterval(testctx.DefaultPollInterval).
		WithRetryOnError()
	err := w.WaitUntil(ctx, fetchSecret, isManagedByCertManager)
	if err != nil {
		t.Fatalf("Timeout waiting for secret %s to be managed by cert-manager: %v", name, err)
	}
}

func deleteCertManagerResources(ctx context.Context, k8sClient *k8sclient.Client) {
	certGVK := certManagerCertificate
	issuerGVK := certManagerClusterIssuer

	// Delete Certificate first (cert-manager resource)
	certObj := &unstructured.Unstructured{}
	certObj.SetGroupVersionKind(certGVK)
	certObj.SetName(configv1alpha1.DefaultWebhookSecretName)
	certObj.SetNamespace(groveNamespace)
	if err := k8sClient.Delete(ctx, certObj); err != nil {
		Logger.Warnf("Failed to delete Certificate (may not exist): %v", err)
	}

	// Delete the Secret managed by cert-manager
	if err := k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: configv1alpha1.DefaultWebhookSecretName, Namespace: groveNamespace}}); err != nil {
		Logger.Warnf("Failed to delete Secret (may not exist): %v", err)
	}

	// Delete ClusterIssuer last
	issuerObj := &unstructured.Unstructured{}
	issuerObj.SetGroupVersionKind(issuerGVK)
	issuerObj.SetName("selfsigned-issuer")
	if err := k8sClient.Delete(ctx, issuerObj); err != nil {
		Logger.Warnf("Failed to delete ClusterIssuer (may not exist): %v", err)
	}
}

func installCertManager(t *testing.T, ctx context.Context, tc *testctx.TestContext) {
	t.Helper()

	cmConfig := &setup.HelmInstallConfig{
		RestConfig:      tc.Client.RestConfig,
		ReleaseName:     certManagerReleaseName,
		ChartRef:        certManagerChartRef,
		ChartVersion:    certManagerVersion,
		Namespace:       certManagerNamespace,
		CreateNamespace: true,
		RepoURL:         certManagerRepoURL,
		Values: map[string]interface{}{
			"installCRDs": true,
		},
		HelmLoggerFunc: Logger.Debugf,
		Logger:         Logger,
	}

	if _, err := setup.InstallHelmChart(cmConfig); err != nil {
		t.Fatalf("Failed to install cert-manager: %v", err)
	}

	// Ensure cert-manager is actually up and running before returning
	if err := pods.NewPodManager(tc.Client, Logger).WaitForReadyInNamespace(ctx, cmConfig.Namespace, 3, testctx.DefaultPollTimeout, testctx.DefaultPollInterval); err != nil {
		t.Fatalf("cert-manager pods failed to become ready: %v", err)
	}
}

func waitForClusterIssuer(t *testing.T, ctx context.Context, k8sClient *k8sclient.Client, name string) {
	t.Helper()

	issuerGVK := certManagerClusterIssuer

	fetchIssuer := waiter.FetchFunc[*unstructured.Unstructured](func(ctx context.Context) (*unstructured.Unstructured, error) {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(issuerGVK)
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, obj); err != nil {
			return nil, err
		}
		return obj, nil
	})

	w := waiter.New[*unstructured.Unstructured]().
		WithTimeout(30 * time.Second).
		WithInterval(1 * time.Second).
		WithRetryOnError()
	err := w.WaitUntil(ctx, fetchIssuer, func(issuer *unstructured.Unstructured) bool { return checkReadyStatus(issuer) })

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

		if condition["type"] == "Ready" && condition["status"] == string(metav1.ConditionTrue) {
			return true
		}
	}
	return false
}

func uninstallCertManager(t *testing.T, restConfig *rest.Config) {
	t.Helper()

	cmConfig := &setup.HelmInstallConfig{
		RestConfig:     restConfig,
		ReleaseName:    certManagerReleaseName,
		Namespace:      certManagerNamespace,
		HelmLoggerFunc: Logger.Debugf,
		Logger:         Logger,
	}

	if err := setup.UninstallHelmChart(cmConfig); err != nil {
		Logger.Warnf("Failed to uninstall cert-manager (may not exist): %v", err)
	}
}

const (
	// certManagerInjectAnnotation is the annotation cert-manager uses to inject CA bundles
	certManagerInjectAnnotation = "cert-manager.io/inject-ca-from"
	// certManagerCertNameAnnotation is the annotation cert-manager adds to secrets it manages
	certManagerCertNameAnnotation = "cert-manager.io/certificate-name"
	// groveNamespace is the namespace where Grove is installed
	groveNamespace = setup.OperatorNamespace
)

// webhookNames lists the ValidatingWebhookConfiguration and MutatingWebhookConfiguration names to check
var webhookConfigNames = []string{
	"podcliqueset-validating-webhook",
	"podcliqueset-defaulting-webhook",
}

// verifyCertManagerMode verifies that Grove is using cert-manager for certificate management.
// It checks:
// 1. Webhook configurations have the cert-manager.io/inject-ca-from annotation
// 2. The certificate secret has cert-manager annotations
func verifyCertManagerMode(t *testing.T, ctx context.Context, k8sClient *k8sclient.Client) {
	t.Helper()

	Logger.Debug("Verifying cert-manager mode is active...")

	// Check webhook configurations have cert-manager annotation
	for _, webhookName := range webhookConfigNames {
		if err := verifyWebhookHasCertManagerAnnotation(ctx, k8sClient, webhookName, true); err != nil {
			t.Fatalf("Webhook %s does not have cert-manager annotation: %v", webhookName, err)
		}
	}

	// Check secret has cert-manager annotations
	if err := verifyWebhookSecretCertManagerStatus(ctx, k8sClient, true); err != nil {
		t.Fatalf("Secret verification for cert-manager mode failed: %v", err)
	}

	Logger.Debug("Verified: Grove is using cert-manager certificates")
}

// verifyAutoProvisionMode verifies that Grove is using auto-provisioned certificates.
func verifyAutoProvisionMode(t *testing.T, ctx context.Context, k8sClient *k8sclient.Client) {
	t.Helper()

	Logger.Debug("Verifying auto-provision mode is active...")

	// Check secret does NOT have cert-manager annotations (proving it's managed by Grove's cert-controller)
	if err := verifyWebhookSecretCertManagerStatus(ctx, k8sClient, false); err != nil {
		t.Fatalf("Secret verification for auto-provision mode failed: %v", err)
	}

	Logger.Debug("Verified: Grove is using auto-provisioned certificates (secret not managed by cert-manager)")
}

// verifyWebhookHasCertManagerAnnotation checks if a webhook configuration has the cert-manager annotation.
// If shouldHave is true, it verifies the annotation exists; if false, it verifies the annotation is absent or empty.
func verifyWebhookHasCertManagerAnnotation(ctx context.Context, k8sClient *k8sclient.Client, webhookName string, shouldHave bool) error {
	// Try ValidatingWebhookConfiguration first
	var vwc admissionregistrationv1.ValidatingWebhookConfiguration
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: webhookName}, &vwc); err == nil {
		annotationValue := vwc.Annotations[certManagerInjectAnnotation]
		hasAnnotation := annotationValue != ""

		if shouldHave && !hasAnnotation {
			return fmt.Errorf("ValidatingWebhookConfiguration %s missing cert-manager annotation", webhookName)
		}
		if !shouldHave && hasAnnotation {
			return fmt.Errorf("ValidatingWebhookConfiguration %s has unexpected cert-manager annotation: %s", webhookName, annotationValue)
		}
		Logger.Debugf("ValidatingWebhookConfiguration %s: cert-manager annotation present=%v (expected=%v)", webhookName, hasAnnotation, shouldHave)
		return nil
	}

	// Try MutatingWebhookConfiguration
	var mwc admissionregistrationv1.MutatingWebhookConfiguration
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: webhookName}, &mwc); err == nil {
		annotationValue := mwc.Annotations[certManagerInjectAnnotation]
		hasAnnotation := annotationValue != ""

		if shouldHave && !hasAnnotation {
			return fmt.Errorf("MutatingWebhookConfiguration %s missing cert-manager annotation", webhookName)
		}
		if !shouldHave && hasAnnotation {
			return fmt.Errorf("MutatingWebhookConfiguration %s has unexpected cert-manager annotation: %s", webhookName, annotationValue)
		}
		Logger.Debugf("MutatingWebhookConfiguration %s: cert-manager annotation present=%v (expected=%v)", webhookName, hasAnnotation, shouldHave)
		return nil
	}

	return fmt.Errorf("webhook configuration %s not found as ValidatingWebhookConfiguration or MutatingWebhookConfiguration", webhookName)
}

// verifyWebhookSecretCertManagerStatus checks if the webhook certificate secret's cert-manager management status matches expectations.
// If expectManaged is true, it verifies cert-manager annotations exist; if false, it verifies they are absent.
func verifyWebhookSecretCertManagerStatus(ctx context.Context, k8sClient *k8sclient.Client, expectManaged bool) error {
	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: groveNamespace, Name: configv1alpha1.DefaultWebhookSecretName}, &secret); err != nil {
		return fmt.Errorf("failed to get secret %s/%s: %w", groveNamespace, configv1alpha1.DefaultWebhookSecretName, err)
	}

	// Check for cert-manager certificate name annotation
	certName := secret.Annotations[certManagerCertNameAnnotation]
	isManagedByCertManager := certName != ""

	if expectManaged && !isManagedByCertManager {
		return fmt.Errorf("secret %s is not managed by cert-manager (missing %s annotation)", configv1alpha1.DefaultWebhookSecretName, certManagerCertNameAnnotation)
	}
	if !expectManaged && isManagedByCertManager {
		return fmt.Errorf("secret %s is unexpectedly managed by cert-manager (has %s=%s)", configv1alpha1.DefaultWebhookSecretName, certManagerCertNameAnnotation, certName)
	}

	Logger.Debugf("Secret %s: managed by cert-manager=%v (expected=%v)", configv1alpha1.DefaultWebhookSecretName, isManagedByCertManager, expectManaged)
	return nil
}

// verifyWebhookServingCertificate verifies that the webhook is actually serving the certificate from the Secret.
// This connects to the webhook endpoint via TLS and compares the served certificate with the one in the Secret.
// It includes retry logic to handle timing issues with:
// - Kubernetes secret volume propagation delays
// - The certwatcher's 10-second polling interval for detecting certificate changes
func verifyWebhookServingCertificate(t *testing.T, ctx context.Context, k8sClient *k8sclient.Client) {
	t.Helper()

	Logger.Debug("Verifying webhook is serving the correct certificate (with retries for cert reload timing)...")

	// Retry for up to 30 seconds to account for:
	// - Kubernetes secret volume update propagation (can take up to the kubelet sync period)
	// - certwatcher 2-second polling interval
	var lastExpectedSerial, lastServedSerial string
	fetchServedCert := waiter.FetchFunc[*x509.Certificate](func(ctx context.Context) (*x509.Certificate, error) {
		servedCert, err := getServedCertificate(ctx, k8sClient)
		if err != nil {
			Logger.Debugf("Failed to get served certificate from webhook: %v", err)
		}
		return servedCert, err
	})
	matchesSecretCert := waiter.Predicate[*x509.Certificate](func(servedCert *x509.Certificate) bool {
		lastServedSerial = servedCert.SerialNumber.String()
		expectedCert, err := getCertificateFromSecret(ctx, k8sClient)
		if err != nil {
			Logger.Debugf("Failed to get certificate from secret: %v", err)
			return false
		}
		lastExpectedSerial = expectedCert.SerialNumber.String()
		if certificatesMatch(expectedCert, servedCert) {
			Logger.Debugf("Certificate match! Serial: %s", lastExpectedSerial)
			return true
		}
		Logger.Debugf("Certificate mismatch (will retry): expected serial=%s, served serial=%s",
			lastExpectedSerial, lastServedSerial)
		return false
	})
	w := waiter.New[*x509.Certificate]().
		WithTimeout(30 * time.Second).
		WithInterval(2 * time.Second).
		WithRetryOnError()
	err := w.WaitUntil(ctx, fetchServedCert, matchesSecretCert)

	if err != nil {
		t.Fatalf("Certificate mismatch: webhook is not serving the expected certificate after retries.\n"+
			"Expected serial: %s\n"+
			"Served serial: %s\n"+
			"This may indicate the operator has not reloaded the certificate from the secret.",
			lastExpectedSerial, lastServedSerial)
	}

	Logger.Debug("Verified: webhook is serving the correct certificate from the Secret")
}

// getCertificateFromSecret retrieves and parses the TLS certificate from the webhook Secret.
func getCertificateFromSecret(ctx context.Context, k8sClient *k8sclient.Client) (*x509.Certificate, error) {
	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: groveNamespace, Name: configv1alpha1.DefaultWebhookSecretName}, &secret); err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	certPEM, ok := secret.Data["tls.crt"]
	if !ok {
		return nil, fmt.Errorf("secret does not contain tls.crt")
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from tls.crt")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return cert, nil
}

// getServedCertificate connects to the webhook service and retrieves the certificate it's serving.
// It uses port-forwarding to connect to the operator pod.
func getServedCertificate(ctx context.Context, k8sClient *k8sclient.Client) (*x509.Certificate, error) {
	// Find the grove-operator pod
	var podList corev1.PodList
	if err := k8sClient.List(ctx, &podList, client.InNamespace(groveNamespace), setup.OperatorPodLabels); err != nil {
		return nil, fmt.Errorf("failed to list operator pods: %w", err)
	}
	if len(podList.Items) == 0 {
		return nil, fmt.Errorf("no grove-operator pods found")
	}

	podName := podList.Items[0].Name
	Logger.Debugf("Using operator pod: %s", podName)

	// Set up port forwarding
	localPort, stopChan, err := setupPortForward(ctx, k8sClient.RestConfig, groveNamespace, podName, 9443)
	if err != nil {
		return nil, fmt.Errorf("failed to set up port forwarding: %w", err)
	}
	defer close(stopChan)

	// Give port-forward a moment to establish
	time.Sleep(500 * time.Millisecond)

	// Connect via TLS and get the certificate
	cert, err := getTLSCertificate(fmt.Sprintf("localhost:%d", localPort))
	if err != nil {
		return nil, fmt.Errorf("failed to get TLS certificate: %w", err)
	}

	return cert, nil
}

// setupPortForward creates a port-forward to the specified pod and returns the local port.
func setupPortForward(ctx context.Context, restConfig *rest.Config, namespace, podName string, remotePort int) (int, chan struct{}, error) {
	// Find a free local port
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return 0, nil, fmt.Errorf("failed to find free port: %w", err)
	}
	localPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	// Create the port-forward URL
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, podName)

	transport, upgrader, err := spdy.RoundTripperFor(restConfig)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create round tripper: %w", err)
	}

	// Parse the URL properly
	parsedURL, err := parsePortForwardURL(restConfig.Host, path)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to parse URL: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, parsedURL)

	stopChan := make(chan struct{}, 1)
	readyChan := make(chan struct{})

	ports := []string{fmt.Sprintf("%d:%d", localPort, remotePort)}

	// Create a buffer to capture output (we don't need to display it)
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}

	pf, err := portforward.New(dialer, ports, stopChan, readyChan, out, errOut)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create port forwarder: %w", err)
	}

	// Start port forwarding in a goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := pf.ForwardPorts(); err != nil {
			errChan <- err
		}
	}()

	// Wait for port-forward to be ready or error
	select {
	case <-readyChan:
		Logger.Debugf("Port forward established: localhost:%d -> %s:%d", localPort, podName, remotePort)
	case err := <-errChan:
		return 0, nil, fmt.Errorf("port forward failed: %w", err)
	case <-time.After(10 * time.Second):
		close(stopChan)
		return 0, nil, fmt.Errorf("timeout waiting for port forward to be ready")
	case <-ctx.Done():
		close(stopChan)
		return 0, nil, ctx.Err()
	}

	// Check for any errors in errOut
	if errOut.Len() > 0 {
		Logger.Warnf("Port forward stderr: %s", errOut.String())
	}

	return localPort, stopChan, nil
}

// parsePortForwardURL parses the host and path into a proper URL for port forwarding.
func parsePortForwardURL(host, path string) (*url.URL, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	u.Path = path
	return u, nil
}

// getTLSCertificate connects to the specified address via TLS and returns the server's certificate.
func getTLSCertificate(address string) (*x509.Certificate, error) {
	// Connect with InsecureSkipVerify since we're just extracting the certificate
	conn, err := tls.Dial("tcp", address, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // We need to connect without verification to extract the cert
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

	// Get the peer certificates
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, fmt.Errorf("no certificates received from server")
	}

	// Return the leaf certificate (first one)
	return certs[0], nil
}

// certificatesMatch compares two certificates to determine if they are the same.
// We compare the raw DER bytes for an exact match.
func certificatesMatch(cert1, cert2 *x509.Certificate) bool {
	return bytes.Equal(cert1.Raw, cert2.Raw)
}
