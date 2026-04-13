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
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"
	groveutils "github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	errSyncPCLQLevelRC   grovecorev1alpha1.ErrorCode = "ERR_SYNC_PCLQ_RESOURCE_CLAIM"
	errDeletePCLQLevelRC grovecorev1alpha1.ErrorCode = "ERR_DELETE_PCLQ_RESOURCE_CLAIM"
)

type _resource struct {
	client client.Client
	scheme *runtime.Scheme
}

// New creates a new ResourceClaim operator for managing PCLQ-level ResourceClaims.
func New(client client.Client, scheme *runtime.Scheme) component.Operator[grovecorev1alpha1.PodClique] {
	return &_resource{
		client: client,
		scheme: scheme,
	}
}

// GetExistingResourceNames returns the names of PCLQ-level ResourceClaims
// by selecting on the grove.io/podclique label that Sync stamps on each RC.
func (r _resource) GetExistingResourceNames(ctx context.Context, _ logr.Logger, pclqObjMeta metav1.ObjectMeta) ([]string, error) {
	objMetaList := &metav1.PartialObjectMetadataList{}
	objMetaList.SetGroupVersionKind(resourcev1.SchemeGroupVersion.WithKind("ResourceClaim"))
	if err := r.client.List(ctx,
		objMetaList,
		client.InNamespace(pclqObjMeta.Namespace),
		client.MatchingLabels(pclqResourceClaimLabels(pclqObjMeta)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errSyncPCLQLevelRC,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing ResourceClaims for PCLQ %s", pclqObjMeta.Name),
		)
	}
	return k8sutils.FilterMapOwnedResourceNames(pclqObjMeta, objMetaList.Items), nil
}

// Sync creates or patches PCLQ-level ResourceClaims (AllReplicas + PerReplica)
// and cleans up stale PerReplica RCs from previous scale-in operations.
// The PodClique is the ownerReference target so that RCs are garbage-collected
// when the PCLQ is deleted.
func (r _resource) Sync(ctx context.Context, _ logr.Logger, pclq *grovecorev1alpha1.PodClique) error {
	pcs, err := componentutils.GetPodCliqueSet(ctx, r.client, pclq.ObjectMeta)
	if err != nil {
		return groveerr.WrapError(err,
			errSyncPCLQLevelRC,
			component.OperationSync,
			fmt.Sprintf("failed to get PCS for PCLQ %s", client.ObjectKeyFromObject(pclq)),
		)
	}

	cliqueName, err := groveutils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
	if err != nil {
		return groveerr.WrapError(err,
			errSyncPCLQLevelRC,
			component.OperationSync,
			fmt.Sprintf("failed to extract clique name from PCLQ %s", client.ObjectKeyFromObject(pclq)),
		)
	}
	pclqTemplateSpec := componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName)
	if pclqTemplateSpec == nil || len(pclqTemplateSpec.ResourceSharing) == 0 {
		return nil
	}
	resourceSharers := resourceclaim.ResourceSharersFromPCLQ(pclqTemplateSpec.ResourceSharing)

	labels := pclqResourceClaimLabels(pclq.ObjectMeta)
	currentReplicas := int(pclq.Spec.Replicas)

	if err := r.ensureAllReplicasRCs(ctx, pclq, pcs, resourceSharers, labels); err != nil {
		return err
	}
	if err := r.ensurePerReplicaRCs(ctx, pclq, pcs, resourceSharers, labels, currentReplicas); err != nil {
		return err
	}

	return resourceclaim.CleanupStalePerReplicaRCs(
		ctx, r.client,
		pclq.Namespace, labels,
		currentReplicas,
		apicommon.LabelPodCliquePodIndex,
	)
}

func (r _resource) ensureAllReplicasRCs(
	ctx context.Context,
	pclq *grovecorev1alpha1.PodClique,
	pcs *grovecorev1alpha1.PodCliqueSet,
	resourceSharers []resourceclaim.ResourceSharer,
	labels map[string]string,
) error {
	if err := resourceclaim.EnsureResourceClaims(
		ctx, r.client,
		pclq.Name, pclq.Namespace,
		resourceSharers,
		pcs.Spec.Template.ResourceClaimTemplates,
		labels,
		pclq, r.scheme,
		nil,
	); err != nil {
		return groveerr.WrapError(err,
			errSyncPCLQLevelRC,
			component.OperationSync,
			fmt.Sprintf("Error ensuring PCLQ AllReplicas RCs for %s", client.ObjectKeyFromObject(pclq)),
		)
	}
	return nil
}

func (r _resource) ensurePerReplicaRCs(
	ctx context.Context,
	pclq *grovecorev1alpha1.PodClique,
	pcs *grovecorev1alpha1.PodCliqueSet,
	resourceSharers []resourceclaim.ResourceSharer,
	labels map[string]string,
	currentReplicas int,
) error {
	for replicaIdx := range currentReplicas {
		idx := replicaIdx
		replicaLabels := maps.Clone(labels)
		replicaLabels[apicommon.LabelPodCliquePodIndex] = strconv.Itoa(idx)
		if err := resourceclaim.EnsureResourceClaims(
			ctx, r.client,
			pclq.Name, pclq.Namespace,
			resourceSharers,
			pcs.Spec.Template.ResourceClaimTemplates,
			replicaLabels,
			pclq, r.scheme,
			&idx,
		); err != nil {
			return groveerr.WrapError(err,
				errSyncPCLQLevelRC,
				component.OperationSync,
				fmt.Sprintf("Error ensuring PCLQ PerReplica RCs for %s rep %d", client.ObjectKeyFromObject(pclq), replicaIdx),
			)
		}
	}
	return nil
}

// pclqResourceClaimLabels returns the standard RC labels plus grove.io/podclique
// so that PCLQ-level RCs can be listed by a single label selector.
func pclqResourceClaimLabels(pclqObjMeta metav1.ObjectMeta) map[string]string {
	pcsName := componentutils.GetPodCliqueSetName(pclqObjMeta)
	labels := resourceclaim.ResourceClaimLabels(pcsName)
	labels[apicommon.LabelPodClique] = pclqObjMeta.Name
	return labels
}

// Delete explicitly deletes all PCLQ-level ResourceClaims owned by this PodClique.
// This is required because the PCLQ finalizer's verifyNoResourcesAwaitsCleanup
// blocks finalizer removal until all owned resources are gone; relying solely on
// GC would create a deadlock since GC only fires after the PCLQ is fully deleted.
func (r _resource) Delete(ctx context.Context, _ logr.Logger, pclqObjMeta metav1.ObjectMeta) error {
	labels := pclqResourceClaimLabels(pclqObjMeta)
	if err := r.client.DeleteAllOf(ctx, &resourcev1.ResourceClaim{},
		client.InNamespace(pclqObjMeta.Namespace),
		client.MatchingLabels(labels),
	); err != nil {
		return groveerr.WrapError(err,
			errDeletePCLQLevelRC,
			component.OperationDelete,
			fmt.Sprintf("Error deleting PCLQ-level ResourceClaims for %s/%s", pclqObjMeta.Namespace, pclqObjMeta.Name),
		)
	}
	return nil
}
