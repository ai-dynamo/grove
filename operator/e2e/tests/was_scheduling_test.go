//go:build e2e

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

package tests

import (
	"context"
	"slices"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgang"
	"github.com/ai-dynamo/grove/operator/e2e/grove/topology"
	k8spods "github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Test_WAS1_HierarchyCreatedForPodGang(t *testing.T) {
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-gang", YAMLPath: "../yaml/was-gang.yaml", Namespace: "default", ExpectedPods: 3,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	pods, err := DeployWorkloadAndGetPods(tc, 3)
	require.NoError(t, err)

	gangs, err := podgang.NewVerifier(tc.Client, Logger).List(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name})
	require.NoError(t, err)
	require.Len(t, gangs, 1)
	gang := &gangs[0]
	options := []client.ListOption{client.InNamespace(tc.Namespace), client.MatchingLabels{apicommon.LabelPodGang: gang.Name}}
	workloads := &schedulingv1beta1.WorkloadList{}
	require.NoError(t, tc.Client.List(tc.Ctx, workloads, options...))
	require.Len(t, workloads.Items, 1)
	require.Equal(t, gang.Name, workloads.Items[0].Name)
	require.True(t, metav1.IsControlledBy(&workloads.Items[0], gang))

	// Mapping unit tests cover the template tree. Here, verify the actual
	// runtime hierarchy and the membership used by the running Pods.
	roots := &schedulingv1alpha3.CompositePodGroupList{}
	require.NoError(t, tc.Client.List(tc.Ctx, roots, options...))
	require.Len(t, roots.Items, 1)
	root := &roots.Items[0]
	require.Equal(t, gang.Name, root.Name)
	require.True(t, metav1.IsControlledBy(root, gang))
	require.Nil(t, root.Spec.ParentCompositePodGroupName)
	require.NotNil(t, root.Spec.SchedulingPolicy.Gang)
	require.EqualValues(t, 2, root.Spec.SchedulingPolicy.Gang.MinGroupCount)

	expectedMinimums := map[string]int32{tc.Workload.Name + "-0-pc-a": 2, tc.Workload.Name + "-0-pc-b": 1}
	leaves := &schedulingv1beta1.PodGroupList{}
	require.NoError(t, tc.Client.List(tc.Ctx, leaves, options...))
	require.Len(t, leaves.Items, len(expectedMinimums))
	leafCliques := make(map[string]string, len(leaves.Items))
	for i := range leaves.Items {
		leaf := &leaves.Items[i]
		clique := leaf.Labels[apicommon.LabelPodClique]
		require.Contains(t, expectedMinimums, clique)
		require.True(t, metav1.IsControlledBy(leaf, gang))
		require.NotNil(t, leaf.Spec.SchedulingPolicy.Gang)
		require.Equal(t, expectedMinimums[clique], leaf.Spec.SchedulingPolicy.Gang.MinCount)
		require.NotNil(t, leaf.Spec.WorkloadRef)
		require.Equal(t, gang.Name, leaf.Spec.WorkloadRef.WorkloadName)
		require.NotNil(t, leaf.Spec.ParentCompositePodGroupName)
		require.Equal(t, root.Name, *leaf.Spec.ParentCompositePodGroupName)
		leafCliques[leaf.Name] = clique
	}
	for _, pod := range pods {
		require.NotNil(t, pod.Spec.SchedulingGroup)
		require.NotNil(t, pod.Spec.SchedulingGroup.PodGroupName)
		leaf := *pod.Spec.SchedulingGroup.PodGroupName
		require.Contains(t, leafCliques, leaf)
		require.Equal(t, pod.Labels[apicommon.LabelPodClique], leafCliques[leaf])
	}
}

func Test_WAS2_GangHeldWhenInsufficientResources(t *testing.T) {
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-gang", YAMLPath: "../yaml/was-gang.yaml", Namespace: "default", ExpectedPods: 3,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	nodesToCordon := tc.SetupAndCordonNodes(1)
	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err)
	require.NoError(t, tc.VerifyPodsArePendingWithUnschedulableEvents(true, 3))

	verifier := podgang.NewVerifier(tc.Client, Logger)
	pcsKey := client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name}
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeInitialized, metav1.ConditionTrue),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionFalse),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionFalse),
		podgang.LastScheduledSetCheckFn(false),
		podgang.LastReadySetCheckFn(false),
	))
	tc.UncordonNodesAndWaitForPods(nodesToCordon, 3)
	tc.ListPodsAndAssertDistinctNodes()
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionTrue),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionTrue),
		podgang.LastScheduledSetCheckFn(true),
		podgang.LastReadySetCheckFn(true),
	))
}

