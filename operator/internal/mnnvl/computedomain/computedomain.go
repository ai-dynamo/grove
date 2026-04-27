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

package computedomain

import (
	"context"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
}

// New creates a new ComputeDomain operator for managing ComputeDomain resources within PodCliqueSets.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client:        cl,
		scheme:        scheme,
		eventRecorder: eventRecorder,
	}
}

// GetExistingResourceNames returns the names of all existing ComputeDomains owned by the PCS.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	logger.Info("Looking for existing ComputeDomains", "pcs", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta))

	existingCDs, err := k8sutils.ListExistingPartialObjectMetadata(ctx,
		r.client,
		mnnvl.ComputeDomainGVK,
		pcsObjMeta,
		getSelectorLabels(pcsObjMeta.Name))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errListComputeDomain,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing ComputeDomains for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}

	return k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, existingCDs), nil
}

// Sync synchronizes ComputeDomain resources for the PodCliqueSet.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	requiredCDs := getRequiredCDNames(pcs)
	if len(requiredCDs) == 0 {
		logger.V(1).Info("PCS does not require any ComputeDomains, skipping sync")
		return nil
	}

	existingCDFQNs, err := r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return err
	}

	toCreate, toDelete := triageCDs(requiredCDs, existingCDFQNs)

	if err := r.triggerDeletionOfComputeDomainsByName(ctx, logger, pcs, toDelete); err != nil {
		return err
	}

	if err := r.createComputeDomains(ctx, logger, pcs, toCreate); err != nil {
		return err
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

	if runResult := utils.RunConcurrently(ctx, logger, tasks); runResult.HasErrors() {
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

	opResult, err := controllerutil.CreateOrPatch(ctx, r.client, cd, func() error {
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

// triggerDeletionOfComputeDomainsByName deletes the specified ComputeDomains.
func (r _resource) triggerDeletionOfComputeDomainsByName(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, cdNames []string) error {
	if len(cdNames) == 0 {
		return nil
	}

	logger.Info("Triggering deletion of excess ComputeDomains", "count", len(cdNames))
	deleteTasks := r.buildDeletionTasks(logger, pcs, cdNames)

	if runResult := utils.RunConcurrently(ctx, logger, deleteTasks); runResult.HasErrors() {
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

// triageCDs computes the set differences between required and existing ComputeDomains,
// returning which CDs need to be created and which names need to be deleted.
func triageCDs(requiredCDs []cdNameInfo, existingCDFQNs []string) (toCreate []cdNameInfo, toDelete []string) {
	requiredByName := lo.SliceToMap(requiredCDs, func(cd cdNameInfo) (string, cdNameInfo) {
		return cd.fullName(), cd
	})
	existingSet := lo.SliceToMap(existingCDFQNs, func(name string) (string, struct{}) {
		return name, struct{}{}
	})

	toCreate = lo.Filter(requiredCDs, func(cd cdNameInfo, _ int) bool {
		_, exists := existingSet[cd.fullName()]
		return !exists
	})
	toDelete = lo.Filter(existingCDFQNs, func(name string, _ int) bool {
		_, required := requiredByName[name]
		return !required
	})
	return toCreate, toDelete
}

// getRequiredCDNames resolves the list of ComputeDomain names needed by a PCS.
// It collects MNNVL groups from all annotation layers (PCS, PCSG configs,
// clique templates), deduplicates, and generates a cdNameInfo per group per replica.
func getRequiredCDNames(pcs *grovecorev1alpha1.PodCliqueSet) []cdNameInfo {
	groups := collectDistinctGroups(pcs)
	if len(groups) == 0 {
		return nil
	}

	var result []cdNameInfo
	for replicaIndex := range int(pcs.Spec.Replicas) {
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

// collectDistinctGroups collects MNNVL group names from all annotation layers
// in the PCS (PCS-level, PCSG configs, clique templates) and returns the
// deduplicated set.
func collectDistinctGroups(pcs *grovecorev1alpha1.PodCliqueSet) map[string]struct{} {
	groups := make(map[string]struct{})

	addGroupFromAnnotation(groups, pcs.Annotations)

	for _, pcsg := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		addGroupFromAnnotation(groups, pcsg.Annotations)
	}

	for _, clique := range pcs.Spec.Template.Cliques {
		if clique != nil {
			addGroupFromAnnotation(groups, clique.Annotations)
		}
	}

	return groups
}

// addGroupFromAnnotation extracts the MNNVL group from annotations and adds it to the set.
func addGroupFromAnnotation(groups map[string]struct{}, annotations map[string]string) {
	if group, ok := resolveGroupName(annotations); ok {
		groups[group] = struct{}{}
	}
}

// resolveGroupName extracts the MNNVL group from a single annotation set.
// Returns (group, true) when mnnvl-group is set.
// Returns ("", true) when auto-mnnvl is enabled without a group (default group).
// Returns ("", false) when MNNVL is not requested.
func resolveGroupName(annotations map[string]string) (string, bool) {
	if group, hasGroup := annotations[mnnvl.AnnotationMNNVLGroup]; hasGroup {
		return group, true
	}
	if mnnvl.IsAutoMNNVLEnabled(annotations) {
		return "", true
	}
	return "", false
}

// generateComputeDomainName creates the CD name for a replica.
// Without a group: {pcs-name}-{replica-index} (e.g., "my-pcs-0").
// With a group: {pcs-name}-{replica-index}-{group-name} (e.g., "my-pcs-0-workers").
func generateComputeDomainName(pcsName string, replicaIndex int, groupName string) string {
	if groupName == "" {
		return fmt.Sprintf("%s-%d", pcsName, replicaIndex)
	}
	return fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, groupName)
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
