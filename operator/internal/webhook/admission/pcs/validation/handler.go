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

package validation

import (
	"context"
	"fmt"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	// ErrValidateCreatePodCliqueSet is the error code returned where the request to create a PodCliqueSet is invalid.
	ErrValidateCreatePodCliqueSet v1alpha1.ErrorCode = "ERR_VALIDATE_CREATE_PODCLIQUESET"
	// ErrValidateUpdatePodCliqueSet is the error code returned where the request to update a PodCliqueSet is invalid.
	ErrValidateUpdatePodCliqueSet v1alpha1.ErrorCode = "ERR_VALIDATE_UPDATE_PODCLIQUESET"
)

// Handler is a handler for validating PodCliqueSet resources.
type Handler struct {
	logger        logr.Logger
	tasConfig     configv1alpha1.TopologyAwareSchedulingConfiguration
	networkConfig configv1alpha1.NetworkAcceleration
}

// NewHandler creates a new handler for PodCliqueSet Webhook.
func NewHandler(mgr manager.Manager, tasConfig configv1alpha1.TopologyAwareSchedulingConfiguration, networkConfig configv1alpha1.NetworkAcceleration) *Handler {
	return &Handler{
		logger:        mgr.GetLogger().WithName("webhook").WithName(Name),
		tasConfig:     tasConfig,
		networkConfig: networkConfig,
	}
}

// ValidateCreate validates a PodCliqueSet create request.
func (h *Handler) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	h.logValidatorFunctionInvocation(ctx)
	pcs, err := castToPodCliqueSet(obj)
	if err != nil {
		return nil, errors.WrapError(err, ErrValidateCreatePodCliqueSet, string(admissionv1.Create), "failed to cast object to PodCliqueSet")
	}

	v := newPCSValidator(pcs, admissionv1.Create, h.tasConfig)
	var allErrs field.ErrorList
	allErrs = append(allErrs, v.validateTopologyConstraintsOnCreate()...)
	warnings, errs := v.validate()
	allErrs = append(allErrs, errs...)

	// Validate MNNVL annotation: reject if annotation="true" but feature is disabled
	if err := mnnvl.ValidateAutoMNNVLOnCreate(pcs, h.networkConfig.AutoMNNVLEnabled); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("metadata", "annotations", mnnvl.AnnotationAutoMNNVL),
			pcs.Annotations[mnnvl.AnnotationAutoMNNVL],
			err.Error()))
	}

	return warnings, allErrs.ToAggregate()
}

// ValidateUpdate validates a PodCliqueSet update request.
func (h *Handler) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	h.logValidatorFunctionInvocation(ctx)
	newPCS, err := castToPodCliqueSet(newObj)
	if err != nil {
		return nil, errors.WrapError(err, ErrValidateUpdatePodCliqueSet, string(admissionv1.Update), "failed to cast new object to PodCliqueSet")
	}
	oldPCS, err := castToPodCliqueSet(oldObj)
	if err != nil {
		return nil, errors.WrapError(err, ErrValidateUpdatePodCliqueSet, string(admissionv1.Update), "failed to cast old object to PodCliqueSet")
	}

	// Validate MNNVL annotation immutability
	if err := mnnvl.ValidateAutoMNNVLOnUpdate(oldPCS, newPCS); err != nil {
		return nil, errors.WrapError(err, ErrValidateUpdatePodCliqueSet, string(admissionv1.Update), err.Error())
	}

	v := newPCSValidator(newPCS, admissionv1.Update, h.tasConfig)
	warnings, errs := v.validate()
	if len(errs) > 0 {
		return warnings, errs.ToAggregate()
	}
	return warnings, v.validateUpdate(oldPCS)
}

// ValidateDelete validates a PodCliqueSet delete request.
func (h *Handler) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// castToPodCliqueSet attempts to cast a runtime.Object to a PodCliqueSet.
func castToPodCliqueSet(obj runtime.Object) (*v1alpha1.PodCliqueSet, error) {
	pcs, ok := obj.(*v1alpha1.PodCliqueSet)
	if !ok {
		return nil, fmt.Errorf("expected an PodCliqueSet object but got %T", obj)
	}
	return pcs, nil
}

// logValidatorFunctionInvocation logs details about the validation request including user and operation information.
func (h *Handler) logValidatorFunctionInvocation(ctx context.Context) {
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		h.logger.Error(err, "failed to get request from context")
		return
	}
	h.logger.Info("PodCliqueSet validation webhook invoked", "name", req.Name, "namespace", req.Namespace, "operation", req.Operation, "user", req.UserInfo.Username)
}
