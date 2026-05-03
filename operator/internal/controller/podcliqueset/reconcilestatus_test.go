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
	"time"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			stats, err := reconciler.computeAvailableAndUpdatedReplicas(context.Background(), logr.Discard(), pcs)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedAvailable, stats.availableReplicas, "Available replicas mismatch")
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

// TestComputePCSUpdateProgressCounts exercises the new bounded-count outputs of
// computeAvailableAndUpdatedReplicas. Standalone PCLQs and PCSGs are tallied separately;
// PCSG-owned PCLQs are tracked on their owning PCSG, not here. Terminating children and
// children whose names aren't in the expected set must be excluded from the counts.
func TestComputePCSUpdateProgressCounts(t *testing.T) {
	pcsHash := "gen-hash-current"
	oldHash := "gen-hash-old"
	pcsUID := uuid.NewUUID()

	standalonePCLQ := func(replicaIndex int32, hash string) *grovecorev1alpha1.PodClique {
		return testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, replicaIndex).
			WithOptions(testutils.WithPCLQCurrentPCSGenerationHash(hash)).Build()
	}
	standalonePCLQNoHash := func(replicaIndex int32) *grovecorev1alpha1.PodClique {
		return testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, replicaIndex).Build()
	}
	standaloneTerminatingPCLQ := func(replicaIndex int32, hash string) *grovecorev1alpha1.PodClique {
		return testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, replicaIndex).
			WithOptions(testutils.WithPCLQCurrentPCSGenerationHash(hash), testutils.WithPCLQTerminating()).Build()
	}
	// makePCSG builds a PCSG whose CurrentPodCliqueSetGenerationHash equals `hash` (i.e. updated).
	makePCSG := func(replicaIndex int, hash string) *grovecorev1alpha1.PodCliqueScalingGroup {
		return testutils.NewPodCliqueScalingGroupBuilder(
			pcsgName(replicaIndex), testNamespace, testPCSName, replicaIndex,
		).WithOptions(testutils.WithPCSGCurrentPCSGenerationHash(hash)).Build()
	}
	// makePCSGNoHash builds a PCSG with no CurrentPodCliqueSetGenerationHash (not yet updated).
	makePCSGNoHash := func(replicaIndex int) *grovecorev1alpha1.PodCliqueScalingGroup {
		return testutils.NewPodCliqueScalingGroupBuilder(
			pcsgName(replicaIndex), testNamespace, testPCSName, replicaIndex,
		).Build()
	}

	testCases := []struct {
		name             string
		setupPCS         func() *grovecorev1alpha1.PodCliqueSet
		childResources   func() []client.Object
		wantUpdatedPCLQs int32
		wantTotalPCLQs   int32
		wantUpdatedPCSGs int32
		wantTotalPCSGs   int32
	}{
		{
			name: "all standalone PCLQs and PCSGs at current hash",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(2).
					WithStandaloneClique("worker").
					WithScalingGroup("compute", []string{"frontend"}).
					WithPodCliqueSetGenerationHash(&pcsHash).Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					makePCSG(0, pcsHash), makePCSG(1, pcsHash),
					standalonePCLQ(0, pcsHash), standalonePCLQ(1, pcsHash),
				}
			},
			wantUpdatedPCLQs: 2, wantTotalPCLQs: 2,
			wantUpdatedPCSGs: 2, wantTotalPCSGs: 2,
		},
		{
			name: "one PCLQ at old hash, one PCSG missing hash",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(2).
					WithStandaloneClique("worker").
					WithScalingGroup("compute", []string{"frontend"}).
					WithPodCliqueSetGenerationHash(&pcsHash).Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					makePCSG(0, pcsHash),         // updated
					makePCSGNoHash(1),            // not updated — no current hash
					standalonePCLQ(0, pcsHash),   // updated
					standalonePCLQ(1, oldHash),   // not updated — old hash
				}
			},
			wantUpdatedPCLQs: 1, wantTotalPCLQs: 2,
			wantUpdatedPCSGs: 1, wantTotalPCSGs: 2,
		},
		{
			name: "terminating standalone PCLQ excluded from updated count",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(2).
					WithStandaloneClique("worker").
					WithPodCliqueSetGenerationHash(&pcsHash).Build()
			},
			childResources: func() []client.Object {
				return []client.Object{
					standalonePCLQ(0, pcsHash),            // counted
					standaloneTerminatingPCLQ(1, pcsHash), // skipped — terminating
				}
			},
			wantUpdatedPCLQs: 1, wantTotalPCLQs: 2,
			wantUpdatedPCSGs: 0, wantTotalPCSGs: 0,
		},
		{
			name: "PCLQ with no generation hash is not counted as updated",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(1).
					WithStandaloneClique("worker").
					WithPodCliqueSetGenerationHash(&pcsHash).Build()
			},
			childResources: func() []client.Object {
				return []client.Object{standalonePCLQNoHash(0)}
			},
			wantUpdatedPCLQs: 0, wantTotalPCLQs: 1,
			wantUpdatedPCSGs: 0, wantTotalPCSGs: 0,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			pcs := tt.setupPCS()
			objects := append([]client.Object{pcs}, tt.childResources()...)
			cl := testutils.CreateDefaultFakeClient(objects)
			r := &Reconciler{client: cl}

			stats, err := r.computeAvailableAndUpdatedReplicas(context.Background(), logr.Discard(), pcs)
			require.NoError(t, err)
			assert.Equal(t, tt.wantUpdatedPCLQs, stats.updatedPCLQs, "updatedPCLQs")
			assert.Equal(t, tt.wantTotalPCLQs, stats.totalPCLQs, "totalPCLQs")
			assert.Equal(t, tt.wantUpdatedPCSGs, stats.updatedPCSGs, "updatedPCSGs")
			assert.Equal(t, tt.wantTotalPCSGs, stats.totalPCSGs, "totalPCSGs")
		})
	}
}

