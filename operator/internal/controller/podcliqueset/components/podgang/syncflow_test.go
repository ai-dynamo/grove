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

package podgang

import (
	"context"
	"slices"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrllogger "sigs.k8s.io/controller-runtime/pkg/log"
)

// This is a critical test for HPA scaling logic:
// - Tests how PodGangs split when scaling: base vs scaled PodGangs
// - Verifies minAvailable logic works correctly during scale up/down
// - Ensures the first minAvailable replicas stay gang-scheduled together
func TestMinAvailableWithHPAScaling(t *testing.T) {
	tests := []struct {
		name                   string
		minAvailable           *int32
		initialReplicas        int32
		scaledReplicas         int32
		expectedBasePodGang    string
		expectedScaledPodGangs []string
	}{
		{
			name:                "Scale up from 2 to 4 with minAvailable=1",
			minAvailable:        ptr.To(int32(1)),
			initialReplicas:     2,
			scaledReplicas:      4,
			expectedBasePodGang: "test-pcs-0", // Contains replicas 0 to (minAvailable-1) = 0
			expectedScaledPodGangs: []string{
				"test-pcs-0-test-sg-0", // scaled PodGang 0 (scaling group replica 1)
				"test-pcs-0-test-sg-1", // scaled PodGang 1 (scaling group replica 2)
				"test-pcs-0-test-sg-2", // scaled PodGang 2 (scaling group replica 3)
			},
		},
		{
			name:                "Scale up from 3 to 6 with minAvailable=2",
			minAvailable:        ptr.To(int32(2)),
			initialReplicas:     3,
			scaledReplicas:      6,
			expectedBasePodGang: "test-pcs-0", // Contains replicas 0-1
			expectedScaledPodGangs: []string{
				"test-pcs-0-test-sg-0", // scaled PodGang 0 (scaling group replica 2)
				"test-pcs-0-test-sg-1", // scaled PodGang 1 (scaling group replica 3)
				"test-pcs-0-test-sg-2", // scaled PodGang 2 (scaling group replica 4)
				"test-pcs-0-test-sg-3", // scaled PodGang 3 (scaling group replica 5)
			},
		},
		{
			name:                "Scale down from 5 to 3 with minAvailable=1",
			minAvailable:        ptr.To(int32(1)),
			initialReplicas:     5,
			scaledReplicas:      3,
			expectedBasePodGang: "test-pcs-0", // Contains replica 0 (unchanged)
			expectedScaledPodGangs: []string{
				"test-pcs-0-test-sg-0", // scaled PodGang 0 (scaling group replica 1)
				"test-pcs-0-test-sg-1", // scaled PodGang 1 (scaling group replica 2)
				// scaling group replicas 3-4 should be deleted
			},
		},
		{
			name:                "Scale to exactly minAvailable",
			minAvailable:        ptr.To(int32(2)),
			initialReplicas:     4,
			scaledReplicas:      2,
			expectedBasePodGang: "test-pcs-0", // Contains replicas 0-1
			expectedScaledPodGangs: []string{
				// No scaled PodGangs when replicas == minAvailable
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create test PodCliqueSet
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       "test-uid-123",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "test-clique",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas:     2,
									MinAvailable: ptr.To(int32(2)),
								},
							},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "test-sg",
								Replicas:     &test.scaledReplicas, // This simulates HPA scaling
								MinAvailable: test.minAvailable,
								CliqueNames:  []string{"test-clique"},
							},
						},
					},
				},
			}

			// Create test PodCliqueScalingGroup (simulates what HPA would create)
			pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-test-sg",
					Namespace: "default",
					Labels: map[string]string{
						"app.kubernetes.io/managed-by":        "grove-operator",
						"app.kubernetes.io/part-of":           "test-pcs",
						"grove.io/podcliqueset-replica-index": "0",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "grove.io/v1alpha1",
							Kind:       "PodCliqueSet",
							Name:       "test-pcs",
							UID:        "test-uid-123",
							Controller: ptr.To(true),
						},
					},
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:     test.scaledReplicas, // This is what HPA modifies
					MinAvailable: test.minAvailable,
					CliqueNames:  []string{"test-clique"},
				},
			}

			// Create fake client with both PCS and PCSG using testutils
			fakeClient := testutils.NewTestClientBuilder().
				WithObjects(pcs, pcsg).
				Build()

			// Test the PodGang creation logic
			r := &_resource{client: fakeClient}
			sc := &syncContext{
				ctx: context.Background(),
				pcs: pcs,
			}

			// Test scaled PodGang creation - this should read the scaled PCSG
			expectedPodGangs, err := r.buildExpectedScaledPodGangsForPCSG(sc, 0)
			require.NoError(t, err)

			// Verify scaled PodGangs
			actualScaledPodGangs := make([]string, 0, len(expectedPodGangs))
			for _, pg := range expectedPodGangs {
				actualScaledPodGangs = append(actualScaledPodGangs, pg.fqn)
			}
			assert.Equal(t, test.expectedScaledPodGangs, actualScaledPodGangs,
				"Scaled PodGangs should match expected after scaling")

			// Test base PodGang logic - this should be independent of scaling
			basePodGangs, err := buildExpectedBasePodGangForPCSReplicas(sc)
			require.NoError(t, err)
			require.Len(t, basePodGangs, 1, "Should have exactly one base PodGang")
			assert.Equal(t, test.expectedBasePodGang, basePodGangs[0].fqn,
				"Base PodGang name should be correct and unchanged by scaling")

			// Verify base PodGang only contains replicas 0 to (minAvailable-1)
			minAvail := int32(1)
			if test.minAvailable != nil {
				minAvail = *test.minAvailable
			}
			expectedBasePodCliques := int(minAvail)
			actualBasePodCliques := len(basePodGangs[0].pclqs)
			assert.Equal(t, expectedBasePodCliques, actualBasePodCliques,
				"Base PodGang should only contain PodCliques for replicas 0 to (minAvailable-1)")
		})
	}
}

