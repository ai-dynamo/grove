//go:build e2e && e2eupgrade

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

package upgrade

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgroup"
	"github.com/ai-dynamo/grove/operator/e2e/grove/topology"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	kaiUpgradePCSName           = "kai-backend-upgrade"
	kaiAggregateFinalizer       = "kai.scheduler/aggregate-podgroup"
	kaiPodGroupAnnotation       = "pod-group-name"
	kaiSubGroupLabel            = "kai.scheduler/subgroup-name"
	kaiSkipPodGrouperAnnotation = "kai.scheduler/skip-podgrouper"
)

var kaiUpgradeTopologyLevels = []corev1alpha1.TopologyLevel{
	{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
	{Domain: corev1alpha1.TopologyDomainBlock, Key: setup.TopologyLabelBlock},
	{Domain: corev1alpha1.TopologyDomainRack, Key: setup.TopologyLabelRack},
	{Domain: corev1alpha1.TopologyDomainHost, Key: setup.TopologyLabelHostname},
}

type kaiUpgradePodSnapshot struct {
	UID            types.UID
	NodeName       string
	LegacyPodGroup string
	LegacySubGroup string
}

type kaiUpgradeState struct {
	pods          map[string]kaiUpgradePodSnapshot
	monitorCancel context.CancelFunc
	monitorResult <-chan error
}

// Test_VUPG3_KAIBackendAggregateMigration verifies both pre-epoch and epoch-based
// per-PodGang KAI PodGroups migrate in place to PCS-owned aggregates.
func Test_VUPG3_KAIBackendAggregateMigration(t *testing.T) {
	for _, fromVersion := range []string{"v0.1.0-alpha.12-rc2", "v0.1.0-alpha.13"} {
		t.Run(fromVersion, func(t *testing.T) {
			state := &kaiUpgradeState{}
			runUpgradeTest(t, upgradeTest{
				fromVersion:     fromVersion,
				nodeWorkerCount: 8,
				prepareOpts: []testctx.TestOption{testctx.WithWorkload(&testctx.WorkloadConfig{
					Name:         kaiUpgradePCSName,
					YAMLPath:     "../../yaml/kai-backend-upgrade.yaml",
					Namespace:    "default",
					ExpectedPods: 6,
				})},
				customizeHelmValues: configureKAIUpgradeHelmValues,
				preUpgrade:          state.prepare,
				postUpgrade:         state.verify,
			})
		})
	}
}

func configureKAIUpgradeHelmValues(values map[string]any) {
	values["config"] = map[string]any{
		"leaderElection": map[string]any{"enabled": false},
		"scheduler": map[string]any{
			"defaultProfileName": "kai-scheduler",
			"profiles":           []any{map[string]any{"name": "kai-scheduler"}},
		},
		"server": map[string]any{
			"healthProbes": map[string]any{"enable": true},
		},
		"topologyAwareScheduling": map[string]any{"enabled": true},
	}
}

func (s *kaiUpgradeState) prepare(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	verifier := topology.NewTopologyVerifier(tc.Client, testctx.Logger)
	require.NoError(t, verifier.EnsureClusterTopology(tc.Ctx, "grove-topology", kaiUpgradeTopologyLevels))
	require.NoError(t, verifier.WaitForKAITopology(
		tc.Ctx,
		"grove-topology",
		[]string{setup.TopologyLabelZone, setup.TopologyLabelBlock, setup.TopologyLabelRack, setup.TopologyLabelHostname},
		tc.Timeout,
		tc.Interval,
	))

	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err, "deploy KAI upgrade workload")
	require.NoError(t, tc.WaitForPods(6), "wait for KAI upgrade workload")
	pods := waitForLegacyKAIPodGroups(t, tc)
	s.pods = snapshotKAIUpgradePods(pods)
	s.monitorCancel, s.monitorResult = monitorAggregateBeforePodMigration(tc)
}

