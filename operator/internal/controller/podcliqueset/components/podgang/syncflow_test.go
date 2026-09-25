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

package podgang

import (
	"context"
	"errors"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllogger "sigs.k8s.io/controller-runtime/pkg/log"
)

var defaultFakeSchedulerRegistry = &testutils.FakeSchedulerRegistry{
	Backends: map[string]scheduler.Backend{
		"default-scheduler": testutils.NewFakeSchedulerBackend("default-scheduler"),
	},
	DefaultBackend: "default-scheduler",
}

// TestVerifyAllPodsCreated tests verifyAllPodsCreated with a minimal syncState and podGangInfo (no
// PCS/prepareSyncFlow). It covers the constituent-PodClique existence check and the requeue-versus-
// success gate for a single PodGang, whose pod accounting is delegated to
// getPodsPendingCreationOrAssociation. The 1:N split of a PodClique across multiple PodGangs is
// covered by TestGetPodsPendingCreation.
func TestVerifyAllPodsCreated(t *testing.T) {
	makePCLQ := func(name string) grovecorev1alpha1.PodClique {
		return grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	}

	tests := []struct {
		name          string
		existingPCLQs []grovecorev1alpha1.PodClique
		podGang       *podGangInfo
		wantRequeue   bool
	}{
		{
			name:          "requeue when not all constituent PodCliques exist yet",
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a")},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 1, minAvailable: 1, associatedPodNames: []string{"a1"}}, {fqn: "pclq-b", replicas: 1, minAvailable: 1}}},
			wantRequeue:   true,
		},
		{
			name:          "requeue when a PodClique has fewer associated pods than its share",
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a")},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 5, minAvailable: 2, associatedPodNames: []string{"a1", "a2"}}}},
			wantRequeue:   true, // 5 share - 2 associated = 3 pending
		},
		{
			name:          "success when every PodClique has its full share associated",
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a")},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 5, minAvailable: 2, associatedPodNames: []string{"a1", "a2", "a3", "a4", "a5"}}}},
			wantRequeue:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := &syncState{
				logger:             ctrllogger.FromContext(t.Context()).WithName("test"),
				existingPCLQByName: componentutils.PodCliqueByName(tt.existingPCLQs),
			}
			r := &_resource{schedRegistry: defaultFakeSchedulerRegistry}
			err := r.verifyAllPodsCreated(sc, tt.podGang)
			if tt.wantRequeue {
				testutils.AssertGroveError(t, &groveerr.GroveError{Code: groveerr.ErrCodeRequeueAfter, Operation: component.OperationSync}, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestGetPodsPendingCreation verifies getPodsPendingCreationOrAssociation, which counts how many
// pods a PodGang still needs before it is ready: the pods from PodCliques that do not exist yet
// (counted at this PodGang's share), plus, for existing PodCliques, the shortfall between this
// PodGang's share (pclqInfo.replicas) and the pods already associated to this PodGang
// (pclqInfo.associatedPodNames).
func TestGetPodsPendingCreation(t *testing.T) {
	makePCLQ := func(name string) grovecorev1alpha1.PodClique {
		return grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	}

	tests := []struct {
		name            string
		podGangs        []*podGangInfo
		existingPCLQs   []grovecorev1alpha1.PodClique
		expectedPending []int // parallel to podGangs
	}{
		{
			name:            "PodClique does not exist yet counts its share",
			podGangs:        []*podGangInfo{{fqn: "pg-0", pclqs: []pclqInfo{{fqn: "worker", replicas: 3}}}},
			expectedPending: []int{3},
		},
		{
			// 1:1 - the standalone PodClique's pods all belong to a single PodGang. This is the initial
			// deployment and RollingRecreate/OnDelete shape.
			name:            "1:1 existing PodClique with its full share associated has no pending pods",
			podGangs:        []*podGangInfo{{fqn: "pg-0", pclqs: []pclqInfo{{fqn: "worker", replicas: 3, associatedPodNames: []string{"worker-0", "worker-1", "worker-2"}}}}},
			existingPCLQs:   []grovecorev1alpha1.PodClique{makePCLQ("worker")},
			expectedPending: []int{0},
		},
		{
			name:            "1:1 existing PodClique with fewer associated pods than its share counts the shortfall",
			podGangs:        []*podGangInfo{{fqn: "pg-0", pclqs: []pclqInfo{{fqn: "worker", replicas: 3, associatedPodNames: []string{"worker-0"}}}}},
			existingPCLQs:   []grovecorev1alpha1.PodClique{makePCLQ("worker")},
			expectedPending: []int{2},
		},
		{
			name:            "existing PodClique with more associated pods than its share clamps the negative shortfall to zero",
			podGangs:        []*podGangInfo{{fqn: "pg-0", pclqs: []pclqInfo{{fqn: "worker", replicas: 1, associatedPodNames: []string{"worker-0", "worker-1"}}}}},
			existingPCLQs:   []grovecorev1alpha1.PodClique{makePCLQ("worker")},
			expectedPending: []int{0},
		},
		{
			// 1:N - the standalone PodClique "worker" (6 replicas) is split 3+3 across two anchor
			// PodGangs post coherent update. Each PodGang counts only the pods associated to it, so a
			// pod belonging to the sibling PodGang is not counted here. pg-0 has 2 of its 3 associated
			// (1 pending); pg-1 has all 3 associated (0 pending).
			name: "1:N standalone PodClique split across two PodGangs counts each share independently",
			podGangs: []*podGangInfo{
				{fqn: "pg-0", pclqs: []pclqInfo{{fqn: "worker", replicas: 3, associatedPodNames: []string{"worker-0", "worker-1"}}}},
				{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "worker", replicas: 3, associatedPodNames: []string{"worker-2", "worker-3", "worker-4"}}}},
			},
			existingPCLQs:   []grovecorev1alpha1.PodClique{makePCLQ("worker")},
			expectedPending: []int{1, 0},
		},
		{
			name: "PodGang with a not-yet-created clique and an under-provisioned clique sums both shortfalls",
			podGangs: []*podGangInfo{{fqn: "pg-0", pclqs: []pclqInfo{
				{fqn: "missing", replicas: 4},
				{fqn: "worker", replicas: 3, associatedPodNames: []string{"worker-0", "worker-1"}},
			}}},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("worker")},
			// missing clique does not exist yet -> its full share of 4; worker exists -> 3 share - 2 associated = 1.
			expectedPending: []int{5},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ss := &syncState{
				logger:             ctrllogger.FromContext(t.Context()).WithName("test"),
				existingPCLQByName: componentutils.PodCliqueByName(test.existingPCLQs),
			}
			r := &_resource{schedRegistry: defaultFakeSchedulerRegistry}

			require.Len(t, test.expectedPending, len(test.podGangs))
			for i, pgi := range test.podGangs {
				actual := r.getPodsPendingCreationOrAssociation(ss, pgi)
				assert.Equal(t, test.expectedPending[i], actual, "PodGang %s", pgi.fqn)
			}
		})
	}
}

// TestCreateOrUpdatePodGangs verifies the createOrUpdatePodGangs orchestration loop: it creates or
// patches each expected PodGang, records the ones that did not previously exist, and marks a PodGang
// Initialized only once all its pods are created and labeled. The per-pod readiness accounting is
// covered by TestVerifyAllPodsCreated and TestGetPodsPendingCreation; this test focuses on the
// loop-level effects (creation recording, requeue-error handling with continue, Initialized
// idempotency, and early return on a create failure). The syncState is built directly so the loop is
// exercised in isolation from prepareSyncFlow and the PodGangMap.
func TestCreateOrUpdatePodGangs(t *testing.T) {
	const (
		ns          = "default"
		pcsName     = "test-pcs"
		anchorEpoch = "1000"
		pclqName    = "test-pcs-0-worker"
	)
	pcsLabels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)
	pgName := apicommon.GenerateAnchorPodGangName(apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}, anchorEpoch)

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: ns, UID: "pcs-uid"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(1))}},
				},
			},
		},
		Status: grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To("gen-hash-1")},
	}
	makePCLQ := func(name string, replicas int32) grovecorev1alpha1.PodClique {
		return grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       grovecorev1alpha1.PodCliqueSpec{Replicas: replicas, MinAvailable: ptr.To(int32(1))},
		}
	}
	makePod := func(name, podGangLabel string) v1.Pod {
		pod := v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if podGangLabel != "" {
			pod.Labels = map[string]string{apicommon.LabelPodGang: podGangLabel}
		}
		return pod
	}
	makeExistingPodGang := func(name string, initialized bool) *groveschedulerv1alpha1.PodGang {
		pg := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels:    lo.Assign(pcsLabels, map[string]string{apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang}),
			},
		}
		if initialized {
			setPodGangCondition(pg, groveschedulerv1alpha1.PodGangConditionTypeInitialized, metav1.ConditionTrue, groveschedulerv1alpha1.ConditionReasonPodGangPodsCreated, "PodGang is fully initialized")
		}
		return pg
	}
	// readyPCLQState builds the syncState pieces for a single fully-populated, correctly labeled PCLQ
	// (2 pods labeled for pgName), so verifyAllPodsCreated passes.
	readyPCLQState := func(podGangName string) ([]grovecorev1alpha1.PodClique, map[string][]v1.Pod) {
		pclq := makePCLQ(pclqName, 2)
		pods := map[string][]v1.Pod{pclqName: {makePod("worker-0", podGangName), makePod("worker-1", podGangName)}}
		return []grovecorev1alpha1.PodClique{pclq}, pods
	}
	// anchorPodGangInfo builds the anchor PodGang's info. associatedPodNames are the pods already
	// associated to this PodGang, mirroring what initializeAssignedAndUnassignedPodsForPCS populates
	// in production. A ready PodGang passes its 2 pod names; a not-ready one passes none.
	anchorPodGangInfo := func(associatedPodNames ...string) *podGangInfo {
		return &podGangInfo{fqn: pgName, pcsReplicaIndex: 0, pclqs: []pclqInfo{{fqn: pclqName, replicas: 2, minAvailable: 1, associatedPodNames: associatedPodNames}}}
	}

	newResource := func(cl client.Client) *_resource {
		return &_resource{
			client:        cl,
			scheme:        groveclientscheme.Scheme,
			eventRecorder: record.NewFakeRecorder(10),
			schedRegistry: defaultFakeSchedulerRegistry,
		}
	}
	isInitializedInCluster := func(t *testing.T, cl client.Client, name string) bool {
		pg := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, pg))
		return k8sutils.IsConditionTrue(pg.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized))
	}

	t.Run("new PodGang, pods not ready - creates PodGang, records creation and requeue error, does not set Initialized", func(t *testing.T) {
		ctx := t.Context()
		// PCLQ exists but has no pods yet, so verifyAllPodsCreated fails.
		pclq := makePCLQ(pclqName, 2)
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := newResource(cl)
		pgi := anchorPodGangInfo()
		ss := &syncState{
			pcs:                   pcs,
			logger:                ctrllogger.FromContext(ctx),
			expectedPodGangs:      []*podGangInfo{pgi},
			existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{},
			existingPCLQByName:    componentutils.PodCliqueByName([]grovecorev1alpha1.PodClique{pclq}),
			existingPCLQPods:      map[string][]v1.Pod{},
		}

		result := r.createOrUpdatePodGangs(ctx, ss)

		require.True(t, result.hasErrors())
		assert.Equal(t, []string{pgName}, result.createdPodGangNames)
		testutils.AssertGroveError(t, &groveerr.GroveError{Code: groveerr.ErrCodeRequeueAfter, Operation: component.OperationSync}, result.errs[0])
		// PodGang object was created even though it is not ready.
		pgAfter := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, pgAfter))
		assert.False(t, isInitializedInCluster(t, cl, pgName))
	})

	t.Run("existing PodGang was Scheduled and Ready, pods regress - flips Scheduled and Ready to False, keeps timestamps", func(t *testing.T) {
		ctx := t.Context()
		// The PodGang was previously Scheduled and Ready with timestamps stamped. In this pass its pods
		// have regressed so verifyAllPodsCreated fails, but the live Scheduled and Ready conditions must
		// still be reconciled to False. LastScheduled and LastReady are never cleared.
		existingPG := makeExistingPodGang(pgName, true)
		setScheduledCondition(existingPG, &groveschedulerv1alpha1.PodGangStatus{}, true, metav1.Now())
		setReadyCondition(existingPG, &groveschedulerv1alpha1.PodGangStatus{}, true, metav1.Now())

		pclq := makePCLQ(pclqName, 2)
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs, existingPG).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := newResource(cl)
		pgi := anchorPodGangInfo()
		ss := &syncState{
			pcs:                   pcs,
			logger:                ctrllogger.FromContext(ctx),
			expectedPodGangs:      []*podGangInfo{pgi},
			existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{pgName: *existingPG},
			existingPCLQByName:    componentutils.PodCliqueByName([]grovecorev1alpha1.PodClique{pclq}),
			existingPCLQPods:      map[string][]v1.Pod{},
		}

		// Read the seeded timestamps back from the client so they carry the same store-truncated
		// precision as the values asserted on after reconcile.
		initial := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, initial))
		wantLastScheduled := initial.Status.LastScheduled
		wantLastReady := initial.Status.LastReady

		result := r.createOrUpdatePodGangs(ctx, ss)

		require.True(t, result.hasErrors(), "verifyAllPodsCreated must record a requeue error")
		testutils.AssertGroveError(t, &groveerr.GroveError{Code: groveerr.ErrCodeRequeueAfter, Operation: component.OperationSync}, result.errs[0])
		patched := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, patched))
		assert.False(t, meta.IsStatusConditionTrue(patched.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeScheduled)), "Scheduled must flip to False on regression")
		assert.False(t, meta.IsStatusConditionTrue(patched.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeReady)), "Ready must flip to False on regression")
		assert.True(t, isInitializedInCluster(t, cl, pgName), "Initialized is a latch and must remain True")
		assert.Equal(t, wantLastScheduled, patched.Status.LastScheduled, "LastScheduled must not be cleared")
		assert.Equal(t, wantLastReady, patched.Status.LastReady, "LastReady must not be cleared")
	})

	t.Run("new PodGang, pods ready - creates PodGang, records creation, sets Initialized=True", func(t *testing.T) {
		ctx := t.Context()
		pclqs, pods := readyPCLQState(pgName)
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := newResource(cl)
		pgi := anchorPodGangInfo("worker-0", "worker-1")
		ss := &syncState{
			pcs:                   pcs,
			logger:                ctrllogger.FromContext(ctx),
			expectedPodGangs:      []*podGangInfo{pgi},
			existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{},
			existingPCLQByName:    componentutils.PodCliqueByName(pclqs),
			existingPCLQPods:      pods,
		}

		result := r.createOrUpdatePodGangs(ctx, ss)

		require.False(t, result.hasErrors(), "unexpected errors: %v", result.errs)
		assert.Equal(t, []string{pgName}, result.createdPodGangNames)
		assert.True(t, isInitializedInCluster(t, cl, pgName))
	})

	t.Run("existing PodGang not yet Initialized, pods ready - does not record creation, sets Initialized=True", func(t *testing.T) {
		ctx := t.Context()
		pclqs, pods := readyPCLQState(pgName)
		existingPG := makeExistingPodGang(pgName, false)
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs, existingPG).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := newResource(cl)
		pgi := anchorPodGangInfo("worker-0", "worker-1")
		ss := &syncState{
			pcs:                   pcs,
			logger:                ctrllogger.FromContext(ctx),
			expectedPodGangs:      []*podGangInfo{pgi},
			existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{pgName: *existingPG},
			existingPCLQByName:    componentutils.PodCliqueByName(pclqs),
			existingPCLQPods:      pods,
		}

		result := r.createOrUpdatePodGangs(ctx, ss)

		require.False(t, result.hasErrors(), "unexpected errors: %v", result.errs)
		assert.Empty(t, result.createdPodGangNames, "existing PodGang must not be recorded as created")
		assert.True(t, isInitializedInCluster(t, cl, pgName))
	})

	t.Run("existing PodGang already Initialized, pods ready - does not record creation, no error", func(t *testing.T) {
		ctx := t.Context()
		pclqs, pods := readyPCLQState(pgName)
		existingPG := makeExistingPodGang(pgName, true)
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs, existingPG).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := newResource(cl)
		pgi := anchorPodGangInfo("worker-0", "worker-1")
		ss := &syncState{
			pcs:                   pcs,
			logger:                ctrllogger.FromContext(ctx),
			expectedPodGangs:      []*podGangInfo{pgi},
			existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{pgName: *existingPG},
			existingPCLQByName:    componentutils.PodCliqueByName(pclqs),
			existingPCLQPods:      pods,
		}

		result := r.createOrUpdatePodGangs(ctx, ss)

		require.False(t, result.hasErrors(), "unexpected errors: %v", result.errs)
		assert.Empty(t, result.createdPodGangNames)
		assert.True(t, isInitializedInCluster(t, cl, pgName))
	})

	t.Run("multiple PodGangs, first not ready second ready - loop continues, both processed", func(t *testing.T) {
		ctx := t.Context()
		firstEpoch := "1000"
		secondEpoch := "2000"
		firstPGName := apicommon.GenerateAnchorPodGangName(apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}, firstEpoch)
		secondPGName := apicommon.GenerateAnchorPodGangName(apicommon.ResourceNameReplica{Name: pcsName, Replica: 1}, secondEpoch)
		firstPCLQName := "test-pcs-0-worker"
		secondPCLQName := "test-pcs-1-worker"

		// First PodGang: PCLQ exists but no pods -> not ready. Second PodGang: fully ready.
		firstPCLQ := makePCLQ(firstPCLQName, 2)
		secondPCLQ := makePCLQ(secondPCLQName, 2)
		pclqs := []grovecorev1alpha1.PodClique{firstPCLQ, secondPCLQ}
		pods := map[string][]v1.Pod{
			secondPCLQName: {makePod("worker-1-0", secondPGName), makePod("worker-1-1", secondPGName)},
		}
		firstPGI := &podGangInfo{fqn: firstPGName, pcsReplicaIndex: 0, pclqs: []pclqInfo{{fqn: firstPCLQName, replicas: 2, minAvailable: 1}}}
		secondPGI := &podGangInfo{fqn: secondPGName, pcsReplicaIndex: 1, pclqs: []pclqInfo{{fqn: secondPCLQName, replicas: 2, minAvailable: 1, associatedPodNames: []string{"worker-1-0", "worker-1-1"}}}}

		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := newResource(cl)
		ss := &syncState{
			pcs:                   pcs,
			logger:                ctrllogger.FromContext(ctx),
			expectedPodGangs:      []*podGangInfo{firstPGI, secondPGI},
			existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{},
			existingPCLQByName:    componentutils.PodCliqueByName(pclqs),
			existingPCLQPods:      pods,
		}

		result := r.createOrUpdatePodGangs(ctx, ss)

		// The first PodGang's verify failure records a requeue error but the loop continues to the second.
		require.True(t, result.hasErrors())
		assert.ElementsMatch(t, []string{firstPGName, secondPGName}, result.createdPodGangNames, "both PodGangs are created despite the first not being ready")
		// The second PodGang was created and marked Initialized; the first was created but not Initialized.
		assert.True(t, isInitializedInCluster(t, cl, secondPGName))
		assert.False(t, isInitializedInCluster(t, cl, firstPGName))
	})

	t.Run("create fails - records error and returns early without processing later PodGangs", func(t *testing.T) {
		ctx := t.Context()
		secondPGName := apicommon.GenerateAnchorPodGangName(apicommon.ResourceNameReplica{Name: pcsName, Replica: 1}, "2000")
		firstPGI := anchorPodGangInfo()
		secondPGI := &podGangInfo{fqn: secondPGName, pcsReplicaIndex: 1, pclqs: []pclqInfo{{fqn: "test-pcs-1-worker", replicas: 2, minAvailable: 1}}}

		createErr := testutils.TestAPIInternalErr
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			RecordErrorForObjects(testutils.ClientMethodCreate, createErr, client.ObjectKey{Namespace: ns, Name: pgName}).
			Build()
		r := newResource(cl)
		ss := &syncState{
			pcs:                   pcs,
			logger:                ctrllogger.FromContext(ctx),
			expectedPodGangs:      []*podGangInfo{firstPGI, secondPGI},
			existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{},
			existingPCLQByName:    map[string]grovecorev1alpha1.PodClique{},
			existingPCLQPods:      map[string][]v1.Pod{},
		}

		result := r.createOrUpdatePodGangs(ctx, ss)

		require.True(t, result.hasErrors())
		assert.Empty(t, result.createdPodGangNames, "no PodGang is recorded as created when the create call fails")
		// The loop returns early, so the second PodGang is never created.
		err := cl.Get(ctx, client.ObjectKey{Namespace: ns, Name: secondPGName}, &groveschedulerv1alpha1.PodGang{})
		assert.True(t, apierrors.IsNotFound(err), "second PodGang must not be created after early return")
	})
}