// This test checks the accounting of the number of pending pods before creating a PodGang
func TestGetPodsPendingCreation(t *testing.T) {
	tests := []struct {
		name                          string
		pcsgMinAvailable              *int32
		pcsgTemplateReplicas          int32
		expectedPendingPodsPerPodGang []int
		totalNumPendingPods           int
	}{
		{
			name:                          "PCSG startup replicas=2, minAvailable=1",
			pcsgMinAvailable:              ptr.To(int32(1)),
			pcsgTemplateReplicas:          2,
			totalNumPendingPods:           13,
			expectedPendingPodsPerPodGang: []int{8, 5},
		},
		{
			name:                          "PCSG startup replicas=3, minAvailable=1",
			pcsgMinAvailable:              ptr.To(int32(1)),
			pcsgTemplateReplicas:          3,
			totalNumPendingPods:           18,
			expectedPendingPodsPerPodGang: []int{8, 5, 5},
		},
		{
			name:                          "PCSG startup replicas=3, minAvailable=2",
			pcsgMinAvailable:              ptr.To(int32(2)),
			pcsgTemplateReplicas:          3,
			totalNumPendingPods:           18,
			expectedPendingPodsPerPodGang: []int{13, 5},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create test PodCliqueSet
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       "test-uid-123",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "frontend",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas:     3,
									MinAvailable: ptr.To(int32(1)),
								},
							},
							{
								Name: "prefill-leader",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas:     1,
									MinAvailable: ptr.To(int32(1)),
								},
							},
							{
								Name: "prefill-worker",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas:     4,
									MinAvailable: ptr.To(int32(3)),
								},
							},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "prefill",
								Replicas:     &test.pcsgTemplateReplicas,
								MinAvailable: test.pcsgMinAvailable,
								CliqueNames:  []string{"prefill-leader", "prefill-worker"},
							},
						},
					},
				},
			}

			// Create fake client with both PCS and PCSG using testutils
			fakeClient := testutils.NewTestClientBuilder().
				WithObjects(pcs).
				Build()

			// Setup test
			r := &_resource{client: fakeClient}
			ctx := context.Background()
			logger := ctrllogger.FromContext(ctx).WithName("grove-test")

			// Prepare sync context
			sc, err := r.prepareSyncFlow(ctx, logger, pcs)
			require.NoError(t, err)

			// Validate the number of expected PodGangs
			assert.Equal(t, len(test.expectedPendingPodsPerPodGang), len(sc.expectedPodGangs))

			// Verify pending pods per PodGang and total number of pending pods
			var totalNumPendingPods int
			pendingPodGangNames := sc.getPodGangNamesPendingCreation()
			for i, podGang := range sc.expectedPodGangs {
				isPodGangPendingCreation := slices.Contains(pendingPodGangNames, podGang.fqn)
				assert.True(t, isPodGangPendingCreation)
				numPendingPods := r.getPodsPendingCreationOrAssociation(sc, podGang)
				assert.Equal(t, test.expectedPendingPodsPerPodGang[i], numPendingPods)
				totalNumPendingPods += numPendingPods
			}
			assert.Equal(t, test.totalNumPendingPods, totalNumPendingPods)
		})
	}
}

