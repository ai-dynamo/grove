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
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
func New(client client.Client, scheme *runtime.Scheme) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client: client,
		scheme: scheme,
	}
}

// GetExistingResourceNames returns the names of all ResourceClaims owned by this PCS.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	objMetaList := &metav1.PartialObjectMetadataList{}
	objMetaList.SetGroupVersionKind(resourcev1.SchemeGroupVersion.WithKind("ResourceClaim"))
	if err := r.client.List(ctx,
		objMetaList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(resourceclaim.ResourceClaimLabels(pcsObjMeta.Name)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errSyncPCSResourceClaim,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing ResourceClaims for PCS: %s/%s", pcsObjMeta.Namespace, pcsObjMeta.Name),
		)
	}
	names := k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, objMetaList.Items)
	logger.V(1).Info("Listed existing ResourceClaims", "pcs", pcsObjMeta.Name, "count", len(names))
	return names, nil
}

// Sync creates or patches PCS-level ResourceClaims (AllReplicas + PerReplica)
// and cleans up stale RCs from previous scale-in operations.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	if len(pcs.Spec.Template.ResourceSharing) == 0 {
		return nil
	}
	resourceSharers := resourceclaim.ResourceSharersFromPCS(pcs.Spec.Template.ResourceSharing)

	labels := resourceclaim.ResourceClaimLabels(pcs.Name)
	tasks := make([]utils.Task, 0, int(pcs.Spec.Replicas)+1)

	// AllReplicas-scope: one set of RCs per PCS (not per replica)
	tasks = append(tasks, utils.Task{
		Name: "EnsurePCSAllReplicasRCs",
		Fn: func(ctx context.Context) error {
			return resourceclaim.EnsureResourceClaims(
				ctx, r.client,
				pcs.Name, pcs.Namespace,
				resourceSharers,
				pcs.Spec.Template.ResourceClaimTemplates,
				labels,
				pcs, r.scheme,
				nil,
			)
		},
	})

	// PerReplica-scope: one set of RCs per PCS replica
	for replica := range pcs.Spec.Replicas {
		replicaIdx := int(replica)
		replicaLabels := maps.Clone(labels)
		replicaLabels[apicommon.LabelPodCliqueSetReplicaIndex] = strconv.Itoa(replicaIdx)
		tasks = append(tasks, utils.Task{
			Name: fmt.Sprintf("EnsurePCSPerReplicaRCs-rep-%d", replicaIdx),
			Fn: func(ctx context.Context) error {
				return resourceclaim.EnsureResourceClaims(
					ctx, r.client,
					pcs.Name, pcs.Namespace,
					resourceSharers,
					pcs.Spec.Template.ResourceClaimTemplates,
					replicaLabels,
					pcs, r.scheme,
					&replicaIdx,
				)
			},
		})
	}

	if runResult := utils.RunConcurrently(ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errSyncPCSResourceClaim,
			component.OperationSync,
			fmt.Sprintf("Error syncing PCS-level ResourceClaims for %s", client.ObjectKeyFromObject(pcs)),
		)
	}

	// Clean up stale RCs from previous replicas (e.g. after scale-in)
	if err := r.cleanupStaleResourceClaims(ctx, logger, pcs); err != nil {
		return err
	}

	logger.V(4).Info("Successfully synced PCS-level ResourceClaims")
	return nil
}

// cleanupStaleResourceClaims deletes PCS-level PerReplica ResourceClaims that
// belong to PCS replicas that no longer exist after a scale-in.
// Uses LabelPodCliqueSetReplicaIndex for the NotIn selector, which only matches
// PCS-level RCs (PCSG/PCLQ-level RCs use different label keys).
func (r _resource) cleanupStaleResourceClaims(ctx context.Context, _ logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	labels := resourceclaim.ResourceClaimLabels(pcs.Name)
	if err := resourceclaim.CleanupStalePerReplicaRCs(
		ctx, r.client,
		pcs.Namespace, labels,
		int(pcs.Spec.Replicas),
		apicommon.LabelPodCliqueSetReplicaIndex,
	); err != nil {
		return groveerr.WrapError(err,
			errSyncPCSResourceClaim,
			component.OperationSync,
			fmt.Sprintf("Error cleaning up stale ResourceClaims for %s", client.ObjectKeyFromObject(pcs)),
		)
	}
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
