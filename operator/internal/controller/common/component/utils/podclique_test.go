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

package utils

import (
	"context"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/hash"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
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

// TestIsPCLQUpdateInProgress tests the IsPCLQUpdateInProgress function
func TestIsPCLQUpdateInProgress(t *testing.T) {
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
					RollingUpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			// Tests when rolling update is in progress
			name: "update_in_progress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueRollingUpdateProgress{
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
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueRollingUpdateProgress{
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
			result := IsPCLQUpdateInProgress(tc.pclq)
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
					RollingUpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			// Tests when rolling update is in progress
			name: "update_in_progress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueRollingUpdateProgress{
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
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueRollingUpdateProgress{
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

// TestGetPCLQsByOwnerReplicaIndex tests the GetPCLQsByOwnerReplicaIndex function
func TestGetPCLQsByOwnerReplicaIndex(t *testing.T) {
	tests := []struct {
		name           string
		ownerKind      string
		ownerObjectKey client.ObjectKey
		selectorLabels map[string]string
		existingPCLQs  []grovecorev1alpha1.PodClique
		expected       map[string][]string // replica index -> pod clique names
		expectError    bool
	}{
		{
			name:      "groups_by_replica_index",
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
						Name:      "test-pclq-0",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey:                "test-pcs",
							apicommon.LabelPodCliqueSetReplicaIndex: "0",
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
						Name:      "test-pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey:                "test-pcs",
							apicommon.LabelPodCliqueSetReplicaIndex: "0",
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
							apicommon.LabelPartOfKey:                "test-pcs",
							apicommon.LabelPodCliqueSetReplicaIndex: "1",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "test-pcs",
							},
						},
					},
				},
			},
			expected: map[string][]string{
				"0": {"test-pclq-0", "test-pclq-1"},
				"1": {"test-pclq-2"},
			},
			expectError: false,
		},
		{
			name:      "empty_result",
			ownerKind: "PodCliqueSet",
			ownerObjectKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			selectorLabels: map[string]string{
				apicommon.LabelPartOfKey: "test-pcs",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{},
			expected:      map[string][]string{},
			expectError:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			runtimeObjs := []runtime.Object{}
			for i := range tc.existingPCLQs {
				runtimeObjs = append(runtimeObjs, &tc.existingPCLQs[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(runtimeObjs...).
				Build()

			ctx := context.Background()
			result, err := GetPCLQsByOwnerReplicaIndex(ctx, fakeClient, tc.ownerKind, tc.ownerObjectKey, tc.selectorLabels)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(tc.expected), len(result))
				for replicaIndex, expectedNames := range tc.expected {
					pclqs, exists := result[replicaIndex]
					assert.True(t, exists, "Expected replica index %s to exist", replicaIndex)
					assert.Equal(t, len(expectedNames), len(pclqs))
					for i, pclq := range pclqs {
						assert.Equal(t, expectedNames[i], pclq.Name)
					}
				}
			}
		})
	}
}

// TestGetPCLQsMatchingLabels tests the GetPCLQsMatchingLabels function
func TestGetPCLQsMatchingLabels(t *testing.T) {
	tests := []struct {
		name           string
		namespace      string
		selectorLabels map[string]string
		existingPCLQs  []grovecorev1alpha1.PodClique
		expectedNames  []string
		expectError    bool
	}{
		{
			name:      "finds_matching_podcliques",
			namespace: "default",
			selectorLabels: map[string]string{
				apicommon.LabelPartOfKey: "test-pcs",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pclq-2",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pclq-3",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "other-pcs",
						},
					},
				},
			},
			expectedNames: []string{"pclq-1", "pclq-2"},
			expectError:   false,
		},
		{
			name:      "no_matching_podcliques",
			namespace: "default",
			selectorLabels: map[string]string{
				apicommon.LabelPartOfKey: "nonexistent",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
					},
				},
			},
			expectedNames: []string{},
			expectError:   false,
		},
		{
			name:      "filters_by_namespace",
			namespace: "default",
			selectorLabels: map[string]string{
				apicommon.LabelPartOfKey: "test-pcs",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pclq-2",
						Namespace: "other-namespace",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
					},
				},
			},
			expectedNames: []string{"pclq-1"},
			expectError:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			runtimeObjs := []runtime.Object{}
			for i := range tc.existingPCLQs {
				runtimeObjs = append(runtimeObjs, &tc.existingPCLQs[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(runtimeObjs...).
				Build()

			ctx := context.Background()
			result, err := GetPCLQsMatchingLabels(ctx, fakeClient, tc.namespace, tc.selectorLabels)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(tc.expectedNames), len(result))
				for i, pclq := range result {
					assert.Equal(t, tc.expectedNames[i], pclq.Name)
				}
			}
		})
	}
}

