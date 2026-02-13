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
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errCodeListPodClique                                 grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUE"
	errCodeMissingStartupType                            grovecorev1alpha1.ErrorCode = "ERR_UNDEFINED_STARTUP_TYPE"
	errCodeSetPodCliqueOwnerReference                    grovecorev1alpha1.ErrorCode = "ERR_SET_PODCLIQUE_OWNER_REFERENCE"
	errCodeBuildPodClique                                grovecorev1alpha1.ErrorCode = "ERR_BUILD_PODCLIQUE"
	errCodeCreatePodCliques                              grovecorev1alpha1.ErrorCode = "ERR_CREATE_PODCLIQUES"
	errCodeDeletePodClique                               grovecorev1alpha1.ErrorCode = "ERR_DELETE_PODCLIQUE"
	errCodeGetPodCliqueSet                               grovecorev1alpha1.ErrorCode = "ERR_GET_PODCLIQUESET"
	errCodeMissingPCSReplicaIndex                        grovecorev1alpha1.ErrorCode = "ERR_MISSING_PODCLIQUESET_REPLICA_INDEX"
	errCodePCSReplicaIndexIntConversion                  grovecorev1alpha1.ErrorCode = "ERR_PODCLIQUESET_REPLICA_INDEX_CONVERSION"
	errCodeListPodCliquesForPCSG                         grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUE_FOR_PCSG"
	errCodeCreatePodClique                               grovecorev1alpha1.ErrorCode = "ERR_CREATE_PODCLIQUE"
	errCodeParsePodCliqueScalingGroupReplicaIndex        grovecorev1alpha1.ErrorCode = "ERR_PARSE_PODCLIQUESCALINGGROUP_REPLICA_INDEX"
	errCodeUpdateStatus                                  grovecorev1alpha1.ErrorCode = "ERR_UPDATE_STATUS"
	errCodeComputePendingPodCliqueScalingGroupUpdateWork grovecorev1alpha1.ErrorCode = "ERR_COMPUTE_PENDINGUPDATE_WORK"
	errComputePodSpecTemplateHash                        grovecorev1alpha1.ErrorCode = "ERR_COMPUTE_POD_TEMPLATE_HASH"
)

var (
	errPCCGMinAvailableBreached = errors.New("minAvailable has been breached for PodCliqueScalingGroup")
)

type _resource struct {
	client                   client.Client
	scheme                   *runtime.Scheme
	eventRecorder            record.EventRecorder
	podTemplateSpecHashCache *hash.PodTemplateSpecHashCache
}

// New creates a new PodClique operator for managing PodClique resources within PodCliqueScalingGroups
func New(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, podTemplateSpecHashCache *hash.PodTemplateSpecHashCache) component.Operator[grovecorev1alpha1.PodCliqueScalingGroup] {
	return &_resource{
		client:                   client,
		scheme:                   scheme,
		eventRecorder:            eventRecorder,
		podTemplateSpecHashCache: podTemplateSpecHashCache,
	}
}

// GetExistingResourceNames returns the names of all the existing resources that the PodClique Operator manages.
// GetExistingResourceNames returns the names of all existing PodCliques managed by the specified PodCliqueScalingGroup
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsgObjMeta metav1.ObjectMeta) ([]string, error) {
	logger.Info("Looking for existing PodCliques managed by PodCliqueScalingGroup")
	pclqPartialObjMetaList, err := k8sutils.ListExistingPartialObjectMetadata(ctx,
		r.client,
		grovecorev1alpha1.SchemeGroupVersion.WithKind("PodClique"),
		pcsgObjMeta,
		getPodCliqueSelectorLabels(pcsgObjMeta))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodClique,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing PodCliques for PodCliqueScalingGroup: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsgObjMeta)),
		)
	}
	return k8sutils.FilterMapOwnedResourceNames(pcsgObjMeta, pclqPartialObjMetaList), nil
}

// Sync synchronizes all resources that the PodClique Operator manages.
// Sync ensures that the desired PodCliques exist for the PodCliqueScalingGroup with proper scaling and dependencies
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) error {
	syncCtx, err := r.prepareSyncContext(ctx, logger, pcsg)
	if err != nil {
		return err
	}
	logger.Info("Starting PodCliqueScalingGroup Sync", "pcsgObjectKey", client.ObjectKeyFromObject(syncCtx.pcsg))
	// Run the sync flow
	if err = r.runSyncFlow(logger, syncCtx); err != nil {
		return err
	}
	return nil
}

