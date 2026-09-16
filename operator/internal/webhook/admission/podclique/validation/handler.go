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
	"fmt"
	"net/http"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Handler validates updates to PodClique resources.
type Handler struct {
	logger        logr.Logger
	client        client.Client
	decoder       admission.Decoder
	schedRegistry scheduler.Registry
}

// NewHandler creates a new handler for the PodClique validating webhook.
func NewHandler(mgr manager.Manager, schedRegistry scheduler.Registry) *Handler {
	return &Handler{
		logger:        mgr.GetLogger().WithName("webhook").WithName(Name),
		client:        mgr.GetClient(),
		decoder:       admission.NewDecoder(mgr.GetScheme()),
		schedRegistry: schedRegistry,
	}
}

// Handle validates a PodClique update, delegating to the scheduler backend that owns it.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Update {
		return admission.Allowed(fmt.Sprintf("operation %s is not validated", req.Operation))
	}
	oldReplicas, newReplicas, err := h.decodeReplicas(req)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	backend, err := h.resolveBackend(ctx, req)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if backend == nil {
		return admission.Allowed("no scheduler backend is registered for this PodClique")
	}
	scaleValidator, ok := backend.(scheduler.PodCliqueScaleValidator)
	if !ok {
		return admission.Allowed(fmt.Sprintf("scheduler backend %q does not restrict PodClique scaling", backend.Name()))
	}
	if err := scaleValidator.ValidatePodCliqueScale(ctx, oldReplicas, newReplicas); err != nil {
		h.logger.Info("Denying PodClique update",
			"podClique", client.ObjectKey{Namespace: req.Namespace, Name: req.Name},
			"subResource", req.SubResource,
			"user", req.UserInfo.Username,
			"oldReplicas", oldReplicas,
			"newReplicas", newReplicas,
			"reason", err.Error())
		return admission.Denied(err.Error())
	}
	return admission.Allowed("PodClique update is allowed by the scheduler backend")
}

// decodeReplicas returns the replica counts the request moves between. The scale subresource admits
// autoscaling/v1 Scale, the parent resource admits PodCliques.
func (h *Handler) decodeReplicas(req admission.Request) (int32, int32, error) {
	if req.SubResource == scaleSubResource {
		var oldScale, newScale autoscalingv1.Scale
		if err := h.decodeOldAndNew(req, &oldScale, &newScale); err != nil {
			return 0, 0, err
		}
		return oldScale.Spec.Replicas, newScale.Spec.Replicas, nil
	}

	var oldPCLQ, newPCLQ grovecorev1alpha1.PodClique
	if err := h.decodeOldAndNew(req, &oldPCLQ, &newPCLQ); err != nil {
		return 0, 0, err
	}
	return oldPCLQ.Spec.Replicas, newPCLQ.Spec.Replicas, nil
}

// resolveBackend returns the backend owning the PodClique, or nil if none is registered.
// Resolved from the PodClique as it stands, never from the incoming object: schedulerName is not
// immutable on a live PodClique, so trusting it would let one request switch to a permissive backend
// and scale at the same time.
func (h *Handler) resolveBackend(ctx context.Context, req admission.Request) (scheduler.Backend, error) {
	pclq := &grovecorev1alpha1.PodClique{}
	if req.SubResource == scaleSubResource {
		if err := h.client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, pclq); err != nil {
			return nil, fmt.Errorf("failed to get PodClique to resolve its scheduler backend: %w", err)
		}
	} else {
		if err := h.decoder.DecodeRaw(req.OldObject, pclq); err != nil {
			return nil, fmt.Errorf("failed to decode PodClique to resolve its scheduler backend: %w", err)
		}
	}
	return h.schedRegistry.GetOrDefault(pclq.Spec.PodSpec.SchedulerName), nil
}

// decodeOldAndNew decodes req.OldObject and req.Object into oldObj and newObj.
func (h *Handler) decodeOldAndNew(req admission.Request, oldObj, newObj runtime.Object) error {
	if err := h.decoder.DecodeRaw(req.OldObject, oldObj); err != nil {
		return fmt.Errorf("failed to decode old object: %w", err)
	}
	if err := h.decoder.DecodeRaw(req.Object, newObj); err != nil {
		return fmt.Errorf("failed to decode new object: %w", err)
	}
	return nil
}
