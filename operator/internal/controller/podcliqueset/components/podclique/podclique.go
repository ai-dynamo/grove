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
	"maps"
	"slices"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/hash"
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
	errComputePodSpecTemplateHash  grovecorev1alpha1.ErrorCode = "ERR_COMPUTE_POD_TEMPLATE_HASH"
)

type _resource struct {
	client               client.Client
	scheme               *runtime.Scheme
	eventRecorder            record.EventRecorder
	podTemplateSpecHashCache *hash.PodTemplateSpecHashCache
}

// New creates an instance of PodClique components operator.
func New(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, podTemplateSpecHashCache *hash.PodTemplateSpecHashCache) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client:                   client,
		scheme:                   scheme,
		eventRecorder:            eventRecorder,
		podTemplateSpecHashCache: podTemplateSpecHashCache,
	}
}

// GetExistingResourceNames returns the names of all the existing resources that the PodClique Operator manages.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	logger.Info("Looking for existing PodCliques")
	pclqPartialObjMetaList, err := k8sutils.ListExistingPartialObjectMetadata(ctx,
		r.client,
		grovecorev1alpha1.SchemeGroupVersion.WithKind("PodClique"),
		pcsObjMeta,
		getPodCliqueSelectorLabels(pcsObjMeta))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodCliques,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing PartialObjectMetadata of PodCliques for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	return k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, pclqPartialObjMetaList), nil
}

