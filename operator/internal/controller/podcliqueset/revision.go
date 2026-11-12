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
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	controllerrevision "github.com/ai-dynamo/grove/operator/internal/utils/controllerrevision"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxControllerRevisionNameLength = 63
	// This is kept at 10 for now. We can make this configurable at some point.
	previousControllerRevisionsToKeep int64 = 10
)

// ensureUpdateRevision makes the ControllerRevisions decide and track updates, the way a StatefulSet does. It
// resolves the revision whose template matches spec.template, the update revision. When that revision changes, it
// decides whether the change replaces Pods and starts the update if it does. It writes the update revision, the
// update progress and the generation hash in one status update. It then prunes the revision history.
func (r *Reconciler) ensureUpdateRevision(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	expectationKey := updateRevisionExpectationKey(pcs)
	// If the update revision is not reflected in the informer cache yet, requeue. Deciding again on a stale status
	// would restart an update that already started.
	if !r.isUpdateRevisionExpectationSatisfied(expectationKey, pcs.Status.UpdateRevision) {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("UpdateRevision is not up-to-date for PodCliqueSet: %v", pcsObjectKey))
	}
	r.updateRevisionExpectations.Delete(expectationKey)

	revisions, err := r.listOwnedRevisions(ctx, pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to list ControllerRevisions", err)
	}
	updateRevision, err := r.createOrUpdateLatestControllerRevision(ctx, pcs, revisions)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to ensure ControllerRevision", err)
	}
	if pcs.Status.UpdateRevision != updateRevision.Name || pcs.Status.CurrentGenerationHash == nil {
		if err = r.processUpdateRevisionChange(ctx, logger, pcs, expectationKey, updateRevision.Name); err != nil {
			return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("could not process the update revision of PodCliqueSet: %v", pcsObjectKey), err)
		}
	}
	if err = r.cleanupControllerRevisions(ctx, pcs, revisions, updateRevision); err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to clean up ControllerRevisions", err)
	}
	return ctrlcommon.ContinueReconcile()
}

// updateRevisionExpectationKey returns the key for the in-memory update-revision expectation of a PCS.
// The key scheme is <pcs-namespace>/<pcs-name>/<pcs-UID>. Suffixing a UID ensures that a stale
// remnant expectation entry for the same <pcs-namespace>/<pcs-name> does not block reconciliation of
// a new PCS (with a different UID).
// See https://github.com/ai-dynamo/grove/issues/782 for context.
func updateRevisionExpectationKey(pcs *grovecorev1beta1.PodCliqueSet) string {
	return fmt.Sprintf("%s/%s", client.ObjectKeyFromObject(pcs), pcs.UID)
}

// isUpdateRevisionExpectationSatisfied checks if the cached update revision matches the last one written.
func (r *Reconciler) isUpdateRevisionExpectationSatisfied(expectationKey, updateRevision string) bool {
	expectedUpdateRevision, ok := r.updateRevisionExpectations.Load(expectationKey)
	return !ok || expectedUpdateRevision.(string) == updateRevision
}

