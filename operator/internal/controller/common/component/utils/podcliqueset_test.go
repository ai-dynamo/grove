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

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestGetExpectedPCSGFQNsForPCS tests the GetExpectedPCSGFQNsForPCS function
func TestGetExpectedPCSGFQNsForPCS(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pcs is the PodCliqueSet
		pcs *grovecorev1alpha1.PodCliqueSet
		// expected are the expected PCSG FQNs
		expected []string
	}{
		{
			// Tests with one replica and one scaling group
			name: "single_replica_single_scaling_group",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:        "sg1",
								CliqueNames: []string{"clique1", "clique2"},
							},
						},
					},
				},
			},
			expected: []string{"test-pcs-0-sg1"},
		},
		{
			// Tests with multiple replicas and scaling groups
			name: "multiple_replicas_multiple_scaling_groups",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 2,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:        "sg1",
								CliqueNames: []string{"clique1"},
							},
							{
								Name:        "sg2",
								CliqueNames: []string{"clique2"},
							},
						},
					},
				},
			},
			expected: []string{"test-pcs-0-sg1", "test-pcs-0-sg2", "test-pcs-1-sg1", "test-pcs-1-sg2"},
		},
		{
			// Tests with zero replicas
			name: "zero_replicas",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 0,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:        "sg1",
								CliqueNames: []string{"clique1"},
							},
						},
					},
				},
			},
			expected: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GetExpectedPCSGFQNsForPCS(tc.pcs)
			// Sort both slices to ensure order-independent comparison
			assert.ElementsMatch(t, tc.expected, result)
		})
	}
}

// TestGetPodCliqueFQNsForPCSNotInPCSG tests the GetPodCliqueFQNsForPCSNotInPCSG function
func TestGetPodCliqueFQNsForPCSNotInPCSG(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pcs is the PodCliqueSet
		pcs *grovecorev1alpha1.PodCliqueSet
		// expected are the expected PodClique FQNs
		expected []string
	}{
		{
			// Tests with standalone cliques only
			name: "standalone_cliques_only",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 2,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "standalone1"},
							{Name: "standalone2"},
						},
					},
				},
			},
			expected: []string{
				"test-pcs-0-standalone1",
				"test-pcs-0-standalone2",
				"test-pcs-1-standalone1",
				"test-pcs-1-standalone2",
			},
		},
		{
			// Tests with mixed standalone and scaling group cliques
			name: "mixed_standalone_and_scaling_group",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "standalone1"},
							{Name: "in-sg1"},
							{Name: "standalone2"},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:        "sg1",
								CliqueNames: []string{"in-sg1"},
							},
						},
					},
				},
			},
			expected: []string{
				"test-pcs-0-standalone1",
				"test-pcs-0-standalone2",
			},
		},
		{
			// Tests with all cliques in scaling groups
			name: "all_cliques_in_scaling_groups",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "clique1"},
							{Name: "clique2"},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:        "sg1",
								CliqueNames: []string{"clique1", "clique2"},
							},
						},
					},
				},
			},
			expected: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GetPodCliqueFQNsForPCSNotInPCSG(tc.pcs)
			assert.ElementsMatch(t, tc.expected, result)
		})
	}
}

// TestGetPodCliqueSetName tests the GetPodCliqueSetName function
func TestGetPodCliqueSetName(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// objectMeta is the object metadata
		objectMeta metav1.ObjectMeta
		// expected is the expected PCS name
		expected string
	}{
		{
			// Tests extracting PCS name from labels
			name: "gets_pcs_name_from_label",
			objectMeta: metav1.ObjectMeta{
				Name: "some-object",
				Labels: map[string]string{
					common.LabelPartOfKey: "my-pcs",
				},
			},
			expected: "my-pcs",
		},
		{
			// Tests when label is missing
			name: "missing_label",
			objectMeta: metav1.ObjectMeta{
				Name:   "some-object",
				Labels: map[string]string{},
			},
			expected: "",
		},
		{
			// Tests when labels are nil
			name: "nil_labels",
			objectMeta: metav1.ObjectMeta{
				Name: "some-object",
			},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GetPodCliqueSetName(tc.objectMeta)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestIsAutoUpdateStrategy tests the IsAutoUpdateStrategy function.
func TestIsAutoUpdateStrategy(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected bool
	}{
		{
			name:     "nil_pcs",
			pcs:      nil,
			expected: false,
		},
		{
			name: "nil_update_strategy_defaults_to_auto",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{},
			},
			expected: true,
		},
		{
			name: "rolling_recreate_is_auto",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy},
				},
			},
			expected: true,
		},
		{
			name: "on_delete_is_not_auto",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, IsAutoUpdateStrategy(tc.pcs))
		})
	}
}

