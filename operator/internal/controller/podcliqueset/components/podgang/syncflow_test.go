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
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllogger "sigs.k8s.io/controller-runtime/pkg/log"
)

const testGenerationHash = "testhash"

// TestVerifyAllPodsCreated tests verifyAllPodsCreated with minimal sc + podGangInfo (no PCS/prepareSyncFlow).
// It covers both the PCLQ existence check and getPodsPendingCreationOrAssociation logic (Replicas and podgang label).
func TestVerifyAllPodsCreated(t *testing.T) {
	makePod := func(name string, podGangLabel string) corev1.Pod {
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
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
		existingPods  map[string][]corev1.Pod
		existingPCLQs []grovecorev1alpha1.PodClique
		podGang       *podGangInfo
		wantRequeue   bool
	}{
		{
			name:          "requeue when not all constituent PCLQs exist yet",
			existingPods:  map[string][]corev1.Pod{"pclq-a": {makePod("a1", "pg-1")}},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a", 1, 1)},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 1, minAvailable: 1}, {fqn: "pclq-b", replicas: 1, minAvailable: 1}}},
			wantRequeue:   true,
		},
		{
			name: "requeue when PCLQ has fewer pods than Replicas (even if >= MinAvailable)",
			existingPods: map[string][]corev1.Pod{
				"pclq-a": {makePod("a1", "pg-1"), makePod("a2", "pg-1")}, // 2 pods, Replicas=5, MinAvailable=2
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a", 5, 2)},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 5, minAvailable: 2}}},
			wantRequeue:   true, // Still pending: 5-2=3 pods to create
		},
		{
			name: "requeue when Pod missing podgang label",
			existingPods: map[string][]corev1.Pod{
				"pclq-a": {makePod("a1", ""), makePod("a2", "pg-1")}, // a1 missing label
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a", 2, 1)},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 2, minAvailable: 1}}},
			wantRequeue:   true, // a1 needs association
		},
		{
			name: "requeue when Pod has wrong podgang label",
			existingPods: map[string][]corev1.Pod{
				"pclq-a": {makePod("a1", "pg-wrong"), makePod("a2", "pg-1")},
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a", 2, 1)},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 2, minAvailable: 1}}},
			wantRequeue:   true, // a1 has wrong label
		},
		{
			name: "success when all Replicas created and all pods have correct podgang label",
			existingPods: map[string][]corev1.Pod{
				"pclq-a": {makePod("a1", "pg-1"), makePod("a2", "pg-1"), makePod("a3", "pg-1"), makePod("a4", "pg-1"), makePod("a5", "pg-1")},
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{makePCLQ("pclq-a", 5, 2)},
			podGang:       &podGangInfo{fqn: "pg-1", pclqs: []pclqInfo{{fqn: "pclq-a", replicas: 5, minAvailable: 2}}},
			wantRequeue:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sc := &syncContext{
				logger:                ctrllogger.FromContext(t.Context()).WithName("test"),
				existingPCLQPods:      tc.existingPods,
				existingPCLQs:         tc.existingPCLQs,
				existingPCLQByName:    componentutils.PodCliqueByName(tc.existingPCLQs),
				expectedPodGangs:      []*podGangInfo{tc.podGang},
				expectedPodGangByName: map[string]*podGangInfo{tc.podGang.fqn: tc.podGang},
				unassignedPodsByPCLQ:  map[string][]corev1.Pod{},
			}
			r := &_resource{schedRegistry: testutils.NewDefaultFakeRegistry()}
			// Populate pclqInfo.associatedPodNames the same way prepareSyncFlow does in
			// production, so verifyAllPodsCreated has the same view.
			sc.initializeAssignedAndUnassignedPodsForPCS()
			err := r.verifyAllPodsCreated(sc, tc.podGang)
			if tc.wantRequeue {
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

// TestGetPodsPendingCreation checks the accounting of the number of pending pods before creating a PodGang.
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

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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
								Replicas:     &tc.pcsgTemplateReplicas,
								MinAvailable: tc.pcsgMinAvailable,
								CliqueNames:  []string{"prefill-leader", "prefill-worker"},
							},
						},
					},
				},
			}

			pgms := buildTestPodGangMaps(pcs)
			objs := []client.Object{pcs}
			for _, pgm := range pgms {
				objs = append(objs, pgm)
			}
			fakeClient := testutils.NewTestClientBuilder().
				WithObjects(objs...).
				Build()

			r := &_resource{client: fakeClient, schedRegistry: testutils.NewDefaultFakeRegistry()}
			ctx := t.Context()
			logger := ctrllogger.FromContext(ctx).WithName("grove-test")

			sc, err := r.prepareSyncFlow(ctx, logger, pcs)
			require.NoError(t, err)

			assert.Equal(t, len(tc.expectedPendingPodsPerPodGang), len(sc.expectedPodGangs))

			var totalNumPendingPods int
			pendingPodGangNames := sc.getPodGangNamesPendingCreation()
			for i, podGang := range sc.expectedPodGangs {
				isPodGangPendingCreation := slices.Contains(pendingPodGangNames, podGang.fqn)
				assert.True(t, isPodGangPendingCreation)
				numPendingPods := r.getPodsPendingCreationOrAssociation(podGang)
				assert.Equal(t, tc.expectedPendingPodsPerPodGang[i], numPendingPods)
				totalNumPendingPods += numPendingPods
			}
			assert.Equal(t, tc.totalNumPendingPods, totalNumPendingPods)
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
	makePod := func(name, podGangLabel string) *corev1.Pod {
		labels := lo.Assign(pcsLabels)
		if podGangLabel != "" {
			labels[apicommon.LabelPodGang] = podGangLabel
		}
		return &corev1.Pod{
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
		pgms := buildTestPodGangMaps(pcs)
		objs := []client.Object{pcs, pclq}
		for _, pgm := range pgms {
			objs = append(objs, pgm)
		}
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(objs...).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10), schedRegistry: testutils.NewDefaultFakeRegistry()}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		require.Len(t, sc.expectedPodGangs, 1)
		require.Empty(t, sc.existingPodGangs, "PodGang should not exist yet")

		result := r.createOrUpdatePodGangs(ctx, sc)
		require.True(t, result.hasErrors(), "should have requeue error because pods don't exist yet")
		require.Len(t, result.createdPodGangNames, 1, "PodGang should still be recorded as created")
		assert.Equal(t, pgName, result.createdPodGangNames[0])

		pgAfter := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, pgAfter))
		assert.Equal(t, pcsName, pgAfter.OwnerReferences[0].Name)

		var groveErr *groveerr.GroveError
		require.True(t, errors.As(result.errs[0], &groveErr))
		assert.Equal(t, groveerr.ErrCodeRequeueAfter, groveErr.Code)
	})

	t.Run("new PodGang, pods exist but missing PodGang label - creates PodGang, records requeue error", func(t *testing.T) {
		ctx := t.Context()
		pcs := makePCS()
		pclq := makePCLQ()
		pod1 := makePod("worker-0", "")
		pod2 := makePod("worker-1", "")
		pgms := buildTestPodGangMaps(pcs)
		objs := []client.Object{pcs, pclq, pod1, pod2}
		for _, pgm := range pgms {
			objs = append(objs, pgm)
		}
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(objs...).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10), schedRegistry: testutils.NewDefaultFakeRegistry()}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		require.Empty(t, sc.existingPodGangs)

		result := r.createOrUpdatePodGangs(ctx, sc)
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
		pgms := buildTestPodGangMaps(pcs)
		objs := []client.Object{pcs, pclq, pod1, pod2}
		for _, pgm := range pgms {
			objs = append(objs, pgm)
		}
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(objs...).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10), schedRegistry: testutils.NewDefaultFakeRegistry()}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		require.Empty(t, sc.existingPodGangs)

		result := r.createOrUpdatePodGangs(ctx, sc)
		require.False(t, result.hasErrors(), "should succeed: %v", result.errs)
		require.Len(t, result.createdPodGangNames, 1)
		assert.Equal(t, pgName, result.createdPodGangNames[0])

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
		pgms := buildTestPodGangMaps(pcs)
		objs := []client.Object{pcs, pclq, pg, pod1, pod2}
		for _, pgm := range pgms {
			objs = append(objs, pgm)
		}
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(objs...).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10), schedRegistry: testutils.NewDefaultFakeRegistry()}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		assert.True(t, sc.isExistingPodGang(pgName))

		result := r.createOrUpdatePodGangs(ctx, sc)
		require.False(t, result.hasErrors(), "should succeed: %v", result.errs)
		assert.Empty(t, result.createdPodGangNames, "should not record creation for existing PodGang")

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
		pod1 := makePod("worker-2", pgName)
		pod2 := makePod("worker-3", pgName)
		pgms := buildTestPodGangMaps(pcs)
		objs := []client.Object{pcs, pclq, pg, pod1, pod2}
		for _, pgm := range pgms {
			objs = append(objs, pgm)
		}
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(objs...).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10), schedRegistry: testutils.NewDefaultFakeRegistry()}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		assert.True(t, sc.isExistingPodGang(pgName))

		result := r.createOrUpdatePodGangs(ctx, sc)
		require.False(t, result.hasErrors(), "should succeed: %v", result.errs)
		assert.Empty(t, result.createdPodGangNames)

		pgAfter := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, pgAfter))
		require.Len(t, pgAfter.Spec.PodGroups, 1)
		refs := pgAfter.Spec.PodGroups[0].PodReferences
		require.Len(t, refs, 2)
		refNames := []string{refs[0].Name, refs[1].Name}
		assert.ElementsMatch(t, []string{"worker-2", "worker-3"}, refNames, "PodReferences should point to replacement pods, not old ones")

		assert.True(t, sc.isPodGangInitialized(pgName))
	})

	t.Run("multiple PodGangs, first not ready second ready - both processed, requeue for first", func(t *testing.T) {
		ctx := t.Context()
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
		pod1 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "worker-1-0", Namespace: ns,
				Labels:          lo.Assign(pcsLabels, map[string]string{apicommon.LabelPodGang: pg1Name}),
				OwnerReferences: []metav1.OwnerReference{{Name: pclq1Name, UID: "pclq1-uid", Controller: ptr.To(true)}},
			},
		}
		pgms := buildTestPodGangMaps(pcs)
		objs := []client.Object{pcs, pclq0, pclq1, pod1}
		for _, pgm := range pgms {
			objs = append(objs, pgm)
		}
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(objs...).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10), schedRegistry: testutils.NewDefaultFakeRegistry()}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		require.Len(t, sc.expectedPodGangs, 2, "should have 2 expected PodGangs for 2 PCS replicas")

		result := r.createOrUpdatePodGangs(ctx, sc)

		require.Len(t, result.createdPodGangNames, 2, "both PodGangs should be recorded as created")
		assert.ElementsMatch(t, []string{pg0Name, pg1Name}, result.createdPodGangNames)

		require.True(t, result.hasErrors(), "should have errors because first PodGang's pods are not ready")

		pg1After := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pg1Name}, pg1After))
		require.NotEmpty(t, pg1After.Status.Conditions)
		assert.Equal(t, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized), pg1After.Status.Conditions[0].Type)
		assert.Equal(t, metav1.ConditionTrue, pg1After.Status.Conditions[0].Status)

		pg0After := &groveschedulerv1alpha1.PodGang{}
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pg0Name}, pg0After))
		assert.False(t, sc.isPodGangInitialized(pg0Name), "PodGang 0 should not be initialized")
	})

	t.Run("existing PodGang, pods missing PodGang label - updates PodGang, records requeue error", func(t *testing.T) {
		ctx := t.Context()
		pcs := makePCS()
		pclq := makePCLQ()
		pg := makeExistingPodGang()
		pod1 := makePod("worker-0", "")
		pod2 := makePod("worker-1", "")
		pgms := buildTestPodGangMaps(pcs)
		objs := []client.Object{pcs, pclq, pg, pod1, pod2}
		for _, pgm := range pgms {
			objs = append(objs, pgm)
		}
		fakeClient := testutils.NewTestClientBuilder().
			WithObjects(objs...).
			WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
			Build()
		r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme, eventRecorder: record.NewFakeRecorder(10), schedRegistry: testutils.NewDefaultFakeRegistry()}
		sc, err := r.prepareSyncFlow(ctx, ctrllogger.FromContext(ctx).WithName("test"), pcs)
		require.NoError(t, err)
		assert.True(t, sc.isExistingPodGang(pgName))

		result := r.createOrUpdatePodGangs(ctx, sc)
		require.True(t, result.hasErrors(), "should have requeue error because pods are not associated")
		assert.Empty(t, result.createdPodGangNames, "should not record creation for existing PodGang")

		var groveErr *groveerr.GroveError
		require.True(t, errors.As(result.errs[0], &groveErr))
		assert.Equal(t, groveerr.ErrCodeRequeueAfter, groveErr.Code)
	})
}

