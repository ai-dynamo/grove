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

package validation

import (
	"context"
	"fmt"

	"github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	// ErrValidateUpdatePodClique is the error code returned when the request to update a PodClique is invalid.
	ErrValidateUpdatePodClique v1alpha1.ErrorCode = "ERR_VALIDATE_UPDATE_PODCLIQUE"
)

// Handler validates PodClique resources, blocking Spec.Replicas changes while a Coherent
// update is in progress on the owning PodCliqueSet.
type Handler struct {
	logger logr.Logger
	client client.Client
}

// NewHandler creates a new validating webhook handler for PodClique.
func NewHandler(mgr manager.Manager) *Handler {
	return &Handler{
		logger: mgr.GetLogger().WithName("webhook").WithName(Name),
		client: mgr.GetClient(),
	}
}

// ValidateCreate is a no-op — Spec.Replicas of a freshly created PCLQ cannot collide with
// an in-progress Coherent update for it.
func (h *Handler) ValidateCreate(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate rejects Spec.Replicas changes on a PodClique while the owning PodCliqueSet
// has a Coherent update in progress. The owning PCS is resolved via the LabelPartOfKey label,
// which is set on every PCLQ regardless of whether it is standalone or PCSG-owned.
func (h *Handler) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldPCLQ, err := castToPodClique(oldObj)
	if err != nil {
		return nil, errors.WrapError(err, ErrValidateUpdatePodClique, "Update", "failed to cast old object to PodClique")
	}
	newPCLQ, err := castToPodClique(newObj)
	if err != nil {
		return nil, errors.WrapError(err, ErrValidateUpdatePodClique, "Update", "failed to cast new object to PodClique")
	}

	if oldPCLQ.Spec.Replicas == newPCLQ.Spec.Replicas {
		return nil, nil
	}

	pcs, err := componentutils.GetPodCliqueSet(ctx, h.client, newPCLQ.ObjectMeta)
	if err != nil {
		// If the owning PCS cannot be resolved we cannot tell whether a Coherent update is in
		// progress; fail open and allow the change rather than blocking recovery actions on an
		// orphaned PCLQ.
		if apierrors.IsNotFound(err) {
			h.logger.Info("Owning PodCliqueSet not found; allowing PCLQ replica change",
				"pclq", client.ObjectKeyFromObject(newPCLQ))
			return nil, nil
		}
		return nil, errors.WrapError(err, ErrValidateUpdatePodClique, "Update",
			fmt.Sprintf("failed to get owning PodCliqueSet for PodClique %s/%s", newPCLQ.Namespace, newPCLQ.Name))
	}

	if componentutils.IsCoherentUpdateInProgress(pcs) {
		return nil, fmt.Errorf("spec.replicas changes are not allowed while a coherent update is in progress on PodCliqueSet %s/%s; complete the update before scaling",
			pcs.Namespace, pcs.Name)
	}
	return nil, nil
}

// ValidateDelete is a no-op.
func (h *Handler) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func castToPodClique(obj runtime.Object) (*v1alpha1.PodClique, error) {
	pclq, ok := obj.(*v1alpha1.PodClique)
	if !ok {
		return nil, fmt.Errorf("expected a PodClique object but got %T", obj)
	}
	return pclq, nil
}