// Delete deletes all resources that the PodClique Operator manages.
// Delete removes all PodCliques managed by the specified PodCliqueScalingGroup
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsgObjectMeta metav1.ObjectMeta) error {
	logger.Info("Triggering deletion of PodCliques managed by PodCliqueScalingGroup")
	existingPCLQNames, err := r.GetExistingResourceNames(ctx, logger, pcsgObjectMeta)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeListPodClique,
			component.OperationDelete,
			fmt.Sprintf("Unable to fetch existing PodClique names for PodCliqueScalingGroup: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsgObjectMeta)),
		)
	}
	deleteTasks := make([]utils.Task, 0, len(existingPCLQNames))
	for _, pclqName := range existingPCLQNames {
		pclqObjectKey := client.ObjectKey{Name: pclqName, Namespace: pcsgObjectMeta.Namespace}
		task := utils.Task{
			Name: "DeletePodClique-" + pclqName,
			Fn: func(ctx context.Context) error {
				if err := client.IgnoreNotFound(r.client.Delete(ctx, emptyPodClique(pclqObjectKey))); err != nil {
					return groveerr.WrapError(err,
						errCodeDeletePodClique,
						component.OperationDelete,
						fmt.Sprintf("Failed to delete PodClique: %v for PodCliqueScalingGroup: %v", pclqObjectKey, k8sutils.GetObjectKeyFromObjectMeta(pcsgObjectMeta)),
					)
				}
				return nil
			},
		}
		deleteTasks = append(deleteTasks, task)
	}
	if runResult := utils.RunConcurrently(ctx, logger, deleteTasks); runResult.HasErrors() {
		logger.Error(runResult.GetAggregatedError(), "Error deleting PodCliques", "run summary", runResult.GetSummary())
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errCodeDeletePodClique,
			component.OperationDelete,
			fmt.Sprintf("Error deleting PodCliques for PodCliqueScalingGroup: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsgObjectMeta)),
		)
	}

	logger.Info("Deleted PodCliques belonging to PodCliqueScalingGroup")
	return nil
}

// triggerDeletionOfPodCliques executes concurrent deletion tasks for PodCliques and handles any resulting errors
func (r _resource) triggerDeletionOfPodCliques(ctx context.Context, logger logr.Logger, pcsgObjectKey client.ObjectKey, deletionTasks []utils.Task) error {
	if len(deletionTasks) == 0 {
		return nil
	}
	if runResult := utils.RunConcurrently(ctx, logger, deletionTasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errCodeDeletePodClique,
			component.OperationSync,
			fmt.Sprintf("Error deleting PodCliques for PodCliqueScalingGroup: %v", pcsgObjectKey),
		)
	}
	logger.Info("Deleted PodCliques of PodCliqueScalingGroup", "pcsgObjectKey", pcsgObjectKey)
	return nil
}

// createDeleteTasks creates deletion tasks for PodCliques belonging to specific PCSG replica indices
func (r _resource) createDeleteTasks(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsgName string, pcsgReplicasToDelete []string, reason string) []utils.Task {
	deletionTasks := make([]utils.Task, 0, len(pcsgReplicasToDelete))
	for _, pcsgReplicaIndex := range pcsgReplicasToDelete {
		task := utils.Task{
			Name: "DeletePCSGReplicaPodCliques-" + pcsgReplicaIndex,
			Fn: func(ctx context.Context) error {
				if err := r.client.DeleteAllOf(ctx,
					&grovecorev1alpha1.PodClique{},
					client.InNamespace(pcs.Namespace),
					client.MatchingLabels(getLabelsToDeletePCSGReplicaIndexPCLQs(pcs.Name, pcsgName, pcsgReplicaIndex))); err != nil {
					r.eventRecorder.Eventf(pcs, corev1.EventTypeWarning, constants.ReasonPodCliqueScalingGroupReplicaDeleteFailed, "Error deleting PodCliqueScalingGroup %s ReplicaIndex %s : %v", pcsgName, pcsgReplicaIndex, err)
					logger.Error(err, "failed to delete PodCliques for PCSG replica index", "pcsgReplicaIndex", pcsgReplicaIndex, "reason", reason)
					return err
				}
				logger.Info("Deleting PodCliqueScalingGroup replica", "pcsgName", pcsgName, "pcsgReplicaIndex", pcsgReplicaIndex)
				r.eventRecorder.Eventf(pcs, corev1.EventTypeNormal, constants.ReasonPodCliqueScalingGroupReplicaDeleteSuccessful, "Deleted PodCliqueScalingGroup %s replicaIndex: %s", pcsgName, pcsgReplicaIndex)
				return nil
			},
		}
		deletionTasks = append(deletionTasks, task)
	}
	return deletionTasks
}