// expectedPodGangTopologyConstraints declares the topology constraints expected on a single
// materialized PodGang, addressed by fqn. Each field asserts both the required and preferred keys of
// the corresponding TopologyConstraint via expectedTopologyPackConstraint.
type expectedPodGangTopologyConstraints struct {
	fqn                    string
	topologyPackConstraint *expectedTopologyPackConstraint
	pclqPackConstraints    map[string]expectedTopologyPackConstraint
	pcsgPackConstraints    map[string]expectedTopologyPackConstraint
}

type expectedTopologyPackConstraint struct {
	requiredKey  string
	preferredKey string
}

// TestComputeExpectedPodGangsWithTopologyConstraints verifies that the materializer stamps the
// correct topology pack constraints on each materialized PodGang. The constraints are authored on
// the PodCliqueSet template (PCS level), on individual PodClique templates (PCLQ level), and on
// PodCliqueScalingGroup configs (PCSG group level). The PodGangMap entries are seeded from the PCS
// spec, and each case asserts the PodGang-level, PCLQ-level, and PCSG-group-level constraints on
// the anchor and any tail PodGang.
//
// Association note: the non-anchor PodGang carries the PCS-level constraint at the PodGang level and
// the PCSG constraint as a group-level TopologyConstraintGroupConfig. This differs from the older
// scheme where a scaled PodGang promoted the PCSG constraint to the PodGang level.
func TestComputeExpectedPodGangsWithTopologyConstraints(t *testing.T) {
	const (
		pcsName     = "test-pcs"
		namespace   = "default"
		genHash     = "test-hash"
		anchorEpoch = "1000"
		tailEpoch   = "1001"
	)
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
	anchorName := apicommon.GenerateAnchorPodGangName(apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}, anchorEpoch)
	tailName := func(pcsg string, idx int32) string {
		return apicommon.GenerateNonAnchorPodGangName(apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}, tailEpoch, pcsg, idx)
	}
	tests := []struct {
		name       string
		tasEnabled bool
		// pcsDeprecatedPackDomain sets the PCS-level constraint via the deprecated PackDomain field
		// (required-only). pcsTopologyConstraint sets it via the modern Pack struct. A case sets at
		// most one; the modern form takes precedence when both are set.
		pcsDeprecatedPackDomain            *grovecorev1alpha1.TopologyLevel
		pcsTopologyConstraint              *grovecorev1alpha1.TopologyConstraint
		pclqTemplateSpecs                  []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs                        []grovecorev1alpha1.PodCliqueScalingGroupConfig
		expectedNumPodGangs                int
		expectedPodGangTopologyConstraints []expectedPodGangTopologyConstraints
	}{
		{
			name:       "PCS with a single standalone PCLQ where no topology constraints are set",
			tasEnabled: true,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
			},
			expectedNumPodGangs: 1,
		},
		{
			name:                    "PCS with single standalone PCLQ where topology constraints are set at PCS only",
			tasEnabled:              true,
			pcsDeprecatedPackDomain: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:                    anchorName,
					topologyPackConstraint: &expectedTopologyPackConstraint{requiredKey: topologyLevelZone.Key},
				},
			},
		},
		{
			name:       "PCS with preferred-only topology constraint at PCS level",
			tasEnabled: true,
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				Pack: &grovecorev1alpha1.TopologyPackConstraint{PreferredDomain: "host"},
			},
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:                    anchorName,
					topologyPackConstraint: &expectedTopologyPackConstraint{preferredKey: topologyLevelHost.Key},
				},
			},
		},
		{
			name:       "PCS with required and preferred topology constraints at PCS level",
			tasEnabled: true,
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "zone", PreferredDomain: "host"},
			},
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:                    anchorName,
					topologyPackConstraint: &expectedTopologyPackConstraint{requiredKey: topologyLevelZone.Key, preferredKey: topologyLevelHost.Key},
				},
			},
		},
		{
			name:       "PCS with stale preferred domain preserves required topology constraint",
			tasEnabled: true,
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "rack", PreferredDomain: "block"},
			},
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:                    anchorName,
					topologyPackConstraint: &expectedTopologyPackConstraint{requiredKey: topologyLevelRack.Key},
				},
			},
		},
		{
			name:       "PCS with stale required domain preserves preferred topology constraint",
			tasEnabled: true,
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "block", PreferredDomain: "rack"},
			},
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:                    anchorName,
					topologyPackConstraint: &expectedTopologyPackConstraint{preferredKey: topologyLevelRack.Key},
				},
			},
		},
		{
			name:       "PCS with single standalone PCLQ where topology constraints are set for one of the PCLQs",
			tasEnabled: true,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "router", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "host"}},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(1))},
				},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:                 anchorName,
					pclqPackConstraints: map[string]expectedTopologyPackConstraint{"test-pcs-0-worker": {requiredKey: topologyLevelHost.Key}},
				},
			},
		},
		{
			name:       "PCS with preferred-only topology constraint on standalone PCLQ",
			tasEnabled: true,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "router", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{Pack: &grovecorev1alpha1.TopologyPackConstraint{PreferredDomain: "host"}},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(1))},
				},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:                 anchorName,
					pclqPackConstraints: map[string]expectedTopologyPackConstraint{"test-pcs-0-worker": {preferredKey: topologyLevelHost.Key}},
				},
			},
		},
		{
			name:                    "PCS with single standalone PCLQs where topology constraints are set at all levels",
			tasEnabled:              true,
			pcsDeprecatedPackDomain: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "router",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "zone"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))},
				},
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(1))},
				},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:                    anchorName,
					topologyPackConstraint: &expectedTopologyPackConstraint{requiredKey: topologyLevelZone.Key},
					pclqPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-worker": {requiredKey: topologyLevelHost.Key},
						"test-pcs-0-router": {requiredKey: topologyLevelZone.Key},
					},
				},
			},
		},
		{
			name:                    "PCS with PCSG where topology constraints are set at PCS and PCSG levels",
			tasEnabled:              true,
			pcsDeprecatedPackDomain: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "decode-leader",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
				},
				{
					Name:               "decode-worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(1))},
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
					fqn:                    anchorName,
					topologyPackConstraint: &expectedTopologyPackConstraint{requiredKey: topologyLevelZone.Key},
					pclqPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-0-decode-leader": {requiredKey: topologyLevelHost.Key},
						"test-pcs-0-scaling-group-0-decode-worker": {requiredKey: topologyLevelHost.Key},
					},
					pcsgPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-0": {requiredKey: topologyLevelRack.Key},
					},
				},
				{
					fqn:                    tailName("scaling-group", 1),
					topologyPackConstraint: &expectedTopologyPackConstraint{requiredKey: topologyLevelZone.Key},
					pclqPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-1-decode-leader": {requiredKey: topologyLevelHost.Key},
						"test-pcs-0-scaling-group-1-decode-worker": {requiredKey: topologyLevelHost.Key},
					},
					pcsgPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-1": {requiredKey: topologyLevelRack.Key},
					},
				},
			},
		},
		{
			name:       "PCS with preferred-only topology constraint on PCSG",
			tasEnabled: true,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "decode-leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
				{Name: "decode-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(1))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "scaling-group",
					Replicas:           ptr.To(int32(2)),
					MinAvailable:       ptr.To(int32(1)),
					CliqueNames:        []string{"decode-leader", "decode-worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{Pack: &grovecorev1alpha1.TopologyPackConstraint{PreferredDomain: "rack"}},
				},
			},
			expectedNumPodGangs: 2,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn:                 anchorName,
					pcsgPackConstraints: map[string]expectedTopologyPackConstraint{"test-pcs-0-scaling-group-0": {preferredKey: topologyLevelRack.Key}},
				},
				{
					fqn:                 tailName("scaling-group", 1),
					pcsgPackConstraints: map[string]expectedTopologyPackConstraint{"test-pcs-0-scaling-group-1": {preferredKey: topologyLevelRack.Key}},
				},
			},
		},
		{
			name:                    "PCS with standalone PCLQ and PCSG where topology constraints are set at all levels",
			tasEnabled:              true,
			pcsDeprecatedPackDomain: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "router",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "zone"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
				},
				{
					Name:               "decode-leader",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
				},
				{
					Name:               "decode-worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(1))},
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
					fqn:                    anchorName,
					topologyPackConstraint: &expectedTopologyPackConstraint{requiredKey: topologyLevelZone.Key},
					pclqPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-router":                        {requiredKey: topologyLevelZone.Key},
						"test-pcs-0-scaling-group-0-decode-leader": {requiredKey: topologyLevelHost.Key},
						"test-pcs-0-scaling-group-0-decode-worker": {requiredKey: topologyLevelHost.Key},
					},
					pcsgPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-0": {requiredKey: topologyLevelRack.Key},
					},
				},
				{
					fqn:                    tailName("scaling-group", 1),
					topologyPackConstraint: &expectedTopologyPackConstraint{requiredKey: topologyLevelZone.Key},
					pclqPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-1-decode-leader": {requiredKey: topologyLevelHost.Key},
						"test-pcs-0-scaling-group-1-decode-worker": {requiredKey: topologyLevelHost.Key},
					},
					pcsgPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-1": {requiredKey: topologyLevelRack.Key},
					},
				},
			},
		},
		{
			name:                    "PCS with topology constraints set for PCLQ and PCSG but TAS is disabled",
			tasEnabled:              false,
			pcsDeprecatedPackDomain: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "router",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "zone"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
				},
				{
					Name:               "decode-leader",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
				},
				{
					Name:               "decode-worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(1))},
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
			name:                    "PCS with PCSG where PCSG has nil topology constraints and falls back to PCS level",
			tasEnabled:              true,
			pcsDeprecatedPackDomain: &topologyLevelZone,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "decode-leader",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
				},
				{
					Name:               "decode-worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(1))},
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
					fqn:                    anchorName,
					topologyPackConstraint: &expectedTopologyPackConstraint{requiredKey: topologyLevelZone.Key},
					pclqPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-0-decode-leader": {requiredKey: topologyLevelHost.Key},
						"test-pcs-0-scaling-group-0-decode-worker": {requiredKey: topologyLevelHost.Key},
					},
				},
				{
					fqn:                    tailName("scaling-group", 1),
					topologyPackConstraint: &expectedTopologyPackConstraint{requiredKey: topologyLevelZone.Key},
					pclqPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-1-decode-leader": {requiredKey: topologyLevelHost.Key},
						"test-pcs-0-scaling-group-1-decode-worker": {requiredKey: topologyLevelHost.Key},
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var pcsTopologyConstraint *grovecorev1alpha1.TopologyConstraint
			switch {
			case test.pcsTopologyConstraint != nil:
				pcsTopologyConstraint = test.pcsTopologyConstraint
			case test.pcsDeprecatedPackDomain != nil:
				pcsTopologyConstraint = &grovecorev1alpha1.TopologyConstraint{PackDomain: test.pcsDeprecatedPackDomain.Domain}
			}
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: namespace, UID: "test-uid-123"},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TopologyConstraint:           pcsTopologyConstraint,
						Cliques:                      test.pclqTemplateSpecs,
						PodCliqueScalingGroupConfigs: test.pcsgConfigs,
					},
				},
			}

			// Author the PodGangMap entries from the PCS spec. The anchor entry carries the standalone
			// PodCliques and each PCSG's [0, MinAvailable) indices. When a PCSG has replicas beyond its
			// MinAvailable, a tail entry carries the remaining indices.
			anchorPodCliques := make(map[string]int32)
			for _, clique := range test.pclqTemplateSpecs {
				if componentutils.FindScalingGroupConfigForClique(test.pcsgConfigs, clique.Name) == nil {
					anchorPodCliques[clique.Name] = clique.Spec.Replicas
				}
			}
			anchorPCSGIndices := make(map[string][]int32)
			tailPCSGIndices := make(map[string][]int32)
			for _, cfg := range test.pcsgConfigs {
				minAvail := *cfg.MinAvailable
				var anchorIdx, tailIdx []int32
				for i := int32(0); i < *cfg.Replicas; i++ {
					if i < minAvail {
						anchorIdx = append(anchorIdx, i)
					} else {
						tailIdx = append(tailIdx, i)
					}
				}
				anchorPCSGIndices[cfg.Name] = anchorIdx
				if len(tailIdx) > 0 {
					tailPCSGIndices[cfg.Name] = tailIdx
				}
			}
			entries := []grovecorev1alpha1.PodGangEntry{
				testutils.NewPodGangEntryBuilder(genHash, anchorEpoch).
					WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
					WithPodCliques(anchorPodCliques).
					WithPCSGReplicaIndices(anchorPCSGIndices).
					Build(),
			}
			if len(tailPCSGIndices) > 0 {
				entries = append(entries, testutils.NewPodGangEntryBuilder(genHash, tailEpoch).
					WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
					WithPCSGReplicaIndices(tailPCSGIndices).
					WithDependsOn(anchorEpoch).Build())
			}
			pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, pcs.UID, 0).WithEntries(entries...).Build()

			fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs, pgm).Build()
			r := &_resource{client: fakeClient, schedRegistry: defaultFakeSchedulerRegistry}
			ss := &syncState{
				pcs:            pcs,
				logger:         ctrllogger.FromContext(t.Context()),
				tasEnabled:     test.tasEnabled,
				topologyLevels: clusterTopologyLevels,
			}

			actual, err := r.computeExpectedPodGangs(t.Context(), ss)
			require.NoError(t, err)

			computedAnchorPodGangs := lo.Filter(actual, func(pg *podGangInfo, _ int) bool {
				return pg.fqn == anchorName
			})
			require.Len(t, computedAnchorPodGangs, 1)
			require.Equal(t, test.expectedNumPodGangs, len(actual))

			if !test.tasEnabled {
				mustNotHaveAnyTopologyConstraints(t, actual)
				return
			}
			for _, expectedPGConstraint := range test.expectedPodGangTopologyConstraints {
				computedPodGang, found := lo.Find(actual, func(pg *podGangInfo) bool {
					return pg.fqn == expectedPGConstraint.fqn
				})
				require.True(t, found, "expected PodGang %s not found", expectedPGConstraint.fqn)

				assertPodGangLevelConstraint(t, computedPodGang, expectedPGConstraint)
				assertPCLQConstraints(t, computedPodGang, expectedPGConstraint)
				assertPCSGConstraints(t, computedPodGang, expectedPGConstraint)
			}
		})
	}
}