func TestIsOnDeleteStrategy(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected bool
	}{
		{
			name:     "nil_pcs",
			pcs:      nil,
			expected: false,
		},
		{
			name: "nil_update_strategy_is_not_on_delete",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{},
			},
			expected: false,
		},
		{
			name: "on_delete_strategy",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy},
				},
			},
			expected: true,
		},
		{
			name: "rolling_recreate_is_not_on_delete",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy},
				},
			},
			expected: false,
		},
		{
			name: "coherent_is_not_on_delete",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, IsOnDeleteStrategy(tc.pcs))
		})
	}
}

func TestIsCoherentStrategy(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected bool
	}{
		{
			name:     "nil_pcs",
			pcs:      nil,
			expected: false,
		},
		{
			name: "nil_update_strategy_is_not_coherent",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{},
			},
			expected: false,
		},
		{
			name: "rolling_recreate_is_not_coherent",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy},
				},
			},
			expected: false,
		},
		{
			name: "coherent_is_coherent",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
				},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, IsCoherentStrategy(tc.pcs))
		})
	}
}

func TestIsCoherentUpdateInProgress(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected bool
	}{
		{
			name:     "nil_pcs",
			pcs:      nil,
			expected: false,
		},
		{
			name: "coherent_strategy_no_progress",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
				},
			},
			expected: false,
		},
		{
			name: "coherent_strategy_update_in_progress",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
				},
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt: metav1.Now(),
					},
				},
			},
			expected: true,
		},
		{
			name: "coherent_strategy_update_ended",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
				},
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   ptr.To(metav1.Now()),
					},
				},
			},
			expected: false,
		},
		{
			name: "rolling_recreate_with_update_progress_is_not_coherent_in_progress",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy},
				},
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt: metav1.Now(),
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, IsCoherentUpdateInProgress(tc.pcs))
		})
	}
}

func TestIsRollingRecreateUpdateInProgress(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected bool
	}{
		{
			name: "nil_strategy_with_update_in_progress",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt: metav1.Now(),
					},
				},
			},
			expected: true,
		},
		{
			name: "rolling_recreate_strategy_update_in_progress",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy},
				},
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt: metav1.Now(),
					},
				},
			},
			expected: true,
		},
		{
			name: "rolling_recreate_strategy_update_ended",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy},
				},
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   ptr.To(metav1.Now()),
					},
				},
			},
			expected: false,
		},
		{
			name: "coherent_strategy_is_not_rolling_recreate",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
				},
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt: metav1.Now(),
					},
				},
			},
			expected: false,
		},
		{
			name: "no_update_progress",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: nil,
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, IsRollingRecreateUpdateInProgress(tc.pcs))
		})
	}
}

func TestCountStandalonePCLQs(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected int
	}{
		{
			name: "all_standalone",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "worker"},
							{Name: "router"},
						},
					},
				},
			},
			expected: 2,
		},
		{
			name: "mixed_standalone_and_pcsg",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "router"},
							{Name: "decode-leader"},
							{Name: "decode-worker"},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "sg", CliqueNames: []string{"decode-leader", "decode-worker"}},
						},
					},
				},
			},
			expected: 1,
		},
		{
			name: "all_in_pcsg",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "decode-leader"},
							{Name: "decode-worker"},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "sg", CliqueNames: []string{"decode-leader", "decode-worker"}},
						},
					},
				},
			},
			expected: 0,
		},
		{
			name: "no_cliques",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{},
				},
			},
			expected: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, CountStandalonePCLQs(tc.pcs))
		})
	}
}

func TestGetPCSGOwnedCliqueNames(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected sets.Set[string]
	}{
		{
			name: "no PCSG configs returns empty set",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "worker"},
						},
					},
				},
			},
			expected: sets.New[string](),
		},
		{
			name: "single PCSG with two member cliques",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "router"},
							{Name: "decode-leader"},
							{Name: "decode-worker"},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "decode", CliqueNames: []string{"decode-leader", "decode-worker"}},
						},
					},
				},
			},
			expected: sets.New("decode-leader", "decode-worker"),
		},
		{
			name: "multiple PCSGs each contribute their member cliques",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "prefill", CliqueNames: []string{"pleader", "pworker"}},
							{Name: "decode", CliqueNames: []string{"dleader", "dworker"}},
						},
					},
				},
			},
			expected: sets.New("pleader", "pworker", "dleader", "dworker"),
		},
		{
			name: "PCSG with empty CliqueNames contributes nothing",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "empty", CliqueNames: nil},
						},
					},
				},
			},
			expected: sets.New[string](),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, GetPCSGOwnedCliqueNames(tc.pcs))
		})
	}
}

