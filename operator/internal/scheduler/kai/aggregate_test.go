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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestSyncAggregatePodGroupSerializesPodGangsForSameReplica(t *testing.T) {
	ctx := context.Background()
	pcs := newPodCliqueSet("job", "team-a")
	base := testutils.NewPodGangBuilder("job-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		Build()
	setPodCliqueSetControllerOwner(base, pcs)
	scaled := testutils.NewPodGangBuilder("job-0-workers-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		Build()
	setPodCliqueSetControllerOwner(scaled, pcs)
	scaled.Labels[apicommon.LabelPodCliqueScalingGroup] = "job-0-workers"

	baseClient := testutils.NewTestClientBuilder().WithObjects(pcs, base, scaled).Build()
	blockingClient := &blockingPodGangListClient{
		Client:            baseClient,
		firstListStarted:  make(chan struct{}),
		secondListStarted: make(chan struct{}),
		releaseFirstList:  make(chan struct{}),
	}
	backend := New(blockingClient, baseClient.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}).(*schedulerBackend)
	require.NoError(t, backend.Init(blockingClient))

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- backend.syncAggregatePodGroup(ctx, pcs, base)
	}()
	require.Eventually(t, func() bool {
		select {
		case <-blockingClient.firstListStarted:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	secondInvoked := make(chan struct{})
	secondResult := make(chan error, 1)
	go func() {
		close(secondInvoked)
		secondResult <- backend.syncAggregatePodGroup(ctx, pcs, scaled)
	}()
	<-secondInvoked
	assert.Never(t, func() bool {
		select {
		case <-blockingClient.secondListStarted:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, time.Millisecond)

	close(blockingClient.releaseFirstList)
	require.NoError(t, <-firstResult)
	require.NoError(t, <-secondResult)
}

func TestBuildAggregatePodGroupBaseOnlyOmitsScaledPodGangCollection(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	base := testutils.NewPodGangBuilder("job-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("base-a", 1).
		WithPodGroup("base-b", 2).
		Build()
	setPodCliqueSetControllerOwner(base, pcs)

	cl := testutils.NewTestClientBuilder().Build()
	backend := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}).(*schedulerBackend)
	require.NoError(t, backend.Init(cl))

	aggregate, err := backend.buildAggregatePodGroup(
		pcs,
		0,
		base,
		[]groveschedulerv1alpha1.PodGang{*base},
		"",
	)
	require.NoError(t, err)
	require.NotNil(t, aggregate.Spec.MinSubGroup)
	assert.Equal(t, int32(1), *aggregate.Spec.MinSubGroup)
	assert.Nil(t, aggregate.Spec.MinMember)
	assert.Nil(t, findSubGroup(aggregate, scaledPodGangsSubGroupName))

	baseBranch := requireSubGroup(t, aggregate, stableKAIName(base.Name))
	assert.Nil(t, baseBranch.Parent)
	require.NotNil(t, baseBranch.MinSubGroup)
	assert.Equal(t, int32(2), *baseBranch.MinSubGroup)
}

func TestBuildAggregatePodGroupUsesSharedScaledPodGangCollection(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	base := testutils.NewPodGangBuilder("job-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("base-a", 1).
		WithPodGroup("base-b", 2).
		Build()
	setPodCliqueSetControllerOwner(base, pcs)

	scaledA := testutils.NewPodGangBuilder("job-0-workers-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("scaled-a", 1).
		Build()
	setPodCliqueSetControllerOwner(scaledA, pcs)
	scaledA.Labels[apicommon.LabelPodCliqueScalingGroup] = "job-0-workers"

	scaledB := testutils.NewPodGangBuilder("job-0-workers-1", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("scaled-b", 1).
		Build()
	setPodCliqueSetControllerOwner(scaledB, pcs)
	scaledB.Labels[apicommon.LabelPodCliqueScalingGroup] = "job-0-workers"

	cl := testutils.NewTestClientBuilder().Build()
	backend := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}).(*schedulerBackend)
	require.NoError(t, backend.Init(cl))

	aggregate, err := backend.buildAggregatePodGroup(
		pcs,
		0,
		base,
		[]groveschedulerv1alpha1.PodGang{*base, *scaledA, *scaledB},
		"",
	)
	require.NoError(t, err)

	require.NotNil(t, aggregate.Spec.MinSubGroup)
	assert.Equal(t, int32(2), *aggregate.Spec.MinSubGroup)
	assert.Nil(t, aggregate.Spec.MinMember)

	baseBranch := requireSubGroup(t, aggregate, stableKAIName(base.Name))
	assert.Equal(t, base.Name, baseBranch.Name)
	assert.Nil(t, baseBranch.Parent)
	require.NotNil(t, baseBranch.MinSubGroup)
	assert.Equal(t, int32(2), *baseBranch.MinSubGroup)

	scaledCollection := requireSubGroup(t, aggregate, scaledPodGangsSubGroupName)
	assert.Nil(t, scaledCollection.Parent)
	require.NotNil(t, scaledCollection.MinSubGroup)
	assert.Equal(t, int32(0), *scaledCollection.MinSubGroup)
	assert.Nil(t, scaledCollection.MinMember)
	assert.Nil(t, scaledCollection.TopologyConstraint)

	rootSubGroups := make([]string, 0, 2)
	for i := range aggregate.Spec.SubGroups {
		if aggregate.Spec.SubGroups[i].Parent == nil {
			rootSubGroups = append(rootSubGroups, aggregate.Spec.SubGroups[i].Name)
		}
	}
	assert.ElementsMatch(t, []string{baseBranch.Name, scaledPodGangsSubGroupName}, rootSubGroups)

	for _, scaledPodGang := range []*groveschedulerv1alpha1.PodGang{scaledA, scaledB} {
		branch := requireSubGroup(t, aggregate, stableKAIName(scaledPodGang.Name))
		require.NotNil(t, branch.Parent)
		assert.Equal(t, scaledCollection.Name, *branch.Parent)
	}
}

func TestBuildAggregatePodGroupSharesScaledCollectionAcrossPCSGLabels(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	base := testutils.NewPodGangBuilder("job-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("base", 1).
		Build()
	setPodCliqueSetControllerOwner(base, pcs)

	scaledA := testutils.NewPodGangBuilder("job-0-workers-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("worker", 1).
		Build()
	setPodCliqueSetControllerOwner(scaledA, pcs)
	scaledA.Labels[apicommon.LabelPodCliqueScalingGroup] = "job-0-workers"

	scaledB := testutils.NewPodGangBuilder("job-0-evaluators-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("evaluator", 1).
		Build()
	setPodCliqueSetControllerOwner(scaledB, pcs)
	scaledB.Labels[apicommon.LabelPodCliqueScalingGroup] = "job-0-evaluators"

	cl := testutils.NewTestClientBuilder().Build()
	backend := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}).(*schedulerBackend)
	require.NoError(t, backend.Init(cl))

	aggregate, err := backend.buildAggregatePodGroup(
		pcs,
		0,
		base,
		[]groveschedulerv1alpha1.PodGang{*base, *scaledA, *scaledB},
		"",
	)
	require.NoError(t, err)
	require.NotNil(t, aggregate.Spec.MinSubGroup)
	assert.Equal(t, int32(2), *aggregate.Spec.MinSubGroup)
	assert.Nil(t, aggregate.Spec.MinMember)

	collection := requireSubGroup(t, aggregate, scaledPodGangsSubGroupName)
	assert.Nil(t, collection.Parent)
	require.NotNil(t, collection.MinSubGroup)
	assert.Equal(t, int32(0), *collection.MinSubGroup)
	assert.Nil(t, collection.MinMember)
	assert.Nil(t, collection.TopologyConstraint)
	assert.Nil(t, findSubGroup(aggregate, scaledA.Labels[apicommon.LabelPodCliqueScalingGroup]))
	assert.Nil(t, findSubGroup(aggregate, scaledB.Labels[apicommon.LabelPodCliqueScalingGroup]))

	for _, scaledPodGang := range []*groveschedulerv1alpha1.PodGang{scaledA, scaledB} {
		branch := requireSubGroup(t, aggregate, stableKAIName(scaledPodGang.Name))
		require.NotNil(t, branch.Parent)
		assert.Equal(t, collection.Name, *branch.Parent)
	}

	rootSubGroups := make([]string, 0, 2)
	for i := range aggregate.Spec.SubGroups {
		if aggregate.Spec.SubGroups[i].Parent == nil {
			rootSubGroups = append(rootSubGroups, aggregate.Spec.SubGroups[i].Name)
		}
	}
	assert.ElementsMatch(t, []string{stableKAIName(base.Name), scaledPodGangsSubGroupName}, rootSubGroups)
}

func TestBuildAggregatePodGroupRequiresPCSGLabelForScaledPodGang(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	base := testutils.NewPodGangBuilder("job-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("base", 1).
		Build()
	setPodCliqueSetControllerOwner(base, pcs)

	scaled := testutils.NewPodGangBuilder("job-0-workers-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("worker", 1).
		Build()
	setPodCliqueSetControllerOwner(scaled, pcs)

	cl := testutils.NewTestClientBuilder().Build()
	backend := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}).(*schedulerBackend)
	require.NoError(t, backend.Init(cl))

	_, err := backend.buildAggregatePodGroup(
		pcs,
		0,
		base,
		[]groveschedulerv1alpha1.PodGang{*base, *scaled},
		"",
	)
	require.ErrorContains(t, err, "missing required label")
	assert.ErrorContains(t, err, apicommon.LabelPodCliqueScalingGroup)
}

func TestBuildAggregatePodGroupRejectsReservedScaledCollectionName(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	base := testutils.NewPodGangBuilder("job-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("base", 1).
		Build()
	setPodCliqueSetControllerOwner(base, pcs)

	scaled := testutils.NewPodGangBuilder(scaledPodGangsSubGroupName, "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("worker", 1).
		Build()
	setPodCliqueSetControllerOwner(scaled, pcs)
	scaled.Labels[apicommon.LabelPodCliqueScalingGroup] = "job-0-workers"

	cl := testutils.NewTestClientBuilder().Build()
	backend := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}).(*schedulerBackend)
	require.NoError(t, backend.Init(cl))

	_, err := backend.buildAggregatePodGroup(
		pcs,
		0,
		base,
		[]groveschedulerv1alpha1.PodGang{*base, *scaled},
		"",
	)
	require.ErrorContains(t, err, "conflicts with reserved KAI subgroup name")
}

func TestSyncAggregatePodGroupMigratesLegacyResourcesAndMapsTopology(t *testing.T) {
	ctx := context.Background()
	pcs := newPodCliqueSet("job", "team-a")

	base := testutils.NewPodGangBuilder("job-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("job-0-workers-0-trainer", 2).
		Build()
	base.UID = types.UID("base-uid")
	setPodCliqueSetControllerOwner(base, pcs)
	base.Annotations = map[string]string{"grove.io/topology-name": "grove-topology"}
	base.Spec.TopologyConstraint = topologyConstraint("zone")
	base.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{{
		Name:               "job-0-workers-0",
		PodGroupNames:      []string{"job-0-workers-0-trainer"},
		TopologyConstraint: topologyConstraint("rack"),
	}}
	base.Spec.PodGroups[0].TopologyConstraint = topologyConstraint("host")

	scaled := testutils.NewPodGangBuilder("job-0-workers-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("job-0-workers-1-trainer", 2).
		Build()
	scaled.UID = types.UID("scaled-uid")
	setPodCliqueSetControllerOwner(scaled, pcs)
	scaled.Labels[apicommon.LabelPodCliqueScalingGroup] = "job-0-workers"
	scaled.Annotations = map[string]string{"grove.io/topology-name": "grove-topology"}
	scaled.Spec.TopologyConstraint = topologyConstraint("rack")
	scaled.Spec.PodGroups[0].TopologyConstraint = topologyConstraint("host")

	clusterTopology := &grovecorev1alpha1.ClusterTopologyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "grove-topology"},
		Spec: grovecorev1alpha1.ClusterTopologyBindingSpec{
			SchedulerTopologyBindings: []grovecorev1alpha1.SchedulerTopologyBinding{{
				SchedulerName:     string(configv1alpha1.SchedulerNameKai),
				TopologyReference: "external-kai-topology",
			}},
		},
	}
	basePod := testAggregatePod("base-pod", pcs.Name, "job-0", "job-0-workers-0-trainer")
	scaledPod := testAggregatePod("scaled-pod", pcs.Name, "job-0-workers-0", "job-0-workers-1-trainer")
	legacyBase := &kaischedulingv2alpha2.PodGroup{ObjectMeta: metav1.ObjectMeta{Name: base.Name, Namespace: base.Namespace}}
	legacyScaled := &kaischedulingv2alpha2.PodGroup{ObjectMeta: metav1.ObjectMeta{Name: scaled.Name, Namespace: scaled.Namespace}}
	stableLegacyBase := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "pg-" + base.Name + "-" + string(base.UID),
			Namespace:       base.Namespace,
			OwnerReferences: []metav1.OwnerReference{podGangOwnerReference(base)},
		},
	}
	stableLegacyScaled := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "pg-" + scaled.Name + "-" + string(scaled.UID),
			Namespace:       scaled.Namespace,
			OwnerReferences: []metav1.OwnerReference{podGangOwnerReference(scaled)},
		},
	}
	basePod.Annotations[annotationPodGroup] = stableLegacyBase.Name
	scaledPod.Annotations[annotationPodGroup] = stableLegacyScaled.Name
	unrelated := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: base.Namespace},
	}

	cl := testutils.NewTestClientBuilder().Build()
	recorder := record.NewFakeRecorder(10)
	backend := New(cl, cl.Scheme(), recorder, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}).(*schedulerBackend)
	require.NoError(t, backend.Init(cl))
	require.NoError(t, controllerutil.SetControllerReference(base, legacyBase, cl.Scheme()))
	require.NoError(t, controllerutil.SetControllerReference(scaled, legacyScaled, cl.Scheme()))
	for _, object := range []client.Object{
		pcs,
		clusterTopology,
		base,
		scaled,
		basePod,
		scaledPod,
		legacyBase,
		legacyScaled,
		stableLegacyBase,
		stableLegacyScaled,
		unrelated,
	} {
		require.NoError(t, cl.Create(ctx, object))
	}

	require.NoError(t, backend.SyncPodGang(ctx, base))

	aggregate := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: "grove-job-0"}, aggregate))
	assert.True(t, metav1.IsControlledBy(aggregate, pcs))
	assert.Equal(t, "external-kai-topology", aggregate.Spec.TopologyConstraint.Topology)
	assert.Equal(t, "zone", aggregate.Spec.TopologyConstraint.RequiredTopologyLevel)

	collection := requireSubGroup(t, aggregate, scaledPodGangsSubGroupName)
	assert.Nil(t, collection.TopologyConstraint, "scaled collection is structural, not one PCSG placement unit")
	basePCSGReplica := requireSubGroup(t, aggregate, "job-0-workers-0")
	require.NotNil(t, basePCSGReplica.TopologyConstraint)
	assert.Equal(t, "external-kai-topology", basePCSGReplica.TopologyConstraint.Topology)
	assert.Equal(t, "rack", basePCSGReplica.TopologyConstraint.RequiredTopologyLevel)
	scaledBranch := requireSubGroup(t, aggregate, "job-0-workers-0-gang")
	require.NotNil(t, scaledBranch.Parent)
	assert.Equal(t, collection.Name, *scaledBranch.Parent)
	require.NotNil(t, scaledBranch.TopologyConstraint)
	assert.Equal(t, "rack", scaledBranch.TopologyConstraint.RequiredTopologyLevel)
	assert.Equal(t, "external-kai-topology", requireSubGroup(t, aggregate, "job-0-workers-1-trainer").TopologyConstraint.Topology)

	for _, pod := range []*corev1.Pod{basePod, scaledPod} {
		current := &corev1.Pod{}
		require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(pod), current))
		assert.Equal(t, aggregate.Name, current.Annotations[annotationPodGroup])
		assert.Equal(t, current.Labels[apicommon.LabelPodClique], current.Labels[labelSubGroup])
	}
	for _, name := range []string{base.Name, scaled.Name, stableLegacyBase.Name, stableLegacyScaled.Name} {
		err := cl.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: name}, &kaischedulingv2alpha2.PodGroup{})
		assert.True(t, apierrors.IsNotFound(err), "legacy PodGroup %s must be deleted", name)
	}
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(unrelated), &kaischedulingv2alpha2.PodGroup{}))

	currentScaled := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(scaled), currentScaled))
	require.Contains(t, currentScaled.Finalizers, podGangFinalizer)
	require.NoError(t, cl.Delete(ctx, currentScaled))
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(scaled), currentScaled))
	require.ErrorContains(t, backend.SyncPodGang(ctx, currentScaled), "outside aggregate")
	require.Len(t, recorder.Events, 1)
	event := <-recorder.Events
	assert.Contains(t, event, "Warning "+eventReasonSyncFailed)
	assert.Contains(t, event, "outside aggregate")
	require.NoError(t, cl.Delete(ctx, scaledPod))
	require.NoError(t, backend.SyncPodGang(ctx, currentScaled))
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: "grove-job-0"}, aggregate))
	require.NotNil(t, aggregate.Spec.MinSubGroup)
	assert.Equal(t, int32(1), *aggregate.Spec.MinSubGroup)
	assert.Nil(t, findSubGroup(aggregate, scaledPodGangsSubGroupName), "scaled collection must be removed after final scale-down")
	assert.Nil(t, findSubGroup(aggregate, scaledBranch.Name), "scaled PodGang branch must be removed after scale-down")
}

