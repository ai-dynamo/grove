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
	"context"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/webhook/admission/scaleguard"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Handler validates PodClique resources. It rejects a spec.replicas change, whether made directly or
// through the scale subresource, while the owning PodCliqueSet replica is under a coherent update.
type Handler struct {
	logger logr.Logger
	client client.Client
}

// NewHandler creates a validating webhook handler for PodClique.
func NewHandler(mgr manager.Manager) *Handler {
	return &Handler{
		logger: mgr.GetLogger().WithName("webhook").WithName(Name),
		client: mgr.GetClient(),
	}
}

// Handle admits or denies a PodClique admission request, guarding replica scaling during a coherent update.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	return scaleguard.Handle(ctx, req, h.client, h.logger, &grovecorev1alpha1.PodClique{})
}
