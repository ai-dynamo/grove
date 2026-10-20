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

package kube

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestWASHierarchyGangScopedMembership(t *testing.T) {
	backend, cl := newRuntimeBackend(t)
	for _, name := range []string{"pcs-0-1000", "pcs-0-1001"} {
		gang := runtimeGang(name)
		gang.Spec.PodGroups = append(gang.Spec.PodGroups, groveschedulerv1alpha1.PodGroup{Name: "router", MinReplicas: 1})
		require.NoError(t, backend.SyncPodGang(t.Context(), gang))
		for _, group := range gang.Spec.PodGroups {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
				apicommon.LabelPodGang: gang.Name, apicommon.LabelPodClique: group.Name,
			}}}
			require.NoError(t, backend.PreparePod(pod))
			assert.Equal(t, backend.Name(), pod.Spec.SchedulerName)
			leaf := &schedulingv1beta1.PodGroup{}
			require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Namespace: gang.Namespace, Name: *pod.Spec.SchedulingGroup.PodGroupName}, leaf))
			assert.True(t, metav1.IsControlledBy(leaf, gang))
			assert.Equal(t, ptr.To(gang.Name), leaf.Spec.ParentCompositePodGroupName)
			require.NotNil(t, leaf.Spec.WorkloadRef)
			assert.Equal(t, gang.Name, leaf.Spec.WorkloadRef.WorkloadName)
			assert.Equal(t, leafTemplateName(group.Name), leaf.Spec.WorkloadRef.TemplateName)
		}
	}
	leaves := &schedulingv1beta1.PodGroupList{}
	require.NoError(t, cl.List(t.Context(), leaves))
	require.Len(t, leaves.Items, 4)
	assert.NotEqual(t, leaves.Items[0].Name, leaves.Items[1].Name)
}

func TestWASPreparePodRequiresBothLabels(t *testing.T) {
	backend, _ := newRuntimeBackend(t)
	for _, label := range []string{apicommon.LabelPodGang, apicommon.LabelPodClique} {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{label: "value"}},
			Spec: corev1.PodSpec{
				SchedulerName:   "existing-scheduler",
				SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("existing-group")},
			},
		}
		before := pod.DeepCopy()
		require.Error(t, backend.PreparePod(pod))
		assert.Equal(t, before, pod, "failed preparation must not mutate the Pod")
	}
}

func TestWASHierarchyMutableMinimumsAndStatus(t *testing.T) {
	gang := runtimeGang("gang")
	gang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{{
		Name: "workers", PodGroupNames: []string{"worker"},
	}}
	backend, cl := newRuntimeBackend(t)
	require.NoError(t, backend.SyncPodGang(t.Context(), gang))
	leaf := &schedulingv1beta1.PodGroup{}
	key := client.ObjectKey{Namespace: gang.Namespace, Name: runtimeLeafName(gang.Name, "worker")}
	require.NoError(t, cl.Get(t.Context(), key, leaf))
	leaf.Status.Conditions = []metav1.Condition{{Type: "PodGroupInitiallyScheduled", Status: metav1.ConditionTrue, Reason: "Scheduled"}}
	require.NoError(t, cl.Status().Update(t.Context(), leaf))
	gang.Spec.PodGroups[0].MinReplicas = 3
	require.NoError(t, backend.SyncPodGang(t.Context(), gang))
	require.NoError(t, cl.Get(t.Context(), key, leaf))
	assert.EqualValues(t, 3, leaf.Spec.SchedulingPolicy.Gang.MinCount)
	assert.Equal(t, metav1.ConditionTrue, leaf.Status.Conditions[0].Status)
	workload := &schedulingv1beta1.Workload{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(gang), workload))
	assert.EqualValues(t, 3, workload.Spec.CompositePodGroupTemplates[0].CompositePodGroupTemplates[0].PodGroupTemplates[0].SchedulingPolicy.Gang.MinCount)
	rv := leaf.ResourceVersion
	require.NoError(t, backend.SyncPodGang(t.Context(), gang))
	require.NoError(t, cl.Get(t.Context(), key, leaf))
	assert.Equal(t, rv, leaf.ResourceVersion, "unchanged reconciliation must not write")
}

