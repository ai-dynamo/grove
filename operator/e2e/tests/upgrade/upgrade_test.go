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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgangmap"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	updateutils "github.com/ai-dynamo/grove/operator/e2e/tests/update/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/google/go-github/v86/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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
	// lastV1Alpha1Release is the last Grove release whose grove.io CRDs serve and store only v1alpha1.
	// The downgrade test pins it because from v0.2.0-beta1 on a release already stores v1beta1, leaving
	// the documented downgrade with nothing to move back.
	lastV1Alpha1Release = "v0.1.0-alpha.17"
)

// podSurvivalUpgrade holds the state shared between the pre-upgrade and post-upgrade steps of the pod
// survival test. The pod list is captured before the upgrade and asserted against afterwards.
type podSurvivalUpgrade struct {
	fromVersion          string
	workload             *testctx.WorkloadConfig
	podsBeforeUpgrade    *corev1.PodList
	storedVersionsBefore []string
}

// deployWorkload deploys the workload on the fromVersion operator and captures its pods and the versions
// that release stores.
func (s *podSurvivalUpgrade) deployWorkload(t *testing.T, tc *testctx.TestContext) {
	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err, "applying workload")
	s.podsBeforeUpgrade, err = tc.ListPods()
	require.NoError(t, err, "listing workload pods")
	s.storedVersionsBefore = commonStoredVersions(t, tc)
}