// processUpdateRevisionChange records a new update revision. It starts an update when the change replaces Pods.
// Only then is the generation hash recomputed, so it never changes on an operator upgrade.
func (r *Reconciler) processUpdateRevisionChange(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, expectationKey, updateRevision string) error {
	previousUpdateRevision := pcs.Status.UpdateRevision
	startUpdate, scope, err := r.decideUpdate(ctx, pcs, previousUpdateRevision, updateRevision)
	if err != nil {
		return err
	}
	pcs.Status.UpdateRevision = updateRevision
	if startUpdate || pcs.Status.CurrentGenerationHash == nil {
		pcs.Status.CurrentGenerationHash = ptr.To(computeGenerationHash(&pcs.Spec.Template))
	}
	if startUpdate {
		pcs.Status.UpdateProgress = newUpdateProgress(pcs, scope)
		pcs.Status.UpdatedReplicas = 0
	}
	if err = r.client.Status().Update(ctx, pcs); err != nil {
		return fmt.Errorf("could not update UpdateRevision for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	r.updateRevisionExpectations.Store(expectationKey, updateRevision)
	logger.Info("Recorded the update revision of the PodCliqueSet", "previousUpdateRevision", previousUpdateRevision, "updateRevision", updateRevision, "updateStarted", startUpdate)
	return nil
}

// decideUpdate reports whether moving to updateRevision replaces Pods, and which components the update covers.
//   - When the previous update revision is known, the update starts only if the two revisions differ in a pod
//     template. An edit to anything else, such as replicas, starts no update, and an update in progress keeps
//     going.
//   - When the previous update revision is unknown, the PodCliques decide. This covers a PodCliqueSet created by
//     an older operator, whose children carry no revision.
//
// The scope names the components with a PodClique whose pod-template-hash changes, plus, during a coherent
// update, the components that update has not finished rolling.
func (r *Reconciler) decideUpdate(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, previousUpdateRevision, updateRevision string) (bool, updateScope, error) {
	if previousUpdateRevision == updateRevision {
		return false, newUpdateScope(), nil
	}
	startUpdate := false
	if previousUpdateRevision != "" {
		previousTemplate, err := controllerrevision.GetTemplate(ctx, r.client, pcs, previousUpdateRevision)
		switch {
		case err == nil:
			if controllerrevision.PodTemplatesEqual(previousTemplate, &pcs.Spec.Template) {
				return false, newUpdateScope(), nil
			}
			startUpdate = true
		case !apierrors.IsNotFound(err):
			return false, newUpdateScope(), err
		}
	}
	scope, err := r.computeChangedScope(ctx, pcs, updateRevision)
	if err != nil {
		return false, newUpdateScope(), err
	}
	startUpdate = startUpdate || !scope.isEmpty()
	if !startUpdate || !componentutils.IsCoherentStrategy(pcs) {
		return startUpdate, scope, nil
	}
	scope, err = r.withPreviousPendingScope(ctx, pcs, scope)
	return startUpdate, scope, err
}

// cleanupControllerRevisions keeps the update revision and the ten revision numbers before it. It never deletes the
// current revision or a revision that a PodClique or PodCliqueScalingGroup still references. History is retained
// while an update is in progress.
func (r *Reconciler) cleanupControllerRevisions(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, revisions []appsv1.ControllerRevision, updateRevision *appsv1.ControllerRevision) error {
	if pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt == nil {
		return nil
	}
	oldestToKeep := updateRevision.Revision - previousControllerRevisionsToKeep
	if oldestToKeep <= 0 {
		return nil
	}
	candidates := make([]*appsv1.ControllerRevision, 0, len(revisions))
	for i := range revisions {
		revision := &revisions[i]
		// Skip if an already existing revision was fast-fowarded.
		if revision.Name == updateRevision.Name || revision.Name == pcs.Status.CurrentRevision || revision.Revision >= oldestToKeep {
			continue
		}
		candidates = append(candidates, revision)
	}
	if len(candidates) == 0 {
		return nil
	}
	referenced, err := r.listReferencedRevisionNames(ctx, pcs)
	if err != nil {
		return err
	}
	var deleteErrors []error
	for _, revision := range candidates {
		if referenced.Has(revision.Name) {
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

// listReferencedRevisionNames returns the revisions that the PodCliques and PodCliqueScalingGroups of the
// PodCliqueSet were written from, have reached, or are updating to.
func (r *Reconciler) listReferencedRevisionNames(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet) (sets.Set[string], error) {
	referenced := sets.New[string]()
	insert := func(names ...string) {
		for _, name := range names {
			if name != "" {
				referenced.Insert(name)
			}
		}
	}
	pclqs, err := componentutils.ListPCLQsMatchingLabels(ctx, r.client, pcs.Namespace, apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))
	if err != nil {
		return nil, err
	}
	for i := range pclqs {
		pclq := &pclqs[i]
		insert(pclq.Labels[apicommon.LabelPodCliqueSetRevision], pclq.Status.CurrentPodCliqueSetRevision)
		if pclq.Status.UpdateProgress != nil {
			insert(pclq.Status.UpdateProgress.PodCliqueSetRevision)
		}
	}
	pcsgs, err := componentutils.GetPCSGsForPCS(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, err
	}
	for i := range pcsgs {
		pcsg := &pcsgs[i]
		insert(pcsg.Labels[apicommon.LabelPodCliqueSetRevision], pcsg.Status.CurrentPodCliqueSetRevision)
		if pcsg.Status.UpdateProgress != nil {
			insert(pcsg.Status.UpdateProgress.PodCliqueSetRevision)
		}
	}
	return referenced, nil
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
		pcsName = strings.TrimRight(pcsName[:maxPrefixLength], "-.")
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
