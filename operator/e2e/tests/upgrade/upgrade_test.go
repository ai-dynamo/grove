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
	"testing"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgangmap"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/google/go-github/v86/github"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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
	s.podsBeforeUpgrade, err = tc.ListPods()
	require.NoError(t, err, "listing workload pods")
}

// verifyPodsSurvive checks that the grove.io CRDs store v1beta1 next to v1alpha1 and that the workload is
// readable at both versions, scales the workload, then asserts the pre-upgrade pods were not recreated and
// the init containers were updated.
func (s *podSurvivalUpgrade) verifyPodsSurvive(t *testing.T, tc *testctx.TestContext) {
	waitForStoredVersions(t, tc, corev1alpha1.SchemeGroupVersion.Version, grovev1beta1.SchemeGroupVersion.Version)
	requirePCSReadableAtBothVersions(t, tc, types.NamespacedName{Namespace: s.workload.Namespace, Name: s.workload.Name})
	verifyPodUIDsUnchanged(t, tc, s.podsBeforeUpgrade)

	tc.ScalePCSAndWait(s.workload.Name, 2, 4, 0)
	initContainerImage := fmt.Sprintf("ghcr.io/ai-dynamo/grove/grove-initc:%s", s.fromVersion)
	verifyInitContainerUpdate(t, tc, s.podsBeforeUpgrade, initContainerImage)
	verifyPodUIDsUnchanged(t, tc, s.podsBeforeUpgrade)
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

// Test_VDGR1_DowngradeToLatestGitHubRelease verifies the documented downgrade: after an upgrade to the
// operator built from the current checkout, grove-install-crds prepare-downgrade moves the grove.io objects
// back to grove.io/v1alpha1, and a Helm rollback to the released version then succeeds. The workload's pods
// survive both the upgrade and the downgrade, and the released operator keeps managing the workload.
//
// The released version can be controlled with GROVE_UPGRADE_FROM_VERSION.
func Test_VDGR1_DowngradeToLatestGitHubRelease(t *testing.T) {
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
		postUpgrade:     s.downgradeAndVerify,
	})
}

// downgradeAndVerify checks the stored versions after the upgrade, prepares the downgrade, rolls the release
// back to the installed version, and checks the CRDs, the workload and its pods on the released operator.
// It deletes the workload itself, because the shared cleanup reads grove.io/v1beta1, which the released
// CRDs no longer serve.
func (s *podSurvivalUpgrade) downgradeAndVerify(t *testing.T, tc *testctx.TestContext) {
	alpha := corev1alpha1.SchemeGroupVersion.Version
	pcsKey := types.NamespacedName{Namespace: s.workload.Namespace, Name: s.workload.Name}

	waitForStoredVersions(t, tc, alpha, grovev1beta1.SchemeGroupVersion.Version)
	verifyPodUIDsUnchanged(t, tc, s.podsBeforeUpgrade)
	pcsBeforeDowngrade := &corev1alpha1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(t.Context(), pcsKey, pcsBeforeDowngrade), "get PodCliqueSet at v1alpha1")

	runPrepareDowngrade(t)
	requireCRDsServeOnly(t, tc, alpha)

	testctx.Logger.Infof("rolling back to the released Grove operator %s", s.fromVersion)
	rollbackGrove(t, tc, 1)
	requireCRDsServeOnly(t, tc, alpha)

	pcsAfterDowngrade := &corev1alpha1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(t.Context(), pcsKey, pcsAfterDowngrade), "get PodCliqueSet after the downgrade")
	require.True(t, equality.Semantic.DeepEqual(pcsBeforeDowngrade.Spec, pcsAfterDowngrade.Spec),
		"PodCliqueSet spec changed during the downgrade")
	verifyPodUIDsUnchanged(t, tc, s.podsBeforeUpgrade)

	testctx.Logger.Info("scaling the workload on the released operator")
	patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"replicas":2}}`))
	require.NoError(t, tc.Client.Patch(t.Context(), pcsAfterDowngrade, patch), "scale PodCliqueSet at v1alpha1")
	require.NoError(t, tc.WaitForPods(4), "released operator did not scale the workload")
	verifyPodUIDsUnchanged(t, tc, s.podsBeforeUpgrade)

	testctx.Logger.Info("deleting the workload at v1alpha1")
	require.NoError(t, tc.Client.Delete(t.Context(), pcsAfterDowngrade), "delete PodCliqueSet at v1alpha1")
	_, err := tc.WaitForPodCount(0)
	require.NoError(t, err, "workload pods were not deleted")
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

	// The legacy workload has no PodGangMap, and the new operator creates one only after it set the migration
	// gate, so the PodGangMap is waited for before the gate is waited to clear.
	testctx.Logger.Info("verifying the migrated PodGangMap has an anchor holding worker index 0")
	require.NoError(t, podgangmap.WaitUntilVerified(t.Context(), verifier, pcsNsName, 0, pgmRecoverTimeout, defaultPollInterval,
		podgangmap.AnchorPCSGReplicaIndicesCheckFn("worker", []int32{0}),
	), "migrated PodGangMap not in expected state")

	testctx.Logger.Info("waiting for the legacy workload to finish migrating to the epoch-based scheme")
	waitForMigrationComplete(t, tc, pcsNsName)

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
			pcs := &grovev1beta1.PodCliqueSet{}
			if err := tc.Client.Get(ctx, pcsNsName, pcs); err != nil {
				return false, err
			}
			return meta.FindStatusCondition(pcs.Status.Conditions, apiconstants.ConditionTypePodGangMigrationInProgress) == nil, nil
		})
	require.NoError(t, err, "migration did not complete: PodGangMigrationInProgress condition still present")
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