func TestSyncAggregatePodGroupDeletesOwnerControlledLegacyPodGroupOnRetry(t *testing.T) {
	ctx := context.Background()
	pcs := newPodCliqueSet("retry-job", "team-a")
	base := testutils.NewPodGangBuilder("retry-job-0", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithPodGroup("worker", 1).
		Build()
	base.UID = types.UID("base-uid")
	setPodCliqueSetControllerOwner(base, pcs)

	aggregateName := "grove-retry-job-0"
	pod := testAggregatePod("worker-pod", pcs.Name, base.Name, "worker")
	pod.Annotations[annotationPodGroup] = aggregateName
	pod.Annotations[annotationKeySkipPGR] = annotationValSkipPGR
	pod.Labels[labelSubGroup] = "worker"

	legacy := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "pg-" + base.Name + "-" + string(base.UID),
			Namespace:       base.Namespace,
			OwnerReferences: []metav1.OwnerReference{podGangOwnerReference(base)},
		},
	}
	unrelated := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "unrelated-retry-group", Namespace: base.Namespace},
	}

	cl := testutils.NewTestClientBuilder().Build()
	backend := New(
		cl,
		cl.Scheme(),
		record.NewFakeRecorder(10),
		configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai},
	).(*schedulerBackend)
	require.NoError(t, backend.Init(cl))
	for _, object := range []client.Object{pcs, base, pod, legacy, unrelated} {
		require.NoError(t, cl.Create(ctx, object))
	}

	require.NoError(t, backend.SyncPodGang(ctx, base))

	err := cl.Get(ctx, client.ObjectKeyFromObject(legacy), &kaischedulingv2alpha2.PodGroup{})
	assert.True(t, apierrors.IsNotFound(err), "owner-controlled legacy PodGroup must be deleted on retry")
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(unrelated), &kaischedulingv2alpha2.PodGroup{}))

	currentPod := &corev1.Pod{}
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(pod), currentPod))
	assert.Equal(t, aggregateName, currentPod.Annotations[annotationPodGroup])
	assert.Equal(t, "worker", currentPod.Labels[labelSubGroup])
}

