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

package expectations

import (
	"fmt"
	"sync"

	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// CurrentlyUpdating tracks the PodCliqueSet CurrentlyUpdating selection that must appear in the informer
// cache before the next PodCliqueSet reconciliation plans further update work. It chiefly protects PCS
// replica recreation, whose planner reads CurrentlyUpdating to compute the replacement budget and the
// reserved replica indices. A stale read there could select a conflicting replacement.
type CurrentlyUpdating struct {
	expectations sync.Map
}

// Record stores the CurrentlyUpdating selection that must be observed through the informer cache.
func (e *CurrentlyUpdating) Record(pcsObjectMeta metav1.ObjectMeta, currentlyUpdating []grovev1beta1.PodCliqueSetReplicaUpdateProgress) {
	e.expectations.Store(currentlyUpdatingExpectationKey(pcsObjectMeta), digestCurrentlyUpdating(currentlyUpdating))
}

// ClearIfSatisfied reports whether no expectation exists or the cached CurrentlyUpdating selection matches
// it. A matching expectation is removed.
//
// A plain Delete is used rather than sync.Map.CompareAndDelete because the stored value is a
// sets.Set (a map), which is not comparable with ==, so CompareAndDelete would never delete it.
// This is safe because controller-runtime serializes reconciliations of a single PodCliqueSet, so no
// concurrent Record for the same key can occur between the Load and the Delete below.
func (e *CurrentlyUpdating) ClearIfSatisfied(pcsObjectMeta metav1.ObjectMeta, currentlyUpdating []grovev1beta1.PodCliqueSetReplicaUpdateProgress) bool {
	expectationKey := currentlyUpdatingExpectationKey(pcsObjectMeta)
	storedExpectation, exists := e.expectations.Load(expectationKey)
	if !exists {
		return true
	}
	expectedDigests := storedExpectation.(sets.Set[replicaUpdateProgressDigest])
	if !expectedDigests.Equal(digestCurrentlyUpdating(currentlyUpdating)) {
		return false
	}
	e.expectations.Delete(expectationKey)
	return true
}

// Forget removes the CurrentlyUpdating expectation for the PodCliqueSet.
func (e *CurrentlyUpdating) Forget(pcsObjectMeta metav1.ObjectMeta) {
	e.expectations.Delete(currentlyUpdatingExpectationKey(pcsObjectMeta))
}

// currentlyUpdatingExpectationKey returns the expectation key for a PodCliqueSet.
func currentlyUpdatingExpectationKey(pcsObjectMeta metav1.ObjectMeta) string {
	return fmt.Sprintf("%s/%s/%s", pcsObjectMeta.Namespace, pcsObjectMeta.Name, pcsObjectMeta.UID)
}

// replicaUpdateProgressDigest is the subset of a PodCliqueSetReplicaUpdateProgress entry that the
// CurrentlyUpdating expectation compares. It captures the selection and replacement-lifecycle facts that
// drive planning and completion, ignoring timestamps, Message, and InFlightEpochs.
//
// NOTE to future authors: every field must remain a comparable value type (no pointers, slices, or maps).
// The digest is used as a set element, and set equality relies on Go struct equality over all fields.
// Adding a non-comparable field, or a pointer whose identity differs across reconciliations, would break
// ClearIfSatisfied.
type replicaUpdateProgressDigest struct {
	// replicaIndex identifies the PCS replica selected for update. For replica recreation, it identifies
	// the replacement source rather than the target.
	replicaIndex int32
	// hasReplacement reports whether the entry records a source-to-target PCS replica replacement.
	// It distinguishes a replacement targeting index zero from an entry without replacement progress.
	hasReplacement bool
	// targetReplicaIndex identifies the replacement target PCS replica. It is meaningful only when
	// hasReplacement is true.
	targetReplicaIndex int32
	// canceled reports whether the replacement's CanceledAt is set. The timestamp value is not compared.
	canceled bool
	// sourceCleanupCompleted reports whether SourceCleanupCompletedAt is set. The timestamp value is
	// ignored, and retired-source cleanup is compared separately.
	sourceCleanupCompleted bool
	// ended reports whether the replica update's UpdateEndedAt is set. It distinguishes completed entries
	// from unfinished work without comparing when completion occurred.
	ended bool
	// retiredSourcePCSReplicaIndices encodes the sorted retired-source set as a string so the digest remains
	// comparable. Ordering and duplicates are ignored, and nil and empty lists are equivalent.
	retiredSourcePCSReplicaIndices string
}

// digestCurrentlyUpdating reduces the CurrentlyUpdating entries to the set of facts the expectation compares.
func digestCurrentlyUpdating(currentlyUpdating []grovev1beta1.PodCliqueSetReplicaUpdateProgress) sets.Set[replicaUpdateProgressDigest] {
	digests := sets.New[replicaUpdateProgressDigest]()
	for _, replicaProgress := range currentlyUpdating {
		digest := replicaUpdateProgressDigest{
			replicaIndex: replicaProgress.ReplicaIndex,
			ended:        replicaProgress.UpdateEndedAt != nil,
		}
		if replicaProgress.Replacement != nil {
			digest.hasReplacement = true
			digest.targetReplicaIndex = replicaProgress.Replacement.TargetReplicaIndex
			digest.canceled = replicaProgress.Replacement.CanceledAt != nil
			digest.sourceCleanupCompleted = replicaProgress.Replacement.SourceCleanupCompletedAt != nil
			retiredSourcePCSReplicaIndices := sets.New(replicaProgress.Replacement.RetiredSourceReplicaIndices...)
			digest.retiredSourcePCSReplicaIndices = fmt.Sprint(sets.List(retiredSourcePCSReplicaIndices))
		}
		digests.Insert(digest)
	}
	return digests
}
