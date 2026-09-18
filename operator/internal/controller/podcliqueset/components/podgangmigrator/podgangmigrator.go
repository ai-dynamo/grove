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

package podgangmigrator

import (
	"context"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/podgangmigrator"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	errCodeGetPodGangMap      grovecorev1alpha1.ErrorCode = "ERR_GET_PODGANGMAP"
	errCodeListPodCliques     grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUES"
	errCodeListPods           grovecorev1alpha1.ErrorCode = "ERR_LIST_PODS"
	errCodeResolvePodGangName grovecorev1alpha1.ErrorCode = "ERR_RESOLVE_PODGANG_NAME"
	errCodeMissingLabels      grovecorev1alpha1.ErrorCode = "ERR_MISSING_LABELS"
	errCodePatchPodGangLabels grovecorev1alpha1.ErrorCode = "ERR_PATCH_PODGANG_LABELS"
	errCodeClearMigrationGate grovecorev1alpha1.ErrorCode = "ERR_CLEAR_MIGRATION_GATE"
	errCodeGetPodGang         grovecorev1alpha1.ErrorCode = "ERR_GET_PODGANG"
)

type _resource struct {
	client        client.Client
	scheme        *runtime.Scheme
	schedRegistry scheduler.Registry
}

// New creates a new PodGang migration operator. It migrates a PodCliqueSet from the legacy PodGang
// naming to the epoch-based PodGang naming and PodGangMap scheme. It uses the PodGangMap created
// earlier in the same reconcile as the source of truth for the new names.
func New(client client.Client, scheme *runtime.Scheme, schedRegistry scheduler.Registry) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client:        client,
		scheme:        scheme,
		schedRegistry: schedRegistry,
	}
}

// GetExistingResourceNames returns nil. This is an action component that owns no resource lifecycle of
// its own.
func (r _resource) GetExistingResourceNames(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) ([]string, error) {
	return nil, nil
}

// Sync migrates the PodCliqueSet to the epoch-based PodGang scheme. It does the following:
//   - For every replica, rewrite the grove.io/podgang label on each constituent PodClique and Pod to
//     the epoch-based name the PodGangMap assigns, and drop the legacy grove.io/base-podgang label.
//   - Clear the migration gate once every epoch-named PodGang exists, unblocking the
//     PodCliqueScalingGroup and PodClique reconcilers.
//
// It creates no PodGang. The PodGang component in a later sync group creates them. Holding the gate
// until they exist stops an unblocked rollout from deleting an old Pod whose replacement is gated on a
// PodGang that does not exist yet.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	if !podgangmigrator.IsMigrationInProgress(pcs) {
		return nil
	}
	for pcsReplicaIndex := range int(pcs.Spec.Replicas) {
		if err := r.migrateReplica(ctx, pcs, pcsReplicaIndex); err != nil {
			return err
		}
	}
	allCreated, err := r.allEpochBasedPodGangsCreated(ctx, pcs)
	if err != nil {
		return err
	}
	if !allCreated {
		// Continue the reconcile so the PodGang component creates the missing PodGangs, then requeue to
		// re-check and lift the gate.
		return groveerr.New(groveerr.ErrCodeContinueReconcileAndRequeue, component.OperationSync,
			fmt.Sprintf("All epoch-schemed PodGangs for PodCliqueSet %v are not yet created, holding the migration gate until they exist", client.ObjectKeyFromObject(pcs)))
	}
	return r.clearMigrationGate(ctx, logger, pcs)
}

