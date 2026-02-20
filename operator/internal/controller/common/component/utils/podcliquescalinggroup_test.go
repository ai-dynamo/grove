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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/hash"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFindScalingGroupConfigForClique(t *testing.T) {
	// Create test scaling group configurations
	scalingGroupConfigs := []grovecorev1alpha1.PodCliqueScalingGroupConfig{
		{
			Name:        "sga",
			CliqueNames: []string{"pca", "pcb"},
		},
		{
			Name:        "sgb",
			CliqueNames: []string{"pcc", "pcd", "pce"},
		},
		{
			Name:        "sgc",
			CliqueNames: []string{"pcf"},
		},
	}

	tests := []struct {
		name               string
		configs            []grovecorev1alpha1.PodCliqueScalingGroupConfig
		cliqueName         string
		expectedFound      bool
		expectedConfigName string
	}{
		{
			name:               "clique found in first scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pca",
			expectedFound:      true,
			expectedConfigName: "sga",
		},
		{
			name:               "clique found in second scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pcd",
			expectedFound:      true,
			expectedConfigName: "sgb",
		},
		{
			name:               "clique found in third scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pcf",
			expectedFound:      true,
			expectedConfigName: "sgc",
		},
		{
			name:               "clique not found in any scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "nonexistent",
			expectedFound:      false,
			expectedConfigName: "",
		},
		{
			name:               "empty clique name",
			configs:            scalingGroupConfigs,
			cliqueName:         "",
			expectedFound:      false,
			expectedConfigName: "",
		},
		{
			name:               "empty configs",
			configs:            []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
			cliqueName:         "anyClique",
			expectedFound:      false,
			expectedConfigName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := FindScalingGroupConfigForClique(tt.configs, tt.cliqueName)
			assert.Equal(t, tt.expectedFound, config != nil)
			if tt.expectedFound {
				assert.Equal(t, tt.expectedConfigName, config.Name)
			} else {
				// When not found, config should be nil
				assert.Nil(t, config)
			}
		})
	}
}

// TestGetPCSGsForPCS tests the GetPCSGsForPCS function
func TestGetPCSGsForPCS(t *testing.T) {
	tests := []struct {
		name          string
		pcsObjKey     client.ObjectKey
		existingPCSGs []grovecorev1alpha1.PodCliqueScalingGroup
		expectedNames []string
		expectError   bool
	}{
		{
			name: "finds_all_pcsgs_for_pcs",
			pcsObjKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			existingPCSGs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "test-pcs",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg2",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "test-pcs",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:    "other-pcs",
						},
					},
				},
			},
			expectedNames: []string{"test-pcs-0-sg1", "test-pcs-0-sg2"},
			expectError:   false,
		},
		{
			name: "no_matching_pcsgs",
			pcsObjKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			existingPCSGs: []grovecorev1alpha1.PodCliqueScalingGroup{},
			expectedNames: []string{},
			expectError:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			runtimeObjs := []runtime.Object{}
			for i := range tc.existingPCSGs {
				runtimeObjs = append(runtimeObjs, &tc.existingPCSGs[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(runtimeObjs...).
				Build()

			ctx := context.Background()
			result, err := GetPCSGsForPCS(ctx, fakeClient, tc.pcsObjKey)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(tc.expectedNames), len(result))
				for i, pcsg := range result {
					assert.Equal(t, tc.expectedNames[i], pcsg.Name)
				}
			}
		})
	}
}

// TestGenerateDependencyNamesForBasePodGang tests the GenerateDependencyNamesForBasePodGang function
func TestGenerateDependencyNamesForBasePodGang(t *testing.T) {
	tests := []struct {
		name             string
		pcs              *grovecorev1alpha1.PodCliqueSet
		pcsReplicaIndex  int
		parentCliqueName string
		expectedDepNames []string
	}{
		{
			name: "clique_in_scaling_group",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "sg1",
								CliqueNames:  []string{"worker"},
								MinAvailable: ptr.To[int32](3),
							},
						},
					},
				},
			},
			pcsReplicaIndex:  0,
			parentCliqueName: "worker",
			expectedDepNames: []string{
				"test-pcs-0-sg1-0-worker",
				"test-pcs-0-sg1-1-worker",
				"test-pcs-0-sg1-2-worker",
			},
		},
		{
			name: "standalone_clique",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "sg1",
								CliqueNames:  []string{"worker"},
								MinAvailable: ptr.To[int32](1),
							},
						},
					},
				},
			},
			pcsReplicaIndex:  1,
			parentCliqueName: "master",
			expectedDepNames: []string{
				"test-pcs-1-master",
			},
		},
		{
			name: "min_available_one",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "sg1",
								CliqueNames:  []string{"worker"},
								MinAvailable: ptr.To[int32](1),
							},
						},
					},
				},
			},
			pcsReplicaIndex:  0,
			parentCliqueName: "worker",
			expectedDepNames: []string{
				"test-pcs-0-sg1-0-worker",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GenerateDependencyNamesForBasePodGang(tc.pcs, tc.pcsReplicaIndex, tc.parentCliqueName)
			assert.Equal(t, tc.expectedDepNames, result)
		})
	}
}