// TestComputeExpectedPodGangs tests the computeExpectedPodGangs function
func TestComputeExpectedPodGangs(t *testing.T) {
	tests := []struct {
		name                      string
		pcsReplicas               int32
		pclqs                     []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs               []grovecorev1alpha1.PodCliqueScalingGroupConfig
		expectedNumPodGangs       int
		expectedBasePodGangNames  []string
		expectedScaledPodGangFQNs []string
	}{
		{
			name:        "Simple PCS with standalone PCLQs only",
			pcsReplicas: 2,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "worker",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     3,
						MinAvailable: ptr.To(int32(2)),
					},
				},
			},
			pcsgConfigs:               nil,
			expectedNumPodGangs:       2,
			expectedBasePodGangNames:  []string{"test-pcs-0", "test-pcs-1"},
			expectedScaledPodGangFQNs: []string{},
		},
		{
			name:        "PCS with PCSG having minAvailable=1",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "sg-worker",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     2,
						MinAvailable: ptr.To(int32(2)),
					},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "scaling-group",
					Replicas:     ptr.To(int32(3)),
					MinAvailable: ptr.To(int32(1)),
					CliqueNames:  []string{"sg-worker"},
				},
			},
			expectedNumPodGangs:       3,
			expectedBasePodGangNames:  []string{"test-pcs-0"},
			expectedScaledPodGangFQNs: []string{"test-pcs-0-scaling-group-0", "test-pcs-0-scaling-group-1"},
		},
		{
			name:        "PCS with mixed standalone PCLQ and PCSG",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "standalone",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     2,
						MinAvailable: ptr.To(int32(1)),
					},
				},
				{
					Name: "scalable",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     3,
						MinAvailable: ptr.To(int32(2)),
					},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "sg",
					Replicas:     ptr.To(int32(4)),
					MinAvailable: ptr.To(int32(2)),
					CliqueNames:  []string{"scalable"},
				},
			},
			expectedNumPodGangs:       3,
			expectedBasePodGangNames:  []string{"test-pcs-0"},
			expectedScaledPodGangFQNs: []string{"test-pcs-0-sg-0", "test-pcs-0-sg-1"},
		},
		{
			name:        "Multiple PCS replicas with PCSG",
			pcsReplicas: 2,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "worker",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     2,
						MinAvailable: ptr.To(int32(1)),
					},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "worker-sg",
					Replicas:     ptr.To(int32(2)),
					MinAvailable: ptr.To(int32(1)),
					CliqueNames:  []string{"worker"},
				},
			},
			expectedNumPodGangs:      4,
			expectedBasePodGangNames: []string{"test-pcs-0", "test-pcs-1"},
			expectedScaledPodGangFQNs: []string{
				"test-pcs-0-worker-sg-0",
				"test-pcs-1-worker-sg-0",
			},
		},
		{
			name:        "PCSG with minAvailable equals replicas",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "worker",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     2,
						MinAvailable: ptr.To(int32(2)),
					},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:         "sg",
					Replicas:     ptr.To(int32(2)),
					MinAvailable: ptr.To(int32(2)),
					CliqueNames:  []string{"worker"},
				},
			},
			expectedNumPodGangs:       1,
			expectedBasePodGangNames:  []string{"test-pcs-0"},
			expectedScaledPodGangFQNs: []string{},
		},
		{
			name:        "Multiple PCSGs in one PCS replica",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker-a", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(2))}},
				{Name: "worker-b", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(2))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg-a", Replicas: ptr.To(int32(3)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"worker-a"}},
				{Name: "sg-b", Replicas: ptr.To(int32(2)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"worker-b"}},
			},
			expectedNumPodGangs:       4,
			expectedBasePodGangNames:  []string{"test-pcs-0"},
			expectedScaledPodGangFQNs: []string{"test-pcs-0-sg-a-0", "test-pcs-0-sg-a-1", "test-pcs-0-sg-b-0"},
		},
		{
			name:        "Multiple cliques in one PCSG",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(2))}},
				{Name: "helper", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg", Replicas: ptr.To(int32(3)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"worker", "helper"}},
			},
			expectedNumPodGangs:       3,
			expectedBasePodGangNames:  []string{"test-pcs-0"},
			expectedScaledPodGangFQNs: []string{"test-pcs-0-sg-0", "test-pcs-0-sg-1"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Setup
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       "test-uid-123",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: test.pcsReplicas,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques:                      test.pclqs,
						PodCliqueScalingGroupConfigs: test.pcsgConfigs,
					},
				},
			}
			fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
			r := &_resource{client: fakeClient}
			sc := &syncContext{
				ctx:            context.Background(),
				pcs:            pcs,
				logger:         ctrllogger.FromContext(context.Background()),
				existingPCSGs:  []grovecorev1alpha1.PodCliqueScalingGroup{},
				existingPCLQs:  []grovecorev1alpha1.PodClique{},
				tasEnabled:     false,
				topologyLevels: nil,
			}

			// Test
			err := r.computeExpectedPodGangs(sc)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, test.expectedNumPodGangs, len(sc.expectedPodGangs))

			// Verify base PodGang names
			var basePodGangNames []string
			var scaledPodGangNames []string
			for _, pg := range sc.expectedPodGangs {
				if slices.Contains(test.expectedBasePodGangNames, pg.fqn) {
					basePodGangNames = append(basePodGangNames, pg.fqn)
				} else {
					scaledPodGangNames = append(scaledPodGangNames, pg.fqn)
				}
			}
			assert.ElementsMatch(t, test.expectedBasePodGangNames, basePodGangNames)
			assert.ElementsMatch(t, test.expectedScaledPodGangFQNs, scaledPodGangNames)
		})
	}
}

