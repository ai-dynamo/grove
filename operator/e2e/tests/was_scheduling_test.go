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
	"os"
	"slices"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgang"
	"github.com/ai-dynamo/grove/operator/e2e/grove/topology"
	k8spods "github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func Test_WAS1_HierarchyCreatedForPodGang(t *testing.T) {
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-gang", YAMLPath: "../yaml/was-gang.yaml", Namespace: "default", ExpectedPods: 3,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	pcs := readWASFixture(tc)
	// Pod membership must not depend on clique declaration order.
	slices.Reverse(pcs.Spec.Template.Cliques)
	require.NoError(t, tc.Client.Create(tc.Ctx, pcs))
	require.NoError(t, tc.WaitForPods(3))
	pods, err := tc.ListPods()
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
	for _, pod := range pods.Items {
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
	requireWASPodsUnschedulable(tc, 3)

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

func Test_WAS4_ScalingGroupPreservesAnchor(t *testing.T) {
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-scaling", YAMLPath: "../yaml/was-scaling.yaml", Namespace: "default", ExpectedPods: 3,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	ensureGroveTopology(tc.Ctx, t, topology.NewTopologyVerifier(tc.Client, Logger))
	original, err := DeployWorkloadAndGetPods(tc, 3)
	require.NoError(t, err)
	verifier := podgang.NewVerifier(tc.Client, Logger)
	pcsKey := client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name}
	anchor := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
	anchorWorkload := &schedulingv1beta1.Workload{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&anchor), anchorWorkload))

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

	gangs := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 2, tc.Timeout, tc.Interval)
	var scaledGang groveschedulerv1alpha1.PodGang
	for _, gang := range gangs {
		nativeWorkload := &schedulingv1beta1.Workload{}
		require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&gang), nativeWorkload))
		require.True(t, metav1.IsControlledBy(nativeWorkload, &gang))
		if gang.Name == anchor.Name {
			require.Equal(t, anchor.UID, gang.UID, "scale-out must preserve the anchor PodGang")
			require.Equal(t, anchorWorkload.UID, nativeWorkload.UID, "scale-out must preserve the anchor Workload")
		} else {
			require.NotEqual(t, anchorWorkload.UID, nativeWorkload.UID)
			scaledGang = gang
		}
	}
	require.NotEmpty(t, scaledGang.Name)

	require.NoError(t, tc.ScalePCSG("was-scaling-0-workers", 1))
	require.NoError(t, tc.WaitForPods(3))
	survivor := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
	require.Equal(t, anchor.UID, survivor.UID, "scale-in must preserve the anchor PodGang")
	remaining, err := tc.ListPods()
	require.NoError(t, err)
	remainingUIDs := make(map[string]types.UID, len(remaining.Items))
	for _, pod := range remaining.Items {
		remainingUIDs[pod.Name] = pod.UID
	}
	for _, pod := range original {
		require.Equal(t, pod.UID, remainingUIDs[pod.Name], "scale-in must preserve every original Pod")
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		workloads := &schedulingv1beta1.WorkloadList{}
		leaves := &schedulingv1beta1.PodGroupList{}
		composites := &schedulingv1alpha3.CompositePodGroupList{}
		options := []client.ListOption{client.InNamespace(tc.Namespace), client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}}
		require.NoError(c, tc.Client.List(tc.Ctx, workloads, options...))
		require.Len(c, workloads.Items, 1, "remaining Workloads: %+v", workloads.Items)
		assert.Equal(c, anchorWorkload.UID, workloads.Items[0].UID)
		require.NoError(c, tc.Client.List(tc.Ctx, leaves, options...))
		assert.Len(c, leaves.Items, 2, "remaining PodGroups: %+v", leaves.Items)
		require.NoError(c, tc.Client.List(tc.Ctx, composites, options...))
		assert.Len(c, composites.Items, 2, "remaining CompositePodGroups: %+v", composites.Items)
	}, tc.Timeout, tc.Interval, "scale-in must garbage-collect the removed gang hierarchy")
	for _, object := range []client.Object{&schedulingv1beta1.Workload{}, &schedulingv1alpha3.CompositePodGroup{}} {
		require.True(t, apierrors.IsNotFound(tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&scaledGang), object)),
			"the removed gang's %T must be garbage-collected", object)
	}
}