// TestComputeExpectedPodGangs tests the computeExpectedPodGangs function driven by PodGangMap.
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

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       "test-uid-123",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: tc.pcsReplicas,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques:                      tc.pclqs,
						PodCliqueScalingGroupConfigs: tc.pcsgConfigs,
					},
				},
			}
			pgms := buildTestPodGangMaps(pcs)
			objs := []client.Object{pcs}
			for _, pgm := range pgms {
				objs = append(objs, pgm)
			}
			fakeClient := testutils.NewTestClientBuilder().WithObjects(objs...).Build()
			r := &_resource{client: fakeClient, schedRegistry: testutils.NewDefaultFakeRegistry()}
			sc := &syncContext{
				pcs:            pcs,
				logger:         ctrllogger.FromContext(t.Context()),
				existingPCSGs:  []grovecorev1alpha1.PodCliqueScalingGroup{},
				existingPCLQs:  []grovecorev1alpha1.PodClique{},
				tasEnabled:     false,
				topologyLevels: nil,
			}

			err := r.computeExpectedPodGangs(t.Context(), sc)

			require.NoError(t, err)
			assert.Equal(t, tc.expectedNumPodGangs, len(sc.expectedPodGangs))

			var basePodGangNames []string
			var scaledPodGangNames []string
			for _, pg := range sc.expectedPodGangs {
				if slices.Contains(tc.expectedBasePodGangNames, pg.fqn) {
					basePodGangNames = append(basePodGangNames, pg.fqn)
				} else {
					scaledPodGangNames = append(scaledPodGangNames, pg.fqn)
				}
			}
			assert.ElementsMatch(t, tc.expectedBasePodGangNames, basePodGangNames)
			assert.ElementsMatch(t, tc.expectedScaledPodGangFQNs, scaledPodGangNames)
		})
	}
}