// getLabelsToDeletePCSGReplicaIndexPCLQs creates label selectors for identifying PodCliques to delete for a specific PCSG replica
func getLabelsToDeletePCSGReplicaIndexPCLQs(pcsName, pcsgName, pcsgReplicaIndex string) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelComponentKey:                      apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
			apicommon.LabelPodCliqueScalingGroup:             pcsgName,
			apicommon.LabelPodCliqueScalingGroupReplicaIndex: pcsgReplicaIndex,
		},
	)
}

// getPCSGTemplateNumPods calculates the total number of pods across all PodCliques in the PodCliqueScalingGroup template
func (r _resource) getPCSGTemplateNumPods(pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) int {
	var pcsgTemplateNumPods int
	pcMap := make(map[string]*grovecorev1alpha1.PodCliqueTemplateSpec, len(pcs.Spec.Template.Cliques))
	for _, pclqTemplateSpec := range pcs.Spec.Template.Cliques {
		pcMap[pclqTemplateSpec.Name] = pclqTemplateSpec
	}
	for _, pclqTemplateName := range pcsg.Spec.CliqueNames {
		pclqTemplateSpec, ok := pcMap[pclqTemplateName]
		if !ok {
			continue
		}
		pcsgTemplateNumPods += int(pclqTemplateSpec.Spec.Replicas)
	}
	return pcsgTemplateNumPods
}

// doCreate creates or updates a PodClique resource with proper configuration from PCS and PCSG templates
func (r _resource) doCreate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pcsgReplicaIndex int, pclqObjectKey client.ObjectKey) error {
	logger.Info("Running CreateOrUpdate PodClique", "pclqObjectKey", pclqObjectKey)
	pclq := emptyPodClique(pclqObjectKey)
	pcsgObjKey := client.ObjectKeyFromObject(pclq)
	if err := r.buildResource(logger, pcs, pcsg, pcsgReplicaIndex, pclq); err != nil {
		return err
	}
	if err := r.client.Create(ctx, pclq); err != nil {
		if apierrors.IsAlreadyExists(err) {
			logger.Info("PodClique creation failed as it already exists", "pclq", pclqObjectKey)
			return nil
		}
		r.eventRecorder.Eventf(pcsg, corev1.EventTypeWarning, constants.ReasonPodCliqueCreateFailed, "PodClique %v creation failed: %v", pclqObjectKey, err)
		return groveerr.WrapError(err,
			errCodeCreatePodClique,
			component.OperationSync,
			fmt.Sprintf("Error creating PodClique: %v for PodCliqueScalingGroup: %v", pclqObjectKey, pcsgObjKey),
		)
	}
	r.eventRecorder.Eventf(pcsg, corev1.EventTypeNormal, constants.ReasonPodCliqueCreateSuccessful, "PodClique %v created successfully", pclqObjectKey)
	logger.Info("Successfully created PodClique", "pclqObjectKey", pclqObjectKey)
	return nil
}

// buildResource constructs a PodClique resource from templates, setting up metadata, labels, dependencies and environment variables
func (r _resource) buildResource(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pcsgReplicaIndex int, pclq *grovecorev1alpha1.PodClique) error {
	var err error
	pclqObjectKey, pcsObjectKey := client.ObjectKeyFromObject(pclq), client.ObjectKeyFromObject(pcs)
	pclqTemplateSpec, foundAtIndex, ok := lo.FindIndexOf(pcs.Spec.Template.Cliques, func(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec) bool {
		return strings.HasSuffix(pclq.Name, pclqTemplateSpec.Name)
	})
	if !ok {
		logger.Info("Error building PodClique resource, PodClique template spec not found in PodCliqueSet", "podCliqueObjectKey", pclqObjectKey, "podCliqueSetObjectKey", pcsObjectKey)
		return groveerr.New(errCodeBuildPodClique,
			component.OperationSync,
			fmt.Sprintf("Error building PodClique resource, PodCliqueTemplateSpec for PodClique: %v not found in PodCliqueSet: %v", pclqObjectKey, pcsObjectKey),
		)
	}
	// Set PodClique.ObjectMeta
	// ------------------------------------
	if err = controllerutil.SetControllerReference(pcsg, pclq, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errCodeSetPodCliqueOwnerReference,
			component.OperationSync,
			fmt.Sprintf("Error setting controller reference for PodClique: %v", client.ObjectKeyFromObject(pclq)),
		)
	}

	pcsReplicaIndex, err := getPCSReplicaFromPCSG(pcsg)
	if err != nil {
		return err
	}

	podGangName := apicommon.GeneratePodGangNameForPodCliqueOwnedByPCSG(pcs, pcsReplicaIndex, pcsg, pcsgReplicaIndex)
	podTemplateSpecHash, err := r.podTemplateSpecHashCache.GetOrCompute(pcs.Name, pclqTemplateSpec, pcs.Spec.Template.PriorityClassName)
	if err != nil {
		return groveerr.WrapError(err,
			errComputePodSpecTemplateHash,
			component.OperationSync,
			fmt.Sprintf("Error computing PodTemplateSpec hash for PodClique: %v", pclqObjectKey),
		)
	}
	pclq.Labels = getLabels(pcs, pcsReplicaIndex, pcsg, pcsgReplicaIndex, pclqObjectKey, pclqTemplateSpec, podGangName, podTemplateSpecHash)
	pclq.Annotations = pclqTemplateSpec.Annotations
	// set PodCliqueSpec
	// ------------------------------------
	pclq.Spec = *pclqTemplateSpec.Spec.DeepCopy()
	pcsgTemplateNumPods := r.getPCSGTemplateNumPods(pcs, pcsg)
	r.addEnvironmentVariablesToPodContainerSpecs(pclq, pcsgTemplateNumPods)
	dependentPCLQNames, err := identifyFullyQualifiedStartupDependencyNames(pcs, pcsReplicaIndex, pcsg, pcsgReplicaIndex, pclq, foundAtIndex)
	if err != nil {
		return err
	}
	pclq.Spec.StartsAfter = dependentPCLQNames

	// Inject MNNVL resourceClaims if enabled on PCSG (propagated from PCS)
	if mnnvl.IsAutoMNNVLEnabled(pcsg.Annotations) {
		mnnvl.InjectMNNVLIntoPodSpec(logger, &pclq.Spec.PodSpec, apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex})
	}

	return nil
}