func Test_WAS5_UpdateStrategies(t *testing.T) {
	for _, strategy := range []grovecorev1alpha1.UpdateStrategyType{
		grovecorev1alpha1.CoherentStrategy,
		grovecorev1alpha1.RollingRecreateStrategy,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
				testctx.WithWorkload(&testctx.WorkloadConfig{
					Name: "was-update", YAMLPath: "../yaml/was-update.yaml", Namespace: "default", ExpectedPods: 5,
				}))
			t.Cleanup(cleanup)
			setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

			pcs := readWASFixture(tc)
			pcs.Spec.UpdateStrategy.Type = strategy
			require.NoError(t, tc.Client.Create(tc.Ctx, pcs))
			require.NoError(t, tc.WaitForPods(5))
			before, err := tc.ListPods()
			require.NoError(t, err)
			oldUIDs := capturePodUIDs(before)

			verifier := podgang.NewVerifier(tc.Client, Logger)
			pcsKey := client.ObjectKeyFromObject(pcs)
			oldGang := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
			oldWorkload := &schedulingv1beta1.Workload{}
			oldRoot := &schedulingv1alpha3.CompositePodGroup{}
			gangKey := client.ObjectKeyFromObject(&oldGang)
			require.NoError(t, tc.Client.Get(tc.Ctx, gangKey, oldWorkload))
			require.NoError(t, tc.Client.Get(tc.Ctx, gangKey, oldRoot))
			oldLeaves := &schedulingv1beta1.PodGroupList{}
			require.NoError(t, tc.Client.List(tc.Ctx, oldLeaves, client.InNamespace(tc.Namespace),
				client.MatchingLabels{apicommon.LabelPodGang: oldGang.Name}))
			require.Len(t, oldLeaves.Items, 2)
			oldHierarchy := []client.Object{oldWorkload, oldRoot}
			for i := range oldLeaves.Items {
				oldHierarchy = append(oldHierarchy, &oldLeaves.Items[i])
			}

			require.NoError(t, tc.Client.Get(tc.Ctx, pcsKey, pcs))
			patch := client.MergeFrom(pcs.DeepCopy())
			for _, clique := range pcs.Spec.Template.Cliques {
				clique.Spec.PodSpec.Containers[0].Env = []corev1.EnvVar{{Name: "WAS_REVISION", Value: "updated"}}
			}
			require.NoError(t, tc.Client.Patch(tc.Ctx, pcs, patch))
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				pods, err := tc.ListPods()
				require.NoError(c, err)
				assert.True(c, k8spods.AllReady(5)(pods), "all replacement Pods must become Ready")
				for _, pod := range pods.Items {
					assert.NotContains(c, oldUIDs, pod.UID, "Pod %s must be replaced", pod.Name)
					assert.Contains(c, pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "WAS_REVISION", Value: "updated"})
				}
				current := &grovecorev1alpha1.PodCliqueSet{}
				require.NoError(c, tc.Client.Get(tc.Ctx, pcsKey, current))
				require.NotNil(c, current.Status.ObservedGeneration)
				assert.Equal(c, current.Generation, *current.Status.ObservedGeneration)
				assert.Equal(c, current.Spec.Replicas, current.Status.UpdatedReplicas)
				require.NotNil(c, current.Status.UpdateProgress)
				assert.NotNil(c, current.Status.UpdateProgress.UpdateEndedAt)
			}, tc.Timeout, tc.Interval, "the update must complete with all Pods replaced")

			newGang := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
			if strategy == grovecorev1alpha1.RollingRecreateStrategy {
				require.Equal(t, oldGang.UID, newGang.UID, "RollingRecreate must preserve the anchor")
				for _, object := range oldHierarchy {
					current := object.DeepCopyObject().(client.Object)
					require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(object), current))
					require.Equal(t, object.GetUID(), current.GetUID(), "%T must survive RollingRecreate", object)
				}
			} else {
				require.NotEqual(t, oldGang.Name, newGang.Name, "Coherent must create a new anchor epoch")
				require.EventuallyWithT(t, func(c *assert.CollectT) {
					for _, object := range oldHierarchy {
						current := object.DeepCopyObject().(client.Object)
						err := tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(object), current)
						assert.True(c, apierrors.IsNotFound(err), "old %T %s must be deleted: %v", object, object.GetName(), err)
					}
				}, tc.Timeout, tc.Interval)
			}

			currentWorkload := &schedulingv1beta1.Workload{}
			require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&newGang), currentWorkload))
			require.True(t, metav1.IsControlledBy(currentWorkload, &newGang))
			updatedPods, err := tc.ListPods()
			require.NoError(t, err)
			for _, pod := range updatedPods.Items {
				require.Equal(t, newGang.Name, pod.Labels[apicommon.LabelPodGang])
				require.NotNil(t, pod.Spec.SchedulingGroup)
				require.NotNil(t, pod.Spec.SchedulingGroup.PodGroupName)
				leaf := &schedulingv1beta1.PodGroup{}
				require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{
					Namespace: tc.Namespace, Name: *pod.Spec.SchedulingGroup.PodGroupName,
				}, leaf))
				require.True(t, metav1.IsControlledBy(leaf, &newGang))
				require.Equal(t, pod.Labels[apicommon.LabelPodClique], leaf.Labels[apicommon.LabelPodClique])
				require.NotNil(t, leaf.Spec.WorkloadRef)
				require.Equal(t, currentWorkload.Name, leaf.Spec.WorkloadRef.WorkloadName)
			}
		})
	}
}

