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

package podclique

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcilePCSGReplicaDistribution drives the desired-state sync for the PodCliques owned by a
// PodCliqueScalingGroup. It updates pcsg.Status.PodGangMapping to the desired per-PodGang replica
// assignments and then reconciles live PodCliques to match.
//
// Why the desired distribution is recorded in status:
// A resource's status usually carries observed state, and desired state usually lives in the spec.
// This field is neither. It is a decision the controller derives and owns, and it is recorded in
// status because it cannot be expressed in the spec nor reconstructed by observation. Kubernetes
// sanctions status carrying controller-owned derived state of this kind, so this is not a misuse of
// status. Three points make the case:
//   - It is not user intent. The intent is Spec.Replicas = N. This field is the controller's derived
//     placement decision, not a second copy of intent.
//   - It cannot be reconstructed from what is observable. The needed placement is history-dependent,
//     meaning the correct target can differ for two pasts that present identically today. If a PCSG
//     replica is lost out of band, its replacement must go back into the PodGang the lost replica
//     was in, but the spec, the PodGangMap, and the live PodCliques together do not record which
//     PodGang that was. Only the persisted replica indices tell the reconciler to refill that
//     PodGang rather than the most recent one.
//   - It is single-writer and self-consistent. It is written and read by this same reconciler in
//     lockstep with its own actions, so its correctness does not depend on the PodGangMap
//     component's asynchronous update timing.
//
// Direction of authority:
//   - Coherent update in progress: the PodGangMap drives. status.PodGangMapping is rebuilt from PGM
//     entries each reconcile.
//   - Steady state: status.PodGangMapping drives. A scale-out appends new ScaleOut assignments and a
//     scale-in drains assignments by role.
//
// Each assignment records the entry epoch, role, anchor index, and this PCSG's replica indices. The
// PodGang name is not stored; reconcilePCSGReplicasToDesiredMapping derives it from the replica index.
func (r _resource) reconcilePCSGReplicaDistribution(ctx context.Context, logger logr.Logger, ss *syncState) error {
	desiredMapping, err := r.computeDesiredPCSGReplicaMapping(ss)
	if err != nil {
		return err
	}
	if err = r.patchPCSGPodGangMapping(ctx, ss, desiredMapping); err != nil {
		return err
	}
	return r.reconcilePCSGReplicasToDesiredMapping(ctx, logger, ss, desiredMapping)
}

// computeDesiredPCSGReplicaMapping returns the desired per-PodGang replica assignments for this PCSG.
//
// During a coherent update the PodGangMap is authoritative and the assignments are rebuilt from PGM
// entries. In steady state the existing status assignments are the source of truth and are adjusted
// only when Spec.Replicas drifts from the assigned replica count.
func (r _resource) computeDesiredPCSGReplicaMapping(ss *syncState) ([]grovecorev1alpha1.PodGangReplicaAssignment, error) {
	if componentutils.IsCoherentUpdateInProgress(ss.pcs) {
		return r.buildMappingFromPodGangMap(ss), nil
	}

	var desired []grovecorev1alpha1.PodGangReplicaAssignment
	if len(ss.pcsg.Status.PodGangMapping) == 0 {
		// Fresh PCSG — seed from the PodGangMap (created by the PCS reconciler from spec).
		desired = r.buildMappingFromPodGangMap(ss)
	} else {
		desired = cloneAssignments(ss.pcsg.Status.PodGangMapping)
	}

	currentSum := sumReplicaIndices(desired)
	diff := ss.pcsg.Spec.Replicas - currentSum
	switch {
	case diff > 0:
		// Scale-out adds the new replica indices [currentSum, Spec.Replicas) to the ScaleOut
		// assignment. The index set is contiguous, so a new replica takes the next index up. The
		// PodGang name is not stored (consumers derive it from the index). The epoch is the pre-created
		// ScaleOut entry's epoch read from the PodGangMap.
		scaleOutEpoch, err := scaleOutEpochFromPGM(ss)
		if err != nil {
			return nil, err
		}
		desired = appendToScaleOutAssignment(desired, lo.RangeFrom(currentSum, int(diff)), scaleOutEpoch)
	case diff < 0:
		drainAssignmentsForScaleIn(desired, int(-diff))
	}
	return dropEmptyAssignments(desired), nil
}

