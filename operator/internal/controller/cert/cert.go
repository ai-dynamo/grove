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
	"fmt"
	"os"
	"strings"

	"github.com/ai-dynamo/grove/operator/internal/constants"
	authorizationwebhook "github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/authorization"
	defaultingwebhook "github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/defaulting"
	validatingwebhook "github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/validation"

	"github.com/go-logr/logr"
	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	serviceName                      = "grove-operator"
	certificateAuthorityName         = "Grove-CA"
	certificateAuthorityOrganization = "Grove"
)

// ManageWebhookCerts manages webhook certificates based on the AutoProvision configuration.
// When AutoProvision=true: uses cert-controller for automatic certificate generation and management.
// When AutoProvision=false: waits for externally provided certificates (e.g., from cert-manager, cluster admin).
func ManageWebhookCerts(mgr ctrl.Manager, certDir string, secretName string, authorizerEnabled bool, autoProvision bool, certsReadyCh chan struct{}) error {
	namespace, err := getOperatorNamespace()
	if err != nil {
		return err
	}

	logger := ctrl.Log.WithName("cert-management")

	if !autoProvision {
		logger.Info("Using externally provided certificates",
			"certDir", certDir, "secretName", secretName)
		// Certificates are managed externally, signal ready immediately
		close(certsReadyCh)
		return nil
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

// getOperatorNamespace reads the operator's namespace from namespace file
func getOperatorNamespace() (string, error) {
	data, err := os.ReadFile(constants.OperatorNamespaceFile)
	if err != nil {
		return "", err
	}
	namespace := strings.TrimSpace(string(data))
	if len(namespace) == 0 {
		return "", fmt.Errorf("operator namespace is empty")
	}
	return namespace, nil
}