func Test_WAS6_PartialGangAdmissionMinCount(t *testing.T) {
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-partial", YAMLPath: "../yaml/was-partial.yaml", Namespace: "default", ExpectedPods: 3,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	nodesToCordon := tc.SetupAndCordonNodes(1)
	before, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err)
	require.NoError(t, tc.WaitForPodCountAndPhases(3, 2, 1))
	require.NoError(t, tc.WaitForReadyPods(2))

	verifier := podgang.NewVerifier(tc.Client, Logger)
	pcsKey := client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name}
	gang := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
	leaves := &schedulingv1beta1.PodGroupList{}
	require.NoError(t, tc.Client.List(tc.Ctx, leaves, client.InNamespace(tc.Namespace),
		client.MatchingLabels{apicommon.LabelPodGang: gang.Name}))
	require.Len(t, leaves.Items, 1)
	leaf := &leaves.Items[0]
	require.NotNil(t, leaf.Spec.SchedulingPolicy.Gang)
	require.EqualValues(t, 2, leaf.Spec.SchedulingPolicy.Gang.MinCount)
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionTrue),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionTrue),
	), "the gang must become Ready at minAvailable without waiting for its extra replica")

	tc.UncordonNodesAndWaitForPods(nodesToCordon, 3)
	tc.ListPodsAndAssertDistinctNodes()
	after, err := tc.ListPods()
	require.NoError(t, err)
	oldUIDs := make(map[string]types.UID, len(before.Items))
	for _, pod := range before.Items {
		oldUIDs[pod.Name] = pod.UID
	}
	for _, pod := range after.Items {
		require.Equal(t, oldUIDs[pod.Name], pod.UID, "extra capacity must not recreate Pods")
		require.NotNil(t, pod.Spec.SchedulingGroup)
		require.Equal(t, &leaf.Name, pod.Spec.SchedulingGroup.PodGroupName)
	}
	currentLeaf := &schedulingv1beta1.PodGroup{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(leaf), currentLeaf))
	require.Equal(t, leaf.UID, currentLeaf.UID)
}