// TestResolveTopologyLevels verifies that resolveTopologyLevels resolves cluster topology levels from
// the PodCliqueSet's effective topologyName, and returns nil (no error) when there is no constraint,
// no resolvable topologyName, or the referenced ClusterTopologyBinding does not exist. The caller
// gates this on topology-aware scheduling being enabled, so these cases assume TAS is on.
func TestResolveTopologyLevels(t *testing.T) {
	const (
		ns           = "default"
		pcsName      = "test-pcs"
		topologyName = "my-topology"
	)
	ctLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: "zone", Key: "topology.kubernetes.io/zone"},
		{Domain: "rack", Key: "topology.kubernetes.io/rack"},
		{Domain: "host", Key: "kubernetes.io/hostname"},
	}

	tests := []struct {
		name                  string
		buildPCS              func() *grovecorev1alpha1.PodCliqueSet
		clusterTopologyExists bool
		wantTopologyLevels    []grovecorev1alpha1.TopologyLevel
	}{
		{
			name: "PCS-level topologyName set and ClusterTopologyBinding exists resolves levels",
			buildPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(pcsName, ns, "pcs-uid").
					WithStandaloneClique("worker").
					WithTopologyConstraint(&grovecorev1alpha1.TopologyConstraint{TopologyName: topologyName, Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "rack"}}).
					Build()
			},
			clusterTopologyExists: true,
			wantTopologyLevels:    ctLevels,
		},
		{
			name: "PCS-level constraint using the deprecated packDomain field resolves levels",
			buildPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(pcsName, ns, "pcs-uid").
					WithStandaloneClique("worker").
					WithTopologyConstraint(&grovecorev1alpha1.TopologyConstraint{TopologyName: topologyName, PackDomain: "rack"}).
					Build()
			},
			clusterTopologyExists: true,
			wantTopologyLevels:    ctLevels,
		},
		{
			name: "no topology constraint on PCS returns nil",
			buildPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(pcsName, ns, "pcs-uid").
					WithStandaloneClique("worker").
					Build()
			},
			wantTopologyLevels: nil,
		},
		{
			name: "topologyName set only on a child clique constraint resolves levels",
			buildPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(pcsName, ns, "pcs-uid").
					WithPodCliqueTemplateSpec(&grovecorev1alpha1.PodCliqueTemplateSpec{
						Name:               "worker",
						TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{TopologyName: topologyName, Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "rack"}},
						Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))},
					}).
					Build()
			},
			clusterTopologyExists: true,
			wantTopologyLevels:    ctLevels,
		},
		{
			name: "PCS-level topologyName set but ClusterTopologyBinding not found returns nil",
			buildPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(pcsName, ns, "pcs-uid").
					WithStandaloneClique("worker").
					WithTopologyConstraint(&grovecorev1alpha1.TopologyConstraint{TopologyName: "missing-topology", Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "rack"}}).
					Build()
			},
			clusterTopologyExists: false,
			wantTopologyLevels:    nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcs := test.buildPCS()

			objs := []client.Object{pcs}
			if test.clusterTopologyExists {
				resolvedName, err := componentutils.FindExplicitTopologyNameForPodCliqueSet(pcs)
				require.NoError(t, err)
				objs = append(objs, makeClusterTopologyBindingWithLevels(resolvedName, ctLevels))
			}

			fakeClient := testutils.NewTestClientBuilder().WithObjects(objs...).Build()
			r := &_resource{client: fakeClient, schedRegistry: defaultFakeSchedulerRegistry}

			actual, err := r.resolveTopologyLevels(t.Context(), ctrllogger.FromContext(t.Context()), pcs)
			require.NoError(t, err)
			assert.Equal(t, test.wantTopologyLevels, actual)
		})
	}
}