func Test_WAS3_PreferredTopologyIgnored(t *testing.T) {
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 1,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-preferred", YAMLPath: "../yaml/was-preferred.yaml", Namespace: "default", ExpectedPods: 2,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	topologyVerifier := topology.NewTopologyVerifier(tc.Client, Logger)
	ensureGroveTopology(tc.Ctx, t, topologyVerifier)
	pods, err := DeployWorkloadAndGetPods(tc, 2)
	require.NoError(t, err)
	require.NoError(t, topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, pods, setup.TopologyLabelRack))

	gangs, err := podgang.NewVerifier(tc.Client, Logger).List(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name})
	require.NoError(t, err)
	require.Len(t, gangs, 1)
	gang := &gangs[0]
	root := &schedulingv1alpha3.CompositePodGroup{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(gang), root))
	require.Equal(t, &schedulingv1alpha3.CompositePodGroupSchedulingConstraints{
		Topology: []schedulingv1alpha3.TopologyConstraint{{Key: setup.TopologyLabelRack}},
	}, root.Spec.SchedulingConstraints)
	require.NoError(t, waiter.New[*corev1.EventList]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval).
		WaitUntil(tc.Ctx, func(ctx context.Context) (*corev1.EventList, error) {
			events := &corev1.EventList{}
			err := tc.Client.List(ctx, events, client.InNamespace(tc.Namespace),
				client.MatchingFields{"involvedObject.uid": string(gang.UID)})
			return events, err
		}, func(events *corev1.EventList) bool {
			return slices.ContainsFunc(events.Items, func(event corev1.Event) bool {
				return event.Type == corev1.EventTypeWarning && event.Reason == "KubeBackendPreferredTopologyIgnored"
			})
		}), "expected a preferred-topology warning on PodGang %s", gang.Name)
}

func Test_WAS4_Scaling(t *testing.T) {
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-scaling", YAMLPath: "../yaml/was-scaling.yaml", Namespace: "default", ExpectedPods: 3,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	ensureGroveTopology(tc.Ctx, t, topology.NewTopologyVerifier(tc.Client, Logger))
	original, err := DeployWorkloadAndGetPods(tc, 3)
	require.NoError(t, err)
	require.NoError(t, tc.ScalePCSG("was-scaling-0-workers", 2))
	require.NoError(t, tc.WaitForPods(5))
	scaled, err := tc.ListPods()
	require.NoError(t, err)
	scaledUIDs := make(map[string]types.UID, len(scaled.Items))
	leafNodes := make(map[string]string)
	for _, pod := range scaled.Items {
		scaledUIDs[pod.Name] = pod.UID
		require.NotNil(t, pod.Spec.SchedulingGroup)
		require.NotNil(t, pod.Spec.SchedulingGroup.PodGroupName)
		leaf := *pod.Spec.SchedulingGroup.PodGroupName
		if node, exists := leafNodes[leaf]; exists {
			require.Equal(t, node, pod.Spec.NodeName, "PCSG members must share their required host domain")
		} else {
			leafNodes[leaf] = pod.Spec.NodeName
		}
	}
	for _, pod := range original {
		require.Equal(t, pod.UID, scaledUIDs[pod.Name], "scale-out must preserve every original Pod")
	}

	gangs, err := podgang.NewVerifier(tc.Client, Logger).List(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name})
	require.NoError(t, err)
	require.Len(t, gangs, 2)

	require.NoError(t, tc.ScalePCSG("was-scaling-0-workers", 1))
	require.NoError(t, tc.WaitForPods(3))
	require.Eventually(t, func() bool {
		workloads := &schedulingv1beta1.WorkloadList{}
		leaves := &schedulingv1beta1.PodGroupList{}
		composites := &schedulingv1alpha3.CompositePodGroupList{}
		options := []client.ListOption{client.InNamespace(tc.Namespace), client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}}
		return tc.Client.List(tc.Ctx, workloads, options...) == nil && len(workloads.Items) == 1 &&
			tc.Client.List(tc.Ctx, leaves, options...) == nil && len(leaves.Items) == 2 &&
			tc.Client.List(tc.Ctx, composites, options...) == nil && len(composites.Items) == 2
	}, tc.Timeout, tc.Interval, "scale-in must garbage-collect the removed gang hierarchy")
}

func Test_WAS5_CoherentUpdateAcrossAnchorEpochs(t *testing.T) {
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-update", YAMLPath: "../yaml/was-update.yaml", Namespace: "default", ExpectedPods: 5,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	before, err := DeployWorkloadAndGetPods(tc, 5)
	require.NoError(t, err)
	oldUIDs := make(map[types.UID]bool, len(before))
	for _, pod := range before {
		oldUIDs[pod.UID] = true
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name}, pcs))
	for _, clique := range pcs.Spec.Template.Cliques {
		clique.Spec.PodSpec.Containers[0].Env = []corev1.EnvVar{{Name: "WAS_REVISION", Value: "updated"}}
	}
	require.NoError(t, tc.Client.Update(tc.Ctx, pcs))
	require.Eventually(t, func() bool {
		pods, err := tc.ListPods()
		if err != nil || !k8spods.AllReady(5)(pods) {
			return false
		}
		for _, pod := range pods.Items {
			if oldUIDs[pod.UID] || pod.Spec.SchedulingGroup == nil ||
				!slices.Contains(pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "WAS_REVISION", Value: "updated"}) {
				return false
			}
		}
		return true
	}, tc.Timeout, tc.Interval, "all replicas must roll into new WAS gangs and become Ready")
}
