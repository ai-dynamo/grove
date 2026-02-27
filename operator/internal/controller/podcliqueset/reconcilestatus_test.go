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

package podcliqueset

import (
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Test constants
const (
	testNamespace = "test-namespace"
	testPCSName   = "test-pcs"
)

func TestComputePCSAvailableReplicas(t *testing.T) {
	pcsGenerationHash := string(uuid.NewUUID())
	pcsUID := uuid.NewUUID()
	testCases := []struct {
		name              string
		setupPCS          func() *grovecorev1alpha1.PodCliqueSet
		childResources    func() []client.Object
		expectedAvailable int32
	}{
		{
			name: "all healthy - 2 replicas with standalone and scaling groups",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(2).
					WithStandaloneClique("worker").
					WithScalingGroup("compute", []string{"frontend", "backend"}).
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					// Healthy PCSGs
					testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-compute", testNamespace, testPCSName, 0).
						WithOptions(testutils.WithPCSGAvailableReplicas(1)).Build(),
					testutils.NewPodCliqueScalingGroupBuilder("test-pcs-1-compute", testNamespace, testPCSName, 1).
						WithOptions(testutils.WithPCSGAvailableReplicas(1)).Build(),
					// Healthy standalone PodCliques
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "worker", testNamespace, 0).
						WithOptions(testutils.WithPCLQReplicaReadyStatus(1), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "worker", testNamespace, 1).
						WithOptions(testutils.WithPCLQReplicaReadyStatus(1), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
				}
			},
			expectedAvailable: 2,
		},
		{
			name: "mixed health - 1 healthy, 1 unhealthy",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(2).
					WithStandaloneClique("worker").
					WithScalingGroup("compute", []string{"frontend"}).
					Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-compute", testNamespace, testPCSName, 0).
						WithOptions(testutils.WithPCSGAvailableReplicas(1)).Build(),
					testutils.NewPodCliqueScalingGroupBuilder("test-pcs-1-compute", testNamespace, testPCSName, 1).
						WithOptions(testutils.WithPCSGMinAvailableBreached()).Build(),
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "worker", testNamespace, 0).
						WithOptions(testutils.WithPCLQReplicaReadyStatus(1), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "worker", testNamespace, 1).
						WithOptions(testutils.WithPCLQTerminating(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
				}
			},
			expectedAvailable: 1,
		},
		{
			name: "count mismatch - missing resources",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(1).
					WithStandaloneClique("worker").
					WithScalingGroup("compute", []string{"frontend"}).
					Build()
			},
			childResources: func() []client.Object {
				// Missing PCSG, extra standalone PodClique
				return []client.Object{
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "test-pcs-0-worker", testNamespace, 0).
						WithOptions(testutils.WithPCLQAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "test-pcs-0-extra", testNamespace, 0).
						WithOptions(testutils.WithPCLQAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
				}
			},
			expectedAvailable: 0,
		},
		{
			name: "empty configuration",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(1).
					Build()
			},
			childResources:    func() []client.Object { return []client.Object{} },
			expectedAvailable: 1,
		},
		{
			name: "only standalone cliques - all healthy",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(2).
					WithStandaloneClique("worker").
					WithStandaloneClique("monitor").
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "worker", testNamespace, 0).
						WithOptions(testutils.WithPCLQReplicaReadyStatus(1), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "monitor", testNamespace, 0).
						WithOptions(testutils.WithPCLQReplicaReadyStatus(1), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "worker", testNamespace, 1).
						WithOptions(testutils.WithPCLQReplicaReadyStatus(1), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "monitor", testNamespace, 1).
						WithOptions(testutils.WithPCLQReplicaReadyStatus(1), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
				}
			},
			expectedAvailable: 2,
		},
		{
			name: "only scaling groups - all healthy",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(2).
					WithScalingGroup("compute", []string{"frontend", "backend"}).
					WithScalingGroup("storage", []string{"database"}).
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-compute", testNamespace, testPCSName, 0).
						WithOptions(testutils.WithPCSGAvailableReplicas(1)).Build(),
					testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-storage", testNamespace, testPCSName, 0).
						WithOptions(testutils.WithPCSGAvailableReplicas(1)).Build(),
					testutils.NewPodCliqueScalingGroupBuilder("test-pcs-1-compute", testNamespace, testPCSName, 1).
						WithOptions(testutils.WithPCSGAvailableReplicas(1)).Build(),
					testutils.NewPodCliqueScalingGroupBuilder("test-pcs-1-storage", testNamespace, testPCSName, 1).
						WithOptions(testutils.WithPCSGAvailableReplicas(1)).Build(),
				}
			},
			expectedAvailable: 2,
		},
		{
			name: "terminating resources",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(1).
					WithStandaloneClique("worker").
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "test-pcs-0-worker", testNamespace, 0).
						WithOptions(testutils.WithPCLQTerminating(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
				}
			},
			expectedAvailable: 0,
		},
		{
			name: "unknown condition status",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(1).
					WithScalingGroup("compute", []string{"frontend"}).
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-compute", testNamespace, testPCSName, 0).
						WithOptions(testutils.WithPCSGUnknownCondition()).Build(),
				}
			},
			expectedAvailable: 0,
		},
		{
			name: "no conditions set",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(1).
					WithStandaloneClique("worker").
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "test-pcs-0-worker", testNamespace, 0).
						WithOptions(testutils.WithPCLQNoConditions()).Build(),
				}
			},
			expectedAvailable: 0,
		},
		// Edge cases
		{
			name: "no child resources when expected",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(1).
					WithScalingGroup("compute", []string{"frontend"}).
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					Build()
			},
			childResources:    func() []client.Object { return []client.Object{} },
			expectedAvailable: 0,
		},
		{
			name: "extra unexpected resources",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(1).
					WithStandaloneClique("worker").
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "worker", testNamespace, 0).
						WithOptions(testutils.WithPCLQReplicaReadyStatus(1), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-unexpected", testNamespace, testPCSName, 0).
						WithOptions(testutils.WithPCSGAvailableReplicas(1)).Build(),
				}
			},
			expectedAvailable: 1,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			//t.Parallel()
			pcs := tt.setupPCS()
			existingObjects := []client.Object{pcs}
			existingObjects = append(existingObjects, tt.childResources()...)
			cl := testutils.CreateDefaultFakeClient(existingObjects)
			reconciler := &Reconciler{client: cl}
			// Compute available replicas
			available, _, err := reconciler.computeAvailableAndUpdatedReplicas(context.Background(), logr.Discard(), pcs)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedAvailable, available, "Available replicas mismatch")
		})
	}
}

