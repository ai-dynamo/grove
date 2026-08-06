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
	"strings"
	"testing"

	nameutils "github.com/ai-dynamo/grove/operator/api/common"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/topology"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
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
	kaiUpgradeFromVersion = "v0.1.0-alpha.11"
	kaiUpgradePCSName     = "kai-backend-upgrade"
)

var kaiUpgradeTopologyLevels = []corev1alpha1.TopologyLevel{
	{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
	{Domain: corev1alpha1.TopologyDomainBlock, Key: setup.TopologyLabelBlock},
	{Domain: corev1alpha1.TopologyDomainRack, Key: setup.TopologyLabelRack},
	{Domain: corev1alpha1.TopologyDomainHost, Key: setup.TopologyLabelHostname},
}

type kaiUpgradePodSnapshot struct {
	UID      types.UID
	NodeName string
}

// TestKAIBackendUpgradeFromAlpha11 verifies the in-place migration from
// PodGang-owned KAI PodGroups to one PCS-owned aggregate PodGroup per PCS replica.
func TestKAIBackendUpgradeFromAlpha11(t *testing.T) {
	workload := &testctx.WorkloadConfig{
		Name:         kaiUpgradePCSName,
		YAMLPath:     "../../yaml/kai-backend-upgrade.yaml",
		Namespace:    "default",
		ExpectedPods: 6,
	}
	cfg := testConfig{
		fromVersion:         kaiUpgradeFromVersion,
		workload:            workload,
		requiredWorkerNodes: 8,
		customizeHelmValues: configureKAIUpgradeHelmValues,
		beforeDeploy:        prepareKAIUpgradeTopology,
	}

	tc := setupTest(t, cfg)
	baselinePods, legacyPodGroups := waitForLegacyKAIPodGroups(t, tc)
	snapshots := snapshotKAIUpgradePods(baselinePods)

	upgradeGrove(t, tc, cfg)

	waitForKAIUpgradeMigration(t, tc, snapshots, legacyPodGroups)
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

func prepareKAIUpgradeTopology(t *testing.T, tc *testctx.TestContext) {
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
}

func waitForLegacyKAIPodGroups(t *testing.T, tc *testctx.TestContext) ([]corev1.Pod, map[string]struct{}) {
	t.Helper()

	var pods []corev1.Pod
	legacyPodGroups := map[string]struct{}{}
	waitForKAIUpgradeCondition(t, tc, func(ctx context.Context) error {
		podList, err := tc.ListPods()
		if err != nil {
			return err
		}
		if len(podList.Items) != 6 {
			return fmt.Errorf("found %d Pods, expected 6", len(podList.Items))
		}

		currentLegacyPodGroups := map[string]struct{}{}
		for i := range podList.Items {
			pod := &podList.Items[i]
			podGroupName := pod.Annotations["pod-group-name"]
			if !strings.HasPrefix(podGroupName, "pg-") {
				return fmt.Errorf("Pod %s has not converged to a KAI podgrouper PodGroup: %q", pod.Name, podGroupName)
			}

			podGroup := &kaischedulingv2alpha2.PodGroup{}
			if err = tc.Client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: podGroupName}, podGroup); err != nil {
				return err
			}
			owner := findPodGangOwner(podGroup)
			if owner == nil || owner.Name != pod.Labels[nameutils.LabelPodGang] {
				return fmt.Errorf("legacy PodGroup %s has unexpected owner %v", podGroupName, owner)
			}
			currentLegacyPodGroups[podGroupName] = struct{}{}
		}
		if len(currentLegacyPodGroups) != 4 {
			return fmt.Errorf("found %d legacy PodGroups, expected 4", len(currentLegacyPodGroups))
		}

		pods = append([]corev1.Pod(nil), podList.Items...)
		legacyPodGroups = currentLegacyPodGroups
		return nil
	})
	return pods, legacyPodGroups
}

