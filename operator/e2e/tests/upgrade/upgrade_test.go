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
	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgangmap"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	pcsBeforeUpgrade  *grovev1alpha1.PodCliqueSet
}

// deployWorkload deploys the workload on the fromVersion operator and captures its pods.
func (s *podSurvivalUpgrade) deployWorkload(t *testing.T, tc *testctx.TestContext) {
	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err, "applying workload")
	s.pcsBeforeUpgrade = waitForPCSConvergence(t, tc)
	require.NoError(t, tc.WaitForPods(s.workload.ExpectedPods))
	s.podsBeforeUpgrade, err = tc.ListPods()
	require.NoError(t, err, "listing workload pods")
}

// verifyPodsSurvive scales the workload, then asserts the pre-upgrade pods were not recreated and the
// init containers were updated.
func (s *podSurvivalUpgrade) verifyPodsSurvive(t *testing.T, tc *testctx.TestContext) *commonrevision.Revision {
	tc.ScalePCSAndWait(s.workload.Name, 2, 4, 0)
	pcs, revision := waitForRevisionAdoption(t, tc)
	require.Equal(t, s.pcsBeforeUpgrade.Status.CurrentGenerationHash, pcs.Status.CurrentGenerationHash,
		"adoption and scaling must preserve the selected generation hash")
	require.NoError(t, tc.WaitForPods(4))
	initContainerImage := fmt.Sprintf("ghcr.io/ai-dynamo/grove/grove-initc:%s", s.fromVersion)
	verifyInitContainerUpdate(t, tc, s.podsBeforeUpgrade, initContainerImage)
	verifyAdoptedPodHashes(t, tc, revision, s.podsBeforeUpgrade)
	return revision
}

// Test_VUPG1_RevisionAdoptionAndUpdate covers both released hash layouts and a subsequent template update.
// GROVE_UPGRADE_FROM_VERSION can select a single starting release.
func Test_VUPG1_RevisionAdoptionAndUpdate(t *testing.T) {
	versions := []string{"v0.1.0-alpha.9", defaultUpgradeFromVersion}
	if version := os.Getenv("GROVE_UPGRADE_FROM_VERSION"); version != "" {
		versions = []string{version}
	}

	for _, version := range versions {
		t.Run(version, func(t *testing.T) {
			s := &podSurvivalUpgrade{fromVersion: version, workload: revisionWorkload()}
			runUpgradeTest(t, upgradeTest{
				fromVersion:     version,
				nodeWorkerCount: 1,
				prepareOpts:     []testctx.TestOption{testctx.WithWorkload(s.workload)},
				preUpgrade:      s.deployWorkload,
				postUpgrade: func(t *testing.T, tc *testctx.TestContext) {
					before := s.verifyPodsSurvive(t, tc)
					oldBootstrapHash, err := before.CliqueHash("bootstrap")
					require.NoError(t, err)
					oldWorkerHash, err := before.CliqueHash("worker")
					require.NoError(t, err)

					updateWorkerAndWait(t, tc, "after-upgrade")
					_, after := waitForRevisionAdoption(t, tc)
					require.NotEqual(t, before.Name(), after.Name())
					bootstrapHash, err := after.CliqueHash("bootstrap")
					require.NoError(t, err)
					workerHash, err := after.CliqueHash("worker")
					require.NoError(t, err)
					require.Equal(t, oldBootstrapHash, bootstrapHash, "unchanged clique identity must survive an update")
					require.NotEqual(t, oldWorkerHash, workerHash)
				},
			})
		})
	}
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

func Test_VUPG3_DowngradeAfterRevisionAdoption(t *testing.T) {
	version := upgradeFromVersion()
	s := &podSurvivalUpgrade{fromVersion: version, workload: revisionWorkload()}
	runUpgradeTest(t, upgradeTest{
		fromVersion:     version,
		nodeWorkerCount: 1,
		prepareOpts:     []testctx.TestOption{testctx.WithWorkload(s.workload)},
		preUpgrade: func(t *testing.T, tc *testctx.TestContext) {
			waitForReleasedOperator(t, tc, version)
			s.deployWorkload(t, tc)
			require.Nil(t, s.pcsBeforeUpgrade.Status.CurrentRevision)
			if progress := s.pcsBeforeUpgrade.Status.UpdateProgress; progress != nil {
				require.Empty(t, progress.CurrentlyUpdating, "the baseline must have no update in progress")
			}
			revisions := &appsv1.ControllerRevisionList{}
			require.NoError(t, tc.Client.List(t.Context(), revisions, client.InNamespace(tc.Namespace)))
			for i := range revisions.Items {
				require.False(t, metav1.IsControlledBy(&revisions.Items[i], s.pcsBeforeUpgrade),
					"the starting release must not manage ControllerRevisions")
			}
		},
		postUpgrade: func(t *testing.T, tc *testctx.TestContext) {
			beforeDowngrade, selected := waitForRevisionAdoption(t, tc)
			verifyPodUIDsUnchanged(t, tc, s.podsBeforeUpgrade)
			require.Equal(t, s.pcsBeforeUpgrade.Spec, beforeDowngrade.Spec,
				"the round trip must not change the workload template")

			downgradeGrove(t, tc, version)
			waitForPCSConvergence(t, tc)
			require.NoError(t, tc.WaitForPods(s.workload.ExpectedPods))
			// Only scaling is needed to prove the old operator resumed management.
			tc.ScalePCSAndWait(s.workload.Name, 2, 4, 0)
			scaled := waitForPCSConvergence(t, tc)
			require.NoError(t, tc.WaitForPods(4))
			expected := beforeDowngrade.Spec.DeepCopy()
			expected.Replicas = 2
			require.Equal(t, *expected, scaled.Spec, "only replicas may change after downgrading")
			require.NoError(t, tc.Client.Get(t.Context(), client.ObjectKey{Namespace: tc.Namespace, Name: selected.Name()}, &appsv1.ControllerRevision{}),
				"the downgraded operator must ignore ControllerRevisions")
			verifyStableWorkload(t, tc, 4)
		},
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

	_, pullRepo, tag := upgradeImageSettings()
	require.ElementsMatch(
		t,
		initContainers,
		// Expect a mix of the existing pods with the old initc and new pods with the updated initc
		[]string{
			pullRepo + "/grove-initc:" + tag,
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
