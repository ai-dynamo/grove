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
