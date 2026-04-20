// /*
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
// */

package podclique

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errListPodClique               grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUE"
	errSyncPodClique               grovecorev1alpha1.ErrorCode = "ERR_SYNC_PODCLIQUE"
	errDeletePodClique             grovecorev1alpha1.ErrorCode = "ERR_DELETE_PODCLIQUE"
	errCodeListPodCliques          grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUES"
	errCodeCreateOrUpdatePodClique grovecorev1alpha1.ErrorCode = "ERR_CREATE_OR_UPDATE_PODCLIQUE"
)

type _resource struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
}

// New creates an instance of PodClique components operator.
func New(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client:        client,
		scheme:        scheme,
		eventRecorder: eventRecorder,
	}
}

// GetExistingResourceNames returns the names of all the existing resources that the PodClique Operator manages.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	names, _, err := r.listExistingPCLQs(ctx, logger, pcsObjMeta)
	return names, err
}

// listExistingPCLQs returns the owned PodClique names and a name→spec-hash map extracted from
// the single PartialObjectMetadata list call. The spec-hash map lets the createOrUpdate path
// short-circuit no-op reconciles without an extra per-object Get.
func (r _resource) listExistingPCLQs(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, map[string]string, error) {
	logger.Info("Looking for existing PodCliques")
	pclqPartialObjMetaList, err := k8sutils.ListExistingPartialObjectMetadata(ctx,
		r.client,
		grovecorev1alpha1.SchemeGroupVersion.WithKind("PodClique"),
		pcsObjMeta,
		getPodCliqueSelectorLabels(pcsObjMeta))
	if err != nil {
		return nil, nil, groveerr.WrapError(err,
			errCodeListPodCliques,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing PodCliques for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	names, hashes := k8sutils.FilterOwnedResourceNamesAndAnnotation(pcsObjMeta, pclqPartialObjMetaList, apiconstants.AnnotationSpecHash)
	return names, hashes, nil
}

// Sync synchronizes all resources that the PodClique Operator manages.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	existingPCLQFQNs, existingSpecHashes, err := r.listExistingPCLQs(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return groveerr.WrapError(err,
			errSyncPodClique,
			component.OperationSync,
			fmt.Sprintf("Unable to fetch existing PodClique names for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}

	if err := r.triggerDeletionOfExcessPCLQs(ctx, logger, pcs, existingPCLQFQNs); err != nil {
		return err
	}
	if err := r.createOrUpdatePCLQs(ctx, logger, pcs, existingPCLQFQNs, existingSpecHashes); err != nil {
		return err
	}

	return nil
}

// triggerDeletionOfExcessPCLQs deletes PodCliques that exceed the desired replica count.
func (r _resource) triggerDeletionOfExcessPCLQs(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, existingPCLQFQNs []string) error {
	expectedPCLQFQNs := componentutils.GetPodCliqueFQNsForPCSNotInPCSG(pcs)
	// Check if the number of existing PodCliques is greater than expected, if so, we need to delete the extra ones.
	diff := len(existingPCLQFQNs) - len(expectedPCLQFQNs)
	if diff > 0 {
		logger.Info("Found more PodCliques than expected", "expected", expectedPCLQFQNs, "existing", existingPCLQFQNs)
		logger.Info("Triggering deletion of extra PodCliques", "count", diff)
		// collect the names of the extra PodCliques to delete
		deletionCandidateNames, err := getPodCliqueNamesToDelete(pcs.Name, int(pcs.Spec.Replicas), existingPCLQFQNs)
		if err != nil {
			return err
		}
		deletePCLQTasks := r.createDeleteTasks(logger, pcs, deletionCandidateNames)
		return r.triggerDeletionOfPodCliques(ctx, logger, client.ObjectKeyFromObject(pcs), deletePCLQTasks)
	}
	return nil
}

// createOrUpdatePCLQs creates or updates all expected PodCliques for the PodCliqueSet.
// existingSpecHashes, extracted from the initial list call, lets doCreateOrUpdate skip
// no-op reconciles without a per-PodClique Get.
func (r _resource) createOrUpdatePCLQs(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, existingPCLQFQNs []string, existingSpecHashes map[string]string) error {
	expectedPCLQNames, _ := componentutils.GetExpectedPCLQNamesGroupByOwner(pcs)
	// Precompute the pod-template hash once per template (it's identical across replicas).
	// Without this we'd run dump.ForHash for every one of the N replicas × M templates PodCliques.
	templateHashes := make(map[string]string, len(pcs.Spec.Template.Cliques))
	for _, ts := range pcs.Spec.Template.Cliques {
		templateHashes[ts.Name] = componentutils.ComputePCLQPodTemplateHash(ts, pcs.Spec.Template.PriorityClassName)
	}
	tasks := make([]utils.Task, 0, len(expectedPCLQNames))

	for pcsReplica := range pcs.Spec.Replicas {
		for _, expectedPCLQName := range expectedPCLQNames {
			pclqObjectKey := client.ObjectKey{
				Name:      apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: int(pcsReplica)}, expectedPCLQName),
				Namespace: pcs.Namespace,
			}
			pclqExists := slices.Contains(existingPCLQFQNs, pclqObjectKey.Name)
			cachedSpecHash := existingSpecHashes[pclqObjectKey.Name]
			createOrUpdateTask := utils.Task{
				Name: fmt.Sprintf("CreateOrUpdatePodClique-%s", pclqObjectKey),
				Fn: func(ctx context.Context) error {
					return r.doCreateOrUpdate(ctx, logger, pcs, pcsReplica, pclqObjectKey, pclqExists, cachedSpecHash, templateHashes)
				},
			}
			tasks = append(tasks, createOrUpdateTask)
		}
	}
	if runResult := utils.RunConcurrently(ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errSyncPodClique,
			component.OperationSync,
			fmt.Sprintf("Error Create of PodCliques for PodCliqueSet: %v, run summary: %s", client.ObjectKeyFromObject(pcs), runResult.GetSummary()),
		)
	}
	return nil
}

