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

package kai

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"sync"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBackend_SyncPodGang_AggregateLockScope(t *testing.T) {
	tests := []struct {
		name          string
		secondReplica int
		wantBlocked   bool
	}{
		{name: "serializes sibling PodGangs", secondReplica: 0, wantBlocked: true},
		{name: "allows another replica concurrently", secondReplica: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := newPodCliqueSet("lock-scope", "team-a")
			pcs.Spec.Replicas = 2
			objects := []client.Object{pcs}
			podGangsByReplica := make(map[int][]*groveschedulerv1alpha1.PodGang, 2)
			for replica := range 2 {
				first := testutils.NewPodGangBuilder("first", pcs.Namespace).WithPodGroup("worker", 1).Build()
				second := testutils.NewPodGangBuilder("second", pcs.Namespace).WithPodGroup("worker", 1).Build()
				firstEntry := configureTestAnchorEntry(pcs, first, replica, "1000", 0)
				secondEntry := configureTestAnchorEntry(pcs, second, replica, "2000", 1)
				pgm := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, replica).
					WithEntries(firstEntry, secondEntry).
					Build()
				objects = append(objects, pgm, first, second)
				podGangsByReplica[replica] = []*groveschedulerv1alpha1.PodGang{first, second}
			}

			baseClient := testutils.NewTestClientBuilder().WithObjects(objects...).Build()
			blockingClient := &blockingPodGangMapGetClient{
				Client:     baseClient,
				blockedKey: testPodGangMapKey(pcs, 0),
				getStarted: make(chan client.ObjectKey, 4),
				release:    make(chan struct{}),
			}
			backend := New(blockingClient, baseClient.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
			initTestBackend(t, backend, blockingClient)

			firstResult := make(chan error, 1)
			go func() { firstResult <- backend.SyncPodGang(context.Background(), podGangsByReplica[0][0]) }()
			require.Equal(t, blockingClient.blockedKey, receiveObjectKey(t, blockingClient.getStarted))

			secondResult := make(chan error, 1)
			go func() {
				secondResult <- backend.SyncPodGang(context.Background(), podGangsByReplica[tt.secondReplica][1])
			}()
			if tt.wantBlocked {
				select {
				case key := <-blockingClient.getStarted:
					t.Fatalf("second reconciliation reached PodGangMap %s before sibling released aggregate lock", key)
				case <-time.After(50 * time.Millisecond):
				}
			} else {
				require.Equal(t, testPodGangMapKey(pcs, 1), receiveObjectKey(t, blockingClient.getStarted))
				require.NoError(t, <-secondResult)
			}

			close(blockingClient.release)
			require.NoError(t, <-firstResult)
			if tt.wantBlocked {
				require.NoError(t, <-secondResult)
			}
		})
	}
}

func TestBackend_PreparePod(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").
		WithSchedulerName("default-scheduler").
		Build()
	pod.Labels = map[string]string{
		apicommon.LabelPartOfKey:                "test-pcs",
		apicommon.LabelPodCliqueSetReplicaIndex: "2",
		apicommon.LabelPodGang:                  "test-podgang-epoch-1000",
		apicommon.LabelPodClique:                "test-clique",
	}
	pod.Annotations = map[string]string{"keep": "me"}

	require.NoError(t, b.PreparePod(pod))

	assert.Equal(t, "kai-scheduler", pod.Spec.SchedulerName)
	assert.Equal(t, "me", pod.Annotations["keep"])
	assert.Equal(t, "true", pod.Annotations["kai.scheduler/skip-podgrouper"])
	assert.Equal(t, aggregatePodGroupName("test-pcs", 2), pod.Annotations["pod-group-name"])
	assert.Equal(t, podGroupLeafName("test-podgang-epoch-1000", "test-clique"), pod.Labels["kai.scheduler/subgroup-name"])
}

func TestBackend_PreparePod_PreservesExistingSkipAnnotation(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").
		Build()
	pod.Labels = map[string]string{
		apicommon.LabelPartOfKey:                "test-pcs",
		apicommon.LabelPodCliqueSetReplicaIndex: "0",
		apicommon.LabelPodGang:                  "test-podgang",
		apicommon.LabelPodClique:                "test-clique",
	}
	pod.Annotations = map[string]string{"kai.scheduler/skip-podgrouper": "custom"}

	require.NoError(t, b.PreparePod(pod))

	assert.Equal(t, "true", pod.Annotations["kai.scheduler/skip-podgrouper"])
}