func (s *kaiUpgradeState) verify(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	pcs := waitForKAIUpgradeMigration(t, tc, s.pods)
	s.monitorCancel()
	require.NoError(t, <-s.monitorResult, "aggregate must exist before Pod membership changes")

	revertOnePodToLegacyMembership(t, tc, s.pods)
	restartGroveOperator(t, tc)
	waitForKAIUpgradeMigration(t, tc, s.pods)

	tc.ScalePCSGAcrossAllReplicasAndWait(kaiUpgradePCSName, "workers", 2, 1, 4, 0)
	waitForScaledAggregatePodGroups(t, tc, pcs)

	manager := workload.NewWorkloadManager(tc.Client, testctx.Logger)
	require.NoError(t, manager.DeletePCSAndWait(tc.Ctx, tc.Namespace, kaiUpgradePCSName, tc.Timeout, tc.Interval))
	waitForKAIUpgradeCondition(t, tc, func(ctx context.Context) error {
		podGroups := &kaischedulingv2alpha2.PodGroupList{}
		if err := tc.Client.List(ctx, podGroups, client.InNamespace(tc.Namespace), client.MatchingLabels{
			apicommon.LabelPartOfKey: kaiUpgradePCSName,
		}); err != nil {
			return err
		}
		if len(podGroups.Items) != 0 {
			return fmt.Errorf("found %d PodGroups after PodCliqueSet deletion", len(podGroups.Items))
		}
		return nil
	})
}

func waitForLegacyKAIPodGroups(t *testing.T, tc *testctx.TestContext) []corev1.Pod {
	t.Helper()

	var pods []corev1.Pod
	waitForKAIUpgradeCondition(t, tc, func(ctx context.Context) error {
		podList, err := tc.ListPods()
		if err != nil {
			return err
		}
		if len(podList.Items) != 6 {
			return fmt.Errorf("found %d Pods, expected 6", len(podList.Items))
		}

		current := map[string]types.UID{}
		for i := range podList.Items {
			pod := &podList.Items[i]
			podGroupName := pod.Annotations[kaiPodGroupAnnotation]
			podGangName := pod.Labels[apicommon.LabelPodGang]
			if podGroupName == "" || podGroupName != podGangName {
				return fmt.Errorf("Pod %s references KAI PodGroup %q, expected its PodGang %q", pod.Name, podGroupName, podGangName)
			}
			podGroup := &kaischedulingv2alpha2.PodGroup{}
			if err = tc.Client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: podGroupName}, podGroup); err != nil {
				return err
			}
			owner := metav1.GetControllerOf(podGroup)
			if owner == nil || owner.Kind != "PodGang" || owner.Name != pod.Labels[apicommon.LabelPodGang] {
				return fmt.Errorf("legacy PodGroup %s has unexpected controller %v", podGroupName, owner)
			}
			if previous, found := current[podGroupName]; found && previous != owner.UID {
				return fmt.Errorf("legacy PodGroup %s has inconsistent PodGang identity", podGroupName)
			}
			current[podGroupName] = owner.UID
		}
		if len(current) != 4 {
			return fmt.Errorf("found %d legacy PodGroups, expected 4", len(current))
		}
		pods = append([]corev1.Pod(nil), podList.Items...)
		return nil
	})
	return pods
}

func snapshotKAIUpgradePods(pods []corev1.Pod) map[string]kaiUpgradePodSnapshot {
	snapshots := make(map[string]kaiUpgradePodSnapshot, len(pods))
	for i := range pods {
		snapshots[pods[i].Name] = kaiUpgradePodSnapshot{
			UID:            pods[i].UID,
			NodeName:       pods[i].Spec.NodeName,
			LegacyPodGroup: pods[i].Annotations[kaiPodGroupAnnotation],
			LegacySubGroup: pods[i].Labels[kaiSubGroupLabel],
		}
	}
	return snapshots
}