// TestGetPodCliqueSet tests the GetPodCliqueSet function
func TestGetPodCliqueSet(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// objectMeta is the metadata of the object requesting the PCS
		objectMeta metav1.ObjectMeta
		// existingPCS is the existing PodCliqueSet
		existingPCS *grovecorev1alpha1.PodCliqueSet
		// expectedPCSName is the expected PCS name
		expectedPCSName string
		// expectError indicates if an error is expected
		expectError bool
	}{
		{
			// Tests successful retrieval of PodCliqueSet
			name: "successful_retrieval",
			objectMeta: metav1.ObjectMeta{
				Name:      "test-pclq",
				Namespace: "default",
				Labels: map[string]string{
					common.LabelPartOfKey: "test-pcs",
				},
			},
			existingPCS: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
			},
			expectedPCSName: "test-pcs",
			expectError:     false,
		},
		{
			// Tests when PodCliqueSet doesn't exist
			name: "pcs_not_found",
			objectMeta: metav1.ObjectMeta{
				Name:      "test-pclq",
				Namespace: "default",
				Labels: map[string]string{
					common.LabelPartOfKey: "test-pcs",
				},
			},
			existingPCS:     nil,
			expectedPCSName: "",
			expectError:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup scheme
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			// Build runtime objects
			runtimeObjs := []runtime.Object{}
			if tc.existingPCS != nil {
				runtimeObjs = append(runtimeObjs, tc.existingPCS)
			}

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(runtimeObjs...).
				Build()

			// Call function
			ctx := context.Background()
			pcs, err := GetPodCliqueSet(ctx, fakeClient, tc.objectMeta)

			// Verify results
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedPCSName, pcs.Name)
			}
		})
	}
}

// TestGetExpectedPCLQNamesGroupByOwner tests the GetExpectedPCLQNamesGroupByOwner function
func TestGetExpectedPCLQNamesGroupByOwner(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pcs is the PodCliqueSet
		pcs *grovecorev1alpha1.PodCliqueSet
		// expectedPCLQNamesForPCS are expected clique names owned by PCS
		expectedPCLQNamesForPCS []string
		// expectedPCLQNamesForPCSG are expected clique names owned by PCSG
		expectedPCLQNamesForPCSG []string
	}{
		{
			// Tests with mixed ownership
			name: "mixed_ownership",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "standalone1"},
							{Name: "in-sg1"},
							{Name: "standalone2"},
							{Name: "in-sg2"},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:        "sg1",
								CliqueNames: []string{"in-sg1"},
							},
							{
								Name:        "sg2",
								CliqueNames: []string{"in-sg2"},
							},
						},
					},
				},
			},
			expectedPCLQNamesForPCS:  []string{"standalone1", "standalone2"},
			expectedPCLQNamesForPCSG: []string{"in-sg1", "in-sg2"},
		},
		{
			// Tests with all cliques owned by PCS
			name: "all_owned_by_pcs",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "clique1"},
							{Name: "clique2"},
						},
					},
				},
			},
			expectedPCLQNamesForPCS:  []string{"clique1", "clique2"},
			expectedPCLQNamesForPCSG: []string{},
		},
		{
			// Tests with all cliques owned by PCSG
			name: "all_owned_by_pcsg",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "clique1"},
							{Name: "clique2"},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:        "sg1",
								CliqueNames: []string{"clique1", "clique2"},
							},
						},
					},
				},
			},
			expectedPCLQNamesForPCS:  []string{},
			expectedPCLQNamesForPCSG: []string{"clique1", "clique2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcsNames, pcsgNames := GetExpectedPCLQNamesGroupByOwner(tc.pcs)
			assert.ElementsMatch(t, tc.expectedPCLQNamesForPCS, pcsNames)
			assert.ElementsMatch(t, tc.expectedPCLQNamesForPCSG, pcsgNames)
		})
	}
}

func TestGetStandalonePCLQReplicasFromSpec(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(2))}},
					{Name: "prefill-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "prefill", Replicas: ptr.To(int32(4)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"prefill-worker"}},
				},
			},
		},
	}

	result := GetStandalonePCLQReplicasFromPCSTemplateSpec(pcs)
	assert.Equal(t, map[string]int32{"frontend": 5}, result)
}

