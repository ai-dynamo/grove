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

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBackend_EmptyPodGangMapDrainsAndRegrows(t *testing.T) {
	pcs := newPodCliqueSet("empty-pcs", "team-a")
	anchor := testutils.NewPodGangBuilder("anchor", pcs.Namespace).WithPodGroup("worker", 1).Build()
	pgm := configureTestAnchorPodGang(pcs, anchor)
	anchor.Finalizers = []string{podGangFinalizer}
	pod := legacyAggregatePod(pcs, anchor, "worker")
	aggregate := ownedAggregatePodGroup(pcs, 0)
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, pgm, anchor, pod).Build()
	b := New(cl, cl.Scheme(), nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	initTestBackend(t, b, cl)
	require.NoError(t, cl.Create(t.Context(), aggregate))
	pgm.Spec.Entries = nil
	require.NoError(t, cl.Update(t.Context(), pgm))
	require.NoError(t, cl.Delete(t.Context(), anchor))
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(anchor), anchor))

	assert.ErrorContains(t, b.SyncPodGang(t.Context(), anchor), "waiting for 1 Pods")
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(anchor), anchor))
	assert.Contains(t, anchor.Finalizers, podGangFinalizer)
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(aggregate), &kaischedulingv2alpha2.PodGroup{}))

	require.NoError(t, cl.Delete(t.Context(), pod))
	require.NoError(t, b.SyncPodGang(t.Context(), anchor))
	assert.True(t, apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(anchor), &groveschedulerv1alpha1.PodGang{})))
	assert.True(t, apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(aggregate), &kaischedulingv2alpha2.PodGroup{})))

	regrown := testutils.NewPodGangBuilder("regrown", pcs.Namespace).WithPodGroup("worker", 2).Build()
	pgm.Spec.Entries = []grovecorev1alpha1.PodGangEntry{configureTestAnchorEntry(pcs, regrown, 0, "2000")}
	require.NoError(t, cl.Update(t.Context(), pgm))
	require.NoError(t, cl.Create(t.Context(), regrown))
	require.NoError(t, b.SyncPodGang(t.Context(), regrown))
	require.NoError(t, cl.Get(t.Context(), aggregatePodGroupKey(pcs, 0), aggregate))
	assert.Equal(t, int32(2), *requireSubGroup(t, aggregate, podGroupLeafName(regrown.Name, "worker")).MinMember)
}