// triggerDeletionOfPodCliques executes deletion tasks for PodCliques.
func (r _resource) triggerDeletionOfPodCliques(ctx context.Context, logger logr.Logger, pcsObjKey client.ObjectKey, deletionTasks []utils.Task) error {
	if len(deletionTasks) == 0 {
		return nil
	}
	if runResult := utils.RunConcurrently(ctx, logger, deletionTasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errDeletePodClique,
			component.OperationSync,
			fmt.Sprintf("Error deleting PodCliques for PodCliqueSet: %v", pcsObjKey.Name),
		)
	}
	logger.Info("Deleted PodCliques of PodCliqueSet", "pcsObjectKey", pcsObjKey)
	return nil
}

// createDeleteTasks generates deletion tasks for the specified PodCliques.
func (r _resource) createDeleteTasks(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, targetPCLQNames []string) []utils.Task {
	deletionTasks := make([]utils.Task, 0, len(targetPCLQNames))
	for _, pclqName := range targetPCLQNames {
		pclqObjectKey := client.ObjectKey{
			Name:      pclqName,
			Namespace: pcs.Namespace,
		}
		pclq := emptyPodClique(pclqObjectKey)
		task := utils.Task{
			Name: "DeleteExcessPodClique-" + pclqName,
			Fn: func(ctx context.Context) error {
				if err := client.IgnoreNotFound(r.client.Delete(ctx, pclq)); err != nil {
					logger.Error(err, "failed to delete excess PodClique", "objectKey", pclqObjectKey)
					r.eventRecorder.Eventf(pcs, corev1.EventTypeWarning, constants.ReasonPodCliqueDeleteFailed, "Error deleting PodClique %v: %v", pclqObjectKey, err)
					return err
				}
				logger.Info("Deleted PodClique", "pclqObjectKey", pclqObjectKey)
				r.eventRecorder.Eventf(pcs, corev1.EventTypeNormal, constants.ReasonPodCliqueDeleteSuccessful, "Deleted PodClique: %s", pclqName)
				return nil
			},
		}
		deletionTasks = append(deletionTasks, task)
	}
	return deletionTasks
}

