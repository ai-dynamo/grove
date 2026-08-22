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

package utils

import (
	"context"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestGetPCLQsByOwner tests the GetPCLQsByOwner function
func TestGetPCLQsByOwner(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// ownerKind is the kind of the owner
		ownerKind string
		// ownerObjectKey is the owner's object key
		ownerObjectKey client.ObjectKey
		// selectorLabels are the labels to match
		selectorLabels map[string]string
		// existingPCLQs are the existing PodCliques
		existingPCLQs []grovecorev1alpha1.PodClique
		// expectedPCLQs are the expected PodCliques
		expectedPCLQs []string
		// expectError indicates if an error is expected
		expectError bool
	}{
		{
			// Tests finding PodCliques owned by a PodCliqueSet
			name:      "finds_owned_podcliques",
			ownerKind: "PodCliqueSet",
			ownerObjectKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			selectorLabels: map[string]string{
				apicommon.LabelPartOfKey: "test-pcs",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "test-pcs",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-2",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "test-pcs",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-pclq",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "other-pcs",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "other-pcs",
							},
						},
					},
				},
			},
			expectedPCLQs: []string{"test-pclq-1", "test-pclq-2"},
			expectError:   false,
		},
		{
			// Tests when no PodCliques match the owner
			name:      "no_matching_owner",
			ownerKind: "PodCliqueSet",
			ownerObjectKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			selectorLabels: map[string]string{
				apicommon.LabelPartOfKey: "test-pcs",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "other-pcs",
							},
						},
					},
				},
			},
			expectedPCLQs: []string{},
			expectError:   false,
		},
		{
			// Tests when PodCliques have no owner references
			name:      "no_owner_references",
			ownerKind: "PodCliqueSet",
			ownerObjectKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			selectorLabels: map[string]string{
				apicommon.LabelPartOfKey: "test-pcs",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
					},
				},
			},
			expectedPCLQs: []string{},
			expectError:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup scheme
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			// Build runtime objects
			runtimeObjs := []runtime.Object{}
			for i := range tc.existingPCLQs {
				runtimeObjs = append(runtimeObjs, &tc.existingPCLQs[i])
			}

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(runtimeObjs...).
				Build()

			// Call function
			ctx := context.Background()
			pclqs, err := GetPCLQsByOwner(ctx, fakeClient, tc.ownerKind, tc.ownerObjectKey, tc.selectorLabels)

			// Verify results
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(tc.expectedPCLQs), len(pclqs))
				for i, pclq := range pclqs {
					assert.Equal(t, tc.expectedPCLQs[i], pclq.Name)
				}
			}
		})
	}
}

// TestGroupPCLQsByPodGangName tests the GroupPCLQsByPodGangName function
func TestGroupPCLQsByPodGangName(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pclqs is the list of PodCliques to group
		pclqs []grovecorev1alpha1.PodClique
		// expected is the expected grouping
		expected map[string][]grovecorev1alpha1.PodClique
	}{
		{
			// Tests grouping PodCliques by PodGang name
			name: "groups_by_podgang_name",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-1",
						Labels: map[string]string{
							apicommon.LabelPodGang: "podgang-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-2",
						Labels: map[string]string{
							apicommon.LabelPodGang: "podgang-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-3",
						Labels: map[string]string{
							apicommon.LabelPodGang: "podgang-2",
						},
					},
				},
			},
			expected: map[string][]grovecorev1alpha1.PodClique{
				"podgang-1": {
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "pclq-1",
							Labels: map[string]string{
								apicommon.LabelPodGang: "podgang-1",
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "pclq-2",
							Labels: map[string]string{
								apicommon.LabelPodGang: "podgang-1",
							},
						},
					},
				},
				"podgang-2": {
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "pclq-3",
							Labels: map[string]string{
								apicommon.LabelPodGang: "podgang-2",
							},
						},
					},
				},
			},
		},
		{
			// Tests with empty list
			name:     "empty_list",
			pclqs:    []grovecorev1alpha1.PodClique{},
			expected: map[string][]grovecorev1alpha1.PodClique{},
		},
		{
			// Tests with PodCliques missing PodGang label
			name: "missing_podgang_label",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "pclq-1",
						Labels: map[string]string{},
					},
				},
			},
			expected: map[string][]grovecorev1alpha1.PodClique{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GroupPCLQsByPodGangName(tc.pclqs)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestIsPCLQAutoUpdateInProgress tests the IsPCLQAutoUpdateInProgress function
func TestIsPCLQAutoUpdateInProgress(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pclq is the PodClique to check
		pclq *grovecorev1alpha1.PodClique
		// expected is the expected result
		expected bool
	}{
		{
			// Tests when no rolling update progress exists
			name: "no_rolling_update_progress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			// Tests when rolling update is in progress
			name: "update_in_progress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt: metav1.Now(),
					},
				},
			},
			expected: true,
		},
		{
			// Tests when rolling update is completed
			name: "update_completed",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   &metav1.Time{Time: metav1.Now().Time},
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsPCLQAutoUpdateInProgress(tc.pclq)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestIsLastPCLQUpdateCompleted tests the IsLastPCLQUpdateCompleted function
func TestIsLastPCLQUpdateCompleted(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pclq is the PodClique to check
		pclq *grovecorev1alpha1.PodClique
		// expected is the expected result
		expected bool
	}{
		{
			// Tests when no rolling update progress exists
			name: "no_rolling_update_progress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			// Tests when rolling update is in progress
			name: "update_in_progress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt: metav1.Now(),
					},
				},
			},
			expected: false,
		},
		{
			// Tests when rolling update is completed
			name: "update_completed",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   &metav1.Time{Time: metav1.Now().Time},
					},
				},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsLastPCLQUpdateCompleted(tc.pclq)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestWasPCLQEverScheduled covers the durable signal used to gate gang termination.