func TestHierarchyStructureEqualPreservesNamedPrioritiesWhenReordering(t *testing.T) {
	gang := runtimeGang("gang")
	gang.Spec.PodGroups = append(gang.Spec.PodGroups, groveschedulerv1alpha1.PodGroup{Name: "router", MinReplicas: 1})
	desired, err := buildWorkloadForPodGang(gang)
	require.NoError(t, err)
	leaves := desired.Spec.CompositePodGroupTemplates[0].PodGroupTemplates
	leaves[0].Name, leaves[0].PriorityClassName = "z-explicit", "high"
	leaves[1].Name = "a-default"
	existing := desired.DeepCopy()
	existing.Spec.CompositePodGroupTemplates[0].PriorityClassName = "default"
	leaves = existing.Spec.CompositePodGroupTemplates[0].PodGroupTemplates
	leaves[1].PriorityClassName = "default"
	leaves[0].Priority, leaves[1].Priority = ptr.To(int32(100)), ptr.To(int32(0))
	leaves[0], leaves[1] = leaves[1], leaves[0]
	beforeExisting, beforeDesired := existing.DeepCopy(), desired.DeepCopy()

	require.True(t, hierarchyStructureEqual(existing, desired), "defaults and ordering must not request a replacement")
	assert.Equal(t, beforeExisting, existing, "comparison must not mutate observed objects")
	assert.Equal(t, beforeDesired, desired, "comparison must not mutate desired objects")
	leaves[1].PriorityClassName = "wrong"
	assert.False(t, hierarchyStructureEqual(existing, desired), "an explicit priority must not inherit another leaf's default")
	flat := desired.DeepCopy()
	flat.Spec.PodGroupTemplates = flat.Spec.CompositePodGroupTemplates[0].PodGroupTemplates
	flat.Spec.CompositePodGroupTemplates = nil
	assert.True(t, hierarchyStructureEqual(flat, flat), "top-level leaves need no composite priority default")
}

func TestWASHierarchyReleasedMinimumRecovery(t *testing.T) {
	for _, missing := range []string{"workload", "leaf", "both"} {
		t.Run(missing, func(t *testing.T) {
			gang := runtimeGang("gang")
			backend, cl := newRuntimeBackend(t)
			require.NoError(t, backend.SyncPodGang(t.Context(), gang))
			gang.Spec.PodGroups[0].MinReplicas = 0
			require.NoError(t, backend.SyncPodGang(t.Context(), gang))
			if missing != "leaf" {
				require.NoError(t, cl.Delete(t.Context(), &schedulingv1beta1.Workload{ObjectMeta: metav1.ObjectMeta{Name: gang.Name, Namespace: gang.Namespace}}))
			}
			if missing != "workload" {
				require.NoError(t, cl.Delete(t.Context(), &schedulingv1beta1.PodGroup{ObjectMeta: metav1.ObjectMeta{Name: runtimeLeafName(gang.Name, "worker"), Namespace: gang.Namespace}}))
			}
			err := backend.SyncPodGang(t.Context(), gang)
			if missing == "both" {
				require.Error(t, err, "never invent a positive minimum without persisted evidence")
				return
			}
			require.NoError(t, err)
			leaf := &schedulingv1beta1.PodGroup{}
			require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Name: runtimeLeafName(gang.Name, "worker"), Namespace: gang.Namespace}, leaf))
			assert.EqualValues(t, 2, leaf.Spec.SchedulingPolicy.Gang.MinCount)
			assert.Zero(t, gang.Spec.PodGroups[0].MinReplicas, "cached input must not be mutated")
		})
	}
}

func TestWASHierarchyRejectsImmutableChangesWithoutDeletion(t *testing.T) {
	for _, mutation := range []struct {
		name string
		edit func(*groveschedulerv1alpha1.PodGang)
	}{
		{"priority", func(g *groveschedulerv1alpha1.PodGang) { g.Spec.PriorityClassName = "high" }},
		{"shape", func(g *groveschedulerv1alpha1.PodGang) {
			g.Spec.PodGroups = append(g.Spec.PodGroups, groveschedulerv1alpha1.PodGroup{Name: "new", MinReplicas: 1})
		}},
		{"topology", func(g *groveschedulerv1alpha1.PodGang) {
			g.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To("rack")}}
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			gang := runtimeGang("gang")
			backend, cl := newRuntimeBackend(t)
			require.NoError(t, backend.SyncPodGang(t.Context(), gang))
			before := &schedulingv1beta1.Workload{}
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(gang), before))
			mutation.edit(gang)
			require.ErrorContains(t, backend.SyncPodGang(t.Context(), gang), "immutable scheduling fields")
			after := &schedulingv1beta1.Workload{}
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(gang), after))
			assert.Equal(t, before, after, "must preserve serving hierarchy")
		})
	}
}

func TestWASHierarchyMissingWorkloadCannotHideStaleRuntime(t *testing.T) {
	gang := runtimeGang("gang")
	backend, cl := newRuntimeBackend(t)
	require.NoError(t, backend.SyncPodGang(t.Context(), gang))
	require.NoError(t, cl.Delete(t.Context(), &schedulingv1beta1.Workload{ObjectMeta: metav1.ObjectMeta{Name: gang.Name, Namespace: gang.Namespace}}))
	gang.Spec.PriorityClassName = "changed"
	require.ErrorContains(t, backend.SyncPodGang(t.Context(), gang), "immutable scheduling fields")
	workload := &schedulingv1beta1.Workload{}
	assert.True(t, apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(gang), workload)))
}