// scaleOutEpochFromPGM returns the epoch of the current-generation ScaleOut entry in the PodGangMap.
// The PodGangMap pre-creates exactly one ScaleOut entry per PCS replica whenever the PodCliqueSet has
// any PodCliqueScalingGroup, so a scale-out never mints an epoch, it attaches to this one. Absence is
// a contract violation and returns an error.
func scaleOutEpochFromPGM(ss *syncState) (string, error) {
	currentHash := *ss.pcs.Status.CurrentGenerationHash
	for _, entry := range ss.podGangMap.Spec.Entries {
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut && entry.PodCliqueSetGenerationHash == currentHash {
			return entry.Epoch, nil
		}
	}
	return "", fmt.Errorf("no ScaleOut entry for generation %s in PodGangMap of PodCliqueSet %s replica %d", currentHash, ss.pcs.Name, ss.pcsReplicaIndex)
}

// buildMappingFromPodGangMap constructs the per-PodGangMap-entry replica assignments for this PCSG
// from the PCS replica's PodGangMap (cached on sc.podGangMap). Each assignment is one PodGangMap entry
// projected onto this PCSG. It carries the entry epoch, role, anchor index, and the replica indices
// this PCSG contributes to that entry. The PodGang name is not stored. Consumers derive it from the
// index. Entries that do not reference this PCSG are skipped.
func (r _resource) buildMappingFromPodGangMap(ss *syncState) []grovecorev1alpha1.PodGangReplicaAssignment {
	var assignments []grovecorev1alpha1.PodGangReplicaAssignment
	for _, entry := range ss.podGangMap.Spec.Entries {
		indices, ok := entry.PCSGReplicaIndices[ss.pcsgConfig.Name]
		if !ok || len(indices) == 0 {
			continue
		}
		assignments = append(assignments, grovecorev1alpha1.PodGangReplicaAssignment{
			Epoch:          entry.Epoch,
			Role:           entry.Role,
			AnchorIndex:    entry.AnchorIndex,
			ReplicaIndices: slices.Clone(indices),
		})
	}
	return assignments
}

// cloneAssignments deep-clones a slice of PodGangReplicaAssignment so the caller can mutate without
// aliasing the original (which is typically a status field).
func cloneAssignments(in []grovecorev1alpha1.PodGangReplicaAssignment) []grovecorev1alpha1.PodGangReplicaAssignment {
	out := make([]grovecorev1alpha1.PodGangReplicaAssignment, len(in))
	for i := range in {
		in[i].DeepCopyInto(&out[i])
	}
	return out
}

// sumReplicaIndices returns the total count of PCSG replica indices across all assignments.
func sumReplicaIndices(assignments []grovecorev1alpha1.PodGangReplicaAssignment) int32 {
	var total int32
	for i := range assignments {
		total += int32(len(assignments[i].ReplicaIndices))
	}
	return total
}

// appendToScaleOutAssignment adds the given PodCliqueScalingGroup replica indices to the ScaleOut
// assignment, creating that assignment with the given scaleOutEpoch if this PodCliqueScalingGroup has
// no ScaleOut assignment yet. The epoch is the PodGangMap's pre-created ScaleOut entry epoch, so the
// assignment always carries a real epoch. It returns the resulting slice.
func appendToScaleOutAssignment(pgReplicaAssignments []grovecorev1alpha1.PodGangReplicaAssignment, pcsgIndices []int32, scaleOutEpoch string) []grovecorev1alpha1.PodGangReplicaAssignment {
	for i := range pgReplicaAssignments {
		if pgReplicaAssignments[i].Role == grovecorev1alpha1.PodGangEntryRoleScaleOut {
			pgReplicaAssignments[i].ReplicaIndices = append(pgReplicaAssignments[i].ReplicaIndices, pcsgIndices...)
			return pgReplicaAssignments
		}
	}
	scaleOutAssignment := grovecorev1alpha1.PodGangReplicaAssignment{
		Epoch:          scaleOutEpoch,
		Role:           grovecorev1alpha1.PodGangEntryRoleScaleOut,
		ReplicaIndices: pcsgIndices,
	}
	return append(pgReplicaAssignments, scaleOutAssignment)
}