func TestBackend_PreparePod_RequiresAggregateIdentity(t *testing.T) {
	validLabels := map[string]string{
		apicommon.LabelPartOfKey:                "test-pcs",
		apicommon.LabelPodCliqueSetReplicaIndex: "0",
		apicommon.LabelPodGang:                  "test-podgang",
		apicommon.LabelPodClique:                "test-clique",
	}
	tests := []struct {
		name          string
		label         string
		value         string
		wantErrSubstr string
	}{
		{name: "PodCliqueSet name", label: apicommon.LabelPartOfKey, wantErrSubstr: apicommon.LabelPartOfKey},
		{name: "replica index", label: apicommon.LabelPodCliqueSetReplicaIndex, wantErrSubstr: apicommon.LabelPodCliqueSetReplicaIndex},
		{name: "invalid replica index", label: apicommon.LabelPodCliqueSetReplicaIndex, value: "invalid", wantErrSubstr: "failed to convert replica index"},
		{name: "negative replica index", label: apicommon.LabelPodCliqueSetReplicaIndex, value: "-1", wantErrSubstr: "invalid grove.io/podcliqueset-replica-index"},
		{name: "PodGang name", label: apicommon.LabelPodGang, wantErrSubstr: apicommon.LabelPodGang},
		{name: "PodClique name", label: apicommon.LabelPodClique, wantErrSubstr: apicommon.LabelPodClique},
	}

	b := &schedulerBackend{name: string(configv1alpha1.SchedulerNameKai)}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{}
			pod.Labels = maps.Clone(validLabels)
			if tt.value == "" {
				delete(pod.Labels, tt.label)
			} else {
				pod.Labels[tt.label] = tt.value
			}

			require.ErrorContains(t, b.PreparePod(pod), tt.wantErrSubstr)
		})
	}
}

func TestBackend_SyncPodGang_CreateAndUpdate(t *testing.T) {
	pcs := newPodCliqueSet("test-pcs", "team-a", podCliqueTemplateWithQueue("worker-template", "team-a"))
	podGang := testutils.NewPodGangBuilder("test-podgang", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("worker", 2).
		Build()
	pgm := configureTestAnchorPodGang(pcs, podGang)
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, pgm, podGang).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	initTestBackend(t, b, cl)

	ctx := context.Background()
	require.NoError(t, b.SyncPodGang(ctx, podGang))

	aggregate := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(ctx, aggregatePodGroupKey(pcs, 0), aggregate))
	assert.True(t, metav1.IsControlledBy(aggregate, pcs))
	assert.Equal(t, "team-a", aggregate.Spec.Queue)
	assert.Equal(t, int32(2), *requireSubGroup(t, aggregate, podGroupLeafName(podGang.Name, "worker")).MinMember)
	current := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(podGang), current))
	assert.Equal(t, annotationValSkipPGR, current.Annotations[annotationKeySkipPGR])
	assert.Contains(t, current.Finalizers, podGangFinalizer)

	current.Spec.PodGroups[0].MinReplicas = 4
	require.NoError(t, cl.Update(ctx, current))
	require.NoError(t, b.SyncPodGang(ctx, current))
	require.NoError(t, cl.Get(ctx, aggregatePodGroupKey(pcs, 0), aggregate))
	assert.Equal(t, int32(4), *requireSubGroup(t, aggregate, podGroupLeafName(podGang.Name, "worker")).MinMember)
}

