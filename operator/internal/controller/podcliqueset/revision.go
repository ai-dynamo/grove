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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	controllerrevision "github.com/ai-dynamo/grove/operator/internal/utils/controllerrevision"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxControllerRevisionNameLength = 63
	// This is kept at 10 for now. We can make this configurable at some point.
	previousControllerRevisionsToKeep int64 = 10
)

// ensureCurrentControllerRevision creates the ControllerRevision for the current PodCliqueSetSpec
// template when it is missing, fast-forwards an equivalent older revision's number, and prunes history.
func (r *Reconciler) ensureCurrentControllerRevision(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	revisions, err := r.listOwnedRevisions(ctx, pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to list ControllerRevisions", err)
	}
	current, err := r.createOrUpdateLatestControllerRevision(ctx, pcs, revisions)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to ensure ControllerRevision", err)
	}
	if err := r.cleanupControllerRevisions(ctx, pcs, revisions, current); err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to clean up ControllerRevisions", err)
	}
	logger.V(1).Info("Reconciled PodCliqueSet ControllerRevision")
	return ctrlcommon.ContinueReconcile()
}

// cleanupControllerRevisions keeps the current revision and its previous ten revision numbers.
// History is retained while an update is in progress.
func (r *Reconciler) cleanupControllerRevisions(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, revisions []appsv1.ControllerRevision, current *appsv1.ControllerRevision) error {
	if pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt == nil {
		return nil
	}
	oldestToKeep := current.Revision - previousControllerRevisionsToKeep
	if oldestToKeep <= 0 {
		return nil
	}
	var deleteErrors []error
	for i := range revisions {
		revision := &revisions[i]
		// Skip if an already existing revision was fast-fowarded.
		if revision.Name == current.Name || revision.Revision >= oldestToKeep {
			continue
		}
		// Refuse to delete revisions updated since they were listed, or if the cache is not up to date.
		preconditions := client.Preconditions{UID: &revision.UID, ResourceVersion: &revision.ResourceVersion}
		if err := client.IgnoreNotFound(r.client.Delete(ctx, revision, preconditions)); err != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("failed to delete ControllerRevision %s: %w", client.ObjectKeyFromObject(revision), err))
		}
	}
	return errors.Join(deleteErrors...)
}

// createOrUpdateLatestControllerRevision ensures that the corresponding ControllerRevision for the current PodCliqueSet is present in the cluster
func (r *Reconciler) createOrUpdateLatestControllerRevision(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, revisions []appsv1.ControllerRevision) (*appsv1.ControllerRevision, error) {
	desiredTemplate := controllerrevision.NormalizeTemplate(&pcs.Spec.Template)

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
			return equivalentRevision, nil
		}
		// update controllerrevision to nextRevision
		equivalentRevision.Revision = nextRevision
		return equivalentRevision, r.client.Update(ctx, equivalentRevision)
	}

	// create the missing controllerrevision
	data, err := controllerrevision.EncodeTemplate(desiredTemplate)
	if err != nil {
		return nil, err
	}
	return r.createControllerRevision(ctx, pcs, data, nextRevision)
}

// createControllerRevision creates the ControllerRevision for data at nextRevision. The revision name
// is derived from a content hash plus a collision count. A name clash with an existing revision that
// stores identical data adopts that revision (idempotent create); a clash with different data bumps the
// collision count and retries, so a foreign revision holding the hashed name cannot wedge creation.
func (r *Reconciler) createControllerRevision(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, data []byte, nextRevision int64) (*appsv1.ControllerRevision, error) {
	for collisionCount := int32(0); ; collisionCount++ {
		revision := buildControllerRevision(pcs, data, nextRevision, collisionCount)
		err := r.client.Create(ctx, revision)
		if err == nil {
			return revision, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		existing := &appsv1.ControllerRevision{}
		if getErr := r.client.Get(ctx, client.ObjectKeyFromObject(revision), existing); getErr != nil {
			return nil, getErr
		}
		if bytes.Equal(existing.Data.Raw, data) && metav1.IsControlledBy(existing, pcs) {
			return existing, nil
		}
		if collisionCount == int32(1<<31-1) {
			return nil, fmt.Errorf("ControllerRevision collision count overflow for PodCliqueSet %s", client.ObjectKeyFromObject(pcs))
		}
	}
}

func (r *Reconciler) listOwnedRevisions(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet) ([]appsv1.ControllerRevision, error) {
	list := &appsv1.ControllerRevisionList{}
	if err := r.client.List(ctx, list, client.InNamespace(pcs.Namespace), client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))); err != nil {
		return nil, err
	}
	owned := make([]appsv1.ControllerRevision, 0, len(list.Items))
	for i := range list.Items {
		// Ensure a new PCS with the same name does not adopt ControllerRevisions.
		if metav1.IsControlledBy(&list.Items[i], pcs) {
			owned = append(owned, list.Items[i])
		}
	}
	return owned, nil
}

func buildControllerRevision(pcs *grovecorev1beta1.PodCliqueSet, data []byte, revisionNumber int64, collisionCount int32) *appsv1.ControllerRevision {
	hash := hashRevisionData(data, collisionCount)
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)
	labels[apicommon.LabelControllerRevisionDataHash] = hash
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      controllerRevisionName(pcs.Name, hash),
			Namespace: pcs.Namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, schema.GroupVersionKind{
				Group: grovecorev1beta1.SchemeGroupVersion.Group, Version: grovecorev1beta1.SchemeGroupVersion.Version, Kind: apiconstants.KindPodCliqueSet,
			})},
		},
		Data:     runtime.RawExtension{Raw: data},
		Revision: revisionNumber,
	}
}

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

func hashRevisionData(data []byte, collisionCount int32) string {
	hasher := fnv.New64a()
	_, _ = hasher.Write(data)
	_ = binary.Write(hasher, binary.LittleEndian, collisionCount)
	return utilrand.SafeEncodeString(strconv.FormatUint(hasher.Sum64(), 10))
}
