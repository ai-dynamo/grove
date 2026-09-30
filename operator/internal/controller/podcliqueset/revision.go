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
	"slices"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	"github.com/ai-dynamo/grove/operator/internal/utils/podtemplatehash"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// processRevision will load, initialize, and compare controller revisions for the given PodCliqueSet.
// If there are differences, it will initiate an upgrade.
func (r *Reconciler) processRevision(ctx context.Context, _ logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
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
		startUpdate, err = r.updateInitialRevision(ctx, pcs, &desiredData)
		if err != nil {
			return ctrlcommon.ReconcileWithErrors("error creating initial revision", err)
		}
	} else {
		equal, err := currentRevision.MatchesOrderedCliques(desiredData.Cliques)
		if err != nil {
			return ctrlcommon.ReconcileWithErrors("error comparing desired revision", err)
		}

		if equal {
			return ctrlcommon.ContinueReconcile()
		}
		if err := currentRevision.RetainCliqueHashes(desiredData.Cliques); err != nil {
			return ctrlcommon.ReconcileWithErrors("error retaining unchanged clique identities", err)
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

// updateInitialRevision verifies legacy identities before binding them to the
// desired templates. It returns whether a new update must be started.
func (r *Reconciler) updateInitialRevision(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, data *commonrevision.Data) (bool, error) {
	generationHash := ptr.Deref(pcs.Status.CurrentGenerationHash, "")
	if generationHash == "" {
		// Bootstrap creates children at the selected revision without starting a rolling update.
		return false, nil
	}
	templates := make([]*corev1.PodTemplateSpec, len(pcs.Spec.Template.Cliques))
	for i, clique := range pcs.Spec.Template.Cliques {
		templates[i] = podtemplatehash.PodTemplateSpec(pcs, clique)
		// Legacy hashing always used PCS priority, even for an explicit Pod
		// priority. Do not overwrite the actual template stored in the revision.
		templates[i].Spec.PriorityClassName = pcs.Spec.Template.PriorityClassName
	}
	version := slices.Index(podtemplatehash.LegacyHashes(templates...), generationHash)
	if version >= 0 {
		// This also works before scale-only generations are observed, with no
		// children, and while PCSG children still carry an older template.
		data.GenerationHash = generationHash
		for i, template := range templates {
			data.Cliques[i].Hash = podtemplatehash.LegacyHashes(template)[version]
		}
		return false, nil
	}
	if pcs.Status.ObservedGeneration == nil || *pcs.Status.ObservedGeneration == pcs.Generation {
		return false, fmt.Errorf("cannot verify legacy generation hash %q for PodCliqueSet %v", generationHash, client.ObjectKeyFromObject(pcs))
	}

	// A pending template edit can leave unchanged cliques at legacy identities.
	// Only retain hashes computed from the desired input, never an arbitrary
	// child's hash. Live specs contain injected fields and are not hash inputs.
	existingHashes, err := r.existingPodTemplateHashes(ctx, pcs)
	if err != nil {
		return false, err
	}
	for i, template := range templates {
		matches := existingHashes.Intersection(sets.New(podtemplatehash.LegacyHashes(template)...))
		if matches.Len() > 1 {
			return false, fmt.Errorf("multiple legacy identities for clique %s: %v", data.Cliques[i].Name, sets.List(matches))
		}
		for hash := range matches {
			data.Cliques[i].Hash = hash
		}
	}
	return true, nil
}

func (r *Reconciler) existingPodTemplateHashes(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (sets.Set[string], error) {
	pcsgs := &grovecorev1alpha1.PodCliqueScalingGroupList{}
	pclqs := &grovecorev1alpha1.PodCliqueList{}
	pods := &corev1.PodList{}
	options := []client.ListOption{
		client.InNamespace(pcs.Namespace),
		client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)),
	}
	// Cache omissions during this one-time migration must not become persisted
	// evidence that an unchanged component has no legacy identity.
	for _, list := range []client.ObjectList{pcsgs, pclqs, pods} {
		if err := r.apiReader.List(ctx, list, options...); err != nil {
			return nil, fmt.Errorf("could not list legacy resources for PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err)
		}
	}
	pcsgNames := sets.New(k8sutils.FilterMapOwnedResourceNames(pcs.ObjectMeta, pcsgs.Items)...)
	owners := sets.New[types.UID](pcs.UID)
	for _, pcsg := range pcsgs.Items {
		if pcsgNames.Has(pcsg.Name) {
			owners.Insert(pcsg.UID)
		}
	}
	hashes, pclqUIDs := sets.New[string](), sets.New[types.UID]()
	for _, pclq := range pclqs.Items {
		owner := metav1.GetControllerOf(&pclq)
		if owner == nil || !owners.Has(owner.UID) {
			continue
		}
		pclqUIDs.Insert(pclq.UID)
		hashes.Insert(pclq.Labels[apicommon.LabelPodTemplateHash])
	}
	for _, pod := range pods.Items {
		owner := metav1.GetControllerOf(&pod)
		if owner != nil && pclqUIDs.Has(owner.UID) {
			hashes.Insert(pod.Labels[apicommon.LabelPodTemplateHash])
		}
	}
	hashes.Delete("")
	return hashes, nil
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
	if startUpdate {
		updateProgress := &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
		// Capture pending work before replacing the previous generation and update scope.
		if componentutils.IsCoherentStrategy(pcs) {
			scope, err := r.computeCoherentUpdateScope(ctx, pcs, revision)
			if err != nil {
				return fmt.Errorf("could not compute coherent update scope for PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err)
			}
			updateProgress.InScopeStandalonePodCliques = sets.List(scope.standalonePCLQs)
			updateProgress.InScopePodCliqueScalingGroups = sets.List(scope.podCliqueScalingGroups)
		}
		pcs.Status.UpdateProgress = updateProgress
		pcs.Status.UpdatedReplicas = 0

		// OnDelete strategy sets UpdateEndedAt too, since we do not know when all the pods will manually be deleted, and gang termination is disabled when an update is in progress
		if !componentutils.IsRollingUpdateStrategy(pcs) {
			pcs.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
		}
	}

	pcs.Status.CurrentRevision = ptr.To(revision.Name())
	pcs.Status.CurrentGenerationHash = ptr.To(revision.GenerationHash())

	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return fmt.Errorf("could not update revision status for PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err)
	}

	r.pcsRevisionExpectations.Store(pcs.UID, revision)
	componentutils.CachePodCliqueSetRevision(ctx, pcs, revision)

	return nil
}

// truncateRevisionHistory deletes non-current revisions controlled by the PodCliqueSet.
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
		if revision.Name == *pcs.Status.CurrentRevision || !metav1.IsControlledBy(&revision, pcs) {
			continue
		}

		if err := client.IgnoreNotFound(r.client.Delete(ctx, &revision)); err != nil {
			return ctrlcommon.ReconcileWithErrors("error truncating ControllerRevision history",
				fmt.Errorf("could not delete ControllerRevision %v: %w", client.ObjectKeyFromObject(&revision), err))
		}
	}

	return ctrlcommon.ContinueReconcile()
}