// addEnvironmentVariablesToPodContainerSpecs injects PCSG-specific environment variables into all containers in the PodClique
func (r _resource) addEnvironmentVariablesToPodContainerSpecs(pclq *grovecorev1alpha1.PodClique, pcsgTemplateNumPods int) {
	pcsgEnvVars := []corev1.EnvVar{
		{
			Name: apiconstants.EnvVarPodCliqueScalingGroupName,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.labels['%s']", apicommon.LabelPodCliqueScalingGroup),
				},
			},
		},
		{
			Name:  apiconstants.EnvVarPodCliqueScalingGroupTemplateNumPods,
			Value: strconv.Itoa(pcsgTemplateNumPods),
		},
		{
			Name: apiconstants.EnvVarPodCliqueScalingGroupIndex,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.labels['%s']", apicommon.LabelPodCliqueScalingGroupReplicaIndex),
				},
			},
		},
	}
	pclqObjPodSpec := &pclq.Spec.PodSpec
	componentutils.AddEnvVarsToContainers(pclqObjPodSpec.Containers, pcsgEnvVars)
	componentutils.AddEnvVarsToContainers(pclqObjPodSpec.InitContainers, pcsgEnvVars)
}

// getPCSReplicaFromPCSG extracts the PodCliqueSet replica index from PodCliqueScalingGroup labels
func getPCSReplicaFromPCSG(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) (int, error) {
	pcsReplicaIndex, ok := pcsg.GetLabels()[apicommon.LabelPodCliqueSetReplicaIndex]
	if !ok {
		return 0, groveerr.New(errCodeMissingPCSReplicaIndex, component.OperationSync, fmt.Sprintf("failed to get the PodCliqueSet replica ind value from the labels for PodCliqueScalingGroup %s", client.ObjectKeyFromObject(pcsg)))
	}
	pcsReplica, err := strconv.Atoi(pcsReplicaIndex)
	if err != nil {
		return 0, groveerr.WrapError(err,
			errCodePCSReplicaIndexIntConversion,
			component.OperationSync,
			"failed to convert replica index value from string to integer",
		)
	}
	return pcsReplica, nil
}

// identifyFullyQualifiedStartupDependencyNames resolves startup dependencies based on PCS startup type configuration
func identifyFullyQualifiedStartupDependencyNames(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pcsgReplicaIndex int, pclq *grovecorev1alpha1.PodClique, foundAtIndex int) ([]string, error) {
	cliqueStartupType := pcs.Spec.Template.StartupType
	if cliqueStartupType == nil {
		// Ideally this should never happen as the defaulting webhook should set it v1alpha1.CliqueStartupTypeInOrder as the default value.
		// If it is still nil, then by not returning an error we break the API contract. It is a bug that should be fixed.
		return nil, groveerr.New(errCodeMissingStartupType, component.OperationSync, fmt.Sprintf("PodClique: %v has nil StartupType", client.ObjectKeyFromObject(pclq)))
	}
	switch *cliqueStartupType {
	case grovecorev1alpha1.CliqueStartupTypeInOrder:
		return getInOrderStartupDependencies(pcs, pcsReplicaIndex, pcsg, pcsgReplicaIndex, foundAtIndex), nil
	case grovecorev1alpha1.CliqueStartupTypeExplicit:
		return getExplicitStartupDependencies(pcs, pcsReplicaIndex, pcsg, pcsgReplicaIndex, pclq), nil
	default:
		return nil, nil
	}
}