func Test_WAS7_StandaloneCliqueScalingPreservesHierarchy(t *testing.T) {
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-partial", YAMLPath: "../yaml/was-partial.yaml", Namespace: "default", ExpectedPods: 3,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	original, err := DeployWorkloadAndGetPods(tc, 3)
	require.NoError(t, err)
	verifier := podgang.NewVerifier(tc.Client, Logger)
	pcsKey := client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name}
	gang := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
	workload := &schedulingv1beta1.Workload{}
	root := &schedulingv1alpha3.CompositePodGroup{}
	leaf := &schedulingv1beta1.PodGroup{}
	require.NotNil(t, original[0].Spec.SchedulingGroup)
	require.NotNil(t, original[0].Spec.SchedulingGroup.PodGroupName)
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&gang), workload))
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&gang), root))
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{
		Namespace: tc.Namespace, Name: *original[0].Spec.SchedulingGroup.PodGroupName,
	}, leaf))
	previousUIDs := make(map[string]types.UID, len(original))
	for _, pod := range original {
		previousUIDs[pod.Name] = pod.UID
	}

	for _, step := range []struct {
		replicas int
		ready    int
		pending  int
	}{
		{replicas: 4, ready: 3, pending: 1},
		{replicas: 2, ready: 2, pending: 0},
	} {
		require.NoError(t, tc.ScalePodClique(tc.Workload.Name+"-0-worker", step.replicas))
		require.NoError(t, tc.WaitForPodCountAndPhases(step.replicas, step.ready, step.pending))
		require.NoError(t, tc.WaitForReadyPods(step.ready))
		currentGang := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
		require.Equal(t, gang.UID, currentGang.UID)
		for _, object := range []client.Object{workload, root, leaf} {
			current := object.DeepCopyObject().(client.Object)
			require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(object), current))
			require.Equal(t, object.GetUID(), current.GetUID(), "scaling must preserve %T", object)
		}
		currentLeaf := &schedulingv1beta1.PodGroup{}
		require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(leaf), currentLeaf))
		require.NotNil(t, currentLeaf.Spec.SchedulingPolicy.Gang)
		require.EqualValues(t, 2, currentLeaf.Spec.SchedulingPolicy.Gang.MinCount)

		pods, err := tc.ListPods()
		require.NoError(t, err)
		currentUIDs := make(map[string]types.UID, len(pods.Items))
		for _, pod := range pods.Items {
			currentUIDs[pod.Name] = pod.UID
			require.Equal(t, gang.Name, pod.Labels[apicommon.LabelPodGang])
			require.NotNil(t, pod.Spec.SchedulingGroup)
			require.Equal(t, &leaf.Name, pod.Spec.SchedulingGroup.PodGroupName)
		}
		if step.replicas > len(previousUIDs) {
			for name, uid := range previousUIDs {
				require.Equal(t, uid, currentUIDs[name], "scale-out must preserve Pod %s", name)
			}
		} else {
			for name, uid := range currentUIDs {
				require.Equal(t, previousUIDs[name], uid, "scale-in must not replace surviving Pod %s", name)
			}
		}
		previousUIDs = currentUIDs
		require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
			podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionTrue),
			podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionTrue),
		))
	}
}

