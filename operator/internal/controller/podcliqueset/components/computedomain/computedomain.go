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

package computedomain

import (
	"context"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errSyncComputeDomain   grovecorev1alpha1.ErrorCode = "ERR_SYNC_COMPUTEDOMAIN"
	errDeleteComputeDomain grovecorev1alpha1.ErrorCode = "ERR_DELETE_COMPUTEDOMAIN"
	errListComputeDomain   grovecorev1alpha1.ErrorCode = "ERR_LIST_COMPUTEDOMAIN"

	// labelComponentNameComputeDomain is the component name for ComputeDomain resources.
	labelComponentNameComputeDomain = "pcs-computedomain"
)

type _resource struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
	disableSync   bool
}

// cdNameInfo holds the resolved identity for a single ComputeDomain resource.
// The full CD name is computed via fullName() to avoid redundancy with the
// individual components (pcsName, replicaIndex, groupName).
type cdNameInfo struct {
	pcsName      string
	replicaIndex int
	groupName    string // empty for the default group
}

func (c cdNameInfo) fullName() string {
	return generateComputeDomainName(c.pcsName, c.replicaIndex, c.groupName)
}

// ShouldRegister reports whether the ComputeDomain component should be registered.
func ShouldRegister(restMapper meta.RESTMapper, autoMNNVLEnabled bool, logger logr.Logger) bool {
	if autoMNNVLEnabled {
		// Startup preflight has already confirmed that the ComputeDomain API exists.
		return true
	}

	_, err := restMapper.RESTMapping(mnnvl.ComputeDomainGVK.GroupKind(), mnnvl.ComputeDomainGVK.Version)
	if err != nil {
		if meta.IsNoMatchError(err) {
			logger.Info("ComputeDomain API is unavailable, skipping component registration")
		} else {
			logger.Error(err, "could not confirm ComputeDomain API availability, skipping component registration")
		}
		return false
	}
	return true
}

// New creates a new ComputeDomain operator for managing ComputeDomain resources within PodCliqueSets.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, disableSync bool) component.Operator[grovecorev1alpha1.PodCliqueSet, pcssync.State, pcssync.Result] {
	return &_resource{
		client:        cl,
		scheme:        scheme,
		eventRecorder: eventRecorder,
		disableSync:   disableSync,
	}
}

// GetExistingResourceNames returns the names of all existing ComputeDomains owned by the PCS.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	logger.Info("Looking for existing ComputeDomains", "pcs", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta))

	existingCDs, err := r.listExistingComputeDomains(ctx, pcsObjMeta)
	if err != nil {
		return nil, err
	}
	return lo.Map(existingCDs, func(cd metav1.PartialObjectMetadata, _ int) string { return cd.Name }), nil
}

// listExistingComputeDomains lists ComputeDomains controlled by the PCS.
func (r _resource) listExistingComputeDomains(ctx context.Context, pcsObjMeta metav1.ObjectMeta) ([]metav1.PartialObjectMetadata, error) {
	existingCDs, err := k8sutils.ListExistingPartialObjectMetadata(ctx,
		r.client,
		mnnvl.ComputeDomainGVK,
		pcsObjMeta,
		getSelectorLabels(pcsObjMeta.Name))
	if err != nil {
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, groveerr.WrapError(err,
			errListComputeDomain,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing ComputeDomains for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	return lo.Filter(existingCDs, func(cd metav1.PartialObjectMetadata, _ int) bool {
		return metav1.IsControlledBy(&cd, &pcsObjMeta)
	}), nil
}

// getSelectorLabels returns labels for selecting ComputeDomains of a PCS.
func getSelectorLabels(pcsName string) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelComponentKey: labelComponentNameComputeDomain,
		},
	)
}

