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
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgangmap"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/google/go-github/v86/github"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// upgradePCSGWorkloadName is the name of the all-PodCliqueScalingGroup workload used by the
	// migration recovery test.
	upgradePCSGWorkloadName = "upgrade-pcsg-only"
	// upgradePCSGWorkloadNamespace is the namespace of that workload.
	upgradePCSGWorkloadNamespace = "default"
	// upgradeBootstrapPCLQName is the bootstrap PodClique scaled by the pod-index upgrade test.
	upgradeBootstrapPCLQName = "upgrade-survivor-0-upgrade-group-0-bootstrap"
)

// podSurvivalUpgrade holds the state shared between the pre-upgrade and post-upgrade steps of the pod
// survival test. The pod list is captured before the upgrade and asserted against afterwards.
type podSurvivalUpgrade struct {
	fromVersion       string
	workload          *testctx.WorkloadConfig
	podsBeforeUpgrade *corev1.PodList
}

// deployWorkload deploys the workload on the fromVersion operator and captures its pods.
func (s *podSurvivalUpgrade) deployWorkload(t *testing.T, tc *testctx.TestContext) {
	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err, "applying workload")
	scalePCLQ(t, tc, upgradeBootstrapPCLQName, 2, 3)
	s.podsBeforeUpgrade, err = tc.ListPods()
	require.NoError(t, err, "listing workload pods")
}

// verifyPodsSurvive verifies the upgrade, churn, and scaling behavior of PCSG pod indices before
// scaling the workload and checking the init container migration.
func (s *podSurvivalUpgrade) verifyPodsSurvive(t *testing.T, tc *testctx.TestContext) {
	waitForPCSGPodIndices(t, tc, 0, 1, 2)
	verifyPodUIDsUnchanged(t, tc, s.podsBeforeUpgrade)

	deletePodAndVerifyIndexedReplacement(t, tc, 3)
	waitForPCSGPodIndices(t, tc, 0, 1, 2)
	scalePCLQAndVerifyIndices(t, tc, upgradeBootstrapPCLQName, 1, 2, []int{0, 1})
	scalePCLQAndVerifyIndices(t, tc, upgradeBootstrapPCLQName, 2, 3, []int{0, 1, 2})

	tc.ScalePCSAndWait(s.workload.Name, 2, 5, 0)
	initContainerImage := fmt.Sprintf("ghcr.io/ai-dynamo/grove/grove-initc:%s", s.fromVersion)
	verifyInitContainerUpdate(t, tc, s.podsBeforeUpgrade, initContainerImage)
}

// Test_VUPG1_UpgradeFromLatestGitHubRelease verifies that a workload's pods created with the latest released
// version of Grove are not recreated during an upgrade to the operator built from the current
// checkout.
//
// The initial version of Grove to install can be controlled with GROVE_UPGRADE_FROM_VERSION.
func Test_VUPG1_UpgradeFromLatestGitHubRelease(t *testing.T) {
	fromVersion := os.Getenv("GROVE_UPGRADE_FROM_VERSION")
	if fromVersion == "" {
		fromVersion = latestGitHubRelease(t)
	}

	s := &podSurvivalUpgrade{
		fromVersion: fromVersion,
		workload: &testctx.WorkloadConfig{
			Name:         "upgrade-survivor",
			YAMLPath:     "../../yaml/upgrade.yaml",
			Namespace:    "default",
			ExpectedPods: 2,
		},
	}

	runUpgradeTest(t, upgradeTest{
		fromVersion:     s.fromVersion,
		nodeWorkerCount: 1,
		prepareOpts:     []testctx.TestOption{testctx.WithWorkload(s.workload)},
		preUpgrade:      s.deployWorkload,
		postUpgrade:     s.verifyPodsSurvive,
	})
}