func Test_WAS8_ScalingGroupTailWaitsForAnchor(t *testing.T) {
	const (
		expectedPods = 7
		holdWindow   = 10 * time.Second
		groveGate    = "grove.io/podgang-pending-creation"
	)
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-scaling", YAMLPath: "../yaml/was-scaling.yaml", Namespace: "default", ExpectedPods: expectedPods,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	cordoned := tc.SetupAndCordonNodes(1)
	node := &corev1.Node{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{Name: cordoned[0]}, node))
	require.NotEmpty(t, node.Labels[corev1.LabelHostname])
	pcs := readWASFixture(tc)
	pcs.Spec.Template.TopologyConstraint = nil
	group := &pcs.Spec.Template.PodCliqueScalingGroupConfigs[0]
	*group.Replicas = 3
	group.TopologyConstraint = nil
	// Only the anchor's router needs the cordoned node; both tails could otherwise run.
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique.Name == "router" {
			clique.Spec.PodSpec.NodeSelector[corev1.LabelHostname] = node.Labels[corev1.LabelHostname]
		}
	}
	require.NoError(t, tc.Client.Create(tc.Ctx, pcs))
	before, err := tc.WaitForPodCount(expectedPods)
	require.NoError(t, err)

	verifier := podgang.NewVerifier(tc.Client, Logger)
	pcsKey := client.ObjectKeyFromObject(pcs)
	gangs := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 3, tc.Timeout, tc.Interval)
	tails := nonAnchorPodGangs(gangs)
	require.Len(t, tails, 2)
	anchorIndex := slices.IndexFunc(gangs, func(gang groveschedulerv1alpha1.PodGang) bool {
		return gang.Labels[apicommon.LabelPodGangRole] == string(grovecorev1alpha1.PodGangEntryRoleAnchor)
	})
	require.NotEqual(t, -1, anchorIndex)
	anchor := &gangs[anchorIndex]
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := tc.ListPods()
		require.NoError(c, err)
		require.Len(c, pods.Items, expectedPods)
		anchorPods := 0
		for _, pod := range pods.Items {
			assert.Empty(c, pod.Spec.NodeName)
			assert.Equal(c, corev1.PodPending, pod.Status.Phase)
			if pod.Labels[apicommon.LabelPodGang] == anchor.Name {
				anchorPods++
				assert.False(c, hasSchedulingGate(&pod, groveGate))
			} else {
				assert.True(c, slices.ContainsFunc(tails, func(tail groveschedulerv1alpha1.PodGang) bool {
					return tail.Name == pod.Labels[apicommon.LabelPodGang]
				}))
				assert.True(c, hasSchedulingGate(&pod, groveGate))
			}
		}
		assert.Equal(c, 3, anchorPods)
	}, tc.Timeout, tc.Interval)
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionFalse),
		podgang.LastScheduledSetCheckFn(false),
	))
	requireWASPodsRemain(tc, holdWindow, func(pods *corev1.PodList) {
		require.Len(t, pods.Items, expectedPods)
		for _, pod := range pods.Items {
			require.Empty(t, pod.Spec.NodeName, "Pod %s must remain unbound", pod.Name)
			if pod.Labels[apicommon.LabelPodGang] != anchor.Name {
				require.True(t, hasSchedulingGate(&pod, groveGate), "tail Pod %s must stay gated", pod.Name)
			}
		}
	})

	tc.UncordonNodesAndWaitForPods(cordoned, expectedPods)
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionTrue),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionTrue),
	))
	after, err := tc.ListPods()
	require.NoError(t, err)
	originalUIDs := make(map[string]types.UID, len(before.Items))
	for _, pod := range before.Items {
		originalUIDs[pod.Name] = pod.UID
	}
	for _, pod := range after.Items {
		require.Equal(t, originalUIDs[pod.Name], pod.UID, "releasing the anchor must not recreate Pod %s", pod.Name)
		require.False(t, hasSchedulingGate(&pod, groveGate))
		require.NotNil(t, pod.Spec.SchedulingGroup)
		require.NotNil(t, pod.Spec.SchedulingGroup.PodGroupName)
		leaf := &schedulingv1beta1.PodGroup{}
		require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{
			Namespace: tc.Namespace, Name: *pod.Spec.SchedulingGroup.PodGroupName,
		}, leaf))
		gangIndex := slices.IndexFunc(gangs, func(gang groveschedulerv1alpha1.PodGang) bool {
			return gang.Name == pod.Labels[apicommon.LabelPodGang]
		})
		require.NotEqual(t, -1, gangIndex)
		require.True(t, metav1.IsControlledBy(leaf, &gangs[gangIndex]))
		require.NotNil(t, leaf.Spec.WorkloadRef)
		require.Equal(t, gangs[gangIndex].Name, leaf.Spec.WorkloadRef.WorkloadName)
	}
}