type expectedPodGangTopologyConstraints struct {
	fqn             string
	topologyLevel   *grovecorev1alpha1.TopologyLevel
	pclqConstraints map[string]grovecorev1alpha1.TopologyLevel
	pcsgConstraints map[string]grovecorev1alpha1.TopologyLevel

	// topologyPackConstraint, pclqPackConstraints, and pcsgPackConstraints are the preferred-pack-aware
	// shape introduced by #652. Use these when a test case must express required+preferred together,
	// or a preferred-only constraint. When non-nil they take precedence over topologyLevel /
	// pclqConstraints / pcsgConstraints above.
	topologyPackConstraint *expectedTopologyPackConstraint
	pclqPackConstraints    map[string]expectedTopologyPackConstraint
	pcsgPackConstraints    map[string]expectedTopologyPackConstraint
}

type expectedTopologyPackConstraint struct {
	requiredKey  string
	preferredKey string
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
			name:       "PCS with preferred-only topology constraint at PCS level",
			tasEnabled: true,
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				Pack: &grovecorev1alpha1.TopologyPackConstraint{PreferredDomain: "host"},
			},
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
					fqn: "test-pcs-0",
					topologyPackConstraint: &expectedTopologyPackConstraint{
						preferredKey: topologyLevelHost.Key,
					},
				},
			},
		},
		{
			name:       "PCS with required and preferred topology constraints at PCS level",
			tasEnabled: true,
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				Pack: &grovecorev1alpha1.TopologyPackConstraint{
					RequiredDomain:  "zone",
					PreferredDomain: "host",
				},
			},
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
					fqn: "test-pcs-0",
					topologyPackConstraint: &expectedTopologyPackConstraint{
						requiredKey:  topologyLevelZone.Key,
						preferredKey: topologyLevelHost.Key,
					},
				},
			},
		},
		{
			name:       "PCS with stale preferred domain preserves required topology constraint",
			tasEnabled: true,
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				Pack: &grovecorev1alpha1.TopologyPackConstraint{
					RequiredDomain:  "rack",
					PreferredDomain: "block",
				},
			},
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
					fqn: "test-pcs-0",
					topologyPackConstraint: &expectedTopologyPackConstraint{
						requiredKey: topologyLevelRack.Key,
					},
				},
			},
		},
		{
			name:       "PCS with stale required domain preserves preferred topology constraint",
			tasEnabled: true,
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				Pack: &grovecorev1alpha1.TopologyPackConstraint{
					RequiredDomain:  "block",
					PreferredDomain: "rack",
				},
			},
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
					fqn: "test-pcs-0",
					topologyPackConstraint: &expectedTopologyPackConstraint{
						preferredKey: topologyLevelRack.Key,
					},
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
					Name: "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
						Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "host"},
					},
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
			name:       "PCS with preferred-only topology constraint on standalone PCLQ",
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
					Name: "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
						Pack: &grovecorev1alpha1.TopologyPackConstraint{PreferredDomain: "host"},
					},
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     2,
						MinAvailable: ptr.To(int32(1)),
					},
				},
			},
			expectedNumPodGangs: 1,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn: "test-pcs-0",
					pclqPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-worker": {preferredKey: topologyLevelHost.Key},
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
					topologyLevel: &topologyLevelZone,
					pclqConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-scaling-group-1-decode-leader": topologyLevelHost,
						"test-pcs-0-scaling-group-1-decode-worker": topologyLevelHost,
					},
					pcsgConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-scaling-group-1": topologyLevelRack,
					},
				},
			},
		},
		{
			name:       "PCS with preferred-only topology constraint on PCSG",
			tasEnabled: true,
			pclqTemplateSpecs: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "decode-leader",
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:     1,
						MinAvailable: ptr.To(int32(1)),
					},
				},
				{
					Name: "decode-worker",
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
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
						Pack: &grovecorev1alpha1.TopologyPackConstraint{PreferredDomain: "rack"},
					},
				},
			},
			expectedNumPodGangs: 2,
			expectedPodGangTopologyConstraints: []expectedPodGangTopologyConstraints{
				{
					fqn: "test-pcs-0",
					pcsgPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-0": {preferredKey: topologyLevelRack.Key},
					},
				},
				{
					fqn: "test-pcs-0-scaling-group-0",
					pcsgPackConstraints: map[string]expectedTopologyPackConstraint{
						"test-pcs-0-scaling-group-1": {preferredKey: topologyLevelRack.Key},
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
					topologyLevel: &topologyLevelZone,
					pclqConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-scaling-group-1-decode-leader": topologyLevelHost,
						"test-pcs-0-scaling-group-1-decode-worker": topologyLevelHost,
					},
					pcsgConstraints: map[string]grovecorev1alpha1.TopologyLevel{
						"test-pcs-0-scaling-group-1": topologyLevelRack,
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
			var pcsTopologyConstraint *grovecorev1alpha1.TopologyConstraint
			switch {
			case tc.pcsTopologyConstraint != nil:
				pcsTopologyConstraint = tc.pcsTopologyConstraint
			case tc.pcsTopologyLevel != nil:
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

			pgms := buildTestPodGangMaps(pcs)
			objs := []client.Object{pcs}
			for _, pgm := range pgms {
				objs = append(objs, pgm)
			}
			fakeClient := testutils.NewTestClientBuilder().WithObjects(objs...).Build()
			r := &_resource{client: fakeClient, schedRegistry: testutils.NewDefaultFakeRegistry()}
			sc := &syncContext{
				pcs:            pcs,
				logger:         ctrllogger.FromContext(t.Context()),
				existingPCSGs:  []grovecorev1alpha1.PodCliqueScalingGroup{},
				existingPCLQs:  []grovecorev1alpha1.PodClique{},
				tasEnabled:     tc.tasEnabled,
				topologyLevels: clusterTopologyLevels,
			}

			err := r.computeExpectedPodGangs(t.Context(), sc)
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
				for _, expectedPGConstraint := range tc.expectedPodGangTopologyConstraints {
					computedPodGang, found := lo.Find(sc.expectedPodGangs, func(pg *podGangInfo) bool {
						return pg.fqn == expectedPGConstraint.fqn
					})
					require.True(t, found, "Expected PodGang %s not found", expectedPGConstraint.fqn)

					assertPodGangLevelConstraint(t, computedPodGang, expectedPGConstraint)
					assertPCLQConstraints(t, computedPodGang, expectedPGConstraint)
					assertPCSGConstraints(t, computedPodGang, expectedPGConstraint)
				}
			}
		})
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

			pgms := buildTestPodGangMaps(pcs)
			var objs []client.Object
			objs = append(objs, pcs)
			for _, pgm := range pgms {
				objs = append(objs, pgm)
			}
			if tc.clusterTopologyExists {
				topologyName, err := componentutils.ResolveEffectiveTopologyNameForPodCliqueSet(pcs)
				require.NoError(t, err)
				objs = append(objs, makeClusterTopologyBindingWithLevels(topologyName, ctLevels))
			}

			fakeClient := testutils.NewTestClientBuilder().WithObjects(objs...).Build()
			r := &_resource{
				client:        fakeClient,
				scheme:        groveclientscheme.Scheme,
				eventRecorder: record.NewFakeRecorder(10),
				tasConfig:     configv1alpha1.TopologyAwareSchedulingConfiguration{Enabled: tc.tasEnabled},
				schedRegistry: testutils.NewDefaultFakeRegistry(),
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

	makePod := func() *corev1.Pod {
		return &corev1.Pod{
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
				makeClusterTopologyBindingWithLevels("my-topology", []grovecorev1alpha1.TopologyLevel{
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
			pgms := buildTestPodGangMaps(pcs)
			objs := []client.Object{pcs, makePCLQ(), makePod(), tc.existingPodGang}
			for _, pgm := range pgms {
				objs = append(objs, pgm)
			}
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
				schedRegistry: testutils.NewDefaultFakeRegistry(),
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

	makePod := func(podGangLabel string) *corev1.Pod {
		labels := lo.Assign(pcsLabels)
		if podGangLabel != "" {
			labels[apicommon.LabelPodGang] = podGangLabel
		}
		return &corev1.Pod{
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
			pgms := buildTestPodGangMaps(pcs)
			var objs []client.Object
			objs = append(objs, pcs, pclq, pod)
			for _, pgm := range pgms {
				objs = append(objs, pgm)
			}
			if tc.topologyName != "" {
				objs = append(objs, makeClusterTopologyBindingWithLevels(tc.topologyName, ctLevels))
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
				schedRegistry: testutils.NewDefaultFakeRegistry(),
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

func TestArePodGangMinReplicasReady(t *testing.T) {
	makeReadyPod := func(name string) corev1.Pod {
		return *testutils.NewPodBuilder(name, "default").
			WithCondition(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
			Build()
	}
	makeNotReadyPod := func(name string) corev1.Pod {
		return *testutils.NewPodBuilder(name, "default").
			WithCondition(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse}).
			Build()
	}

	tests := []struct {
		name                   string
		existingPodsByPCLQName map[string][]corev1.Pod
		pgi                    *podGangInfo
		expectedResult         bool
	}{
		{
			name: "all associated pods ready and >= minAvailable",
			existingPodsByPCLQName: map[string][]corev1.Pod{
				"pcs-0-frontend": {makeReadyPod("fe-0"), makeReadyPod("fe-1"), makeReadyPod("fe-2")},
			},
			pgi: &podGangInfo{
				fqn: "pg-0",
				pclqs: []pclqInfo{
					{fqn: "pcs-0-frontend", minAvailable: 2, associatedPodNames: []string{"fe-0", "fe-1", "fe-2"}},
				},
			},
			expectedResult: true,
		},
		{
			name: "not enough ready pods for minAvailable",
			existingPodsByPCLQName: map[string][]corev1.Pod{
				"pcs-0-frontend": {makeReadyPod("fe-0"), makeNotReadyPod("fe-1"), makeNotReadyPod("fe-2")},
			},
			pgi: &podGangInfo{
				fqn: "pg-0",
				pclqs: []pclqInfo{
					{fqn: "pcs-0-frontend", minAvailable: 2, associatedPodNames: []string{"fe-0", "fe-1", "fe-2"}},
				},
			},
			expectedResult: false,
		},
		{
			name: "only counts pods associated to this PodGang",
			existingPodsByPCLQName: map[string][]corev1.Pod{
				"pcs-0-frontend": {makeReadyPod("fe-0"), makeReadyPod("fe-1"), makeReadyPod("fe-2"), makeReadyPod("fe-3")},
			},
			pgi: &podGangInfo{
				fqn: "pg-1",
				pclqs: []pclqInfo{
					{fqn: "pcs-0-frontend", minAvailable: 2, associatedPodNames: []string{"fe-2", "fe-3"}},
				},
			},
			expectedResult: true,
		},
		{
			name: "unassociated ready pods do not satisfy minAvailable",
			existingPodsByPCLQName: map[string][]corev1.Pod{
				"pcs-0-frontend": {makeReadyPod("fe-0"), makeReadyPod("fe-1"), makeNotReadyPod("fe-2")},
			},
			pgi: &podGangInfo{
				fqn: "pg-1",
				pclqs: []pclqInfo{
					{fqn: "pcs-0-frontend", minAvailable: 2, associatedPodNames: []string{"fe-2"}},
				},
			},
			expectedResult: false,
		},
		{
			name: "multiple PodGroups all must satisfy minAvailable",
			existingPodsByPCLQName: map[string][]corev1.Pod{
				"pcs-0-frontend": {makeReadyPod("fe-0"), makeReadyPod("fe-1")},
				"pcs-0-backend":  {makeReadyPod("be-0"), makeNotReadyPod("be-1")},
			},
			pgi: &podGangInfo{
				fqn: "pg-0",
				pclqs: []pclqInfo{
					{fqn: "pcs-0-frontend", minAvailable: 2, associatedPodNames: []string{"fe-0", "fe-1"}},
					{fqn: "pcs-0-backend", minAvailable: 2, associatedPodNames: []string{"be-0", "be-1"}},
				},
			},
			expectedResult: false,
		},
	}

	r := &_resource{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sc := &syncContext{existingPCLQPods: tc.existingPodsByPCLQName}
			assert.Equal(t, tc.expectedResult, r.arePodGangMinReplicasReady(sc, tc.pgi))
		})
	}
}

// TestPatchPodGangCondition verifies that patchPodGangCondition uses Get-modify-Patch with
// client.MergeFrom so existing conditions on the PodGang are preserved when a new condition
// is set. The earlier implementation built a fresh PodGang with only the new condition and
// patched with client.Merge — JSON Merge Patch (RFC 7396) replaces lists wholesale, which
// wiped every previously-set condition. This test locks in the fix.
func TestPatchPodGangCondition(t *testing.T) {
	ctx := t.Context()
	const (
		pcsName = "test-pcs"
		ns      = "test-ns"
		pgName  = "test-pcs-0"
	)

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: ns},
	}
	existingPG := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: pgName, Namespace: ns},
		Status: groveschedulerv1alpha1.PodGangStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(groveschedulerv1alpha1.PodGangConditionTypeInitialized),
					Status:             metav1.ConditionTrue,
					Reason:             groveschedulerv1alpha1.ConditionReasonPodGangPodsCreated,
					Message:            "Initial seed",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	fakeClient := testutils.NewTestClientBuilder().
		WithObjects(pcs, existingPG).
		WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
		Build()
	r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme}
	sc := &syncContext{pcs: pcs, logger: ctrllogger.FromContext(ctx).WithName("test")}

	// Add a second condition; the existing Initialized condition must remain.
	require.NoError(t, r.patchPodGangCondition(
		ctx, sc, pgName,
		groveschedulerv1alpha1.PodGangConditionTypeReady,
		metav1.ConditionTrue,
		groveschedulerv1alpha1.ConditionReasonPodGangReady,
		"MinAvailable pods of every PodGroup are ready",
	))

	got := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, got))

	assert.Len(t, got.Status.Conditions, 2, "existing Initialized condition must not be wiped")
	condByType := make(map[string]metav1.Condition, len(got.Status.Conditions))
	for _, c := range got.Status.Conditions {
		condByType[c.Type] = c
	}
	require.Contains(t, condByType, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized))
	require.Contains(t, condByType, string(groveschedulerv1alpha1.PodGangConditionTypeReady))
	assert.Equal(t, metav1.ConditionTrue, condByType[string(groveschedulerv1alpha1.PodGangConditionTypeReady)].Status)
	assert.Equal(t, "Initial seed", condByType[string(groveschedulerv1alpha1.PodGangConditionTypeInitialized)].Message,
		"existing condition message must be preserved untouched")
}

