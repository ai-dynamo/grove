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
	"fmt"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// preparePCSGenerationChange prepares generation metadata and update progress on the owned working copy.
// A true result means baseline metadata was initialized and must be persisted before further planning.
// A false result can still include a newly prepared update. This method does not persist either result.
func (r *_resource) preparePCSGenerationChange(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, reconcileTime metav1.Time) (bool, error) {
	legacyGenerationHash, schedulingConfigurationHash, combinedGenerationHash := computePCSHashes(pcs)
	if initializePCSGenerationBaseline(pcs, schedulingConfigurationHash, combinedGenerationHash) {
		return true, nil
	}

	recordedSchedulingConfigurationHash := *pcs.Status.AppliedSchedulingConfigurationHash
	if componentutils.IsRollingUpdateInProgress(pcs) || componentutils.IsPCSReplicaRecreationInProgress(pcs) {
		recordedSchedulingConfigurationHash = pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash
	}
	matchesCombinedGeneration := *pcs.Status.CurrentGenerationHash == combinedGenerationHash
	isUnchangedLegacyGeneration := *pcs.Status.CurrentGenerationHash == legacyGenerationHash &&
		schedulingConfigurationHash == recordedSchedulingConfigurationHash
	if matchesCombinedGeneration || isUnchangedLegacyGeneration {
		return false, nil
	}

	var scope updateScope
	if !requiresPCSReplicaRecreation(pcs, schedulingConfigurationHash) && componentutils.IsCoherentStrategy(pcs) {
		var err error
		scope, err = r.computeCoherentUpdateScope(ctx, pcs)
		if err != nil {
			return false, groveerr.WrapError(err, errCodeComputeCoherentUpdateScope, component.OperationSync,
				fmt.Sprintf("could not compute coherent update scope for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)))
		}
	}
	pcs.Status.UpdateProgress = preparePCSUpdateProgress(pcs, schedulingConfigurationHash, scope, reconcileTime)
	pcs.Status.CurrentGenerationHash = new(combinedGenerationHash)
	pcs.Status.UpdatedReplicas = 0
	return false, nil
}

// initializePCSGenerationBaseline fills missing metadata without rewriting an existing identity or target.
// Admission rejects scheduling changes until the applied baseline exists, so desired scheduling is a valid
// fallback for legacy objects without any recorded scheduling metadata.
func initializePCSGenerationBaseline(pcs *grovecorev1beta1.PodCliqueSet, schedulingConfigurationHash, combinedGenerationHash string) bool {
	initialized := false
	if pcs.Status.CurrentGenerationHash == nil {
		pcs.Status.CurrentGenerationHash = new(combinedGenerationHash)
		initialized = true
	}
	if pcs.Status.AppliedSchedulingConfigurationHash == nil {
		appliedSchedulingConfigurationHash := schedulingConfigurationHash
		if progress := pcs.Status.UpdateProgress; progress != nil && progress.TargetSchedulingConfigurationHash != "" {
			appliedSchedulingConfigurationHash = progress.TargetSchedulingConfigurationHash
		}
		pcs.Status.AppliedSchedulingConfigurationHash = new(appliedSchedulingConfigurationHash)
		initialized = true
	}
	isUpdateActive := componentutils.IsRollingUpdateInProgress(pcs) || componentutils.IsPCSReplicaRecreationInProgress(pcs)
	if isUpdateActive && pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash == "" {
		pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash = *pcs.Status.AppliedSchedulingConfigurationHash
		initialized = true
	}
	return initialized
}