// TestComputeExpectedPodGangs verifies that the materializer turns the PodGangMap entries of each PCS
// replica into the expected set of PodGangs. It covers the permutations of entry roles: an anchor is
// always present, a Tail entry is optional and may hold one or more indices, and a ScaleOut entry is
// optional and may hold one or more indices. Topology constraints are out of scope here (TAS is off).
func TestComputeExpectedPodGangs(t *testing.T) {
	const (
		pcsName       = "test-pcs"
		namespace     = "default"
		genHash       = "test-hash"
		anchorEpoch   = "1000"
		tailEpoch     = "1001"
		scaleOutEpoch = "1002"
	)
	// entrySpec declares the PodGangMap entries for a single PCS replica. The anchor is always present.
	type entrySpec struct {
		anchorPCSGIndices   map[string][]int32
		tailPCSGIndices     map[string][]int32
		scaleOutPCSGIndices map[string][]int32
		// emptyScaleOut requests a ScaleOut entry carrying no replica indices (the pre-created,
		// unused ScaleOut entry the PodGangMap always keeps). The materializer must produce no
		// PodGang for it.
		emptyScaleOut bool
	}
	tests := []struct {
		name        string
		pcsReplicas int32
		pclqs       []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig
		entries     entrySpec
	}{
		{
			name:        "anchor only, standalone cliques, no scaling group",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
			},
			entries: entrySpec{},
		},
		{
			name:        "anchor only, scaling group with replicas equal to minAvailable",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "sg-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(2))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg", Replicas: ptr.To(int32(2)), MinAvailable: ptr.To(int32(2)), CliqueNames: []string{"sg-worker"}},
			},
			entries: entrySpec{anchorPCSGIndices: map[string][]int32{"sg": {0, 1}}},
		},
		{
			name:        "anchor and tail, scaling group with replicas above minAvailable",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "sg-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg", Replicas: ptr.To(int32(3)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"sg-worker"}},
			},
			entries: entrySpec{
				anchorPCSGIndices: map[string][]int32{"sg": {0}},
				tailPCSGIndices:   map[string][]int32{"sg": {1, 2}},
			},
		},
		{
			name:        "anchor and scaleout, scaled out beyond template replicas",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "sg-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg", Replicas: ptr.To(int32(1)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"sg-worker"}},
			},
			entries: entrySpec{
				anchorPCSGIndices:   map[string][]int32{"sg": {0}},
				scaleOutPCSGIndices: map[string][]int32{"sg": {1}},
			},
		},
		{
			name:        "anchor, tail and scaleout together",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "sg-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg", Replicas: ptr.To(int32(3)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"sg-worker"}},
			},
			entries: entrySpec{
				anchorPCSGIndices:   map[string][]int32{"sg": {0}},
				tailPCSGIndices:     map[string][]int32{"sg": {1, 2}},
				scaleOutPCSGIndices: map[string][]int32{"sg": {3}},
			},
		},
		{
			name:        "multiple scaling groups contribute to tail",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker-a", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
				{Name: "worker-b", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg-a", Replicas: ptr.To(int32(3)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"worker-a"}},
				{Name: "sg-b", Replicas: ptr.To(int32(2)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"worker-b"}},
			},
			entries: entrySpec{
				anchorPCSGIndices: map[string][]int32{"sg-a": {0}, "sg-b": {0}},
				tailPCSGIndices:   map[string][]int32{"sg-a": {1, 2}, "sg-b": {1}},
			},
		},
		{
			name:        "multiple PCS replicas each with anchor and tail",
			pcsReplicas: 2,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg", Replicas: ptr.To(int32(2)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"worker"}},
			},
			entries: entrySpec{
				anchorPCSGIndices: map[string][]int32{"sg": {0}},
				tailPCSGIndices:   map[string][]int32{"sg": {1}},
			},
		},
		{
			name:        "scaleout entry with multiple indices",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "sg-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg", Replicas: ptr.To(int32(1)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"sg-worker"}},
			},
			entries: entrySpec{
				anchorPCSGIndices:   map[string][]int32{"sg": {0}},
				scaleOutPCSGIndices: map[string][]int32{"sg": {1, 2}},
			},
		},
		{
			name:        "empty scaleout entry materializes no PodGang",
			pcsReplicas: 1,
			pclqs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "sg-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg", Replicas: ptr.To(int32(1)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"sg-worker"}},
			},
			entries: entrySpec{
				anchorPCSGIndices: map[string][]int32{"sg": {0}},
				emptyScaleOut:     true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: namespace, UID: "test-uid-123"},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: test.pcsReplicas,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques:                      test.pclqs,
						PodCliqueScalingGroupConfigs: test.pcsgConfigs,
					},
				},
			}

			// Standalone pod counts carried by the anchor entry, keyed by clique name.
			anchorPodCliques := make(map[string]int32)
			for _, clique := range test.pclqs {
				if componentutils.FindScalingGroupConfigForClique(test.pcsgConfigs, clique.Name) == nil {
					anchorPodCliques[clique.Name] = clique.Spec.Replicas
				}
			}

			// Seed one PodGangMap per replica from the declared entries, and derive the expected
			// PodGang names by role from the same entries.
			var expectedAnchorNames, expectedTailNames, expectedScaleOutNames []string
			objs := []client.Object{pcs}
			for replicaIndex := range int(test.pcsReplicas) {
				rnr := apicommon.ResourceNameReplica{Name: pcsName, Replica: replicaIndex}
				entries := []grovecorev1alpha1.PodGangEntry{
					testutils.NewPodGangEntryBuilder(genHash, anchorEpoch).
						WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
						WithPodCliques(anchorPodCliques).
						WithPCSGReplicaIndices(test.entries.anchorPCSGIndices).
						Build(),
				}
				expectedAnchorNames = append(expectedAnchorNames, apicommon.GenerateAnchorPodGangName(rnr, anchorEpoch))
				if len(test.entries.tailPCSGIndices) > 0 {
					entries = append(entries, testutils.NewPodGangEntryBuilder(genHash, tailEpoch).
						WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
						WithPCSGReplicaIndices(test.entries.tailPCSGIndices).
						WithDependsOn(anchorEpoch).Build())
					expectedTailNames = append(expectedTailNames, nonAnchorPodGangNames(rnr, tailEpoch, test.pcsgConfigs, test.entries.tailPCSGIndices)...)
				}
				if len(test.entries.scaleOutPCSGIndices) > 0 || test.entries.emptyScaleOut {
					entries = append(entries, testutils.NewPodGangEntryBuilder(genHash, scaleOutEpoch).
						WithRole(grovecorev1alpha1.PodGangEntryRoleScaleOut).
						WithPCSGReplicaIndices(test.entries.scaleOutPCSGIndices).
						WithDependsOn(anchorEpoch).Build())
					expectedScaleOutNames = append(expectedScaleOutNames, nonAnchorPodGangNames(rnr, scaleOutEpoch, test.pcsgConfigs, test.entries.scaleOutPCSGIndices)...)
				}
				objs = append(objs, testutils.NewPodGangMapBuilder(pcsName, namespace, pcs.UID, replicaIndex).WithEntries(entries...).Build())
			}

			fakeClient := testutils.NewTestClientBuilder().WithObjects(objs...).Build()
			r := &_resource{client: fakeClient, schedRegistry: defaultFakeSchedulerRegistry}
			ss := &syncState{pcs: pcs, logger: ctrllogger.FromContext(t.Context())}

			actual, err := r.computeExpectedPodGangs(t.Context(), ss)
			require.NoError(t, err)

			actualNamesByRole := podGangNamesByRole(actual)
			assert.ElementsMatch(t, expectedAnchorNames, actualNamesByRole[grovecorev1alpha1.PodGangEntryRoleAnchor], "anchor PodGangs")
			assert.ElementsMatch(t, expectedTailNames, actualNamesByRole[grovecorev1alpha1.PodGangEntryRoleTail], "tail PodGangs")
			assert.ElementsMatch(t, expectedScaleOutNames, actualNamesByRole[grovecorev1alpha1.PodGangEntryRoleScaleOut], "scaleout PodGangs")
			assert.Len(t, actual, len(expectedAnchorNames)+len(expectedTailNames)+len(expectedScaleOutNames))
		})
	}
}