func waitForKAIUpgradeMigration(
	t *testing.T,
	tc *testctx.TestContext,
	snapshots map[string]kaiUpgradePodSnapshot,
) *corev1alpha1.PodCliqueSet {
	t.Helper()

	pcs := &corev1alpha1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: kaiUpgradePCSName}, pcs))
	waitForKAIUpgradeCondition(t, tc, func(ctx context.Context) error {
		podGroups := &kaischedulingv2alpha2.PodGroupList{}
		if err := tc.Client.List(ctx, podGroups, client.InNamespace(tc.Namespace), client.MatchingLabels{
			apicommon.LabelPartOfKey: kaiUpgradePCSName,
		}); err != nil {
			return err
		}
		currentAggregates := make(map[int]*kaischedulingv2alpha2.PodGroup, 2)
		for replica := range 2 {
			aggregate, err := podgroup.FilterAggregatePodGroupForPCSReplica(podGroups.Items, pcs.Name, replica)
			if err != nil {
				return err
			}
			if !metav1.IsControlledBy(aggregate, pcs) {
				return fmt.Errorf("aggregate PodGroup %s is not controlled by PodCliqueSet UID %s", aggregate.Name, pcs.UID)
			}
			currentAggregates[replica] = aggregate
		}

		podList, err := tc.ListPods()
		if err != nil {
			return err
		}
		if len(podList.Items) != len(snapshots) {
			return fmt.Errorf("found %d Pods, expected %d", len(podList.Items), len(snapshots))
		}
		for i := range podList.Items {
			pod := &podList.Items[i]
			snapshot, found := snapshots[pod.Name]
			if !found || snapshot.UID != pod.UID || snapshot.NodeName != pod.Spec.NodeName {
				return fmt.Errorf("Pod %s was recreated or moved during the upgrade", pod.Name)
			}
			if pod.Status.Phase != corev1.PodRunning {
				return fmt.Errorf("Pod %s is in phase %s, expected Running", pod.Name, pod.Status.Phase)
			}
			replica, err := strconv.Atoi(pod.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
			if err != nil {
				return fmt.Errorf("Pod %s has invalid replica index: %w", pod.Name, err)
			}
			aggregate := currentAggregates[replica]
			if pod.Annotations[kaiPodGroupAnnotation] != aggregate.Name {
				return fmt.Errorf("Pod %s references PodGroup %q, expected %q", pod.Name, pod.Annotations[kaiPodGroupAnnotation], aggregate.Name)
			}
			if pod.Annotations[kaiSkipPodGrouperAnnotation] != "true" {
				return fmt.Errorf("Pod %s does not skip the KAI podgrouper", pod.Name)
			}
			if !containsLeaf(aggregate, pod.Labels[kaiSubGroupLabel]) {
				return fmt.Errorf("Pod %s references missing aggregate leaf %q", pod.Name, pod.Labels[kaiSubGroupLabel])
			}
		}

		podGangs := &groveschedulerv1alpha1.PodGangList{}
		if err = tc.Client.List(ctx, podGangs, client.InNamespace(tc.Namespace), client.MatchingLabels{
			apicommon.LabelPartOfKey: kaiUpgradePCSName,
		}); err != nil {
			return err
		}
		for i := range podGangs.Items {
			if !slices.Contains(podGangs.Items[i].Finalizers, kaiAggregateFinalizer) {
				return fmt.Errorf("PodGang %s is missing aggregate finalizer", podGangs.Items[i].Name)
			}
		}

		return nil
	})
	assertNoActivePodReferencesLegacyOrMissingPodGroup(t, tc, snapshots)
	return pcs
}

func assertNoActivePodReferencesLegacyOrMissingPodGroup(
	t *testing.T,
	tc *testctx.TestContext,
	snapshots map[string]kaiUpgradePodSnapshot,
) {
	t.Helper()
	legacyPodGroups := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		legacyPodGroups[snapshot.LegacyPodGroup] = struct{}{}
	}

	podList, err := tc.ListPods()
	require.NoError(t, err)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		podGroupName := pod.Annotations[kaiPodGroupAnnotation]
		require.NotEmpty(t, podGroupName, "active Pod %s must reference a PodGroup", pod.Name)
		require.NotContains(t, legacyPodGroups, podGroupName, "active Pod %s still references legacy PodGroup %s", pod.Name, podGroupName)
		err = tc.Client.Get(tc.Ctx, client.ObjectKey{Namespace: pod.Namespace, Name: podGroupName}, &kaischedulingv2alpha2.PodGroup{})
		require.NoError(t, err, "active Pod %s references missing PodGroup %s", pod.Name, podGroupName)
	}
}

func monitorAggregateBeforePodMigration(tc *testctx.TestContext) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(tc.Ctx)
	result := make(chan error, 1)
	go func() {
		defer close(result)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				result <- nil
				return
			case <-ticker.C:
				podList, err := tc.ListPods()
				if err != nil {
					continue
				}
				for i := range podList.Items {
					name := podList.Items[i].Annotations[kaiPodGroupAnnotation]
					if !strings.HasPrefix(name, "grove-"+kaiUpgradePCSName+"-") {
						continue
					}
					err = tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, &kaischedulingv2alpha2.PodGroup{})
					if apierrors.IsNotFound(err) {
						result <- fmt.Errorf("Pod %s referenced aggregate PodGroup %s before it existed", podList.Items[i].Name, name)
						return
					}
				}
			}
		}
	}()
	return cancel, result
}

