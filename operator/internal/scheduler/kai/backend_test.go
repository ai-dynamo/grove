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
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBackend_PreparePod(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").
		WithSchedulerName("default-scheduler").
		Build()
	pod.Labels = map[string]string{
		apicommon.LabelPodGang:                  "different-podgang-name",
		apicommon.LabelPodClique:                "test-clique",
		apicommon.LabelPartOfKey:                "test-pcs",
		apicommon.LabelPodCliqueSetReplicaIndex: "0",
	}
	pod.Annotations = map[string]string{"keep": "me"}

	require.NoError(t, b.PreparePod(pod))

	assert.Equal(t, "kai-scheduler", pod.Spec.SchedulerName)
	assert.Equal(t, "me", pod.Annotations["keep"])
	assert.Equal(t, "true", pod.Annotations["kai.scheduler/skip-podgrouper"])
	assert.Equal(t, "grove-test-pcs-0", pod.Annotations["pod-group-name"])
	assert.Equal(t, "test-clique", pod.Labels["kai.scheduler/subgroup-name"])
}

func TestAggregatePodGroupNameUsesPCSNameAndReplica(t *testing.T) {
	pcsName := "My.Very_Long_PCS_Name_With_Invalid_Characters_And_Enough_Content_To_Require_Hashing"

	replicaZero := aggregatePodGroupName(pcsName, 0)
	replicaOne := aggregatePodGroupName(pcsName, 1)

	assert.Equal(t, replicaZero, aggregatePodGroupName(pcsName, 0), "aggregate names must be deterministic")
	assert.NotEqual(t, replicaZero, replicaOne, "PCS replicas must have distinct aggregate names")
	assert.LessOrEqual(t, len(replicaZero), 63)
	assert.LessOrEqual(t, len(replicaOne), 63)
	assert.Regexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, replicaZero)
	assert.Regexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, replicaOne)
	assert.Contains(t, replicaZero, "grove-my-very-long-pcs-name")
}

func TestBackend_PreparePod_PreservesExistingSkipAnnotation(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").
		Build()
	pod.Labels = map[string]string{
		apicommon.LabelPodGang:                  "test-pcs-0",
		apicommon.LabelPodClique:                "test-clique",
		apicommon.LabelPartOfKey:                "test-pcs",
		apicommon.LabelPodCliqueSetReplicaIndex: "0",
	}
	pod.Annotations = map[string]string{"kai.scheduler/skip-podgrouper": "custom"}

	require.NoError(t, b.PreparePod(pod))

	assert.Equal(t, "true", pod.Annotations["kai.scheduler/skip-podgrouper"])
}

