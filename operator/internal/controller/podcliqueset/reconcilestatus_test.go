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

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
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
						CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
							{
								ReplicaIndex:    2,
								UpdateStartedAt: replicaUpdateStartedAt,
							},
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

// TestMutateTopologyLevelUnavailableConditions tests the mutateTopologyLevelUnavailableConditions function.
// It covers TAS-disabled paths, backward-compat paths (missing topologyName), and fully-specified
// ClusterTopology paths (not found, unavailable domains, all available).
func TestMutateTopologyLevelUnavailableConditions(t *testing.T) {
	// basePCS returns a PodCliqueSet with a PCS-level TopologyConstraint pointing at "my-topology".
	basePCS := func(topologyName string) *grovecorev1alpha1.PodCliqueSet {
		var tc *grovecorev1alpha1.TopologyConstraint
		if topologyName != "" {
			tc = &grovecorev1alpha1.TopologyConstraint{
				TopologyName: topologyName,
				PackDomain:   grovecorev1alpha1.TopologyDomainRack,
			}
		}
		return &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:       testPCSName,
				Namespace:  testNamespace,
				Generation: 1,
			},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 1,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					TopologyConstraint: tc,
					Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
						{
							Name: "worker",
							Spec: grovecorev1alpha1.PodCliqueSpec{
								Replicas:     1,
								MinAvailable: ptr.To(int32(1)),
							},
						},
					},
				},
			},
		}
	}

	// clusterTopology builds a ClusterTopology with the given levels.
	clusterTopology := func(name string, levels []grovecorev1alpha1.TopologyLevel) *grovecorev1alpha1.ClusterTopology {
		return &grovecorev1alpha1.ClusterTopology{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: grovecorev1alpha1.ClusterTopologySpec{
				Levels: levels,
			},
		}
	}

	standardLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
	}

	testCases := []struct {
		name           string
		tasEnabled     bool
		setupPCS       func() *grovecorev1alpha1.PodCliqueSet
		extraObjects   []client.Object
		wantErr        bool
		wantCondNil    bool // true when we expect the condition to be absent
		wantStatus     metav1.ConditionStatus
		wantReason     string
		wantMsgContain string
	}{
		{
			name:       "TAS disabled, no topology constraints — condition removed",
			tasEnabled: false,
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return basePCS("") // no TopologyConstraint at any level
			},
			wantCondNil: true,
		},
		{
			name:       "TAS disabled, PCS has topology constraints — Unknown/TopologyAwareSchedulingDisabled",
			tasEnabled: false,
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := basePCS("")
				// Add a clique-level constraint so getUniqueTopologyDomainsInPodCliqueSet returns domains.
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					PackDomain: grovecorev1alpha1.TopologyDomainRack,
				}
				return pcs
			},
			wantStatus:     metav1.ConditionUnknown,
			wantReason:     apicommonconstants.ConditionReasonTopologyAwareSchedulingDisabled,
			wantMsgContain: "disabled",
		},
		{
			name:       "TAS enabled, topologyName set, CT exists, all domains available — False/AllClusterTopologyLevelsAvailable",
			tasEnabled: true,
			setupPCS:   func() *grovecorev1alpha1.PodCliqueSet { return basePCS("my-topology") },
			extraObjects: []client.Object{
				clusterTopology("my-topology", standardLevels),
			},
			wantStatus:     metav1.ConditionFalse,
			wantReason:     apicommonconstants.ConditionReasonAllTopologyLevelsAvailable,
			wantMsgContain: "available",
		},
		{
			name:       "TAS enabled, named ClusterTopology is used instead of another available topology",
			tasEnabled: true,
			setupPCS:   func() *grovecorev1alpha1.PodCliqueSet { return basePCS("selected-topology") },
			extraObjects: []client.Object{
				clusterTopology("selected-topology", standardLevels),
				clusterTopology("other-topology", []grovecorev1alpha1.TopologyLevel{
					{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				}),
			},
			wantStatus:     metav1.ConditionFalse,
			wantReason:     apicommonconstants.ConditionReasonAllTopologyLevelsAvailable,
			wantMsgContain: "available",
		},
		{
			name:       "TAS enabled, topologyName set, CT exists, some domains unavailable — True/ClusterTopologyLevelsUnavailable",
			tasEnabled: true,
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := basePCS("my-topology")
				// Add a clique-level constraint with a domain not present in the CT levels.
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "my-topology",
					PackDomain:   grovecorev1alpha1.TopologyDomainHost, // "host" is not in standardLevels
				}
				return pcs
			},
			extraObjects: []client.Object{
				// CT only has zone and rack — "host" is missing.
				clusterTopology("my-topology", standardLevels),
			},
			wantStatus:     metav1.ConditionTrue,
			wantReason:     apicommonconstants.ConditionReasonTopologyLevelsUnavailable,
			wantMsgContain: "Unavailable",
		},
		{
			name:         "TAS enabled, topologyName set, CT not found — Unknown/ClusterTopologyNotFound",
			tasEnabled:   true,
			setupPCS:     func() *grovecorev1alpha1.PodCliqueSet { return basePCS("missing-topology") },
			extraObjects: nil, // no CT in fake store
			wantStatus:   metav1.ConditionUnknown,
			wantReason:   apicommonconstants.ConditionReasonClusterTopologyNotFound,
		},
		{
			name:       "TAS enabled, no topologyName, clique has constraints (backward compat) — Unknown/TopologyNameMissing",
			tasEnabled: true,
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := basePCS("") // PCS-level TopologyConstraint is nil
				// Only a clique-level constraint is present, but it is incomplete.
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					PackDomain: grovecorev1alpha1.TopologyDomainRack,
				}
				return pcs
			},
			wantStatus:     metav1.ConditionUnknown,
			wantReason:     apicommonconstants.ConditionReasonTopologyNameMissing,
			wantMsgContain: "both topologyName and packDomain",
		},
		{
			name:           "TAS enabled, no constraints at all — False/AllClusterTopologyLevelsAvailable with no-constraints message",
			tasEnabled:     true,
			setupPCS:       func() *grovecorev1alpha1.PodCliqueSet { return basePCS("") },
			extraObjects:   nil,
			wantStatus:     metav1.ConditionFalse,
			wantReason:     apicommonconstants.ConditionReasonAllTopologyLevelsAvailable,
			wantMsgContain: "No topology constraints defined",
		},
		{
			name:       "TAS enabled, incomplete PCS topology constraint with valid PCSG constraint — Unknown/TopologyNameMissing",
			tasEnabled: true,
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := basePCS("")
				pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "my-topology",
				}
				pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{
						Name:        "workers",
						CliqueNames: []string{"worker"},
						TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
							TopologyName: "my-topology",
							PackDomain:   grovecorev1alpha1.TopologyDomainRack,
						},
					},
				}
				return pcs
			},
			extraObjects: []client.Object{
				clusterTopology("my-topology", standardLevels),
			},
			wantStatus:     metav1.ConditionUnknown,
			wantReason:     apicommonconstants.ConditionReasonTopologyNameMissing,
			wantMsgContain: "both topologyName and packDomain",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pcs := tc.setupPCS()
			existingObjects := []client.Object{pcs}
			existingObjects = append(existingObjects, tc.extraObjects...)
			fakeClient := testutils.CreateDefaultFakeClient(existingObjects)

			r := &Reconciler{
				client:    fakeClient,
				tasConfig: configv1alpha1.TopologyAwareSchedulingConfiguration{Enabled: tc.tasEnabled},
			}

			err := r.mutateTopologyLevelUnavailableConditions(context.Background(), logr.Discard(), pcs)

			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			cond := apimeta.FindStatusCondition(pcs.Status.Conditions, apicommonconstants.ConditionTopologyLevelsUnavailable)
			if tc.wantCondNil {
				assert.Nil(t, cond, "expected condition to be absent")
				return
			}
			if !assert.NotNil(t, cond, "expected condition to be present") {
				return
			}
			assert.Equal(t, tc.wantStatus, cond.Status, "condition status mismatch")
			assert.Equal(t, tc.wantReason, cond.Reason, "condition reason mismatch")
			if tc.wantMsgContain != "" {
				assert.Contains(t, cond.Message, tc.wantMsgContain, "condition message mismatch")
			}
			assert.Equal(t, pcs.Generation, cond.ObservedGeneration, "ObservedGeneration should match PCS generation")
		})
	}
}