func TestGetPCSGReplicasFromSpec(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "prefill", Replicas: ptr.To(int32(4)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"prefill-worker"}},
					{Name: "decode", Replicas: ptr.To(int32(3)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"decode-worker"}},
				},
			},
		},
	}

	result := GetPCSGReplicasFromPCSTemplateSpec(pcs)
	assert.Equal(t, map[string]int32{"prefill": 4, "decode": 3}, result)
}

func TestGetPCSReplicaIndexFromObjectMeta(t *testing.T) {
	tests := []struct {
		name    string
		objMeta metav1.ObjectMeta
		want    int
		wantErr bool
	}{
		{
			name:    "valid replica index label",
			objMeta: metav1.ObjectMeta{Name: "pcs-2-frontend", Labels: map[string]string{common.LabelPodCliqueSetReplicaIndex: "2"}},
			want:    2,
		},
		{
			name:    "zero replica index",
			objMeta: metav1.ObjectMeta{Name: "pcs-0-frontend", Labels: map[string]string{common.LabelPodCliqueSetReplicaIndex: "0"}},
			want:    0,
		},
		{
			name:    "missing label is an error",
			objMeta: metav1.ObjectMeta{Name: "pcs-0-frontend", Labels: map[string]string{}},
			wantErr: true,
		},
		{
			name:    "nil labels is an error",
			objMeta: metav1.ObjectMeta{Name: "pcs-0-frontend"},
			wantErr: true,
		},
		{
			name:    "non-numeric label is an error",
			objMeta: metav1.ObjectMeta{Name: "pcs-0-frontend", Labels: map[string]string{common.LabelPodCliqueSetReplicaIndex: "abc"}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GetPCSReplicaIndexFromObjectMeta(tc.objMeta)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestIsPCSReplicaInCurrentlyUpdating(t *testing.T) {
	ended := ptr.To(metav1.Now())
	updating := func(replicaIndices ...int32) *grovecorev1alpha1.PodCliqueSetUpdateProgress {
		cu := make([]grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress, 0, len(replicaIndices))
		for _, idx := range replicaIndices {
			cu = append(cu, grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{ReplicaIndex: idx})
		}
		return &grovecorev1alpha1.PodCliqueSetUpdateProgress{CurrentlyUpdating: cu}
	}
	tests := []struct {
		name           string
		updateProgress *grovecorev1alpha1.PodCliqueSetUpdateProgress
		replicaIndex   int
		want           bool
	}{
		{name: "nil UpdateProgress", updateProgress: nil, replicaIndex: 0, want: false},
		{name: "replica in CurrentlyUpdating", updateProgress: updating(0), replicaIndex: 0, want: true},
		{name: "replica not in CurrentlyUpdating", updateProgress: updating(1), replicaIndex: 0, want: false},
		{name: "one of several updating replicas", updateProgress: updating(1, 3), replicaIndex: 3, want: true},
		{
			name: "replica present but UpdateEndedAt set",
			updateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, UpdateEndedAt: ended}},
			},
			replicaIndex: 0,
			want:         false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{Status: grovecorev1alpha1.PodCliqueSetStatus{UpdateProgress: tc.updateProgress}}
			assert.Equal(t, tc.want, IsPCSReplicaInCurrentlyUpdating(pcs, tc.replicaIndex))
		})
	}
}

func TestIsPCSReplicaUnderCoherentUpdate(t *testing.T) {
	coherent := func() *grovecorev1alpha1.PodCliqueSetUpdateStrategy {
		return &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}
	}
	inProgressUpdating0 := &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		UpdateStartedAt:   metav1.Now(),
		CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
	}
	tests := []struct {
		name         string
		strategy     *grovecorev1alpha1.PodCliqueSetUpdateStrategy
		progress     *grovecorev1alpha1.PodCliqueSetUpdateProgress
		replicaIndex int
		want         bool
	}{
		{name: "coherent update in progress on this replica", strategy: coherent(), progress: inProgressUpdating0, replicaIndex: 0, want: true},
		{name: "coherent update in progress on different replica", strategy: coherent(), progress: inProgressUpdating0, replicaIndex: 1, want: false},
		{name: "coherent strategy but no update in progress", strategy: coherent(), progress: nil, want: false},
		{
			name:     "rolling recreate strategy is never coherent",
			strategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy},
			progress: inProgressUpdating0,
			want:     false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				Spec:   grovecorev1alpha1.PodCliqueSetSpec{UpdateStrategy: tc.strategy},
				Status: grovecorev1alpha1.PodCliqueSetStatus{UpdateProgress: tc.progress},
			}
			assert.Equal(t, tc.want, IsPCSReplicaUnderCoherentUpdate(pcs, tc.replicaIndex))
		})
	}
}
