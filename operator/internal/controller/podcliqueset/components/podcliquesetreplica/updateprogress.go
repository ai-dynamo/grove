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

package podcliquesetreplica

import (
	"context"
	"fmt"
	"slices"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type pcsReplicaInfo struct {
	replicaIndex int
	pclqs        []grovecorev1alpha1.PodClique
	pcsgs        []grovecorev1alpha1.PodCliqueScalingGroup
	updateDone   bool
}

// computeUpdateProgress calculates update completion for a PCS replica.
func (pri *pcsReplicaInfo) computeUpdateProgress(pcs *grovecorev1alpha1.PodCliqueSet) {
	pcsGenerationHashCandidates := componentutils.ComputePCSGenerationHashCandidates(pcs)
	updatedPCLQs := 0
	for _, pclq := range pri.pclqs {
		expectedTemplateHashes, err := componentutils.GetExpectedPCLQPodTemplateHashCandidates(pcs, pclq.ObjectMeta)
		if err == nil && isPCLQUpdateComplete(&pclq, expectedTemplateHashes, pcsGenerationHashCandidates) {
			updatedPCLQs++
		}
	}
	updatedPCSGs := 0
	for _, pcsg := range pri.pcsgs {
		if componentutils.IsPCSGUpdateComplete(&pcsg, pcsGenerationHashCandidates.Canonical, pcsGenerationHashCandidates.Legacy) {
			updatedPCSGs++
		}
	}
	allPCLQsUpdated := updatedPCLQs == componentutils.CountStandalonePCLQs(pcs)
	allPCSGsUpdated := updatedPCSGs == len(pcs.Spec.Template.PodCliqueScalingGroupConfigs)
	pri.updateDone = allPCLQsUpdated && allPCSGsUpdated
}

// isPCLQUpdateComplete checks if a PodClique has completed its update to the target generation.
//
// A PodClique is considered updated when all three pieces have converged on the desired spec:
//   - PCLQ object: its pod-template-hash label reflects the desired template (the spec side).
//   - PCLQ status: both CurrentPodTemplateHash and CurrentPodCliqueSetGenerationHash report the
//     desired hashes (the runtime side - written by the PodClique controller only after a rolling
//     update completes).
//   - Replicas: at least MinAvailable pods are running on the new template AND ready.
//
// Each hash check accepts either the canonical or legacy hash via HashCandidates.Matches to
// support the v0.1.0-alpha.8 -> canonical hash migration window.
func isPCLQUpdateComplete(pclq *grovecorev1alpha1.PodClique, expectedPodTemplateHashes, currentPCSGenerationHashes componentutils.HashCandidates) bool {
	pclqSpecOnDesiredTemplate := expectedPodTemplateHashes.Matches(pclq.Labels[apicommon.LabelPodTemplateHash])

	pclqStatusOnDesiredTemplate := pclq.Status.CurrentPodTemplateHash != nil &&
		expectedPodTemplateHashes.Matches(*pclq.Status.CurrentPodTemplateHash)

	pclqStatusOnDesiredPCSGeneration := pclq.Status.CurrentPodCliqueSetGenerationHash != nil &&
		currentPCSGenerationHashes.Matches(*pclq.Status.CurrentPodCliqueSetGenerationHash)

	minAvailableReplicasOnDesiredTemplate := pclq.Status.UpdatedReplicas >= *pclq.Spec.MinAvailable
	minAvailableReplicasReady := pclq.Status.ReadyReplicas >= *pclq.Spec.MinAvailable

	return pclqSpecOnDesiredTemplate &&
		pclqStatusOnDesiredTemplate &&
		pclqStatusOnDesiredPCSGeneration &&
		minAvailableReplicasOnDesiredTemplate &&
		minAvailableReplicasReady
}

// getPCSReplicaInfos fetches the PCLQs and PCSGs for each PCS replica.
func (r _resource) getPCSReplicaInfos(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsIndicesToTerminate []int) ([]pcsReplicaInfo, error) {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	pclqsByPCSIndex, err := componentutils.GetPCLQsByOwnerReplicaIndex(ctx, r.client, constants.KindPodCliqueSet, client.ObjectKeyFromObject(pcs), apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("could not list PCLQs for PCS: %v", pcsObjectKey),
		)
	}
	pcsgsByPCSIndex, err := componentutils.GetPCSGsByPCSReplicaIndex(ctx, r.client, client.ObjectKeyFromObject(pcs))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCSGs,
			component.OperationSync,
			fmt.Sprintf("could not list PCSGs for PCS: %v", pcsObjectKey),
		)
	}
	replicaInfos := make([]pcsReplicaInfo, 0, pcs.Spec.Replicas)
	for pcsReplicaIndex := range int(pcs.Spec.Replicas) {
		if slices.Contains(pcsIndicesToTerminate, pcsReplicaIndex) {
			continue
		}
		replicaInfos = append(replicaInfos, pcsReplicaInfo{
			replicaIndex: pcsReplicaIndex,
			pclqs:        pclqsByPCSIndex[pcsReplicaIndex],
			pcsgs:        pcsgsByPCSIndex[pcsReplicaIndex],
		})
	}
	return replicaInfos, nil
}

// patchUpdateProgressStatus persists update progress to the PCS status using a merge patch.
func (r _resource) patchUpdateProgressStatus(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, original *grovecorev1alpha1.PodCliqueSet) error {
	if err := r.client.Status().Patch(ctx, pcs, client.MergeFrom(original)); err != nil {
		return groveerr.WrapError(
			err,
			errCodeUpdatePCSStatus,
			component.OperationSync,
			"could not patch update progress",
		)
	}
	logger.Info("Updated the PodCliqueSet status with update progress")
	return nil
}

// markCurrentReplicaUpdateDone records that the currently-updating replica finished.
// It sets UpdateEndedAt and clears InFlightPodGangs (no-op for strategies that don't use it).
func (r _resource) markCurrentReplicaUpdateDone(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	original := pcs.DeepCopy()
	pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt = ptr.To(metav1.Now())
	pcs.Status.UpdateProgress.CurrentlyUpdating[0].InFlightPodGangs = nil
	if err := r.patchUpdateProgressStatus(ctx, logger, pcs, original); err != nil {
		logger.Error(err, "failed to patch update progress", "replicaIndex", pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
		return err
	}
	return nil
}