// TestGroupPCLQsByPCSGReplicaIndex tests the GroupPCLQsByPCSGReplicaIndex function
func TestGroupPCLQsByPCSGReplicaIndex(t *testing.T) {
	tests := []struct {
		name     string
		pclqs    []grovecorev1alpha1.PodClique
		expected map[string]int // replica index -> count
	}{
		{
			name: "groups_by_pcsg_replica_index",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-1",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-2",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-3",
						Labels: map[string]string{
							apicommon.LabelPodCliqueScalingGroupReplicaIndex: "1",
						},
					},
				},
			},
			expected: map[string]int{
				"0": 2,
				"1": 1,
			},
		},
		{
			name:     "empty_list",
			pclqs:    []grovecorev1alpha1.PodClique{},
			expected: map[string]int{},
		},
		{
			name: "missing_label",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "pclq-1",
						Labels: map[string]string{},
					},
				},
			},
			expected: map[string]int{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GroupPCLQsByPCSGReplicaIndex(tc.pclqs)
			assert.Equal(t, len(tc.expected), len(result))
			for replicaIndex, expectedCount := range tc.expected {
				pclqs, exists := result[replicaIndex]
				assert.True(t, exists, "Expected replica index %s to exist", replicaIndex)
				assert.Equal(t, expectedCount, len(pclqs))
			}
		})
	}
}

// TestGroupPCLQsByPCSReplicaIndex tests the GroupPCLQsByPCSReplicaIndex function
func TestGroupPCLQsByPCSReplicaIndex(t *testing.T) {
	tests := []struct {
		name     string
		pclqs    []grovecorev1alpha1.PodClique
		expected map[string]int // replica index -> count
	}{
		{
			name: "groups_by_pcs_replica_index",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-1",
						Labels: map[string]string{
							apicommon.LabelPodCliqueSetReplicaIndex: "0",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-2",
						Labels: map[string]string{
							apicommon.LabelPodCliqueSetReplicaIndex: "0",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-3",
						Labels: map[string]string{
							apicommon.LabelPodCliqueSetReplicaIndex: "1",
						},
					},
				},
			},
			expected: map[string]int{
				"0": 2,
				"1": 1,
			},
		},
		{
			name:     "empty_list",
			pclqs:    []grovecorev1alpha1.PodClique{},
			expected: map[string]int{},
		},
		{
			name: "missing_label",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "pclq-1",
						Labels: map[string]string{},
					},
				},
			},
			expected: map[string]int{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GroupPCLQsByPCSReplicaIndex(tc.pclqs)
			assert.Equal(t, len(tc.expected), len(result))
			for replicaIndex, expectedCount := range tc.expected {
				pclqs, exists := result[replicaIndex]
				assert.True(t, exists, "Expected replica index %s to exist", replicaIndex)
				assert.Equal(t, expectedCount, len(pclqs))
			}
		})
	}
}

// TestGetMinAvailableBreachedPCLQInfo tests the GetMinAvailableBreachedPCLQInfo function
func TestGetMinAvailableBreachedPCLQInfo(t *testing.T) {
	now := time.Now()
	fiveMinutesAgo := metav1.NewTime(now.Add(-5 * time.Minute))
	tenMinutesAgo := metav1.NewTime(now.Add(-10 * time.Minute))

	tests := []struct {
		name             string
		pclqs            []grovecorev1alpha1.PodClique
		terminationDelay time.Duration
		since            time.Time
		expectedNames    []string
		expectedWaitFor  time.Duration
	}{
		{
			name: "finds_breached_podcliques",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-1",
					},
					Status: grovecorev1alpha1.PodCliqueStatus{
						Conditions: []metav1.Condition{
							{
								Type:               "MinAvailableBreached",
								Status:             metav1.ConditionTrue,
								LastTransitionTime: fiveMinutesAgo,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-2",
					},
					Status: grovecorev1alpha1.PodCliqueStatus{
						Conditions: []metav1.Condition{
							{
								Type:               "MinAvailableBreached",
								Status:             metav1.ConditionTrue,
								LastTransitionTime: tenMinutesAgo,
							},
						},
					},
				},
			},
			terminationDelay: 15 * time.Minute,
			since:            now,
			expectedNames:    []string{"pclq-1", "pclq-2"},
			expectedWaitFor:  5 * time.Minute, // 15 min delay - 10 min elapsed = 5 min remaining
		},
		{
			name: "no_breached_condition",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-1",
					},
					Status: grovecorev1alpha1.PodCliqueStatus{
						Conditions: []metav1.Condition{
							{
								Type:   "MinAvailableBreached",
								Status: metav1.ConditionFalse,
							},
						},
					},
				},
			},
			terminationDelay: 15 * time.Minute,
			since:            now,
			expectedNames:    nil,
			expectedWaitFor:  0,
		},
		{
			name:             "empty_podclique_list",
			pclqs:            []grovecorev1alpha1.PodClique{},
			terminationDelay: 15 * time.Minute,
			since:            now,
			expectedNames:    nil,
			expectedWaitFor:  0,
		},
		{
			name: "no_condition_at_all",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-1",
					},
					Status: grovecorev1alpha1.PodCliqueStatus{
						Conditions: []metav1.Condition{},
					},
				},
			},
			terminationDelay: 15 * time.Minute,
			since:            now,
			expectedNames:    nil,
			expectedWaitFor:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			names, waitFor := GetMinAvailableBreachedPCLQInfo(tc.pclqs, tc.terminationDelay, tc.since)
			assert.Equal(t, tc.expectedNames, names)
			// Allow 1 second tolerance for time calculations
			if tc.expectedWaitFor > 0 {
				assert.InDelta(t, tc.expectedWaitFor, waitFor, float64(time.Second))
			} else {
				assert.Equal(t, tc.expectedWaitFor, waitFor)
			}
		})
	}
}