// getPodCliqueNamesToDelete identifies PodCliques whose replica index exceeds the desired count.
func getPodCliqueNamesToDelete(pcsName string, pcsReplicas int, existingPCLQNames []string) ([]string, error) {
	pclqsToDelete := make([]string, 0, len(existingPCLQNames))
	for _, pclqName := range existingPCLQNames {
		extractedPCSReplica, err := utils.GetPodCliqueSetReplicaIndexFromPodCliqueFQN(pcsName, pclqName)
		if err != nil {
			return nil, groveerr.WrapError(err,
				errSyncPodClique,
				component.OperationSync,
				fmt.Sprintf("Failed to extract PodCliqueSet replica index from PodClique name: %s", pclqName),
			)
		}
		if extractedPCSReplica >= pcsReplicas {
			// If the extracted replica index is greater than or equal to the number of replicas in the PodCliqueSet,
			// then this PodClique is an extra one that should be deleted.
			pclqsToDelete = append(pclqsToDelete, pclqName)
		}
	}
	return pclqsToDelete, nil
}

// Delete deletes all resources that the PodClique Operator manages.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjectMeta metav1.ObjectMeta) error {
	logger.Info("Triggering deletion of PodCliques")
	existingPCLQNames, err := r.GetExistingResourceNames(ctx, logger, pcsObjectMeta)
	if err != nil {
		return groveerr.WrapError(err,
			errListPodClique,
			component.OperationDelete,
			fmt.Sprintf("Unable to fetch existing PodClique names for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjectMeta)),
		)
	}
	deleteTasks := make([]utils.Task, 0, len(existingPCLQNames))
	for _, pclqName := range existingPCLQNames {
		pclqObjectKey := client.ObjectKey{Name: pclqName, Namespace: pcsObjectMeta.Namespace}
		task := utils.Task{
			Name: "DeletePodClique-" + pclqName,
			Fn: func(ctx context.Context) error {
				if err := client.IgnoreNotFound(r.client.Delete(ctx, emptyPodClique(pclqObjectKey))); err != nil {
					return fmt.Errorf("failed to delete PodClique: %v for PodCliqueSet: %v with error: %w", pclqObjectKey, k8sutils.GetObjectKeyFromObjectMeta(pcsObjectMeta), err)
				}
				return nil
			},
		}
		deleteTasks = append(deleteTasks, task)
	}
	if runResult := utils.RunConcurrently(ctx, logger, deleteTasks); runResult.HasErrors() {
		logger.Error(runResult.GetAggregatedError(), "Error deleting PodCliques", "run summary", runResult.GetSummary())
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errDeletePodClique,
			component.OperationDelete,
			fmt.Sprintf("Error deleting PodCliques for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjectMeta)),
		)
	}

	logger.Info("Deleted PodCliques")
	return nil
}

// doCreateOrUpdate creates or updates a single PodClique resource.
// cachedSpecHash is the grove.io/spec-hash annotation value read from the list call
// (empty when the PodClique is new or the annotation is absent). When it matches the
// desired hash the reconcile is a pure no-op: no Get, no patch.
// templateHashes is a precomputed map of template-name → PodTemplateHash so each reconcile
// pays the dump.ForHash cost once per template instead of once per (replica × template).
func (r _resource) doCreateOrUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int32, pclqObjectKey client.ObjectKey, pclqExists bool, cachedSpecHash string, templateHashes map[string]string) error {
	logger.Info("Running CreateOrUpdate PodClique", "pclqObjectKey", pclqObjectKey)
	pcsObjKey := client.ObjectKeyFromObject(pcs)

	desiredSpecHash := computePCLQSpecHashForPCS(pcs, pcsReplica, pclqObjectKey, templateHashes)
	if pclqExists && desiredSpecHash != "" && cachedSpecHash == desiredSpecHash {
		logger.V(4).Info("PodClique spec hash unchanged, skipping no-op update", "pclqObjectKey", pclqObjectKey)
		return nil
	}

	pclq := emptyPodClique(pclqObjectKey)
	opResult, err := k8sutils.CreateOrUpdate(ctx, r.client, pclq, func() error {
		if err := r.buildResource(logger, pclq, pcs, int(pcsReplica), pclqExists); err != nil {
			return err
		}
		// Stamp the spec hash so future reconciles can short-circuit. buildResource assigns
		// pclq.Annotations = pclqTemplateSpec.Annotations (shared reference) so we must copy
		// before inserting our key to avoid mutating the PCS spec in memory.
		if desiredSpecHash != "" {
			merged := make(map[string]string, len(pclq.Annotations)+1)
			for k, v := range pclq.Annotations {
				merged[k] = v
			}
			merged[apiconstants.AnnotationSpecHash] = desiredSpecHash
			pclq.Annotations = merged
		}
		return nil
	})
	if err != nil {
		r.eventRecorder.Eventf(pcs, corev1.EventTypeWarning, constants.ReasonPodCliqueCreateOrUpdateFailed, "PodClique %v creation or updation failed: %v", pclqObjectKey, err)
		return groveerr.WrapError(err,
			errCodeCreateOrUpdatePodClique,
			component.OperationSync,
			fmt.Sprintf("Error creating or updating PodClique: %v for PodCliqueSet: %v", pclqObjectKey, pcsObjKey),
		)
	}

	r.eventRecorder.Eventf(pcs, corev1.EventTypeNormal, constants.ReasonPodCliqueCreateOrUpdateSuccessful, "PodClique %v created or updated successfully", pclqObjectKey)
	logger.Info("triggered create or update of PodClique for PodCliqueSet", "pcs", pcsObjKey, "pclqObjectKey", pclqObjectKey, "result", opResult)
	return nil
}

