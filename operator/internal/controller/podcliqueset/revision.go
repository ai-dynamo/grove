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

package podcliqueset

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	controllerrevision "github.com/ai-dynamo/grove/operator/internal/utils/controllerrevision"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const maxControllerRevisionNameLength = 63

// ensureCurrentControllerRevision creates the desired ControllerRevision from the PodCliqueSetSpec.
// Bootstrap initializes both revision fields. Later template changes advance only UpdateRevision;
// CurrentRevision advances after the rollout completes.
func (r *Reconciler) ensureCurrentControllerRevision(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	revision, collisionCount, err := r.createOrUpdateLatestControllerRevision(ctx, pcs)
	if err != nil {
		if apierrors.IsAlreadyExists(err) && collisionCount > ptr.Deref(pcs.Status.CollisionCount, 0) {
			pcs.Status.CollisionCount = new(collisionCount)
			if result := r.updatePodCliqueSetStatus(ctx, pcs, "failed to persist ControllerRevision collision count"); result.HasErrors() {
				return result
			}
		}
		return ctrlcommon.ReconcileWithErrors("failed to bootstrap ControllerRevision", err)
	}

	// The PCS status is up to date with the latest desired ControllerRevision.
	if pcs.Status.CurrentRevision != "" &&
		revision.Name == pcs.Status.UpdateRevision &&
		ptr.Deref(pcs.Status.CollisionCount, 0) == collisionCount {
		return ctrlcommon.ContinueReconcile()
	}

	// CurrentRevision is initialized once and remains the last fully converged revision.
	if pcs.Status.CurrentRevision == "" {
		pcs.Status.CurrentRevision = revision.Name
	}
	pcs.Status.UpdateRevision = revision.Name
	if collisionCount != ptr.Deref(pcs.Status.CollisionCount, 0) {
		pcs.Status.CollisionCount = new(collisionCount)
	}
	if result := r.updatePodCliqueSetStatus(ctx, pcs, "failed to record PodCliqueSetStatus update with the latest ControllerRevision"); result.HasErrors() {
		return result
	}

	logger.Info("Reconciled PodCliqueSet ControllerRevision", "revision", revision.Name)
	return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, "waiting for ControllerRevision status to be observed")
}

func (r *Reconciler) updatePodCliqueSetStatus(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, description string) ctrlcommon.ReconcileStepResult {
	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return ctrlcommon.ReconcileWithErrors(description, fmt.Errorf("could not update status for PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err))
	}
	return ctrlcommon.ContinueReconcile()
}

// createOrUpdateLatestControllerRevision ensures that the corresponding ControllerRevision for the current PodCliqueSet is present in the cluster
func (r *Reconciler) createOrUpdateLatestControllerRevision(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (*appsv1.ControllerRevision, int32, error) {
	desiredTemplate := controllerrevision.NormalizeTemplate(&pcs.Spec.Template)
	revisions, err := r.listOwnedRevisions(ctx, pcs)
	if err != nil {
		return nil, 0, err
	}

	// nextRevision is used to create the next controllerrevision, or update an already existing older revision
	var nextRevision int64 = 1
	var equivalentRevision *appsv1.ControllerRevision
	for i := range revisions {
		revision := &revisions[i]
		if revision.Revision >= nextRevision {
			nextRevision = revision.Revision + 1
		}
		equal, compareErr := controllerrevision.DataMatchesTemplate(revision.Data.Raw, desiredTemplate)
		if compareErr == nil && equal {
			// there should always only be one controllerrevision that matches the template. its revision should be moved forward if it is lagging behind
			equivalentRevision = revision
		}
	}

	// update the controllerrevision if necessary
	if equivalentRevision != nil {
		if equivalentRevision.Revision == nextRevision-1 {
			// equivalentRevision is already the latest revision, no need to update it
			return equivalentRevision, ptr.Deref(pcs.Status.CollisionCount, 0), nil
		} else {
			// update controllerrevision to nextRevision
			equivalentRevision.Revision = nextRevision
			return equivalentRevision, ptr.Deref(pcs.Status.CollisionCount, 0), r.client.Update(ctx, equivalentRevision)
		}
	}

	// create the missing controllerrevision
	data, err := controllerrevision.EncodeTemplate(desiredTemplate)
	if err != nil {
		return nil, 0, err
	}
	collisionCount := ptr.Deref(pcs.Status.CollisionCount, 0)

	revision := buildControllerRevision(pcs, data, nextRevision, collisionCount)
	if err = r.client.Create(ctx, revision); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, collisionCount, err
	}
	if apierrors.IsAlreadyExists(err) {
		if collisionCount == int32(1<<31-1) {
			return nil, 0, fmt.Errorf("ControllerRevision collision count overflow")
		}
		collisionCount++
	}
	return revision, collisionCount, err
}

func (r *Reconciler) listOwnedRevisions(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) ([]appsv1.ControllerRevision, error) {
	list := &appsv1.ControllerRevisionList{}
	if err := r.client.List(ctx, list, client.InNamespace(pcs.Namespace), client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))); err != nil {
		return nil, err
	}
	owned := make([]appsv1.ControllerRevision, 0, len(list.Items))
	for i := range list.Items {
		// Labels identify the PCS by name; ownership also verifies its UID so revisions from a
		// previously deleted PCS with the same name are not reused.
		if metav1.IsControlledBy(&list.Items[i], pcs) {
			owned = append(owned, list.Items[i])
		}
	}
	return owned, nil
}

func buildControllerRevision(pcs *grovecorev1alpha1.PodCliqueSet, data []byte, revisionNumber int64, collisionCount int32) *appsv1.ControllerRevision {
	hash := hashRevisionData(data, collisionCount)
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)
	labels[apicommon.LabelControllerRevisionDataHash] = hash
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      controllerRevisionName(pcs.Name, hash),
			Namespace: pcs.Namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, schema.GroupVersionKind{
				Group: grovecorev1alpha1.SchemeGroupVersion.Group, Version: grovecorev1alpha1.SchemeGroupVersion.Version, Kind: apiconstants.KindPodCliqueSet,
			})},
		},
		Data:     runtime.RawExtension{Raw: data},
		Revision: revisionNumber,
	}
}

// TODO: @renormalize check if this is the right way to go ahead
func controllerRevisionName(pcsName, hash string) string {
	suffix := "-" + hash
	maxPrefixLength := maxControllerRevisionNameLength - len(suffix)
	if len(pcsName) > maxPrefixLength {
		pcsName = strings.TrimRight(pcsName[:maxPrefixLength], "-")
	}
	if pcsName == "" {
		return hash
	}
	return pcsName + suffix
}

// TODO: @renormalize verify this function. Check if a helper can be made for this since there are other places that compute hash
func hashRevisionData(data []byte, collisionCount int32) string {
	hasher := fnv.New64a()
	_, _ = hasher.Write(data)
	_ = binary.Write(hasher, binary.LittleEndian, collisionCount)
	return utilrand.SafeEncodeString(strconv.FormatUint(hasher.Sum64(), 10))
}