// migrateReplica migrates one PodCliqueSet replica. It does the following:
//   - Resolve the epoch-based PodGang name each PodClique must carry from the PodGangMap.
//   - Rewrite grove.io/podgang and drop grove.io/base-podgang on the PodClique.
//   - Do the same on every Pod of that PodClique, reusing the PodClique's name since a Pod inherits its
//     owner PodClique's PodGang.
func (r _resource) migrateReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int) error {
	pgm, err := componentutils.GetPodGangMap(ctx, r.client, client.ObjectKeyFromObject(pcs), pcsReplicaIndex)
	if err != nil {
		return groveerr.WrapError(err, errCodeGetPodGangMap, component.OperationSync,
			fmt.Sprintf("failed to get PodGangMap for replica %d of PodCliqueSet %v", pcsReplicaIndex, client.ObjectKeyFromObject(pcs)))
	}
	pcsRnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}

	replicaSelector := lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex)},
	)
	pclqs, err := componentutils.ListPCLQsMatchingLabels(ctx, r.client, pcs.Namespace, replicaSelector)
	if err != nil {
		return groveerr.WrapError(err, errCodeListPodCliques, component.OperationSync,
			fmt.Sprintf("failed to list PodCliques for replica %d of PodCliqueSet %v", pcsReplicaIndex, client.ObjectKeyFromObject(pcs)))
	}

	for _, pclq := range pclqs {
		targetPodGangName, err := resolveTargetPodGangName(pgm, pcsRnr, &pclq.ObjectMeta)
		if err != nil {
			return err
		}
		if err := r.migratePodGangLabels(ctx, &pclq, targetPodGangName); err != nil {
			return err
		}
		if err := r.migratePodsOfPodClique(ctx, pcs.Namespace, pclq.Name, targetPodGangName); err != nil {
			return err
		}
	}
	return nil
}

// resolveTargetPodGangName returns the epoch-based PodGang name the given object must carry. The object
// is a PodClique or one of its Pods, identified by its ObjectMeta.
//   - When the object has no grove.io/podcliquescalinggroup label it is standalone and belongs to the
//     anchor PodGang, whose name is built from the PodGangMap's anchor epoch.
//   - When the object has a grove.io/podcliquescalinggroup label it is owned by a PodCliqueScalingGroup.
//     Its grove.io/podcliquescalinggroup-replica-index label selects the PodGangMap entry that owns that
//     replica index, and that entry's PodGang is the target.
func resolveTargetPodGangName(pgm *grovecorev1alpha1.PodGangMap, pcsRnr apicommon.ResourceNameReplica, objMeta *metav1.ObjectMeta) (string, error) {
	pcsgFQN, isPCSGOwned := objMeta.Labels[apicommon.LabelPodCliqueScalingGroup]
	if !isPCSGOwned {
		epoch, err := componentutils.AnchorPodGangEpoch(pgm)
		if err != nil {
			return "", groveerr.WrapError(err, errCodeResolvePodGangName, component.OperationSync,
				fmt.Sprintf("failed to resolve anchor PodGang epoch for %q", objMeta.Name))
		}
		return apicommon.GenerateAnchorPodGangName(pcsRnr, epoch), nil
	}
	pcsgConfigName, err := apicommon.ExtractScalingGroupNameFromPCSGFQN(pcsgFQN, pcsRnr)
	if err != nil {
		return "", groveerr.WrapError(err, errCodeResolvePodGangName, component.OperationSync,
			fmt.Sprintf("failed to extract PodCliqueScalingGroup config name from %q for %q", pcsgFQN, objMeta.Name))
	}
	indexLabel := objMeta.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex]
	pcsgReplicaIndex, err := strconv.Atoi(indexLabel)
	if err != nil {
		return "", groveerr.New(errCodeResolvePodGangName, component.OperationSync,
			fmt.Sprintf("label %s on %q is not a valid integer %q", apicommon.LabelPodCliqueScalingGroupReplicaIndex, objMeta.Name, indexLabel))
	}
	podGangName, err := componentutils.PodGangNameForPCSGReplica(pgm, pcsRnr, pcsgConfigName, int32(pcsgReplicaIndex))
	if err != nil {
		return "", groveerr.WrapError(err, errCodeResolvePodGangName, component.OperationSync,
			fmt.Sprintf("failed to resolve PodGang name for %q", objMeta.Name))
	}
	return podGangName, nil
}

// migratePodGangLabels rewrites the PodGang-scheme labels on obj to the new scheme. It does the following:
//   - Set grove.io/podgang to targetPodGangName.
//   - Drop the legacy grove.io/base-podgang label.
//
// It patches only on divergence and touches labels only, never spec, so no Pod is recreated. Pods
// carry additional scheduler-backend membership annotations, so they migrate through migratePodLabelsAndAnnotations
// instead.
func (r _resource) migratePodGangLabels(ctx context.Context, obj client.Object, targetPodGangName string) error {
	labels := obj.GetLabels()
	if len(labels) == 0 {
		return groveerr.New(errCodeMissingLabels, component.OperationSync,
			fmt.Sprintf("%v has no labels, which is an unexpected state for an operator-managed resource", client.ObjectKeyFromObject(obj)))
	}
	if !podGangLabelsNeedUpdate(labels, targetPodGangName) {
		return nil
	}
	original := obj.DeepCopyObject().(client.Object)
	rewritePodGangLabels(labels, targetPodGangName)
	obj.SetLabels(labels)
	if err := r.client.Patch(ctx, obj, client.MergeFrom(original)); err != nil {
		return groveerr.WrapError(err, errCodePatchPodGangLabels, component.OperationSync,
			fmt.Sprintf("failed to migrate PodGang labels on %v", client.ObjectKeyFromObject(obj)))
	}
	return nil
}