func TestBackend_SyncPodGang_IncompletePodGangMapPreservesAggregateAndInstallsFinalizer(t *testing.T) {
	pcs := newPodCliqueSet("incomplete-pcs", "team-a")
	podGang := testutils.NewPodGangBuilder("anchor", pcs.Namespace).
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("worker", 1).
		Build()
	pgm := configureTestAnchorPodGang(pcs, podGang)
	pgm.Spec.Entries = append(pgm.Spec.Entries, testutils.NewTailEntry("test-generation", "2000", "workers", 1))
	existing := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      aggregatePodGroupName(pcs.Name, 0),
			Namespace: pcs.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
				Kind:       "PodCliqueSet",
				Name:       pcs.Name,
				UID:        pcs.UID,
				Controller: ptr.To(true),
			}},
		},
		Spec: kaischedulingv2alpha2.PodGroupSpec{Queue: "preserve-me"},
	}

	cl := testutils.NewTestClientBuilder().WithObjects(pcs, pgm, podGang, existing).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	initTestBackend(t, b, cl)

	err := b.SyncPodGang(context.Background(), podGang)
	assert.True(t, apierrors.IsNotFound(err))

	updatedPodGang := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(podGang), updatedPodGang))
	assert.Contains(t, updatedPodGang.Finalizers, podGangFinalizer)
	assert.Equal(t, annotationValSkipPGR, updatedPodGang.Annotations[annotationKeySkipPGR])

	unchanged := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(existing), unchanged))
	assert.Equal(t, "preserve-me", unchanged.Spec.Queue)
}

func TestBackend_SyncPodGang_ReparentsHistoricalPodGroupBeforeFinalizerRemoval(t *testing.T) {
	pcs := newPodCliqueSet("owned-pcs", "team-a")
	podGang := terminatingPodGang(pcs)
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, podGang).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	initTestBackend(t, b, cl)

	historical := historicalPodGroup(podGang)
	require.NoError(t, cl.Create(context.Background(), historical))
	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	updated := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(historical), updated))
	owner := metav1.GetControllerOf(updated)
	require.NotNil(t, owner)
	assert.Equal(t, "PodCliqueSet", owner.Kind)
	assert.Equal(t, pcs.Name, owner.Name)
	assert.Equal(t, pcs.UID, owner.UID)
	assertPodGangFinalizerRemoved(t, cl, podGang)
}

func TestBackend_SyncPodGang_DoesNotReparentStaleHistoricalPodGroup(t *testing.T) {
	pcs := newPodCliqueSet("owned-pcs", "team-a")
	podGang := terminatingPodGang(pcs)
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, podGang).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	initTestBackend(t, b, cl)

	historical := historicalPodGroup(podGang)
	historical.OwnerReferences[0].UID = "stale-podgang-uid"
	require.NoError(t, cl.Create(context.Background(), historical))
	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	updated := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(historical), updated))
	assert.Equal(t, types.UID("stale-podgang-uid"), metav1.GetControllerOf(updated).UID)
	assertPodGangFinalizerRemoved(t, cl, podGang)
}

func TestBackend_SyncPodGang_HoldsFinalizerWhenHistoricalReparentFails(t *testing.T) {
	pcs := newPodCliqueSet("owned-pcs", "team-a")
	podGang := terminatingPodGang(pcs)
	patchErr := apierrors.NewInternalError(errors.New("apiserver unavailable"))
	cl := testutils.NewTestClientBuilder().
		WithObjects(pcs, podGang).
		RecordErrorForObjects(testutils.ClientMethodPatch, patchErr, client.ObjectKeyFromObject(podGang)).
		Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	initTestBackend(t, b, cl)
	require.NoError(t, cl.Create(context.Background(), historicalPodGroup(podGang)))

	require.ErrorContains(t, b.SyncPodGang(context.Background(), podGang), "reparent historical KAI PodGroup")
	updated := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(podGang), updated))
	assert.Contains(t, updated.Finalizers, podGangFinalizer)
}

func TestBackend_SyncPodGang_ReleasesFinalizerWhenPodCliqueSetIsDeletingOrMissing(t *testing.T) {
	tests := []struct {
		name       string
		includePCS bool
		deletePCS  bool
	}{
		{name: "deleting", includePCS: true, deletePCS: true},
		{name: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := newPodCliqueSet("owned-pcs", "team-a")
			if tt.deletePCS {
				now := metav1.Now()
				pcs.DeletionTimestamp = &now
				pcs.Finalizers = []string{"test.grove.io/hold"}
			}
			podGang := terminatingPodGang(pcs)
			objects := []client.Object{podGang}
			if tt.includePCS {
				objects = append(objects, pcs)
			}
			cl := testutils.NewTestClientBuilder().WithObjects(objects...).Build()
			b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
			initTestBackend(t, b, cl)

			require.NoError(t, b.SyncPodGang(context.Background(), podGang))
			assertPodGangFinalizerRemoved(t, cl, podGang)
		})
	}
}

