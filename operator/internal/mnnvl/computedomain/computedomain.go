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
	"strings"

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
	// Skip if PCS doesn't have MNNVL enabled
	if !mnnvl.IsAutoMNNVLEnabled(pcs.Annotations) {
		logger.V(1).Info("PCS does not have MNNVL enabled, skipping ComputeDomain sync")
		return nil
	}

	existingCDFQNs, err := r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return err
	}

	if err := r.triggerDeletionOfExcessComputeDomains(ctx, logger, pcs, existingCDFQNs); err != nil {
		return err
	}

	if err := r.createComputeDomains(ctx, logger, pcs, existingCDFQNs); err != nil {
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

	// Remove finalizers and delete all CDs
	deleteTasks := make([]utils.Task, 0, len(existingCDFQNs))
	for _, cdName := range existingCDFQNs {
		cdName := cdName // Capture loop variable
		cdObjKey := client.ObjectKey{Name: cdName, Namespace: pcsObjMeta.Namespace}
		task := utils.Task{
			Name: "DeleteComputeDomain-" + cdName,
			Fn: func(ctx context.Context) error {
				return r.deleteCD(ctx, logger, cdObjKey)
			},
		}
		deleteTasks = append(deleteTasks, task)
	}

	if runResult := utils.RunConcurrently(ctx, logger, deleteTasks); runResult.HasErrors() {
		logger.Error(runResult.GetAggregatedError(), "Error deleting ComputeDomains", "run summary", runResult.GetSummary())
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errDeleteComputeDomain,
			component.OperationDelete,
			fmt.Sprintf("Error deleting ComputeDomains for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}

	logger.Info("Deleted ComputeDomains", "count", len(existingCDFQNs))
	return nil
}

// createComputeDomains creates ComputeDomains for each replica that doesn't already have one.
// Note: Unlike PodClique, we only create ComputeDomains - no updates are needed since CD spec is immutable.
func (r _resource) createComputeDomains(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, existingCDFQNs []string) error {
	var tasks []utils.Task

	for replicaIndex := range int(pcs.Spec.Replicas) {
		cdObjKey := client.ObjectKey{
			Name:      generateComputeDomainName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex}),
			Namespace: pcs.Namespace,
		}

		// Skip if ComputeDomain already exists - no update needed
		if lo.Contains(existingCDFQNs, cdObjKey.Name) {
			continue
		}

		task := utils.Task{
			Name: fmt.Sprintf("CreateComputeDomain-%s", cdObjKey),
			Fn: func(ctx context.Context) error {
				return r.doCreate(ctx, logger, pcs, replicaIndex, cdObjKey)
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
func (r _resource) doCreate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int, cdObjKey client.ObjectKey) error {
	logger.Info("Creating ComputeDomain", "cdObjectKey", cdObjKey)
	cd := emptyComputeDomain(cdObjKey)
	pcsObjKey := client.ObjectKeyFromObject(pcs)

	opResult, err := controllerutil.CreateOrPatch(ctx, r.client, cd, func() error {
		return r.buildResource(cd, pcs, replicaIndex)
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
func (r _resource) buildResource(cd *unstructured.Unstructured, pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int) error {
	rctName := mnnvl.GenerateRCTName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex})

	// Set labels - aligned with PodClique labeling pattern
	cdComponentLabels := map[string]string{
		apicommon.LabelAppNameKey:               cd.GetName(),
		apicommon.LabelComponentKey:             labelComponentNameComputeDomain,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(replicaIndex),
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

// triggerDeletionOfExcessComputeDomains deletes ComputeDomains for replicas that no longer exist (scale-in).
func (r _resource) triggerDeletionOfExcessComputeDomains(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, existingCDFQNs []string) error {
	cdFQNsToDelete, err := getCDFQNsToDelete(pcs.Name, int(pcs.Spec.Replicas), existingCDFQNs)
	if err != nil {
		return err
	}
	if len(cdFQNsToDelete) == 0 {
		return nil
	}

	logger.Info("Triggering deletion of excess ComputeDomains", "count", len(cdFQNsToDelete))
	deleteTasks := r.buildDeletionTasks(logger, pcs, cdFQNsToDelete)
	return r.triggerDeletionOfComputeDomains(ctx, logger, client.ObjectKeyFromObject(pcs), deleteTasks)
}

// triggerDeletionOfComputeDomains executes deletion tasks for ComputeDomains.
func (r _resource) triggerDeletionOfComputeDomains(ctx context.Context, logger logr.Logger, pcsObjKey client.ObjectKey, deletionTasks []utils.Task) error {
	if len(deletionTasks) == 0 {
		return nil
	}
	if runResult := utils.RunConcurrently(ctx, logger, deletionTasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errDeleteComputeDomain,
			component.OperationSync,
			fmt.Sprintf("Error deleting ComputeDomains for PodCliqueSet: %s", pcsObjKey.Name),
		)
	}
	logger.Info("Deleted ComputeDomains of PodCliqueSet", "pcsObjectKey", pcsObjKey)
	return nil
}

// getCDFQNsToDelete identifies ComputeDomains whose replica index exceeds the desired count.
func getCDFQNsToDelete(pcsName string, desiredReplicas int, existingCDFQNs []string) ([]string, error) {
	var cdFQNsToDelete []string
	for _, cdName := range existingCDFQNs {
		replicaIndex, err := parseReplicaIndexFromName(cdName, pcsName)
		if err != nil {
			return nil, groveerr.WrapError(err,
				errSyncComputeDomain,
				component.OperationSync,
				fmt.Sprintf("Failed to extract replica index from ComputeDomain name: %s", cdName),
			)
		}
		if replicaIndex >= desiredReplicas {
			cdFQNsToDelete = append(cdFQNsToDelete, cdName)
		}
	}
	return cdFQNsToDelete, nil
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

// generateComputeDomainName creates the CD name for a replica.
// Format: {pcs-name}-{replica-index} (e.g., "my-pcs-0")
func generateComputeDomainName(pcsNameReplica apicommon.ResourceNameReplica) string {
	return fmt.Sprintf("%s-%d", pcsNameReplica.Name, pcsNameReplica.Replica)
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

// parseReplicaIndexFromName extracts the replica index from a ComputeDomain name.
// Expected format: {pcsName}-{replicaIndex} (e.g., "mypcs-0")
func parseReplicaIndexFromName(cdName, pcsName string) (int, error) {
	prefix := pcsName + "-"
	if !strings.HasPrefix(cdName, prefix) {
		return -1, fmt.Errorf("ComputeDomain name %q does not have expected prefix %q", cdName, prefix)
	}
	// Extract the replica index part
	indexStr := strings.TrimPrefix(cdName, prefix)
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		return -1, fmt.Errorf("failed to parse replica index from ComputeDomain name %q: %w", cdName, err)
	}
	return index, nil
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