func Test_WAS9_GangAdmitsWhenCapacityFrees(t *testing.T) {
	const (
		blockerName = "was-capacity-blocker"
		holdWindow  = 10 * time.Second
	)
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-gang", YAMLPath: "../yaml/was-gang.yaml", Namespace: "default", ExpectedPods: 3,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	pcs := readWASFixture(tc)
	// The blocker occupies one of the three slots required by the gang's anti-affinity.
	for _, clique := range pcs.Spec.Template.Cliques {
		for i := range clique.Spec.PodSpec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			term := &clique.Spec.PodSpec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[i]
			term.LabelSelector = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: apicommon.LabelPartOfKey, Operator: metav1.LabelSelectorOpIn,
				Values: []string{pcs.Name, blockerName},
			}}}
		}
	}
	blocker := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: blockerName, Namespace: tc.Namespace,
			Labels: map[string]string{apicommon.LabelPartOfKey: blockerName},
		},
		Spec: *pcs.Spec.Template.Cliques[0].Spec.PodSpec.DeepCopy(),
	}
	require.NoError(t, tc.Client.Create(tc.Ctx, blocker))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tc.Ctx), tc.Timeout)
		defer cancel()
		require.NoError(t, client.IgnoreNotFound(tc.Client.Delete(ctx, blocker)))
	})
	require.NoError(t, k8spods.NewPodManager(tc.Client, Logger).WaitForReady(
		tc.Ctx, []string{tc.Namespace}, apicommon.LabelPartOfKey+"="+blockerName, 1, tc.Timeout, tc.Interval))

	require.NoError(t, tc.Client.Create(tc.Ctx, pcs))
	before, err := tc.WaitForPodCount(3)
	require.NoError(t, err)
	requireWASPodsUnschedulable(tc, 3)
	verifier := podgang.NewVerifier(tc.Client, Logger)
	pcsKey := client.ObjectKeyFromObject(pcs)
	gang := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
	workload := &schedulingv1beta1.Workload{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&gang), workload))
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionFalse),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionFalse),
		podgang.LastScheduledSetCheckFn(false),
	))
	requireWASPodsRemain(tc, holdWindow, func(pods *corev1.PodList) {
		require.Len(t, pods.Items, 3)
		for _, pod := range pods.Items {
			require.Empty(t, pod.Spec.NodeName, "Pod %s must remain unbound while capacity is blocked", pod.Name)
			require.Equal(t, corev1.PodPending, pod.Status.Phase)
		}
	})

	require.NoError(t, tc.Client.Delete(tc.Ctx, blocker))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(blocker), &corev1.Pod{}))
	}, tc.Timeout, tc.Interval, "the blocker must be deleted before its capacity is reused")
	require.NoError(t, tc.WaitForPods(3))
	tc.ListPodsAndAssertDistinctNodes()
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionTrue),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionTrue),
		podgang.LastScheduledSetCheckFn(true),
	))
	currentGang := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
	require.Equal(t, gang.UID, currentGang.UID)
	currentWorkload := &schedulingv1beta1.Workload{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(workload), currentWorkload))
	require.Equal(t, workload.UID, currentWorkload.UID)
	after, err := tc.ListPods()
	require.NoError(t, err)
	require.Len(t, after.Items, len(before.Items))
	for _, original := range before.Items {
		index := slices.IndexFunc(after.Items, func(pod corev1.Pod) bool { return pod.Name == original.Name })
		require.NotEqual(t, -1, index, "queued Pod %s must survive admission", original.Name)
		require.Equal(t, original.UID, after.Items[index].UID)
		require.NotNil(t, original.Spec.SchedulingGroup)
		require.Equal(t, original.Spec.SchedulingGroup, after.Items[index].Spec.SchedulingGroup)
	}
}

