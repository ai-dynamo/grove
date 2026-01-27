// /*
// Copyright 2024 The Grove Authors.
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

package webhook

import (
	"fmt"
	"log/slog"
	"os"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/authorization"
	"github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/defaulting"
	pcsvalidation "github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/validation"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// Register registers the webhooks with the controller manager.
func Register(mgr manager.Manager, authorizerConfig configv1alpha1.AuthorizerConfig, tasConfig configv1alpha1.TopologyAwareSchedulingConfiguration, networkConfig configv1alpha1.NetworkAcceleration) error {
	defaultingWebhook := defaulting.NewHandler(mgr, networkConfig)
	slog.Info("Registering webhook with manager", "handler", defaulting.Name)
	if err := defaultingWebhook.RegisterWithManager(mgr); err != nil {
		return fmt.Errorf("failed adding %s webhook handler: %v", defaulting.Name, err)
	}
	pcsValidatingWebhook := pcsvalidation.NewHandler(mgr, tasConfig, networkConfig)
	slog.Info("Registering webhook with manager", "handler", pcsvalidation.Name)
	if err := pcsValidatingWebhook.RegisterWithManager(mgr); err != nil {
		return fmt.Errorf("failed adding %s webhook handler: %v", pcsvalidation.Name, err)
	}
	if authorizerConfig.Enabled {
		serviceAccountName, ok := os.LookupEnv(constants.EnvVarServiceAccountName)
		if !ok {
			return fmt.Errorf("can not register authorizer webhook with no \"%s\" environment vairable", constants.EnvVarServiceAccountName)
		}
		namespace, err := os.ReadFile(constants.OperatorNamespaceFile)
		if err != nil {
			return fmt.Errorf("error reading namespace file with error: %w", err)
		}
		reconcilerServiceAccountUserName := generateReconcilerServiceAccountUsername(string(namespace), serviceAccountName)
		authorizerWebhook := authorization.NewHandler(mgr, authorizerConfig, reconcilerServiceAccountUserName)
		slog.Info("Registering webhook with manager", "handler", authorization.Name)
		if err := authorizerWebhook.RegisterWithManager(mgr); err != nil {
			return fmt.Errorf("failed adding %s webhook handler: %v", authorization.Name, err)
		}
	}
	return nil
}

func generateReconcilerServiceAccountUsername(namespace, serviceAccountName string) string {
	return fmt.Sprintf("system:serviceaccount:%s:%s", namespace, serviceAccountName)
}
