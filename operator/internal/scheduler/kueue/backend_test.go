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

package kueue

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBackend_PreparePod_Defaults(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	assert.NoError(t, b.Init(nil))

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "test-queue"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
				},
			},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-0-worker"}}
	pod := &corev1.Pod{Spec: corev1.PodSpec{SchedulerName: "kueue"}}
	preparation := scheduler.PodPreparationContext{
		PodCliqueSet: pcs,
		PodClique:    pclq,
		PodGangName:  "test-pcs-0",
	}

	require.NoError(t, b.PreparePod(pod, preparation))

	assert.Equal(t, string(configv1alpha1.SchedulerNameKube), pod.Spec.SchedulerName)
	assert.Equal(t, "test-queue", pod.Labels[queueNameLabel])
	assert.Equal(t, "test-pcs-0", pod.Labels[podGroupNameLabel])
	assert.Equal(t, "test-pcs-0", pod.Labels[prebuiltWorkloadNameLabel])
	assert.Equal(t, "2", pod.Annotations[podGroupTotalCountAnnotation])
	assert.Equal(t, "test-pcs-0-worker", pod.Annotations[roleHashAnnotation])
	// Grove pods are not marked as a Kueue serving group, so Kueue can finalize them on teardown.
	assert.Empty(t, pod.Annotations[podGroupServingAnnotation])
	assert.Equal(t, "false", pod.Annotations[retriableInGroupAnnotation])
}

func TestBackend_PreparePod_ConfigAndExistingMetadata(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{
		Name: configv1alpha1.SchedulerNameKueue,
		Config: &runtime.RawExtension{
			Raw: []byte(`{"requiredTopologyKey":"topology.ai-dynamo.io/rack","underlyingSchedulerName":"custom-scheduler"}`),
		},
	}
	b := New(cl, cl.Scheme(), recorder, profile)
	assert.NoError(t, b.Init(nil))

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "configured-queue"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 7}},
				},
			},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-0-worker"}}
	pod := &corev1.Pod{}
	preparation := scheduler.PodPreparationContext{
		PodCliqueSet: pcs,
		PodClique:    pclq,
		PodGangName:  "test-pcs-0",
	}

	require.NoError(t, b.PreparePod(pod, preparation))

	assert.Equal(t, "custom-scheduler", pod.Spec.SchedulerName)
	assert.Equal(t, "configured-queue", pod.Labels[queueNameLabel])
	assert.Equal(t, "test-pcs-0", pod.Labels[podGroupNameLabel])
	assert.Equal(t, "7", pod.Annotations[podGroupTotalCountAnnotation])
	assert.Equal(t, "topology.ai-dynamo.io/rack", pod.Annotations[podSetRequiredTopologyAnnotation])
}

func TestBackend_PreparePod_PCSGAndTopology(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	pcsgReplicas := int32(2)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{},
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "decode", CliqueNames: []string{"leader", "worker"}, Replicas: &pcsgReplicas},
				},
			},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "demo-0-decode-0-worker"}}
	rackKey := "topology.ai-dynamo.io/rack"
	podGang := &groveschedulerv1alpha1.PodGang{
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{
					Name: "demo-0-decode-0-worker",
					TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
						PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
					},
				},
			},
		},
	}
	pod := &corev1.Pod{}
	preparation := scheduler.PodPreparationContext{
		PodCliqueSet: pcs,
		PodClique:    pclq,
		PodGang:      podGang,
		PodGangName:  "demo-0",
	}

	require.NoError(t, b.PreparePod(pod, preparation))

	assert.Equal(t, "6", pod.Annotations[podGroupTotalCountAnnotation])
	assert.Equal(t, "demo-0-decode-0-worker", pod.Annotations[roleHashAnnotation])
	assert.Equal(t, rackKey, pod.Annotations[podSetRequiredTopologyAnnotation])
}

func TestBackend_PreparePod_RequiresPodGangForTopology(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{},
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
				},
			},
		},
	}
	preparation := scheduler.PodPreparationContext{
		PodCliqueSet: pcs,
		PodClique:    &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "demo-0-worker"}},
		PodGangName:  "demo-0",
	}

	err := b.PreparePod(&corev1.Pod{}, preparation)

	require.ErrorContains(t, err, "is required to resolve Kueue topology")
}

func TestBackend_SyncPodGang_RequiresPodCliqueSetQueueLabel(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
				},
			},
		},
	}
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: "default",
			Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{{Name: "demo-0-worker", MinReplicas: 1}},
		},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, podGang).Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	require.ErrorContains(t, b.SyncPodGang(context.Background(), podGang), `must set label "kueue.x-k8s.io/queue-name"`)
}

func TestBackend_SyncPodGang_CreatesPrebuiltWorkloadForSimplePCS(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4}},
				},
			},
		},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	rackKey := "topology.ai-dynamo.io/rack"
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: "default",
			UID:       "demo-0-uid",
			Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{
					Name:        "demo-0-worker",
					MinReplicas: 2,
					TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
						PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
					},
				},
			},
		},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(workloadGVK)
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))

	queueName, _, _ := unstructured.NestedString(got.Object, "spec", "queueName")
	assert.Equal(t, "grove-poc", queueName)

	ownerRefs := got.GetOwnerReferences()
	require.Len(t, ownerRefs, 1)
	assert.Equal(t, "PodGang", ownerRefs[0].Kind)
	assert.Equal(t, "demo-0", ownerRefs[0].Name)

	podSets, _, _ := unstructured.NestedSlice(got.Object, "spec", "podSets")
	require.Len(t, podSets, 1)
	podSet := podSets[0].(map[string]any)
	assert.Equal(t, "demo-0-worker", podSet["name"])
	assert.Equal(t, int64(4), podSet["count"])
	assert.Equal(t, int64(2), podSet["minCount"])
	topologyRequest := podSet["topologyRequest"].(map[string]any)
	assert.Equal(t, rackKey, topologyRequest["required"])
}

