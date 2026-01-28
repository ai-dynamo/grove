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

package setup

const (
	// OperatorNamespace is the namespace where the Grove operator is deployed for E2E tests.
	// This is used during installation (via Skaffold) and for finding operator pods during diagnostics.
	OperatorNamespace = "grove-system"

	// OperatorDeploymentName is the name of the operator deployment (also the Helm release name).
	// This is used to find operator pods for log collection during test failures.
	OperatorDeploymentName = "grove-operator"

	// DefaultWebhookPort is the default port for the webhook server.
	// NOTE: If you change this, also update config.server.webhooks.port in operator/charts/values.yaml
	DefaultWebhookPort = 9443

	// DefaultWebhookServerCertDir is the default directory for webhook certificates.
	// NOTE: If you change this, also update config.server.webhooks.serverCertDir in operator/charts/values.yaml
	DefaultWebhookServerCertDir = "/etc/grove-operator/webhook-certs"
)