// verifyPodsSurvive checks that the grove.io CRDs store this checkout's version alongside whatever the
// installed release stored and that the workload is readable at both versions, scales the workload, then
// asserts the pre-upgrade pods were not recreated and the init containers were updated.
func (s *podSurvivalUpgrade) verifyPodsSurvive(t *testing.T, tc *testctx.TestContext) {
	waitForStoredVersions(t, tc, withCurrentStorageVersion(s.storedVersionsBefore)...)
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

// Test_VDGR1_DowngradeToLastV1Alpha1Release verifies the documented downgrade: after an upgrade to the
// operator built from the current checkout, run-api-downgrade moves the grove.io objects back to
// grove.io/v1alpha1, and a Helm rollback to the released version then succeeds. The workload's pods
// survive both the upgrade and the downgrade, and the released operator keeps managing the workload.
//
// The released version can be controlled with GROVE_UPGRADE_FROM_VERSION.
func Test_VDGR1_DowngradeToLastV1Alpha1Release(t *testing.T) {
	fromVersion := os.Getenv("GROVE_UPGRADE_FROM_VERSION")
	if fromVersion == "" {
		fromVersion = lastV1Alpha1Release
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

	waitForStoredVersions(t, tc, withCurrentStorageVersion(s.storedVersionsBefore)...)
	verifyPodUIDsUnchanged(t, tc, s.podsBeforeUpgrade)
	pcsBeforeDowngrade := &corev1alpha1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(t.Context(), pcsKey, pcsBeforeDowngrade), "get PodCliqueSet at v1alpha1")

	runAPIDowngrade(t)
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

// Test_VUPG3_LegacyGenerationRetentionPreservesStandaloneScaling checks that upgrading Grove does not
// start a workload update. The workload must keep its existing generation hash, update history,
// and pods, and must still scale and detect when too few pods are scheduled.
//
// The test covers Coherent and RollingRecreate, both with no previous workload update and with a completed one.
//
// Scenario:
//  1. Install v0.2.1-beta1 and create a Ready workload. In the completed-update cases,
//     finish the workload update before upgrading Grove.
//  2. Upgrade Grove without editing the workload. Check that scheduling metadata is initialized
//     without changing its generation hash or update history, starting an update, or replacing pods.
//  3. Scale the standalone frontend from two to three pods. Verify the extra pod becomes Ready
//     without replacing the original pods.
//  4. Cordon the workers and delete the frontend pods. Verify MinAvailableBreached=True
//     while their replacements cannot schedule.
//  5. Uncordon the workers and verify recovery with the same generation hash and update history.
func Test_VUPG3_LegacyGenerationRetentionPreservesStandaloneScaling(t *testing.T) {
	for _, strategy := range []grovev1beta1.UpdateStrategyType{grovev1beta1.CoherentStrategy, grovev1beta1.RollingRecreateStrategy} {
		for _, history := range []string{"idle", "completed"} {
			t.Run(string(strategy)+"/"+history, func(t *testing.T) {
				var beforePCS *grovev1beta1.PodCliqueSet
				var beforeFrontend grovev1beta1.PodClique
				var beforePGM *grovev1beta1.PodGangMap
				var beforePods *corev1.PodList
				runUpgradeTest(t, upgradeTest{
					fromVersion:     "v0.2.1-beta1",
					nodeWorkerCount: 10,
					prepareOpts: []testctx.TestOption{testctx.WithWorkload(&testctx.WorkloadConfig{
						Name: "workload-coherent", YAMLPath: "../../yaml/workload-coherent.yaml",
						Namespace: "default", ExpectedPods: 6,
					})},
					preUpgrade: func(t *testing.T, tc *testctx.TestContext) {
						_, err := tc.DeployAndVerifyWorkload()
						require.NoError(t, err)
						require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, strategy))
						if history == "completed" {
							require.NoError(t, updateutils.TriggerPodCliqueUpdateWithRevision(tc, "before-upgrade", "frontend", "prefill", "decode"))
							require.NoError(t, updateutils.WaitForRollingUpdateComplete(tc, 1))
							require.NoError(t, tc.WaitForPods(6))
						}
						beforePCS, err = updateutils.GetPCS(tc, tc.Workload.Name)
						require.NoError(t, err)
						require.NotNil(t, beforePCS.Status.CurrentGenerationHash)
						require.Nil(t, beforePCS.Status.AppliedSchedulingConfigurationHash, "the released operator must produce a legacy baseline")
						if history == "idle" {
							require.Nil(t, beforePCS.Status.UpdateProgress)
						}
						require.NotNil(t, beforePCS.Spec.Template.TerminationDelay)
						require.Greater(t, beforePCS.Spec.Template.TerminationDelay.Duration, tc.Timeout, "breach observation must precede gang termination")
						frontendKey := client.ObjectKey{Namespace: tc.Namespace, Name: updateutils.StandaloneCliqueFQN(tc, "frontend")}
						require.NoError(t, tc.Client.Get(tc.Ctx, frontendKey, &beforeFrontend))
						if history == "completed" {
							require.NotNil(t, beforeFrontend.Status.UpdateProgress)
							require.NotNil(t, beforeFrontend.Status.UpdateProgress.UpdateEndedAt)
						}
						beforePGM, err = updateutils.GetReplicaPodGangMap(tc, 0)
						require.NoError(t, err)
						beforePods, err = tc.ListPods()
						require.NoError(t, err)
						require.Len(t, beforePods.Items, 6)
						updateutils.AssertGenerationHashConverged(tc)
						updateutils.AssertNoActiveChildUpdates(tc)
					},
					postUpgrade: func(t *testing.T, tc *testctx.TestContext) {
						t.Log("verify baseline initialization without a new generation or update")
						initialized := waitForSchedulingBaseline(t, tc)
						assertLegacyPCSUnchanged(t, beforePCS, initialized)
						var frontend grovev1beta1.PodClique
						frontendKey := client.ObjectKeyFromObject(&beforeFrontend)
						require.NoError(t, tc.Client.Get(tc.Ctx, frontendKey, &frontend))
						require.Equal(t, beforeFrontend.UID, frontend.UID)
						require.Equal(t, beforeFrontend.Status.UpdateProgress, frontend.Status.UpdateProgress)
						verifyPodUIDsUnchanged(t, tc, beforePods)
						updateutils.AssertGenerationHashConverged(tc)
						updateutils.AssertNoActiveChildUpdates(tc)

						t.Log("scale the existing frontend PodClique from two to three pods")
						beforeCount, beforeEpochs := standaloneAnchorInventory(beforePGM.Spec.Entries, "frontend")
						require.Equal(t, int32(2), beforeCount)
						require.NoError(t, tc.ScalePodClique(frontend.Name, 3))
						require.NoError(t, tc.WaitForPods(7))
						pgm, err := updateutils.GetReplicaPodGangMap(tc, 0)
						require.NoError(t, err)
						count, epochs := standaloneAnchorInventory(pgm.Spec.Entries, "frontend")
						require.Equal(t, int32(3), count)
						require.ElementsMatch(t, beforeEpochs, epochs)
						require.Equal(t, beforePGM.UID, pgm.UID)
						verifyPodUIDsUnchanged(t, tc, beforePods)
						updateutils.AssertPodGangMapSingleGeneration(t, tc)
						current, err := updateutils.GetPCS(tc, tc.Workload.Name)
						require.NoError(t, err)
						assertLegacyPCSUnchanged(t, beforePCS, current)

						t.Log("prevent frontend scheduling and require a MinAvailable breach")
						var workers corev1.NodeList
						require.NoError(t, tc.Client.List(tc.Ctx, &workers, client.MatchingLabels{setup.WorkerNodeLabelKey: setup.WorkerNodeLabelValue}))
						var schedulable []string
						for _, node := range workers.Items {
							if !node.Spec.Unschedulable {
								schedulable = append(schedulable, node.Name)
							}
						}
						require.NotEmpty(t, schedulable)
						defer tc.UncordonNodes(schedulable)
						tc.CordonNodes(schedulable)
						var frontendPods corev1.PodList
						require.NoError(t, tc.Client.List(tc.Ctx, &frontendPods, client.InNamespace(tc.Namespace),
							client.MatchingLabels{apicommon.LabelPodClique: frontend.Name}))
						require.Len(t, frontendPods.Items, 3)
						for i := range frontendPods.Items {
							require.NoError(t, tc.Client.Delete(tc.Ctx, &frontendPods.Items[i]))
						}
						require.EventuallyWithT(t, func(c *assert.CollectT) {
							require.NoError(c, tc.Client.Get(tc.Ctx, frontendKey, &frontend))
							condition := meta.FindStatusCondition(frontend.Status.Conditions, apiconstants.ConditionTypeMinAvailableBreached)
							require.NotNil(c, condition)
							assert.Equal(c, metav1.ConditionTrue, condition.Status)
							assert.Equal(c, apiconstants.ConditionReasonScheduledReplicasBelowMinAvailable, condition.Reason)
							assert.Zero(c, frontend.Status.ScheduledReplicas)
						}, tc.Timeout, tc.Interval)
						if beforeFrontend.Status.UpdateProgress == nil {
							require.Nil(t, frontend.Status.UpdateProgress)
						} else {
							require.NotNil(t, frontend.Status.UpdateProgress)
							require.Equal(t, beforeFrontend.Status.UpdateProgress.UpdateStartedAt, frontend.Status.UpdateProgress.UpdateStartedAt)
							require.Equal(t, beforeFrontend.Status.UpdateProgress.UpdateEndedAt, frontend.Status.UpdateProgress.UpdateEndedAt)
						}
						tc.UncordonNodes(schedulable)
						require.NoError(t, tc.WaitForPods(7))
						current, err = updateutils.GetPCS(tc, tc.Workload.Name)
						require.NoError(t, err)
						assertLegacyPCSUnchanged(t, beforePCS, current)
						updateutils.AssertGenerationHashConverged(tc)
						updateutils.AssertNoActiveChildUpdates(tc)
					},
				})
			})
		}
	}
}