// drainAssignmentsForScaleIn removes count PCSG replica indices from the assignments for a scale-in.
// It drains in role order: ScaleOut PodGangs first, then Tail, then Anchor from the highest AnchorIndex
// down, so the anchor with AnchorIndex 0 (which carries the MinAvailable replicas) is drained last and
// only when it is the sole or final anchor. The webhook guarantees Spec.Replicas - count >= MinAvailable,
// so the drain removes count indices without reducing the clique below MinAvailable. Within a chosen
// assignment the highest replica index is removed first. Emptied assignments are left in place and
// removed by the caller. This sorts assignments in place; the order is irrelevant to callers.
func drainAssignmentsForScaleIn(pgReplicaAssignments []grovecorev1alpha1.PodGangReplicaAssignment, count int) {
	drainPriority := func(a grovecorev1alpha1.PodGangReplicaAssignment) int {
		switch a.Role {
		case grovecorev1alpha1.PodGangEntryRoleScaleOut:
			return 0
		case grovecorev1alpha1.PodGangEntryRoleTail:
			return 1
		default:
			return 2
		}
	}
	// Order the assignments so a scale-in drains ScaleOut first, then Tail, then Anchor. Among
	// anchors the highest AnchorIndex is drained first so the AnchorIndex 0 anchor is reached last.
	sort.SliceStable(pgReplicaAssignments, func(i, j int) bool {
		ip := drainPriority(pgReplicaAssignments[i])
		jp := drainPriority(pgReplicaAssignments[j])
		if ip != jp {
			return ip < jp
		}
		if pgReplicaAssignments[i].Role == grovecorev1alpha1.PodGangEntryRoleAnchor {
			return pgReplicaAssignments[i].AnchorIndex > pgReplicaAssignments[j].AnchorIndex
		}
		return false
	})

	remaining := count
	for i := range pgReplicaAssignments {
		if remaining == 0 {
			break
		}
		a := &pgReplicaAssignments[i]
		slices.Sort(a.ReplicaIndices)
		take := min(remaining, len(a.ReplicaIndices))
		a.ReplicaIndices = a.ReplicaIndices[:len(a.ReplicaIndices)-take]
		remaining -= take
	}
}

// dropEmptyAssignments removes assignments whose ReplicaIndices are empty. These arise from a
// scale-in that drained a PodGang's indices to zero.
func dropEmptyAssignments(assignments []grovecorev1alpha1.PodGangReplicaAssignment) []grovecorev1alpha1.PodGangReplicaAssignment {
	return slices.DeleteFunc(assignments, func(a grovecorev1alpha1.PodGangReplicaAssignment) bool {
		return len(a.ReplicaIndices) == 0
	})
}

// patchPCSGPodGangMapping persists the desired assignments to pcsg.Status.PodGangMapping if they
// differ from the current value. The desired assignments are canonicalized (sorted by PodGang name,
// each ReplicaIndices sorted) so the stored order is deterministic and a plain equality check avoids
// waking other reconcilers via a no-op patch. An empty desired is normalized to nil.
func (r _resource) patchPCSGPodGangMapping(ctx context.Context, ss *syncState, desired []grovecorev1alpha1.PodGangReplicaAssignment) error {
	canonicalizeAssignments(desired)
	if len(desired) == 0 {
		desired = nil
	}
	if reflect.DeepEqual(ss.pcsg.Status.PodGangMapping, desired) {
		return nil
	}
	patch := client.MergeFrom(ss.pcsg.DeepCopy())
	ss.pcsg.Status.PodGangMapping = desired
	if err := client.IgnoreNotFound(r.client.Status().Patch(ctx, ss.pcsg, patch)); err != nil {
		return groveerr.WrapError(err,
			errCodeUpdateStatus,
			component.OperationSync,
			fmt.Sprintf("failed to patch PodGangMapping on PodCliqueScalingGroup %v",
				client.ObjectKeyFromObject(ss.pcsg)))
	}
	return nil
}

// canonicalizeAssignments sorts assignments by Epoch and sorts each assignment's ReplicaIndices
// ascending, giving a deterministic stored order so equality checks and persisted status are stable.
// Epoch is unique per assignment (the single ScaleOut assignment has an empty epoch and sorts first).
func canonicalizeAssignments(assignments []grovecorev1alpha1.PodGangReplicaAssignment) {
	for i := range assignments {
		slices.Sort(assignments[i].ReplicaIndices)
	}
	slices.SortFunc(assignments, func(a, b grovecorev1alpha1.PodGangReplicaAssignment) int {
		return cmp.Compare(a.Epoch, b.Epoch)
	})
}

