// /*
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
// */

package podcliqueset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"
	groveutils "github.com/ai-dynamo/grove/operator/internal/utils"
	"github.com/ai-dynamo/grove/operator/internal/utils/podtemplatehash"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type revisionSelection struct {
	name           string
	generationHash string
}

type revisionExpectation struct {
	uid       types.UID
	selection revisionSelection
}

func (r *Reconciler) processRevision(ctx context.Context, _ logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	pcsObjectName := cache.NamespacedNameAsObjectName(pcsObjectKey).String()
	currentRevision, expectationResult := r.observeRevisionExpectation(ctx, pcsObjectName, pcs)
	if ctrlcommon.ShortCircuitReconcileFlow(expectationResult) {
		return expectationResult
	}

	if pcs.Status.CurrentRevision == nil {
		return r.initializeControllerRevision(ctx, pcs, pcsObjectName)
	}

	if currentRevision == nil {
		var err error
		currentRevision, err = componentutils.GetSelectedPodCliqueSetRevision(ctx, r.client, pcs)
		if err != nil {
			return ctrlcommon.ReconcileWithErrors("error loading current ControllerRevision", err)
		}
	}

	desiredCliques, err := marshalDesiredCliques(pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error serializing desired revision", err)
	}
	equal, err := currentRevision.MatchesOrderedCliques(desiredCliques)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error comparing desired revision", err)
	}
	if equal {
		return ctrlcommon.ContinueReconcile()
	}

	for i, clique := range pcs.Spec.Template.Cliques {
		selectedHash, matches, err := currentRevision.MatchingCliqueHash(desiredCliques[i])
		if err != nil {
			return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("error comparing revision for clique %s", clique.Name), err)
		}
		if matches {
			desiredCliques[i].Hash = selectedHash
			continue
		}
		desiredCliques[i].Hash = podtemplatehash.ComputePodClique(clique, pcs.Spec.Template.PriorityClassName)
	}

	data := commonrevision.Data{
		Cliques:        desiredCliques,
		GenerationHash: computeGenerationHash(pcs),
	}
	if err = r.createAndSelectControllerRevision(ctx, pcs, pcsObjectName, data, true); err != nil {
		return ctrlcommon.ReconcileWithErrors("error creating and selecting ControllerRevision", err)
	}
	return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("waiting for selected ControllerRevision %q to be observed for PodCliqueSet: %v", *pcs.Status.CurrentRevision, pcsObjectKey))
}

func (r *Reconciler) initializeControllerRevision(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsObjectName string) ctrlcommon.ReconcileStepResult {
	legacy := pcs.Status.ObservedGeneration != nil
	if legacy {
		if *pcs.Status.ObservedGeneration != pcs.Generation {
			return ctrlcommon.ReconcileWithErrors("legacy revision migration blocked", fmt.Errorf("PodCliqueSet %v changed after its last observed generation", client.ObjectKeyFromObject(pcs)))
		}
		if pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt == nil {
			return ctrlcommon.ReconcileWithErrors("legacy revision migration blocked", fmt.Errorf("PodCliqueSet %v has an active update", client.ObjectKeyFromObject(pcs)))
		}
	} else if pcs.Status.CurrentGenerationHash != nil {
		return ctrlcommon.ReconcileWithErrors("revision initialization blocked", fmt.Errorf("PodCliqueSet %v has a generation hash but no observed generation", client.ObjectKeyFromObject(pcs)))
	}

	generationHash := computeGenerationHash(pcs)
	cliqueHashes := candidateCliqueHashes(pcs)
	if legacy {
		var err error
		cliqueHashes, err = r.legacyCliqueHashes(ctx, pcs, cliqueHashes)
		if err != nil {
			return ctrlcommon.ReconcileWithErrors("legacy revision migration blocked", err)
		}
		if pcs.Status.CurrentGenerationHash != nil {
			generationHash = *pcs.Status.CurrentGenerationHash
		}
	}

	desiredCliques, err := marshalDesiredCliques(pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error serializing desired revision", err)
	}
	for i := range desiredCliques {
		desiredCliques[i].Hash = cliqueHashes[desiredCliques[i].Name]
	}
	data := commonrevision.Data{
		Cliques:        desiredCliques,
		GenerationHash: generationHash,
	}
	if err = r.createAndSelectControllerRevision(ctx, pcs, pcsObjectName, data, false); err != nil {
		return ctrlcommon.ReconcileWithErrors("error creating and selecting ControllerRevision", err)
	}
	if legacy {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, "adopted legacy PodCliqueSet revision without mutating children")
	}
	return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("waiting for selected ControllerRevision %q to be observed for PodCliqueSet: %v", *pcs.Status.CurrentRevision, client.ObjectKeyFromObject(pcs)))
}