// TestBuildStandalonePCLQInfosForAnchorEntry verifies the anchor entry's standalone PodClique counts
// become pclqInfos with the fields sourced from the template, that a clique absent from the entry is
// skipped, and that a clique carrying a zero count (left by a scale-in on an anchor that survives for
// its other constituents) is skipped so no PodGroup with zero pods but a positive MinReplicas results.
func TestBuildStandalonePCLQInfosForAnchorEntry(t *testing.T) {
	const (
		pcsName   = "test-pcs"
		namespace = "default"
	)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: namespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
					{Name: "aux", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
				},
			},
		},
	}
	pclqName := func(clique string) string {
		return apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}, clique)
	}

	tests := []struct {
		name        string
		podCliques  map[string]int32
		expectedFQN []string
	}{
		{
			name:        "present cliques become pclqInfos",
			podCliques:  map[string]int32{"worker": 3, "aux": 1},
			expectedFQN: []string{pclqName("worker"), pclqName("aux")},
		},
		{
			name:        "a clique absent from the entry is skipped",
			podCliques:  map[string]int32{"worker": 3},
			expectedFQN: []string{pclqName("worker")},
		},
		{
			name:        "a zero-count clique is skipped",
			podCliques:  map[string]int32{"worker": 3, "aux": 0},
			expectedFQN: []string{pclqName("worker")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ss := &syncState{pcs: pcs, logger: ctrllogger.FromContext(t.Context())}
			entry := testutils.NewPodGangEntryBuilder("hash", "1000").
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
				WithPodCliques(test.podCliques).Build()

			actual := buildStandalonePCLQInfosForAnchorEntry(ss, 0, entry)

			actualFQNs := lo.Map(actual, func(pi pclqInfo, _ int) string { return pi.fqn })
			assert.ElementsMatch(t, test.expectedFQN, actualFQNs)
			for _, pi := range actual {
				assert.True(t, pi.isStandalone, "standalone cliques must be marked standalone")
				if pi.fqn == pclqName("worker") {
					assert.Equal(t, int32(3), pi.replicas)
					assert.Equal(t, int32(2), pi.minAvailable)
				}
			}
		})
	}
}

