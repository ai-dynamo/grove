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

// Handle validates an update to a PodClique, delegating to the scheduler backend resolved for it.
// The webhook is registered for both the podcliques resource and its scale subresource, because
// kubectl scale and the HorizontalPodAutoscaler write replicas through the subresource and a rule on
// the parent resource alone would not see them.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Update {
		return admission.Allowed(fmt.Sprintf("operation %s is not validated", req.Operation))
	}
	schedulerName, oldReplicas, newReplicas, err := h.decodeRequest(ctx, req)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	backend := h.schedRegistry.GetOrDefault(schedulerName)
	if backend == nil {
		return admission.Allowed(fmt.Sprintf("no scheduler backend is registered for schedulerName %q", schedulerName))
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

// decodeRequest returns the schedulerName that resolves the backend, together with the replica counts
// carried by the admission request. All cliques of a PodCliqueSet share the same resolved
// schedulerName and a PodClique inherits its clique template's PodSpec, so the PodClique identifies
// its own backend. On the scale subresource the admitted objects are autoscaling/v1 Scale, which
// carries no schedulerName, so the PodClique is fetched for that purpose alone; the replica counts
// still come from the request, never from the fetched object.
func (h *Handler) decodeRequest(ctx context.Context, req admission.Request) (string, int32, int32, error) {
	if req.SubResource == scaleSubResource {
		oldScale := &autoscalingv1.Scale{}
		if err := h.decoder.DecodeRaw(req.OldObject, oldScale); err != nil {
			return "", 0, 0, fmt.Errorf("failed to decode old Scale for PodClique %s/%s: %w", req.Namespace, req.Name, err)
		}
		newScale := &autoscalingv1.Scale{}
		if err := h.decoder.DecodeRaw(req.Object, newScale); err != nil {
			return "", 0, 0, fmt.Errorf("failed to decode new Scale for PodClique %s/%s: %w", req.Namespace, req.Name, err)
		}
		pclq := &grovecorev1alpha1.PodClique{}
		if err := h.client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, pclq); err != nil {
			return "", 0, 0, fmt.Errorf("failed to get PodClique %s/%s: %w", req.Namespace, req.Name, err)
		}
		return pclq.Spec.PodSpec.SchedulerName, oldScale.Spec.Replicas, newScale.Spec.Replicas, nil
	}

	oldPCLQ := &grovecorev1alpha1.PodClique{}
	if err := h.decoder.DecodeRaw(req.OldObject, oldPCLQ); err != nil {
		return "", 0, 0, fmt.Errorf("failed to decode old PodClique %s/%s: %w", req.Namespace, req.Name, err)
	}
	newPCLQ := &grovecorev1alpha1.PodClique{}
	if err := h.decoder.DecodeRaw(req.Object, newPCLQ); err != nil {
		return "", 0, 0, fmt.Errorf("failed to decode new PodClique %s/%s: %w", req.Namespace, req.Name, err)
	}
	return newPCLQ.Spec.PodSpec.SchedulerName, oldPCLQ.Spec.Replicas, newPCLQ.Spec.Replicas, nil
}