// Test_VUPG4_MinAvailableChangeAfterLegacyUpgrade verifies that a scheduling change after a real
// operator upgrade recreates a legacy PCS replica rather than merely changing generation metadata.
//
// Scenario:
//  1. Install v0.2.1-beta1 and deploy workload2 under Coherent with one Ready replica and ten pods.
//     Set sg-x maxUnavailable to two so a later MinAvailable of two is valid. Capture the legacy PCS,
//     standalone PodClique, PCSG, and PodGangMap without rewriting their generation metadata.
//  2. Upgrade to the current checkout. Require scheduling-baseline initialization without changing
//     the PCS specification, generation identity, update history, or replica resource UIDs.
//  3. Block fresh replica-zero pod readiness and increase sg-x MinAvailable from one to two.
//     Require active replica recreation with a new target and the original scheduling hash still applied.
//  4. Require fresh PodClique, PCSG, and PodGangMap UIDs, with both sg-x indices in the anchor and no tail.
//  5. Release readiness and require ten Ready pods, an observed and completed update, child generation
//     convergence, and stable replacement UIDs with the target scheduling hash now applied.
func Test_VUPG4_MinAvailableChangeAfterLegacyUpgrade(t *testing.T) {
	var beforePCS *grovev1beta1.PodCliqueSet
	var beforePCLQ grovev1beta1.PodClique
	var beforePCSG grovev1beta1.PodCliqueScalingGroup
	var beforePGM *grovev1beta1.PodGangMap
	runUpgradeTest(t, upgradeTest{
		fromVersion:     "v0.2.1-beta1",
		nodeWorkerCount: 10,
		prepareOpts: []testctx.TestOption{testctx.WithWorkload(&testctx.WorkloadConfig{
			Name: "workload2", YAMLPath: "../../yaml/workload2.yaml", Namespace: "default", ExpectedPods: 10,
		})},
		preUpgrade: func(t *testing.T, tc *testctx.TestContext) {
			_, err := tc.DeployAndVerifyWorkload()
			require.NoError(t, err)
			require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, grovev1beta1.CoherentStrategy))
			beforePCS, err = updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			original := beforePCS.DeepCopy()
			require.Len(t, beforePCS.Spec.Template.PodCliqueScalingGroupConfigs, 1)
			group := &beforePCS.Spec.Template.PodCliqueScalingGroupConfigs[0]
			require.Equal(t, "sg-x", group.Name)
			require.NotNil(t, group.RollingUpdate)
			group.RollingUpdate.MaxUnavailable = new(int32(2))
			require.NoError(t, tc.Client.Patch(tc.Ctx, beforePCS, client.MergeFrom(original)))
			require.NotNil(t, beforePCS.Status.CurrentGenerationHash)
			require.Nil(t, beforePCS.Status.AppliedSchedulingConfigurationHash)
			require.Nil(t, beforePCS.Status.UpdateProgress)
			require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: updateutils.StandaloneCliqueFQN(tc, "pc-a")}, &beforePCLQ))
			require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: updateutils.PCSGFQN(tc, "sg-x")}, &beforePCSG))
			beforePGM, err = updateutils.GetReplicaPodGangMap(tc, 0)
			require.NoError(t, err)
			updateutils.AssertReplicaPodGangMap(t, beforePGM.Spec.Entries, updateutils.ExpectedReplicaPodGangMap{
				GenerationHash: *beforePCS.Status.CurrentGenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
				PCSGName: "sg-x", AnchorIndices: []int32{0}, TailIndices: []int32{1},
			})
		},
		postUpgrade: func(t *testing.T, tc *testctx.TestContext) {
			initialized := waitForSchedulingBaseline(t, tc)
			assertLegacyPCSUnchanged(t, beforePCS, initialized)
			var pclq grovev1beta1.PodClique
			var pcsg grovev1beta1.PodCliqueScalingGroup
			require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&beforePCLQ), &pclq))
			require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&beforePCSG), &pcsg))
			pgm, err := updateutils.GetReplicaPodGangMap(tc, 0)
			require.NoError(t, err)
			require.Equal(t, beforePCLQ.UID, pclq.UID)
			require.Equal(t, beforePCSG.UID, pcsg.UID)
			require.Equal(t, beforePGM.UID, pgm.UID)
			updateutils.AssertGenerationHashConverged(tc)
			updateutils.AssertNoActiveChildUpdates(tc)

			t.Log("hold replacement readiness and increase sg-x MinAvailable")
			const stageName = "pod-ready-blocked-r0-standalone"
			require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, "../../yaml/kwok/pod-ready-blocked-r0-standalone.yaml"))
			defer func() { assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, stageName)) }()
			changed := initialized.DeepCopy()
			require.Len(t, changed.Spec.Template.PodCliqueScalingGroupConfigs, 1)
			require.Equal(t, "sg-x", changed.Spec.Template.PodCliqueScalingGroupConfigs[0].Name)
			changed.Spec.Template.PodCliqueScalingGroupConfigs[0].MinAvailable = new(int32(2))
			require.NoError(t, tc.Client.Patch(tc.Ctx, changed, client.MergeFrom(initialized)))
			require.NoError(t, updateutils.WaitForGenerationHashChange(tc, *initialized.Status.CurrentGenerationHash))
			target, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			require.NotNil(t, target.Status.UpdateProgress)
			require.True(t, target.Status.UpdateProgress.RequiresReplicaRecreation)
			require.Nil(t, target.Status.UpdateProgress.UpdateEndedAt)
			require.NotEmpty(t, target.Status.UpdateProgress.TargetSchedulingConfigurationHash)
			require.NotEqual(t, *initialized.Status.AppliedSchedulingConfigurationHash, target.Status.UpdateProgress.TargetSchedulingConfigurationHash)
			require.Equal(t, initialized.Status.AppliedSchedulingConfigurationHash, target.Status.AppliedSchedulingConfigurationHash)

			require.EventuallyWithT(t, func(c *assert.CollectT) {
				require.NoError(c, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&beforePCLQ), &pclq))
				require.NoError(c, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&beforePCSG), &pcsg))
				pgm, err = updateutils.GetReplicaPodGangMap(tc, 0)
				require.NoError(c, err)
				assert.NotEqual(c, beforePCLQ.UID, pclq.UID)
				assert.NotEqual(c, beforePCSG.UID, pcsg.UID)
				assert.NotEqual(c, beforePGM.UID, pgm.UID)
				require.NotNil(c, pcsg.Spec.MinAvailable)
				assert.Equal(c, int32(2), *pcsg.Spec.MinAvailable)
				assert.Equal(c, int32(2), pcsg.Spec.Replicas)
				assert.Equal(c, *target.Status.CurrentGenerationHash, pclq.Labels[apicommon.LabelPodCliqueSetGenerationHash])
			}, tc.Timeout, tc.Interval)
			wantPodGangMap := updateutils.ExpectedReplicaPodGangMap{
				GenerationHash: *target.Status.CurrentGenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
				PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
			}
			updateutils.AssertReplicaPodGangMap(t, pgm.Spec.Entries, wantPodGangMap)
			pclqUID, pcsgUID, pgmUID := pclq.UID, pcsg.UID, pgm.UID
			blocked, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			require.NotNil(t, blocked.Status.UpdateProgress)
			require.Nil(t, blocked.Status.UpdateProgress.UpdateEndedAt)
			require.Equal(t, initialized.Status.AppliedSchedulingConfigurationHash, blocked.Status.AppliedSchedulingConfigurationHash)

			t.Log("release readiness and require completion at the new scheduling target")
			require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, stageName))
			updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
			require.NoError(t, tc.WaitForPods(10))
			require.NoError(t, updateutils.WaitForRollingUpdateComplete(tc, 1))
			completed, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			require.Equal(t, target.Status.CurrentGenerationHash, completed.Status.CurrentGenerationHash)
			require.Equal(t, new(target.Status.UpdateProgress.TargetSchedulingConfigurationHash), completed.Status.AppliedSchedulingConfigurationHash)
			require.Equal(t, int32(1), completed.Status.AvailableReplicas)
			require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&beforePCLQ), &pclq))
			require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&beforePCSG), &pcsg))
			pgm, err = updateutils.GetReplicaPodGangMap(tc, 0)
			require.NoError(t, err)
			require.Equal(t, pclqUID, pclq.UID)
			require.Equal(t, pcsgUID, pcsg.UID)
			require.Equal(t, pgmUID, pgm.UID)
			require.Equal(t, beforePCLQ.Spec.MinAvailable, pclq.Spec.MinAvailable)
			require.Equal(t, new(int32(2)), pcsg.Spec.MinAvailable)
			require.Equal(t, int32(2), pcsg.Spec.Replicas)
			updateutils.AssertReplicaPodGangMap(t, pgm.Spec.Entries, wantPodGangMap)
			updateutils.AssertGenerationHashConverged(tc)
			updateutils.AssertNoActiveChildUpdates(tc)
		},
	})
}