// TestGroupPCSGsByPCSReplicaIndex tests the GroupPCSGsByPCSReplicaIndex function
func TestGroupPCSGsByPCSReplicaIndex(t *testing.T) {
	tests := []struct {
		name     string
		pcsgs    []grovecorev1alpha1.PodCliqueScalingGroup
		expected map[string]int // replica index -> count
	}{
		{
			name: "groups_by_pcs_replica_index",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pcsg-1",
						Labels: map[string]string{
							apicommon.LabelPodCliqueSetReplicaIndex: "0",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pcsg-2",
						Labels: map[string]string{
							apicommon.LabelPodCliqueSetReplicaIndex: "0",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pcsg-3",
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
			pcsgs:    []grovecorev1alpha1.PodCliqueScalingGroup{},
			expected: map[string]int{},
		},
		{
			name: "missing_label",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "pcsg-1",
						Labels: map[string]string{},
					},
				},
			},
			expected: map[string]int{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GroupPCSGsByPCSReplicaIndex(tc.pcsgs)
			assert.Equal(t, len(tc.expected), len(result))
			for replicaIndex, expectedCount := range tc.expected {
				pcsgs, exists := result[replicaIndex]
				assert.True(t, exists, "Expected replica index %s to exist", replicaIndex)
				assert.Equal(t, expectedCount, len(pcsgs))
			}
		})
	}
}

// TestGetPCSGsByPCSReplicaIndex tests the GetPCSGsByPCSReplicaIndex function
func TestGetPCSGsByPCSReplicaIndex(t *testing.T) {
	tests := []struct {
		name          string
		pcsObjKey     client.ObjectKey
		existingPCSGs []grovecorev1alpha1.PodCliqueScalingGroup
		expected      map[string][]string // replica index -> pcsg names
		expectError   bool
	}{
		{
			name: "groups_by_replica_index",
			pcsObjKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			existingPCSGs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey:             apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:                "test-pcs",
							apicommon.LabelPodCliqueSetReplicaIndex: "0",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-sg2",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey:             apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:                "test-pcs",
							apicommon.LabelPodCliqueSetReplicaIndex: "0",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-1-sg1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey:             apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:                "test-pcs",
							apicommon.LabelPodCliqueSetReplicaIndex: "1",
						},
					},
				},
			},
			expected: map[string][]string{
				"0": {"test-pcs-0-sg1", "test-pcs-0-sg2"},
				"1": {"test-pcs-1-sg1"},
			},
			expectError: false,
		},
		{
			name: "empty_result",
			pcsObjKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			existingPCSGs: []grovecorev1alpha1.PodCliqueScalingGroup{},
			expected:      map[string][]string{},
			expectError:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			runtimeObjs := []runtime.Object{}
			for i := range tc.existingPCSGs {
				runtimeObjs = append(runtimeObjs, &tc.existingPCSGs[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(runtimeObjs...).
				Build()

			ctx := context.Background()
			result, err := GetPCSGsByPCSReplicaIndex(ctx, fakeClient, tc.pcsObjKey)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(tc.expected), len(result))
				for replicaIndex, expectedNames := range tc.expected {
					pcsgs, exists := result[replicaIndex]
					assert.True(t, exists, "Expected replica index %s to exist", replicaIndex)
					assert.Equal(t, len(expectedNames), len(pcsgs))
					for i, pcsg := range pcsgs {
						assert.Equal(t, expectedNames[i], pcsg.Name)
					}
				}
			}
		})
	}
}

// TestIsPCSGUpdateInProgress tests the IsPCSGUpdateInProgress function
func TestIsPCSGUpdateInProgress(t *testing.T) {
	tests := []struct {
		name     string
		pcsg     *grovecorev1alpha1.PodCliqueScalingGroup
		expected bool
	}{
		{
			name: "no_rolling_update_progress",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					RollingUpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			name: "update_in_progress",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateProgress{
						UpdateStartedAt: metav1.Now(),
					},
				},
			},
			expected: true,
		},
		{
			name: "update_completed",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateProgress{
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
			result := IsPCSGUpdateInProgress(tc.pcsg)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestIsPCSGUpdateComplete tests the IsPCSGUpdateComplete function
func TestIsPCSGUpdateComplete(t *testing.T) {
	hash1 := "hash123"

	tests := []struct {
		name              string
		pcsg              *grovecorev1alpha1.PodCliqueScalingGroup
		pcsGenerationHash string
		expected          bool
	}{
		{
			name: "update_complete_matching_hash",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetGenerationHash: &hash1,
				},
			},
			pcsGenerationHash: "hash123",
			expected:          true,
		},
		{
			name: "update_incomplete_different_hash",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetGenerationHash: &hash1,
				},
			},
			pcsGenerationHash: "hash456",
			expected:          false,
		},
		{
			name: "no_current_hash",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetGenerationHash: nil,
				},
			},
			pcsGenerationHash: "hash123",
			expected:          false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsPCSGUpdateComplete(tc.pcsg, tc.pcsGenerationHash)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestGetPodCliqueFQNsForPCSG tests the GetPodCliqueFQNsForPCSG function
