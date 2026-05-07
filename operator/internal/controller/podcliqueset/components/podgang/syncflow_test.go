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
	"errors"
	"slices"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// TestVerifyAllPodsCreated tests verifyAllPodsCreated with minimal sc + podGangInfo (no PCS/prepareSyncFlow).
// It covers both the PCLQ existence check and getPodsPendingCreationOrAssociation logic (Replicas and podgang label).
func TestVerifyAllPodsCreated(t *testing.T) {
	makePod := func(name string, podGangLabel string) v1.Pod {
		pod := v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
		if podGangLabel != "" {
			pod.Labels = map[string]string{apicommon.LabelPodGang: podGangLabel}
		}
		return pod
	}
	makePCLQ := func(name string, replicas, minAvailable int32) grovecorev1alpha1.PodClique {
		return grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       grovecorev1alpha1.PodCliqueSpec{Replicas: replicas, MinAvailable: ptr.To(minAvailable)},
		}
	}

	tests := []struct {
		name          string
		existingPods  map[string][]v1.Pod
		existingPCLQs []grovecorev1alpha1.PodClique
		podGang       *podGangInfo
		wantRequeue   bool
	}{
		{
			name:          "requeue when not all constituent PCLQs exist yet",
			existingPods:  map[string][]v1.Pod{"pclq-a": {makePod("a1", "pg-1")}},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a", 1, 1)},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 1, minAvailable: 1}, {fqn: "pclq-b", replicas: 1, minAvailable: 1}}},
			wantRequeue:   true,
		},
		{
			name: "requeue when PCLQ has fewer pods than Replicas (even if >= MinAvailable)",
			existingPods: map[string][]v1.Pod{
				"pclq-a": {makePod("a1", "pg-1"), makePod("a2", "pg-1")}, // 2 pods, Replicas=5, MinAvailable=2
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a", 5, 2)},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 5, minAvailable: 2}}},
			wantRequeue:   true, // Still pending: 5-2=3 pods to create
		},
		{
			name: "requeue when Pod missing podgang label",
			existingPods: map[string][]v1.Pod{
				"pclq-a": {makePod("a1", ""), makePod("a2", "pg-1")}, // a1 missing label
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a", 2, 1)},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 2, minAvailable: 1}}},
			wantRequeue:   true, // a1 needs association
		},
		{
			name: "requeue when Pod has wrong podgang label",
			existingPods: map[string][]v1.Pod{
				"pclq-a": {makePod("a1", "pg-wrong"), makePod("a2", "pg-1")},
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a", 2, 1)},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 2, minAvailable: 1}}},
			wantRequeue:   true, // a1 has wrong label
		},
		{
			name: "success when all Replicas created and all pods have correct podgang label",
			existingPods: map[string][]v1.Pod{
				"pclq-a": {makePod("a1", "pg-1"), makePod("a2", "pg-1"), makePod("a3", "pg-1"), makePod("a4", "pg-1"), makePod("a5", "pg-1")},
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a", 5, 2)},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 5, minAvailable: 2}}},
			wantRequeue:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := &syncContext{
				logger:           ctrllogger.FromContext(t.Context()).WithName("test"),
				existingPCLQPods: tt.existingPods,
				existingPCLQs:    tt.existingPCLQs,
			}
			r := &_resource{}
			err := r.verifyAllPodsCreated(sc, tt.podGang)
			if tt.wantRequeue {
				require.Error(t, err)
				var groveErr *groveerr.GroveError
				require.True(t, errors.As(err, &groveErr))
				assert.Equal(t, groveerr.ErrCodeRequeueAfter, groveErr.Code)
			} else {
				require.NoError(t, err)
			}
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
			ctx := t.Context()
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

// TestCreateOrUpdatePodGangs tests the createOrUpdatePodGangs flow.
func TestCreateOrUpdatePodGangs(t *testing.T) {
	ns := "default"
	pcsName := "test-pcs"
	pcsLabels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)
	pgName := "test-pcs-0"
	pclqName := "test-pcs-0-worker"

	makePCS := func() *grovecorev1alpha1.PodCliqueSet {
		return &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: ns, UID: "pcs-uid"},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 1,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
						{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(1))}},
					},
				},
			},
		}
	}
	makePCLQ := func() *grovecorev1alpha1.PodClique {
		return &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name: pclqName, Namespace: ns, UID: "pclq-uid",
				Labels:          pcsLabels,
				OwnerReferences: []metav1.OwnerReference{{Name: pcsName, UID: "pcs-uid", Controller: ptr.To(true)}},
			},
			Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(1))},
		}
	}
	makePod := func(name, podGangLabel string) *v1.Pod {
		labels := lo.Assign(pcsLabels)
		if podGangLabel != "" {
			labels[apicommon.LabelPodGang] = podGangLabel
		}
		return &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns,
				Labels:          labels,
				OwnerReferences: []metav1.OwnerReference{{Name: pclqName, UID: "pclq-uid", Controller: ptr.To(true)}},
			},
		}
	}
	makeExistingPodGang := func() *groveschedulerv1alpha1.PodGang {
		pgLabels := lo.Assign(pcsLabels, map[string]string{apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang})
		return &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name: pgName, Namespace: ns,
				Labels:          pgLabels,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "grove.io/v1alpha1", Kind: "PodCliqueSet", Name: pcsName, UID: "pcs-uid", Controller: ptr.To(true)}},
			},
			Spec: groveschedulerv1alpha1.PodGangSpec{},
		}
	}

	t.Run("new PodGang, PCLQ exists but no pods yet - creates PodGang, records requeue error", func(t *testing.T) {
		ctx := t.Context()
		pcs := makePCS()
		pclq := makePCLQ()
		// PCLQ exists (created by PodCliqueSet reconciler before PodGang) but PodClique controller
		// hasn't created pods yet — this is the typical "not ready" scenario.
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(pcs, pclq).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10)}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		require.Len(t, sc.expectedPodGangs, 1)
		require.Empty(t, sc.existingPodGangs, "PodGang should not exist yet")

		result := r.createOrUpdatePodGangs(ctx, sc)
		// createOrUpdatePodGang succeeds, but verifyAllPodsCreated fails (no pods) → requeue error recorded, loop continues
		require.True(t, result.hasErrors(), "should have requeue error because pods don't exist yet")
		require.Len(t, result.createdPodGangNames, 1, "PodGang should still be recorded as created")
		assert.Equal(t, pgName, result.createdPodGangNames[0])

		// Verify PodGang was created in the cluster
		pgAfter := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, pgAfter))
		assert.Equal(t, pcsName, pgAfter.OwnerReferences[0].Name)

		// Initialized=True should NOT have been set (verification failed)
		var groveErr *groveerr.GroveError
		require.True(t, errors.As(result.errs[0], &groveErr))
		assert.Equal(t, groveerr.ErrCodeRequeueAfter, groveErr.Code)
	})

	t.Run("new PodGang, pods exist but missing PodGang label - creates PodGang, records requeue error", func(t *testing.T) {
		ctx := t.Context()
		pcs := makePCS()
		pclq := makePCLQ()
		// Pods exist but have no grove.io/podgang label
		pod1 := makePod("worker-0", "")
		pod2 := makePod("worker-1", "")
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(pcs, pclq, pod1, pod2).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10)}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		require.Empty(t, sc.existingPodGangs)

		result := r.createOrUpdatePodGangs(ctx, sc)
		// verifyAllPodsCreated fails because pods don't have the PodGang label → error recorded, loop continues
		require.True(t, result.hasErrors(), "should have requeue error because pods are missing PodGang label")
		require.Len(t, result.createdPodGangNames, 1)

		var groveErr *groveerr.GroveError
		require.True(t, errors.As(result.errs[0], &groveErr))
		assert.Equal(t, groveerr.ErrCodeRequeueAfter, groveErr.Code)
	})

	t.Run("new PodGang, all pods ready and labeled - creates PodGang, sets Initialized=True", func(t *testing.T) {
		ctx := t.Context()
		pcs := makePCS()
		pclq := makePCLQ()
		pod1 := makePod("worker-0", pgName)
		pod2 := makePod("worker-1", pgName)
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(pcs, pclq, pod1, pod2).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10)}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		require.Empty(t, sc.existingPodGangs)

		result := r.createOrUpdatePodGangs(ctx, sc)
		require.False(t, result.hasErrors(), "should succeed: %v", result.errs)
		require.Len(t, result.createdPodGangNames, 1)
		assert.Equal(t, pgName, result.createdPodGangNames[0])

		// Verify PodGang exists and Initialized=True
		pgAfter := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, pgAfter))
		require.Len(t, pgAfter.Status.Conditions, 1)
		assert.Equal(t, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized), pgAfter.Status.Conditions[0].Type)
		assert.Equal(t, metav1.ConditionTrue, pgAfter.Status.Conditions[0].Status)
	})

	t.Run("existing PodGang, all pods ready - updates PodGang, sets Initialized=True", func(t *testing.T) {
		ctx := t.Context()
		pcs := makePCS()
		pclq := makePCLQ()
		pg := makeExistingPodGang()
		pod1 := makePod("worker-0", pgName)
		pod2 := makePod("worker-1", pgName)
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(pcs, pclq, pg, pod1, pod2).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10)}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		assert.True(t, sc.isExistingPodGang(pgName))

		result := r.createOrUpdatePodGangs(ctx, sc)
		require.False(t, result.hasErrors(), "should succeed: %v", result.errs)
		// Existing PodGang → not recorded as new creation
		assert.Empty(t, result.createdPodGangNames, "should not record creation for existing PodGang")

		// Verify PodGang was updated and Initialized=True
		pgAfter := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, pgAfter))
		require.Len(t, pgAfter.Spec.PodGroups, 1)
		assert.Equal(t, pclqName, pgAfter.Spec.PodGroups[0].Name)
		assert.Len(t, pgAfter.Spec.PodGroups[0].PodReferences, 2)
		require.NotEmpty(t, pgAfter.Status.Conditions)
		assert.Equal(t, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized), pgAfter.Status.Conditions[0].Type)
		assert.Equal(t, metav1.ConditionTrue, pgAfter.Status.Conditions[0].Status)
	})

	t.Run("existing initialized PodGang, pods replaced - updates PodReferences to replacement pods", func(t *testing.T) {
		ctx := t.Context()
		pcs := makePCS()
		pclq := makePCLQ()
		pg := makeExistingPodGang()
		// PodGang already has Initialized=True and old PodReferences (worker-0, worker-1)
		pg.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{
			{
				Name: pclqName,
				PodReferences: []groveschedulerv1alpha1.NamespacedName{
					{Namespace: ns, Name: "worker-0"},
					{Namespace: ns, Name: "worker-1"},
				},
				MinReplicas: 1,
			},
		}
		pg.Status.Conditions = []metav1.Condition{
			{
				Type:   string(groveschedulerv1alpha1.PodGangConditionTypeInitialized),
				Status: metav1.ConditionTrue,
				Reason: groveschedulerv1alpha1.ConditionReasonPodGangPodsCreated,
			},
		}
		// Original pods (worker-0, worker-1) are gone; replacement pods (worker-2, worker-3) exist with correct label
		pod1 := makePod("worker-2", pgName)
		pod2 := makePod("worker-3", pgName)
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(pcs, pclq, pg, pod1, pod2).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10)}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		assert.True(t, sc.isExistingPodGang(pgName))

		result := r.createOrUpdatePodGangs(ctx, sc)
		require.False(t, result.hasErrors(), "should succeed: %v", result.errs)
		assert.Empty(t, result.createdPodGangNames)

		// Verify PodReferences now point to the replacement pods
		pgAfter := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, pgAfter))
		require.Len(t, pgAfter.Spec.PodGroups, 1)
		refs := pgAfter.Spec.PodGroups[0].PodReferences
		require.Len(t, refs, 2)
		refNames := []string{refs[0].Name, refs[1].Name}
		assert.ElementsMatch(t, []string{"worker-2", "worker-3"}, refNames, "PodReferences should point to replacement pods, not old ones")

		// isPodGangInitialized returns true → patchPodGangInitializedStatus is skipped (no redundant status patch)
		assert.True(t, sc.isPodGangInitialized(pgName))
	})

	t.Run("multiple PodGangs, first not ready second ready - both processed, requeue for first", func(t *testing.T) {
		ctx := t.Context()
		// 2 PCS replicas → 2 base PodGangs: test-pcs-0 and test-pcs-1
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: ns, UID: "pcs-uid"},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 2,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
						{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
					},
				},
			},
		}
		pclq0Name := "test-pcs-0-worker"
		pclq1Name := "test-pcs-1-worker"
		pg0Name := "test-pcs-0"
		pg1Name := "test-pcs-1"
		pclq0 := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name: pclq0Name, Namespace: ns, UID: "pclq0-uid",
				Labels:          pcsLabels,
				OwnerReferences: []metav1.OwnerReference{{Name: pcsName, UID: "pcs-uid", Controller: ptr.To(true)}},
			},
			Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
		}
		pclq1 := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name: pclq1Name, Namespace: ns, UID: "pclq1-uid",
				Labels:          pcsLabels,
				OwnerReferences: []metav1.OwnerReference{{Name: pcsName, UID: "pcs-uid", Controller: ptr.To(true)}},
			},
			Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
		}
		// PodGang 0: no pods → verifyAllPodsCreated will fail
		// PodGang 1: pod exists with correct label → will succeed and get Initialized=True
		pod1 := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "worker-1-0", Namespace: ns,
				Labels:          lo.Assign(pcsLabels, map[string]string{apicommon.LabelPodGang: pg1Name}),
				OwnerReferences: []metav1.OwnerReference{{Name: pclq1Name, UID: "pclq1-uid", Controller: ptr.To(true)}},
			},
		}
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(pcs, pclq0, pclq1, pod1).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10)}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		require.Len(t, sc.expectedPodGangs, 2, "should have 2 expected PodGangs for 2 PCS replicas")

		result := r.createOrUpdatePodGangs(ctx, sc)

		// Both PodGangs should be created
		require.Len(t, result.createdPodGangNames, 2, "both PodGangs should be recorded as created")
		assert.ElementsMatch(t, []string{pg0Name, pg1Name}, result.createdPodGangNames)

		// Result should have errors (from PodGang 0's failed verification) triggering a requeue
		require.True(t, result.hasErrors(), "should have errors because first PodGang's pods are not ready")

		// PodGang 1 should be fully initialized despite PodGang 0's failure
		pg1After := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pg1Name}, pg1After))
		require.NotEmpty(t, pg1After.Status.Conditions)
		assert.Equal(t, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized), pg1After.Status.Conditions[0].Type)
		assert.Equal(t, metav1.ConditionTrue, pg1After.Status.Conditions[0].Status)

		// PodGang 0 should exist but NOT be initialized (verification failed)
		pg0After := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pg0Name}, pg0After))
		assert.False(t, sc.isPodGangInitialized(pg0Name), "PodGang 0 should not be initialized")
	})

	t.Run("existing PodGang, pods missing PodGang label - updates PodGang, records requeue error", func(t *testing.T) {
		ctx := t.Context()
		pcs := makePCS()
		pclq := makePCLQ()
		pg := makeExistingPodGang()
		// Pods exist but without the PodGang label
		pod1 := makePod("worker-0", "")
		pod2 := makePod("worker-1", "")
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(pcs, pclq, pg, pod1, pod2).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10)}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		assert.True(t, sc.isExistingPodGang(pgName))

		result := r.createOrUpdatePodGangs(ctx, sc)
		// createOrUpdatePodGang succeeds, but verifyAllPodsCreated fails → error recorded, loop continues
		require.True(t, result.hasErrors(), "should have requeue error because pods are not associated")
		assert.Empty(t, result.createdPodGangNames, "should not record creation for existing PodGang")

		var groveErr *groveerr.GroveError
		require.True(t, errors.As(result.errs[0], &groveErr))
		assert.Equal(t, groveerr.ErrCodeRequeueAfter, groveErr.Code)
	})
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
				pcs:            pcs,
				logger:         ctrllogger.FromContext(t.Context()),
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
		{
			name:             "PCS with PCSG where PCSG has nil topology constraints and falls back to PCS level",
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
					Name:         "scaling-group",
					Replicas:     ptr.To(int32(2)),
					MinAvailable: ptr.To(int32(1)),
					CliqueNames:  []string{"decode-leader", "decode-worker"},
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
				},
				{
					fqn:           "test-pcs-0-scaling-group-0",
					topologyLevel: &topologyLevelZone,
					pclqConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-scaling-group-1-decode-leader": topologyLevelHost,
						"test-pcs-0-scaling-group-1-decode-worker": topologyLevelHost,
					},
				},
			},
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
				pcs:            pcs,
				logger:         ctrllogger.FromContext(t.Context()),
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