func TestWASHierarchyRejectsForeignAndTerminatingObjects(t *testing.T) {
	for _, state := range []string{"foreign", "terminating"} {
		t.Run(state, func(t *testing.T) {
			gang := runtimeGang("gang")
			backend, cl := newRuntimeBackend(t)
			require.NoError(t, backend.SyncPodGang(t.Context(), gang))
			leaf := &schedulingv1beta1.PodGroup{}
			key := client.ObjectKey{Name: runtimeLeafName(gang.Name, "worker"), Namespace: gang.Namespace}
			require.NoError(t, cl.Get(t.Context(), key, leaf))
			if state == "foreign" {
				leaf.OwnerReferences = nil
				require.NoError(t, cl.Update(t.Context(), leaf))
			} else {
				leaf.Finalizers = []string{"scheduling.k8s.io/podgroup-protection"}
				require.NoError(t, cl.Update(t.Context(), leaf))
				require.NoError(t, cl.Delete(t.Context(), leaf))
			}
			require.Error(t, backend.SyncPodGang(t.Context(), gang))
			require.NoError(t, cl.Get(t.Context(), key, leaf), "must not adopt, delete, or strip finalizers")
		})
	}
}

func TestWASHierarchyRecoversPartialCreateAndPatchConflicts(t *testing.T) {
	gang := runtimeGang("gang")
	backend, cl := newRuntimeBackend(t)
	failCreate := true
	backend.client = interceptor.NewClient(cl, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, leaf := obj.(*schedulingv1beta1.PodGroup); leaf && failCreate {
				return errors.New("injected leaf creation failure")
			}
			return c.Create(ctx, obj, opts...)
		},
	})
	require.Error(t, backend.SyncPodGang(t.Context(), gang))
	leaf := &schedulingv1beta1.PodGroup{}
	key := client.ObjectKey{Namespace: gang.Namespace, Name: runtimeLeafName(gang.Name, "worker")}
	require.True(t, apierrors.IsNotFound(cl.Get(t.Context(), key, leaf)))
	failCreate = false
	require.NoError(t, backend.SyncPodGang(t.Context(), gang))
	require.NoError(t, cl.Get(t.Context(), key, leaf))
	assert.EqualValues(t, 2, leaf.Spec.SchedulingPolicy.Gang.MinCount)
	gang.Spec.PodGroups[0].MinReplicas = 3
	backend.client = interceptor.NewClient(cl, interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, obj client.Object, patch client.Patch, _ ...client.PatchOption) error {
			data, err := patch.Data(obj)
			require.NoError(t, err)
			var body struct {
				Metadata struct {
					ResourceVersion string `json:"resourceVersion"`
				} `json:"metadata"`
			}
			require.NoError(t, json.Unmarshal(data, &body))
			require.NotEmpty(t, body.Metadata.ResourceVersion, "mutable updates must use optimistic locking")
			return apierrors.NewConflict(schema.GroupResource{Group: "scheduling.k8s.io", Resource: "workloads"}, obj.GetName(), errors.New("concurrent write"))
		},
	})
	assert.True(t, apierrors.IsConflict(backend.SyncPodGang(t.Context(), gang)))
	backend.client = cl
	require.NoError(t, backend.SyncPodGang(t.Context(), gang))
}

func TestWASHierarchyIgnoresOrderingAndWarnsForPreferredTopology(t *testing.T) {
	gang := runtimeGang("gang")
	gang.Spec.PodGroups = append(gang.Spec.PodGroups, groveschedulerv1alpha1.PodGroup{Name: "other", MinReplicas: 1})
	gang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To("rack"), Preferred: ptr.To("host")},
	}
	backend, _ := newRuntimeBackend(t)
	require.NoError(t, backend.SyncPodGang(t.Context(), gang))
	gang.Spec.PodGroups[0], gang.Spec.PodGroups[1] = gang.Spec.PodGroups[1], gang.Spec.PodGroups[0]
	require.NoError(t, backend.SyncPodGang(t.Context(), gang))
	select {
	case event := <-backend.eventRecorder.(*record.FakeRecorder).Events:
		assert.Contains(t, event, "KubeBackendPreferredTopologyIgnored")
	default:
		t.Fatal("expected preferred topology warning")
	}
}

func newRuntimeBackend(t *testing.T) (*schedulerBackend, client.WithWatch) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))
	require.NoError(t, schedulingv1beta1.AddToScheme(scheme))
	require.NoError(t, schedulingv1alpha3.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&schedulingv1beta1.PodGroup{}, &schedulingv1alpha3.CompositePodGroup{}).Build()
	return &schedulerBackend{
		client: cl, scheme: scheme, name: string(configv1alpha1.SchedulerNameKube),
		gangSchedulingEnabled: true, eventRecorder: record.NewFakeRecorder(100),
	}, cl
}

func runtimeGang(name string) *groveschedulerv1alpha1.PodGang {
	gang := testutils.NewPodGangBuilder(name, "default").WithPodGroup("worker", 2).Build()
	gang.UID = types.UID(name)
	return gang
}