func TestGetPodCliqueFQNsForPCSG(t *testing.T) {
	tests := []struct {
		name         string
		pcsg         *grovecorev1alpha1.PodCliqueScalingGroup
		expectedFQNs []string
	}{
		{
			name: "single_replica_single_clique",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs-0-sg1",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    1,
					CliqueNames: []string{"worker"},
				},
			},
			expectedFQNs: []string{
				"test-pcs-0-sg1-0-worker",
			},
		},
		{
			name: "multiple_replicas_multiple_cliques",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs-0-sg1",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    2,
					CliqueNames: []string{"worker", "master"},
				},
			},
			expectedFQNs: []string{
				"test-pcs-0-sg1-0-worker",
				"test-pcs-0-sg1-0-master",
				"test-pcs-0-sg1-1-worker",
				"test-pcs-0-sg1-1-master",
			},
		},
		{
			name: "three_replicas_three_cliques",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs-1-sg2",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    3,
					CliqueNames: []string{"worker", "master", "coordinator"},
				},
			},
			expectedFQNs: []string{
				"test-pcs-1-sg2-0-worker",
				"test-pcs-1-sg2-0-master",
				"test-pcs-1-sg2-0-coordinator",
				"test-pcs-1-sg2-1-worker",
				"test-pcs-1-sg2-1-master",
				"test-pcs-1-sg2-1-coordinator",
				"test-pcs-1-sg2-2-worker",
				"test-pcs-1-sg2-2-master",
				"test-pcs-1-sg2-2-coordinator",
			},
		},
		{
			name: "zero_replicas",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs-0-sg1",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    0,
					CliqueNames: []string{"worker"},
				},
			},
			expectedFQNs: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GetPodCliqueFQNsForPCSG(tc.pcsg)
			assert.Equal(t, tc.expectedFQNs, result)
		})
	}
}

// TestGetPCLQTemplateHashesForPCSG tests the GetPCLQTemplateHashesForPCSG function
func TestGetPCLQTemplateHashesForPCSG(t *testing.T) {
	tests := []struct {
		name              string
		pcs               *grovecorev1alpha1.PodCliqueSet
		pcsg              *grovecorev1alpha1.PodCliqueScalingGroup
		expectError       bool
		expectedHashCount int
	}{
		{
			name: "successfully_computes_hashes",
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
									PodSpec: corev1.PodSpec{
										Containers: []corev1.Container{
											{
												Name:  "main",
												Image: "test:latest",
											},
										},
									},
								},
							},
							{
								Name: "master",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									RoleName: "master",
									Replicas: 1,
									PodSpec: corev1.PodSpec{
										Containers: []corev1.Container{
											{
												Name:  "main",
												Image: "test:latest",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-sg1",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    2,
					CliqueNames: []string{"worker", "master"},
				},
			},
			expectError:       false,
			expectedHashCount: 4, // 2 replicas * 2 cliques
		},
		{
			name: "single_replica_single_clique",
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
									PodSpec: corev1.PodSpec{
										Containers: []corev1.Container{
											{
												Name:  "main",
												Image: "test:latest",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-sg1",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    1,
					CliqueNames: []string{"worker"},
				},
			},
			expectError:       false,
			expectedHashCount: 1,
		},
		{
			name: "skips_missing_clique_template",
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
									PodSpec: corev1.PodSpec{
										Containers: []corev1.Container{
											{
												Name:  "main",
												Image: "test:latest",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-sg1",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    1,
					CliqueNames: []string{"worker", "nonexistent"},
				},
			},
			expectError:       false,
			expectedHashCount: 1, // Only worker exists
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cache := hash.GetPodTemplateSpecHashCache(ctx)
			logger := logr.Discard()

			result, err := GetPCLQTemplateHashesForPCSG(cache, logger, tc.pcs, tc.pcsg)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedHashCount, len(result))
				// Verify all hashes are non-empty
				for fqn, hashValue := range result {
					assert.NotEmpty(t, hashValue, "Hash for %s should not be empty", fqn)
				}
			}
		})
	}
}