// Test_VUPG5_UpgradeDuringLegacyUpdatePreservesGeneration checks that a new operator finishes an
// update started by the old operator without changing its legacy target hash or restarting the update.
// The hash must also remain unchanged after completion and a later reconciliation.
//
// Scenario:
//  1. Install v0.2.1-beta1 and create a Ready workload under Coherent or RollingRecreate.
//     Give the frontend update enough time to remain unfinished during the operator upgrade.
//  2. Cordon the test workers and update the frontend template. Wait for new-revision pods to
//     remain unscheduled and for an active frontend update. Under Coherent, also require mixed
//     generations in the PodGangMap. Capture the legacy target hash and update progress.
//  3. Upgrade Grove without editing the workload. Wait for scheduling metadata to initialize and
//     for a new PCS label to reach the PodGangs, proving the new operator has reconciled the workload.
//     Verify the target hash, update start times, selected replica, and existing pods are preserved.
//  4. Uncordon the workers. Require completion at the same legacy target hash, with six Ready pods
//     and both frontend pods using the requested template. No child update may remain unfinished.
//  5. Change another label value and wait for its propagation. Verify the legacy hash, completed
//     update timestamps, and pods remain unchanged after this later reconciliation.
func Test_VUPG5_UpgradeDuringLegacyUpdatePreservesGeneration(t *testing.T) {
	const revision = "before-operator-upgrade"
	for _, strategy := range []grovev1beta1.UpdateStrategyType{grovev1beta1.CoherentStrategy, grovev1beta1.RollingRecreateStrategy} {
		t.Run(string(strategy), func(t *testing.T) {
			var beforePCS *grovev1beta1.PodCliqueSet
			var beforeFrontend *grovev1beta1.PodClique
			var beforePGM *grovev1beta1.PodGangMap
			var beforePods *corev1.PodList
			var workers []string
			runUpgradeTest(t, upgradeTest{
				fromVersion:     "v0.2.1-beta1",
				nodeWorkerCount: 10,
				prepareOpts: []testctx.TestOption{
					testctx.WithWorkload(&testctx.WorkloadConfig{
						Name: "workload-coherent", YAMLPath: "../../yaml/workload-coherent.yaml",
						Namespace: "default", ExpectedPods: 6,
					}),
					testctx.WithTimeout(defaultPollTimeout),
				},
				preUpgrade: func(t *testing.T, tc *testctx.TestContext) {
					_, err := tc.DeployAndVerifyWorkload()
					require.NoError(t, err)
					require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, strategy))
					pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
					require.NoError(t, err)
					original := pcs.DeepCopy()
					require.Equal(t, "frontend", pcs.Spec.Template.Cliques[0].Name)
					require.NotNil(t, pcs.Spec.Template.Cliques[0].RollingUpdate)
					pcs.Spec.Template.Cliques[0].RollingUpdate.ProgressDeadline = &metav1.Duration{Duration: 30 * time.Minute}
					require.NoError(t, tc.Client.Patch(tc.Ctx, pcs, client.MergeFrom(original)))
					initialHash := updateutils.PCSGenerationHash(t, tc)

					var nodes corev1.NodeList
					require.NoError(t, tc.Client.List(tc.Ctx, &nodes, client.MatchingLabels{setup.WorkerNodeLabelKey: setup.WorkerNodeLabelValue}))
					for _, node := range nodes.Items {
						if !node.Spec.Unschedulable {
							workers = append(workers, node.Name)
						}
					}
					require.NotEmpty(t, workers)
					t.Cleanup(func() {
						ctx, cancel := context.WithTimeout(context.WithoutCancel(tc.Ctx), tc.Timeout)
						defer cancel()
						cleanupTC := *tc
						cleanupTC.Ctx = ctx
						for _, name := range workers {
							assert.NoError(t, cleanupTC.UncordonNode(name))
						}
					})
					tc.CordonNodes(workers)
					require.NoError(t, updateutils.TriggerPodCliqueUpdateWithRevision(tc, revision, "frontend"))
					require.NoError(t, updateutils.WaitForOrdinalUpdating(tc, 0))
					if strategy == grovev1beta1.CoherentStrategy {
						updateutils.WaitForReplicaMidCoherentRoll(t, tc, 0)
					}
					frontendKey := client.ObjectKey{Namespace: tc.Namespace, Name: updateutils.StandaloneCliqueFQN(tc, "frontend")}
					require.EventuallyWithT(t, func(c *assert.CollectT) {
						var frontend grovev1beta1.PodClique
						require.NoError(c, tc.Client.Get(tc.Ctx, frontendKey, &frontend))
						require.NotNil(c, frontend.Status.UpdateProgress)
						require.Nil(c, frontend.Status.UpdateProgress.UpdateEndedAt)
						require.Zero(c, frontend.Status.UpdateProgress.UpdatedScheduledReplicas)
						var frontendPods corev1.PodList
						require.NoError(c, tc.Client.List(tc.Ctx, &frontendPods, client.InNamespace(tc.Namespace),
							client.MatchingLabels{apicommon.LabelPodClique: frontend.Name}))
						require.Len(c, frontendPods.Items, 2)
						updatedPods := 0
						for _, pod := range frontendPods.Items {
							require.Nil(c, pod.DeletionTimestamp)
							require.Len(c, pod.Spec.Containers, 1)
							for _, env := range pod.Spec.Containers[0].Env {
								if env.Name == "UPDATE_TRIGGER" && env.Value == revision {
									assert.Empty(c, pod.Spec.NodeName, "the replacement must remain unscheduled")
									updatedPods++
								}
							}
						}
						require.Positive(c, updatedPods, "the old operator must have created unscheduled replacement pods")
						beforeFrontend = frontend.DeepCopy()
					}, tc.Timeout, tc.Interval)
					beforePCS, err = updateutils.GetPCS(tc, tc.Workload.Name)
					require.NoError(t, err)
					require.NotNil(t, beforePCS.Status.CurrentGenerationHash)
					require.NotEqual(t, initialHash, *beforePCS.Status.CurrentGenerationHash)
					require.Nil(t, beforePCS.Status.AppliedSchedulingConfigurationHash)
					require.NotNil(t, beforePCS.Status.UpdateProgress)
					require.Nil(t, beforePCS.Status.UpdateProgress.UpdateEndedAt)
					require.Empty(t, beforePCS.Status.UpdateProgress.TargetSchedulingConfigurationHash)
					require.Len(t, beforePCS.Status.UpdateProgress.CurrentlyUpdating, 1)
					require.Equal(t, int32(0), beforePCS.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
					require.Nil(t, beforePCS.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt)
					require.Nil(t, beforePCS.Status.UpdateProgress.CurrentlyUpdating[0].Replacement)
					require.Equal(t, *beforePCS.Status.CurrentGenerationHash, beforeFrontend.Status.UpdateProgress.PodCliqueSetGenerationHash)
					if strategy == grovev1beta1.CoherentStrategy {
						require.NotEmpty(t, beforePCS.Status.UpdateProgress.CurrentlyUpdating[0].InFlightEpochs)
					}
					beforePGM, err = updateutils.GetReplicaPodGangMap(tc, 0)
					require.NoError(t, err)
					beforePods, err = tc.ListPods()
					require.NoError(t, err)
					require.Len(t, beforePods.Items, 6)
				},
				postUpgrade: func(t *testing.T, tc *testctx.TestContext) {
					// An unfinished update need not have observed its spec generation yet.
					require.EventuallyWithT(t, func(c *assert.CollectT) {
						pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
						require.NoError(c, err)
						require.NotNil(c, pcs.Status.AppliedSchedulingConfigurationHash)
						require.NotNil(c, pcs.Status.UpdateProgress)
						assert.Equal(c, *pcs.Status.AppliedSchedulingConfigurationHash, pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash)
					}, tc.Timeout, tc.Interval)
					requirePCSLabelPropagation(t, tc, "while-held")
					resumed, err := updateutils.GetPCS(tc, tc.Workload.Name)
					require.NoError(t, err)
					assertLegacyUpdateIdentity(t, beforePCS, resumed)
					require.Nil(t, resumed.Status.UpdateProgress.UpdateEndedAt)
					require.Equal(t, beforePCS.Status.UpdateProgress.CurrentlyUpdating, resumed.Status.UpdateProgress.CurrentlyUpdating)
					require.Equal(t, beforePCS.Status.UpdateProgress.InScopeStandalonePodCliques, resumed.Status.UpdateProgress.InScopeStandalonePodCliques)
					require.Equal(t, beforePCS.Status.UpdateProgress.InScopePodCliqueScalingGroups, resumed.Status.UpdateProgress.InScopePodCliqueScalingGroups)
					var frontend grovev1beta1.PodClique
					require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(beforeFrontend), &frontend))
					require.Equal(t, beforeFrontend.UID, frontend.UID)
					require.NotNil(t, frontend.Status.UpdateProgress)
					require.Nil(t, frontend.Status.UpdateProgress.UpdateEndedAt)
					require.Equal(t, beforeFrontend.Status.UpdateProgress.UpdateStartedAt, frontend.Status.UpdateProgress.UpdateStartedAt)
					require.Equal(t, *beforePCS.Status.CurrentGenerationHash, frontend.Status.UpdateProgress.PodCliqueSetGenerationHash)
					pgm, err := updateutils.GetReplicaPodGangMap(tc, 0)
					require.NoError(t, err)
					require.Equal(t, beforePGM.UID, pgm.UID)
					verifyPodUIDsUnchanged(t, tc, beforePods)

					tc.UncordonNodes(workers)
					require.NoError(t, updateutils.WaitForRollingUpdateComplete(tc, 1))
					require.NoError(t, tc.WaitForPods(6))
					completed, err := updateutils.GetPCS(tc, tc.Workload.Name)
					require.NoError(t, err)
					assertLegacyUpdateIdentity(t, beforePCS, completed)
					require.Equal(t, resumed.Status.AppliedSchedulingConfigurationHash, completed.Status.AppliedSchedulingConfigurationHash)
					updateutils.AssertAffectedPodsAtRevision(tc, "frontend", revision)
					updateutils.AssertGenerationHashConverged(tc)
					updateutils.AssertNoActiveChildUpdates(tc)
					updateutils.AssertPodGangMapSingleGeneration(t, tc)
					completedPods, err := tc.ListPods()
					require.NoError(t, err)

					requirePCSLabelPropagation(t, tc, "after-completion")
					stable, err := updateutils.GetPCS(tc, tc.Workload.Name)
					require.NoError(t, err)
					assertLegacyUpdateIdentity(t, beforePCS, stable)
					require.Equal(t, completed.Status.UpdateProgress.UpdateEndedAt, stable.Status.UpdateProgress.UpdateEndedAt)
					require.Equal(t, completed.Status.AppliedSchedulingConfigurationHash, stable.Status.AppliedSchedulingConfigurationHash)
					verifyPodUIDsUnchanged(t, tc, completedPods)
					updateutils.AssertGenerationHashConverged(tc)
					updateutils.AssertNoActiveChildUpdates(tc)
					updateutils.AssertPodGangMapSingleGeneration(t, tc)
				},
			})
		})
	}
}