// Test_VUPG2_RecoverPodGangMapAfterScaleBelowMinAvailable verifies that after migrating a
// legacy workload from v0.1.0-alpha.11 to the epoch-based PodGang scheme, scaling a
// PodCliqueScalingGroup below MinAvailable drains its PodGangMap to empty without wedging, and scaling
// it back up rebuilds the anchor and reschedules the pods. This exercises the fix for issues #808 and
// #809 through the real migration path.
func Test_VUPG2_RecoverPodGangMapAfterScaleBelowMinAvailable(t *testing.T) {
	runUpgradeTest(t, upgradeTest{
		fromVersion:     "v0.1.0-alpha.11",
		nodeWorkerCount: 4,
		prepareOpts: []testctx.TestOption{testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         upgradePCSGWorkloadName,
			YAMLPath:     "../../yaml/upgrade-pcsg-only.yaml",
			Namespace:    upgradePCSGWorkloadNamespace,
			ExpectedPods: 2,
		})},
		preUpgrade:  deployWorkloadOnFromVersion,
		postUpgrade: verifyPodGangMapRecoversAfterScaleBelowMinAvailable,
	})
}

// deployWorkloadOnFromVersion deploys the configured workload on the fromVersion operator.
func deployWorkloadOnFromVersion(t *testing.T, tc *testctx.TestContext) {
	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err, "applying workload on the fromVersion operator")
}

// verifyPodGangMapRecoversAfterScaleBelowMinAvailable waits for the migration to complete, checks the
// migrated PodGangMap, scales the worker group below MinAvailable to zero so the anchor drains away,
// then scales it back up and checks the anchor is rebuilt and the pods run.
func verifyPodGangMapRecoversAfterScaleBelowMinAvailable(t *testing.T, tc *testctx.TestContext) {
	verifier := podgangmap.NewVerifier(tc.Client, testctx.Logger)
	pcsNsName := types.NamespacedName{Namespace: upgradePCSGWorkloadNamespace, Name: upgradePCSGWorkloadName}

	testctx.Logger.Info("waiting for the legacy workload to finish migrating to the epoch-based scheme")
	waitForMigrationComplete(t, tc, pcsNsName)

	testctx.Logger.Info("verifying the migrated PodGangMap has an anchor holding worker index 0")
	require.NoError(t, podgangmap.WaitUntilVerified(t.Context(), verifier, pcsNsName, 0, pgmRecoverTimeout, defaultPollInterval,
		podgangmap.AnchorPCSGReplicaIndicesCheckFn("worker", []int32{0}),
	), "migrated PodGangMap not in expected state")

	testctx.Logger.Info("scaling the worker group to 0 and verifying the PodGangMap holds no entries")
	tc.ScalePCSGAcrossAllReplicasAndWait(upgradePCSGWorkloadName, "worker", 1, 0, 0, 0)
	require.NoError(t, podgangmap.WaitUntilVerified(t.Context(), verifier, pcsNsName, 0, pgmRecoverTimeout, defaultPollInterval,
		podgangmap.NoEntriesCheckFn(),
	))

	testctx.Logger.Info("scaling the worker group back to 1 and verifying the anchor is rebuilt")
	tc.ScalePCSGAcrossAllReplicasAndWait(upgradePCSGWorkloadName, "worker", 1, 1, 2, 0)
	require.NoError(t, podgangmap.WaitUntilVerified(t.Context(), verifier, pcsNsName, 0, pgmRecoverTimeout, defaultPollInterval,
		podgangmap.AnchorPCSGReplicaIndicesCheckFn("worker", []int32{0}),
	))

	require.NoError(t, tc.WaitForRunningPods(2), "pods not running after scaling the group back up")
}

// waitForMigrationComplete polls the PodCliqueSet until the PodGangMigrationInProgress condition is
// cleared, which the operator does once every replica has migrated to the epoch-based PodGang scheme.
func waitForMigrationComplete(t *testing.T, tc *testctx.TestContext, pcsNsName types.NamespacedName) {
	t.Helper()
	err := wait.PollUntilContextTimeout(t.Context(), defaultPollInterval, defaultPollTimeout, true,
		func(ctx context.Context) (bool, error) {
			pcs := &grovev1alpha1.PodCliqueSet{}
			if err := tc.Client.Get(ctx, pcsNsName, pcs); err != nil {
				return false, err
			}
			return meta.FindStatusCondition(pcs.Status.Conditions, apiconstants.ConditionTypePodGangMigrationInProgress) == nil, nil
		})
	require.NoError(t, err, "migration did not complete: PodGangMigrationInProgress condition still present")
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
			index, ok := pod.Labels[apicommon.LabelPodCliqueScalingGroupPodIndex]
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
			if _, ok := pod.Labels[apicommon.LabelPodCliqueScalingGroupPodIndex]; !ok {
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
	pclq := &grovev1alpha1.PodClique{}
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
