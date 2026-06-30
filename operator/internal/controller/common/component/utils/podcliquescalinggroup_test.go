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
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// TestIsPCSGUpdateInProgress tests the IsPCSGUpdateInProgress function
func TestIsPCSGUpdateInProgress(t *testing.T) {
	tests := []struct {
		name     string
		pcsg     *grovecorev1alpha1.PodCliqueScalingGroup
		expected bool
	}{
		{
			name: "returns_false_when_update_progress_is_nil",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					UpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			name: "returns_true_when_update_in_progress",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   nil, // nil means in progress
					},
				},
			},
			expected: true,
		},
		{
			name: "returns_false_when_update_completed",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   ptr.To(metav1.Now()), // set means completed
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
	tests := []struct {
		name              string
		pcsg              *grovecorev1alpha1.PodCliqueScalingGroup
		pcsGenerationHash string
		compatibleHashes  []string
		expected          bool
	}{
		{
			name: "returns_false_when_current_pcs_generation_hash_is_nil",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetGenerationHash: nil,
				},
			},
			pcsGenerationHash: "hash1",
			expected:          false,
		},
		{
			name: "returns_true_when_hash_matches",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetGenerationHash: ptr.To("hash1"),
				},
			},
			pcsGenerationHash: "hash1",
			expected:          true,
		},
		{
			name: "returns_false_when_hash_differs",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetGenerationHash: ptr.To("old-hash"),
				},
			},
			pcsGenerationHash: "new-hash",
			expected:          false,
		},
		{
			name: "returns_true_when_hash_matches_compatible_legacy_hash",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetGenerationHash: ptr.To("legacy-hash"),
				},
			},
			pcsGenerationHash: "canonical-hash",
			compatibleHashes:  []string{"legacy-hash"},
			expected:          true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsPCSGUpdateComplete(tc.pcsg, tc.pcsGenerationHash, tc.compatibleHashes...)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestListPCSGsForPCS(t *testing.T) {
	pcsName := "test-pcs"
	namespace := "default"
	pcsObjKey := client.ObjectKey{Namespace: namespace, Name: pcsName}
	matchingLabels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)

	t.Run("returns matching PCSGs", func(t *testing.T) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0-sga",
				Namespace: namespace,
				Labels:    matchingLabels,
			},
			Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
				Replicas: 3,
			},
		}
		cl := testutils.NewTestClientBuilder().WithObjects(pcsg).Build()

		result, err := ListPCSGsForPCS(context.Background(), cl, pcsObjKey)

		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, "test-pcs-0-sga", result[0].Name)
	})

	t.Run("excludes PCSGs belonging to a different PCS", func(t *testing.T) {
		ownedPCSG := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0-sga",
				Namespace: namespace,
				Labels:    matchingLabels,
			},
		}
		otherLabels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources("other-pcs")
		otherPCSG := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "other-pcs-0-sga",
				Namespace: namespace,
				Labels:    otherLabels,
			},
		}
		cl := testutils.NewTestClientBuilder().WithObjects(ownedPCSG, otherPCSG).Build()

		result, err := ListPCSGsForPCS(context.Background(), cl, pcsObjKey)

		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, "test-pcs-0-sga", result[0].Name)
	})

	t.Run("returns empty when no PCSGs exist", func(t *testing.T) {
		cl := testutils.NewTestClientBuilder().Build()

		result, err := ListPCSGsForPCS(context.Background(), cl, pcsObjKey)

		require.NoError(t, err)
		assert.Empty(t, result)
	})
}