// TestGetExistingPCLQsForPCS verifies that getExistingPCLQsForPCS returns exactly the PodCliques
// selected by the PodCliqueSet managed-resource labels, regardless of whether the PodClique is owned
// directly by the PCS (standalone) or by a PodCliqueScalingGroup.
func TestGetExistingPCLQsForPCS(t *testing.T) {
	const (
		pcsName   = "test-pcs"
		namespace = "default"
	)
	pcsLabels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)

	standalonePCLQ := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-0-worker", Namespace: namespace, Labels: pcsLabels},
	}
	pcsgPCLQ := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-0-sg-0-worker", Namespace: namespace, Labels: pcsLabels},
	}
	// A PodClique belonging to a different PodCliqueSet must not be returned.
	otherPCLQ := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-pcs-0-worker",
			Namespace: namespace,
			Labels:    apicommon.GetDefaultLabelsForPodCliqueSetManagedResources("other-pcs"),
		},
	}

	pcs := &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: namespace}}
	fakeClient := testutils.NewTestClientBuilder().WithObjects(standalonePCLQ, pcsgPCLQ, otherPCLQ).Build()
	r := &_resource{client: fakeClient, schedRegistry: defaultFakeSchedulerRegistry}

	actual, err := r.getExistingPCLQsForPCS(t.Context(), pcs)
	require.NoError(t, err)

	actualNames := lo.Map(actual, func(pclq grovecorev1alpha1.PodClique, _ int) string { return pclq.Name })
	assert.ElementsMatch(t, []string{standalonePCLQ.Name, pcsgPCLQ.Name}, actualNames)
}

// TestGetExistingPodsByPCLQForPCS verifies that getExistingPodsByPCLQForPCS groups non-terminating
// pods by their owning PodClique, skips terminating pods, and ignores pods belonging to another
// PodCliqueSet.
func TestGetExistingPodsByPCLQForPCS(t *testing.T) {
	const (
		pcsName   = "test-pcs"
		namespace = "default"
	)
	pcsLabels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)

	makePod := func(name, ownerPCLQ string, labels map[string]string, terminating bool) *v1.Pod {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				Namespace:       namespace,
				Labels:          labels,
				OwnerReferences: []metav1.OwnerReference{{Name: ownerPCLQ, Controller: ptr.To(true)}},
			},
		}
		if terminating {
			pod.DeletionTimestamp = ptr.To(metav1.Now())
			pod.Finalizers = []string{"grove.io/test"}
		}
		return pod
	}

	worker0 := makePod("worker-0", "test-pcs-0-worker", pcsLabels, false)
	worker1 := makePod("worker-1", "test-pcs-0-worker", pcsLabels, false)
	leader0 := makePod("leader-0", "test-pcs-0-leader", pcsLabels, false)
	terminating := makePod("worker-2", "test-pcs-0-worker", pcsLabels, true)
	otherPCSPod := makePod("other-0", "other-pcs-0-worker", apicommon.GetDefaultLabelsForPodCliqueSetManagedResources("other-pcs"), false)

	fakeClient := testutils.NewTestClientBuilder().WithObjects(worker0, worker1, leader0, terminating, otherPCSPod).Build()
	r := &_resource{client: fakeClient, schedRegistry: defaultFakeSchedulerRegistry}

	actual, err := r.getExistingPodsByPCLQForPCS(t.Context(), client.ObjectKey{Namespace: namespace, Name: pcsName})
	require.NoError(t, err)

	assert.Len(t, actual, 2)
	assert.ElementsMatch(t, []string{"worker-0", "worker-1"}, podNames(actual["test-pcs-0-worker"]))
	assert.ElementsMatch(t, []string{"leader-0"}, podNames(actual["test-pcs-0-leader"]))
}

// TestInitializeAssignedAndUnassignedPodsForPCS verifies the in-memory bucketing: a pod labeled with a
// known expected PodGang is associated to that PodGang's constituent PodClique, a pod without the
// PodGang label is recorded as unassigned, and a pod labeled with an unknown PodGang is dropped.
func TestInitializeAssignedAndUnassignedPodsForPCS(t *testing.T) {
	const pclqName = "test-pcs-0-worker"

	makePod := func(name, podGangLabel string) v1.Pod {
		pod := v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
		if podGangLabel != "" {
			pod.Labels = map[string]string{apicommon.LabelPodGang: podGangLabel}
		}
		return pod
	}

	assignedPodGang := &podGangInfo{fqn: "test-pcs-0-1000", pclqs: []pclqInfo{{fqn: pclqName}}}
	ss := &syncState{
		existingPCLQPods: map[string][]v1.Pod{
			pclqName: {
				makePod("assigned-0", "test-pcs-0-1000"),
				makePod("unassigned-0", ""),
				makePod("unknown-0", "test-pcs-0-9999"),
			},
		},
		expectedPodGangByName: map[string]*podGangInfo{assignedPodGang.fqn: assignedPodGang},
		unassignedPodsByPCLQ:  make(map[string][]v1.Pod),
	}

	ss.initializeAssignedAndUnassignedPodsForPCS()

	assert.Equal(t, []string{"assigned-0"}, assignedPodGang.pclqs[0].associatedPodNames)
	assert.ElementsMatch(t, []string{"unassigned-0"}, podNames(ss.unassignedPodsByPCLQ[pclqName]))
}

// TestArePodGangMinReplicasScheduled verifies arePodGangMinReplicasScheduled counts only pods that
// are associated to the PodGang (named in associatedPodNames) and scheduled, and requires every
// constituent PodClique to meet MinReplicas.
func TestArePodGangMinReplicasScheduled(t *testing.T) {
	tests := []struct {
		name         string
		existingPods map[string][]v1.Pod
		podGang      *podGangInfo
		want         bool
	}{
		{
			name:         "all associated pods scheduled meets MinReplicas",
			existingPods: map[string][]v1.Pod{"pclq-a": {scheduledTestPod("a1"), scheduledTestPod("a2")}},
			podGang:      &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", minAvailable: 2, associatedPodNames: []string{"a1", "a2"}}}},
			want:         true,
		},
		{
			name:         "fewer scheduled pods than MinReplicas",
			existingPods: map[string][]v1.Pod{"pclq-a": {scheduledTestPod("a1"), unscheduledTestPod("a2")}},
			podGang:      &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", minAvailable: 2, associatedPodNames: []string{"a1", "a2"}}}},
			want:         false,
		},
		{
			name:         "scheduled pod not in associatedPodNames is not counted",
			existingPods: map[string][]v1.Pod{"pclq-a": {scheduledTestPod("a1"), scheduledTestPod("a2")}},
			podGang:      &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", minAvailable: 2, associatedPodNames: []string{"a1"}}}},
			want:         false,
		},
		{
			name: "one PodClique below MinReplicas fails the whole PodGang",
			existingPods: map[string][]v1.Pod{
				"pclq-a": {scheduledTestPod("a1"), scheduledTestPod("a2")},
				"pclq-b": {scheduledTestPod("b1")},
			},
			podGang: &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{
				{fqn: "pclq-a", minAvailable: 2, associatedPodNames: []string{"a1", "a2"}},
				{fqn: "pclq-b", minAvailable: 2, associatedPodNames: []string{"b1"}},
			}},
			want: false,
		},
		{
			name:         "more scheduled pods than MinReplicas still meets it",
			existingPods: map[string][]v1.Pod{"pclq-a": {scheduledTestPod("a1"), scheduledTestPod("a2"), scheduledTestPod("a3")}},
			podGang:      &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", minAvailable: 2, associatedPodNames: []string{"a1", "a2", "a3"}}}},
			want:         true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := &syncState{existingPCLQPods: tc.existingPods}
			r := &_resource{}
			actual := r.arePodGangMinReplicasScheduled(ss, tc.podGang)
			assert.Equal(t, tc.want, actual)
		})
	}
}

// TestArePodGangMinReplicasReady verifies arePodGangMinReplicasReady counts only pods that are
// associated to the PodGang (named in associatedPodNames) and ready, and requires every
// constituent PodClique to meet MinReplicas. A scheduled-but-not-ready pod does not count.
func TestArePodGangMinReplicasReady(t *testing.T) {
	tests := []struct {
		name         string
		existingPods map[string][]v1.Pod
		podGang      *podGangInfo
		want         bool
	}{
		{
			name:         "all associated pods ready meets MinReplicas",
			existingPods: map[string][]v1.Pod{"pclq-a": {readyTestPod("a1"), readyTestPod("a2")}},
			podGang:      &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", minAvailable: 2, associatedPodNames: []string{"a1", "a2"}}}},
			want:         true,
		},
		{
			name:         "scheduled but not ready does not meet MinReplicas",
			existingPods: map[string][]v1.Pod{"pclq-a": {scheduledTestPod("a1"), scheduledTestPod("a2")}},
			podGang:      &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", minAvailable: 2, associatedPodNames: []string{"a1", "a2"}}}},
			want:         false,
		},
		{
			name:         "ready pod not in associatedPodNames is not counted",
			existingPods: map[string][]v1.Pod{"pclq-a": {readyTestPod("a1"), readyTestPod("a2")}},
			podGang:      &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", minAvailable: 2, associatedPodNames: []string{"a1"}}}},
			want:         false,
		},
		{
			name: "one PodClique below MinReplicas fails the whole PodGang",
			existingPods: map[string][]v1.Pod{
				"pclq-a": {readyTestPod("a1"), readyTestPod("a2")},
				"pclq-b": {readyTestPod("b1")},
			},
			podGang: &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{
				{fqn: "pclq-a", minAvailable: 2, associatedPodNames: []string{"a1", "a2"}},
				{fqn: "pclq-b", minAvailable: 2, associatedPodNames: []string{"b1"}},
			}},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := &syncState{existingPCLQPods: tc.existingPods}
			r := &_resource{}
			actual := r.arePodGangMinReplicasReady(ss, tc.podGang)
			assert.Equal(t, tc.want, actual)
		})
	}
}