func marshalDesiredCliques(pcs *grovecorev1alpha1.PodCliqueSet) ([]commonrevision.CliqueData, error) {
	podTemplates := rolloutPodTemplateSpecs(pcs)
	cliques := make([]commonrevision.CliqueData, len(pcs.Spec.Template.Cliques))
	for i, clique := range pcs.Spec.Template.Cliques {
		cliques[i].Name = clique.Name
		template, err := json.Marshal(podTemplates[i])
		if err != nil {
			return nil, fmt.Errorf("could not serialize clique %s: %w", clique.Name, err)
		}
		cliques[i].Template = template
	}
	return cliques, nil
}

func candidateCliqueHashes(pcs *grovecorev1alpha1.PodCliqueSet) map[string]string {
	hashes := make(map[string]string, len(pcs.Spec.Template.Cliques))
	for _, clique := range pcs.Spec.Template.Cliques {
		hashes[clique.Name] = podtemplatehash.ComputePodClique(clique, pcs.Spec.Template.PriorityClassName)
	}
	return hashes
}

func (r *Reconciler) legacyCliqueHashes(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, hashes map[string]string) (map[string]string, error) {
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)
	pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
	if err := r.client.List(ctx, pcsgList, client.InNamespace(pcs.Namespace), client.MatchingLabels(labels)); err != nil {
		return nil, fmt.Errorf("could not list PodCliqueScalingGroups: %w", err)
	}
	pcsgUIDs := make(map[types.UID]struct{}, len(pcsgList.Items))
	for i := range pcsgList.Items {
		if metav1.IsControlledBy(&pcsgList.Items[i], pcs) {
			pcsgUIDs[pcsgList.Items[i].UID] = struct{}{}
		}
	}

	pclqList := &grovecorev1alpha1.PodCliqueList{}
	if err := r.client.List(ctx, pclqList, client.InNamespace(pcs.Namespace), client.MatchingLabels(labels)); err != nil {
		return nil, fmt.Errorf("could not list PodCliques: %w", err)
	}
	found := make(map[string]string, len(hashes))
	for i := range pclqList.Items {
		pclq := &pclqList.Items[i]
		if !pclq.DeletionTimestamp.IsZero() {
			continue
		}
		owner := metav1.GetControllerOfNoCopy(pclq)
		pcsgOwned := false
		if owner != nil {
			_, pcsgOwned = pcsgUIDs[owner.UID]
		}
		if !metav1.IsControlledBy(pclq, pcs) && !pcsgOwned {
			continue
		}
		cliqueName, err := groveutils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
		if err != nil {
			return nil, err
		}
		if _, exists := hashes[cliqueName]; !exists {
			continue
		}
		hash := pclq.Labels[apicommon.LabelPodTemplateHash]
		if hash == "" {
			return nil, fmt.Errorf("PodClique %v has no pod template hash", client.ObjectKeyFromObject(pclq))
		}
		if previous, exists := found[cliqueName]; exists && previous != hash {
			return nil, fmt.Errorf("clique %s has conflicting pod template hashes", cliqueName)
		}
		found[cliqueName] = hash
	}
	maps.Copy(hashes, found)
	return hashes, nil
}

func (r *Reconciler) ensureControllerRevision(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, data commonrevision.Data) (*appsv1.ControllerRevision, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("could not serialize ControllerRevision data: %w", err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))[:16]
	prefix := pcs.Name
	if len(prefix) > 236 {
		prefix = strings.TrimRight(prefix[:236], "-.")
	}
	revision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:            prefix + "-" + hash,
			Namespace:       pcs.Namespace,
			Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet"))},
		},
		Data:     runtime.RawExtension{Raw: raw},
		Revision: pcs.Generation,
	}
	if err = r.client.Create(ctx, revision); err == nil {
		return revision, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return nil, err
	}

	existing := &appsv1.ControllerRevision{}
	if err = r.client.Get(ctx, client.ObjectKeyFromObject(revision), existing); err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(existing, pcs) || !bytes.Equal(existing.Data.Raw, raw) {
		return nil, fmt.Errorf("ControllerRevision %v already exists with different ownership or data", client.ObjectKeyFromObject(existing))
	}
	return existing, nil
}