// TestGetPodCliquesWithParentPCS tests the GetPodCliquesWithParentPCS function
func TestGetPodCliquesWithParentPCS(t *testing.T) {
	tests := []struct {
		name          string
		pcsObjKey     client.ObjectKey
		existingPCLQs []grovecorev1alpha1.PodClique
		expectedNames []string
		expectError   bool
	}{
		{
			name: "finds_podcliques_with_parent_pcs",
			pcsObjKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "test-pcs",
							apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueSetPodClique,
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pclq-2",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "test-pcs",
							apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueSetPodClique,
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pclq-3",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "test-pcs",
							apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
						},
					},
				},
			},
			expectedNames: []string{"pclq-1", "pclq-2"},
			expectError:   false,
		},
		{
			name: "no_matching_podcliques",
			pcsObjKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "other-pcs",
							apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueSetPodClique,
						},
					},
				},
			},
			expectedNames: []string{},
			expectError:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			runtimeObjs := []runtime.Object{}
			for i := range tc.existingPCLQs {
				runtimeObjs = append(runtimeObjs, &tc.existingPCLQs[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(runtimeObjs...).
				Build()

			ctx := context.Background()
			result, err := GetPodCliquesWithParentPCS(ctx, fakeClient, tc.pcsObjKey)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(tc.expectedNames), len(result))
				for i, pclq := range result {
					assert.Equal(t, tc.expectedNames[i], pclq.Name)
				}
			}
		})
	}
}

// TestFindPodCliqueTemplateSpecByName tests the FindPodCliqueTemplateSpecByName function
func TestFindPodCliqueTemplateSpecByName(t *testing.T) {
	tests := []struct {
		name         string
		pcs          *grovecorev1alpha1.PodCliqueSet
		pclqName     string
		expectedName *string
	}{
		{
			name: "finds_matching_template_spec",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "worker",
							},
							{
								Name: "master",
							},
						},
					},
				},
			},
			pclqName:     "worker",
			expectedName: ptr.To("worker"),
		},
		{
			name: "no_matching_template_spec",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "worker",
							},
						},
					},
				},
			},
			pclqName:     "nonexistent",
			expectedName: nil,
		},
		{
			name: "empty_cliques_list",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{},
					},
				},
			},
			pclqName:     "worker",
			expectedName: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := FindPodCliqueTemplateSpecByName(tc.pcs, tc.pclqName)
			if tc.expectedName == nil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
				assert.Equal(t, *tc.expectedName, result.Name)
			}
		})
	}
}

// TestGetExpectedPCLQPodTemplateHash tests the GetExpectedPCLQPodTemplateHash function
func TestGetExpectedPCLQPodTemplateHash(t *testing.T) {
	tests := []struct {
		name           string
		pcs            *grovecorev1alpha1.PodCliqueSet
		pclqObjectMeta metav1.ObjectMeta
		expectError    bool
		errorContains  string
	}{
		{
			name: "successfully_computes_hash",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pcs",
					Generation: 1,
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PriorityClassName: "high-priority",
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "worker",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									RoleName: "worker",
									Replicas: 3,
								},
							},
						},
					},
				},
			},
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0-worker",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPartOfKey:                "test-pcs",
					apicommon.LabelPodCliqueSetReplicaIndex: "0",
					apicommon.LabelPodClique:                "worker",
				},
			},
			expectError: false,
		},
		{
			name: "fails_when_clique_name_not_found",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pcs",
					Generation: 1,
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{},
					},
				},
			},
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "invalid-name",
				Namespace: "default",
				Labels:    map[string]string{},
			},
			expectError:   true,
			errorContains: "failed to find PodClique name from FQN",
		},
		{
			name: "fails_when_template_spec_not_found",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pcs",
					Generation: 1,
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "worker",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									RoleName: "worker",
									Replicas: 3,
								},
							},
						},
					},
				},
			},
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0-master",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPartOfKey:                "test-pcs",
					apicommon.LabelPodCliqueSetReplicaIndex: "0",
					apicommon.LabelPodClique:                "master",
				},
			},
			expectError:   true,
			errorContains: "failed to find matching PodCliqueTemplateSpec",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cache := hash.GetPodTemplateSpecHashCache(ctx)

			hash, err := GetExpectedPCLQPodTemplateHash(cache, tc.pcs, tc.pclqObjectMeta)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, hash)
			}
		})
	}
}