// TestSetScheduledCondition verifies setScheduledCondition sets the Scheduled condition from the live
// scheduled count and stamps LastScheduled on a fresh transition to True, leaves it unchanged while the
// PodGang stays scheduled, advances it when the PodGang is scheduled again after going unscheduled, and
// backfills it when the condition is already True but LastScheduled was never set.
func TestSetScheduledCondition(t *testing.T) {
	schedCond := func(status metav1.ConditionStatus) []metav1.Condition {
		return []metav1.Condition{{Type: string(groveschedulerv1alpha1.PodGangConditionTypeScheduled), Status: status}}
	}
	earlier := metav1.NewTime(time.Now().Add(-time.Hour))
	now := metav1.NewTime(time.Now())

	tests := []struct {
		name                 string
		originalStatus       groveschedulerv1alpha1.PodGangStatus
		currentLastScheduled *metav1.Time
		minReplicasScheduled bool
		wantConditionTrue    bool
		wantSet              bool
		wantAdvanced         bool
	}{
		{
			name:                 "not scheduled before or now - stays nil",
			originalStatus:       groveschedulerv1alpha1.PodGangStatus{Conditions: schedCond(metav1.ConditionFalse)},
			minReplicasScheduled: false,
			wantConditionTrue:    false,
			wantSet:              false,
		},
		{
			name:                 "transitions to scheduled this reconcile - sets LastScheduled",
			originalStatus:       groveschedulerv1alpha1.PodGangStatus{Conditions: schedCond(metav1.ConditionFalse)},
			minReplicasScheduled: true,
			wantConditionTrue:    true,
			wantSet:              true,
			wantAdvanced:         true,
		},
		{
			name:                 "stays scheduled - does not change existing LastScheduled",
			originalStatus:       groveschedulerv1alpha1.PodGangStatus{Conditions: schedCond(metav1.ConditionTrue)},
			currentLastScheduled: &earlier,
			minReplicasScheduled: true,
			wantConditionTrue:    true,
			wantSet:              true,
			wantAdvanced:         false,
		},
		{
			name:                 "scheduled again after previously going unscheduled - advances LastScheduled",
			originalStatus:       groveschedulerv1alpha1.PodGangStatus{Conditions: schedCond(metav1.ConditionFalse)},
			currentLastScheduled: &earlier,
			minReplicasScheduled: true,
			wantConditionTrue:    true,
			wantSet:              true,
			wantAdvanced:         true,
		},
		{
			name:                 "already scheduled with no LastScheduled - backfills on upgrade",
			originalStatus:       groveschedulerv1alpha1.PodGangStatus{Conditions: schedCond(metav1.ConditionTrue)},
			minReplicasScheduled: true,
			wantConditionTrue:    true,
			wantSet:              true,
			wantAdvanced:         true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pg := &groveschedulerv1alpha1.PodGang{Status: groveschedulerv1alpha1.PodGangStatus{LastScheduled: tc.currentLastScheduled}}
			setScheduledCondition(pg, &tc.originalStatus, tc.minReplicasScheduled, now)
			assert.Equal(t, tc.wantConditionTrue, meta.IsStatusConditionTrue(pg.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeScheduled)))
			actual := pg.Status.LastScheduled
			if !tc.wantSet {
				assert.Nil(t, actual)
				return
			}
			require.NotNil(t, actual)
			if tc.wantAdvanced {
				assert.Equal(t, now, *actual, "LastScheduled should advance to now")
			} else {
				assert.Equal(t, earlier, *actual, "LastScheduled should be unchanged")
			}
		})
	}
}

// TestSetReadyCondition verifies setReadyCondition sets the Ready condition from the live ready count
// and stamps LastReady on a fresh transition to True, leaves it unchanged while the PodGang stays
// ready, advances it when the PodGang is ready again after going not-ready, and backfills it when the
// condition is already True but LastReady was never set.
func TestSetReadyCondition(t *testing.T) {
	readyCond := func(status metav1.ConditionStatus) []metav1.Condition {
		return []metav1.Condition{{Type: string(groveschedulerv1alpha1.PodGangConditionTypeReady), Status: status}}
	}
	earlier := metav1.NewTime(time.Now().Add(-time.Hour))
	now := metav1.NewTime(time.Now())

	tests := []struct {
		name              string
		originalStatus    groveschedulerv1alpha1.PodGangStatus
		currentLastReady  *metav1.Time
		minReplicasReady  bool
		wantConditionTrue bool
		wantSet           bool
		wantAdvanced      bool
	}{
		{
			name:              "not ready before or now - stays nil",
			originalStatus:    groveschedulerv1alpha1.PodGangStatus{Conditions: readyCond(metav1.ConditionFalse)},
			minReplicasReady:  false,
			wantConditionTrue: false,
			wantSet:           false,
		},
		{
			name:              "transitions to ready this reconcile - sets LastReady",
			originalStatus:    groveschedulerv1alpha1.PodGangStatus{Conditions: readyCond(metav1.ConditionFalse)},
			minReplicasReady:  true,
			wantConditionTrue: true,
			wantSet:           true,
			wantAdvanced:      true,
		},
		{
			name:              "stays ready - does not change existing LastReady",
			originalStatus:    groveschedulerv1alpha1.PodGangStatus{Conditions: readyCond(metav1.ConditionTrue)},
			currentLastReady:  &earlier,
			minReplicasReady:  true,
			wantConditionTrue: true,
			wantSet:           true,
			wantAdvanced:      false,
		},
		{
			name:              "ready again after previously going not-ready - advances LastReady",
			originalStatus:    groveschedulerv1alpha1.PodGangStatus{Conditions: readyCond(metav1.ConditionFalse)},
			currentLastReady:  &earlier,
			minReplicasReady:  true,
			wantConditionTrue: true,
			wantSet:           true,
			wantAdvanced:      true,
		},
		{
			name:              "already ready with no LastReady - backfills on upgrade",
			originalStatus:    groveschedulerv1alpha1.PodGangStatus{Conditions: readyCond(metav1.ConditionTrue)},
			minReplicasReady:  true,
			wantConditionTrue: true,
			wantSet:           true,
			wantAdvanced:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pg := &groveschedulerv1alpha1.PodGang{Status: groveschedulerv1alpha1.PodGangStatus{LastReady: tc.currentLastReady}}
			setReadyCondition(pg, &tc.originalStatus, tc.minReplicasReady, now)
			assert.Equal(t, tc.wantConditionTrue, meta.IsStatusConditionTrue(pg.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeReady)))
			actual := pg.Status.LastReady
			if !tc.wantSet {
				assert.Nil(t, actual)
				return
			}
			require.NotNil(t, actual)
			if tc.wantAdvanced {
				assert.Equal(t, now, *actual, "LastReady should advance to now")
			} else {
				assert.Equal(t, earlier, *actual, "LastReady should be unchanged")
			}
		})
	}
}