// assertLegacyUpdateIdentity checks the unchanged workload and the update identity created by the old operator.
// Scheduling metadata and completion progress may change, but the legacy target and start time must not.
func assertLegacyUpdateIdentity(t *testing.T, before, after *grovev1beta1.PodCliqueSet) {
	t.Helper()
	require.Equal(t, before.UID, after.UID)
	require.Equal(t, before.Generation, after.Generation)
	require.Equal(t, before.Spec, after.Spec)
	require.Equal(t, before.Status.CurrentGenerationHash, after.Status.CurrentGenerationHash)
	require.NotNil(t, after.Status.UpdateProgress)
	require.Equal(t, before.Status.UpdateProgress.UpdateStartedAt, after.Status.UpdateProgress.UpdateStartedAt)
	require.Equal(t, before.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash, after.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash)
	require.False(t, after.Status.UpdateProgress.RequiresReplicaRecreation)
}

// requirePCSLabelPropagation applies a label and waits until every PodGang carries it.
// PodGang synchronization follows replica update planning, so this proves the new operator has reconciled the PCS.
func requirePCSLabelPropagation(t *testing.T, tc *testctx.TestContext, value string) {
	t.Helper()
	const key = "e2e.grove.io/upgrade-check"
	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, key, value))
	require.NoError(t, tc.Client.Patch(tc.Ctx, pcs, client.RawPatch(types.MergePatchType, patch)))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var podGangs groveschedulerv1alpha1.PodGangList
		require.NoError(c, tc.Client.List(tc.Ctx, &podGangs, client.InNamespace(tc.Namespace),
			client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}))
		require.NotEmpty(c, podGangs.Items)
		for _, podGang := range podGangs.Items {
			assert.Equal(c, value, podGang.Labels[key], "PodGang %s must observe the new PCS label", podGang.Name)
		}
	}, tc.Timeout, tc.Interval)
}