func TestArePodGangMinReplicasScheduled(t *testing.T) {
	makeScheduledPod := func(name string) corev1.Pod {
		return *testutils.NewPodBuilder(name, "default").
			WithCondition(corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}).
			Build()
	}
	makeUnscheduledPod := func(name string) corev1.Pod {
		return *testutils.NewPodBuilder(name, "default").
			WithCondition(corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionFalse}).
			Build()
	}

	tests := []struct {
		name                   string
		existingPodsByPCLQName map[string][]corev1.Pod
		pgi                    *podGangInfo
		expectedResult         bool
	}{
		{
			name: "all associated pods scheduled and >= minAvailable",
			existingPodsByPCLQName: map[string][]corev1.Pod{
				"pcs-0-frontend": {makeScheduledPod("fe-0"), makeScheduledPod("fe-1"), makeScheduledPod("fe-2")},
			},
			pgi: &podGangInfo{
				fqn: "pg-0",
				pclqs: []pclqInfo{
					{fqn: "pcs-0-frontend", minAvailable: 2, associatedPodNames: []string{"fe-0", "fe-1", "fe-2"}},
				},
			},
			expectedResult: true,
		},
		{
			name: "not enough scheduled pods for minAvailable",
			existingPodsByPCLQName: map[string][]corev1.Pod{
				"pcs-0-frontend": {makeScheduledPod("fe-0"), makeUnscheduledPod("fe-1"), makeUnscheduledPod("fe-2")},
			},
			pgi: &podGangInfo{
				fqn: "pg-0",
				pclqs: []pclqInfo{
					{fqn: "pcs-0-frontend", minAvailable: 2, associatedPodNames: []string{"fe-0", "fe-1", "fe-2"}},
				},
			},
			expectedResult: false,
		},
		{
			name: "only counts pods associated to this PodGang",
			existingPodsByPCLQName: map[string][]corev1.Pod{
				"pcs-0-frontend": {makeScheduledPod("fe-0"), makeScheduledPod("fe-1"), makeScheduledPod("fe-2"), makeScheduledPod("fe-3")},
			},
			pgi: &podGangInfo{
				fqn: "pg-1",
				pclqs: []pclqInfo{
					{fqn: "pcs-0-frontend", minAvailable: 2, associatedPodNames: []string{"fe-2", "fe-3"}},
				},
			},
			expectedResult: true,
		},
		{
			name: "multiple PodGroups all must satisfy minAvailable",
			existingPodsByPCLQName: map[string][]corev1.Pod{
				"pcs-0-frontend": {makeScheduledPod("fe-0"), makeScheduledPod("fe-1")},
				"pcs-0-backend":  {makeScheduledPod("be-0"), makeUnscheduledPod("be-1")},
			},
			pgi: &podGangInfo{
				fqn: "pg-0",
				pclqs: []pclqInfo{
					{fqn: "pcs-0-frontend", minAvailable: 2, associatedPodNames: []string{"fe-0", "fe-1"}},
					{fqn: "pcs-0-backend", minAvailable: 2, associatedPodNames: []string{"be-0", "be-1"}},
				},
			},
			expectedResult: false,
		},
	}

	r := &_resource{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sc := &syncContext{existingPCLQPods: tc.existingPodsByPCLQName}
			assert.Equal(t, tc.expectedResult, r.arePodGangMinReplicasScheduled(sc, tc.pgi))
		})
	}
}