func TestBackend_SyncPodGang_CreatesPrebuiltWorkloadForPCSGWithMinCountEqualsCount(t *testing.T) {
	pcsgReplicas := int32(2)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "decode", CliqueNames: []string{"leader", "worker"}, Replicas: &pcsgReplicas},
				},
			},
		},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: "default",
			Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				// MinReplicas is deliberately lower than the clique replicas to prove the PCSG
				// all-or-nothing override forces minCount == count.
				{Name: "demo-0-decode-0-worker", MinReplicas: 1},
			},
		},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(workloadGVK)
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))

	podSets, _, _ := unstructured.NestedSlice(got.Object, "spec", "podSets")
	require.Len(t, podSets, 1)
	podSet := podSets[0].(map[string]any)
	assert.Equal(t, "demo-0-decode-0-worker", podSet["name"])
	assert.Equal(t, int64(2), podSet["count"])
	// PodCliqueScalingGroup cliques are all-or-nothing: minCount is omitted (Kueue defaults it to count).
	// Kueue also rejects Workloads where more than one podSet sets minCount.
	_, hasMinCount := podSet["minCount"]
	assert.False(t, hasMinCount)
}

func TestBackend_ValidatePodCliqueSet_MinCount(t *testing.T) {
	testCases := []struct {
		description string
		cliques     []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig
		wantErr     bool
		wantErrMsg  string
	}{
		{
			description: "no partial-gang standalone clique is valid",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](4)}},
			},
		},
		{
			description: "single partial-gang standalone clique is valid",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](2)}},
				{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To[int32](2)}},
			},
		},
		{
			description: "two partial-gang standalone cliques are rejected",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](2)}},
				{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To[int32](1)}},
			},
			wantErr:    true,
			wantErrMsg: "at most one standalone PodClique with minAvailable < replicas",
		},
		{
			description: "scaling-group cliques with minAvailable == replicas are valid",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](4)}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "decode", CliqueNames: []string{"leader", "worker"}},
			},
		},
		{
			description: "scaling-group clique with minAvailable < replicas is rejected",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](3)}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "decode", CliqueNames: []string{"leader", "worker"}},
			},
			wantErr:    true,
			wantErrMsg: "members of a PodCliqueScalingGroup to set minAvailable == replicas",
		},
		{
			description: "nil minAvailable is treated as full gang",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4}},
				{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To[int32](1)}},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			cl := testutils.CreateDefaultFakeClient(nil)
			recorder := record.NewFakeRecorder(10)
			profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
			b := New(cl, cl.Scheme(), recorder, profile)
			require.NoError(t, b.Init(nil))

			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques:                      tc.cliques,
						PodCliqueScalingGroupConfigs: tc.pcsgConfigs,
					},
				},
			}

			err := b.ValidatePodCliqueSet(context.Background(), pcs)
			if tc.wantErr {
				require.ErrorContains(t, err, tc.wantErrMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBackend_TopologyGVR(t *testing.T) {
	b := newKueueBackend(testutils.CreateDefaultFakeClient(nil))

	assert.Equal(t, schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta2",
		Resource: "topologies",
	}, b.TopologyGVR())
}

func TestBackend_SyncTopologyCreatesKueueTopology(t *testing.T) {
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	b := newKueueBackend(cl)
	ct := testClusterTopology()

	require.NoError(t, b.SyncTopology(ctx, cl, ct))

	topology := newKueueTopology(ct.Name)
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: ct.Name}, topology))
	assert.True(t, metav1.IsControlledBy(topology, ct))
	assert.Equal(t, []any{
		map[string]any{"nodeLabel": "topology.ai-dynamo.io/rack"},
		map[string]any{"nodeLabel": "kubernetes.io/hostname"},
	}, kueueTopologyLevels(topology))
}

func TestBackend_CheckTopologyDrift(t *testing.T) {
	ctx := context.Background()
	ct := testClusterTopology()
	topology, err := buildKueueTopology(ct.Name, ct, testutils.CreateDefaultFakeClient(nil).Scheme())
	require.NoError(t, err)
	cl := testutils.NewTestClientBuilder().WithObjects(ct, topology).Build()
	b := newKueueBackend(cl)

	inSync, message, _, err := b.CheckTopologyDrift(ctx, ct, grovecorev1alpha1.SchedulerTopologyBinding{
		SchedulerName:     string(configv1alpha1.SchedulerNameKueue),
		TopologyReference: ct.Name,
	})

	require.NoError(t, err)
	assert.True(t, inSync)
	assert.Empty(t, message)
}

func newKueueBackend(cl client.Client) scheduler.TopologyAwareBackend {
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	return b.(scheduler.TopologyAwareBackend)
}

func testClusterTopology() *grovecorev1alpha1.ClusterTopologyBinding {
	return &grovecorev1alpha1.ClusterTopologyBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "grove-kind-topology",
			UID:  uuid.NewUUID(),
		},
		Spec: grovecorev1alpha1.ClusterTopologyBindingSpec{
			Levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainRack, Key: "topology.ai-dynamo.io/rack"},
				{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
		},
	}
}