func TestBackend_ValidatePodCliqueSetQueues(t *testing.T) {
	tests := []struct {
		name          string
		pcs           *grovecorev1alpha1.PodCliqueSet
		wantErrSubstr string
	}{
		{
			name: "matching PodCliqueSet and template queues",
			pcs: newPodCliqueSet(
				"matching-queues-pcs",
				"team-a",
				podCliqueTemplateWithQueue("worker", "team-a"),
			),
		},
		{
			name: "PodCliqueSet queue without template queue",
			pcs: newPodCliqueSet(
				"pcs-only-queue-pcs",
				"team-a",
				podCliqueTemplateWithQueue("worker", ""),
			),
		},
		{
			name: "matching template-only queues",
			pcs: newPodCliqueSet(
				"template-only-queue-pcs",
				"",
				podCliqueTemplateWithQueue("worker-a", "team-a"),
				podCliqueTemplateWithQueue("worker-b", "team-a"),
			),
		},
		{
			name: "different PodCliqueSet and template queues",
			pcs: newPodCliqueSet(
				"conflicting-pcs-template-queues-pcs",
				"team-a",
				podCliqueTemplateWithQueue("worker", "team-b"),
			),
			wantErrSubstr: "is \"team-a\" but PodClique template \"worker\" resolves to \"team-b\"",
		},
		{
			name: "conflicting template-only queues",
			pcs: newPodCliqueSet(
				"conflicting-template-queues-pcs",
				"",
				podCliqueTemplateWithQueue("worker-a", "team-a"),
				podCliqueTemplateWithQueue("worker-b", "team-b"),
			),
			wantErrSubstr: "conflicting KAI queues",
		},
		{
			name: "missing queue",
			pcs: newPodCliqueSet(
				"missing-queue-pcs",
				"",
				podCliqueTemplateWithQueue("worker", ""),
			),
			wantErrSubstr: "no KAI queue is configured",
		},
		{
			name: "labels override annotations",
			pcs: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := newPodCliqueSet(
					"label-precedence-pcs",
					"team-a",
					podCliqueTemplateWithQueue("worker", "team-a"),
				)
				pcs.Annotations = map[string]string{labelKeyQueueName: "team-b"}
				pcs.Spec.Template.Cliques[0].Annotations = map[string]string{labelKeyQueueName: "team-b"}
				return pcs
			}(),
		},
		{
			name: "annotations are used when labels are absent",
			pcs: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := newPodCliqueSet(
					"annotation-fallback-pcs",
					"",
					podCliqueTemplateWithQueue("worker", ""),
				)
				pcs.Annotations = map[string]string{labelKeyQueueName: "team-a"}
				pcs.Spec.Template.Cliques[0].Annotations = map[string]string{labelKeyQueueName: "team-a"}
				return pcs
			}(),
		},
	}

	b := &schedulerBackend{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := b.ValidatePodCliqueSet(context.Background(), tt.pcs)
			if tt.wantErrSubstr != "" {
				require.ErrorContains(t, err, tt.wantErrSubstr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestBackend_SyncPodGang_MappingFailuresDoNotCreatePodGroup(t *testing.T) {
	tests := []struct {
		name          string
		pcs           *grovecorev1alpha1.PodCliqueSet
		omitPCS       bool
		wantErrSubstr string
	}{
		{
			name:          "missing PodCliqueSet controller owner",
			wantErrSubstr: "has no controlling PodCliqueSet",
		},
		{
			name: "controlling PodCliqueSet is absent",
			pcs: newPodCliqueSet(
				"absent-pcs",
				"team-a",
				podCliqueTemplateWithQueue("worker", "team-b"),
			),
			omitPCS:       true,
			wantErrSubstr: "get controlling PodCliqueSet default/absent-pcs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			podGang := testutils.NewPodGangBuilder("test-podgang", "default").
				WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
				Build()
			objects := []client.Object{podGang}
			if tt.pcs != nil {
				pgm := configureTestAnchorPodGang(tt.pcs, podGang)
				if !tt.omitPCS {
					objects = append(objects, tt.pcs, pgm)
				}
			}

			cl := testutils.NewTestClientBuilder().WithObjects(objects...).Build()
			b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
			initTestBackend(t, b, cl)

			err := b.SyncPodGang(context.Background(), podGang)
			require.ErrorContains(t, err, tt.wantErrSubstr)

			podGroups := &kaischedulingv2alpha2.PodGroupList{}
			require.NoError(t, cl.List(context.Background(), podGroups))
			assert.Empty(t, podGroups.Items, "PodGroup must not be created when PodGang mapping fails")
		})
	}
}

func TestPodGroupsEqual_AllowsTargetOnlyMetadata(t *testing.T) {
	desired := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"source-label": "desired"},
			Annotations: map[string]string{"source-annotation": "desired"},
		},
	}
	existing := desired.DeepCopy()
	existing.Labels[labelKeyNodePoolName] = "runtime-node-pool"
	existing.Annotations["kai.scheduler/runtime"] = "preserve"

	assert.True(t, podGroupsEqual(existing, desired))

	existing.Labels["source-label"] = "stale"
	assert.False(t, podGroupsEqual(existing, desired))
}

