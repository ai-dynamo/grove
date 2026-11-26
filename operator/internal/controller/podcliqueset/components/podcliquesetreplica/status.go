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

package podcliquesetreplica

import (
	"context"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// patchPCSScalingAndUpdateProgress persists prepared status with an optimistic lock.
// Failure leaves the original PCS and expectations unchanged. A conflict requires fresh planning.
func (r *_resource) patchPCSScalingAndUpdateProgress(ctx context.Context, pcs, workingPCS *grovecorev1beta1.PodCliqueSet) (bool, error) {
	if apiequality.Semantic.DeepEqual(pcs.Status, workingPCS.Status) {
		return false, nil
	}
	patch := client.MergeFromWithOptions(pcs, client.MergeFromWithOptimisticLock{})
	if err := r.client.Status().Patch(ctx, workingPCS, patch); err != nil {
		return false, groveerr.WrapError(err, errCodeUpdatePCSStatus, component.OperationSync,
			"could not patch PodCliqueSet scaling and update progress")
	}
	r.recordPCSProgressExpectations(pcs, workingPCS)
	pcs.Status = *workingPCS.Status.DeepCopy()
	pcs.ResourceVersion = workingPCS.ResourceVersion
	return true, nil
}

// recordPCSProgressExpectations records only changed expectation purposes from a successful API response.
// Comparing UpdateProgress decides whether to record the existing selection digest, not to broaden it.
func (r *_resource) recordPCSProgressExpectations(pcs, updatedPCS *grovecorev1beta1.PodCliqueSet) {
	generationHashesChanged := !utils.EqualOptional(pcs.Status.CurrentGenerationHash, updatedPCS.Status.CurrentGenerationHash) ||
		!utils.EqualOptional(pcs.Status.AppliedSchedulingConfigurationHash, updatedPCS.Status.AppliedSchedulingConfigurationHash)
	scalingProgressChanged := !apiequality.Semantic.DeepEqual(pcs.Status.ScalingProgress, updatedPCS.Status.ScalingProgress)
	updateProgressChanged := !apiequality.Semantic.DeepEqual(pcs.Status.UpdateProgress, updatedPCS.Status.UpdateProgress)
	if generationHashesChanged {
		r.generationHashExpectations.Record(updatedPCS.ObjectMeta, *updatedPCS.Status.CurrentGenerationHash, updatedPCS.Status.AppliedSchedulingConfigurationHash)
	}
	if scalingProgressChanged {
		r.scalingProgressExpectations.Record(updatedPCS.ObjectMeta, updatedPCS.Status.ScalingProgress)
	}
	if updateProgressChanged {
		var currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress
		if updatedPCS.Status.UpdateProgress != nil {
			currentlyUpdating = updatedPCS.Status.UpdateProgress.CurrentlyUpdating
		}
		r.currentlyUpdatingExpectations.Record(updatedPCS.ObjectMeta, currentlyUpdating)
	}
}