// Sync synchronizes ComputeDomain resources for the PodCliqueSet.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, state *pcssync.State) (pcssync.Result, error) {
	if r.disableSync {
		return pcssync.Result{}, nil
	}

	if len(collectDistinctGroups(pcs)) == 0 {
		logger.V(1).Info("PCS does not require any ComputeDomains, skipping sync")
		return pcssync.Result{}, nil
	}

	expectedCDs := computeExpectedComputeDomains(pcs, state.ExpectedReplicaIndices)
	existingCDs, err := r.listExistingComputeDomains(ctx, pcs.ObjectMeta)
	if err != nil {
		return pcssync.Result{}, err
	}
	existingCDNames := lo.Map(existingCDs, func(cd metav1.PartialObjectMetadata, _ int) string { return cd.Name })
	toCreate, toDelete := computeComputeDomainChanges(expectedCDs, existingCDNames)

	replicaIndicesAwaitingResourceCleanup, err := getScaleInPCSReplicaIndicesAwaitingComputeDomainCleanup(existingCDs, state.ScaleInReplicaIndices)
	if err != nil {
		return pcssync.Result{}, err
	}
	if err := r.triggerDeletionOfComputeDomainsByName(ctx, logger, pcs, toDelete); err != nil {
		return pcssync.Result{}, err
	}
	if err := r.createComputeDomains(ctx, logger, pcs, toCreate); err != nil {
		return pcssync.Result{}, err
	}

	return pcssync.Result{
		ScaleInReplicaIndicesAwaitingResourceCleanup: replicaIndicesAwaitingResourceCleanup,
	}, nil
}

// collectDistinctGroups determines which MNNVL groups need ComputeDomains by
// resolving the effective group for each GPU clique using hierarchical
// annotation resolution (clique → PCSG → PCS). Non-GPU cliques are skipped
// so that a PCS-level annotation doesn't create orphaned CDs when no GPU
// cliques exist.
func collectDistinctGroups(pcs *grovecorev1alpha1.PodCliqueSet) map[string]struct{} {
	groups := make(map[string]struct{})

	pcsgByClique := buildPCSGLookup(pcs)

	for _, clique := range pcs.Spec.Template.Cliques {
		if clique == nil || !mnnvl.HasGPUInPodSpec(&clique.Spec.PodSpec) {
			continue
		}
		var pcsgAnnotations map[string]string
		if pcsgCfg, ok := pcsgByClique[clique.Name]; ok {
			pcsgAnnotations = pcsgCfg.Annotations
		}
		if group, ok := mnnvl.ResolveGroupNameHierarchically(clique.Annotations, pcsgAnnotations, pcs.Annotations); ok {
			groups[group] = struct{}{}
		}
	}

	return groups
}

// buildPCSGLookup builds a map from clique name to the PCSG config that contains it.
func buildPCSGLookup(pcs *grovecorev1alpha1.PodCliqueSet) map[string]grovecorev1alpha1.PodCliqueScalingGroupConfig {
	lookup := make(map[string]grovecorev1alpha1.PodCliqueScalingGroupConfig)
	for _, pcsg := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		for _, cliqueName := range pcsg.CliqueNames {
			lookup[cliqueName] = pcsg
		}
	}
	return lookup
}

// computeExpectedComputeDomains returns the desired ComputeDomains for the expected PCS replica identities.
// It resolves and deduplicates MNNVL groups across the PCS, PCSG configs, and clique templates,
// then produces one ComputeDomain per group for each expected PCS replica index.
func computeExpectedComputeDomains(pcs *grovecorev1alpha1.PodCliqueSet, expectedPCSReplicaIndices []int) []cdNameInfo {
	groups := collectDistinctGroups(pcs)
	if len(groups) == 0 {
		return nil
	}

	var result []cdNameInfo
	for _, replicaIndex := range expectedPCSReplicaIndices {
		for group := range groups {
			result = append(result, cdNameInfo{
				pcsName:      pcs.Name,
				replicaIndex: replicaIndex,
				groupName:    group,
			})
		}
	}
	return result
}