// reconcilePCSGReplicasToDesiredMapping reconciles the live PodCliques of this PodCliqueScalingGroup
// to the desired replica assignments, in order:
//  1. Derive each replica index's target PodGang name from the assignment role. An anchor
//     assignment's indices map to the one anchor PodGang. A non-anchor assignment's indices each map
//     to their own non-anchor PodGang.
//  2. Compute which replica indices to create and which to delete.
//  3. Apply deletions before creations, so the controller does not race itself at an index still
//     held by a doomed PodClique.
//  4. Sync the PodClique specs for the OnDelete strategy.
//
// A replica index whose live PodGang label disagrees with the desired name is deleted and recreated
// under the correct PodGang on the next reconcile. A replica index absent from every assignment is
// obsolete and its PodCliques are deleted.
func (r _resource) reconcilePCSGReplicasToDesiredMapping(ctx context.Context, logger logr.Logger, ss *syncState, desired []grovecorev1alpha1.PodGangReplicaAssignment) error {
	// Derive each replica index's PodGang name. An anchor assignment's indices all map to the anchor
	// PodGang name. A non-anchor assignment's indices each map to their own non-anchor PodGang name.
	desiredIndexToPG := make(map[int]string)
	for i := range desired {
		for _, idx := range desired[i].ReplicaIndices {
			if desired[i].Role == grovecorev1alpha1.PodGangEntryRoleAnchor {
				desiredIndexToPG[int(idx)] = apicommon.GenerateAnchorPodGangName(ss.pcsResourceNameReplica, desired[i].Epoch)
			} else {
				desiredIndexToPG[int(idx)] = apicommon.GenerateNonAnchorPodGangName(ss.pcsResourceNameReplica, desired[i].Epoch, ss.pcsgConfig.Name, idx)
			}
		}
	}

	deletions, creations, err := computePCSGReplicaCreationsAndDeletions(desiredIndexToPG, ss.existingPCLQs, ss.pcsg.Spec.CliqueNames)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeParsePodCliqueScalingGroupReplicaIndex,
			component.OperationSync,
			fmt.Sprintf("failed to compute PCSG replica deltas for PodCliqueScalingGroup %v",
				client.ObjectKeyFromObject(ss.pcsg)))
	}
	logger.V(4).Info("pcsg indices for deletions and creations", "deletions", deletions, "creations", creations)
	if len(deletions) > 0 {
		if err = r.deletePCSGReplicas(ctx, logger, ss, deletions); err != nil {
			return err
		}
	}
	if len(creations) > 0 {
		if err = r.createPCSGReplicas(ctx, logger, ss, creations); err != nil {
			return err
		}
	}
	if componentutils.IsOnDeleteStrategy(ss.pcs) {
		if err = r.syncOnDeletePCLQSpecs(ctx, logger, ss, desiredIndexToPG); err != nil {
			return err
		}
	}
	return nil
}

// computePCSGReplicaCreationsAndDeletions compares desiredIndexToPG (the authoritative replica index
// → PodGang mapping from status) against the live PCLQs and returns:
//   - deletions: replica indices whose live PCLQs should be deleted. Sources:
//     1. Indices not in desiredIndexToPG (obsolete — index belongs to no PodGang).
//     2. Indices whose live LabelPodGang disagrees with desired (the PCLQ will be recreated
//     under the correct PodGang on the next reconcile).
//   - creations: index → PodGang for indices in desired that either have no surviving live PCLQ
//     or are only partially populated (some cliques present, some missing). Partial replicas
//     stay in `creations` so the next reconcile creates the missing siblings; the existing
//     PCLQs are left untouched (the Create attempt swallows AlreadyExists for the present ones).
//
// The live PCLQs are indexed by indexLivePCSGReplicas, which skips terminating PCLQs and validates
// the shared-PodGang-label contract. The AlreadyExists swallow in doCreate handles the brief race
// window where the operator tries to re-create an FQN whose old PCLQ is still terminating.
func computePCSGReplicaCreationsAndDeletions(desiredIndexToPG map[int]string, livePCLQs []grovecorev1alpha1.PodClique, pcsgCliqueNames []string) (deletionIndices []int, creations map[int]string, err error) {
	livePodGangByIndex, liveCliquesByIndex, err := indexLivePCSGReplicas(livePCLQs)
	if err != nil {
		return nil, nil, err
	}

	creations = make(map[int]string, len(desiredIndexToPG))
	maps.Copy(creations, desiredIndexToPG)

	expectedCliques := sets.New(pcsgCliqueNames...)
	for idx, livePodGangLabel := range livePodGangByIndex {
		desiredPG, inDesired := desiredIndexToPG[idx]
		if !inDesired || livePodGangLabel != desiredPG {
			// Either the index is obsolete or its PodGang label disagrees with desired —
			// delete and let the next reconcile recreate under the correct PodGang.
			deletionIndices = append(deletionIndices, idx)
			continue
		}
		// Correct PodGang label. The index is only fully covered when every clique in the
		// PCSG config has a non-terminating PCLQ at this index. A half-populated replica
		// (some cliques missing) keeps creations[idx] populated so the next reconcile creates
		// the missing siblings; doCreate swallows AlreadyExists for siblings that already exist.
		if liveCliquesByIndex[idx].Equal(expectedCliques) {
			delete(creations, idx)
		}
	}

	return deletionIndices, creations, nil
}