// TestMirrorUpdateProgressToRollingUpdateProgressPCS tests the mirrorUpdateProgressToRollingUpdateProgress function for PodCliqueSet
func TestMirrorUpdateProgressToRollingUpdateProgressPCS(t *testing.T) {
	updateStartedAt := metav1.Now()
	updateEndedAt := metav1.NewTime(updateStartedAt.Add(1))
	replicaUpdateStartedAt := metav1.NewTime(updateStartedAt.Add(2))
	tests := []struct {
		name                          string
		pcs                           *grovecorev1alpha1.PodCliqueSet
		expectedRollingUpdateProgress *grovecorev1alpha1.PodCliqueSetRollingUpdateProgress
	}{
		{
			name: "nil UpdateProgress results in nil RollingUpdateProgress",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: nil,
				},
			},
			expectedRollingUpdateProgress: nil,
		},
		{
			name: "UpdateProgress with CurrentlyUpdating",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt:               updateStartedAt,
						UpdatedPodCliqueScalingGroups: []string{"pcsg-1"},
						UpdatedPodCliques:             []string{"pclq-1", "pclq-2"},
						CurrentlyUpdating: &grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
							ReplicaIndex:    2,
							UpdateStartedAt: replicaUpdateStartedAt,
						},
					},
				},
			},
			expectedRollingUpdateProgress: &grovecorev1alpha1.PodCliqueSetRollingUpdateProgress{
				UpdateStartedAt:               updateStartedAt,
				UpdatedPodCliqueScalingGroups: []string{"pcsg-1"},
				UpdatedPodCliques:             []string{"pclq-1", "pclq-2"},
				CurrentlyUpdating: &grovecorev1alpha1.PodCliqueSetReplicaRollingUpdateProgress{
					ReplicaIndex:    2,
					UpdateStartedAt: replicaUpdateStartedAt,
				},
			},
		},
		{
			name: "clears existing RollingUpdateProgress when UpdateProgress is nil",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: nil,
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueSetRollingUpdateProgress{
						UpdateStartedAt:               updateStartedAt,
						UpdateEndedAt:                 ptr.To(updateEndedAt),
						UpdatedPodCliqueScalingGroups: []string{"old-pcsg"},
						UpdatedPodCliques:             []string{"old-pclq"},
					},
				},
			},
			expectedRollingUpdateProgress: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call the function
			mirrorUpdateProgressToRollingUpdateProgress(tt.pcs)

			// Assert the result
			if tt.expectedRollingUpdateProgress == nil {
				assert.Nil(t, tt.pcs.Status.RollingUpdateProgress,
					"RollingUpdateProgress should be nil")
			} else {
				assert.NotNil(t, tt.pcs.Status.RollingUpdateProgress,
					"RollingUpdateProgress should not be nil")
				assert.Equal(t, tt.expectedRollingUpdateProgress.UpdateStartedAt,
					tt.pcs.Status.RollingUpdateProgress.UpdateStartedAt,
					"UpdateStartedAt should match")
				assert.Equal(t, tt.expectedRollingUpdateProgress.UpdateEndedAt,
					tt.pcs.Status.RollingUpdateProgress.UpdateEndedAt,
					"UpdateEndedAt should match")
				assert.Equal(t, tt.expectedRollingUpdateProgress.UpdatedPodCliqueScalingGroups,
					tt.pcs.Status.RollingUpdateProgress.UpdatedPodCliqueScalingGroups,
					"UpdatedPodCliqueScalingGroups should match")
				assert.Equal(t, tt.expectedRollingUpdateProgress.UpdatedPodCliques,
					tt.pcs.Status.RollingUpdateProgress.UpdatedPodCliques,
					"UpdatedPodCliques should match")

				// Check CurrentlyUpdating
				if tt.expectedRollingUpdateProgress.CurrentlyUpdating == nil {
					assert.Nil(t, tt.pcs.Status.RollingUpdateProgress.CurrentlyUpdating,
						"CurrentlyUpdating should be nil")
				} else {
					assert.NotNil(t, tt.pcs.Status.RollingUpdateProgress.CurrentlyUpdating,
						"CurrentlyUpdating should not be nil")
					assert.Equal(t, tt.expectedRollingUpdateProgress.CurrentlyUpdating.ReplicaIndex,
						tt.pcs.Status.RollingUpdateProgress.CurrentlyUpdating.ReplicaIndex,
						"ReplicaIndex should match")
					assert.Equal(t, tt.expectedRollingUpdateProgress.CurrentlyUpdating.UpdateStartedAt,
						tt.pcs.Status.RollingUpdateProgress.CurrentlyUpdating.UpdateStartedAt,
						"CurrentlyUpdating.UpdateStartedAt should match")
				}
			}
		})
	}
}