func (r *Reconciler) createAndSelectControllerRevision(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsObjectName string, data commonrevision.Data, startUpdate bool) error {
	revision, err := r.ensureControllerRevision(ctx, pcs, data)
	if err != nil {
		return fmt.Errorf("could not create ControllerRevision: %w", err)
	}
	selection := revisionSelection{name: revision.Name, generationHash: data.GenerationHash}
	if err = r.setRevisionStatus(ctx, pcs, pcsObjectName, selection, startUpdate); err != nil {
		return fmt.Errorf("could not select ControllerRevision: %w", err)
	}
	return nil
}

func (r *Reconciler) truncateRevisionHistory(ctx context.Context, _ logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	if !shouldTruncateRevisionHistory(pcs) {
		return ctrlcommon.ContinueReconcile()
	}

	revisions := &appsv1.ControllerRevisionList{}
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)
	if err := r.client.List(ctx, revisions, client.InNamespace(pcs.Namespace), client.MatchingLabels(labels)); err != nil {
		return ctrlcommon.ReconcileWithErrors("error listing ControllerRevision history", err)
	}
	for i := range revisions.Items {
		revision := &revisions.Items[i]
		if revision.Name == *pcs.Status.CurrentRevision || !metav1.IsControlledBy(revision, pcs) {
			continue
		}
		if err := client.IgnoreNotFound(r.client.Delete(ctx, revision)); err != nil {
			return ctrlcommon.ReconcileWithErrors("error truncating ControllerRevision history",
				fmt.Errorf("could not delete ControllerRevision %v: %w", client.ObjectKeyFromObject(revision), err))
		}
	}
	return ctrlcommon.ContinueReconcile()
}

func shouldTruncateRevisionHistory(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	// OnDelete marks orchestration complete immediately, before users replace the old Pods.
	// Retain its history because UpdateEndedAt is not proof that historical revisions are unused.
	return pcs.Status.CurrentRevision != nil && *pcs.Status.CurrentRevision != "" &&
		componentutils.IsAutoUpdateStrategy(pcs) &&
		pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt != nil
}

func (r *Reconciler) setRevisionStatus(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsObjectName string, selection revisionSelection, startUpdate bool) error {
	pcs.Status.CurrentRevision = ptr.To(selection.name)
	pcs.Status.CurrentGenerationHash = ptr.To(selection.generationHash)
	if startUpdate {
		pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
		if pcs.Spec.UpdateStrategy != nil && pcs.Spec.UpdateStrategy.Type == grovecorev1alpha1.OnDeleteStrategy {
			pcs.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
		}
		pcs.Status.UpdatedReplicas = 0
	}
	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return fmt.Errorf("could not update revision status for PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	r.pcsRevisionExpectations.Store(pcsObjectName, revisionExpectation{uid: pcs.UID, selection: selection})
	return nil
}

func (r *Reconciler) observeRevisionExpectation(ctx context.Context, pcsObjectName string, pcs *grovecorev1alpha1.PodCliqueSet) (*commonrevision.SelectedRevision, ctrlcommon.ReconcileStepResult) {
	value, ok := r.pcsRevisionExpectations.Load(pcsObjectName)
	if !ok {
		return nil, ctrlcommon.ContinueReconcile()
	}
	expected := value.(revisionExpectation)
	if expected.uid != pcs.UID {
		r.pcsRevisionExpectations.CompareAndDelete(pcsObjectName, value)
		return nil, ctrlcommon.ContinueReconcile()
	}
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	if pcs.Status.CurrentRevision == nil || pcs.Status.CurrentGenerationHash == nil ||
		*pcs.Status.CurrentRevision != expected.selection.name || *pcs.Status.CurrentGenerationHash != expected.selection.generationHash {
		return nil, ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("current revision is not up-to-date for PodCliqueSet: %v", pcsObjectKey))
	}
	selectedRevision, err := componentutils.GetSelectedPodCliqueSetRevision(ctx, r.client, pcs)
	if apierrors.IsNotFound(err) {
		return nil, ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("selected ControllerRevision %q is not yet visible for PodCliqueSet: %v", expected.selection.name, pcsObjectKey))
	}
	if err != nil {
		return nil, ctrlcommon.ReconcileWithErrors("error loading current ControllerRevision", err)
	}
	r.pcsRevisionExpectations.CompareAndDelete(pcsObjectName, value)
	return selectedRevision, ctrlcommon.ContinueReconcile()
}