// indexLivePCSGReplicas groups non-terminating live PCLQs by PCSG replica index. It returns, per
// index, the LabelPodGang shared by every PCLQ at that index (livePodGangByIndex) and the set of
// clique names present (liveCliquesByIndex, used to detect half-populated indices). All live PCLQs
// at one index must share the same LabelPodGang, which Grove stamps once at creation and never
// updates. A missing label or divergent labels at one index indicate a contract violation and
// surface as an error. Terminating PCLQs are skipped.
func indexLivePCSGReplicas(livePCLQs []grovecorev1alpha1.PodClique) (livePodGangByIndex map[int]string, liveCliquesByIndex map[int]sets.Set[string], err error) {
	livePodGangByIndex = make(map[int]string, len(livePCLQs))
	liveCliquesByIndex = make(map[int]sets.Set[string], len(livePCLQs))
	for i := range livePCLQs {
		if k8sutils.IsResourceTerminating(livePCLQs[i].ObjectMeta) {
			continue
		}
		idx, parseErr := k8sutils.GetPodCliqueScalingGroupReplicaIndex(livePCLQs[i].ObjectMeta)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		pgLabel, ok := livePCLQs[i].Labels[apicommon.LabelPodGang]
		if !ok {
			return nil, nil, fmt.Errorf("PodClique %s is missing required %s label", livePCLQs[i].Name, apicommon.LabelPodGang)
		}
		if existing, seen := livePodGangByIndex[idx]; seen && existing != pgLabel {
			return nil, nil, fmt.Errorf("PodCliques at PCSG replica index %d have divergent %s labels: %q vs %q", idx, apicommon.LabelPodGang, existing, pgLabel)
		}
		livePodGangByIndex[idx] = pgLabel

		cliqueName, parseErr := utils.GetPodCliqueNameFromPodCliqueFQN(livePCLQs[i].ObjectMeta)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		if liveCliquesByIndex[idx] == nil {
			liveCliquesByIndex[idx] = sets.New[string]()
		}
		liveCliquesByIndex[idx].Insert(cliqueName)
	}
	return livePodGangByIndex, liveCliquesByIndex, nil
}

// deletePCSGReplicas deletes all PCLQs belonging to the given PCSG replica indices.
func (r _resource) deletePCSGReplicas(ctx context.Context, logger logr.Logger, ss *syncState, replicaIndices []int) error {
	deletionTasks := r.createDeleteTasks(logger, ss.pcs, ss.pcsg.Name, replicaIndices, "delete excess PCSG replicas")
	return r.triggerDeletionOfPodCliques(ctx, logger, client.ObjectKeyFromObject(ss.pcsg), deletionTasks)
}