func TestReleaseMinReplicasConstraint(t *testing.T) {
	const (
		pcsName = "test-pcs"
		ns      = "test-ns"
		pgName  = "test-pcs-0"
	)
	standaloneFQN := "test-pcs-0-frontend"
	pcsgMemberFQN := "test-pcs-0-sg-0-worker"

	tests := []struct {
		name                string
		pgi                 *podGangInfo
		initialMinReplicas  map[string]int32
		expectedMinReplicas map[string]int32
	}{
		{
			name: "standalone PodGroup is released; PCSG-member PodGroup retains MinReplicas",
			pgi: &podGangInfo{
				fqn: pgName,
				pclqs: []pclqInfo{
					{fqn: standaloneFQN, minAvailable: 2, isStandalone: true},
					{fqn: pcsgMemberFQN, minAvailable: 3, isStandalone: false},
				},
			},
			initialMinReplicas: map[string]int32{
				standaloneFQN: 2,
				pcsgMemberFQN: 3,
			},
			expectedMinReplicas: map[string]int32{
				standaloneFQN: 0,
				pcsgMemberFQN: 3,
			},
		},
		{
			name: "no standalone PodGroups is a no-op",
			pgi: &podGangInfo{
				fqn: pgName,
				pclqs: []pclqInfo{
					{fqn: pcsgMemberFQN, minAvailable: 3, isStandalone: false},
				},
			},
			initialMinReplicas: map[string]int32{
				pcsgMemberFQN: 3,
			},
			expectedMinReplicas: map[string]int32{
				pcsgMemberFQN: 3,
			},
		},
		{
			name: "all-standalone releases every PodGroup",
			pgi: &podGangInfo{
				fqn: pgName,
				pclqs: []pclqInfo{
					{fqn: standaloneFQN, minAvailable: 2, isStandalone: true},
				},
			},
			initialMinReplicas: map[string]int32{
				standaloneFQN: 2,
			},
			expectedMinReplicas: map[string]int32{
				standaloneFQN: 0,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			pg := &groveschedulerv1alpha1.PodGang{
				ObjectMeta: metav1.ObjectMeta{Name: pgName, Namespace: ns},
			}
			for fqn, mr := range tc.initialMinReplicas {
				pg.Spec.PodGroups = append(pg.Spec.PodGroups, groveschedulerv1alpha1.PodGroup{
					Name:        fqn,
					MinReplicas: mr,
				})
			}
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: ns},
			}
			fakeClient := testutils.NewTestClientBuilder().
				WithObjects(pcs, pg).
				Build()
			existingByName := map[string]groveschedulerv1alpha1.PodGang{pgName: *pg}
			r := &_resource{client: fakeClient}
			sc := &syncContext{
				pcs:                   pcs,
				logger:                ctrllogger.FromContext(ctx).WithName("test"),
				existingPodGangByName: existingByName,
			}

			require.NoError(t, r.releaseMinReplicasConstraint(ctx, sc, tc.pgi))

			got := &groveschedulerv1alpha1.PodGang{}
			require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, got))
			require.Len(t, got.Spec.PodGroups, len(tc.expectedMinReplicas))
			for _, pg := range got.Spec.PodGroups {
				assert.Equal(t, tc.expectedMinReplicas[pg.Name], pg.MinReplicas, "PodGroup %s", pg.Name)
			}
		})
	}
}