// migratePodsOfPodClique migrates every non-terminating Pod owned by the named PodClique to the
// epoch-based scheme. On each Pod it rewrites grove.io/podgang to targetPodGangName, drops
// grove.io/base-podgang, and refreshes the scheduler-backend membership annotations so the Pod joins
// the epoch-based scheduler PodGroup.
func (r _resource) migratePodsOfPodClique(ctx context.Context, namespace, pclqName, targetPodGangName string) error {
	podList := &corev1.PodList{}
	if err := r.client.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels(map[string]string{apicommon.LabelPodClique: pclqName}),
	); err != nil {
		return groveerr.WrapError(err, errCodeListPods, component.OperationSync,
			fmt.Sprintf("failed to list Pods for PodClique %q in namespace %q", pclqName, namespace))
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		if err := r.migratePodLabelsAndAnnotations(ctx, pod, targetPodGangName); err != nil {
			return err
		}
	}
	return nil
}

// migratePodLabelsAndAnnotations migrates a single Pod to the epoch-based scheme in one patch. It rewrites the PodGang
// labels and refreshes the scheduler-backend membership annotations for targetPodGangName. It patches
// only on divergence and touches metadata only, never spec, so the Pod is not recreated.
func (r _resource) migratePodLabelsAndAnnotations(ctx context.Context, pod *corev1.Pod, targetPodGangName string) error {
	labels := pod.GetLabels()
	if len(labels) == 0 {
		return groveerr.New(errCodeMissingLabels, component.OperationSync,
			fmt.Sprintf("%v has no labels, which is an unexpected state for an operator-managed resource", client.ObjectKeyFromObject(pod)))
	}
	membership := r.podGangMembershipAnnotations(pod, targetPodGangName)
	if !podGangLabelsNeedUpdate(labels, targetPodGangName) && !annotationsNeedUpdate(pod.Annotations, membership) {
		return nil
	}
	original := pod.DeepCopy()
	rewritePodGangLabels(labels, targetPodGangName)
	pod.SetLabels(labels)
	if len(membership) > 0 {
		pod.Annotations = lo.Assign(pod.Annotations, membership)
	}
	if err := r.client.Patch(ctx, pod, client.MergeFrom(original)); err != nil {
		return groveerr.WrapError(err, errCodePatchPodGangLabels, component.OperationSync,
			fmt.Sprintf("failed to migrate PodGang metadata on %v", client.ObjectKeyFromObject(pod)))
	}
	return nil
}

// podGangLabelsNeedUpdate reports whether the PodGang-scheme labels differ from the epoch-based scheme,
// i.e. grove.io/podgang is not yet targetPodGangName or the legacy grove.io/base-podgang label is
// still present.
func podGangLabelsNeedUpdate(labels map[string]string, targetPodGangName string) bool {
	_, hasBasePodGang := labels[apicommon.LabelBasePodGang]
	return labels[apicommon.LabelPodGang] != targetPodGangName || hasBasePodGang
}

// rewritePodGangLabels rewrites the PodGang-scheme labels in place: it sets grove.io/podgang to
// targetPodGangName and drops the legacy grove.io/base-podgang label.
func rewritePodGangLabels(labels map[string]string, targetPodGangName string) {
	labels[apicommon.LabelPodGang] = targetPodGangName
	delete(labels, apicommon.LabelBasePodGang)
}