// computeComputeDomainChanges compares expected and existing ComputeDomains and returns
// the expected resources to create and the existing resource names to delete.
func computeComputeDomainChanges(expectedCDs []cdNameInfo, existingCDNames []string) (toCreate []cdNameInfo, toDelete []string) {
	expectedByName := lo.SliceToMap(expectedCDs, func(cd cdNameInfo) (string, cdNameInfo) {
		return cd.fullName(), cd
	})
	existingSet := lo.SliceToMap(existingCDNames, func(name string) (string, struct{}) {
		return name, struct{}{}
	})

	toCreate = lo.Filter(expectedCDs, func(cd cdNameInfo, _ int) bool {
		_, exists := existingSet[cd.fullName()]
		return !exists
	})
	toDelete = lo.Filter(existingCDNames, func(name string, _ int) bool {
		_, expected := expectedByName[name]
		return !expected
	})
	return toCreate, toDelete
}

// generateComputeDomainName creates the CD name for a replica.
// Format: {pcs-name}-{replica-index}-{group-name} (e.g., "my-pcs-0-workers").
func generateComputeDomainName(pcsName string, replicaIndex int, groupName string) string {
	return fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, groupName)
}

// getScaleInPCSReplicaIndicesAwaitingComputeDomainCleanup returns scale-in replica indices for which at least one ComputeDomain still exists.
func getScaleInPCSReplicaIndicesAwaitingComputeDomainCleanup(existingCDs []metav1.PartialObjectMetadata, scaleInReplicaIndices []int) ([]int, error) {
	scaleInPCSReplicaIndices := sets.New(scaleInReplicaIndices...)
	pcsReplicaIndicesAwaitingResourceCleanup := sets.New[int]()
	for _, cd := range existingCDs {
		pcsReplicaIndex, err := k8sutils.GetPodCliqueSetReplicaIndex(cd.ObjectMeta)
		if err != nil {
			return nil, groveerr.WrapError(err,
				errSyncComputeDomain,
				component.OperationSync,
				fmt.Sprintf("Invalid PCS replica index label on ComputeDomain: %v", client.ObjectKeyFromObject(&cd)),
			)
		}
		if scaleInPCSReplicaIndices.Has(pcsReplicaIndex) {
			pcsReplicaIndicesAwaitingResourceCleanup.Insert(pcsReplicaIndex)
		}
	}
	return sets.List(pcsReplicaIndicesAwaitingResourceCleanup), nil
}

// triggerDeletionOfComputeDomainsByName deletes the specified ComputeDomains.
func (r _resource) triggerDeletionOfComputeDomainsByName(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, cdNames []string) error {
	if len(cdNames) == 0 {
		return nil
	}

	logger.Info("Triggering deletion of excess ComputeDomains", "count", len(cdNames))
	deleteTasks := r.buildDeletionTasks(logger, pcs, cdNames)

	if runResult := utils.RunConcurrentlyAndWait(ctx, logger, deleteTasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errDeleteComputeDomain,
			component.OperationSync,
			fmt.Sprintf("Error deleting ComputeDomains for PodCliqueSet: %s", pcs.Name),
		)
	}
	logger.Info("Deleted excess ComputeDomains", "pcs", client.ObjectKeyFromObject(pcs))
	return nil
}

// buildDeletionTasks generates deletion tasks for the specified ComputeDomains.
func (r _resource) buildDeletionTasks(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, targetCDFQNs []string) []utils.Task {
	deleteTasks := make([]utils.Task, 0, len(targetCDFQNs))
	for _, cdName := range targetCDFQNs {
		cdName := cdName // Capture loop variable
		cdObjKey := client.ObjectKey{Name: cdName, Namespace: pcs.Namespace}
		task := utils.Task{
			Name: "DeleteComputeDomain-" + cdObjKey.Name,
			Fn: func(ctx context.Context) error {
				if err := r.deleteCD(ctx, logger, cdObjKey); err != nil {
					logger.Error(err, "Failed to delete ComputeDomain", "objectKey", cdObjKey)
					r.eventRecorder.Eventf(pcs, corev1.EventTypeWarning, constants.ReasonComputeDomainDeleteFailed,
						"Failed to delete ComputeDomain %s: %v", cdObjKey.Name, err)
					return err
				}
				r.eventRecorder.Eventf(pcs, corev1.EventTypeNormal, constants.ReasonComputeDomainDeleteSuccessful,
					"Deleted ComputeDomain %s", cdObjKey.Name)
				return nil
			},
		}
		deleteTasks = append(deleteTasks, task)
	}
	return deleteTasks
}