// computePCLQSpecHashForPCS returns a stable hash of every input buildResource uses to
// configure a PodClique owned directly by the PodCliqueSet. Returns "" when the template
// cannot be located (signals "unknown, do not skip"). templateHashes provides a precomputed
// ComputePCLQPodTemplateHash value per template-name so the expensive dump.ForHash runs
// once per template rather than once per (replica × template).
func computePCLQSpecHashForPCS(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int32, pclqObjectKey client.ObjectKey, templateHashes map[string]string) string {
	pclqTemplateSpec, foundAtIndex, ok := lo.FindIndexOf(pcs.Spec.Template.Cliques, func(t *grovecorev1alpha1.PodCliqueTemplateSpec) bool {
		return strings.HasSuffix(pclqObjectKey.Name, t.Name)
	})
	if !ok {
		return ""
	}
	templateHash, ok := templateHashes[pclqTemplateSpec.Name]
	if !ok {
		templateHash = componentutils.ComputePCLQPodTemplateHash(pclqTemplateSpec, pcs.Spec.Template.PriorityClassName)
	}
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s|%d|%d", templateHash, pcsReplica, foundAtIndex)
	if pcs.Spec.Template.StartupType != nil {
		_, _ = fmt.Fprintf(h, "|%s", string(*pcs.Spec.Template.StartupType))
	}
	if mnnvl.IsAutoMNNVLEnabled(pcs.Annotations) {
		_, _ = fmt.Fprint(h, "|mnnvl")
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// buildResource configures a PodClique with the desired state from the template.
func (r _resource) buildResource(logger logr.Logger, pclq *grovecorev1alpha1.PodClique, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int, pclqExists bool) error {
	var err error
	pclqObjectKey, pcsObjectKey := client.ObjectKeyFromObject(pclq), client.ObjectKeyFromObject(pcs)
	pclqTemplateSpec, foundAtIndex, ok := lo.FindIndexOf(pcs.Spec.Template.Cliques, func(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec) bool {
		return strings.HasSuffix(pclq.Name, pclqTemplateSpec.Name)
	})
	if !ok {
		logger.Info("PodClique template spec not found in PodCliqueSet", "podCliqueObjectKey", pclqObjectKey, "podCliqueSetObjectKey", pcsObjectKey)
		return groveerr.New(errSyncPodClique,
			component.OperationSync,
			fmt.Sprintf("PodCliqueTemplateSpec for PodClique: %v not found in PodCliqueSet: %v", pclqObjectKey, pcsObjectKey),
		)
	}
	// Set PodClique.ObjectMeta
	// ------------------------------------
	if err = controllerutil.SetControllerReference(pcs, pclq, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errSyncPodClique,
			component.OperationSync,
			fmt.Sprintf("Error setting controller reference for PodClique: %v", client.ObjectKeyFromObject(pclq)),
		)
	}
	pclq.Labels = getLabels(pcs, pcsReplica, pclqObjectKey, pclqTemplateSpec, apicommon.GeneratePodGangNameForPodCliqueOwnedByPodCliqueSet(pcs, pcsReplica))
	pclq.Annotations = pclqTemplateSpec.Annotations
	// set PodCliqueSpec
	// ------------------------------------
	if pclqExists {
		// If an HPA is mutating the number of replicas, then it should not be overwritten by the template spec replicas.
		currentPCLQReplicas := pclq.Spec.Replicas
		pclq.Spec = *pclqTemplateSpec.Spec.DeepCopy()
		pclq.Spec.Replicas = currentPCLQReplicas
	} else {
		pclq.Spec = *pclqTemplateSpec.Spec.DeepCopy()
	}
	var dependentPclqNames []string
	if dependentPclqNames, err = identifyFullyQualifiedStartupDependencyNames(pcs, pclq, pcsReplica, foundAtIndex); err != nil {
		return err
	}
	pclq.Spec.StartsAfter = dependentPclqNames

	// Inject MNNVL resourceClaims if enabled on PCS
	if mnnvl.IsAutoMNNVLEnabled(pcs.Annotations) {
		mnnvl.InjectMNNVLIntoPodSpec(logger, &pclq.Spec.PodSpec, apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplica})
	}

	return nil
}

