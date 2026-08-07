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
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	// ErrValidateUpdatePodClique is the error code returned when the request to update a PodClique is invalid.
	ErrValidateUpdatePodClique grovecorev1alpha1.ErrorCode = "ERR_VALIDATE_UPDATE_PODCLIQUE"
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
func (h *Handler) ValidateCreate(_ context.Context, _ *grovecorev1alpha1.PodClique) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate rejects Spec.Replicas changes on a PodClique while the PCS replica that owns it is
// currently under a Coherent update (the replica is in Status.UpdateProgress.CurrentlyUpdating with
// UpdateEndedAt unset). Replicas of the same PodCliqueSet that are not currently under update may
// still scale. The owning PCS is resolved via the LabelPartOfKey label, which is set on every PCLQ
// regardless of whether it is standalone or PCSG-owned.
func (h *Handler) ValidateUpdate(ctx context.Context, oldPCLQ, newPCLQ *grovecorev1alpha1.PodClique) (admission.Warnings, error) {
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

	pcsReplicaIndex, err := componentutils.GetPCSReplicaIndexFromObjectMeta(newPCLQ.ObjectMeta)
	if err != nil {
		return nil, errors.WrapError(err, ErrValidateUpdatePodClique, "Update",
			fmt.Sprintf("could not determine PodCliqueSet replica index for PodClique %s/%s", newPCLQ.Namespace, newPCLQ.Name))
	}
	if componentutils.IsPCSReplicaUnderCoherentUpdate(pcs, pcsReplicaIndex) {
		return nil, fmt.Errorf("spec.replicas changes are not allowed while a coherent update is in progress on PodCliqueSet %s/%s replica %d; complete the update of this replica before scaling",
			pcs.Namespace, pcs.Name, pcsReplicaIndex)
	}
	return nil, nil
}

// ValidateDelete is a no-op.
func (h *Handler) ValidateDelete(_ context.Context, _ *grovecorev1alpha1.PodClique) (admission.Warnings, error) {
	return nil, nil
}