// createPCSGReplicas creates the PCLQs for the given PCSG replica index → PodGang name
// assignments. Each replica generates one PCLQ per CliqueName in the PCSG config. Creation
// uses doCreate (a plain Create) since this path only handles PCLQs that don't yet exist; the
// OnDelete strategy's "preserve existing replicas via CreateOrPatch" behavior is irrelevant for
// fresh PCLQs.
func (r _resource) createPCSGReplicas(ctx context.Context, logger logr.Logger, ss *syncState, assignments map[int]string) error {
	tasks := make([]utils.Task, 0, len(assignments)*len(ss.pcsg.Spec.CliqueNames))
	// Sort assignments by index for deterministic creation order.
	indices := lo.Keys(assignments)
	sort.Ints(indices)
	for _, pcsgReplicaIndex := range indices {
		podGangName := assignments[pcsgReplicaIndex]
		for _, cliqueName := range ss.pcsg.Spec.CliqueNames {
			pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: ss.pcsg.Name, Replica: pcsgReplicaIndex}, cliqueName)
			pclqObjectKey := client.ObjectKey{Name: pclqFQN, Namespace: ss.pcsg.Namespace}
			pgName := podGangName
			replicaIdx := pcsgReplicaIndex
			tasks = append(tasks, utils.Task{
				Name: fmt.Sprintf("CreatePodClique-%s", pclqFQN),
				Fn: func(ctx context.Context) error {
					return r.doCreate(ctx, logger, ss.pcs, ss.pcsg, replicaIdx, pclqObjectKey, pgName)
				},
			})
		}
	}
	if runResult := utils.RunConcurrently(ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errCodeCreatePodCliques,
			component.OperationSync,
			fmt.Sprintf("Error creating PodCliques for PodCliqueScalingGroup: %v, run summary: %s",
				client.ObjectKeyFromObject(ss.pcsg), runResult.GetSummary()))
	}
	return nil
}

// syncOnDeletePCLQSpecs patches PCSG-owned PodCliques whose pod-template-hash label does not
// match the expected hash. Under OnDelete the reconciliation loop never deletes-and-recreates
// existing replicas — spec updates are picked up only when a pod is manually deleted. To ensure
// that the replacement pod gets the new spec, we must update the PCLQ resource itself, because
// the Pod component builds new pods from pclq.Spec.PodSpec. This is the PCSG equivalent of
// what the PCS controller does for standalone PCLQs via CreateOrPatch.
//
// Only PCLQs that belong to indices present in desiredIndexToPG are patched; PCLQs targeted for
// deletion (absent from desiredIndexToPG) are left alone — they will be removed by
// deletePCSGReplicas on this same reconcile.
func (r _resource) syncOnDeletePCLQSpecs(ctx context.Context, logger logr.Logger, ss *syncState, desiredIndexToPG map[int]string) error {
	tasks := make([]utils.Task, 0)
	for i := range ss.existingPCLQs {
		pclq := &ss.existingPCLQs[i]
		if k8sutils.IsResourceTerminating(pclq.ObjectMeta) {
			continue
		}
		idx, err := k8sutils.GetPodCliqueScalingGroupReplicaIndex(pclq.ObjectMeta)
		if err != nil {
			return groveerr.WrapError(err,
				errCodeParsePodCliqueScalingGroupReplicaIndex,
				component.OperationSync,
				fmt.Sprintf("failed to get PCSG replica index for PodClique %v", client.ObjectKeyFromObject(pclq)))
		}
		podGangName, inDesired := desiredIndexToPG[idx]
		if !inDesired {
			continue
		}
		expectedHash, hasExpected := ss.expectedPCLQPodTemplateHashMap[pclq.Name]
		if !hasExpected {
			continue
		}
		currentHash := pclq.Labels[apicommon.LabelPodTemplateHash]
		if currentHash == expectedHash {
			continue
		}
		pclqObjectKey := client.ObjectKeyFromObject(pclq)
		pgName := podGangName
		replicaIdx := idx
		tasks = append(tasks, utils.Task{
			Name: fmt.Sprintf("UpdatePodClique-%s", pclqObjectKey.Name),
			Fn: func(ctx context.Context) error {
				return r.doCreateOrUpdate(ctx, logger, ss.pcs, ss.pcsg, replicaIdx, pclqObjectKey, true, pgName)
			},
		})
	}
	if len(tasks) == 0 {
		return nil
	}
	logger.Info("Patching PCSG-owned PodCliques to new spec for OnDelete strategy", "count", len(tasks))
	if runResult := utils.RunConcurrently(ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errCodeCreateOrUpdatePodCliques,
			component.OperationSync,
			fmt.Sprintf("Error updating PodCliques for OnDelete PodCliqueScalingGroup %v: %s",
				client.ObjectKeyFromObject(ss.pcsg), runResult.GetSummary()))
	}
	return nil
}
