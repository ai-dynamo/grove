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

package component

import (
	"context"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
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

// TestIsRollingUpdateStrategy tests the IsRollingUpdateStrategy function.
func TestIsRollingUpdateStrategy(t *testing.T) {
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
			assert.Equal(t, tc.expected, IsRollingUpdateStrategy(tc.pcs))
		})
	}
}

// TestGetPodCliqueSet tests the GetPodCliqueSet function
func TestIsCoherentStrategy(t *testing.T) {
	withStrategy := func(strategyType grovecorev1alpha1.UpdateStrategyType) *grovecorev1alpha1.PodCliqueSet {
		pcs := &grovecorev1alpha1.PodCliqueSet{}
		pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: strategyType}
		return pcs
	}
	testCases := []struct {
		description string
		pcs         *grovecorev1alpha1.PodCliqueSet
		want        bool
	}{
		{"nil PodCliqueSet is not Coherent", nil, false},
		{"nil UpdateStrategy is not Coherent", &grovecorev1alpha1.PodCliqueSet{}, false},
		{"RollingRecreate is not Coherent", withStrategy(grovecorev1alpha1.RollingRecreateStrategy), false},
		{"OnDelete is not Coherent", withStrategy(grovecorev1alpha1.OnDeleteStrategy), false},
		{"Coherent is Coherent", withStrategy(grovecorev1alpha1.CoherentStrategy), true},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, IsCoherentStrategy(tc.pcs))
		})
	}
}

func TestIsCoherentUpdateInProgress(t *testing.T) {
	coherent := &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}
	endedAt := metav1.Now()
	testCases := []struct {
		description string
		strategy    *grovecorev1alpha1.PodCliqueSetUpdateStrategy
		progress    *grovecorev1alpha1.PodCliqueSetUpdateProgress
		want        bool
	}{
		{"not Coherent", &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}, &grovecorev1alpha1.PodCliqueSetUpdateProgress{}, false},
		{"Coherent with no UpdateProgress", coherent, nil, false},
		{"Coherent with an in-flight update", coherent, &grovecorev1alpha1.PodCliqueSetUpdateProgress{}, true},
		{"Coherent with an ended update", coherent, &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateEndedAt: &endedAt}, false},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{}
			pcs.Spec.UpdateStrategy = tc.strategy
			pcs.Status.UpdateProgress = tc.progress
			assert.Equal(t, tc.want, IsCoherentUpdateInProgress(pcs))
		})
	}
}

func TestIsRollingUpdateInProgress(t *testing.T) {
	endedAt := metav1.Now()
	testCases := []struct {
		description string
		strategy    *grovecorev1alpha1.PodCliqueSetUpdateStrategy
		progress    *grovecorev1alpha1.PodCliqueSetUpdateProgress
		want        bool
	}{
		{"OnDelete is not a rolling update", &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy}, &grovecorev1alpha1.PodCliqueSetUpdateProgress{}, false},
		{"rolling update strategy with no UpdateProgress", &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}, nil, false},
		{"rolling update strategy with an in-flight update", &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}, &grovecorev1alpha1.PodCliqueSetUpdateProgress{}, true},
		{"a nil UpdateStrategy defaults to a rolling update strategy", nil, &grovecorev1alpha1.PodCliqueSetUpdateProgress{}, true},
		{"rolling update strategy with an ended update", &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}, &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateEndedAt: &endedAt}, false},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{}
			pcs.Spec.UpdateStrategy = tc.strategy
			pcs.Status.UpdateProgress = tc.progress
			assert.Equal(t, tc.want, IsRollingUpdateInProgress(pcs))
		})
	}
}

func TestIsRollingRecreateUpdateInProgress(t *testing.T) {
	testCases := []struct {
		description string
		strategy    *grovecorev1alpha1.PodCliqueSetUpdateStrategy
		progress    *grovecorev1alpha1.PodCliqueSetUpdateProgress
		want        bool
	}{
		{"RollingRecreate with an in-flight update", &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}, &grovecorev1alpha1.PodCliqueSetUpdateProgress{}, true},
		{"a nil UpdateStrategy defaults to RollingRecreate", nil, &grovecorev1alpha1.PodCliqueSetUpdateProgress{}, true},
		{"Coherent with an in-flight update is not RollingRecreate", &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}, &grovecorev1alpha1.PodCliqueSetUpdateProgress{}, false},
		{"OnDelete with an in-flight update is not RollingRecreate", &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.OnDeleteStrategy}, &grovecorev1alpha1.PodCliqueSetUpdateProgress{}, false},
		{"RollingRecreate with no update in progress", &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}, nil, false},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{}
			pcs.Spec.UpdateStrategy = tc.strategy
			pcs.Status.UpdateProgress = tc.progress
			assert.Equal(t, tc.want, IsRollingRecreateUpdateInProgress(pcs))
		})
	}
}

