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
	"context"
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Handler validates ClusterTopology resources.
type Handler struct {
	logger logr.Logger
}

// NewHandler creates a new ClusterTopology validation handler.
func NewHandler(mgr manager.Manager) *Handler {
	return &Handler{
		logger: mgr.GetLogger().WithName("webhook").WithName(Name),
	}
}

// ValidateCreate validates a ClusterTopology create request.
func (h *Handler) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	h.logValidation(ctx)
	ct, err := castToClusterTopology(obj)
	if err != nil {
		return nil, err
	}
	allErrs := validateClusterTopologyLevels(ct.Spec.Levels, field.NewPath("spec", "levels"))
	return nil, allErrs.ToAggregate()
}

// ValidateUpdate validates a ClusterTopology update request.
// Only the new object's levels are validated for structural correctness (domain/key uniqueness).
// Transition validation (e.g., detecting removed levels referenced by PodCliqueSets) is handled
// by the PCS reconciler via the TopologyLevelsUnavailable condition, not by this webhook.
func (h *Handler) ValidateUpdate(ctx context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	h.logValidation(ctx)
	ct, err := castToClusterTopology(newObj)
	if err != nil {
		return nil, err
	}
	allErrs := validateClusterTopologyLevels(ct.Spec.Levels, field.NewPath("spec", "levels"))
	return nil, allErrs.ToAggregate()
}

// ValidateDelete validates a ClusterTopology delete request.
func (h *Handler) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// castToClusterTopology attempts to cast a runtime.Object to a ClusterTopology.
func castToClusterTopology(obj runtime.Object) (*grovecorev1alpha1.ClusterTopology, error) {
	ct, ok := obj.(*grovecorev1alpha1.ClusterTopology)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterTopology object but got %T", obj)
	}
	return ct, nil
}

// logValidation logs details about the validation request.
func (h *Handler) logValidation(ctx context.Context) {
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		h.logger.Error(err, "failed to get request from context")
		return
	}
	h.logger.Info("ClusterTopology validation webhook invoked", "name", req.Name, "operation", req.Operation, "user", req.UserInfo.Username)
}