// makePCSWithTopology creates a minimal PCS with an optional topology constraint.
func makePCSWithTopology(ns, name string, topologyName string) *grovecorev1alpha1.PodCliqueSet {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: "pcs-uid"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
				},
			},
		},
	}
	if topologyName != "" {
		pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
			TopologyName: topologyName,
			PackDomain:   "rack",
		}
	}
	return pcs
}

// makeClusterTopologyWithLevels creates a ClusterTopology with the given levels.
func makeClusterTopologyWithLevels(name string, levels []grovecorev1alpha1.TopologyLevel) *grovecorev1alpha1.ClusterTopology {
	return &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       grovecorev1alpha1.ClusterTopologySpec{Levels: levels},
	}
}

// TestPrepareSyncFlowTopologyResolution verifies that prepareSyncFlow resolves topology levels from the
// PCS topologyName field, not from a hardcoded name.
func TestPrepareSyncFlowTopologyResolution(t *testing.T) {
	ns := "default"
	ctLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: "zone", Key: "topology.kubernetes.io/zone"},
		{Domain: "rack", Key: "topology.kubernetes.io/rack"},
		{Domain: "host", Key: "kubernetes.io/hostname"},
	}

	tests := []struct {
		name                  string
		topologyName          string
		mutatePCS             func(*grovecorev1alpha1.PodCliqueSet)
		clusterTopologyExists bool
		tasEnabled            bool
		wantTopologyLevels    []grovecorev1alpha1.TopologyLevel
		wantErr               bool
	}{
		{
			name:                  "TAS enabled, topologyName set, CT exists - levels populated from CT",
			topologyName:          "my-topology",
			clusterTopologyExists: true,
			tasEnabled:            true,
			wantTopologyLevels:    ctLevels,
		},
		{
			name:                  "TAS enabled, no TopologyConstraint on PCS - topologyLevels stay nil",
			topologyName:          "",
			clusterTopologyExists: false,
			tasEnabled:            true,
			wantTopologyLevels:    nil,
		},
		{
			name:         "TAS enabled, only child explicit topology constraint - topologyLevels resolved from child",
			topologyName: "",
			mutatePCS: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "my-topology",
					PackDomain:   "rack",
				}
			},
			clusterTopologyExists: true,
			tasEnabled:            true,
			wantTopologyLevels:    ctLevels,
		},
		{
			name:                  "TAS enabled, topologyName set, CT not found - topologyLevels stay nil",
			topologyName:          "missing-topology",
			clusterTopologyExists: false,
			tasEnabled:            true,
			wantTopologyLevels:    nil,
		},
		{
			name:                  "TAS disabled, topologyName set, CT exists - topologyLevels stay nil",
			topologyName:          "my-topology",
			clusterTopologyExists: true,
			tasEnabled:            false,
			wantTopologyLevels:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			pcs := makePCSWithTopology(ns, "test-pcs", tc.topologyName)
			if tc.mutatePCS != nil {
				tc.mutatePCS(pcs)
			}

			var objs []client.Object
			objs = append(objs, pcs)
			if tc.clusterTopologyExists {
				topologyName, err := componentutils.ResolveTopologyNameForPodCliqueSet(pcs)
				require.NoError(t, err)
				objs = append(objs, makeClusterTopologyWithLevels(topologyName, ctLevels))
			}

			fakeClient := testutils.NewTestClientBuilder().WithObjects(objs...).Build()
			r := &_resource{
				client:        fakeClient,
				scheme:        groveclientscheme.Scheme,
				eventRecorder: record.NewFakeRecorder(10),
				tasConfig:     configv1alpha1.TopologyAwareSchedulingConfiguration{Enabled: tc.tasEnabled},
			}

			sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)

			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, sc)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, sc)
			assert.Equal(t, tc.wantTopologyLevels, sc.topologyLevels)
		})
	}
}

