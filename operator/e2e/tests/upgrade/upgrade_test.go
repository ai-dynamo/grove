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

// Package upgrade contains an end-to-end test for upgrading the Grove operator.
//
// These tests are disabled by default due to the 'e2e' and 'e2eupgrade' build tags above.
// To run these tests, use:
//
//	go test -tags=e2e,e2eupgrade ./e2e/tests/upgrade/...
//
// Without both build tags, these tests will be skipped entirely.

package upgrade

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/google/go-github/v86/github"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestUpgradeFromGitHubRelease verifies that a workload's pods created with
// the latest released version of Grove will not be recreated during an upgrade
// to the latest code built from the current checkout.
//
// The initial version of Grove to install can be controlled with GROVE_UPGRADE_FROM_VERSION.
func TestUpgradeFromLatestGitHubRelease(t *testing.T) {
	fromVersion := os.Getenv("GROVE_UPGRADE_FROM_VERSION")
	if fromVersion == "" {
		fromVersion = latestGitHubRelease(t)
	}

	workload := &testctx.WorkloadConfig{
		Name:         "upgrade-survivor",
		YAMLPath:     "../../yaml/upgrade.yaml",
		Namespace:    "default",
		ExpectedPods: 2,
	}

	tc := setupTest(t, testConfig{
		fromVersion: fromVersion,
		workload:    workload,
	})

	bootstrapPCLQName := "upgrade-survivor-0-upgrade-group-0-bootstrap"
	scalePCLQ(t, tc, bootstrapPCLQName, 2, 3)

	podsList, err := tc.ListPods()
	require.NoError(t, err, "listing workload pods")

	upgradeGrove(t, tc)
	waitForPCSGPodIndices(t, tc, 0, 1, 2)
	verifyPodUIDsUnchanged(t, tc, podsList)

	deletePodAndVerifyIndexedReplacement(t, tc, 3)
	waitForPCSGPodIndices(t, tc, 0, 1, 2)
	scalePCLQAndVerifyIndices(t, tc, bootstrapPCLQName, 1, 2, []int{0, 1})
	scalePCLQAndVerifyIndices(t, tc, bootstrapPCLQName, 2, 3, []int{0, 1, 2})

	tc.ScalePCSAndWait(workload.Name, 2, 5, 0)

	initContainerImage := fmt.Sprintf("ghcr.io/ai-dynamo/grove/grove-initc:%s", fromVersion)
	verifyInitContainerUpdate(t, tc, podsList, initContainerImage)
}

func waitForPCSGPodIndices(t *testing.T, tc *testctx.TestContext, expectedIndices ...int) {
	t.Helper()
	expected := make([]string, 0, len(expectedIndices))
	for _, index := range expectedIndices {
		expected = append(expected, fmt.Sprint(index))
	}
	sort.Strings(expected)

	require.Eventually(t, func() bool {
		podList, err := tc.ListPods()
		if err != nil || len(podList.Items) != len(expected) {
			return false
		}
		actual := make([]string, 0, len(podList.Items))
		for _, pod := range podList.Items {
			index, ok := pod.Labels[common.LabelPodCliqueScalingGroupPodIndex]
			if !ok {
				return false
			}
			actual = append(actual, index)
		}
		sort.Strings(actual)
		return slices.Equal(actual, expected)
	}, defaultPollTimeout, defaultPollInterval, "PCSG pod indices did not converge")
}

func deletePodAndVerifyIndexedReplacement(t *testing.T, tc *testctx.TestContext, expectedPods int) {
	t.Helper()
	podList, err := tc.ListPods()
	require.NoError(t, err, "listing pods before churn")
	require.NotEmpty(t, podList.Items)
	deletedPodIndex := slices.IndexFunc(podList.Items, func(pod corev1.Pod) bool {
		return len(pods.InitContainerImages(pod)) == 0
	})
	require.NotEqual(t, -1, deletedPodIndex, "expected a pod without init containers")
	deletedPod := podList.Items[deletedPodIndex]
	require.NoError(t, tc.Client.Delete(tc.Ctx, &deletedPod), "deleting a pre-upgrade pod")

	require.Eventually(t, func() bool {
		currentPods, listErr := tc.ListPods()
		if listErr != nil || len(currentPods.Items) != expectedPods {
			return false
		}
		for _, pod := range currentPods.Items {
			if pod.UID == deletedPod.UID {
				return false
			}
			if _, ok := pod.Labels[common.LabelPodCliqueScalingGroupPodIndex]; !ok {
				return false
			}
		}
		return true
	}, defaultPollTimeout, defaultPollInterval, "deleted pod was not replaced with an indexed pod")
}

