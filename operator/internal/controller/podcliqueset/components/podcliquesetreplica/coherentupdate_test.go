// /*
// Copyright 2026 The Grove Authors.
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

package podcliquesetreplica

import (
	"context"
	"fmt"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestComputeCoherentPendingWork(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

	makePCS := func(replicas int32) *grovecorev1alpha1.PodCliqueSet {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pcs", Namespace: "default"},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: replicas,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
						{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To[int32](1)}},
					},
				},
			},
		}
		generationHash := "test-generation-hash"
		pcs.Status.CurrentGenerationHash = &generationHash
		return pcs
	}

	// pclqHashesFromPCS returns the PCS-generation and pod-template hashes for
	// the worker clique so a PCLQ can be marked as fully converged on the desired spec.
	pclqHashesFromPCS := func(pcs *grovecorev1alpha1.PodCliqueSet) (pcsGenerationHash, podTemplateHash string) {
		return *pcs.Status.CurrentGenerationHash,
			componentutils.ComputePCLQPodTemplateHash(pcs.Spec.Template.Cliques[0], pcs.Spec.Template.PriorityClassName)
	}

	makePCLQ := func(name, namespace, pcsName string, replicaIndex int, pcsGenerationHash, podTemplateHash string, updatedReplicas, readyReplicas int32) grovecorev1alpha1.PodClique {
		return grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by":        "grove-operator",
					"app.kubernetes.io/part-of":           pcsName,
					"grove.io/podcliqueset-replica-index": fmt.Sprintf("%d", replicaIndex),
					apicommon.LabelPodTemplateHash:        podTemplateHash,
				},
				OwnerReferences: []metav1.OwnerReference{
					{Kind: "PodCliqueSet", Name: pcsName},
				},
			},
			Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To[int32](1)},
			Status: grovecorev1alpha1.PodCliqueStatus{
				CurrentPodCliqueSetGenerationHash: &pcsGenerationHash,
				CurrentPodTemplateHash:            &podTemplateHash,
				UpdatedReplicas:                   updatedReplicas,
				ReadyReplicas:                     readyReplicas,
			},
		}
	}

	// Build PCS instances and look up their canonical hashes once so test cases can use the
	// matching values to mark a PCLQ "done" or stale values to mark it "pending".
	pcs2 := makePCS(2)
	pcs3 := makePCS(3)
	doneGenHash2, doneTemplateHash2 := pclqHashesFromPCS(pcs2)
	doneGenHash3, doneTemplateHash3 := pclqHashesFromPCS(pcs3)

	tests := []struct {
		name                    string
		pcs                     *grovecorev1alpha1.PodCliqueSet
		pclqs                   []grovecorev1alpha1.PodClique
		pcsIndicesToTerminate   []int
		expectedPendingReplicas []int
		expectedDoneReplicas    []int
	}{
		{
			name: "all_replicas_done",
			pcs:  pcs2,
			pclqs: []grovecorev1alpha1.PodClique{
				makePCLQ("test-pcs-0-worker", "default", "test-pcs", 0, doneGenHash2, doneTemplateHash2, 2, 2),
				makePCLQ("test-pcs-1-worker", "default", "test-pcs", 1, doneGenHash2, doneTemplateHash2, 2, 2),
			},
			expectedPendingReplicas: nil,
			expectedDoneReplicas:    []int{0, 1},
		},
		{
			name: "all_replicas_pending",
			pcs:  pcs2,
			pclqs: []grovecorev1alpha1.PodClique{
				makePCLQ("test-pcs-0-worker", "default", "test-pcs", 0, "stale-gen-hash", "stale-template-hash", 2, 2),
				makePCLQ("test-pcs-1-worker", "default", "test-pcs", 1, "stale-gen-hash", "stale-template-hash", 2, 2),
			},
			expectedPendingReplicas: []int{0, 1},
			expectedDoneReplicas:    nil,
		},
		{
			name: "mixed_done_and_pending",
			pcs:  pcs3,
			pclqs: []grovecorev1alpha1.PodClique{
				makePCLQ("test-pcs-0-worker", "default", "test-pcs", 0, doneGenHash3, doneTemplateHash3, 2, 2),
				makePCLQ("test-pcs-1-worker", "default", "test-pcs", 1, "stale-gen-hash", "stale-template-hash", 2, 2),
				makePCLQ("test-pcs-2-worker", "default", "test-pcs", 2, doneGenHash3, doneTemplateHash3, 2, 2),
			},
			expectedPendingReplicas: []int{1},
			expectedDoneReplicas:    []int{0, 2},
		},
		{
			name: "terminating_replica_excluded",
			pcs:  pcs2,
			pclqs: []grovecorev1alpha1.PodClique{
				makePCLQ("test-pcs-0-worker", "default", "test-pcs", 0, "stale-gen-hash", "stale-template-hash", 2, 2),
				makePCLQ("test-pcs-1-worker", "default", "test-pcs", 1, "stale-gen-hash", "stale-template-hash", 2, 2),
			},
			pcsIndicesToTerminate:   []int{1},
			expectedPendingReplicas: []int{0},
			expectedDoneReplicas:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{tc.pcs}
			for i := range tc.pclqs {
				objs = append(objs, &tc.pclqs[i])
			}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
			r := _resource{client: fakeClient}

			work, err := r.computeCoherentPendingWork(context.Background(), tc.pcs, tc.pcsIndicesToTerminate)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.expectedPendingReplicas, work.pendingReplicaIndices)
			assert.ElementsMatch(t, tc.expectedDoneReplicas, work.doneReplicaIndices)
		})
	}
}

