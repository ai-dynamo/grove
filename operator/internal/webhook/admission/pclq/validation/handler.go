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
	"encoding/json"
	"fmt"
	"net/http"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const denialMessage = "spec.replicas cannot be changed on a PodClique owned by a PodCliqueScalingGroup; scale the owning PodCliqueScalingGroup instead"

// Handler validates PodClique updates, including the scale subresource.
type Handler struct {
	reader client.Reader
	logger logr.Logger
}

// NewHandler creates a PodClique validation handler.
func NewHandler(mgr manager.Manager) *Handler {
	return &Handler{
		reader: mgr.GetAPIReader(),
		logger: mgr.GetLogger().WithName("webhook").WithName(Name),
	}
}

// Handle rejects independent scaling of PodCliques owned by a PodCliqueScalingGroup.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Update {
		return admission.Allowed("operation does not update a PodClique")
	}
	if req.SubResource == "scale" {
		return h.validateScaleUpdate(ctx, req)
	}
	return h.validatePodCliqueUpdate(req)
}

func (h *Handler) validatePodCliqueUpdate(req admission.Request) admission.Response {
	oldPCLQ := &grovecorev1alpha1.PodClique{}
	newPCLQ := &grovecorev1alpha1.PodClique{}
	if err := json.Unmarshal(req.OldObject.Raw, oldPCLQ); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("decoding old PodClique: %w", err))
	}
	if err := json.Unmarshal(req.Object.Raw, newPCLQ); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("decoding new PodClique: %w", err))
	}
	if oldPCLQ.Spec.Replicas != newPCLQ.Spec.Replicas &&
		(isScalingGroupOwned(oldPCLQ) || isScalingGroupOwned(newPCLQ)) {
		return admission.Denied(denialMessage)
	}
	return admission.Allowed("PodClique update is valid")
}

func (h *Handler) validateScaleUpdate(ctx context.Context, req admission.Request) admission.Response {
	scale := &autoscalingv1.Scale{}
	if err := json.Unmarshal(req.Object.Raw, scale); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("decoding Scale: %w", err))
	}

	pclq := &grovecorev1alpha1.PodClique{}
	if err := h.reader.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, pclq); err != nil {
		h.logger.Error(err, "Failed to read PodClique for scale validation")
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("reading PodClique: %w", err))
	}
	if !isScalingGroupOwned(pclq) {
		return admission.Allowed("standalone PodClique scale update is valid")
	}
	if scale.Spec.Replicas != pclq.Spec.Replicas {
		return admission.Denied(denialMessage)
	}
	return admission.Allowed("PodClique scale update does not change replicas")
}

func isScalingGroupOwned(pclq *grovecorev1alpha1.PodClique) bool {
	if owner := metav1.GetControllerOfNoCopy(pclq); owner != nil &&
		owner.APIVersion == grovecorev1alpha1.SchemeGroupVersion.String() &&
		owner.Kind == constants.KindPodCliqueScalingGroup {
		return true
	}
	_, ok := pclq.Labels[apicommon.LabelPodCliqueScalingGroup]
	return ok
}
