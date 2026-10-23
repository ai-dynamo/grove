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

package sync

import (
	"fmt"
	"sync"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// ScalingProgressExpectations tracks scaling progress that must appear in the informer cache
// before the next PodCliqueSet reconciliation proceeds.
type ScalingProgressExpectations struct {
	expectations sync.Map
}

// Record stores the scaling progress that must be observed through the informer cache.
// Nil progress records the expectation that the status field is cleared.
func (e *ScalingProgressExpectations) Record(pcsObjectMeta metav1.ObjectMeta, progress *grovev1alpha1.PodCliqueSetScalingProgress) {
	expectationKey := scalingProgressExpectationKey(pcsObjectMeta)
	expectedProgress := &grovev1alpha1.PodCliqueSetScalingProgress{}
	if progress != nil {
		expectedProgress = progress.DeepCopy()
	}
	e.expectations.Store(expectationKey, expectedProgress)
}

// ClearIfSatisfied reports whether no expectation exists or the cached scaling progress matches it.
// A matching expectation is removed.
func (e *ScalingProgressExpectations) ClearIfSatisfied(pcsObjectMeta metav1.ObjectMeta, progress *grovev1alpha1.PodCliqueSetScalingProgress) bool {
	expectationKey := scalingProgressExpectationKey(pcsObjectMeta)
	storedExpectation, exists := e.expectations.Load(expectationKey)
	if !exists {
		return true
	}
	expectedProgress := storedExpectation.(*grovev1alpha1.PodCliqueSetScalingProgress)
	if !ScalingProgressEqual(expectedProgress, progress) {
		return false
	}
	return e.expectations.CompareAndDelete(expectationKey, storedExpectation)
}

// Forget removes the scaling-progress expectation for the PodCliqueSet.
func (e *ScalingProgressExpectations) Forget(pcsObjectMeta metav1.ObjectMeta) {
	e.expectations.Delete(scalingProgressExpectationKey(pcsObjectMeta))
}

// scalingProgressExpectationKey returns the expectation key for a PodCliqueSet.
func scalingProgressExpectationKey(pcsObjectMeta metav1.ObjectMeta) string {
	return fmt.Sprintf("%s/%s/%s", pcsObjectMeta.Namespace, pcsObjectMeta.Name, pcsObjectMeta.UID)
}

// ScalingProgressEqual reports whether two scaling-progress values contain the same replica-index sets.
// Nil and empty progress are equal.
func ScalingProgressEqual(left, right *grovev1alpha1.PodCliqueSetScalingProgress) bool {
	leftScaleOutReplicaIndices := sets.New[int32]()
	leftScaleInReplicaIndices := sets.New[int32]()
	if left != nil {
		leftScaleOutReplicaIndices.Insert(left.ScaleOutReplicaIndices...)
		leftScaleInReplicaIndices.Insert(left.ScaleInReplicaIndices...)
	}

	rightScaleOutReplicaIndices := sets.New[int32]()
	rightScaleInReplicaIndices := sets.New[int32]()
	if right != nil {
		rightScaleOutReplicaIndices.Insert(right.ScaleOutReplicaIndices...)
		rightScaleInReplicaIndices.Insert(right.ScaleInReplicaIndices...)
	}

	return leftScaleOutReplicaIndices.Equal(rightScaleOutReplicaIndices) &&
		leftScaleInReplicaIndices.Equal(rightScaleInReplicaIndices)
}
