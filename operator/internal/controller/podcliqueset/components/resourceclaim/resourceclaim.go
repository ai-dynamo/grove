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

package resourceclaim

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	errSyncPCSResourceClaim   grovecorev1alpha1.ErrorCode = "ERR_SYNC_PCS_RESOURCE_CLAIM"
	errDeletePCSResourceClaim grovecorev1alpha1.ErrorCode = "ERR_DELETE_PCS_RESOURCE_CLAIM"
)

type _resource struct {
	client client.Client
	scheme *runtime.Scheme
}

// New creates a new ResourceClaim operator for managing PCS-level ResourceClaims.
func New(client client.Client, scheme *runtime.Scheme) component.Operator[grovecorev1alpha1.PodCliqueSet, pcssync.State, pcssync.Result] {
	return &_resource{
		client: client,
		scheme: scheme,
	}
}

// GetExistingResourceNames returns the names of all ResourceClaims owned by this PCS.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	claimList := &resourcev1.ResourceClaimList{}
	if err := r.client.List(ctx,
		claimList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(resourceclaim.ResourceClaimLabels(pcsObjMeta.Name)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errSyncPCSResourceClaim,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing ResourceClaims for PCS: %s/%s", pcsObjMeta.Namespace, pcsObjMeta.Name),
		)
	}
	names := k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, claimList.Items)
	logger.V(1).Info("Listed existing ResourceClaims", "pcs", pcsObjMeta.Name, "count", len(names))
	return names, nil
}

// Sync creates or patches PCS-level ResourceClaims and deletes claims for excess replica indices.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, state *pcssync.State) (pcssync.Result, error) {
	if len(pcs.Spec.Template.ResourceSharing) == 0 {
		return pcssync.Result{}, nil
	}

	resourceClaimsByPCSReplicaIndex, err := r.getResourceClaimsByPCSReplicaIndex(ctx, pcs)
	if err != nil {
		return pcssync.Result{}, err
	}
	replicaIndicesAwaitingResourceCleanup, err := r.triggerDeletionOfExcessResourceClaims(ctx, logger, pcs, resourceClaimsByPCSReplicaIndex, state)
	if err != nil {
		return pcssync.Result{}, err
	}
	if err := r.createOrUpdateResourceClaims(ctx, logger, pcs, state.ExpectedReplicaIndices); err != nil {
		return pcssync.Result{}, err
	}

	return pcssync.Result{
		ScaleInReplicaIndicesAwaitingResourceCleanup: replicaIndicesAwaitingResourceCleanup,
	}, nil
}

// getResourceClaimsByPCSReplicaIndex lists owned PCS-level per-replica ResourceClaims and groups them by replica index.
func (r _resource) getResourceClaimsByPCSReplicaIndex(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[int][]resourcev1.ResourceClaim, error) {
	claimList := &resourcev1.ResourceClaimList{}
	if err := r.client.List(ctx, claimList, client.InNamespace(pcs.Namespace), client.MatchingLabels(resourceclaim.ResourceClaimLabels(pcs.Name))); err != nil {
		return nil, groveerr.WrapError(err,
			errSyncPCSResourceClaim,
			component.OperationSync,
			fmt.Sprintf("Error listing ResourceClaims for PCS: %v", client.ObjectKeyFromObject(pcs)),
		)
	}

	resourceClaimsByPCSReplicaIndex := make(map[int][]resourcev1.ResourceClaim)
	for _, claim := range claimList.Items {
		if !metav1.IsControlledBy(&claim, pcs) {
			continue
		}
		replicaIndexValue, exists := claim.Labels[apicommon.LabelPodCliqueSetReplicaIndex]
		if !exists {
			continue
		}
		replicaIndex, err := strconv.Atoi(replicaIndexValue)
		if err != nil {
			return nil, groveerr.WrapError(err,
				errSyncPCSResourceClaim,
				component.OperationSync,
				fmt.Sprintf("Invalid PCS replica index label on ResourceClaim: %v", client.ObjectKeyFromObject(&claim)),
			)
		}
		resourceClaimsByPCSReplicaIndex[replicaIndex] = append(resourceClaimsByPCSReplicaIndex[replicaIndex], claim)
	}
	return resourceClaimsByPCSReplicaIndex, nil
}