func Test_WAS10_EachRoleMustMeetItsMinimum(t *testing.T) {
	const holdWindow = 10 * time.Second
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-gang", YAMLPath: "../yaml/was-gang.yaml", Namespace: "default", ExpectedPods: 3,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	cordoned := tc.SetupAndCordonNodes(1)
	node := &corev1.Node{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{Name: cordoned[0]}, node))
	require.NotEmpty(t, node.Labels[corev1.LabelHostname])
	pcs := readWASFixture(tc)
	// Two pc-a Pods fit, satisfying the aggregate minimum, but neither can replace pc-b.
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique.Name == "pc-a" {
			*clique.Spec.MinAvailable = 1
			terms := clique.Spec.PodSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			for i := range terms {
				terms[i].MatchExpressions = append(terms[i].MatchExpressions, corev1.NodeSelectorRequirement{
					Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpNotIn,
					Values: []string{node.Labels[corev1.LabelHostname]},
				})
			}
		} else {
			clique.Spec.PodSpec.NodeSelector = map[string]string{corev1.LabelHostname: node.Labels[corev1.LabelHostname]}
		}
	}
	require.NoError(t, tc.Client.Create(tc.Ctx, pcs))
	before, err := tc.WaitForPodCount(3)
	require.NoError(t, err)
	requireWASPodsUnschedulable(tc, 3)

	verifier := podgang.NewVerifier(tc.Client, Logger)
	pcsKey := client.ObjectKeyFromObject(pcs)
	gang := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
	root := &schedulingv1alpha3.CompositePodGroup{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&gang), root))
	require.NotNil(t, root.Spec.SchedulingPolicy.Gang)
	require.EqualValues(t, 2, root.Spec.SchedulingPolicy.Gang.MinGroupCount)
	leaves := &schedulingv1beta1.PodGroupList{}
	require.NoError(t, tc.Client.List(tc.Ctx, leaves, client.InNamespace(tc.Namespace),
		client.MatchingLabels{apicommon.LabelPodGang: gang.Name}))
	require.Len(t, leaves.Items, 2)
	for _, leaf := range leaves.Items {
		require.NotNil(t, leaf.Spec.SchedulingPolicy.Gang)
		require.EqualValues(t, 1, leaf.Spec.SchedulingPolicy.Gang.MinCount)
	}
	requireWASPodsRemain(tc, holdWindow, func(pods *corev1.PodList) {
		require.Len(t, pods.Items, 3)
		for _, pod := range pods.Items {
			require.Empty(t, pod.Spec.NodeName, "Pod %s must remain unbound while pc-b cannot be placed", pod.Name)
			require.Equal(t, corev1.PodPending, pod.Status.Phase)
		}
	})
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionFalse),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionFalse),
		podgang.LastScheduledSetCheckFn(false),
	))

	tc.UncordonNodesAndWaitForPods(cordoned, 3)
	tc.ListPodsAndAssertDistinctNodes()
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionTrue),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionTrue),
	))
	after, err := tc.ListPods()
	require.NoError(t, err)
	require.Equal(t, capturePodUIDs(before), capturePodUIDs(after), "satisfying the missing role must admit the original Pods")
}

func Test_WAS11_SurplusPodLossPreservesGang(t *testing.T) {
	const (
		terminationDelay = 5 * time.Second
		holdWindow       = terminationDelay + 15*time.Second
	)
	tc, cleanup := testctx.PrepareTest(t.Context(), t, 3,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "was-partial", YAMLPath: "../yaml/was-partial.yaml", Namespace: "default", ExpectedPods: 3,
		}))
	t.Cleanup(cleanup)
	setup.RequireWASAPIs(tc.Ctx, t, tc.Client, tc.Namespace)

	pcs := readWASFixture(tc)
	pcs.Spec.Template.TerminationDelay = &metav1.Duration{Duration: terminationDelay}
	require.NoError(t, tc.Client.Create(tc.Ctx, pcs))
	require.NoError(t, tc.WaitForPods(3))
	original, err := tc.ListPods()
	require.NoError(t, err)
	originalUIDs := capturePodUIDs(original)

	verifier := podgang.NewVerifier(tc.Client, Logger)
	pcsKey := client.ObjectKeyFromObject(pcs)
	gang := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionTrue),
		podgang.LastReadySetCheckFn(true),
	))
	workload := &schedulingv1beta1.Workload{}
	root := &schedulingv1alpha3.CompositePodGroup{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&gang), workload))
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&gang), root))
	leaves := &schedulingv1beta1.PodGroupList{}
	require.NoError(t, tc.Client.List(tc.Ctx, leaves, client.InNamespace(tc.Namespace),
		client.MatchingLabels{apicommon.LabelPodGang: gang.Name}))
	require.Len(t, leaves.Items, 1)
	leaf := &leaves.Items[0]
	require.NotNil(t, leaf.Spec.SchedulingPolicy.Gang)
	require.EqualValues(t, 2, leaf.Spec.SchedulingPolicy.Gang.MinCount)

	survivors, cordoned := cordonAndKillPodsFromClique(tc.Ctx, t, tc, tc.Workload.Name+"-0-worker", 1)
	require.Len(t, survivors, 2)
	require.NoError(t, tc.WaitForPodCountAndPhases(3, 2, 1))
	require.NoError(t, tc.WaitForReadyPods(2))
	requireWASPodsRemain(tc, holdWindow, func(pods *corev1.PodList) {
		require.Len(t, pods.Items, 3)
		readySurvivors := 0
		for _, pod := range pods.Items {
			if _, survived := survivors[pod.UID]; survived && isPodReady(&pod) && pod.DeletionTimestamp == nil {
				readySurvivors++
			}
		}
		require.Equal(t, len(survivors), readySurvivors,
			"meeting minAvailable must preserve healthy Pods beyond terminationDelay")
	})
	require.NoError(t, podgang.WaitUntilVerified(tc.Ctx, verifier, pcsKey, tc.Timeout, tc.Interval,
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionTrue),
		podgang.ConditionStatusCheckFn(groveschedulerv1alpha1.PodGangConditionTypeReady, metav1.ConditionTrue),
	))
	currentGang := waitForPodGangCount(tc.Ctx, t, verifier, pcsKey, 1, tc.Timeout, tc.Interval)[0]
	require.Equal(t, gang.UID, currentGang.UID)
	for _, object := range []client.Object{workload, root, leaf} {
		current := object.DeepCopyObject().(client.Object)
		require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(object), current))
		require.Equal(t, object.GetUID(), current.GetUID(), "surplus Pod loss must preserve %T", object)
	}
	held, err := tc.ListPods()
	require.NoError(t, err)
	replacements := 0
	for _, pod := range held.Items {
		require.NotNil(t, pod.Spec.SchedulingGroup)
		require.Equal(t, &leaf.Name, pod.Spec.SchedulingGroup.PodGroupName)
		if _, survived := survivors[pod.UID]; !survived {
			replacements++
			require.NotContains(t, originalUIDs, pod.UID, "the deleted Pod must have a replacement")
			require.Equal(t, corev1.PodPending, pod.Status.Phase)
		}
	}
	require.Equal(t, 1, replacements)

	tc.UncordonNodesAndWaitForPods(cordoned, 3)
	tc.ListPodsAndAssertDistinctNodes()
	recovered, err := tc.ListPods()
	require.NoError(t, err)
	require.Equal(t, capturePodUIDs(held), capturePodUIDs(recovered),
		"restored capacity must admit the replacement without recreating any Pod")
}

