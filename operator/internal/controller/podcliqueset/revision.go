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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// processRevision will load, initialize, and compare controller revisions for the given PodCliqueSet.
// If there are differences, it will initiate an upgrade.
func (r *Reconciler) processRevision(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	currentRevision, err := r.loadCurrentRevision(ctx, pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error loading current revision", err)
	}

	desiredData, err := commonrevision.PodCliqueSetData(pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error serializing desired revision", err)
	}

	startUpdate := true

	if currentRevision == nil {
		if ptr.Deref(pcs.Status.ObservedGeneration, 0) == pcs.Generation {
			if err := r.updateInitialRevision(ctx, logger, pcs, &desiredData); err != nil {
				return ctrlcommon.ReconcileWithErrors("error creating initial revision", err)
			}

			startUpdate = false
		}
	} else {
		equal, err := currentRevision.MatchesOrderedCliques(desiredData.Cliques)
		if err != nil {
			return ctrlcommon.ReconcileWithErrors("error comparing desired revision", err)
		}

		if equal {
			return ctrlcommon.ContinueReconcile()
		}
	}

	if err = r.ensureControllerRevision(ctx, pcs, desiredData, startUpdate); err != nil {
		return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("error creating revision for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)), err)
	}

	return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("waiting for revision %q to be observed for PodCliqueSet: %v", *pcs.Status.CurrentRevision, client.ObjectKeyFromObject(pcs)))
}

// loadCurrentRevision will load the current controller revision from the expectations store.
// If it isn't cached or is stale, it will attempt to reload it from the cluster.
func (r *Reconciler) loadCurrentRevision(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (*commonrevision.Revision, error) {
	var (
		revision *commonrevision.Revision
		err      error
	)

	value, ok := r.pcsRevisionExpectations.Load(pcs.UID)
	if ok {
		revision = value.(*commonrevision.Revision)
		// The PodCliqueSet has a CurrentRevision set that we haven't stored. Clear the expectations cache and continue.
		if ptr.Deref(pcs.Status.CurrentRevision, "") != revision.Name() || ptr.Deref(pcs.Status.CurrentGenerationHash, "") != revision.GenerationHash() {
			revision = nil
			r.pcsRevisionExpectations.CompareAndDelete(pcs.UID, value)
		}
	}

	if revision == nil && pcs.Status.CurrentRevision != nil {
		revision, err = componentutils.GetPodCliqueSetRevision(ctx, r.client, pcs)
		if err != nil {
			return nil, err
		}

		r.pcsRevisionExpectations.Store(pcs.UID, revision)
	}

	return revision, nil
}

// updateInitialRevision is the handover point from a Grove operator that didn't use controller revisions to one that does.
// It tries to initialize the revision with the existing PodClique data to avoid unnecessary rolling upgrades if the
// calculated pod template hash changes in the future.
func (r *Reconciler) updateInitialRevision(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, data *commonrevision.Data) error {
	if generationHash := pcs.Status.CurrentGenerationHash; generationHash != nil {
		data.GenerationHash = *generationHash
	}

	pclqs, err := componentutils.ListPCLQsMatchingLabels(
		ctx,
		r.client,
		pcs.Namespace,
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
	)
	if err != nil {
		return fmt.Errorf("error getting pod cliques: %w", err)
	}

	podTemplateHashes := make(map[string]string, len(pclqs))
	for _, clique := range pclqs {
		cliqueName, err := utils.GetPodCliqueNameFromPodCliqueFQN(clique.ObjectMeta)
		if err != nil {
			return fmt.Errorf("error reading pod clique names: %w", err)
		}
		podTemplateHashes[cliqueName] = clique.Labels[apicommon.LabelPodTemplateHash]
	}

	for i, clique := range pcs.Spec.Template.Cliques {
		if podTemplateHashes[clique.Name] == "" {
			continue
		}

		// This is a copy of the pod template spec conversion code from prior to the controller revision changes were made.
		podTemplate := &corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      clique.Labels,
				Annotations: clique.Annotations,
			},
			Spec: clique.Spec.PodSpec,
		}
		podTemplate.Spec.PriorityClassName = pcs.Spec.Template.PriorityClassName

		podTemplateData, err := json.Marshal(podTemplate)
		if err != nil {
			logger.Error(err, "error marshaling JSON pod template for legacy revision", "cliqueName", clique.Name)
			podTemplateData = []byte(`{}`)
		}

		data.Cliques[i].Template = podTemplateData
		data.Cliques[i].Hash = podTemplateHashes[clique.Name]
	}

	return nil
}

// ensureControllerRevision will create a ControllerRevision object for the given PodCliqueSet and Data.
// It will also update the PodCliqueSet status.
func (r *Reconciler) ensureControllerRevision(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, data commonrevision.Data, startUpdate bool) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("could not serialize revision data: %w", err)
	}

	// {prefix: 236 chars} - {hash: 16 chars} = 253 char max
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))[:16]
	prefix := strings.TrimRight(pcs.Name[:min(len(pcs.Name), validation.DNS1123SubdomainMaxLength-17)], "-.")

	controllerRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:            prefix + "-" + hash,
			Namespace:       pcs.Namespace,
			Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet"))},
		},
		Data:     runtime.RawExtension{Raw: raw},
		Revision: pcs.Generation,
	}

	if err := client.IgnoreAlreadyExists(r.client.Create(ctx, controllerRevision)); err != nil {
		return fmt.Errorf("could not create controller revision object: %w", err)
	}

	revision, err := commonrevision.DecodeRevision(controllerRevision)
	if err != nil {
		return err
	}

	return r.initUpdateProgress(ctx, pcs, revision, startUpdate)
}

// initUpdateProgress initializes a new rolling update by resetting progress tracking.
func (r *Reconciler) initUpdateProgress(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, revision *commonrevision.Revision, startUpdate bool) error {
	pcs.Status.CurrentRevision = ptr.To(revision.Name())
	pcs.Status.CurrentGenerationHash = ptr.To(revision.GenerationHash())

	if startUpdate {
		pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
		pcs.Status.UpdatedReplicas = 0

		// OnDelete strategy sets UpdateEndedAt too, since we do not know when all the pods will manually be deleted, and gang termination is disabled when an update is in progress
		if pcs.Spec.UpdateStrategy != nil && pcs.Spec.UpdateStrategy.Type == grovecorev1alpha1.OnDeleteStrategy {
			pcs.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
		}
	}

	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return fmt.Errorf("could not update revision status for PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err)
	}

	r.pcsRevisionExpectations.Store(pcs.UID, revision)

	return nil
}

// truncateRevisionHistory will retain only the current controller revision, deleting all past data.
func (r *Reconciler) truncateRevisionHistory(ctx context.Context, _ logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	if pcs.Status.CurrentRevision == nil {
		return ctrlcommon.ContinueReconcile()
	}

	revisions := &appsv1.ControllerRevisionList{}
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)
	if err := r.client.List(ctx, revisions, client.InNamespace(pcs.Namespace), client.MatchingLabels(labels)); err != nil {
		return ctrlcommon.ReconcileWithErrors("error listing ControllerRevision history", err)
	}

	for _, revision := range revisions.Items {
		if revision.Name == *pcs.Status.CurrentRevision {
			continue
		}

		if err := client.IgnoreNotFound(r.client.Delete(ctx, &revision)); err != nil {
			return ctrlcommon.ReconcileWithErrors("error truncating ControllerRevision history",
				fmt.Errorf("could not delete ControllerRevision %v: %w", client.ObjectKeyFromObject(&revision), err))
		}
	}

	return ctrlcommon.ContinueReconcile()
}