// triggerDeletionOfExcessResourceClaims deletes per-replica claims outside the expected replica set and returns scale-in indices that still have claims.
func (r _resource) triggerDeletionOfExcessResourceClaims(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, resourceClaimsByReplicaIndex map[int][]resourcev1.ResourceClaim, state *pcssync.State) ([]int, error) {
	expectedReplicaIndices := sets.New(state.ExpectedReplicaIndices...)
	scaleInReplicaIndices := sets.New(state.ScaleInReplicaIndices...)
	var (
		deleteTasks                           []utils.Task
		replicaIndicesAwaitingResourceCleanup []int
	)
	for replicaIndex, claims := range resourceClaimsByReplicaIndex {
		if scaleInReplicaIndices.Has(replicaIndex) && len(claims) > 0 {
			replicaIndicesAwaitingResourceCleanup = append(replicaIndicesAwaitingResourceCleanup, replicaIndex)
		}
		if expectedReplicaIndices.Has(replicaIndex) {
			continue
		}
		for _, claim := range claims {
			claimName := claim.Name
			deleteTasks = append(deleteTasks, utils.Task{
				Name: "DeletePCSPerReplicaResourceClaim-" + claimName,
				Fn: func(ctx context.Context) error {
					return resourceclaim.DeleteResourceClaim(ctx, r.client, claimName, pcs.Namespace)
				},
			})
		}
	}

	if runResult := utils.RunConcurrentlyAndWait(ctx, logger, deleteTasks); runResult.HasErrors() {
		return nil, groveerr.WrapError(runResult.GetAggregatedError(),
			errSyncPCSResourceClaim,
			component.OperationSync,
			fmt.Sprintf("Error deleting excess ResourceClaims for %s", client.ObjectKeyFromObject(pcs)),
		)
	}
	slices.Sort(replicaIndicesAwaitingResourceCleanup)
	return replicaIndicesAwaitingResourceCleanup, nil
}

// createOrUpdateResourceClaims creates or updates PCS-level ResourceClaims for the expected replica indices.
func (r _resource) createOrUpdateResourceClaims(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, expectedReplicaIndices []int) error {
	resourceSharers := resourceclaim.ResourceSharersFromPCS(pcs.Spec.Template.ResourceSharing)
	labels := resourceclaim.ResourceClaimLabels(pcs.Name)
	tasks := make([]utils.Task, 0, len(expectedReplicaIndices)+1)
	tasks = append(tasks, utils.Task{
		Name: "EnsurePCSAllReplicasRCs",
		Fn: func(ctx context.Context) error {
			return resourceclaim.EnsureResourceClaims(ctx, r.client, pcs.Name, pcs.Namespace, resourceSharers, pcs.Spec.Template.ResourceClaimTemplates, labels, pcs, r.scheme, nil)
		},
	})

	for _, replicaIndex := range expectedReplicaIndices {
		replicaLabels := maps.Clone(labels)
		replicaLabels[apicommon.LabelPodCliqueSetReplicaIndex] = strconv.Itoa(replicaIndex)
		tasks = append(tasks, utils.Task{
			Name: fmt.Sprintf("EnsurePCSPerReplicaRCs-rep-%d", replicaIndex),
			Fn: func(ctx context.Context) error {
				return resourceclaim.EnsureResourceClaims(ctx, r.client, pcs.Name, pcs.Namespace, resourceSharers, pcs.Spec.Template.ResourceClaimTemplates, replicaLabels, pcs, r.scheme, &replicaIndex)
			},
		})
	}

	if runResult := utils.RunConcurrentlyAndWait(ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errSyncPCSResourceClaim,
			component.OperationSync,
			fmt.Sprintf("Error syncing PCS-level ResourceClaims for %s", client.ObjectKeyFromObject(pcs)),
		)
	}
	logger.V(4).Info("Successfully synced PCS-level ResourceClaims")
	return nil
}

// Delete deletes all ResourceClaims associated with this PodCliqueSet (across all
// levels: PCS, PCSG, and PCLQ). This is safe because Delete is only called during
// PCS deletion when the entire hierarchy is being torn down.
func (r _resource) Delete(ctx context.Context, _ logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	if err := r.client.DeleteAllOf(ctx, &resourcev1.ResourceClaim{},
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(resourceclaim.ResourceClaimLabels(pcsObjMeta.Name)),
	); err != nil {
		return groveerr.WrapError(err,
			errDeletePCSResourceClaim,
			component.OperationDelete,
			fmt.Sprintf("Error deleting ResourceClaims for %s/%s", pcsObjMeta.Namespace, pcsObjMeta.Name),
		)
	}
	return nil
}