// identifyFullyQualifiedStartupDependencyNames determines the PodClique startup dependencies based on StartupType.
func identifyFullyQualifiedStartupDependencyNames(pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique, pcsReplicaIndex, foundAtIndex int) ([]string, error) {
	cliqueStartupType := pcs.Spec.Template.StartupType
	if cliqueStartupType == nil {
		// Ideally this should never happen as the defaulting webhook should set it v1alpha1.CliqueStartupTypeInOrder as the default value.
		// If it is still nil, then by not returning an error we break the API contract. It is a bug that should be fixed.
		return nil, groveerr.New(errSyncPodClique, component.OperationSync, fmt.Sprintf("PodClique: %v has nil StartupType", client.ObjectKeyFromObject(pclq)))
	}
	switch *cliqueStartupType {
	case grovecorev1alpha1.CliqueStartupTypeInOrder:
		return getInOrderStartupDependencies(pcs, pcsReplicaIndex, foundAtIndex), nil
	case grovecorev1alpha1.CliqueStartupTypeExplicit:
		return getExplicitStartupDependencies(pcs, pcsReplicaIndex, pclq), nil
	default:
		return nil, nil
	}
}

// getInOrderStartupDependencies returns the previous clique as a dependency for in-order startup.
func getInOrderStartupDependencies(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex, foundAtIndex int) []string {
	if foundAtIndex == 0 {
		return nil
	}
	previousCliqueName := pcs.Spec.Template.Cliques[foundAtIndex-1].Name
	return componentutils.GenerateDependencyNamesForBasePodGang(pcs, pcsReplicaIndex, previousCliqueName)
}

// getExplicitStartupDependencies resolves explicitly declared startup dependencies.
func getExplicitStartupDependencies(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, pclq *grovecorev1alpha1.PodClique) []string {
	dependencies := make([]string, 0, len(pclq.Spec.StartsAfter))
	for _, dependency := range pclq.Spec.StartsAfter {
		dependencies = append(dependencies, componentutils.GenerateDependencyNamesForBasePodGang(pcs, pcsReplicaIndex, dependency)...)
	}
	return dependencies
}

// getPodCliqueSelectorLabels returns labels for selecting all PodCliques of a PodCliqueSet.
func getPodCliqueSelectorLabels(pcsObjectMeta metav1.ObjectMeta) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjectMeta.Name),
		map[string]string{
			apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueSetPodClique,
		},
	)
}

// getLabels constructs labels for a PodClique resource including pod template hash.
func getLabels(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int, pclqObjectKey client.ObjectKey, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, podGangName string) map[string]string {
	pclqComponentLabels := map[string]string{
		apicommon.LabelAppNameKey:               pclqObjectKey.Name,
		apicommon.LabelComponentKey:             apicommon.LabelComponentNamePodCliqueSetPodClique,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplica),
		apicommon.LabelPodGang:                  podGangName,
		apicommon.LabelPodTemplateHash:          componentutils.ComputePCLQPodTemplateHash(pclqTemplateSpec, pcs.Spec.Template.PriorityClassName),
	}
	return lo.Assign(
		pclqTemplateSpec.Labels,
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		pclqComponentLabels,
	)
}

// emptyPodClique creates an empty PodClique with only metadata set.
func emptyPodClique(objKey client.ObjectKey) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}
