// /*
// Copyright 2026 The Grove Authors.
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

package validation

import (
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	// Name is the name of the validating webhook handler for ClusterTopology.
	Name        = "clustertopology-validating-webhook"
	webhookPath = "/webhooks/validate-clustertopology"
)

// RegisterWithManager registers the webhook with the manager.
func (h *Handler) RegisterWithManager(mgr manager.Manager) error {
	webhook := admission.
		WithCustomValidator(mgr.GetScheme(), &grovecorev1alpha1.ClusterTopology{}, h).
		WithRecoverPanic(true)
	mgr.GetWebhookServer().Register(webhookPath, webhook)
	return nil
}