// TestPCSMutateReplicasWritesUpdateProgressCounts asserts that mutateReplicas only writes
// the new bounded count fields when UpdateProgress is non-nil. When UpdateProgress is nil,
// the counts must not be allocated/written (no spurious status churn).
func TestPCSMutateReplicasWritesUpdateProgressCounts(t *testing.T) {
	pcsHash := "gen-hash-current"
	pcsUID := uuid.NewUUID()

	build := func(withProgress bool) (*grovecorev1alpha1.PodCliqueSet, []client.Object) {
		b := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
			WithReplicas(2).
			WithStandaloneClique("worker").
			WithScalingGroup("compute", []string{"frontend"}).
			WithPodCliqueSetGenerationHash(&pcsHash)
		if withProgress {
			b = b.WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
				UpdateStartedAt: metav1.Now(),
			})
		}
		pcs := b.Build()
		children := []client.Object{
			testutils.NewPodCliqueScalingGroupBuilder(pcsgName(0), testNamespace, testPCSName, 0).
				WithOptions(testutils.WithPCSGCurrentPCSGenerationHash(pcsHash)).Build(),
			testutils.NewPodCliqueScalingGroupBuilder(pcsgName(1), testNamespace, testPCSName, 1).
				WithOptions(testutils.WithPCSGCurrentPCSGenerationHash(pcsHash)).Build(),
			testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, 0).
				WithOptions(testutils.WithPCLQCurrentPCSGenerationHash(pcsHash)).Build(),
			testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, 1).
				WithOptions(testutils.WithPCLQCurrentPCSGenerationHash(pcsHash)).Build(),
		}
		return pcs, children
	}

	t.Run("UpdateProgress nil — counts not written", func(t *testing.T) {
		pcs, children := build(false)
		cl := testutils.CreateDefaultFakeClient(append([]client.Object{pcs}, children...))
		r := &Reconciler{client: cl}
		require.NoError(t, r.mutateReplicas(context.Background(), logr.Discard(), pcs))
		assert.Nil(t, pcs.Status.UpdateProgress, "UpdateProgress must remain nil when not initialized")
	})
	t.Run("UpdateProgress non-nil — counts populated from informer cache", func(t *testing.T) {
		pcs, children := build(true)
		cl := testutils.CreateDefaultFakeClient(append([]client.Object{pcs}, children...))
		r := &Reconciler{client: cl}
		require.NoError(t, r.mutateReplicas(context.Background(), logr.Discard(), pcs))
		require.NotNil(t, pcs.Status.UpdateProgress)
		assert.Equal(t, int32(2), pcs.Status.UpdateProgress.UpdatedPodCliquesCount)
		assert.Equal(t, int32(2), pcs.Status.UpdateProgress.TotalPodCliquesCount)
		assert.Equal(t, int32(2), pcs.Status.UpdateProgress.UpdatedPodCliqueScalingGroupsCount)
		assert.Equal(t, int32(2), pcs.Status.UpdateProgress.TotalPodCliqueScalingGroupsCount)
	})
}