func TestCheckAndAdvanceCoherentUpdate(t *testing.T) {
	const (
		pcsName = "test-pcs"
		ns      = "default"
		newHash = "new-hash"
	)

	// podGangNameForEpoch derives a stable PodGang/entry name from an epoch so the PGM entry and the
	// PodGang seeded for the same epoch share a name.
	podGangNameForEpoch := func(epoch string) string { return "pg-" + epoch }

	// podGangForEpoch builds a PodGang for the given epoch carrying the labels
	// AllPodGangsAtEpochEverReady selects on (part-of, pcs-replica-index=0, epoch). Its name is
	// derived from the epoch. ready => Status.LastReady set.
	podGangForEpoch := func(epoch string, ready bool) *groveschedulerv1alpha1.PodGang {
		b := testutils.NewPodGangBuilder(podGangNameForEpoch(epoch), ns).
			WithLabel(apicommon.LabelPartOfKey, pcsName).
			WithLabel(apicommon.LabelPodCliqueSetReplicaIndex, "0").
			WithLabel(apicommon.LabelEpoch, epoch)
		if ready {
			b = b.WithStatusLastReady(metav1.NewTime(time.Unix(0, 0)))
		}
		return b.Build()
	}

	// pgmWithEntries builds the replica-0 PodGangMap (name test-pcs-0) with the given entries.
	pgmWithEntries := func(entries ...grovecorev1alpha1.PodGangEntry) *grovecorev1alpha1.PodGangMap {
		return testutils.NewPodGangMapBuilder(pcsName, ns, "uid", 0).WithEntries(entries...).Build()
	}
	// newHashAnchorEntry builds a current-hash anchor entry for the given epoch. Its name matches the
	// PodGang podGangForEpoch creates for the same epoch.
	newHashAnchorEntry := func(epoch string) grovecorev1alpha1.PodGangEntry {
		return testutils.NewPodGangEntryBuilder(podGangNameForEpoch(epoch), newHash, epoch).WithEpochAnchor(true).
			WithPodCliques(map[string]int32{"worker": 2}).Build()
	}

	tests := []struct {
		name                  string
		podGangMap            *grovecorev1alpha1.PodGangMap
		podGangs              []*groveschedulerv1alpha1.PodGang
		inFlightEpochs        []string
		replicaDone           bool
		expectReplicaDone     bool
		expectRequeue         bool
		expectInFlightEpochs  []string
		expectMessageNonEmpty bool
	}{
		{
			name:                  "no PodGangMap yet requeues (first sub-step not emitted)",
			podGangMap:            nil,
			expectRequeue:         true,
			expectMessageNonEmpty: true,
		},
		{
			name:                  "PGM has no current-hash epoch requeues",
			podGangMap:            pgmWithEntries(testutils.NewPodGangEntryBuilder("pg-old", "old-hash", "50").WithEpochAnchor(true).Build()),
			expectRequeue:         true,
			expectMessageNonEmpty: true,
		},
		{
			name:                  "latest epoch not ready sets InFlightEpochs, records message, requeues",
			podGangMap:            pgmWithEntries(newHashAnchorEntry("100")),
			podGangs:              []*groveschedulerv1alpha1.PodGang{podGangForEpoch("100", false)},
			expectRequeue:         true,
			expectInFlightEpochs:  []string{"100"},
			expectMessageNonEmpty: true,
		},
		{
			name:                  "latest epoch ready but replica not done requeues for next sub-step, InFlightEpochs kept",
			podGangMap:            pgmWithEntries(newHashAnchorEntry("100")),
			podGangs:              []*groveschedulerv1alpha1.PodGang{podGangForEpoch("100", true)},
			expectRequeue:         true,
			expectInFlightEpochs:  []string{"100"},
			expectMessageNonEmpty: true,
		},
		{
			name:              "replica already done marks done (early-exit, no PGM read needed)",
			replicaDone:       true,
			expectReplicaDone: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: ns},
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: ptr.To(newHash),
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
							{ReplicaIndex: 0, UpdateStartedAt: metav1.Now(), InFlightEpochs: tc.inFlightEpochs},
						},
					},
				},
			}

			objs := []client.Object{pcs}
			if tc.podGangMap != nil {
				objs = append(objs, tc.podGangMap)
			}
			for _, pg := range tc.podGangs {
				objs = append(objs, pg)
			}
			cl := testutils.NewTestClientBuilder().WithObjects(objs...).WithStatusSubresource(pcs).Build()
			r := _resource{client: cl}

			updateWork := &coherentPendingWork{}
			if tc.replicaDone {
				updateWork.doneReplicaIndices = []int{0}
			} else {
				updateWork.pendingReplicaIndices = []int{0}
			}

			replicaDone, err := r.checkAndAdvanceCoherentUpdate(context.Background(), logr.Discard(), pcs, updateWork)

			progress := pcs.Status.UpdateProgress.CurrentlyUpdating[0]
			if tc.expectReplicaDone {
				require.NoError(t, err)
				assert.True(t, replicaDone)
				assert.NotNil(t, progress.UpdateEndedAt)
				assert.Nil(t, progress.InFlightEpochs)
			} else if tc.expectRequeue {
				require.Error(t, err)
				testutils.CheckGroveError(t, &groveerr.GroveError{Code: groveerr.ErrCodeContinueReconcileAndRequeue, Operation: component.OperationSync}, err)
				assert.False(t, replicaDone)
				if tc.expectInFlightEpochs != nil {
					assert.Equal(t, tc.expectInFlightEpochs, progress.InFlightEpochs)
				}
				if tc.expectMessageNonEmpty {
					require.NotNil(t, progress.Message)
					assert.NotEmpty(t, *progress.Message)
				}
			}
		})
	}
}
