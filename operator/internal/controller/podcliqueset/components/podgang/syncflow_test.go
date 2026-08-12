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
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			sc := &syncState{
				logger:             ctrllogger.FromContext(t.Context()).WithName("test"),
				existingPCLQPods:   tt.existingPods,
				existingPCLQs:      tt.existingPCLQs,
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

// TestGetPodsPendingCreation verifies getPodsPendingCreationOrAssociation, which counts how many pods
// a PodGang still needs before it is ready: pods from PodCliques that do not exist yet (counted at
// the expected replica count), plus, for existing PodCliques, the shortfall between desired replicas
// and live pods, plus any live pods that are not yet labeled for this PodGang (missing or mismatched
// grove.io/podgang label).
func TestGetPodsPendingCreation(t *testing.T) {
	const podGangName = "test-pcs-0-1000"

	makePCLQ := func(name string, replicas int32) grovecorev1alpha1.PodClique {
		return grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       grovecorev1alpha1.PodCliqueSpec{Replicas: replicas},
		}
	}
	makePod := func(name, podGangLabel string) v1.Pod {
		pod := v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
		if podGangLabel != "" {
			pod.Labels = map[string]string{apicommon.LabelPodGang: podGangLabel}
		}
		return pod
	}

	tests := []struct {
		name            string
		podGang         *podGangInfo
		existingPCLQs   []grovecorev1alpha1.PodClique
		existingPods    map[string][]v1.Pod
		expectedPending int
	}{
		{
			name:            "PodClique does not exist yet counts its expected replicas",
			podGang:         &podGangInfo{fqn: podGangName, pclqs: []pclqInfo{{fqn: "worker", replicas: 3}}},
			expectedPending: 3,
		},
		{
			name:            "existing PodClique with fewer pods than replicas counts the shortfall",
			podGang:         &podGangInfo{fqn: podGangName, pclqs: []pclqInfo{{fqn: "worker", replicas: 3}}},
			existingPCLQs:   []grovecorev1alpha1.PodClique{makePCLQ("worker", 3)},
			existingPods:    map[string][]v1.Pod{"worker": {makePod("worker-0", podGangName)}},
			expectedPending: 2,
		},
		{
			name:            "existing PodClique with all pods created and labeled has no pending pods",
			podGang:         &podGangInfo{fqn: podGangName, pclqs: []pclqInfo{{fqn: "worker", replicas: 2}}},
			existingPCLQs:   []grovecorev1alpha1.PodClique{makePCLQ("worker", 2)},
			existingPods:    map[string][]v1.Pod{"worker": {makePod("worker-0", podGangName), makePod("worker-1", podGangName)}},
			expectedPending: 0,
		},
		{
			name:            "existing PodClique with more pods than replicas clamps the negative shortfall to zero",
			podGang:         &podGangInfo{fqn: podGangName, pclqs: []pclqInfo{{fqn: "worker", replicas: 1}}},
			existingPCLQs:   []grovecorev1alpha1.PodClique{makePCLQ("worker", 1)},
			existingPods:    map[string][]v1.Pod{"worker": {makePod("worker-0", podGangName), makePod("worker-1", podGangName)}},
			expectedPending: 0,
		},
		{
			name:            "existing pods missing the PodGang label count as pending association",
			podGang:         &podGangInfo{fqn: podGangName, pclqs: []pclqInfo{{fqn: "worker", replicas: 2}}},
			existingPCLQs:   []grovecorev1alpha1.PodClique{makePCLQ("worker", 2)},
			existingPods:    map[string][]v1.Pod{"worker": {makePod("worker-0", ""), makePod("worker-1", podGangName)}},
			expectedPending: 1,
		},
		{
			name:            "existing pods with a mismatched PodGang label count as pending association",
			podGang:         &podGangInfo{fqn: podGangName, pclqs: []pclqInfo{{fqn: "worker", replicas: 2}}},
			existingPCLQs:   []grovecorev1alpha1.PodClique{makePCLQ("worker", 2)},
			existingPods:    map[string][]v1.Pod{"worker": {makePod("worker-0", "some-other-podgang"), makePod("worker-1", podGangName)}},
			expectedPending: 1,
		},
		{
			name: "multiple PodCliques sum pending-creation and pending-association counts",
			podGang: &podGangInfo{fqn: podGangName, pclqs: []pclqInfo{
				{fqn: "missing", replicas: 4},
				{fqn: "worker", replicas: 3},
			}},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("worker", 3)},
			existingPods: map[string][]v1.Pod{
				"worker": {makePod("worker-0", podGangName), makePod("worker-1", "")},
			},
			// missing PodClique -> 4 (expected replicas); worker -> 1 shortfall (3 desired - 2 pods) + 1 unlabeled pod.
			expectedPending: 6,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ss := &syncState{
				logger:             ctrllogger.FromContext(t.Context()).WithName("test"),
				existingPCLQs:      test.existingPCLQs,
				existingPCLQByName: componentutils.PodCliqueByName(test.existingPCLQs),
				existingPCLQPods:   test.existingPods,
			}
			r := &_resource{schedRegistry: defaultFakeSchedulerRegistry}

			actual := r.getPodsPendingCreationOrAssociation(ss, test.podGang)
			assert.Equal(t, test.expectedPending, actual)
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
			setOrUpdateInitializedCondition(pg, metav1.ConditionTrue, groveschedulerv1alpha1.ConditionReasonPodGangPodsCreated, "PodGang is fully initialized")
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
	anchorPodGangInfo := func() *podGangInfo {
		return &podGangInfo{fqn: pgName, pcsReplicaIndex: 0, pclqs: []pclqInfo{{fqn: pclqName, replicas: 2, minAvailable: 1}}}
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
			existingPCLQs:         []grovecorev1alpha1.PodClique{pclq},
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

	t.Run("new PodGang, pods ready - creates PodGang, records creation, sets Initialized=True", func(t *testing.T) {
		ctx := t.Context()
		pclqs, pods := readyPCLQState(pgName)
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
			existingPCLQs:         pclqs,
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
		pgi := anchorPodGangInfo()
		ss := &syncState{
			pcs:                   pcs,
			logger:                ctrllogger.FromContext(ctx),
			expectedPodGangs:      []*podGangInfo{pgi},
			existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{pgName: *existingPG},
			existingPCLQs:         pclqs,
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
		pgi := anchorPodGangInfo()
		ss := &syncState{
			pcs:                   pcs,
			logger:                ctrllogger.FromContext(ctx),
			expectedPodGangs:      []*podGangInfo{pgi},
			existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{pgName: *existingPG},
			existingPCLQs:         pclqs,
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
		secondPGI := &podGangInfo{fqn: secondPGName, pcsReplicaIndex: 1, pclqs: []pclqInfo{{fqn: secondPCLQName, replicas: 2, minAvailable: 1}}}

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
			existingPCLQs:         pclqs,
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

func makeClusterTopologyBindingWithLevels(name string, levels []grovecorev1alpha1.TopologyLevel) *grovecorev1alpha1.ClusterTopologyBinding {
	return &grovecorev1alpha1.ClusterTopologyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       grovecorev1alpha1.ClusterTopologyBindingSpec{Levels: levels},
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

func podNames(pods []v1.Pod) []string {
	return lo.Map(pods, func(pod v1.Pod, _ int) string { return pod.Name })
}
