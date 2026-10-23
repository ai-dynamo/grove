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
	"testing"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestScalingProgressExpectationsRecord(t *testing.T) {
	objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: types.UID("uid")}
	tests := []struct {
		name             string
		progress         *grovev1alpha1.PodCliqueSetScalingProgress
		expectedProgress *grovev1alpha1.PodCliqueSetScalingProgress
	}{
		{
			name:             "nil progress",
			expectedProgress: &grovev1alpha1.PodCliqueSetScalingProgress{},
		},
		{
			name: "non-empty progress",
			progress: &grovev1alpha1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{3, 1},
				ScaleInReplicaIndices:  []int32{4, 2},
			},
			expectedProgress: &grovev1alpha1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{3, 1},
				ScaleInReplicaIndices:  []int32{4, 2},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expectations := &ScalingProgressExpectations{}

			expectations.Record(objectMeta, test.progress)
			if test.progress != nil {
				test.progress.ScaleInReplicaIndices = nil
			}

			storedProgress, exists := expectations.expectations.Load(scalingProgressExpectationKey(objectMeta))
			require.True(t, exists)
			assert.Equal(t, test.expectedProgress, storedProgress)
		})
	}
}

func TestScalingProgressExpectationsClearIfSatisfied(t *testing.T) {
	objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: types.UID("uid")}
	tests := []struct {
		name             string
		expectedProgress *grovev1alpha1.PodCliqueSetScalingProgress
		observedProgress *grovev1alpha1.PodCliqueSetScalingProgress
		hasExpectation   bool
		expectSatisfied  bool
		expectCleared    bool
	}{
		{
			name:            "no expectation",
			expectSatisfied: true,
		},
		{
			name:             "matching progress",
			hasExpectation:   true,
			expectedProgress: &grovev1alpha1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{3, 1}},
			observedProgress: &grovev1alpha1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1, 3}},
			expectSatisfied:  true,
			expectCleared:    true,
		},
		{
			name:             "nil progress matches empty progress",
			hasExpectation:   true,
			expectedProgress: &grovev1alpha1.PodCliqueSetScalingProgress{},
			expectSatisfied:  true,
			expectCleared:    true,
		},
		{
			name:             "different progress",
			hasExpectation:   true,
			expectedProgress: &grovev1alpha1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{1}},
			observedProgress: &grovev1alpha1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{2}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expectations := &ScalingProgressExpectations{}
			if test.hasExpectation {
				expectations.expectations.Store(scalingProgressExpectationKey(objectMeta), test.expectedProgress)
			}

			satisfied := expectations.ClearIfSatisfied(objectMeta, test.observedProgress)

			assert.Equal(t, test.expectSatisfied, satisfied)
			_, exists := expectations.expectations.Load(scalingProgressExpectationKey(objectMeta))
			if test.expectCleared {
				assert.False(t, exists, "satisfied expectation must be cleared")
			} else if test.hasExpectation {
				assert.True(t, exists, "unsatisfied expectation must be retained")
			}
		})
	}

	t.Run("clears only the satisfied PodCliqueSet expectation", func(t *testing.T) {
		expectations := &ScalingProgressExpectations{}
		otherObjectMeta := metav1.ObjectMeta{Namespace: "default", Name: "other-pcs", UID: types.UID("other-uid")}
		expectedProgress := &grovev1alpha1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1}}
		expectations.expectations.Store(scalingProgressExpectationKey(objectMeta), expectedProgress)
		expectations.expectations.Store(scalingProgressExpectationKey(otherObjectMeta), expectedProgress.DeepCopy())

		satisfied := expectations.ClearIfSatisfied(objectMeta, expectedProgress)

		assert.True(t, satisfied)
		_, exists := expectations.expectations.Load(scalingProgressExpectationKey(objectMeta))
		assert.False(t, exists, "satisfied expectation must be cleared")
		_, exists = expectations.expectations.Load(scalingProgressExpectationKey(otherObjectMeta))
		assert.True(t, exists, "another PodCliqueSet expectation must be retained")
	})
}

func TestScalingProgressExpectationsForget(t *testing.T) {
	expectations := &ScalingProgressExpectations{}
	objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: types.UID("uid")}
	expectations.expectations.Store(
		scalingProgressExpectationKey(objectMeta),
		&grovev1alpha1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1}},
	)

	expectations.Forget(objectMeta)

	_, exists := expectations.expectations.Load(scalingProgressExpectationKey(objectMeta))
	assert.False(t, exists)
}

func TestScalingProgressEqual(t *testing.T) {
	tests := []struct {
		name     string
		left     *grovev1alpha1.PodCliqueSetScalingProgress
		right    *grovev1alpha1.PodCliqueSetScalingProgress
		expected bool
	}{
		{name: "nil values", expected: true},
		{name: "nil and empty", right: &grovev1alpha1.PodCliqueSetScalingProgress{}, expected: true},
		{
			name: "same index sets",
			left: &grovev1alpha1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{3, 1},
				ScaleInReplicaIndices:  []int32{4, 2},
			},
			right: &grovev1alpha1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{1, 3},
				ScaleInReplicaIndices:  []int32{2, 4},
			},
			expected: true,
		},
		{
			name:  "different index sets",
			left:  &grovev1alpha1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{1}},
			right: &grovev1alpha1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{2}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, ScalingProgressEqual(test.left, test.right))
		})
	}
}
