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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

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
			name:                   "Scale to exactly minAvailable",
			minAvailable:           ptr.To(int32(2)),
			initialReplicas:        4,
			scaledReplicas:         2,
			expectedBasePodGang:    "test-pcs-0", // Contains replicas 0-1
			expectedScaledPodGangs: []string{
				// No scaled PodGangs when replicas == minAvailable
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
								Replicas:     &tt.scaledReplicas, // This simulates HPA scaling
								MinAvailable: tt.minAvailable,
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
					Replicas:     tt.scaledReplicas, // This is what HPA modifies
					MinAvailable: tt.minAvailable,
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
			expectedPodGangs, err := r.getExpectedScaledPodGangsForPCSG(sc, 0)
			require.NoError(t, err)

			// Verify scaled PodGangs
			actualScaledPodGangs := make([]string, 0, len(expectedPodGangs))
			for _, pg := range expectedPodGangs {
				actualScaledPodGangs = append(actualScaledPodGangs, pg.fqn)
			}
			assert.Equal(t, tt.expectedScaledPodGangs, actualScaledPodGangs,
				"Scaled PodGangs should match expected after scaling")

			// Test base PodGang logic - this should be independent of scaling
			basePodGangs, err := getExpectedBasePodGangForPCSReplicas(sc)
			require.NoError(t, err)
			require.Len(t, basePodGangs, 1, "Should have exactly one base PodGang")
			assert.Equal(t, tt.expectedBasePodGang, basePodGangs[0].fqn,
				"Base PodGang name should be correct and unchanged by scaling")

			// Verify base PodGang only contains replicas 0 to (minAvailable-1)
			minAvail := int32(1)
			if tt.minAvailable != nil {
				minAvail = *tt.minAvailable
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
								Replicas:     &tt.pcsgTemplateReplicas,
								MinAvailable: tt.pcsgMinAvailable,
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
			assert.Equal(t, len(tt.expectedPendingPodsPerPodGang), len(sc.expectedPodGangs))

			// Verify pending pods per PodGang and total number of pending pods
			var totalNumPendingPods int
			pendingPodGangNames := sc.getPodGangNamesPendingCreation()
			for i, podGang := range sc.expectedPodGangs {
				isPodGangPendingCreation := slices.Contains(pendingPodGangNames, podGang.fqn)
				assert.True(t, isPodGangPendingCreation)
				numPendingPods := r.getPodsPendingCreationOrAssociation(sc, podGang)
				assert.Equal(t, tt.expectedPendingPodsPerPodGang[i], numPendingPods)
				totalNumPendingPods += numPendingPods
			}
			assert.Equal(t, tt.totalNumPendingPods, totalNumPendingPods)
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

// TestComputeExpectedPodGangsWithTopologyConstraints tests topology constraint handling
func TestComputeExpectedPodGangsWithTopologyConstraints(t *testing.T) {
	tests := []struct {
		name                        string
		pcsTopologyConstraint       *grovecorev1alpha1.TopologyConstraint
		pcsgTopologyConstraint      *grovecorev1alpha1.TopologyConstraint
		pclqTopologyConstraint      *grovecorev1alpha1.TopologyConstraint
		expectPGPackConstraintSet   bool
		expectPCSGPackConstraintSet bool
		expectPCLQPackConstraintSet bool
	}{
		{
			name: "PCS level topology constraint",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				PackDomain: "rack",
			},
			expectPGPackConstraintSet:   true,
			expectPCSGPackConstraintSet: false,
			expectPCLQPackConstraintSet: false,
		},
		{
			name: "PCSG level topology constraint",
			pcsgTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				PackDomain: "host",
			},
			expectPGPackConstraintSet:   false,
			expectPCSGPackConstraintSet: true,
			expectPCLQPackConstraintSet: false,
		},
		{
			name: "PCLQ level topology constraint",
			pclqTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				PackDomain: "host",
			},
			expectPGPackConstraintSet:   false,
			expectPCSGPackConstraintSet: false,
			expectPCLQPackConstraintSet: true,
		},
		{
			name:                        "No topology constraints",
			expectPGPackConstraintSet:   false,
			expectPCSGPackConstraintSet: false,
			expectPCLQPackConstraintSet: false,
		},
		{
			name: "All levels with topology constraints",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				PackDomain: "rack",
			},
			pcsgTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				PackDomain: "host",
			},
			pclqTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				PackDomain: "host",
			},
			expectPGPackConstraintSet:   true,
			expectPCSGPackConstraintSet: true,
			expectPCLQPackConstraintSet: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Setup
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TopologyConstraint: test.pcsTopologyConstraint,
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name:               "worker",
								TopologyConstraint: test.pclqTopologyConstraint,
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas:     2,
									MinAvailable: ptr.To(int32(1)),
								},
							},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:               "sg",
								Replicas:           ptr.To(int32(2)),
								MinAvailable:       ptr.To(int32(1)),
								CliqueNames:        []string{"worker"},
								TopologyConstraint: test.pcsgTopologyConstraint,
							},
						},
					},
				},
			}

			fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
			r := &_resource{client: fakeClient}
			sc := &syncContext{
				ctx:           context.Background(),
				pcs:           pcs,
				logger:        ctrllogger.FromContext(context.Background()),
				existingPCSGs: []grovecorev1alpha1.PodCliqueScalingGroup{},
				existingPCLQs: []grovecorev1alpha1.PodClique{},
				tasEnabled:    true,
				topologyLevels: []grovecorev1alpha1.TopologyLevel{
					{Domain: "rack", Key: "topology.kubernetes.io/rack"},
					{Domain: "host", Key: "kubernetes.io/hostname"},
				},
			}

			// Test
			err := r.computeExpectedPodGangs(sc)
			require.NoError(t, err)
			require.NotEmpty(t, sc.expectedPodGangs)

			// Find base PodGang (name format: test-pcs-0)
			var basePodGang *podGangInfo
			for _, pg := range sc.expectedPodGangs {
				if pg.fqn == "test-pcs-0" {
					basePodGang = pg
					break
				}
			}
			require.NotNil(t, basePodGang, "base PodGang should exist")

			// Check PCS-level topology constraint
			if test.expectPGPackConstraintSet {
				require.NotNil(t, basePodGang.topologyConstraint, "topology constraint should exist")
				require.NotNil(t, basePodGang.topologyConstraint.PackConstraint, "pack constraint should exist")
				assert.NotNil(t, basePodGang.topologyConstraint.PackConstraint.Required, "required pack constraint should be set")
			} else {
				if basePodGang.topologyConstraint != nil && basePodGang.topologyConstraint.PackConstraint != nil {
					assert.Nil(t, basePodGang.topologyConstraint.PackConstraint.Required, "required pack constraint should not be set")
				}
			}

			// Check PCSG-level topology constraints
			if test.expectPCSGPackConstraintSet {
				require.NotEmpty(t, basePodGang.pcsgTopologyConstraints, "PCSG topology constraints should exist")
				assert.NotNil(t, basePodGang.pcsgTopologyConstraints[0].TopologyConstraint, "PCSG topology constraint should exist")
				require.NotNil(t, basePodGang.pcsgTopologyConstraints[0].TopologyConstraint.PackConstraint, "PCSG pack constraint should exist")
				assert.NotNil(t, basePodGang.pcsgTopologyConstraints[0].TopologyConstraint.PackConstraint.Required, "PCSG required pack constraint should be set")
			} else {
				if len(basePodGang.pcsgTopologyConstraints) > 0 &&
					basePodGang.pcsgTopologyConstraints[0].TopologyConstraint != nil &&
					basePodGang.pcsgTopologyConstraints[0].TopologyConstraint.PackConstraint != nil {
					assert.Nil(t, basePodGang.pcsgTopologyConstraints[0].TopologyConstraint.PackConstraint.Required, "PCSG required pack constraint should not be set")
				}
			}

			// Check PCLQ-level topology constraints
			require.NotEmpty(t, basePodGang.pclqs, "base PodGang should have PodCliques")
			pclqInfo := basePodGang.pclqs[0]
			if test.expectPCLQPackConstraintSet {
				require.NotNil(t, pclqInfo.packConstraint, "PCLQ pack constraint should exist")
				require.NotNil(t, pclqInfo.packConstraint.PackConstraint, "PCLQ pack constraint should exist")
				assert.NotNil(t, pclqInfo.packConstraint.PackConstraint.Required, "PCLQ required pack constraint should be set")
			} else {
				if pclqInfo.packConstraint != nil && pclqInfo.packConstraint.PackConstraint != nil {
					assert.Nil(t, pclqInfo.packConstraint.PackConstraint.Required, "PCLQ required pack constraint should not be set")
				}
			}
		})
	}
}