type expectedPodGangTopologyConstraints struct {
	fqn             string
	topologyLevel   *grovecorev1alpha1.TopologyLevel
	pclqConstraints map[string]grovecorev1alpha1.TopologyLevel
	pcsgConstraints map[string]grovecorev1alpha1.TopologyLevel
}

// TestComputeExpectedPodGangsWithTopologyConstraints tests computeExpectedPodGangs with topology constraints.
// The focus is on verifying that the correct topology constraints are applied to PodGangs.
// Different combinations of PCS-level, PCLQ-level, and PCSG-level topology constraints are tested.
func TestComputeExpectedPodGangsWithTopologyConstraints(t *testing.T) {
	var (
		topologyLevelZone = grovecorev1alpha1.TopologyLevel{Domain: "zone", Key: "topology.kubernetes.io/zone"}
		topologyLevelRack = grovecorev1alpha1.TopologyLevel{Domain: "rack", Key: "topology.kubernetes.io/rack"}
		topologyLevelHost = grovecorev1alpha1.TopologyLevel{Domain: "host", Key: "kubernetes.io/hostname"}
	)
	clusterTopologyLevels := []grovecorev1alpha1.TopologyLevel{
		topologyLevelZone,
		topologyLevelRack,
		topologyLevelHost,
	}
	tests := []struct {
		name                               string
		tasEnabled                         bool
		pcsTopologyLevel                   *grovecorev1alpha1.TopologyLevel
		pclqTemplateSpecs                  []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs                        []grovecorev1alpha1.PodCliqueScalingGroupConfig
		expectedNumPodGangs                int
		expectedPodGangTopologyConstraints []expectedPodGangTopologyConstraints
	}{
		{
			name:       "PCS with a single standalone PCLQ where no topology constraints are set",
			tasEnabled: true,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "worker",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     3,
						MinAvailable: ptr.To(int32(2)),
					},
				},
			},
			expectedNumPodGangs: 1,
		},
		{
			name:             "PCS with single standalone PCLQ where topology constraints are set at PCS only",
			tasEnabled:       true,
			pcsTopologyLevel: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "worker",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     3,
						MinAvailable: ptr.To(int32(2)),
					},
				},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:           "test-pcs-0",
					topologyLevel: &grovecorev1alpha1.TopologyLevel{Domain: "zone", Key: "topology.kubernetes.io/zone"},
				},
			},
		},
		{
			name:       "PCS with single standalone PCLQ where topology constraints are set for one of the PCLQs",
			tasEnabled: true,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "router",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     3,
						MinAvailable: ptr.To(int32(2)),
					},
				},
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     2,
						MinAvailable: ptr.To(int32(1)),
					},
				},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:           "test-pcs-0",
					topologyLevel: nil,
					pclqConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-worker": topologyLevelHost,
					},
				},
			},
		},
		{
			name:             "PCS with single standalone PCLQs where topology constraints are set at all levels",
			tasEnabled:       true,
			pcsTopologyLevel: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "router",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "zone"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     3,
						MinAvailable: ptr.To(int32(2)),
					},
				},
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     2,
						MinAvailable: ptr.To(int32(1)),
					},
				},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:           "test-pcs-0",
					topologyLevel: &topologyLevelZone,
					pclqConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-worker": topologyLevelHost,
						"test-pcs-0-router": topologyLevelZone,
					},
				},
			},
		},
		{
			name:             "PCS with PCSG where topology constraints are set at PCS and PCSG levels",
			tasEnabled:       true,
			pcsTopologyLevel: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "decode-leader",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     1,
						MinAvailable: ptr.To(int32(1)),
					},
				},
				{
					Name:               "decode-worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     5,
						MinAvailable: ptr.To(int32(1)),
					},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "scaling-group",
					Replicas:           ptr.To(int32(2)),
					MinAvailable:       ptr.To(int32(1)),
					CliqueNames:        []string{"decode-leader", "decode-worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "rack"},
				},
			},
			expectedNumPodGangs: 2,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:           "test-pcs-0",
					topologyLevel: &topologyLevelZone,
					pclqConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-scaling-group-0-decode-leader": topologyLevelHost,
						"test-pcs-0-scaling-group-0-decode-worker": topologyLevelHost,
					},
					pcsgConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-scaling-group-0": topologyLevelRack,
					},
				},
				{
					fqn:           "test-pcs-0-scaling-group-0",
					topologyLevel: &topologyLevelRack,
					pclqConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-scaling-group-1-decode-leader": topologyLevelHost,
						"test-pcs-0-scaling-group-1-decode-worker": topologyLevelHost,
					},
				},
			},
		},
		{
			name:             "PCS with standalone PCLQ and PCSG where topology constraints are set at all levels",
			tasEnabled:       true,
			pcsTopologyLevel: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "router",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "zone"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     1,
						MinAvailable: ptr.To(int32(1)),
					},
				},
				{
					Name:               "decode-leader",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     1,
						MinAvailable: ptr.To(int32(1)),
					},
				},
				{
					Name:               "decode-worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     5,
						MinAvailable: ptr.To(int32(1)),
					},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "scaling-group",
					Replicas:           ptr.To(int32(2)),
					MinAvailable:       ptr.To(int32(1)),
					CliqueNames:        []string{"decode-leader", "decode-worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "rack"},
				},
			},
			expectedNumPodGangs: 2,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:           "test-pcs-0",
					topologyLevel: &topologyLevelZone,
					pclqConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-router":                        topologyLevelZone,
						"test-pcs-0-scaling-group-0-decode-leader": topologyLevelHost,
						"test-pcs-0-scaling-group-0-decode-worker": topologyLevelHost,
					},
					pcsgConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-scaling-group-0": topologyLevelRack,
					},
				},
				{
					fqn:           "test-pcs-0-scaling-group-0",
					topologyLevel: &topologyLevelRack,
					pclqConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-scaling-group-1-decode-leader": topologyLevelHost,
						"test-pcs-0-scaling-group-1-decode-worker": topologyLevelHost,
					},
				},
			},
		},
		{
			name:             "PCS with topology constraints set for PCLQ and PCSG but TAS is disabled",
			tasEnabled:       false,
			pcsTopologyLevel: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "router",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "zone"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     1,
						MinAvailable: ptr.To(int32(1)),
					},
				},
				{
					Name:               "decode-leader",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     1,
						MinAvailable: ptr.To(int32(1)),
					},
				},
				{
					Name:               "decode-worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     5,
						MinAvailable: ptr.To(int32(1)),
					},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "scaling-group",
					Replicas:           ptr.To(int32(2)),
					MinAvailable:       ptr.To(int32(1)),
					CliqueNames:        []string{"decode-leader", "decode-worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "rack"},
				},
			},
			expectedNumPodGangs:                2,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			var pcsTopologyConstraint *grovecorev1alpha1.TopologyConstraint
			if tc.pcsTopologyLevel != nil {
				pcsTopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					PackDomain: tc.pcsTopologyLevel.Domain,
				}
			}
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TopologyConstraint:           pcsTopologyConstraint,
						Cliques:                      tc.pclqTemplateSpecs,
						PodCliqueScalingGroupConfigs: tc.pcsgConfigs,
					},
				},
			}

			fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
			r := &_resource{client: fakeClient}
			sc := &syncContext{
				ctx:            context.Background(),
				pcs:            pcs,
				logger:         ctrllogger.FromContext(context.Background()),
				existingPCSGs:  []grovecorev1alpha1.PodCliqueScalingGroup{},
				existingPCLQs:  []grovecorev1alpha1.PodClique{},
				tasEnabled:     tc.tasEnabled,
				topologyLevels: clusterTopologyLevels,
			}

			// Test
			err := r.computeExpectedPodGangs(sc)
			// Assert
			require.NoError(t, err)

			basePodGangFQN := apicommon.GenerateBasePodGangName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0})
			computedBasePodGangs := lo.Filter(sc.expectedPodGangs, func(pg *podGangInfo, _ int) bool {
				return pg.fqn == basePodGangFQN
			})

			require.NotNil(t, computedBasePodGangs)
			require.Equal(t, len(computedBasePodGangs), 1)
			require.Equal(t, tc.expectedNumPodGangs, len(sc.expectedPodGangs))

			if !tc.tasEnabled {
				mustNotHaveAnyTopologyConstraints(t, sc.expectedPodGangs)
			} else {
				// Iterate over the expected pod gang topology constraints.
				for _, expectedPGConstraint := range tc.expectedPodGangTopologyConstraints {
					// find the computed pod gang
					computedPodGang, found := lo.Find(sc.expectedPodGangs, func(pg *podGangInfo) bool {
						return pg.fqn == expectedPGConstraint.fqn
					})
					require.True(t, found, "Expected PodGang %s not found", expectedPGConstraint.fqn)

					// verify pod gang topology constraint. This is the top level topology constraint.
					// For base pod gang it comes from PodCliqueSet.Spec.Template.TopologyConstraint.
					// For scaled pod gangs it comes from PodCliqueScalingGroup.Spec.TopologyConstraint.
					if expectedPGConstraint.topologyLevel == nil {
						assert.Nil(t, computedPodGang.topologyConstraint)
					} else {
						// assert pod gang topology constraint is set correctly
						assertRequiredTopologyConstraint(t, computedPodGang.topologyConstraint, expectedPGConstraint.topologyLevel.Key)
					}

					// verify pclq topology constraints
					for _, pclq := range computedPodGang.pclqs {
						expectedPCLQConstraint, exists := expectedPGConstraint.pclqConstraints[pclq.fqn]
						if !exists {
							assert.Nil(t, pclq.topologyConstraint)
						} else {
							// assert pclq topology constraint is set correctly
							assertRequiredTopologyConstraint(t, pclq.topologyConstraint, expectedPCLQConstraint.Key)
						}
					}

					// iterate over expected PCSG Constraints and verify computed pcsg topology constraints
					for pcsgFQN, expectedPCSGTC := range expectedPGConstraint.pcsgConstraints {
						actualPCSGTC, found := lo.Find(computedPodGang.pcsgTopologyConstraints, func(pcsgTC groveschedulerv1alpha1.TopologyConstraintGroupConfig) bool {
							return pcsgTC.Name == pcsgFQN
						})
						assert.True(t, found, "Expected PCSG topology constraint for %s not found", pcsgFQN)
						// assert pcsg topology constraint is set correctly
						assertRequiredTopologyConstraint(t, actualPCSGTC.TopologyConstraint, expectedPCSGTC.Key)
					}

					// iterate over computed PCSG topology constraints to ensure that expectations are properly defined.
					// This ensures that developer mistakes are caught when defining the topology constraint expectations.
					for _, actualPCSGTC := range computedPodGang.pcsgTopologyConstraints {
						_, exists := expectedPGConstraint.pcsgConstraints[actualPCSGTC.Name]
						if !exists {
							t.Errorf("Unexpected PCSG topology constraint for %s found in PodGang %s", actualPCSGTC.Name, computedPodGang.fqn)
						}
					}
				}
			}
		})
	}
}

