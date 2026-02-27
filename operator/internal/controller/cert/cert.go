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

package cert

import (
	"context"
	"fmt"
	"os"
	"strings"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	authorizationwebhook "github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/authorization"
	defaultingwebhook "github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/defaulting"
	validatingwebhook "github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/validation"

	"github.com/go-logr/logr"
	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	serviceName                      = "grove-operator"
	certificateAuthorityName         = "Grove-CA"
	certificateAuthorityOrganization = "Grove"
)

// ManageWebhookCerts manages webhook certificates based on the CertProvisionMode configuration.
// When mode=auto: uses cert-controller for automatic certificate generation and management.
// When mode=manual: waits for externally provided certificates (e.g., from cert-manager, cluster admin).
// Returns an error for unrecognized modes to ensure new modes are explicitly handled.
func ManageWebhookCerts(ctx context.Context, mgr ctrl.Manager, cl client.Client, certDir string, secretName string, authorizerEnabled bool, certProvisionMode configv1alpha1.CertProvisionMode, certsReadyCh chan struct{}) error {
	logger := ctrl.Log.WithName("cert-management")

	switch certProvisionMode {
	case configv1alpha1.CertProvisionModeManual:
		logger.Info("Using externally provided certificates (manual mode)",
			"certDir", certDir, "secretName", secretName)
		// Certificates are managed externally, signal ready immediately
		close(certsReadyCh)
		return nil

	case configv1alpha1.CertProvisionModeAuto:
		return setupAutoCertProvisioning(ctx, mgr, cl, certDir, secretName, authorizerEnabled, certsReadyCh, logger)

	default:
		return fmt.Errorf("unsupported cert provision mode: %q", certProvisionMode)
	}
}

// setupAutoCertProvisioning configures cert-controller for automatic certificate management.
func setupAutoCertProvisioning(ctx context.Context, mgr ctrl.Manager, cl client.Client, certDir string, secretName string, authorizerEnabled bool, certsReadyCh chan struct{}, logger logr.Logger) error {
	namespace, err := getOperatorNamespace()
	if err != nil {
		return err
	}

	// Ensure the TLS secret exists before handing off to cert-controller.
	// The upstream cert-controller can only Update existing secrets, not Create them.
	// This allows the operator to self-create the secret when the Helm chart is configured
	// with webhookServerSecret.enabled=false (e.g., for GitOps/helm-template workflows).
	if err := createPlaceholderSecretIfNotExists(ctx, cl, namespace, secretName); err != nil {
		return fmt.Errorf("ensuring webhook TLS secret exists: %w", err)
	}

	logger.Info("Auto-provisioning certificates using cert-controller",
		"secretName", secretName, "certDir", certDir)
	rotator := &cert.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: namespace,
			Name:      secretName,
		},
		CertDir:        certDir,
		CAName:         certificateAuthorityName,
		CAOrganization: certificateAuthorityOrganization,
		IsReady:        certsReadyCh,
		DNSName:        fmt.Sprintf("%s.%s.svc", serviceName, namespace),
		ExtraDNSNames: []string{
			serviceName,
			fmt.Sprintf("%s.%s", serviceName, namespace),
			fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
		},
		Webhooks:               getWebhooks(authorizerEnabled),
		EnableReadinessCheck:   true,
		RestartOnSecretRefresh: true,
	}
	return cert.AddRotator(mgr, rotator)
}

// WaitTillWebhookCertsReady blocks on the certsReady channel. Once the cert-controller
// has ensured that the certificates are generated and injected then it will close this channel.
func WaitTillWebhookCertsReady(logger logr.Logger, certsReady chan struct{}) {
	logger.Info("Waiting for certs to be ready and injected into webhook configurations")
	<-certsReady
	logger.Info("Certs are ready and injected into webhook configurations")
}

// getWebhooks returns the webhooks that are to be registered with the cert-controller
func getWebhooks(authorizerEnabled bool) []cert.WebhookInfo {
	// defaulting and validating webhooks are always enabled, and are therefore registered by default.
	webhooks := []cert.WebhookInfo{
		{
			Type: cert.Mutating,
			Name: defaultingwebhook.Name,
		},
		{
			Type: cert.Validating,
			Name: validatingwebhook.Name,
		},
	}
	if authorizerEnabled {
		webhooks = append(webhooks, cert.WebhookInfo{
			Type: cert.Validating,
			Name: authorizationwebhook.Name,
		})
	}
	return webhooks
}

// createPlaceholderSecretIfNotExists creates the webhook TLS secret if it does not already exist.
// This is needed because the upstream cert-controller (OPA cert-controller) can only
// Update existing secrets — it cannot Create them. If the secret already exists
// (e.g., created by Helm or by CD tools like Argo CD / Flux that apply rendered
// manifests via `helm template | kubectl apply`), it is left untouched to preserve
// any existing certificate data.
func createPlaceholderSecretIfNotExists(ctx context.Context, cl client.Client, namespace, secretName string) error {
	secret := &corev1.Secret{}
	err := cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, secret)
	if !apierrors.IsNotFound(err) {
		return err
	}

	// Secret does not exist — create an empty TLS secret for cert-controller to populate.
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      secretName,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "grove-operator",
				"app.kubernetes.io/component":  "webhook",
				"app.kubernetes.io/part-of":    "grove",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": {},
			"tls.key": {},
			"ca.crt":  {},
		},
	}
	if err := cl.Create(ctx, secret); err != nil {
		// In HA deployments (replicaCount > 1), two replicas can race between
		// Get (not found) and Create. Treat AlreadyExists as success.
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating webhook TLS secret: %w", err)
	}

	ctrl.Log.WithName("cert-management").Info("Created webhook TLS secret",
		"namespace", namespace, "name", secretName)
	return nil
}

// getOperatorNamespace reads the operator's namespace from namespace file
func getOperatorNamespace() (string, error) {
	return getOperatorNamespaceFromFile(constants.OperatorNamespaceFile)
}

// getOperatorNamespaceFromFile reads the operator's namespace from the specified file path.
// This is extracted for testability.
func getOperatorNamespaceFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	namespace := strings.TrimSpace(string(data))
	if len(namespace) == 0 {
		return "", fmt.Errorf("operator namespace is empty")
	}
	return namespace, nil
}