func TestReconcileScheduledCondition(t *testing.T) {
	const (
		pcsName = "test-pcs"
		ns      = "test-ns"
		pgName  = "test-pcs-0"
	)
	standaloneFQN := "test-pcs-0-frontend"
	scheduledPod := *testutils.NewPodBuilder("fe-0", ns).
		WithCondition(corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}).
		Build()
	unscheduledPod := *testutils.NewPodBuilder("fe-0", ns).
		WithCondition(corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionFalse}).
		Build()

	tests := []struct {
		name                  string
		pods                  []corev1.Pod
		initialConditions     []metav1.Condition
		initialMinReplicas    int32
		wantScheduledTrue     bool
		wantMinReplicasZeroed bool
	}{
		{
			name: "already Scheduled=True is a no-op",
			pods: []corev1.Pod{scheduledPod},
			initialConditions: []metav1.Condition{{
				Type:               string(groveschedulerv1alpha1.PodGangConditionTypeScheduled),
				Status:             metav1.ConditionTrue,
				Reason:             groveschedulerv1alpha1.ConditionReasonPodGangScheduled,
				LastTransitionTime: metav1.Now(),
			}},
			initialMinReplicas:    2,
			wantScheduledTrue:     true,
			wantMinReplicasZeroed: false,
		},
		{
			name:                  "not yet scheduled is a no-op",
			pods:                  []corev1.Pod{unscheduledPod},
			initialMinReplicas:    2,
			wantScheduledTrue:     false,
			wantMinReplicasZeroed: false,
		},
		{
			name:                  "scheduled releases MinReplicas and sets Scheduled=True",
			pods:                  []corev1.Pod{scheduledPod},
			initialMinReplicas:    2,
			wantScheduledTrue:     true,
			wantMinReplicasZeroed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: ns},
			}
			existingPG := &groveschedulerv1alpha1.PodGang{
				ObjectMeta: metav1.ObjectMeta{Name: pgName, Namespace: ns},
				Spec: groveschedulerv1alpha1.PodGangSpec{
					PodGroups: []groveschedulerv1alpha1.PodGroup{{
						Name:        standaloneFQN,
						MinReplicas: tc.initialMinReplicas,
					}},
				},
				Status: groveschedulerv1alpha1.PodGangStatus{Conditions: tc.initialConditions},
			}
			fakeClient := testutils.NewTestClientBuilder().
				WithObjects(pcs, existingPG).
				WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
				Build()
			r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme}
			sc := &syncContext{
				pcs:                   pcs,
				logger:                ctrllogger.FromContext(ctx).WithName("test"),
				existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{pgName: *existingPG},
				existingPCLQPods:      map[string][]corev1.Pod{standaloneFQN: tc.pods},
			}
			pgi := &podGangInfo{
				fqn: pgName,
				pclqs: []pclqInfo{
					{fqn: standaloneFQN, minAvailable: 1, associatedPodNames: []string{"fe-0"}, isStandalone: true},
				},
			}

			require.NoError(t, r.reconcileScheduledCondition(ctx, sc, pgi))

			got := &groveschedulerv1alpha1.PodGang{}
			require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, got))
			assert.Equal(t, tc.wantScheduledTrue, k8sutils.IsConditionTrue(got.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeScheduled)))
			require.Len(t, got.Spec.PodGroups, 1)
			if tc.wantMinReplicasZeroed {
				assert.Equal(t, int32(0), got.Spec.PodGroups[0].MinReplicas)
			} else {
				assert.Equal(t, tc.initialMinReplicas, got.Spec.PodGroups[0].MinReplicas)
			}
		})
	}
}