func TestCreateOrUpdatePodGangs_ClearsStaleTopologyStateOnExistingPodGang(t *testing.T) {
	ns := "default"
	pcsName := "test-pcs"
	pgName := "test-pcs-0"
	pclqName := "test-pcs-0-worker"
	pcsLabels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)

	makePCLQ := func() *grovecorev1alpha1.PodClique {
		return &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pclqName,
				Namespace:       ns,
				UID:             "pclq-uid",
				Labels:          pcsLabels,
				OwnerReferences: []metav1.OwnerReference{{Name: pcsName, UID: "pcs-uid", Controller: ptr.To(true)}},
			},
			Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
		}
	}

	makePod := func() *v1.Pod {
		return &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "worker-0",
				Namespace: ns,
				Labels: lo.Assign(pcsLabels, map[string]string{
					apicommon.LabelPodGang: pgName,
				}),
				OwnerReferences: []metav1.OwnerReference{{Name: pclqName, UID: "pclq-uid", Controller: ptr.To(true)}},
			},
		}
	}

	makeExistingPodGang := func(withAnnotation bool, withTopologyConstraint bool) *groveschedulerv1alpha1.PodGang {
		pg := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pgName,
				Namespace: ns,
				Labels:    getLabels(pcsName),
			},
			Spec: groveschedulerv1alpha1.PodGangSpec{
				PodGroups: []groveschedulerv1alpha1.PodGroup{{Name: pclqName, MinReplicas: 1}},
			},
		}
		if withAnnotation {
			pg.Annotations = map[string]string{apicommonconstants.AnnotationTopologyName: "my-topology"}
		}
		if withTopologyConstraint {
			pg.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To("topology.kubernetes.io/rack")},
			}
		}
		return pg
	}

	tests := []struct {
		name                   string
		setupPCS               func() *grovecorev1alpha1.PodCliqueSet
		clusterTopologyObjects []client.Object
		existingPodGang        *groveschedulerv1alpha1.PodGang
		wantAnnotationPresent  bool
		wantTopologyConstraint bool
	}{
		{
			name: "stale ClusterTopology domain removes existing PodGang topology metadata",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return makePCSWithTopology(ns, pcsName, "my-topology")
			},
			clusterTopologyObjects: []client.Object{
				makeClusterTopologyWithLevels("my-topology", []grovecorev1alpha1.TopologyLevel{
					{Domain: "zone", Key: "topology.kubernetes.io/zone"},
				}),
			},
			existingPodGang:        makeExistingPodGang(true, true),
			wantAnnotationPresent:  false,
			wantTopologyConstraint: false,
		},
		{
			name: "invalid current topology state removes stale topology annotation from existing PodGang",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := makePCSWithTopology(ns, pcsName, "")
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
					PackDomain: "rack",
				}
				return pcs
			},
			clusterTopologyObjects: nil,
			existingPodGang:        makeExistingPodGang(true, true),
			wantAnnotationPresent:  false,
			wantTopologyConstraint: false,
		},
		{
			name: "missing ClusterTopology removes stale topology metadata from existing PodGang",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return makePCSWithTopology(ns, pcsName, "missing-topology")
			},
			clusterTopologyObjects: nil,
			existingPodGang:        makeExistingPodGang(true, true),
			wantAnnotationPresent:  false,
			wantTopologyConstraint: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			pcs := tc.setupPCS()
			objs := []client.Object{pcs, makePCLQ(), makePod(), tc.existingPodGang}
			objs = append(objs, tc.clusterTopologyObjects...)

			fakeClient := testutils.NewTestClientBuilder().
				WithObjects(objs...).
				WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
				Build()

			r := &_resource{
				client:        fakeClient,
				scheme:        groveclientscheme.Scheme,
				eventRecorder: record.NewFakeRecorder(10),
				tasConfig:     configv1alpha1.TopologyAwareSchedulingConfiguration{Enabled: true},
			}

			sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
			require.NoError(t, err)

			result := r.createOrUpdatePodGangs(ctx, sc)
			require.False(t, result.hasErrors(), "unexpected sync errors: %v", result.errs)

			pgAfter := &groveschedulerv1alpha1.PodGang{}
			require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, pgAfter))

			_, hasAnnotation := pgAfter.Annotations[apicommonconstants.AnnotationTopologyName]
			assert.Equal(t, tc.wantAnnotationPresent, hasAnnotation)
			if tc.wantAnnotationPresent {
				assert.Equal(t, "my-topology", pgAfter.Annotations[apicommonconstants.AnnotationTopologyName])
			}
			assert.Equal(t, tc.wantTopologyConstraint, pgAfter.Spec.TopologyConstraint != nil)
		})
	}
}

