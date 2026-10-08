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
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/lpx"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	schedulertest "github.com/ai-dynamo/grove/operator/test/utils/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBackend_LPXAggregateUsesLPXSelection(t *testing.T) {
	for _, tc := range []struct {
		name                string
		kaiGroups, tail     bool
		schedulerName       string
		unchangedProjection bool
	}{
		{name: "mixed missing Pod scheduler", kaiGroups: true},
		{name: "mixed legacy LPX Pod scheduler", kaiGroups: true, schedulerName: "lpx-scheduler"},
		{name: "KAI-only Tail without KAI Anchor", kaiGroups: true, tail: true},
		{name: "unchanged KAI-only projection", kaiGroups: true, unchangedProjection: true},
		{name: "LPX-only projection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newLPXAggregateFixture(t, tc.kaiGroups, tc.tail)
			if tc.unchangedProjection {
				fixture.anchor.Spec.PodGroups = fixture.anchor.Spec.PodGroups[1:]
				require.NoError(t, fixture.cl.Update(t.Context(), fixture.anchor))
			}
			if tc.kaiGroups {
				fixture.kaiPod.Spec.SchedulerName = tc.schedulerName
				require.NoError(t, fixture.cl.Create(t.Context(), fixture.kaiPod))
			}
			// Even a misleading schedulerName must not select an LPX-owned Pod for KAI.
			fixture.lpxPod.Spec.SchedulerName = "kai-scheduler"
			require.NoError(t, fixture.cl.Create(t.Context(), fixture.lpxPod))
			require.NoError(t, fixture.backend.SyncPodGang(t.Context(), fixture.anchor))
			require.NoError(t, fixture.backend.SyncPodGang(t.Context(), fixture.anchor))
			fixture.assertProjection(t, tc.kaiGroups)
		})
	}
}

func TestBackend_LPXRequiresCompleteRealMaterializations(t *testing.T) {
	f := newLPXAggregateFixture(t, true, true)
	require.NoError(t, f.cl.Delete(t.Context(), f.kaiGang))
	require.ErrorContains(t, f.backend.SyncPodGang(t.Context(), f.anchor), "failed to get PodGang")
	assert.True(t, apierrors.IsNotFound(f.cl.Get(t.Context(), aggregatePodGroupKey(f.pcs, 0), &kaischedulingv2alpha2.PodGroup{})))
	stored := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.anchor), stored))
	assert.NotContains(t, stored.Finalizers, podGangFinalizer)
}

func TestBackend_LPXDeletionDoesNotWaitForLPXPods(t *testing.T) {
	f := newLPXAggregateFixture(t, true, true)
	require.NoError(t, f.cl.Create(t.Context(), f.lpxPod))
	require.NoError(t, f.backend.SyncPodGang(t.Context(), f.anchor))
	f.pgm.Spec.Entries = f.pgm.Spec.Entries[:1]
	require.NoError(t, f.cl.Update(t.Context(), f.pgm))
	require.NoError(t, f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.kaiGang), f.kaiGang))
	require.NoError(t, f.cl.Delete(t.Context(), f.kaiGang))
	require.NoError(t, f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.kaiGang), f.kaiGang))
	require.NoError(t, f.backend.SyncPodGang(t.Context(), f.kaiGang))
	assert.True(t, apierrors.IsNotFound(f.cl.Get(t.Context(), aggregatePodGroupKey(f.pcs, 0), &kaischedulingv2alpha2.PodGroup{})))
	assert.True(t, apierrors.IsNotFound(f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.kaiGang), &groveschedulerv1alpha1.PodGang{})))
	require.NoError(t, f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.lpxPod), &corev1.Pod{}))
}

func TestBackend_LPXDeletionReleasesFinalizerAfterOwnersDisappear(t *testing.T) {
	f := newLPXAggregateFixture(t, true, false)
	require.NoError(t, f.backend.SyncPodGang(t.Context(), f.anchor))
	for _, group := range f.anchor.Spec.PodGroups {
		pclq := &grovecorev1alpha1.PodClique{}
		require.NoError(t, f.cl.Get(t.Context(), client.ObjectKey{Namespace: f.pcs.Namespace, Name: group.Name}, pclq))
		require.NoError(t, f.cl.Delete(t.Context(), pclq))
	}
	require.NoError(t, f.cl.Delete(t.Context(), f.pcs))
	require.NoError(t, f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.anchor), f.anchor))
	require.NoError(t, f.cl.Delete(t.Context(), f.anchor))
	require.NoError(t, f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.anchor), f.anchor))
	require.NoError(t, f.backend.SyncPodGang(t.Context(), f.anchor))
	assert.True(t, apierrors.IsNotFound(f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.anchor), &groveschedulerv1alpha1.PodGang{})))
}