func TestReconcileReadyCondition(t *testing.T) {
	const (
		pcsName = "test-pcs"
		ns      = "test-ns"
		pgName  = "test-pcs-0"
	)
	pclqFQN := "test-pcs-0-frontend"
	readyPod := *testutils.NewPodBuilder("fe-0", ns).
		WithCondition(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
		Build()
	notReadyPod := *testutils.NewPodBuilder("fe-0", ns).
		WithCondition(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse}).
		Build()
	readyCondTrue := metav1.Condition{
		Type:               string(groveschedulerv1alpha1.PodGangConditionTypeReady),
		Status:             metav1.ConditionTrue,
		Reason:             groveschedulerv1alpha1.ConditionReasonPodGangReady,
		LastTransitionTime: metav1.Now(),
	}

	tests := []struct {
		name              string
		pods              []corev1.Pod
		initialConditions []metav1.Condition
		wantReadyTrue     bool
	}{
		{
			name:          "no Ready condition and pods not ready is a no-op",
			pods:          []corev1.Pod{notReadyPod},
			wantReadyTrue: false,
		},
		{
			name:              "Ready=True with pods still ready is a no-op",
			pods:              []corev1.Pod{readyPod},
			initialConditions: []metav1.Condition{readyCondTrue},
			wantReadyTrue:     true,
		},
		{
			name:          "pods become ready and Ready flips to True",
			pods:          []corev1.Pod{readyPod},
			wantReadyTrue: true,
		},
		{
			name:              "pods stop being ready and Ready flips to False",
			pods:              []corev1.Pod{notReadyPod},
			initialConditions: []metav1.Condition{readyCondTrue},
			wantReadyTrue:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: ns},
			}
			existingPG := &groveschedulerv1alpha1.PodGang{
				ObjectMeta: metav1.ObjectMeta{Name: pgName, Namespace: ns},
				Status:     groveschedulerv1alpha1.PodGangStatus{Conditions: tc.initialConditions},
			}
			fakeClient := testutils.NewTestClientBuilder().
				WithObjects(pcs, existingPG).
				WithStatusSubresource(&groveschedulerv1alpha1.PodGang{}).
				Build()
			r := &_resource{client: fakeClient, scheme: groveclientscheme.Scheme}
			sc := &syncContext{
				pcs:                   pcs,
				logger:                ctrllogger.FromContext(ctx).WithName("test"),
				existingPodGangByName: map[string]groveschedulerv1alpha1.PodGang{pgName: *existingPG},
				existingPCLQPods:      map[string][]corev1.Pod{pclqFQN: tc.pods},
			}
			pgi := &podGangInfo{
				fqn: pgName,
				pclqs: []pclqInfo{
					{fqn: pclqFQN, minAvailable: 1, associatedPodNames: []string{"fe-0"}, isStandalone: true},
				},
			}

			require.NoError(t, r.reconcileReadyCondition(ctx, sc, pgi))

			got := &groveschedulerv1alpha1.PodGang{}
			require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pgName}, got))
			assert.Equal(t, tc.wantReadyTrue, k8sutils.IsConditionTrue(got.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeReady)))
		})
	}
}

// Test helper functions
// ----------------------------------------------------------------------

// buildTestPodGangMaps builds PodGangMap objects for all PCS replicas using the BPG/SPG convention,
// derived from the PCS template (Cliques and PodCliqueScalingGroupConfigs).
//
// The function mutates pcs.Status.CurrentGenerationHash to testGenerationHash if it is not already set,
// so that callers only need to build the PCS spec and do not have to set status fields themselves.
func buildTestPodGangMaps(pcs *grovecorev1alpha1.PodCliqueSet) []*grovecorev1alpha1.PodGangMap {
	if pcs.Status.CurrentGenerationHash == nil {
		pcs.Status.CurrentGenerationHash = ptr.To(testGenerationHash)
	}
	generationHash := *pcs.Status.CurrentGenerationHash

	var pgms []*grovecorev1alpha1.PodGangMap
	for replicaIndex := range int(pcs.Spec.Replicas) {
		pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex})
		standaloneFQNs := componentutils.GetStandalonePCLQFQNSet(pcs, replicaIndex)

		// BPG entry: standalone PCLQs at template replicas.
		bpgPodCliques := make(map[string]int32)
		for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
			fqn := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex}, cliqueTemplate.Name)
			if standaloneFQNs.Has(fqn) {
				bpgPodCliques[cliqueTemplate.Name] = cliqueTemplate.Spec.Replicas
			}
		}

		// BPG entry: each PCSG contributes index slice [0, MinAvailable).
		bpgPCSGIndices := make(map[string][]int32)

		var entries []grovecorev1alpha1.PodGangEntry
		if len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) > 0 {
			// Derive PCSG entries from template config.
			for _, cfg := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
				if cfg.MinAvailable != nil {
					indices := make([]int32, 0, *cfg.MinAvailable)
					for i := int32(0); i < *cfg.MinAvailable; i++ {
						indices = append(indices, i)
					}
					bpgPCSGIndices[cfg.Name] = indices
				}
			}
			bpgName := apicommon.GenerateBasePodGangName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex})
			entries = []grovecorev1alpha1.PodGangEntry{{
				Name:                       bpgName,
				PodCliqueSetGenerationHash: generationHash,
				PodCliques:                 bpgPodCliques,
				PCSGReplicaIndices:         bpgPCSGIndices,
			}}
			// SPG entries from template, holding one replica index each above MinAvailable.
			for _, cfg := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
				pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex}, cfg.Name)
				if cfg.Replicas == nil || cfg.MinAvailable == nil {
					continue
				}
				minAvail := *cfg.MinAvailable
				for scaledIdx := range *cfg.Replicas - minAvail {
					spgName := apicommon.CreatePodGangNameFromPCSGFQN(pcsgFQN, int(scaledIdx))
					entries = append(entries, grovecorev1alpha1.PodGangEntry{
						Name:                       spgName,
						PodCliqueSetGenerationHash: generationHash,
						PCSGReplicaIndices:         map[string][]int32{cfg.Name: {minAvail + scaledIdx}},
					})
				}
			}
		} else {
			// Standalone PCLQs only — single BPG entry.
			bpgName := apicommon.GenerateBasePodGangName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex})
			entries = []grovecorev1alpha1.PodGangEntry{{
				Name:                       bpgName,
				PodCliqueSetGenerationHash: generationHash,
				PodCliques:                 bpgPodCliques,
			}}
		}

		pgms = append(pgms, &grovecorev1alpha1.PodGangMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pgmName,
				Namespace: pcs.Namespace,
			},
			Spec: grovecorev1alpha1.PodGangMapSpec{
				PodCliqueSetReplicaIndex: int32(replicaIndex),
				Entries:                  entries,
			},
		})
	}
	return pgms
}