func TestWasPCLQEverScheduled(t *testing.T) {
	tests := []struct {
		name string
		pclq *grovecorev1alpha1.PodClique
		want bool
	}{
		{
			name: "no durable condition",
			pclq: &grovecorev1alpha1.PodClique{},
			want: false,
		},
		{
			name: "scheduled condition alone does not count",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{Conditions: []metav1.Condition{{
					Type:               constants.ConditionTypePodCliqueScheduled,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
				}}},
			},
			want: false,
		},
		{
			name: "durable condition false",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{Conditions: []metav1.Condition{{
					Type:   constants.ConditionTypeHealthyStateObserved,
					Status: metav1.ConditionFalse,
				}}},
			},
			want: false,
		},
		{
			name: "durable condition true",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{Conditions: []metav1.Condition{{
					Type:   constants.ConditionTypeHealthyStateObserved,
					Status: metav1.ConditionTrue,
				}}},
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, WasPCLQEverScheduled(tc.pclq))
		})
	}
}

// TestWasPCSGEverHealthy covers the PCSG durable-health signal.
func TestWasPCSGEverHealthy(t *testing.T) {
	tests := []struct {
		name string
		pcsg *grovecorev1alpha1.PodCliqueScalingGroup
		want bool
	}{
		{
			name: "no durable condition",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{},
			want: false,
		},
		{
			name: "non-breached condition alone does not count",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{Conditions: []metav1.Condition{{
					Type:               constants.ConditionTypeMinAvailableBreached,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.Now(),
				}}},
			},
			want: false,
		},
		{
			name: "durable condition false",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{Conditions: []metav1.Condition{{
					Type:   constants.ConditionTypeHealthyStateObserved,
					Status: metav1.ConditionFalse,
				}}},
			},
			want: false,
		},
		{
			name: "durable condition true",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{Conditions: []metav1.Condition{{
					Type:   constants.ConditionTypeHealthyStateObserved,
					Status: metav1.ConditionTrue,
				}}},
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, WasPCSGEverHealthy(tc.pcsg))
		})
	}
}

// TestGetMinAvailableBreachedPCLQInfoFiltersNeverScheduled pins the action-gate: PCLQs that
// have MinAvailableBreached=True but were never PodCliqueScheduled=True are excluded from the
// candidate list — gang-termination must not fire on them.
func TestGetMinAvailableBreachedPCLQInfoFiltersNeverScheduled(t *testing.T) {
	pastTransition := metav1.NewTime(time.Now().Add(-time.Hour))

	pclqBreachedNeverScheduled := grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: "never-scheduled"},
		Status: grovecorev1alpha1.PodCliqueStatus{Conditions: []metav1.Condition{
			{
				Type:               constants.ConditionTypePodCliqueScheduled,
				Status:             metav1.ConditionFalse,
				LastTransitionTime: pastTransition,
			},
			{
				Type:               constants.ConditionTypeMinAvailableBreached,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: pastTransition,
			},
		}},
	}
	pclqBreachedAfterHealthy := grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: "regressed"},
		Status: grovecorev1alpha1.PodCliqueStatus{Conditions: []metav1.Condition{
			{
				Type:               constants.ConditionTypePodCliqueScheduled,
				Status:             metav1.ConditionFalse,
				LastTransitionTime: pastTransition,
			},
			{
				Type:               constants.ConditionTypeMinAvailableBreached,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: pastTransition,
			},
			{
				Type:   constants.ConditionTypeHealthyStateObserved,
				Status: metav1.ConditionTrue,
			},
		}},
	}

	pclqs := []grovecorev1alpha1.PodClique{pclqBreachedNeverScheduled, pclqBreachedAfterHealthy}
	names, _ := GetMinAvailableBreachedPCLQInfo(pclqs, time.Hour, time.Now())
	assert.Equal(t, []string{"regressed"}, names, "never-scheduled PCLQ must be filtered out")
}

func TestGroupPCLQsByPCSReplicaIndex(t *testing.T) {
	const (
		pcsName   = "pcs"
		namespace = "default"
		pcsUID    = types.UID("uid")
	)
	tests := []struct {
		name          string
		pclqs         []grovecorev1alpha1.PodClique
		expectErr     bool
		expectedIndex map[int]int // replica index -> number of PodCliques in that group
	}{
		{
			name: "groups PodCliques by replica index",
			pclqs: []grovecorev1alpha1.PodClique{
				*testutils.NewPodCliqueBuilder(pcsName, pcsUID, "clq-a", namespace, 0).Build(),
				*testutils.NewPodCliqueBuilder(pcsName, pcsUID, "clq-b", namespace, 0).Build(),
				*testutils.NewPodCliqueBuilder(pcsName, pcsUID, "clq-a", namespace, 1).Build(),
			},
			expectedIndex: map[int]int{0: 2, 1: 1},
		},
		{
			name: "non-integer replica-index label is an error",
			pclqs: []grovecorev1alpha1.PodClique{
				*testutils.NewPodCliqueBuilder(pcsName, pcsUID, "clq-a", namespace, 0).
					WithLabels(map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "abc"}).Build(),
			},
			expectErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := GroupPCLQsByPCSReplicaIndex(tt.pclqs)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, actual, len(tt.expectedIndex))
			for idx, count := range tt.expectedIndex {
				assert.Len(t, actual[idx], count)
			}
		})
	}
}