// getInOrderStartupDependencies generates dependencies for in-order startup by including all preceding PodCliques in the same replica
func getInOrderStartupDependencies(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pcsgReplicaIndex, foundAtIndex int) []string {
	if foundAtIndex == 0 {
		return nil
	}
	parentCliqueName := pcs.Spec.Template.Cliques[foundAtIndex-1].Name

	// Current pcsgReplicaIndex belongs to the base PodGang
	if pcsgReplicaIndex < int(*pcsg.Spec.MinAvailable) {
		return componentutils.GenerateDependencyNamesForBasePodGang(pcs, pcsReplicaIndex, parentCliqueName)
	}

	// Startup ordering is only enforced within a PodGang.
	// PodCliques that belong to the base PodGang are not considered for startsAfter in scaled PodGangs.
	if !slices.Contains(pcsg.Spec.CliqueNames, parentCliqueName) {
		return nil
	}

	return []string{
		apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: pcsgReplicaIndex}, parentCliqueName),
	}
}

// getExplicitStartupDependencies generates fully qualified names for explicitly defined startup dependencies
func getExplicitStartupDependencies(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pcsgReplicaIndex int, pclq *grovecorev1alpha1.PodClique) []string {
	parentCliqueNames := make([]string, 0, len(pclq.Spec.StartsAfter))
	// Current pcsgReplicaIndex belongs to the base PodGang
	if pcsgReplicaIndex < int(*pcsg.Spec.MinAvailable) {
		for _, dependency := range pclq.Spec.StartsAfter {
			parentCliqueNames = append(parentCliqueNames, componentutils.GenerateDependencyNamesForBasePodGang(pcs, pcsReplicaIndex, dependency)...)
		}
		return parentCliqueNames
	}

	for _, dependency := range pclq.Spec.StartsAfter {
		// Startup ordering is only enforced within the scaled PodCliqueScalingGroup's corresponding PodGang.
		if !slices.Contains(pcsg.Spec.CliqueNames, dependency) {
			continue
		}
		parentCliqueNames = append(parentCliqueNames, apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: pcsgReplicaIndex}, dependency))
	}
	return parentCliqueNames
}

// getPodCliqueSelectorLabels creates label selector map for identifying PodCliques belonging to a PodCliqueScalingGroup
func getPodCliqueSelectorLabels(pcsgObjectMeta metav1.ObjectMeta) map[string]string {
	pcsName := componentutils.GetPodCliqueSetName(pcsgObjectMeta)
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelComponentKey:          apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
			apicommon.LabelPodCliqueScalingGroup: pcsgObjectMeta.Name,
		},
	)
}

// getLabels constructs the complete set of labels for a PodClique including Grove-specific, component, and template labels
func getLabels(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pcsgReplicaIndex int, pclqObjectKey client.ObjectKey, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, podGangName string, podTemplateSpecHash string) map[string]string {
	pclqComponentLabels := map[string]string{
		apicommon.LabelAppNameKey:                        pclqObjectKey.Name,
		apicommon.LabelComponentKey:                      apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
		apicommon.LabelPodCliqueScalingGroup:             pcsg.Name,
		apicommon.LabelPodGang:                           podGangName,
		apicommon.LabelPodCliqueSetReplicaIndex:          strconv.Itoa(pcsReplicaIndex),
		apicommon.LabelPodCliqueScalingGroupReplicaIndex: strconv.Itoa(pcsgReplicaIndex),
		apicommon.LabelPodTemplateHash:                   podTemplateSpecHash,
	}

	// Add base-podgang label for scaled PodGang pods (beyond minAvailable)
	basePodGangName := apicommon.GenerateBasePodGangName(
		apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex},
	)
	if podGangName != basePodGangName {
		// This pod belongs to a scaled PodGang - add the base PodGang label
		pclqComponentLabels[apicommon.LabelBasePodGang] = basePodGangName
	}

	return lo.Assign(
		pclqTemplateSpec.Labels,
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		pclqComponentLabels,
	)
}

// emptyPodClique creates a new empty PodClique resource with basic metadata
func emptyPodClique(objKey client.ObjectKey) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}