// deleteCD removes the finalizer and deletes a ComputeDomain.
func (r _resource) deleteCD(ctx context.Context, logger logr.Logger, cdObjKey client.ObjectKey) error {
	// First remove the finalizer
	if err := r.removeFinalizerFromCD(ctx, logger, cdObjKey); err != nil {
		return fmt.Errorf("failed to remove finalizer from ComputeDomain %s: %w", cdObjKey.Name, err)
	}

	// Then delete the CD
	if err := client.IgnoreNotFound(r.client.Delete(ctx, emptyComputeDomain(cdObjKey))); err != nil {
		return fmt.Errorf("failed to delete ComputeDomain %s: %w", cdObjKey.Name, err)
	}

	logger.Info("Deleted ComputeDomain", "objectKey", cdObjKey)
	return nil
}

// removeFinalizerFromCD removes the protection finalizer from a ComputeDomain.
func (r _resource) removeFinalizerFromCD(ctx context.Context, logger logr.Logger, cdObjKey client.ObjectKey) error {
	cd := emptyComputeDomain(cdObjKey)
	if err := r.client.Get(ctx, cdObjKey, cd); err != nil {
		return client.IgnoreNotFound(err)
	}

	if !controllerutil.ContainsFinalizer(cd, mnnvl.FinalizerComputeDomain) {
		return nil
	}

	controllerutil.RemoveFinalizer(cd, mnnvl.FinalizerComputeDomain)
	if err := r.client.Update(ctx, cd); err != nil {
		return fmt.Errorf("failed to remove finalizer from ComputeDomain %s: %w", cdObjKey.Name, err)
	}

	logger.Info("Removed finalizer from ComputeDomain", "objectKey", cdObjKey)
	return nil
}

// emptyComputeDomain creates an empty ComputeDomain with only metadata set.
// We use unstructured.Unstructured instead of typed structs to avoid
// a compile-time dependency on the NVIDIA DRA driver package.
func emptyComputeDomain(objKey client.ObjectKey) *unstructured.Unstructured {
	cd := &unstructured.Unstructured{}
	cd.SetGroupVersionKind(mnnvl.ComputeDomainGVK)
	cd.SetName(objKey.Name)
	cd.SetNamespace(objKey.Namespace)
	return cd
}

// createComputeDomains creates the given ComputeDomains that don't already exist.
func (r _resource) createComputeDomains(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, cds []cdNameInfo) error {
	if len(cds) == 0 {
		return nil
	}

	tasks := make([]utils.Task, 0, len(cds))
	for _, cd := range cds {
		cd := cd
		cdObjKey := client.ObjectKey{Name: cd.fullName(), Namespace: pcs.Namespace}
		task := utils.Task{
			Name: fmt.Sprintf("CreateComputeDomain-%s", cdObjKey),
			Fn: func(ctx context.Context) error {
				return r.doCreate(ctx, logger, pcs, cd, cdObjKey)
			},
		}
		tasks = append(tasks, task)
	}

	if runResult := utils.RunConcurrentlyAndWait(ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errSyncComputeDomain,
			component.OperationSync,
			fmt.Sprintf("Error creating ComputeDomains for PodCliqueSet: %v, run summary: %s", client.ObjectKeyFromObject(pcs), runResult.GetSummary()),
		)
	}
	return nil
}