func readWASFixture(tc *testctx.TestContext) *grovecorev1alpha1.PodCliqueSet {
	tc.T.Helper()
	data, err := os.ReadFile(tc.Workload.YAMLPath)
	require.NoError(tc.T, err)
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(tc.T, yaml.UnmarshalStrict(data, pcs))
	pcs.Namespace = tc.Namespace
	return pcs
}

func requireWASPodsUnschedulable(tc *testctx.TestContext, count int) {
	tc.T.Helper()
	require.NoError(tc.T, waiter.New[*corev1.PodList]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitUntil(tc.Ctx, func(ctx context.Context) (*corev1.PodList, error) {
			return k8spods.NewPodManager(tc.Client, Logger).List(ctx, tc.Namespace, tc.GetLabelSelector())
		}, func(pods *corev1.PodList) bool {
			if len(pods.Items) != count || !k8spods.AllPending()(pods) {
				return false
			}
			for _, pod := range pods.Items {
				if pod.Spec.NodeName != "" || len(pod.Spec.SchedulingGates) != 0 ||
					!slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
						return condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse &&
							condition.Reason == corev1.PodReasonUnschedulable
					}) {
					return false
				}
			}
			return true
		}), "every WAS Pod must have been rejected by the scheduler")
}

func requireWASPodsRemain(tc *testctx.TestContext, duration time.Duration, check func(*corev1.PodList)) {
	tc.T.Helper()
	const requestTimeout = 5 * time.Second
	var end time.Time
	// Poll synchronously so a blocked API request cannot make the observation pass.
	require.NoError(tc.T, wait.PollUntilContextCancel(tc.Ctx, tc.Interval, true, func(ctx context.Context) (bool, error) {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		pods, err := k8spods.NewPodManager(tc.Client, Logger).List(ctx, tc.Namespace, tc.GetLabelSelector())
		if err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		check(pods)
		if end.IsZero() {
			end = time.Now().Add(duration)
		}
		return !time.Now().Before(end), nil
	}))
}