func scalePCLQAndVerifyIndices(t *testing.T, tc *testctx.TestContext, name string, replicas int32, expectedPods int, expectedIndices []int) {
	t.Helper()
	scalePCLQ(t, tc, name, replicas, expectedPods)
	waitForPCSGPodIndices(t, tc, expectedIndices...)
}

func scalePCLQ(t *testing.T, tc *testctx.TestContext, name string, replicas int32, expectedPods int) {
	t.Helper()
	pclq := &grovecorev1alpha1.PodClique{}
	key := client.ObjectKey{Namespace: tc.Namespace, Name: name}
	require.NoError(t, tc.Client.Get(tc.Ctx, key, pclq), "getting PodClique %s", name)
	pclq.Spec.Replicas = replicas
	require.NoError(t, tc.Client.Update(tc.Ctx, pclq), "scaling PodClique %s", name)
	require.NoError(t, tc.WaitForPods(expectedPods), "waiting for scaled PodClique %s", name)
}

// verifyInitContainerUpdate verifies that workload pods receive the new init container images after an upgrade.
// podsList and initContainerImage should be captured prior to the upgrade.
func verifyInitContainerUpdate(t *testing.T, tc *testctx.TestContext, podsList *corev1.PodList, initContainerImage string) {
	var initContainers []string
	for _, pod := range podsList.Items {
		initContainers = append(initContainers, pods.InitContainerImages(pod)...)
	}

	require.ElementsMatch(t, initContainers, []string{initContainerImage}, "init containers do not match expected list")

	podsList, err := tc.ListPods()
	require.NoError(t, err, "listing workload pods")

	initContainers = make([]string, 0, 2)
	for _, pod := range podsList.Items {
		initContainers = append(initContainers, pods.InitContainerImages(pod)...)
	}

	require.ElementsMatch(
		t,
		initContainers,
		// Expect a mix of the existing pods with the old initc and new pods with the updated initc
		[]string{
			"registry:5001/grove-initc:latest",
			initContainerImage,
		},
		"init containers do not match expected list",
	)
}

// verifyPodUIDsUnchanged verifies that workload pods not recreated.
// podsList should be captured prior to the upgrade.
func verifyPodUIDsUnchanged(t *testing.T, tc *testctx.TestContext, podsList *corev1.PodList) {
	var originalPodUIDs []string
	for _, pod := range podsList.Items {
		originalPodUIDs = append(originalPodUIDs, string(pod.GetUID()))
	}

	podsList, err := tc.ListPods()
	require.NoError(t, err, "listing workload pods")

	currentPodUIDs := make([]string, 0, len(podsList.Items))
	for _, pod := range podsList.Items {
		currentPodUIDs = append(currentPodUIDs, string(pod.GetUID()))
	}

	require.Subsetf(t, currentPodUIDs, originalPodUIDs, "pods were replaced during the operator upgrade")
}

// latestGitHubRelease fetches the latest Grove release tag for the base of the upgrade test.
func latestGitHubRelease(t *testing.T) string {
	t.Helper()

	client := github.NewClient(nil).WithAuthToken(os.Getenv("GITHUB_TOKEN"))
	release, _, err := client.Repositories.GetLatestRelease(t.Context(), "ai-dynamo", "grove")
	require.NoError(t, err, "get latest Grove release from GitHub")
	tagName := release.GetTagName()
	require.NotEmpty(t, tagName, "latest Grove GitHub release did not contain tag_name")
	return tagName
}