// Sync synchronizes all resources that the PodClique Operator manages.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	pclqPartialObjMetaList, err := k8sutils.ListExistingPartialObjectMetadata(ctx,
		r.client,
		grovecorev1alpha1.SchemeGroupVersion.WithKind("PodClique"),
		pcs.ObjectMeta,
		getPodCliqueSelectorLabels(pcs.ObjectMeta))
	if err != nil {
		return groveerr.WrapError(err,
			errCodeListPodCliques,
			component.OperationSync,
			fmt.Sprintf("Error listing PartialObjectMetadata of PodCliques for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}

	existingPCLQFQNToPartialObjMetas := lo.FilterSliceToMap(pclqPartialObjMetaList, func(pclqPartialObjMeta metav1.PartialObjectMetadata) (string, *metav1.ObjectMeta, bool) {
		if !metav1.IsControlledBy(pcs, pclqPartialObjMeta.GetObjectMeta()) {
			return "", nil, false
		}
		return pclqPartialObjMeta.Name, &pclqPartialObjMeta.ObjectMeta, true
	})

	existingPCLQFQNs := slices.Collect(maps.Keys(existingPCLQFQNToPartialObjMetas))

	if err = r.triggerDeletionOfExcessPCLQs(ctx, logger, pcs, existingPCLQFQNs); err != nil {
		return err
	}
	if err = r.createOrUpdatePCLQs(ctx, logger, pcs, existingPCLQFQNToPartialObjMetas); err != nil {
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

// createOrUpdatePCLQs creates or updates PodCliques to match the desired state defined in the PodCliqueSet.
// For each expected PodClique, it checks if it already exists. If it does, it updates it; if not, it creates it.
// This is done concurrently for all expected PodCliques.
func (r _resource) createOrUpdatePCLQs(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, existingPCLQObjMetas map[string]*metav1.ObjectMeta) error {
	expectedPCLQNames, _ := componentutils.GetExpectedPCLQNamesGroupByOwner(pcs)
	tasks := make([]utils.Task, 0, len(expectedPCLQNames)*int(pcs.Spec.Replicas))

	// pre-getOrCompute pod template hashes for optimization.
	newPCLQTemplateSpecHashes, err := r.computePCLQTemplateSpecHashes(pcs)
	if err != nil {
		return groveerr.WrapError(err,
			errComputePodSpecTemplateHash,
			component.OperationSync,
			fmt.Sprintf("Error computing pod template spec hashes for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}

	for pcsReplica := range pcs.Spec.Replicas {
		for _, expectedPCLQName := range expectedPCLQNames {
			pclqObjectKey := client.ObjectKey{
				Name:      apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: int(pcsReplica)}, expectedPCLQName),
				Namespace: pcs.Namespace,
			}
			newPodTemplateSpecHash := newPCLQTemplateSpecHashes[expectedPCLQName]
			existingPCLQObjMeta, _ := existingPCLQObjMetas[pclqObjectKey.Name]
			createOrUpdatePCLQTask := utils.Task{
				Name: fmt.Sprintf("CreateOrUpdatePodClique-%s", pclqObjectKey.Name),
				Fn: func(ctx context.Context) error {
					return r.doCreateOrUpdate(ctx, logger, pcs, pcsReplica, pclqObjectKey, existingPCLQObjMeta, newPodTemplateSpecHash)
				},
			}
			tasks = append(tasks, createOrUpdatePCLQTask)
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

// computePCLQTemplateSpecHashes gets or computes the pod template spec hashes for all PodCliqueTemplateSpecs in the PodCliqueSet.
// It returns a map of PodCliqueTemplateSpec name to its corresponding pod template spec hash. This allows us to compute
// the hash once per template spec and reuse it for all replica.
func (r _resource) computePCLQTemplateSpecHashes(pcs *grovecorev1alpha1.PodCliqueSet) (map[string]string, error) {
	pclqTemplateSpecHashes := make(map[string]string, len(pcs.Spec.Template.Cliques))
	for _, pclqTemplateSpec := range pcs.Spec.Template.Cliques {
		podTemplateSpecHash, err := r.podTemplateSpecHashCache.GetOrCompute(pcs.Name, pcs.Generation, pclqTemplateSpec, pcs.Spec.Template.PriorityClassName)
		if err != nil {
			return nil, err
		}
		pclqTemplateSpecHashes[pclqTemplateSpec.Name] = podTemplateSpecHash
	}
	return pclqTemplateSpecHashes, nil
}

// doCreateOrUpdate creates or updates a single PodClique resource.
func (r _resource) doCreateOrUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int32, pclqObjectKey client.ObjectKey, existingPCLQObjMeta *metav1.ObjectMeta, expectedPodTemplateSpecHash string) error {
	logger.Info("Running CreateOrUpdate PodClique", "pclqObjectKey", pclqObjectKey)

	pclqExists := existingPCLQObjMeta != nil
	// OPTIMIZATION: If the PodClique already exists, check if we can skip the update by comparing the pod template hash.
	if pclqExists && r.shouldSkipUpdate(logger, *existingPCLQObjMeta, expectedPodTemplateSpecHash) {
		logger.V(3).Info("PodClique unchanged (hash match), skipping CreateOrPatch")
		return nil
	}

	pclq := emptyPodClique(pclqObjectKey)
	pcsObjKey := client.ObjectKeyFromObject(pcs)

	opResult, err := controllerutil.CreateOrPatch(ctx, r.client, pclq, func() error {
		return r.buildResource(logger, pcs, int(pcsReplica), pclq, pclqExists, expectedPodTemplateSpecHash)
	})
	if err != nil {
		r.eventRecorder.Eventf(pcs, corev1.EventTypeWarning, constants.ReasonPodCliqueCreateOrUpdateFailed, "PodClique %v creation or updation failed: %v", pclqObjectKey, err)
		return groveerr.WrapError(err,
			errCodeCreateOrUpdatePodClique,
			component.OperationSync,
			fmt.Sprintf("Error creating or updating PodClique: %v for PodCliqueSet: %v", pclqObjectKey, pcsObjKey),
		)
	}

	if opResult != controllerutil.OperationResultNone {
		r.eventRecorder.Eventf(pcs, corev1.EventTypeNormal, constants.ReasonPodCliqueCreateOrUpdateSuccessful, "PodClique %v %s successfully", pclqObjectKey, opResult)
	}
	logger.Info("triggered create or update of PodClique for PodCliqueSet", "pcs", pcsObjKey, "pclqObjectKey", pclqObjectKey, "result", opResult)
	return nil
}

// shouldSkipUpdate determines if a PodClique needs updating by comparing pod template hash.
// Returns true if the update can be skipped (no changes detected).
func (r _resource) shouldSkipUpdate(logger logr.Logger, existingPCLQObjMeta metav1.ObjectMeta, expectedHash string) bool {
	currentHash, ok := existingPCLQObjMeta.Labels[apicommon.LabelPodTemplateHash]
	if !ok {
		logger.Info("PodClique missing pod template hash label, proceeding with update")
		return false
	}
	return currentHash == expectedHash
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

// buildResource configures a PodClique with the desired state from the template.
func (r _resource) buildResource(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int, pclq *grovecorev1alpha1.PodClique, pclqExists bool, podTemplateSpecHash string) error {
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
	if err := controllerutil.SetControllerReference(pcs, pclq, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errSyncPodClique,
			component.OperationSync,
			fmt.Sprintf("Error setting controller reference for PodClique: %v", client.ObjectKeyFromObject(pclq)),
		)
	}
	pclq.Labels = getLabels(pcs, pcsReplica, pclqObjectKey, pclqTemplateSpec, apicommon.GeneratePodGangNameForPodCliqueOwnedByPodCliqueSet(pcs, pcsReplica), podTemplateSpecHash)
	pclq.Annotations = pclqTemplateSpec.Annotations

	// set PodCliqueSpec
	// ------------------------------------
	if pclqExists {
		// If an HPA is mutating the number of replicas, then it should not be overwritten by the template spec replicas.
		currentPCLQReplicas := pclq.Spec.Replicas
		pclq.Spec = pclqTemplateSpec.Spec
		pclq.Spec.Replicas = currentPCLQReplicas
	} else {
		pclq.Spec = pclqTemplateSpec.Spec
	}
	dependentPclqNames, err := identifyFullyQualifiedStartupDependencyNames(pcs, pclq, pcsReplica, foundAtIndex)
	if err != nil {
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
func getLabels(pcs *grovecorev1alpha1.PodCliqueSet,
	pcsReplica int,
	pclqObjectKey client.ObjectKey,
	pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec,
	podGangName string,
	podTemplateHash string) map[string]string {

	pclqComponentLabels := map[string]string{
		apicommon.LabelAppNameKey:               pclqObjectKey.Name,
		apicommon.LabelComponentKey:             apicommon.LabelComponentNamePodCliqueSetPodClique,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplica),
		apicommon.LabelPodGang:                  podGangName,
		apicommon.LabelPodTemplateHash:          podTemplateHash, // Use pre-computed hash
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