func TestCountUpdatedPCLQs(t *testing.T) {
	hash := "h"
	otherHash := "old"
	matching := grovecorev1alpha1.PodClique{}
	matching.Name = "matching"
	matching.Status.CurrentPodCliqueSetGenerationHash = &hash

	nonMatching := grovecorev1alpha1.PodClique{}
	nonMatching.Name = "non-matching"
	nonMatching.Status.CurrentPodCliqueSetGenerationHash = &otherHash

	noHash := grovecorev1alpha1.PodClique{}
	noHash.Name = "no-hash"

	terminatingMatching := grovecorev1alpha1.PodClique{}
	terminatingMatching.Name = "terminating"
	terminatingMatching.Status.CurrentPodCliqueSetGenerationHash = &hash
	now := metav1.NewTime(time.Now())
	terminatingMatching.DeletionTimestamp = &now
	terminatingMatching.Finalizers = []string{"f"}

	tests := []struct {
		name string
		hash *string
		in   []grovecorev1alpha1.PodClique
		want int32
	}{
		{"nil hash → 0 (early return guards against unintialized PCS)", nil, []grovecorev1alpha1.PodClique{matching}, 0},
		{"empty input → 0", &hash, nil, 0},
		{"all matching", &hash, []grovecorev1alpha1.PodClique{matching, matching}, 2},
		{"none matching", &hash, []grovecorev1alpha1.PodClique{nonMatching, noHash}, 0},
		{"mixed", &hash, []grovecorev1alpha1.PodClique{matching, nonMatching, matching, noHash}, 2},
		{"terminating excluded even if hash matches", &hash, []grovecorev1alpha1.PodClique{matching, terminatingMatching}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, countUpdatedPCLQs(tt.hash, tt.in))
		})
	}
}

func TestCountUpdatedPCSGs(t *testing.T) {
	hash := "h"
	otherHash := "old"

	matching := grovecorev1alpha1.PodCliqueScalingGroup{}
	matching.Name = "m"
	matching.Status.CurrentPodCliqueSetGenerationHash = &hash

	nonMatching := grovecorev1alpha1.PodCliqueScalingGroup{}
	nonMatching.Name = "n"
	nonMatching.Status.CurrentPodCliqueSetGenerationHash = &otherHash

	terminatingMatching := grovecorev1alpha1.PodCliqueScalingGroup{}
	terminatingMatching.Name = "t"
	terminatingMatching.Status.CurrentPodCliqueSetGenerationHash = &hash
	now := metav1.NewTime(time.Now())
	terminatingMatching.DeletionTimestamp = &now
	terminatingMatching.Finalizers = []string{"f"}

	tests := []struct {
		name string
		hash *string
		in   []grovecorev1alpha1.PodCliqueScalingGroup
		want int32
	}{
		{"nil hash → 0", nil, []grovecorev1alpha1.PodCliqueScalingGroup{matching}, 0},
		{"all matching", &hash, []grovecorev1alpha1.PodCliqueScalingGroup{matching, matching}, 2},
		{"mixed", &hash, []grovecorev1alpha1.PodCliqueScalingGroup{matching, nonMatching}, 1},
		{"terminating excluded", &hash, []grovecorev1alpha1.PodCliqueScalingGroup{matching, terminatingMatching}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, countUpdatedPCSGs(tt.hash, tt.in))
		})
	}
}

func TestFlattenNamesToSet(t *testing.T) {
	tests := []struct {
		name string
		in   map[int][]string
		want map[string]struct{}
	}{
		{"empty", map[int][]string{}, map[string]struct{}{}},
		{"single replica", map[int][]string{0: {"a", "b"}}, map[string]struct{}{"a": {}, "b": {}}},
		{"multi replica", map[int][]string{0: {"a", "b"}, 1: {"c"}}, map[string]struct{}{"a": {}, "b": {}, "c": {}}},
		{"duplicates collapse to single entry", map[int][]string{0: {"a"}, 1: {"a"}}, map[string]struct{}{"a": {}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, flattenNamesToSet(tt.in))
		})
	}
}

// pcsgName returns the FQN for a PCSG owned by `testPCSName` at the given replica index, using
// the same scheme that componentutils.GetExpectedPCSGFQNsPerPCSReplica produces.
func pcsgName(replicaIndex int) string {
	switch replicaIndex {
	case 0:
		return "test-pcs-0-compute"
	case 1:
		return "test-pcs-1-compute"
	default:
		return ""
	}
}
