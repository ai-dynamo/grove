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
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// updateCoherentReplicaProgress records the observability fields for the replica currently under a
// coherent update. InFlightEpochs mirrors the latest current-hash epoch on the replica's PodGangMap, and
// Message summarizes the in-scope components that have not yet reached the current revision.
func (r _resource) updateCoherentReplicaProgress(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, replicaInfo pcsReplicaInfo) error {
	inFlightEpochs, err := r.inFlightEpochsForReplica(ctx, pcs, replicaInfo.replicaIndex)
	if err != nil {
		return err
	}
	original := pcs.DeepCopy()
	current := &pcs.Status.UpdateProgress.CurrentlyUpdating[0]
	current.InFlightEpochs = inFlightEpochs
	current.Message = coherentProgressMessage(pcs, replicaInfo)
	return r.patchUpdateProgressStatus(ctx, logger, pcs, original)
}

// inFlightEpochsForReplica returns the latest current-hash epoch on the replica's PodGangMap, the batch
// the coherent engine is currently rolling. It is a list to leave room for a future iteration that rolls
// more than one batch at a time. It is empty when no PodGangMap or no current-hash entry exists yet.
func (r _resource) inFlightEpochsForReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int) ([]string, error) {
	pgm, err := componentutils.GetPodGangMap(ctx, r.client, client.ObjectKeyFromObject(pcs), pcsReplicaIndex)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, groveerr.WrapError(err, errCodeGetPodGangMap, component.OperationSync,
			fmt.Sprintf("could not get PodGangMap for PodCliqueSet %v replica %d", client.ObjectKeyFromObject(pcs), pcsReplicaIndex))
	}
	latestEpoch, err := componentutils.LatestEpochForGenerationHash(pgm.Spec.Entries, *pcs.Status.CurrentGenerationHash)
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeInvalidEpoch, component.OperationSync,
			fmt.Sprintf("could not determine the latest current-hash epoch for PodCliqueSet %v replica %d", client.ObjectKeyFromObject(pcs), pcsReplicaIndex))
	}
	if latestEpoch == nil {
		return nil, nil
	}
	return []string{*latestEpoch}, nil
}

// coherentProgressMessage summarizes how many in-scope components of the replica have converged to the
// current revision, or returns nil once all have. It reports counts rather than names so the message
// stays bounded regardless of how many components an update touches.
func coherentProgressMessage(pcs *grovecorev1alpha1.PodCliqueSet, replicaInfo pcsReplicaInfo) *string {
	inScopeStandalone, updatedStandalone, inScopePCSG, updatedPCSG := inScopeUpdateCounts(pcs, replicaInfo)
	if updatedStandalone == inScopeStandalone && updatedPCSG == inScopePCSG {
		return nil
	}
	var parts []string
	if inScopeStandalone > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d standalone PodCliques", updatedStandalone, inScopeStandalone))
	}
	if inScopePCSG > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d PodCliqueScalingGroups", updatedPCSG, inScopePCSG))
	}
	return ptr.To(fmt.Sprintf("%s updated to the current revision", strings.Join(parts, " and ")))
}

// inScopeUpdateCounts returns, for the update's in-scope components, how many are in scope and how many
// have converged to the current generation hash for the given replica, split by standalone PodCliques
// and PodCliqueScalingGroups.
func inScopeUpdateCounts(pcs *grovecorev1alpha1.PodCliqueSet, replicaInfo pcsReplicaInfo) (inScopeStandalone, updatedStandalone, inScopePCSG, updatedPCSG int) {
	inScopeStandaloneNames := sets.New(pcs.Status.UpdateProgress.InScopeStandalonePodCliques...)
	inScopePCSGNames := sets.New(pcs.Status.UpdateProgress.InScopePodCliqueScalingGroups...)
	inScopeStandalone = inScopeStandaloneNames.Len()
	inScopePCSG = inScopePCSGNames.Len()
	for i := range replicaInfo.pclqs {
		pclq := &replicaInfo.pclqs[i]
		cliqueName, err := componentutils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
		if err != nil {
			continue
		}
		if inScopeStandaloneNames.Has(cliqueName) && componentutils.IsPCLQUpdateComplete(pcs, pclq) {
			updatedStandalone++
		}
	}
	pcsNameReplica := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaInfo.replicaIndex}
	for i := range replicaInfo.pcsgs {
		pcsg := &replicaInfo.pcsgs[i]
		pcsgName, err := apicommon.ExtractScalingGroupNameFromPCSGFQN(pcsg.Name, pcsNameReplica)
		if err != nil {
			continue
		}
		if inScopePCSGNames.Has(pcsgName) && componentutils.IsPCSGUpdateComplete(pcsg, *pcs.Status.CurrentGenerationHash) {
			updatedPCSG++
		}
	}
	return
}
