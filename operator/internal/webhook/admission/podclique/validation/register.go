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

package validation

import (
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	// Name is the name of the validating webhook handler for PodClique.
	Name        = "podclique-validating-webhook"
	webhookPath = "/webhooks/validate-podclique"
	// scaleSubResource is the subresource through which kubectl scale and the HorizontalPodAutoscaler
	// write a PodClique's replica count.
	scaleSubResource = "scale"
)

// RegisterWithManager registers the webhook with the manager. A raw admission.Handler is used rather
// than admission.WithCustomValidator because this webhook admits two different object kinds: a
// PodClique on the parent resource and an autoscaling/v1 Scale on the scale subresource.
func (h *Handler) RegisterWithManager(mgr manager.Manager) error {
	webhook := admission.Webhook{
		Handler:      h,
		RecoverPanic: ptr.To(true),
	}
	mgr.GetWebhookServer().Register(webhookPath, &webhook)
	return nil
}