func TestBackend_SyncPodGang_CreateAndUpdate(t *testing.T) {
	pcs := newPodCliqueSet(
		"test-pcs",
		"team-a",
		podCliqueTemplateWithQueue("encoder-template", "team-a"),
		podCliqueTemplateWithQueue("decoder-template", "team-a"),
	)
	podGang := testutils.NewPodGangBuilder("test-pcs-2", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		Build()
	setPodCliqueSetControllerOwner(podGang, pcs)
	podGang.Labels[apicommon.LabelPodCliqueSetReplicaIndex] = "2"
	podGang.Labels[labelKeyQueueName] = "legacy-queue"
	podGang.Annotations = map[string]string{"grove.io/topology-name": "cluster-topology"}
	podGang.Spec.PriorityClassName = "high-priority"
	podGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{
			Required: ptr.To("zone"),
		},
	}
	podGang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
		{
			Name:          "decoder-group",
			PodGroupNames: []string{"decoder"},
			TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{
					Preferred: ptr.To("rack"),
				},
			},
		},
	}
	podGang.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{
		{
			Name:        "encoder",
			MinReplicas: 2,
			TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{
					Required: ptr.To("host"),
				},
			},
		},
		{
			Name:        "decoder",
			MinReplicas: 3,
		},
	}

	cl := testutils.NewTestClientBuilder().
		WithObjects(pcs, podGang, &grovecorev1alpha1.ClusterTopologyBinding{ObjectMeta: metav1.ObjectMeta{Name: "cluster-topology"}}).
		Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(cl))

	ctx := context.Background()
	require.NoError(t, b.SyncPodGang(ctx, podGang))

	gotPodGroup := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: "grove-test-pcs-2", Namespace: podGang.Namespace}, gotPodGroup))

	require.NotNil(t, gotPodGroup.Spec.MinSubGroup)
	assert.Equal(t, int32(1), *gotPodGroup.Spec.MinSubGroup)
	assert.Nil(t, gotPodGroup.Spec.MinMember)
	assert.Equal(t, "team-a", gotPodGroup.Spec.Queue)
	assert.Equal(t, "high-priority", gotPodGroup.Spec.PriorityClassName)
	assert.Equal(t, "zone", gotPodGroup.Spec.TopologyConstraint.RequiredTopologyLevel)

	require.Len(t, gotPodGroup.Spec.SubGroups, 4)
	baseBranch := requireSubGroup(t, gotPodGroup, podGang.Name)
	assert.Nil(t, baseBranch.Parent)

	decoderGroup := requireSubGroup(t, gotPodGroup, "decoder-group")
	require.NotNil(t, decoderGroup.MinSubGroup)
	assert.Equal(t, int32(1), *decoderGroup.MinSubGroup)

	decoder := requireSubGroup(t, gotPodGroup, "decoder")
	require.NotNil(t, decoder.Parent)
	assert.Equal(t, decoderGroup.Name, *decoder.Parent)
	require.NotNil(t, decoder.MinMember)
	assert.Equal(t, int32(3), *decoder.MinMember)

	encoder := requireSubGroup(t, gotPodGroup, "encoder")
	require.NotNil(t, encoder.MinMember)
	assert.Equal(t, int32(2), *encoder.MinMember)
	require.NotNil(t, encoder.Parent)
	assert.Equal(t, baseBranch.Name, *encoder.Parent)
	assert.Equal(t, "host", encoder.TopologyConstraint.RequiredTopologyLevel)

	assert.Nil(t, findSubGroup(gotPodGroup, scaledPodGangsSubGroupName))

	// Update PodGang: change min replicas. Queue remains sourced from the owning PCS.
	updatedPodGang := podGang.DeepCopy()
	updatedPodGang.Spec.PodGroups[0].MinReplicas = 4
	require.NoError(t, cl.Update(ctx, updatedPodGang))

	require.NoError(t, b.SyncPodGang(ctx, updatedPodGang))
	gotAfterUpdate := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: "grove-test-pcs-2", Namespace: podGang.Namespace}, gotAfterUpdate))

	// The owning PCS label supplies the queue, consistently with its templates.
	assert.Equal(t, "team-a", gotAfterUpdate.Spec.Queue)
	require.NotNil(t, gotAfterUpdate.Spec.MinSubGroup)
	assert.Equal(t, int32(1), *gotAfterUpdate.Spec.MinSubGroup)
	assert.Nil(t, gotAfterUpdate.Spec.MinMember)
	updatedEncoder := requireSubGroup(t, gotAfterUpdate, "encoder")
	require.NotNil(t, updatedEncoder.MinMember)
	assert.Equal(t, int32(4), *updatedEncoder.MinMember)
	assert.Nil(t, findSubGroup(gotAfterUpdate, scaledPodGangsSubGroupName))
}

func TestBackend_SyncPodGang_RemovesFinalizerWhenDeletingAndOwningPodCliqueSetIsMissing(t *testing.T) {
	missingPCS := newPodCliqueSet("missing-pcs", "team-a")
	podGang := testutils.NewPodGangBuilder("missing-pcs-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		Build()
	setPodCliqueSetControllerOwner(podGang, missingPCS)
	podGang.Finalizers = []string{podGangFinalizer}
	now := metav1.Now()
	podGang.DeletionTimestamp = &now

	cl := testutils.NewTestClientBuilder().WithObjects(podGang).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	require.NoError(t, b.Init(cl))

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))
	assert.NotContains(t, podGang.Finalizers, podGangFinalizer)

	current := &groveschedulerv1alpha1.PodGang{}
	err := cl.Get(context.Background(), client.ObjectKeyFromObject(podGang), current)
	if !apierrors.IsNotFound(err) {
		require.NoError(t, err)
		assert.NotContains(t, current.Finalizers, podGangFinalizer)
	}
}

func TestBackend_SyncPodGang_ReturnsNotFoundWhenActiveAndOwningPodCliqueSetIsMissing(t *testing.T) {
	missingPCS := newPodCliqueSet("missing-active-pcs", "team-a")
	podGang := testutils.NewPodGangBuilder("missing-active-pcs-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		Build()
	setPodCliqueSetControllerOwner(podGang, missingPCS)
	podGang.Finalizers = []string{podGangFinalizer}

	cl := testutils.NewTestClientBuilder().WithObjects(podGang).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	require.NoError(t, b.Init(cl))

	err := b.SyncPodGang(context.Background(), podGang)
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err))
	assert.Contains(t, podGang.Finalizers, podGangFinalizer)

	current := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(podGang), current))
	assert.Contains(t, current.Finalizers, podGangFinalizer)
}