func snapshotKAIUpgradePods(pods []corev1.Pod) map[string]kaiUpgradePodSnapshot {
	snapshots := make(map[string]kaiUpgradePodSnapshot, len(pods))
	for i := range pods {
		snapshots[pods[i].Name] = kaiUpgradePodSnapshot{
			UID:      pods[i].UID,
			NodeName: pods[i].Spec.NodeName,
		}
	}
	return snapshots
}

func waitForKAIUpgradeMigration(
	t *testing.T,
	tc *testctx.TestContext,
	snapshots map[string]kaiUpgradePodSnapshot,
	legacyPodGroups map[string]struct{},
) {
	t.Helper()

	pcs := &corev1alpha1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: kaiUpgradePCSName}, pcs))

	waitForKAIUpgradeCondition(t, tc, func(ctx context.Context) error {
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
			if !found {
				return fmt.Errorf("Pod %s was created during the upgrade", pod.Name)
			}
			if snapshot.UID != pod.UID || snapshot.NodeName != pod.Spec.NodeName {
				return fmt.Errorf("Pod %s was recreated or moved during the upgrade", pod.Name)
			}
			if pod.Status.Phase != corev1.PodRunning {
				return fmt.Errorf("Pod %s is in phase %s, expected Running", pod.Name, pod.Status.Phase)
			}

			replica := pod.Labels[nameutils.LabelPodCliqueSetReplicaIndex]
			expectedPodGroup := fmt.Sprintf("grove-%s-%s", kaiUpgradePCSName, replica)
			if pod.Annotations["pod-group-name"] != expectedPodGroup {
				return fmt.Errorf("Pod %s references PodGroup %q, expected %q", pod.Name, pod.Annotations["pod-group-name"], expectedPodGroup)
			}
			if pod.Annotations["kai.scheduler/skip-podgrouper"] != "true" {
				return fmt.Errorf("Pod %s does not skip the KAI podgrouper", pod.Name)
			}
			if pod.Labels["kai.scheduler/subgroup-name"] != pod.Labels[nameutils.LabelPodClique] {
				return fmt.Errorf("Pod %s has not migrated to its PodClique subgroup", pod.Name)
			}
		}

		for legacyName := range legacyPodGroups {
			err = tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: legacyName}, &kaischedulingv2alpha2.PodGroup{})
			if err == nil {
				return fmt.Errorf("legacy PodGroup %s still exists", legacyName)
			}
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("checking legacy PodGroup %s deletion: %w", legacyName, err)
			}
		}

		podGroups := &kaischedulingv2alpha2.PodGroupList{}
		if err = tc.Client.List(ctx, podGroups, client.InNamespace(tc.Namespace)); err != nil {
			return err
		}
		if len(podGroups.Items) != 2 {
			return fmt.Errorf("found %d PodGroups after migration, expected 2", len(podGroups.Items))
		}
		byName := make(map[string]*kaischedulingv2alpha2.PodGroup, len(podGroups.Items))
		for i := range podGroups.Items {
			byName[podGroups.Items[i].Name] = &podGroups.Items[i]
		}
		for replica := range 2 {
			expectedName := fmt.Sprintf("grove-%s-%d", kaiUpgradePCSName, replica)
			podGroup, found := byName[expectedName]
			if !found {
				return fmt.Errorf("aggregate PodGroup %s does not exist", expectedName)
			}
			if !metav1.IsControlledBy(podGroup, pcs) {
				return fmt.Errorf("aggregate PodGroup %s is not controlled by PodCliqueSet %s", expectedName, pcs.Name)
			}
		}
		return nil
	})
}

func findPodGangOwner(podGroup *kaischedulingv2alpha2.PodGroup) *metav1.OwnerReference {
	for i := range podGroup.OwnerReferences {
		if podGroup.OwnerReferences[i].Kind == "PodGang" {
			return &podGroup.OwnerReferences[i]
		}
	}
	return nil
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