func TestGetPCSGsByPCSReplicaIndex(t *testing.T) {
	pcsName := "test-pcs"
	namespace := "default"
	pcsObjKey := client.ObjectKey{Namespace: namespace, Name: pcsName}
	matchingLabels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)

	t.Run("groups PCSGs by replica index", func(t *testing.T) {
		pcsg0a := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0-sga",
				Namespace: namespace,
				Labels: func() map[string]string {
					l := make(map[string]string)
					for k, v := range matchingLabels {
						l[k] = v
					}
					l[apicommon.LabelPodCliqueSetReplicaIndex] = "0"
					return l
				}(),
			},
		}
		pcsg0b := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0-sgb",
				Namespace: namespace,
				Labels: func() map[string]string {
					l := make(map[string]string)
					for k, v := range matchingLabels {
						l[k] = v
					}
					l[apicommon.LabelPodCliqueSetReplicaIndex] = "0"
					return l
				}(),
			},
		}
		pcsg1a := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-1-sga",
				Namespace: namespace,
				Labels: func() map[string]string {
					l := make(map[string]string)
					for k, v := range matchingLabels {
						l[k] = v
					}
					l[apicommon.LabelPodCliqueSetReplicaIndex] = "1"
					return l
				}(),
			},
		}
		cl := testutils.NewTestClientBuilder().WithObjects(pcsg0a, pcsg0b, pcsg1a).Build()

		result, err := GetPCSGsByPCSReplicaIndex(context.Background(), cl, pcsObjKey)

		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Len(t, result[0], 2)
		assert.Len(t, result[1], 1)
	})

	t.Run("skips PCSGs without replica index label", func(t *testing.T) {
		pcsgWithLabel := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-0-sga",
				Namespace: namespace,
				Labels: func() map[string]string {
					l := make(map[string]string)
					for k, v := range matchingLabels {
						l[k] = v
					}
					l[apicommon.LabelPodCliqueSetReplicaIndex] = "0"
					return l
				}(),
			},
		}
		pcsgWithoutLabel := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs-orphan",
				Namespace: namespace,
				Labels:    matchingLabels,
			},
		}
		cl := testutils.NewTestClientBuilder().WithObjects(pcsgWithLabel, pcsgWithoutLabel).Build()

		result, err := GetPCSGsByPCSReplicaIndex(context.Background(), cl, pcsObjKey)

		require.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Len(t, result[0], 1)
	})

	t.Run("returns empty map when no PCSGs exist", func(t *testing.T) {
		cl := testutils.NewTestClientBuilder().Build()

		result, err := GetPCSGsByPCSReplicaIndex(context.Background(), cl, pcsObjKey)

		require.NoError(t, err)
		assert.Empty(t, result)
	})
}

// TestGetPCLQTemplateHashes covers the per-clique-once optimization. The hash depends only on
// the clique template + PCS priority class, so every PCSG-replica entry for a given clique must
// carry the same value, and missing-template cliques must be omitted from the result.
func TestGetPCLQTemplateHashes(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pcs", Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				PriorityClassName: "high",
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{
						Name:   "leader",
						Labels: map[string]string{"role": "leader"},
						Spec:   grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)},
					},
					{
						Name:   "worker",
						Labels: map[string]string{"role": "worker"},
						Spec:   grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To[int32](2)},
					},
					// "stranger" is not in any PCSG; should never appear in output.
					{
						Name:   "stranger",
						Labels: map[string]string{"role": "stranger"},
						Spec:   grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)},
					},
				},
			},
		},
	}
	leaderHash := ComputePCLQPodTemplateHash(pcs.Spec.Template.Cliques[0], pcs.Spec.Template.PriorityClassName)
	workerHash := ComputePCLQPodTemplateHash(pcs.Spec.Template.Cliques[1], pcs.Spec.Template.PriorityClassName)
	require.NotEqual(t, leaderHash, workerHash, "test fixture must produce distinct hashes per clique")

	t.Run("populates one entry per (PCSG replica, clique) with the per-clique hash", func(t *testing.T) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-sg", Namespace: "default"},
			Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
				Replicas:    3,
				CliqueNames: []string{"leader", "worker"},
			},
		}

		got := GetPCLQTemplateHashes(pcs, pcsg)

		require.Len(t, got, 6) // 3 replicas × 2 cliques
		for replica := 0; replica < 3; replica++ {
			leaderFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: replica}, "leader")
			workerFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: replica}, "worker")
			assert.Equal(t, leaderHash, got[leaderFQN], "leader hash on replica %d", replica)
			assert.Equal(t, workerHash, got[workerFQN], "worker hash on replica %d", replica)
		}
	})

	t.Run("clique not present in PCS template is silently skipped", func(t *testing.T) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-sg", Namespace: "default"},
			Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
				Replicas:    2,
				CliqueNames: []string{"leader", "missing"}, // "missing" is not in pcs.Spec.Template.Cliques
			},
		}

		got := GetPCLQTemplateHashes(pcs, pcsg)

		require.Len(t, got, 2, "only leader entries should be present, missing is skipped")
		for replica := 0; replica < 2; replica++ {
			leaderFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: replica}, "leader")
			missingFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: replica}, "missing")
			assert.Equal(t, leaderHash, got[leaderFQN])
			_, exists := got[missingFQN]
			assert.False(t, exists, "missing-clique FQN must not appear")
		}
	})

	t.Run("zero replicas returns empty map", func(t *testing.T) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-sg", Namespace: "default"},
			Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
				Replicas:    0,
				CliqueNames: []string{"leader", "worker"},
			},
		}

		got := GetPCLQTemplateHashes(pcs, pcsg)
		assert.Empty(t, got)
	})

	t.Run("empty CliqueNames returns empty map", func(t *testing.T) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-sg", Namespace: "default"},
			Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
				Replicas:    3,
				CliqueNames: nil,
			},
		}

		got := GetPCLQTemplateHashes(pcs, pcsg)
		assert.Empty(t, got)
	})
}