func TestBackend_SyncPodGang_SkipsEmptyTopologyConstraintGroups(t *testing.T) {
	pcs := newPodCliqueSet(
		"empty-group-pcs",
		"team-a",
		podCliqueTemplateWithQueue("worker-template", "team-a"),
	)
	podGang := testutils.NewPodGangBuilder("empty-group-pcs-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		Build()
	setPodCliqueSetControllerOwner(podGang, pcs)
	podGang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
		{
			Name: "empty-group",
			TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{
					Required: ptr.To("host"),
				},
			},
		},
	}
	podGang.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{
		{Name: "worker", MinReplicas: 1},
	}

	cl := testutils.NewTestClientBuilder().WithObjects(pcs, podGang).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	require.NoError(t, b.Init(cl))

	ctx := context.Background()
	require.NoError(t, b.SyncPodGang(ctx, podGang))

	podGroup := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: podGang.Namespace, Name: aggregatePodGroupName(pcs.Name, 0)}, podGroup))
	require.NotNil(t, podGroup.Spec.MinSubGroup)
	assert.Equal(t, int32(1), *podGroup.Spec.MinSubGroup)
	require.Len(t, podGroup.Spec.SubGroups, 2)
	assert.Nil(t, findSubGroup(podGroup, "empty-group"))
	worker := requireSubGroup(t, podGroup, "worker")
	require.NotNil(t, worker.Parent)
	assert.Equal(t, podGang.Name, *worker.Parent)
	assert.Nil(t, findSubGroup(podGroup, scaledPodGangsSubGroupName))
}

func TestBackend_SyncPodGangSetsOwnerReferenceAndSkipAnnotation(t *testing.T) {
	pcs := newPodCliqueSet(
		"owned-pcs",
		"team-a",
		podCliqueTemplateWithQueue("worker-template", "team-a"),
	)
	podGang := testutils.NewPodGangBuilder("owned-pcs-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		Build()
	setPodCliqueSetControllerOwner(podGang, pcs)

	cl := testutils.NewTestClientBuilder().WithObjects(pcs, podGang).Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(cl))

	ctx := context.Background()
	require.NoError(t, b.SyncPodGang(ctx, podGang))

	podGroup := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: aggregatePodGroupName(pcs.Name, 0), Namespace: podGang.Namespace}, podGroup))
	require.Len(t, podGroup.OwnerReferences, 1)
	assert.Equal(t, pcs.Name, podGroup.OwnerReferences[0].Name)

	updatedPodGang := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(podGang), updatedPodGang))
	assert.Equal(t, annotationValSkipPGR, updatedPodGang.Annotations[annotationKeySkipPGR])
}

func TestBackend_SyncPodGang_UsesUniquePodCliqueTemplateQueue(t *testing.T) {
	pcs := newPodCliqueSet(
		"template-queue-pcs",
		"",
		podCliqueTemplateWithQueue("worker-a", "team-a"),
		podCliqueTemplateWithQueue("worker-b", "team-a"),
	)
	podGang := testutils.NewPodGangBuilder("template-queue-pcs-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		Build()
	setPodCliqueSetControllerOwner(podGang, pcs)

	cl := testutils.NewTestClientBuilder().WithObjects(pcs, podGang).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	require.NoError(t, b.Init(cl))

	ctx := context.Background()
	require.NoError(t, b.SyncPodGang(ctx, podGang))

	podGroup := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: podGang.Namespace, Name: aggregatePodGroupName(pcs.Name, 0)}, podGroup))
	assert.Equal(t, "team-a", podGroup.Spec.Queue)
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

func TestBackend_SyncPodGang_QueueResolutionFailuresDoNotCreatePodGroup(t *testing.T) {
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
			name: "conflicting template queues without PodCliqueSet override",
			pcs: newPodCliqueSet(
				"conflicting-queues-pcs",
				"",
				podCliqueTemplateWithQueue("worker-a", "team-a"),
				podCliqueTemplateWithQueue("worker-b", "team-b"),
			),
			wantErrSubstr: "conflicting KAI queues",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			podGangName := "test-podgang"
			if tt.pcs != nil {
				podGangName = tt.pcs.Name + "-0"
			}
			podGang := testutils.NewPodGangBuilder(podGangName, "default").
				WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
				Build()
			objects := []client.Object{podGang}
			if tt.pcs != nil {
				setPodCliqueSetControllerOwner(podGang, tt.pcs)
				if !tt.omitPCS {
					objects = append(objects, tt.pcs)
				}
			}

			cl := testutils.NewTestClientBuilder().WithObjects(objects...).Build()
			b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
			require.NoError(t, b.Init(cl))

			err := b.SyncPodGang(context.Background(), podGang)
			require.ErrorContains(t, err, tt.wantErrSubstr)

			podGroup := &kaischedulingv2alpha2.PodGroup{}
			pcsName := podGang.Name
			if tt.pcs != nil {
				pcsName = tt.pcs.Name
			}
			podGroupName := aggregatePodGroupName(pcsName, 0)
			err = cl.Get(context.Background(), client.ObjectKey{Namespace: podGang.Namespace, Name: podGroupName}, podGroup)
			assert.True(t, apierrors.IsNotFound(err), "PodGroup must not be created when queue resolution fails")
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
	for key, value := range apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name) {
		podGang.Labels[key] = value
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