func assertPodGangLevelConstraint(t *testing.T, pg *podGangInfo, expected expectedPodGangTopologyConstraints) {
	t.Helper()
	if expected.topologyPackConstraint != nil {
		assertPackConstraint(t, pg.topologyConstraint, *expected.topologyPackConstraint)
		return
	}
	if expected.topologyLevel == nil {
		assert.Nil(t, pg.topologyConstraint)
		return
	}
	assertRequiredTopologyConstraint(t, pg.topologyConstraint, expected.topologyLevel.Key)
}

func assertPCLQConstraints(t *testing.T, pg *podGangInfo, expected expectedPodGangTopologyConstraints) {
	t.Helper()
	if expected.pclqPackConstraints != nil {
		for _, pclq := range pg.pclqs {
			want, exists := expected.pclqPackConstraints[pclq.fqn]
			if !exists {
				assert.Nil(t, pclq.topologyConstraint, "PCLQ %s should have no topology constraint", pclq.fqn)
				continue
			}
			assertPackConstraint(t, pclq.topologyConstraint, want)
		}
		return
	}
	for _, pclq := range pg.pclqs {
		want, exists := expected.pclqConstraints[pclq.fqn]
		if !exists {
			assert.Nil(t, pclq.topologyConstraint, "PCLQ %s should have no topology constraint", pclq.fqn)
			continue
		}
		assertRequiredTopologyConstraint(t, pclq.topologyConstraint, want.Key)
	}
}

func assertPCSGConstraints(t *testing.T, pg *podGangInfo, expected expectedPodGangTopologyConstraints) {
	t.Helper()
	if expected.pcsgPackConstraints != nil {
		for pcsgFQN, want := range expected.pcsgPackConstraints {
			actualPCSGTC, found := lo.Find(pg.pcsgTopologyConstraints, func(pcsgTC groveschedulerv1alpha1.TopologyConstraintGroupConfig) bool {
				return pcsgTC.Name == pcsgFQN
			})
			assert.True(t, found, "Expected PCSG topology constraint for %s not found", pcsgFQN)
			assertPackConstraint(t, actualPCSGTC.TopologyConstraint, want)
		}
		for _, actualPCSGTC := range pg.pcsgTopologyConstraints {
			if _, exists := expected.pcsgPackConstraints[actualPCSGTC.Name]; !exists {
				t.Errorf("Unexpected PCSG topology constraint for %s found in PodGang %s", actualPCSGTC.Name, pg.fqn)
			}
		}
		return
	}
	for pcsgFQN, expectedPCSGTC := range expected.pcsgConstraints {
		actualPCSGTC, found := lo.Find(pg.pcsgTopologyConstraints, func(pcsgTC groveschedulerv1alpha1.TopologyConstraintGroupConfig) bool {
			return pcsgTC.Name == pcsgFQN
		})
		assert.True(t, found, "Expected PCSG topology constraint for %s not found", pcsgFQN)
		assertRequiredTopologyConstraint(t, actualPCSGTC.TopologyConstraint, expectedPCSGTC.Key)
	}
	for _, actualPCSGTC := range pg.pcsgTopologyConstraints {
		if _, exists := expected.pcsgConstraints[actualPCSGTC.Name]; !exists {
			t.Errorf("Unexpected PCSG topology constraint for %s found in PodGang %s", actualPCSGTC.Name, pg.fqn)
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

func assertRequiredTopologyConstraint(t *testing.T, got *groveschedulerv1alpha1.TopologyConstraint, wantedKey string) {
	assert.NotNil(t, got)
	assert.NotNil(t, got.PackConstraint)
	assert.Nil(t, got.PackConstraint.Preferred)
	assert.NotNil(t, got.PackConstraint.Required)
	assert.Equal(t, wantedKey, *got.PackConstraint.Required)
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
		require.NotNil(t, got.PackConstraint.Required, "expected required key %q", want.requiredKey)
		assert.Equal(t, want.requiredKey, *got.PackConstraint.Required)
	}
	if want.preferredKey == "" {
		assert.Nil(t, got.PackConstraint.Preferred, "expected no preferred key")
	} else {
		require.NotNil(t, got.PackConstraint.Preferred, "expected preferred key %q", want.preferredKey)
		assert.Equal(t, want.preferredKey, *got.PackConstraint.Preferred)
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

// makeClusterTopologyBindingWithLevels creates a ClusterTopologyBinding with the given levels.
func makeClusterTopologyBindingWithLevels(name string, levels []grovecorev1alpha1.TopologyLevel) *grovecorev1alpha1.ClusterTopologyBinding {
	return &grovecorev1alpha1.ClusterTopologyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       grovecorev1alpha1.ClusterTopologyBindingSpec{Levels: levels},
	}
}