func TestIsPCSReplicaUnderCoherentUpdate(t *testing.T) {
	coherent := &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}
	endedAt := metav1.Now()
	currentlyUpdating := func(replicaIndex int32, ended *metav1.Time) []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress {
		return []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: replicaIndex, UpdateEndedAt: ended}}
	}
	testCases := []struct {
		description     string
		strategy        *grovecorev1alpha1.PodCliqueSetUpdateStrategy
		progress        *grovecorev1alpha1.PodCliqueSetUpdateProgress
		pcsReplicaIndex int
		want            bool
	}{
		{"not a coherent update", &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}, &grovecorev1alpha1.PodCliqueSetUpdateProgress{CurrentlyUpdating: currentlyUpdating(0, nil)}, 0, false},
		{"coherent update but replica not selected", coherent, &grovecorev1alpha1.PodCliqueSetUpdateProgress{CurrentlyUpdating: currentlyUpdating(1, nil)}, 0, false},
		{"coherent update with the replica selected and open", coherent, &grovecorev1alpha1.PodCliqueSetUpdateProgress{CurrentlyUpdating: currentlyUpdating(0, nil)}, 0, true},
		{"coherent update with the replica selected but closed out", coherent, &grovecorev1alpha1.PodCliqueSetUpdateProgress{CurrentlyUpdating: currentlyUpdating(0, &endedAt)}, 0, false},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{}
			pcs.Spec.UpdateStrategy = tc.strategy
			pcs.Status.UpdateProgress = tc.progress
			assert.Equal(t, tc.want, IsPCSReplicaUnderCoherentUpdate(pcs, tc.pcsReplicaIndex))
		})
	}
}

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
			assert.Equal(t, sets.New(tc.expectedPCLQNamesForPCS...), pcsNames)
			assert.Equal(t, sets.New(tc.expectedPCLQNamesForPCSG...), pcsgNames)
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
			name: "no standalone clique, only a scaling group",
			pcs: testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithScalingGroupConfig("sg", []string{"c"}, 4, 2).Build(),
			expected: 0,
		},
		{
			name: "standalone cliques only",
			pcs: testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithStandaloneCliqueReplicas("clq-b", 1).Build(),
			expected: 2,
		},
		{
			name: "standalone clique and scaling group",
			pcs: testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithScalingGroupConfig("sg", []string{"c"}, 4, 2).Build(),
			expected: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := CountStandalonePCLQs(tt.pcs)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestGetStandalonePCLQReplicasFromPCSTemplateSpec(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected map[string]int32
	}{
		{
			name: "no standalone clique yields empty map",
			pcs: testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithScalingGroupConfig("sg", []string{"c"}, 4, 2).Build(),
			expected: map[string]int32{},
		},
		{
			name: "standalone cliques only",
			pcs: testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithStandaloneCliqueReplicas("clq-b", 1).Build(),
			expected: map[string]int32{"clq-a": 3, "clq-b": 1},
		},
		{
			name: "excludes scaling group member cliques",
			pcs: testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithScalingGroupConfig("sg", []string{"c"}, 4, 2).Build(),
			expected: map[string]int32{"clq-a": 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := GetStandalonePCLQReplicasFromPCSTemplateSpec(tt.pcs)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestGetPCSGMinAvailableFromPCSTemplateSpec(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected map[string]int32
	}{
		{
			name:     "no scaling group yields empty map",
			pcs:      testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").WithStandaloneCliqueReplicas("clq-a", 3).Build(),
			expected: map[string]int32{},
		},
		{
			name: "one entry per scaling group",
			pcs: testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithScalingGroupConfig("sg-a", []string{"c1"}, 4, 2).
				WithScalingGroupConfig("sg-b", []string{"c2"}, 3, 1).Build(),
			expected: map[string]int32{"sg-a": 2, "sg-b": 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := GetPCSGMinAvailableFromPCSTemplateSpec(tt.pcs)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestGetPCSGReplicasFromPCSTemplateSpec(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected map[string]int32
	}{
		{
			name:     "no scaling group yields empty map",
			pcs:      testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").WithStandaloneCliqueReplicas("clq-a", 3).Build(),
			expected: map[string]int32{},
		},
		{
			name: "one entry per scaling group",
			pcs: testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithScalingGroupConfig("sg-a", []string{"c1"}, 4, 2).
				WithScalingGroupConfig("sg-b", []string{"c2"}, 3, 1).Build(),
			expected: map[string]int32{"sg-a": 4, "sg-b": 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := GetPCSGReplicasFromPCSTemplateSpec(tt.pcs)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

// TestIsStandalonePCLQ verifies a PodClique is standalone unless it is a member of a
// PodCliqueScalingGroup.
func TestIsStandalonePCLQ(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithStandaloneClique("standalone-clq").
		WithScalingGroupConfig("sg", []string{"member-clq"}, 4, 2).
		Build()
	tests := []struct {
		name       string
		cliqueName string
		expected   bool
	}{
		{"clique not in any scaling group is standalone", "standalone-clq", true},
		{"clique that is a scaling group member is not standalone", "member-clq", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := IsStandalonePCLQ(pcs, tc.cliqueName)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestGetPodCliqueSetReplicaIndexFromPodCliqueFQN(t *testing.T) {
	testCases := []struct {
		description   string
		pcsName       string
		pclqFQNName   string
		expectedIndex int
		expectedErr   bool
	}{
		{
			description:   "PodClique and PCS name without hyphen",
			pcsName:       "inference",
			pclqFQNName:   "inference-0-prefill",
			expectedIndex: 0,
			expectedErr:   false,
		},
		{
			description:   "PodClique name with hyphen and PCS name without hyphen",
			pcsName:       "inference",
			pclqFQNName:   "inference-1-prefill-leader",
			expectedIndex: 1,
			expectedErr:   false,
		},
		{
			description:   "PodClique name with hyphen and PCS name with hyphen",
			pcsName:       "pcs-inference",
			pclqFQNName:   "pcs-inference-2-prefill-worker",
			expectedIndex: 2,
			expectedErr:   false,
		},
		{
			description: "Malformed PodClique FQN name",
			pcsName:     "inference",
			pclqFQNName: "inference-prefill",
			expectedErr: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			index, err := GetPodCliqueSetReplicaIndexFromPodCliqueFQN(tc.pcsName, tc.pclqFQNName)
			if tc.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedIndex, index)
			}
		})
	}
}