// doCreate creates a single ComputeDomain resource.
func (r _resource) doCreate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, cdInfo cdNameInfo, cdObjKey client.ObjectKey) error {
	logger.Info("Creating ComputeDomain", "cdObjectKey", cdObjKey)
	cd := emptyComputeDomain(cdObjKey)
	pcsObjKey := client.ObjectKeyFromObject(pcs)

	opResult, err := k8sutils.CreateOrPatchSpec(ctx, r.client, cd, func() error {
		return r.buildResource(cd, pcs, cdInfo)
	})
	if err != nil {
		r.eventRecorder.Eventf(pcs, corev1.EventTypeWarning, constants.ReasonComputeDomainCreateFailed,
			"ComputeDomain %v creation failed: %v", cdObjKey, err)
		return groveerr.WrapError(err,
			errSyncComputeDomain,
			component.OperationSync,
			fmt.Sprintf("Error creating ComputeDomain: %v for PodCliqueSet: %v", cdObjKey, pcsObjKey),
		)
	}

	r.eventRecorder.Eventf(pcs, corev1.EventTypeNormal, constants.ReasonComputeDomainCreateSuccessful,
		"ComputeDomain %v created successfully", cdObjKey)
	logger.Info("Created ComputeDomain for PodCliqueSet", "pcs", pcsObjKey, "cdObjectKey", cdObjKey, "result", opResult)
	return nil
}

// buildResource configures a ComputeDomain with the desired state.
func (r _resource) buildResource(cd *unstructured.Unstructured, pcs *grovecorev1alpha1.PodCliqueSet, cdInfo cdNameInfo) error {
	rctName := mnnvl.GenerateRCTName(apicommon.ResourceNameReplica{Name: cdInfo.pcsName, Replica: cdInfo.replicaIndex}, cdInfo.groupName)

	cdComponentLabels := map[string]string{
		apicommon.LabelAppNameKey:               cd.GetName(),
		apicommon.LabelComponentKey:             labelComponentNameComputeDomain,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(cdInfo.replicaIndex),
	}
	if cdInfo.groupName != "" {
		cdComponentLabels[mnnvl.LabelMNNVLGroup] = cdInfo.groupName
	}
	cd.SetLabels(lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		cdComponentLabels,
	))

	// Add finalizer to prevent accidental deletion while workload is using it
	controllerutil.AddFinalizer(cd, mnnvl.FinalizerComputeDomain)

	// Set owner reference for garbage collection
	if err := controllerutil.SetControllerReference(pcs, cd, r.scheme); err != nil {
		return groveerr.WrapError(err, errSyncComputeDomain, component.OperationSync,
			fmt.Sprintf("Failed to set owner reference for ComputeDomain: %s", cd.GetName()))
	}

	// Set the spec with ResourceClaimTemplate reference.
	// The CRD expects spec.channel.resourceClaimTemplate.name (nested object, not a flat field).
	if err := unstructured.SetNestedField(cd.Object, rctName, "spec", "channel", "resourceClaimTemplate", "name"); err != nil {
		return groveerr.WrapError(err, errSyncComputeDomain, component.OperationSync,
			fmt.Sprintf("Failed to set resourceClaimTemplate.name for ComputeDomain: %s", cd.GetName()))
	}

	// numNodes is a required field in the ComputeDomain CRD, so we must explicitly set it.
	// We set it to 0 to keep the ComputeDomain elastic - with numNodes=0 and
	// featureGates.IMEXDaemonsWithDNSNames=true (the default), IMEX daemons start
	// immediately without waiting for peers.
	if err := unstructured.SetNestedField(cd.Object, int64(0), "spec", "numNodes"); err != nil {
		return groveerr.WrapError(err, errSyncComputeDomain, component.OperationSync,
			fmt.Sprintf("Failed to set numNodes for ComputeDomain: %s", cd.GetName()))
	}

	return nil
}

// Delete removes finalizers and deletes all ComputeDomains owned by the PodCliqueSet.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	logger.Info("Deleting ComputeDomains", "pcs", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta))

	existingCDFQNs, err := r.GetExistingResourceNames(ctx, logger, pcsObjMeta)
	if err != nil {
		return err
	}

	pcs := &grovecorev1alpha1.PodCliqueSet{ObjectMeta: pcsObjMeta}
	return r.triggerDeletionOfComputeDomainsByName(ctx, logger, pcs, existingCDFQNs)
}