func TestUpdatePodGroup_CopiesDesiredMetadataAndPreservesTargetOnlyMetadata(t *testing.T) {
	existing := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{labelKeyNodePoolName: "runtime-node-pool"},
		},
	}
	desired := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"source-label": "desired"},
			Annotations: map[string]string{"source-annotation": "desired"},
		},
	}

	updatePodGroup(existing, desired)

	assert.Equal(t, map[string]string{
		labelKeyNodePoolName: "runtime-node-pool",
		"source-label":       "desired",
	}, existing.Labels)
	assert.Equal(t, map[string]string{"source-annotation": "desired"}, existing.Annotations)
}

func newPodCliqueSet(name, queue string, cliques ...*grovecorev1alpha1.PodCliqueTemplateSpec) *grovecorev1alpha1.PodCliqueSet {
	builder := testutils.NewPodCliqueSetBuilder(name, "default", types.UID(name+"-uid"))
	for _, clique := range cliques {
		builder.WithPodCliqueTemplateSpec(clique)
	}
	pcs := builder.Build()
	if queue != "" {
		pcs.Labels = map[string]string{labelKeyQueueName: queue}
	}
	return pcs
}

func podCliqueTemplateWithQueue(name, queue string) *grovecorev1alpha1.PodCliqueTemplateSpec {
	labels := map[string]string{}
	if queue != "" {
		labels[labelKeyQueueName] = queue
	}
	return testutils.NewPodCliqueTemplateSpecBuilder(name).WithLabels(labels).Build()
}

func setPodCliqueSetControllerOwner(podGang *groveschedulerv1alpha1.PodGang, pcs *grovecorev1alpha1.PodCliqueSet) {
	if podGang.Labels == nil {
		podGang.Labels = map[string]string{}
	}
	podGang.Labels[apicommon.LabelPodCliqueSetReplicaIndex] = "0"
	podGang.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
		Kind:       "PodCliqueSet",
		Name:       pcs.Name,
		UID:        pcs.UID,
		Controller: ptr.To(true),
	}}
}

func configureTestAnchorPodGang(pcs *grovecorev1alpha1.PodCliqueSet, podGang *groveschedulerv1alpha1.PodGang) *grovecorev1alpha1.PodGangMap {
	entry := configureTestAnchorEntry(pcs, podGang, 0, "1000", 0)
	return testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 0).WithEntries(entry).Build()
}