func TestGetUniqueTopologyDomainsInPodCliqueSet(t *testing.T) {
	makePCS := func(pcsTCFn func(*grovecorev1alpha1.PodCliqueSetTemplateSpec)) *grovecorev1alpha1.PodCliqueSet {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
						{Name: "worker"},
					},
				},
			},
		}
		if pcsTCFn != nil {
			pcsTCFn(&pcs.Spec.Template)
		}
		return pcs
	}

	tests := []struct {
		name        string
		setupPCS    func() *grovecorev1alpha1.PodCliqueSet
		wantDomains []grovecorev1alpha1.TopologyDomain
	}{
		{
			name:        "no constraints — empty result",
			setupPCS:    func() *grovecorev1alpha1.PodCliqueSet { return makePCS(nil) },
			wantDomains: nil,
		},
		{
			name: "PCS-level topologyName only, empty packDomain — empty result",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return makePCS(func(tmpl *grovecorev1alpha1.PodCliqueSetTemplateSpec) {
					tmpl.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "my-topology",
						// PackDomain intentionally empty
					}
				})
			},
			wantDomains: nil,
		},
		{
			name: "PCS-level topologyName and non-empty packDomain — packDomain included",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return makePCS(func(tmpl *grovecorev1alpha1.PodCliqueSetTemplateSpec) {
					tmpl.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "my-topology",
						PackDomain:   grovecorev1alpha1.TopologyDomainRack,
					}
				})
			},
			wantDomains: []grovecorev1alpha1.TopologyDomain{grovecorev1alpha1.TopologyDomainRack},
		},
		{
			name: "PCS empty packDomain + PCSG non-empty packDomain — only PCSG domain included",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return makePCS(func(tmpl *grovecorev1alpha1.PodCliqueSetTemplateSpec) {
					tmpl.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "my-topology",
						// PackDomain intentionally empty
					}
					tmpl.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{
						{
							Name:        "workers",
							CliqueNames: []string{"worker"},
							TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
								PackDomain: grovecorev1alpha1.TopologyDomainRack,
							},
						},
					}
				})
			},
			wantDomains: []grovecorev1alpha1.TopologyDomain{grovecorev1alpha1.TopologyDomainRack},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := componentutils.GetUniqueTopologyDomainsInPodCliqueSet(tt.setupPCS())
			assert.ElementsMatch(t, tt.wantDomains, got)
		})
	}
}