// TestReconcilePodGangStatus verifies reconcilePodGangStatus patches the live PodGang's Scheduled
// and Ready conditions from live pod observation on every call, sets the Initialized latch only
// when allPodsCreated is true, skips the patch when the status is already current, requeues on a
// conflicting patch, and surfaces a non-conflict patch failure as an error.
func TestReconcilePodGangStatus(t *testing.T) {
	const (
		ns       = "default"
		pgName   = "test-pcs-0-1000"
		pclqName = "test-pcs-0-worker"
	)
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", ns, "uid").Build()
	newResource := func(cl client.Client) *_resource {
		return &_resource{
			client:        cl,
			scheme:        groveclientscheme.Scheme,
			eventRecorder: record.NewFakeRecorder(10),
			schedRegistry: defaultFakeSchedulerRegistry,
		}
	}
	pgi := &podGangInfo{fqn: pgName, pcsReplicaIndex: 0, pclqs: []pclqInfo{
		{fqn: pclqName, replicas: 2, minAvailable: 2, associatedPodNames: []string{"worker-0", "worker-1"}},
	}}
	existingPodGang := func() *groveschedulerv1alpha1.PodGang {
		return &groveschedulerv1alpha1.PodGang{ObjectMeta: metav1.ObjectMeta{Name: pgName, Namespace: ns}}
	}
	readyState := func() *syncState {
		return &syncState{
			pcs:              pcs,
			logger:           ctrllogger.FromContext(t.Context()),
			existingPCLQPods: map[string][]v1.Pod{pclqName: {readyTestPod("worker-0"), readyTestPod("worker-1")}},
		}
	}

	t.Run("allPodsCreated true - sets Initialized and patches Scheduled and Ready True with timestamps", func(t *testing.T) {
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs, existingPodGang()).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := newResource(cl)

		err := r.reconcilePodGangStatus(t.Context(), readyState(), pgi, true)

		require.NoError(t, err)
		patched := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: pgName}, patched))
		assert.True(t, meta.IsStatusConditionTrue(patched.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized)))
		assert.True(t, meta.IsStatusConditionTrue(patched.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeScheduled)))
		assert.True(t, meta.IsStatusConditionTrue(patched.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeReady)))
		assert.NotNil(t, patched.Status.LastScheduled)
		assert.NotNil(t, patched.Status.LastReady)
	})

	t.Run("allPodsCreated false - does not set Initialized but still reconciles Scheduled and Ready", func(t *testing.T) {
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs, existingPodGang()).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := newResource(cl)

		err := r.reconcilePodGangStatus(t.Context(), readyState(), pgi, false)

		require.NoError(t, err)
		patched := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: pgName}, patched))
		assert.Nil(t, meta.FindStatusCondition(patched.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized)), "Initialized must not be set when allPodsCreated is false")
		assert.True(t, meta.IsStatusConditionTrue(patched.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeScheduled)))
		assert.True(t, meta.IsStatusConditionTrue(patched.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeReady)))
	})

	t.Run("does not patch when status is already current", func(t *testing.T) {
		seeded := existingPodGang()
		setPodGangCondition(seeded, groveschedulerv1alpha1.PodGangConditionTypeInitialized, metav1.ConditionTrue,
			groveschedulerv1alpha1.ConditionReasonPodGangPodsCreated, "PodGang is fully initialized")
		setScheduledCondition(seeded, &groveschedulerv1alpha1.PodGangStatus{}, true, metav1.Now())
		setReadyCondition(seeded, &groveschedulerv1alpha1.PodGangStatus{}, true, metav1.Now())

		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs, seeded).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			RecordErrorForObjects(testutils.ClientMethodStatusPatch,
				apierrors.NewInternalError(errors.New("patch must not be called when status is unchanged")),
				client.ObjectKey{Namespace: ns, Name: pgName}).
			Build()
		r := newResource(cl)

		// Read the seeded LastScheduled back from the client so it carries the same store-truncated
		// precision as the value asserted on after reconcile.
		initial := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: pgName}, initial))
		wantLastScheduled := initial.Status.LastScheduled

		err := r.reconcilePodGangStatus(t.Context(), readyState(), pgi, true)

		// The recorded StatusPatch error fails the test if a patch is issued. A nil error therefore
		// proves the DeepEqual guard skipped the patch because the status was already current.
		require.NoError(t, err, "an already-current status must not trigger a patch")
		patched := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: pgName}, patched))
		assert.Equal(t, wantLastScheduled, patched.Status.LastScheduled, "LastScheduled must be unchanged")
	})

	t.Run("requeues instead of erroring on a status patch conflict", func(t *testing.T) {
		conflict := apierrors.NewConflict(
			schema.GroupResource{Group: groveschedulerv1alpha1.SchemeGroupVersion.Group, Resource: "podgangs"},
			pgName, errors.New("object was modified"))
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs, existingPodGang()).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			RecordErrorForObjects(testutils.ClientMethodStatusPatch, conflict, client.ObjectKey{Namespace: ns, Name: pgName}).
			Build()
		r := newResource(cl)

		err := r.reconcilePodGangStatus(t.Context(), readyState(), pgi, true)

		testutils.AssertGroveError(t, &groveerr.GroveError{Code: groveerr.ErrCodeRequeueAfter, Operation: component.OperationSync}, err)
	})

	t.Run("returns error on a non-conflict status patch failure", func(t *testing.T) {
		internalErr := apierrors.NewInternalError(errors.New("apiserver boom"))
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs, existingPodGang()).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			RecordErrorForObjects(testutils.ClientMethodStatusPatch, internalErr, client.ObjectKey{Namespace: ns, Name: pgName}).
			Build()
		r := newResource(cl)

		err := r.reconcilePodGangStatus(t.Context(), readyState(), pgi, true)

		testutils.AssertGroveError(t, &groveerr.GroveError{Code: errCodeUpdatePodGangStatus, Cause: internalErr, Operation: component.OperationSync}, err)
	})
}

// TestBuildAdditionalLabelsFromPodGangEntry pins that a PodGang materialized from an entry carries the
// entry's generation hash, epoch and role.
func TestBuildAdditionalLabelsFromPodGangEntry(t *testing.T) {
	entry := grovecorev1alpha1.PodGangEntry{
		Epoch:                      "1500",
		Role:                       grovecorev1alpha1.PodGangEntryRoleAnchor,
		PodCliqueSetGenerationHash: "hash-1",
	}

	actual := buildAdditionalLabelsFromPodGangEntry(entry)

	expected := map[string]string{
		apicommon.LabelEpoch:                      "1500",
		apicommon.LabelPodGangRole:                string(grovecorev1alpha1.PodGangEntryRoleAnchor),
		apicommon.LabelPodCliqueSetGenerationHash: "hash-1",
	}
	assert.Equal(t, expected, actual)
}

func podNames(pods []v1.Pod) []string {
	return lo.Map(pods, func(pod v1.Pod, _ int) string { return pod.Name })
}

// scheduledTestPod returns a Pod with the PodScheduled condition True.
func scheduledTestPod(name string) v1.Pod {
	return *testutils.NewPodBuilder(name, "default").
		WithCondition(v1.PodCondition{Type: v1.PodScheduled, Status: v1.ConditionTrue}).
		Build()
}

// readyTestPod returns a Pod with both the PodScheduled and PodReady conditions True.
func readyTestPod(name string) v1.Pod {
	return *testutils.NewPodBuilder(name, "default").
		WithCondition(v1.PodCondition{Type: v1.PodScheduled, Status: v1.ConditionTrue}).
		WithCondition(v1.PodCondition{Type: v1.PodReady, Status: v1.ConditionTrue}).
		Build()
}

// unscheduledTestPod returns a Pod with no scheduling or readiness conditions.
func unscheduledTestPod(name string) v1.Pod {
	return *testutils.NewPodBuilder(name, "default").Build()
}

func assertPodGangLevelConstraint(t *testing.T, pg *podGangInfo, expected expectedPodGangTopologyConstraints) {
	t.Helper()
	if expected.topologyPackConstraint == nil {
		assert.Nil(t, pg.topologyConstraint)
		return
	}
	assertPackConstraint(t, pg.topologyConstraint, *expected.topologyPackConstraint)
}

func assertPCLQConstraints(t *testing.T, pg *podGangInfo, expected expectedPodGangTopologyConstraints) {
	t.Helper()
	for _, pclq := range pg.pclqs {
		want, exists := expected.pclqPackConstraints[pclq.fqn]
		if !exists {
			assert.Nil(t, pclq.topologyConstraint, "PCLQ %s should have no topology constraint", pclq.fqn)
			continue
		}
		assertPackConstraint(t, pclq.topologyConstraint, want)
	}
}

func assertPCSGConstraints(t *testing.T, pg *podGangInfo, expected expectedPodGangTopologyConstraints) {
	t.Helper()
	for pcsgFQN, want := range expected.pcsgPackConstraints {
		actualPCSGTC, found := lo.Find(pg.pcsgTopologyConstraints, func(pcsgTC groveschedulerv1alpha1.TopologyConstraintGroupConfig) bool {
			return pcsgTC.Name == pcsgFQN
		})
		assert.True(t, found, "expected PCSG topology constraint for %s not found", pcsgFQN)
		assertPackConstraint(t, actualPCSGTC.TopologyConstraint, want)
	}
	for _, actualPCSGTC := range pg.pcsgTopologyConstraints {
		if _, exists := expected.pcsgPackConstraints[actualPCSGTC.Name]; !exists {
			t.Errorf("unexpected PCSG topology constraint for %s found in PodGang %s", actualPCSGTC.Name, pg.fqn)
		}
	}
}

func mustNotHaveAnyTopologyConstraints(t *testing.T, podGangs []*podGangInfo) {
	for _, pg := range podGangs {
		assert.Nil(t, pg.topologyConstraint)
		for _, pclq := range pg.pclqs {
			assert.Nil(t, pclq.topologyConstraint)
		}
		assert.Nil(t, pg.pcsgTopologyConstraints)
	}
}

// assertPackConstraint checks both required and preferred keys of a TopologyConstraint. An empty
// expected key asserts the corresponding side is nil; a non-empty key asserts the value matches.
func assertPackConstraint(t *testing.T, got *groveschedulerv1alpha1.TopologyConstraint, want expectedTopologyPackConstraint) {
	t.Helper()
	if want.requiredKey == "" && want.preferredKey == "" {
		assert.Nil(t, got)
		return
	}
	require.NotNil(t, got)
	require.NotNil(t, got.PackConstraint)
	if want.requiredKey == "" {
		assert.Nil(t, got.PackConstraint.Required, "expected no required key")
	} else {
		require.NotNil(t, got.PackConstraint.Required)
		assert.Equal(t, want.requiredKey, *got.PackConstraint.Required)
	}
	if want.preferredKey == "" {
		assert.Nil(t, got.PackConstraint.Preferred, "expected no preferred key")
	} else {
		require.NotNil(t, got.PackConstraint.Preferred)
		assert.Equal(t, want.preferredKey, *got.PackConstraint.Preferred)
	}
}

// nonAnchorPodGangNames returns the non-anchor PodGang names for the given epoch and PCSG replica
// indices, iterating PCSG configs in order for deterministic output.
func nonAnchorPodGangNames(rnr apicommon.ResourceNameReplica, epoch string, pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig, indicesByPCSG map[string][]int32) []string {
	var names []string
	for _, pcsgConfig := range pcsgConfigs {
		for _, idx := range indicesByPCSG[pcsgConfig.Name] {
			names = append(names, apicommon.GenerateNonAnchorPodGangName(rnr, epoch, pcsgConfig.Name, idx))
		}
	}
	return names
}

// podGangNamesByRole groups the materialized PodGang names by the role recorded on their
// grove.io/podgang-role label.
func podGangNamesByRole(podGangs []*podGangInfo) map[grovecorev1alpha1.PodGangEntryRole][]string {
	byRole := make(map[grovecorev1alpha1.PodGangEntryRole][]string)
	for _, pg := range podGangs {
		role := grovecorev1alpha1.PodGangEntryRole(pg.extraLabels[apicommon.LabelPodGangRole])
		byRole[role] = append(byRole[role], pg.fqn)
	}
	return byRole
}

func makeClusterTopologyBindingWithLevels(name string, levels []grovecorev1alpha1.TopologyLevel) *grovecorev1alpha1.ClusterTopologyBinding {
	return &grovecorev1alpha1.ClusterTopologyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       grovecorev1alpha1.ClusterTopologyBindingSpec{Levels: levels},
	}
}