func podGangOwnerReference(podGang *groveschedulerv1alpha1.PodGang) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: groveschedulerv1alpha1.SchemeGroupVersion.String(),
		Kind:       "PodGang",
		Name:       podGang.Name,
		UID:        podGang.UID,
	}
}

func topologyConstraint(required string) *groveschedulerv1alpha1.TopologyConstraint {
	return &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To(required)},
	}
}

func testAggregatePod(name, pcsName, podGangName, podCliqueName string) *corev1.Pod {
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)
	labels[apicommon.LabelPodCliqueSetReplicaIndex] = "0"
	labels[apicommon.LabelPodGang] = podGangName
	labels[apicommon.LabelPodClique] = podCliqueName
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "default",
			Labels:      labels,
			Annotations: map[string]string{annotationPodGroup: podGangName},
		},
	}
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

type blockingPodGangListClient struct {
	client.Client

	mu                sync.Mutex
	podGangListCalls  int
	firstListStarted  chan struct{}
	secondListStarted chan struct{}
	releaseFirstList  chan struct{}
}

func (c *blockingPodGangListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, isPodGangList := list.(*groveschedulerv1alpha1.PodGangList); isPodGangList {
		c.mu.Lock()
		c.podGangListCalls++
		call := c.podGangListCalls
		switch call {
		case 1:
			close(c.firstListStarted)
		case 2:
			close(c.secondListStarted)
		}
		c.mu.Unlock()
		if call == 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-c.releaseFirstList:
			}
		}
	}
	return c.Client.List(ctx, list, opts...)
}