type lpxAggregateFixture struct {
	cl              client.Client
	backend         scheduler.Backend
	pcs             *grovecorev1alpha1.PodCliqueSet
	pgm             *grovecorev1alpha1.PodGangMap
	anchor, kaiGang *groveschedulerv1alpha1.PodGang
	lpxPod, kaiPod  *corev1.Pod
}

func newLPXAggregateFixture(t *testing.T, kaiGroups, tail bool) *lpxAggregateFixture {
	t.Helper()
	pcs := newPodCliqueSet("lpx-workload", "default")
	lpuClique := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "lpu", pcs.Namespace, 0).Build()
	lpuClique.Spec.PodSpec.Containers[0].Resources = corev1.ResourceRequirements{Requests: corev1.ResourceList{"nvidia.com/lpu": resource.MustParse("1")}}
	kaiClique := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "gpu", pcs.Namespace, 0).Build()
	anchor := testutils.NewPodGangBuilder("anchor", pcs.Namespace).
		WithPodGroups([]groveschedulerv1alpha1.PodGroup{{Name: lpuClique.Name, MinReplicas: 1}}).Build()
	pgm := configureTestAnchorPodGang(pcs, anchor)
	anchor.Labels[apicommon.LabelSchedulerName] = "lpx-scheduler"
	anchor.UID = types.UID("anchor-uid")
	kaiGang := anchor
	objects := []client.Object{pcs, lpuClique, kaiClique, anchor, pgm}
	if kaiGroups && tail {
		entry := testutils.NewTailEntry("test-generation", "2000", "gpu", 0)
		pgm.Spec.Entries = append(pgm.Spec.Entries, entry)
		kaiGang = anchor.DeepCopy()
		kaiGang.Name = apicommon.GenerateNonAnchorPodGangName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, "2000", "gpu", 0)
		kaiGang.UID = types.UID("tail-uid")
		kaiGang.Labels[apicommon.LabelEpoch] = entry.Epoch
		kaiGang.Labels[apicommon.LabelPodGangRole] = string(entry.Role)
		kaiGang.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{{Name: kaiClique.Name, MinReplicas: 2}}
		objects = append(objects, kaiGang)
	} else if kaiGroups {
		anchor.Spec.PodGroups = append(anchor.Spec.PodGroups, groveschedulerv1alpha1.PodGroup{Name: kaiClique.Name, MinReplicas: 2})
	}
	scheme := schedulertest.NewKAIScheme(t)
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	cl := testutils.NewTestClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	backend := lpx.New(cl, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX}, New(cl, scheme, nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}))
	require.NoError(t, backend.Init(cl))
	lpxPod, kaiPod := legacyAggregatePod(pcs, anchor, lpuClique.Name), legacyAggregatePod(pcs, kaiGang, kaiClique.Name)
	lpxPod.Name, kaiPod.Name = "lpu-pod", "gpu-pod"
	kaiPod.UID, kaiPod.Spec.NodeName = types.UID("stable-uid"), "stable-node"
	return &lpxAggregateFixture{cl: cl, backend: backend, pcs: pcs, pgm: pgm, anchor: anchor, kaiGang: kaiGang, lpxPod: lpxPod, kaiPod: kaiPod}
}

func (f *lpxAggregateFixture) assertProjection(t *testing.T, kaiGroups bool) {
	t.Helper()
	storedGang := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.anchor), storedGang))
	assert.Equal(t, f.anchor.Spec, storedGang.Spec, "metadata patch must preserve the real unfiltered spec")
	assert.Contains(t, storedGang.Finalizers, podGangFinalizer)
	storedPod := &corev1.Pod{}
	require.NoError(t, f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.lpxPod), storedPod))
	assert.Equal(t, f.lpxPod.Annotations, storedPod.Annotations)
	assert.Equal(t, f.lpxPod.Labels, storedPod.Labels)
	aggregate := &kaischedulingv2alpha2.PodGroup{}
	err := f.cl.Get(t.Context(), aggregatePodGroupKey(f.pcs, 0), aggregate)
	if !kaiGroups {
		assert.True(t, apierrors.IsNotFound(err))
		return
	}
	require.NoError(t, err)
	leaf := podGroupLeafName(f.kaiGang.Name, f.kaiPod.Labels[apicommon.LabelPodClique])
	assert.Equal(t, int32(2), *requireSubGroup(t, aggregate, leaf).MinMember)
	assert.Nil(t, findSubGroup(aggregate, podGroupLeafName(f.anchor.Name, f.lpxPod.Labels[apicommon.LabelPodClique])))
	require.NoError(t, f.cl.Get(t.Context(), client.ObjectKeyFromObject(f.kaiPod), storedPod))
	assert.Equal(t, aggregate.Name, storedPod.Annotations[annotationPodGroup])
	assert.Equal(t, leaf, storedPod.Labels[labelSubGroup])
	assert.Equal(t, f.kaiPod.UID, storedPod.UID)
	assert.Equal(t, f.kaiPod.Spec, storedPod.Spec)
}