func revertOnePodToLegacyMembership(t *testing.T, tc *testctx.TestContext, snapshots map[string]kaiUpgradePodSnapshot) {
	t.Helper()
	podList, err := tc.ListPods()
	require.NoError(t, err)
	sort.Slice(podList.Items, func(i, j int) bool { return podList.Items[i].Name < podList.Items[j].Name })
	require.NotEmpty(t, podList.Items)
	pod := &podList.Items[0]
	snapshot := snapshots[pod.Name]
	before := pod.DeepCopy()
	pod.Annotations[kaiPodGroupAnnotation] = snapshot.LegacyPodGroup
	delete(pod.Annotations, kaiSkipPodGrouperAnnotation)
	if snapshot.LegacySubGroup == "" {
		delete(pod.Labels, kaiSubGroupLabel)
	} else {
		pod.Labels[kaiSubGroupLabel] = snapshot.LegacySubGroup
	}
	require.NoError(t, tc.Client.Patch(tc.Ctx, pod, client.MergeFrom(before)))
}

func restartGroveOperator(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	podList := &corev1.PodList{}
	require.NoError(t, tc.Client.List(tc.Ctx, podList, client.InNamespace(setup.OperatorNamespace), setup.OperatorPodLabels))
	require.Len(t, podList.Items, 1)
	oldUID := podList.Items[0].UID
	require.NoError(t, tc.Client.Delete(tc.Ctx, &podList.Items[0]))

	waitForKAIUpgradeCondition(t, tc, func(ctx context.Context) error {
		current := &corev1.PodList{}
		if err := tc.Client.List(ctx, current, client.InNamespace(setup.OperatorNamespace), setup.OperatorPodLabels); err != nil {
			return err
		}
		for i := range current.Items {
			if current.Items[i].UID != oldUID && kubeutils.IsPodReady(&current.Items[i]) {
				return nil
			}
		}
		return fmt.Errorf("replacement Grove operator Pod is not ready")
	})
}

func waitForScaledAggregatePodGroups(
	t *testing.T,
	tc *testctx.TestContext,
	pcs *corev1alpha1.PodCliqueSet,
) {
	t.Helper()
	waitForKAIUpgradeCondition(t, tc, func(ctx context.Context) error {
		podGroups := &kaischedulingv2alpha2.PodGroupList{}
		if err := tc.Client.List(ctx, podGroups, client.InNamespace(tc.Namespace), client.MatchingLabels{
			apicommon.LabelPartOfKey: kaiUpgradePCSName,
		}); err != nil {
			return err
		}
		for replica := range 2 {
			aggregate, err := podgroup.FilterAggregatePodGroupForPCSReplica(podGroups.Items, pcs.Name, replica)
			if err != nil {
				return err
			}
			if aggregate.Spec.MinSubGroup == nil || *aggregate.Spec.MinSubGroup != 1 {
				return fmt.Errorf("aggregate PodGroup %s has root minSubGroup %v after scale-in, expected 1", aggregate.Name, aggregate.Spec.MinSubGroup)
			}
			for _, subGroup := range aggregate.Spec.SubGroups {
				if subGroup.Name == "0-non-anchor-podgangs" {
					return fmt.Errorf("aggregate PodGroup %s still contains non-Anchor collection after scale-in", aggregate.Name)
				}
			}
		}
		return nil
	})
}

func containsLeaf(podGroup *kaischedulingv2alpha2.PodGroup, name string) bool {
	for _, subGroup := range podGroup.Spec.SubGroups {
		if subGroup.Name == name && subGroup.MinMember != nil {
			return true
		}
	}
	return false
}

func waitForKAIUpgradeCondition(t *testing.T, tc *testctx.TestContext, check func(context.Context) error) {
	t.Helper()

	var lastErr error
	err := wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		lastErr = check(ctx)
		return lastErr == nil, nil
	})
	require.NoError(t, errors.Join(err, lastErr))
}
