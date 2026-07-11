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
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveutils "github.com/ai-dynamo/grove/operator/internal/utils"

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

func (r *Reconciler) processRevision(ctx context.Context, _ logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	pcsObjectName := cache.NamespacedNameAsObjectName(pcsObjectKey).String()
	if !r.isRevisionExpectationSatisfied(pcsObjectName, pcs.Status) {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("current revision is not up-to-date for PodCliqueSet: %v", pcsObjectKey))
	}
	r.pcsRevisionExpectations.Delete(pcsObjectName)

	if pcs.Status.CurrentRevision == nil {
		return r.initializeControllerRevision(ctx, pcs, pcsObjectName)
	}

	currentData, err := componentutils.GetPodCliqueSetRevisionData(ctx, r.client, pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error loading current ControllerRevision", err)
	}

	desired, desiredCliques, err := marshalDesiredRevision(pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error serializing desired revision", err)
	}
	if bytes.Equal(currentData.Desired, desired) {
		return ctrlcommon.ContinueReconcile()
	}

	cliqueHashes := make(map[string]string, len(pcs.Spec.Template.Cliques))
	for _, clique := range pcs.Spec.Template.Cliques {
		if old, ok := currentData.Cliques[clique.Name]; ok && bytes.Equal(old, desiredCliques[clique.Name]) {
			cliqueHashes[clique.Name] = currentData.CliqueHashes[clique.Name]
			continue
		}
		cliqueHashes[clique.Name] = componentutils.ComputePCLQPodTemplateHash(clique, pcs.Spec.Template.PriorityClassName)
	}

	data := componentutils.PodCliqueSetRevisionData{
		Version:        componentutils.PodCliqueSetRevisionDataVersion,
		Desired:        desired,
		Cliques:        desiredCliques,
		GenerationHash: computeGenerationHash(pcs),
		CliqueHashes:   cliqueHashes,
	}
	revision, err := r.ensureControllerRevision(ctx, pcs, data)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error creating ControllerRevision", err)
	}
	if err = r.setRevisionStatus(ctx, pcs, pcsObjectName, revisionSelection{revision.Name, data.GenerationHash}, true); err != nil {
		return ctrlcommon.ReconcileWithErrors("error selecting ControllerRevision", err)
	}
	return ctrlcommon.ContinueReconcile()
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

	desired, desiredCliques, err := marshalDesiredRevision(pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error serializing desired revision", err)
	}
	data := componentutils.PodCliqueSetRevisionData{
		Version:        componentutils.PodCliqueSetRevisionDataVersion,
		Desired:        desired,
		Cliques:        desiredCliques,
		GenerationHash: generationHash,
		CliqueHashes:   cliqueHashes,
	}
	revision, err := r.ensureControllerRevision(ctx, pcs, data)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error creating ControllerRevision", err)
	}
	if err = r.setRevisionStatus(ctx, pcs, pcsObjectName, revisionSelection{revision.Name, data.GenerationHash}, false); err != nil {
		return ctrlcommon.ReconcileWithErrors("error selecting ControllerRevision", err)
	}
	if legacy {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, "adopted legacy PodCliqueSet revision without mutating children")
	}
	return ctrlcommon.ContinueReconcile()
}

func marshalDesiredRevision(pcs *grovecorev1alpha1.PodCliqueSet) (json.RawMessage, map[string]json.RawMessage, error) {
	podTemplates := rolloutPodTemplateSpecs(pcs)
	desired, err := json.Marshal(podTemplates)
	if err != nil {
		return nil, nil, fmt.Errorf("could not serialize PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	cliques := make(map[string]json.RawMessage, len(pcs.Spec.Template.Cliques))
	for i, clique := range pcs.Spec.Template.Cliques {
		cliques[clique.Name], err = json.Marshal(podTemplates[i])
		if err != nil {
			return nil, nil, fmt.Errorf("could not serialize clique %s: %w", clique.Name, err)
		}
	}
	return desired, cliques, nil
}

func candidateCliqueHashes(pcs *grovecorev1alpha1.PodCliqueSet) map[string]string {
	hashes := make(map[string]string, len(pcs.Spec.Template.Cliques))
	for _, clique := range pcs.Spec.Template.Cliques {
		hashes[clique.Name] = componentutils.ComputePCLQPodTemplateHash(clique, pcs.Spec.Template.PriorityClassName)
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
	for cliqueName, hash := range found {
		hashes[cliqueName] = hash
	}
	return hashes, nil
}

func (r *Reconciler) ensureControllerRevision(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, data componentutils.PodCliqueSetRevisionData) (*appsv1.ControllerRevision, error) {
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
	r.pcsRevisionExpectations.Store(pcsObjectName, selection)
	return nil
}

func (r *Reconciler) isRevisionExpectationSatisfied(pcsObjectName string, status grovecorev1alpha1.PodCliqueSetStatus) bool {
	expected, ok := r.pcsRevisionExpectations.Load(pcsObjectName)
	if !ok || status.CurrentRevision == nil || status.CurrentGenerationHash == nil {
		return !ok
	}
	expectation := expected.(revisionSelection)
	return *status.CurrentRevision == expectation.name && *status.CurrentGenerationHash == expectation.generationHash
}