// TestBuildResourceTopologyAnnotation verifies that PodGangs created by createOrUpdatePodGangs carry the
// grove.io/topology-name annotation when TAS is enabled and a topologyName is set on the PCS, and that
// the annotation is absent otherwise.
func TestBuildResourceTopologyAnnotation(t *testing.T) {
	ns := "default"
	pcsName := "test-pcs"
	pcsLabels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)
	pgName := "test-pcs-0"
	pclqName := "test-pcs-0-worker"
	topologyName := "my-topology"

	makePCLQ := func() *grovecorev1alpha1.PodClique {
		return &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name: pclqName, Namespace: ns, UID: "pclq-uid",
				Labels:          pcsLabels,
				OwnerReferences: []metav1.OwnerReference{{Name: pcsName, UID: "pcs-uid", Controller: ptr.To(true)}},
			},
			Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
		}
	}

	makePod := func(podGangLabel string) *v1.Pod {
		labels := lo.Assign(pcsLabels)
		if podGangLabel != "" {
			labels[apicommon.LabelPodGang] = podGangLabel
		}
		return &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "worker-0", Namespace: ns,
				Labels:          labels,
				OwnerReferences: []metav1.OwnerReference{{Name: pclqName, UID: "pclq-uid", Controller: ptr.To(true)}},
			},
		}
	}

	tests := []struct {
		name           string
		topologyName   string
		tasEnabled     bool
		wantAnnotation bool
	}{
		{
			name:           "TAS enabled, topologyName set - PodGang has topology-name annotation",
			topologyName:   topologyName,
			tasEnabled:     true,
			wantAnnotation: true,
		},
		{
			name:           "TAS enabled, topologyName empty - PodGang has no topology-name annotation",
			topologyName:   "",
			tasEnabled:     true,
			wantAnnotation: false,
		},
		{
			name:           "TAS disabled, topologyName set - PodGang has no topology-name annotation",
			topologyName:   topologyName,
			tasEnabled:     false,
			wantAnnotation: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			pcs := makePCSWithTopology(ns, pcsName, tc.topologyName)
			pclq := makePCLQ()
			pod := makePod(pgName)

			ctLevels := []grovecorev1alpha1.TopologyLevel{
				{Domain: "rack", Key: "topology.kubernetes.io/rack"},
			}
			var objs []client.Object
			objs = append(objs, pcs, pclq, pod)
			if tc.topologyName != "" {
				objs = append(objs, makeClusterTopologyWithLevels(tc.topologyName, ctLevels))
			}

			fakeClient := testutils.NewTestClientBuilder().
				WithObjects(objs...).
				WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
				Build()

			r := &_resource{
				client:        fakeClient,
				scheme:        groveclientscheme.Scheme,
				eventRecorder: record.NewFakeRecorder(10),
				tasConfig:     configv1alpha1.TopologyAwareSchedulingConfiguration{Enabled: tc.tasEnabled},
			}

			sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
			require.NoError(t, err)

			r.createOrUpdatePodGangs(ctx, sc)

			pgAfter := &groveschedulerv1alpha1.PodGang{}
			require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, pgAfter))

			if tc.wantAnnotation {
				assert.Equal(t, tc.topologyName, pgAfter.Annotations[apicommonconstants.AnnotationTopologyName],
					"PodGang should have the topology-name annotation set to the PCS topologyName")
			} else {
				_, hasAnnotation := pgAfter.Annotations[apicommonconstants.AnnotationTopologyName]
				assert.False(t, hasAnnotation, "PodGang should not have the topology-name annotation")
			}
		})
	}
}