// waitForSchedulingBaseline waits for the upgraded operator to initialize and observe the PCS.
func waitForSchedulingBaseline(t *testing.T, tc *testctx.TestContext) *grovev1beta1.PodCliqueSet {
	t.Helper()
	var pcs *grovev1beta1.PodCliqueSet
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		pcs, err = updateutils.GetPCS(tc, tc.Workload.Name)
		require.NoError(c, err)
		require.NotNil(c, pcs.Status.AppliedSchedulingConfigurationHash)
		require.NotNil(c, pcs.Status.ObservedGeneration)
		assert.Equal(c, pcs.Generation, *pcs.Status.ObservedGeneration)
	}, tc.Timeout, tc.Interval)
	return pcs
}

// assertLegacyPCSUnchanged checks that baseline initialization and child scaling do not rewrite the PCS.
func assertLegacyPCSUnchanged(t *testing.T, before, after *grovev1beta1.PodCliqueSet) {
	t.Helper()
	assert.Equal(t, before.UID, after.UID)
	assert.Equal(t, before.Generation, after.Generation)
	assert.Equal(t, before.Spec, after.Spec)
	assert.Equal(t, before.Status.CurrentGenerationHash, after.Status.CurrentGenerationHash)
	assert.Equal(t, before.Status.UpdateProgress, after.Status.UpdateProgress)
}

// standaloneAnchorInventory returns the committed pod count and anchor epochs for a standalone clique.
func standaloneAnchorInventory(entries []grovev1beta1.PodGangEntry, cliqueName string) (int32, []string) {
	var podCount int32
	var epochs []string
	for _, entry := range entries {
		if entry.Role == grovev1beta1.PodGangEntryRoleAnchor && entry.PodCliques[cliqueName] > 0 {
			podCount += entry.PodCliques[cliqueName]
			epochs = append(epochs, entry.Epoch)
		}
	}
	return podCount, epochs
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