func mustNotHaveAnyTopologyConstraints(t *testing.T, podGangs []*podGangInfo) {
	for _, pg := range podGangs {
		// assert no topology constraints are set.
		assert.Nil(t, pg.topologyConstraint)
		for _, pclq := range pg.pclqs {
			assert.Nil(t, pclq.topologyConstraint)
		}
		assert.Nil(t, pg.pcsgTopologyConstraints)
	}
}

func assertRequiredTopologyConstraint(t *testing.T, got *groveschedulerv1alpha1.TopologyConstraint, wantedKey string) {
	assert.NotNil(t, got)
	assert.NotNil(t, got.PackConstraint)
	assert.Nil(t, got.PackConstraint.Preferred)
	assert.NotNil(t, got.PackConstraint.Required)
	assert.Equal(t, wantedKey, *got.PackConstraint.Required)
}

// TestDeterminePCSGReplicas tests the determinePCSGReplicas method
func TestDeterminePCSGReplicas(t *testing.T) {
	tests := []struct {
		name             string
		pcsgFQN          string
		pcsgConfig       grovecorev1alpha1.PodCliqueScalingGroupConfig
		existingPCSGs    []grovecorev1alpha1.PodCliqueScalingGroup
		expectedReplicas int
	}{
		{
			name:    "Returns existing PCSG replicas when PCSG exists",
			pcsgFQN: "test-pcs-0-worker-sg",
			pcsgConfig: grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:         "worker-sg",
				Replicas:     ptr.To(int32(3)),
				MinAvailable: ptr.To(int32(1)),
				CliqueNames:  []string{"worker"},
			},
			existingPCSGs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-worker-sg",
						Namespace: "default",
					},
					Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
						Replicas:     5, // HPA scaled to 5
						MinAvailable: ptr.To(int32(1)),
						CliqueNames:  []string{"worker"},
					},
				},
			},
			expectedReplicas: 5, // Should use actual PCSG replicas
		},
		{
			name:    "Returns template replicas when PCSG does not exist",
			pcsgFQN: "test-pcs-0-worker-sg",
			pcsgConfig: grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:         "worker-sg",
				Replicas:     ptr.To(int32(3)),
				MinAvailable: ptr.To(int32(1)),
				CliqueNames:  []string{"worker"},
			},
			existingPCSGs:    []grovecorev1alpha1.PodCliqueScalingGroup{},
			expectedReplicas: 3, // Should use template replicas
		},
		{
			name:    "Finds the correct PCSG among multiple existing PCSGs and returns its replicas",
			pcsgFQN: "test-pcs-1-worker-sg",
			pcsgConfig: grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:         "worker-sg",
				Replicas:     ptr.To(int32(3)),
				MinAvailable: ptr.To(int32(1)),
				CliqueNames:  []string{"worker"},
			},
			existingPCSGs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-worker-sg",
						Namespace: "default",
					},
					Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
						Replicas:     10,
						MinAvailable: ptr.To(int32(1)),
						CliqueNames:  []string{"worker"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-1-worker-sg", // This one should match
						Namespace: "default",
					},
					Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
						Replicas:     7,
						MinAvailable: ptr.To(int32(1)),
						CliqueNames:  []string{"worker"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-other-sg",
						Namespace: "default",
					},
					Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
						Replicas:     15,
						MinAvailable: ptr.To(int32(2)),
						CliqueNames:  []string{"other"},
					},
				},
			},
			expectedReplicas: 7, // Should find and use the matching PCSG replicas
		},
		{
			name:    "Returns the template replicas when no existing PCSG matches",
			pcsgFQN: "test-pcs-2-worker-sg",
			pcsgConfig: grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:         "worker-sg",
				Replicas:     ptr.To(int32(4)),
				MinAvailable: ptr.To(int32(2)),
				CliqueNames:  []string{"worker"},
			},
			existingPCSGs: []grovecorev1alpha1.PodCliqueScalingGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-0-worker-sg",
						Namespace: "default",
					},
					Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
						Replicas:     10,
						MinAvailable: ptr.To(int32(2)),
						CliqueNames:  []string{"worker"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs-1-worker-sg",
						Namespace: "default",
					},
					Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
						Replicas:     8,
						MinAvailable: ptr.To(int32(2)),
						CliqueNames:  []string{"worker"},
					},
				},
			},
			expectedReplicas: 4, // Should use template replicas as none match
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sc := &syncContext{
				existingPCSGs: test.existingPCSGs,
			}

			actualReplicas := sc.determinePCSGReplicas(test.pcsgFQN, test.pcsgConfig)
			assert.Equal(t, test.expectedReplicas, actualReplicas,
				"determinePCSGReplicas should return expected replica count")
		})
	}
}
