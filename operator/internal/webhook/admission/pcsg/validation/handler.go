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
	// ErrValidateUpdatePodCliqueScalingGroup is the error code returned when the request to update a PodCliqueScalingGroup is invalid.
	ErrValidateUpdatePodCliqueScalingGroup grovecorev1alpha1.ErrorCode = "ERR_VALIDATE_UPDATE_PODCLIQUESCALINGGROUP"
)

// Handler validates PodCliqueScalingGroup resources, blocking Spec.Replicas changes
// while a Coherent update is in progress on the owning PodCliqueSet.
type Handler struct {
	logger logr.Logger
	client client.Client
}

// NewHandler creates a new validating webhook handler for PodCliqueScalingGroup.
func NewHandler(mgr manager.Manager) *Handler {
	return &Handler{
		logger: mgr.GetLogger().WithName("webhook").WithName(Name),
		client: mgr.GetClient(),
	}
}

// ValidateCreate is a no-op — Spec.Replicas of a freshly created PCSG cannot collide
// with an in-progress Coherent update for it.
func (h *Handler) ValidateCreate(_ context.Context, _ *grovecorev1alpha1.PodCliqueScalingGroup) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate rejects Spec.Replicas changes on a PodCliqueScalingGroup while the PCS replica that
// owns it is currently under a Coherent update (the replica is in Status.UpdateProgress.CurrentlyUpdating
// with UpdateEndedAt unset). Replicas of the same PodCliqueSet that are not currently under update may
// still scale. All other field changes are allowed (immutability of other spec fields is enforced by
// the PCS validating webhook on the owning template).
func (h *Handler) ValidateUpdate(ctx context.Context, oldPCSG, newPCSG *grovecorev1alpha1.PodCliqueScalingGroup) (admission.Warnings, error) {
	if oldPCSG.Spec.Replicas == newPCSG.Spec.Replicas {
		return nil, nil
	}

	pcs, err := componentutils.GetPodCliqueSet(ctx, h.client, newPCSG.ObjectMeta)
	if err != nil {
		// If the owning PCS cannot be resolved (deleted, stale label) we cannot tell whether a
		// Coherent update is in progress; fail open and allow the change rather than blocking
		// recovery actions on an orphaned PCSG.
		if apierrors.IsNotFound(err) {
			h.logger.Info("Owning PodCliqueSet not found; allowing PCSG replica change",
				"pcsg", client.ObjectKeyFromObject(newPCSG))
			return nil, nil
		}
		return nil, errors.WrapError(err, ErrValidateUpdatePodCliqueScalingGroup, "Update",
			fmt.Sprintf("failed to get owning PodCliqueSet for PodCliqueScalingGroup %s/%s", newPCSG.Namespace, newPCSG.Name))
	}

	pcsReplicaIndex, err := componentutils.GetPCSReplicaIndexFromObjectMeta(newPCSG.ObjectMeta)
	if err != nil {
		return nil, errors.WrapError(err, ErrValidateUpdatePodCliqueScalingGroup, "Update",
			fmt.Sprintf("could not determine PodCliqueSet replica index for PodCliqueScalingGroup %s/%s", newPCSG.Namespace, newPCSG.Name))
	}
	if componentutils.IsPCSReplicaUnderCoherentUpdate(pcs, pcsReplicaIndex) {
		return nil, fmt.Errorf("spec.replicas changes are not allowed while a coherent update is in progress on PodCliqueSet %s/%s replica %d; complete the update of this replica before scaling",
			pcs.Namespace, pcs.Name, pcsReplicaIndex)
	}
	return nil, nil
}

// ValidateDelete is a no-op.
func (h *Handler) ValidateDelete(_ context.Context, _ *grovecorev1alpha1.PodCliqueScalingGroup) (admission.Warnings, error) {
	return nil, nil
}