// podGangMembershipAnnotations returns the scheduler-specific membership annotations the Pod's
// scheduler backend requires for targetPodGangName, or nil when the backend defines none (for example
// the plain kube backend). The Pod's spec.schedulerName selects the backend.
func (r _resource) podGangMembershipAnnotations(pod *corev1.Pod, targetPodGangName string) map[string]string {
	annotator, ok := r.schedRegistry.GetOrDefault(pod.Spec.SchedulerName).(podgangmigrator.PodGangMembershipAnnotator)
	if !ok {
		return nil
	}
	return annotator.PodGangMembershipAnnotations(targetPodGangName)
}

// annotationsNeedUpdate reports whether existing lacks any key of desired or maps it to a different
// value.
func annotationsNeedUpdate(existing, desired map[string]string) bool {
	for k, v := range desired {
		if existing[k] != v {
			return true
		}
	}
	return false
}

// allEpochBasedPodGangsCreated checks if all expected epoch-scheme based PodGangs are created for the PCS.
// It derives the expected PodGang names using PodGangMap entries and anchor/non-anchor PodGang name generator functions.
func (r _resource) allEpochBasedPodGangsCreated(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (bool, error) {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	for pcsReplicaIndex := range int(pcs.Spec.Replicas) {
		pgm, err := componentutils.GetPodGangMap(ctx, r.client, client.ObjectKeyFromObject(pcs), pcsReplicaIndex)
		if err != nil {
			return false, groveerr.WrapError(err, errCodeGetPodGangMap, component.OperationSync,
				fmt.Sprintf("failed to get PodGangMap for replica %d of PodCliqueSet %v", pcsReplicaIndex, pcsObjectKey))
		}
		pcsRnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
		for _, entry := range pgm.Spec.Entries {
			expectedPodGangNames := expectedPodGangNamesForEntry(pcsRnr, entry)
			for _, expectedPodGangName := range expectedPodGangNames {
				pgExists, err := r.podGangExists(ctx, pcs.Namespace, expectedPodGangName)
				if err != nil {
					return false, err
				}
				if !pgExists {
					return false, nil
				}
			}
		}
	}
	return true, nil
}

// expectedPodGangNamesForEntry computes the expected PodGang names that are associated to the given PodGangEntry.
// For an anchor entry, there will be just one PodGang and for non-anchor entry there can be one or more PodGangs.
func expectedPodGangNamesForEntry(pcsRnr apicommon.ResourceNameReplica, entry grovecorev1alpha1.PodGangEntry) []string {
	if entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor {
		return []string{apicommon.GenerateAnchorPodGangName(pcsRnr, entry.Epoch)}
	}
	var nonAnchorPodGangNames []string
	for pcsgName, pcsgReplicaIndices := range entry.PCSGReplicaIndices {
		for _, pcsgReplicaIndex := range pcsgReplicaIndices {
			nonAnchorPodGangNames = append(nonAnchorPodGangNames, apicommon.GenerateNonAnchorPodGangName(pcsRnr, entry.Epoch, pcsgName, pcsgReplicaIndex))
		}
	}
	return nonAnchorPodGangNames
}

// podGangExists reports whether the named PodGang exists in the namespace.
func (r _resource) podGangExists(ctx context.Context, namespace, podGangName string) (bool, error) {
	pg := &groveschedulerv1alpha1.PodGang{}
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podGangName}, pg); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, groveerr.WrapError(err, errCodeGetPodGang, component.OperationSync,
			fmt.Sprintf("failed to get PodGang %q in namespace %q", podGangName, namespace))
	}
	return true, nil
}

// clearMigrationGate removes the PodGangMigrationInProgress condition once every replica is migrated,
// unblocking the PodCliqueScalingGroup and PodClique reconcilers.
func (r _resource) clearMigrationGate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	if !meta.RemoveStatusCondition(&pcs.Status.Conditions, apiconstants.ConditionTypePodGangMigrationInProgress) {
		return nil
	}
	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return groveerr.WrapError(err, errCodeClearMigrationGate, component.OperationSync,
			fmt.Sprintf("failed to clear PodGang migration gate on PodCliqueSet %v", client.ObjectKeyFromObject(pcs)))
	}
	logger.Info("Cleared PodGang migration gate. PodCliqueSet migrated to epoch-based scheme", "podCliqueSet", client.ObjectKeyFromObject(pcs))
	return nil
}

// Delete is a no-op. The PodGangs, PodCliques and Pods this component migrates are owned and deleted by
// their own components on PodCliqueSet teardown.
func (r _resource) Delete(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) error {
	return nil
}