func configureTestAnchorEntry(pcs *grovecorev1alpha1.PodCliqueSet, podGang *groveschedulerv1alpha1.PodGang, replica int, epoch string, anchorIndex int32) grovecorev1alpha1.PodGangEntry {
	const generationHash = "test-generation"
	entry := testutils.NewPodGangEntryBuilder(generationHash, epoch).
		WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
		WithAnchorIndex(anchorIndex).
		Build()
	podGang.Name = apicommon.GenerateAnchorPodGangName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replica}, epoch)
	setPodCliqueSetControllerOwner(podGang, pcs)
	podGang.Labels[apicommon.LabelPodCliqueSetReplicaIndex] = strconv.Itoa(replica)
	podGang.Labels[apicommon.LabelEpoch] = epoch
	podGang.Labels[apicommon.LabelPodGangRole] = string(entry.Role)
	podGang.Labels[apicommon.LabelPodCliqueSetGenerationHash] = generationHash
	podGang.Labels[apicommon.LabelSchedulerName] = string(configv1alpha1.SchedulerNameKai)
	return entry
}

func requireSubGroup(t *testing.T, podGroup *kaischedulingv2alpha2.PodGroup, name string) *kaischedulingv2alpha2.SubGroup {
	t.Helper()
	subGroup := findSubGroup(podGroup, name)
	require.NotNil(t, subGroup, "subgroup %q not found", name)
	return subGroup
}

func findSubGroup(podGroup *kaischedulingv2alpha2.PodGroup, name string) *kaischedulingv2alpha2.SubGroup {
	for i := range podGroup.Spec.SubGroups {
		if podGroup.Spec.SubGroups[i].Name == name {
			return &podGroup.Spec.SubGroups[i]
		}
	}
	return nil
}

func terminatingPodGang(pcs *grovecorev1alpha1.PodCliqueSet) *groveschedulerv1alpha1.PodGang {
	const name = "terminating-podgang"
	podGang := testutils.NewPodGangBuilder(name, pcs.Namespace).
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithDeletionTimestamp().
		Build()
	podGang.UID = types.UID(name + "-uid")
	podGang.Finalizers = []string{podGangFinalizer}
	setPodCliqueSetControllerOwner(podGang, pcs)
	return podGang
}

func historicalPodGroup(podGang *groveschedulerv1alpha1.PodGang) *kaischedulingv2alpha2.PodGroup {
	return &kaischedulingv2alpha2.PodGroup{ObjectMeta: metav1.ObjectMeta{
		Name:      podGang.Name,
		Namespace: podGang.Namespace,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: groveschedulerv1alpha1.SchemeGroupVersion.String(),
			Kind:       "PodGang",
			Name:       podGang.Name,
			UID:        podGang.UID,
			Controller: ptr.To(true),
		}},
	}}
}

func assertPodGangFinalizerRemoved(t *testing.T, cl client.Client, podGang *groveschedulerv1alpha1.PodGang) {
	t.Helper()
	updated := &groveschedulerv1alpha1.PodGang{}
	err := cl.Get(context.Background(), client.ObjectKeyFromObject(podGang), updated)
	if apierrors.IsNotFound(err) {
		return
	}
	require.NoError(t, err)
	assert.NotContains(t, updated.Finalizers, podGangFinalizer)
}

type blockingPodGangMapGetClient struct {
	client.Client
	blockedKey client.ObjectKey
	getStarted chan client.ObjectKey
	release    chan struct{}
	once       sync.Once
}

func (c *blockingPodGangMapGetClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, isPodGangMap := obj.(*grovecorev1alpha1.PodGangMap); isPodGangMap {
		c.getStarted <- key
		if key == c.blockedKey {
			wait := false
			c.once.Do(func() { wait = true })
			if wait {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-c.release:
				}
			}
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func receiveObjectKey(t *testing.T, keys <-chan client.ObjectKey) client.ObjectKey {
	t.Helper()
	select {
	case key := <-keys:
		return key
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for PodGangMap read")
		return client.ObjectKey{}
	}
}

func testPodGangMapKey(pcs *grovecorev1alpha1.PodCliqueSet, replica int) client.ObjectKey {
	return client.ObjectKey{
		Namespace: pcs.Namespace,
		Name:      apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replica}),
	}
}

type kaiBackendInitializer interface {
	Init(client.Client) error
}

func initTestBackend(t *testing.T, backend kaiBackendInitializer, cl client.Client) {
	t.Helper()
	require.NoError(t, backend.Init(cl))
}
