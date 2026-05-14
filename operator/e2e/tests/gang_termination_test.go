//go:build e2e

// /*
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
// */

package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// terminationDelayInWorkloadYAML mirrors spec.template.terminationDelay in
// operator/e2e/yaml/workload{1,2}-gt.yaml. Tests sleep past this value to
// give the gang-termination flow a chance to fire (or to confirm it didn't).
const terminationDelayInWorkloadYAML = 10 * time.Second

// gangTerminationGrace is how long we wait past terminationDelay before
// asserting outcomes. Adds slack for reconcile + apiserver latency.
const gangTerminationGrace = 15 * time.Second

// totalWL1Pods: pc-a(2) + sg-x[2]*(pc-b(1)+pc-c(3)) = 2 + 2*4 = 10
// totalWL2Pods: same shape (counts are identical, only minAvailable differs)
const totalWLPods = 10

// Test_GT1_GangTerminationFullReplicasPCSOwned: WL1 (full replicas required).
// Cordon a node and kill a ready pod from the PCS-owned PodClique pc-a (min=2,
// replicas=2). Killing 1 leaves 1 < min=2 → MinAvailableBreached=True on pc-a.
// After TerminationDelay the PCS-level gang-termination flow must delete the
// entire PCS replica.
func Test_GT1_GangTerminationFullReplicasPCSOwned(t *testing.T) {
	runFullReplicasScenario(t, "workload1-gt", "workload1-gt.yaml", "workload1-gt-0-pc-a")
}

// Test_GT2_GangTerminationFullReplicasPCSGOwned: WL1, kill a ready pod from a
// PCSG-owned PodClique (sg-x-0-pc-c: min=3, replicas=3). The PCLQ breaches,
// the PCSG sg-x replica 0 breaches via MinAvailableBreached propagation, and
// the PCS-level gang-termination flow takes the whole PCS replica.
func Test_GT2_GangTerminationFullReplicasPCSGOwned(t *testing.T) {
	runFullReplicasScenario(t, "workload1-gt", "workload1-gt.yaml", "workload1-gt-0-sg-x-0-pc-c")
}

// runFullReplicasScenario is the shared body for GT-1 and GT-2. It only differs
// in which PodClique to target.
func runFullReplicasScenario(t *testing.T, workloadName, yamlFile, targetCliqueFQN string) {
	ctx := context.Background()

	Logger.Infof("1. Initialize a %d-node Grove cluster (target clique: %s)", totalWLPods, targetCliqueFQN)
	tc, cleanup := testctx.PrepareTest(ctx, t, totalWLPods,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         workloadName,
			YAMLPath:     "../yaml/" + yamlFile,
			Namespace:    "default",
			ExpectedPods: totalWLPods,
		}),
	)
	defer cleanup()

	Logger.Infof("2. Deploy %s and wait for all %d pods to become ready", workloadName, totalWLPods)
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	if err := tc.WaitForReadyPods(totalWLPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	Logger.Infof("3. Snapshot pod UIDs, cordon a node hosting a ready pod from %s, then delete that pod", targetCliqueFQN)
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	target := findReadyPodFromPodClique(pods, targetCliqueFQN)
	if target == nil {
		dumpPodsByClique(t, pods)
		t.Fatalf("no ready pod found for clique %s", targetCliqueFQN)
	}
	originalUIDs := capturePodUIDs(pods)

	if err := tc.CordonNode(target.Spec.NodeName); err != nil {
		t.Fatalf("Failed to cordon node %s: %v", target.Spec.NodeName, err)
	}
	if err := tc.Client.Delete(ctx, target); err != nil {
		t.Fatalf("Failed to delete pod %s: %v", target.Name, err)
	}

	Logger.Infof("4. Wait %s (terminationDelay + grace) for gang termination to fire", terminationDelayInWorkloadYAML+gangTerminationGrace)
	time.Sleep(terminationDelayInWorkloadYAML + gangTerminationGrace)

	Logger.Info("5. Verify all PCS-replica pods got recreated (zero original UIDs survive)")
	verifyAllPodsRecreated(t, tc, originalUIDs, totalWLPods)
}

// Test_GT3_GangTerminationMinReplicasPCSOwned: WL2 (min replicas allow
// partial). Kill one ready pod from pc-a (min=1, replicas=2) — surviving pod
// keeps min satisfied, expect NO gang termination. Kill the remaining ready
// pod (scheduled now 0) — under the always-breach rule the breach flips True
// and gang termination fires after TerminationDelay.
func Test_GT3_GangTerminationMinReplicasPCSOwned(t *testing.T) {
	ctx := context.Background()
	workloadName := "workload2-gt"
	targetClique := "workload2-gt-0-pc-a"

	Logger.Infof("1. Initialize a %d-node Grove cluster for WL2 / PCS-owned min-replicas scenario", totalWLPods)
	tc, cleanup := testctx.PrepareTest(ctx, t, totalWLPods,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         workloadName,
			YAMLPath:     "../yaml/workload2-gt.yaml",
			Namespace:    "default",
			ExpectedPods: totalWLPods,
		}),
	)
	defer cleanup()

	Logger.Info("2. Deploy WL2 and wait for all pods to become ready")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	if err := tc.WaitForReadyPods(totalWLPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	Logger.Info("3. Kill 1 ready pod from pc-a (min=1, replicas=2) — expect NO gang termination")
	survivorsAfterFirstKill := cordonAndKillPodsFromClique(ctx, t, tc, targetClique, 1)
	time.Sleep(terminationDelayInWorkloadYAML + gangTerminationGrace)
	verifyNoGangTermination(t, tc, survivorsAfterFirstKill, totalWLPods-1)

	Logger.Info("4. Kill the remaining ready pod from pc-a (scheduled now 0) — test plan expects all pods terminated")
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	originalUIDs := capturePodUIDs(pods)
	cordonAndKillPodsFromClique(ctx, t, tc, targetClique, 1)
	time.Sleep(terminationDelayInWorkloadYAML + gangTerminationGrace)

	Logger.Info("5. Verify all PCS-replica pods got recreated")
	verifyAllPodsRecreated(t, tc, originalUIDs, totalWLPods)
}

// Test_GT4_GangTerminationMinReplicasPCSGOwned: WL2, multi-step against
// PCSG-owned pc-c (min=1, replicas=3) across both PCSG replicas. Steps mirror
// the test plan. Under the always-breach rule, killing all pods of a PCSG
// replica's pc-c PCLQ flips MinAvailableBreached=True; PCSG-level gang
// termination handles the per-replica case, PCS-level handles the final
// "both PCSG replicas down" case.
func Test_GT4_GangTerminationMinReplicasPCSGOwned(t *testing.T) {
	ctx := context.Background()
	workloadName := "workload2-gt"
	pcsg0Target := "workload2-gt-0-sg-x-0-pc-c"
	pcsg1Target := "workload2-gt-0-sg-x-1-pc-c"

	Logger.Infof("1. Initialize a %d-node Grove cluster for WL2 / PCSG-owned min-replicas scenario", totalWLPods)
	tc, cleanup := testctx.PrepareTest(ctx, t, totalWLPods,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         workloadName,
			YAMLPath:     "../yaml/workload2-gt.yaml",
			Namespace:    "default",
			ExpectedPods: totalWLPods,
		}),
	)
	defer cleanup()

	Logger.Info("2. Deploy WL2 and wait for all pods to become ready")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	if err := tc.WaitForReadyPods(totalWLPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	Logger.Info("3. Kill 1 ready pod from sg-x-0-pc-c (min=1, replicas=3) — expect NO gang termination")
	survivorsA := cordonAndKillPodsFromClique(ctx, t, tc, pcsg0Target, 1)
	time.Sleep(terminationDelayInWorkloadYAML + gangTerminationGrace)
	verifyNoGangTermination(t, tc, survivorsA, totalWLPods-1)

	Logger.Info("4. Kill the remaining 2 ready pods from sg-x-0-pc-c — test plan expects PCSG-0 (pc-b-0 + pc-c-0) recreated, workload not gang-terminated")
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	pcsg0OriginalUIDs := capturePodUIDsForPCSGReplica(pods, "0")
	pcsg1OriginalUIDs := capturePodUIDsForPCSGReplica(pods, "1")
	pcAOriginalUIDs := capturePodUIDsForClique(pods, "workload2-gt-0-pc-a")
	cordonAndKillPodsFromClique(ctx, t, tc, pcsg0Target, 2)
	time.Sleep(terminationDelayInWorkloadYAML + gangTerminationGrace)
	// Verify only PCSG-0 pods got recreated; PCSG-1 + pc-a survived intact.
	verifyPCSGReplicaRecreatedOnly(t, tc, "0", pcsg0OriginalUIDs, pcsg1OriginalUIDs, pcAOriginalUIDs)

	Logger.Info("5. Kill 1 ready pod from sg-x-1-pc-c — expect NO PCS-level gang termination (pc-a must survive)")
	// PCSG-0 is still cycling (PCSG-replica-scoped restart fires every TerminationDelay
	// because its replacement pods stay Pending on cordoned nodes), so we cannot assert
	// on PCSG-0's UIDs. The signal that the workload is NOT PCS-gang-terminated is that
	// pc-a (the standalone PCLQ) keeps its UIDs — only PCS-level termination would
	// delete pc-a.
	pods, err = tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	pcAUIDsBeforeStep5 := capturePodUIDsForClique(pods, "workload2-gt-0-pc-a")
	cordonAndKillPodsFromClique(ctx, t, tc, pcsg1Target, 1)
	time.Sleep(terminationDelayInWorkloadYAML + gangTerminationGrace)
	verifyNoGangTermination(t, tc, pcAUIDsBeforeStep5, 0)

	Logger.Info("6. Kill the remaining 2 ready pods from sg-x-1-pc-c — test plan expects all PCS pods terminated")
	pods, err = tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	finalOriginalUIDs := capturePodUIDs(pods)
	cordonAndKillPodsFromClique(ctx, t, tc, pcsg1Target, 2)
	time.Sleep(terminationDelayInWorkloadYAML + gangTerminationGrace)
	verifyAllPodsRecreated(t, tc, finalOriginalUIDs, totalWLPods)
}

// Test_GT5_IndividualPCSGReplicaTermination: WL5 (PCS pc-a min=1 r=2;
// sg-x min=1 r=2 of pc-b min=1 r=1, pc-c min=3 r=3). Kill all 3 pods of one
// PCSG replica's pc-c, breaching that replica's MinAvailable. Only that PCSG
// replica's PCLQs should be recreated; the other PCSG replica + pc-a survive.
//
// Differs from GT-4 step 4 in that pc-c here has min=3 (no tolerance), so
// killing even one pod breaches pc-c immediately — we kill all 3 at once.
func Test_GT5_IndividualPCSGReplicaTermination(t *testing.T) {
	ctx := context.Background()
	workloadName := "workload5-gt"
	pcsg0Target := "workload5-gt-0-sg-x-0-pc-c"

	Logger.Infof("1. Initialize a %d-node Grove cluster for WL5 scenario", totalWLPods)
	tc, cleanup := testctx.PrepareTest(ctx, t, totalWLPods,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         workloadName,
			YAMLPath:     "../yaml/workload5-gt.yaml",
			Namespace:    "default",
			ExpectedPods: totalWLPods,
		}),
	)
	defer cleanup()

	Logger.Info("2. Deploy WL5 and wait for all pods to become ready")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	if err := tc.WaitForReadyPods(totalWLPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	Logger.Info("3. Snapshot UIDs by section before kill")
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	pcsg0OriginalUIDs := capturePodUIDsForPCSGReplica(pods, "0")
	pcsg1OriginalUIDs := capturePodUIDsForPCSGReplica(pods, "1")
	pcAOriginalUIDs := capturePodUIDsForClique(pods, "workload5-gt-0-pc-a")

	Logger.Infof("4. Kill all 3 pods from %s — should breach only that PCSG replica", pcsg0Target)
	cordonAndKillPodsFromClique(ctx, t, tc, pcsg0Target, 3)
	time.Sleep(terminationDelayInWorkloadYAML + gangTerminationGrace)

	Logger.Info("5. Verify only PCSG-0 was recreated; PCSG-1 and pc-a kept their UIDs")
	verifyPCSGReplicaRecreatedOnly(t, tc, "0", pcsg0OriginalUIDs, pcsg1OriginalUIDs, pcAOriginalUIDs)
}

// Test_GT6_ScaledPodGangPodDeletion: WL2 (pc-c min=1 r=3 tolerates 2 pod
// losses). Find a pod in a "scaled" PodGang (one labeled with
// grove.io/base-podgang pointing back at the PCS replica's base PodGang),
// delete it. MinAvailable is still satisfied so no breach fires — the
// controller recreates the pod and the workload keeps running.
func Test_GT6_ScaledPodGangPodDeletion(t *testing.T) {
	ctx := context.Background()
	workloadName := "workload2-gt"

	Logger.Infof("1. Initialize a %d-node Grove cluster for WL2 scaled-pod scenario", totalWLPods)
	tc, cleanup := testctx.PrepareTest(ctx, t, totalWLPods,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         workloadName,
			YAMLPath:     "../yaml/workload2-gt.yaml",
			Namespace:    "default",
			ExpectedPods: totalWLPods,
		}),
	)
	defer cleanup()

	Logger.Info("2. Deploy WL2 and wait for all pods to become ready")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	if err := tc.WaitForReadyPods(totalWLPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	Logger.Info("3. Find a scaled pc-c pod (one with grove.io/base-podgang and clique=pc-c so killing it stays within pc-c's min=1 tolerance)")
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	scaled := findReadyScaledPCCPod(pods, "workload2-gt")
	if scaled == nil {
		dumpPodsByClique(t, pods)
		t.Fatalf("no ready pc-c pod with the %s label (scaled PodGang) found", apicommon.LabelBasePodGang)
	}
	pcAUIDs := capturePodUIDsForClique(pods, "workload2-gt-0-pc-a")

	Logger.Infof("4. Cordon node %s and delete pod %s (clique=%s, base-podgang=%s)",
		scaled.Spec.NodeName, scaled.Name, scaled.Labels[apicommon.LabelPodClique], scaled.Labels[apicommon.LabelBasePodGang])
	if err := tc.CordonNode(scaled.Spec.NodeName); err != nil {
		t.Fatalf("Failed to cordon node: %v", err)
	}
	if err := tc.Client.Delete(ctx, scaled); err != nil {
		t.Fatalf("Failed to delete pod: %v", err)
	}

	Logger.Infof("5. Wait %s — base PodGang min still met, no PCS-level gang term should fire", terminationDelayInWorkloadYAML+gangTerminationGrace)
	time.Sleep(terminationDelayInWorkloadYAML + gangTerminationGrace)

	Logger.Info("6. Verify pc-a (standalone PCLQ) kept its UIDs — only PCS-level gang term would delete pc-a")
	verifyNoGangTermination(t, tc, pcAUIDs, 0)
}

// ---------- helpers ----------

// findReadyPodFromPodClique returns the first ready pod whose grove.io/podclique
// label equals cliqueFQN, or nil if none found.
func findReadyPodFromPodClique(pods *corev1.PodList, cliqueFQN string) *corev1.Pod {
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Labels[apicommon.LabelPodClique] != cliqueFQN {
			continue
		}
		if !isPodReady(p) {
			continue
		}
		return p
	}
	return nil
}

// findReadyScaledPCCPod returns the first ready pc-c pod that has a base-podgang
// label set (i.e. lives in a scaled PodGang). Used by GT-6: pc-c has min=1
// replicas=3 so killing one of these doesn't breach the PCLQ, which lets us
// observe the "scaled pod recreated, no full-workload gang term" behaviour
// without triggering a PCSG-scoped restart as a side effect.
func findReadyScaledPCCPod(pods *corev1.PodList, workloadName string) *corev1.Pod {
	for i := range pods.Items {
		p := &pods.Items[i]
		if v, ok := p.Labels[apicommon.LabelBasePodGang]; !ok || v == "" {
			continue
		}
		clique := p.Labels[apicommon.LabelPodClique]
		// pc-c PCLQs in either sg-x replica end with "-pc-c"; this avoids
		// pc-a-1 (scaled standalone PCLQ pod) which has no min-tolerance.
		if clique != workloadName+"-0-sg-x-0-pc-c" && clique != workloadName+"-0-sg-x-1-pc-c" {
			continue
		}
		if !isPodReady(p) {
			continue
		}
		return p
	}
	return nil
}

// findReadyPodsFromPodClique returns up to n ready pods from cliqueFQN.
func findReadyPodsFromPodClique(pods *corev1.PodList, cliqueFQN string, n int) []*corev1.Pod {
	out := make([]*corev1.Pod, 0, n)
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Labels[apicommon.LabelPodClique] != cliqueFQN {
			continue
		}
		if !isPodReady(p) {
			continue
		}
		out = append(out, p)
		if len(out) >= n {
			break
		}
	}
	return out
}

// isPodReady returns true when the pod has PodReady=True.
func isPodReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// capturePodUIDs snapshots every pod's UID for later survivor checks.
func capturePodUIDs(pods *corev1.PodList) map[types.UID]struct{} {
	out := make(map[types.UID]struct{}, len(pods.Items))
	for _, p := range pods.Items {
		out[p.UID] = struct{}{}
	}
	return out
}

// capturePodUIDsForClique returns UIDs of pods belonging to a specific PodClique.
func capturePodUIDsForClique(pods *corev1.PodList, cliqueFQN string) map[types.UID]struct{} {
	out := make(map[types.UID]struct{})
	for _, p := range pods.Items {
		if p.Labels[apicommon.LabelPodClique] == cliqueFQN {
			out[p.UID] = struct{}{}
		}
	}
	return out
}

// capturePodUIDsForPCSGReplica returns UIDs of pods belonging to a specific
// PodCliqueScalingGroup replica index.
func capturePodUIDsForPCSGReplica(pods *corev1.PodList, pcsgReplicaIndex string) map[types.UID]struct{} {
	out := make(map[types.UID]struct{})
	for _, p := range pods.Items {
		if p.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex] == pcsgReplicaIndex {
			out[p.UID] = struct{}{}
		}
	}
	return out
}

// cordonAndKillPodsFromClique picks n ready pods from cliqueFQN, cordons their
// nodes (so the replacement pods cannot reschedule), and deletes them. Returns
// the UIDs that survived in case the caller wants to assert on them.
func cordonAndKillPodsFromClique(ctx context.Context, t *testing.T, tc *testctx.TestContext, cliqueFQN string, n int) map[types.UID]struct{} {
	t.Helper()
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	targets := findReadyPodsFromPodClique(pods, cliqueFQN, n)
	if len(targets) < n {
		dumpPodsByClique(t, pods)
		t.Fatalf("expected %d ready pods from %s, found %d", n, cliqueFQN, len(targets))
	}

	killedUIDs := make(map[types.UID]struct{}, n)
	for _, p := range targets {
		if err := tc.CordonNode(p.Spec.NodeName); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", p.Spec.NodeName, err)
		}
		if err := tc.Client.Delete(ctx, p); err != nil {
			t.Fatalf("Failed to delete pod %s: %v", p.Name, err)
		}
		killedUIDs[p.UID] = struct{}{}
		Logger.Debugf("cordoned node %s and deleted pod %s (clique=%s)", p.Spec.NodeName, p.Name, cliqueFQN)
	}

	survivors := make(map[types.UID]struct{}, len(pods.Items)-n)
	for _, p := range pods.Items {
		if _, k := killedUIDs[p.UID]; k {
			continue
		}
		survivors[p.UID] = struct{}{}
	}
	return survivors
}

// dumpPodsByClique writes one log line per pod with clique/phase/ready info,
// for diagnostics when a "find ready pod" call fails.
func dumpPodsByClique(t *testing.T, pods *corev1.PodList) {
	t.Helper()
	for i := range pods.Items {
		p := &pods.Items[i]
		t.Logf("  pod=%s clique=%s phase=%s ready=%v", p.Name, p.Labels[apicommon.LabelPodClique], p.Status.Phase, isPodReady(p))
	}
}

// verifyAllPodsRecreated polls until every UID in originalUIDs is absent from
// the workload's current pods AND the workload has exactly expectedPods
// non-terminating pods. Strong signal that PCS-level gang termination fired.
func verifyAllPodsRecreated(t *testing.T, tc *testctx.TestContext, originalUIDs map[types.UID]struct{}, expectedPods int) {
	t.Helper()
	deadline := time.Now().Add(tc.Timeout)
	var lastErr string
	for time.Now().Before(deadline) {
		pods, err := tc.ListPods()
		if err != nil {
			t.Fatalf("Failed to list pods during recreate check: %v", err)
		}
		survivors, nonTerminating := 0, 0
		for _, p := range pods.Items {
			if _, ok := originalUIDs[p.UID]; ok {
				survivors++
			}
			if p.DeletionTimestamp == nil {
				nonTerminating++
			}
		}
		if survivors == 0 && nonTerminating == expectedPods {
			Logger.Infof("Gang termination confirmed: 0/%d original UIDs survive, %d non-terminating pods", len(originalUIDs), nonTerminating)
			return
		}
		lastErr = fmt.Sprintf("survivors=%d non-terminating=%d (want survivors=0 non-terminating=%d)", survivors, nonTerminating, expectedPods)
		time.Sleep(tc.Interval)
	}
	t.Fatalf("Gang termination did not occur within %s — final state: %s", tc.Timeout, lastErr)
}

// verifyNoGangTermination asserts that the workload did NOT gang-terminate by
// confirming all expected survivors are still present after the wait. Used for
// the "min-replicas not yet breached" steps.
func verifyNoGangTermination(t *testing.T, tc *testctx.TestContext, expectedSurvivors map[types.UID]struct{}, minRunning int) {
	t.Helper()
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods during no-gang-term check: %v", err)
	}
	currentUIDs := make(map[types.UID]struct{}, len(pods.Items))
	running := 0
	for _, p := range pods.Items {
		currentUIDs[p.UID] = struct{}{}
		if p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
			running++
		}
	}
	survived := 0
	for uid := range expectedSurvivors {
		if _, ok := currentUIDs[uid]; ok {
			survived++
		}
	}
	if survived != len(expectedSurvivors) {
		t.Fatalf("expected gang termination to NOT have fired: %d/%d expected-survivor UIDs are missing", len(expectedSurvivors)-survived, len(expectedSurvivors))
	}
	if running < minRunning {
		t.Fatalf("expected at least %d running pods (no gang termination), got %d", minRunning, running)
	}
	Logger.Infof("No gang termination confirmed: %d/%d expected-survivor UIDs present, %d running", survived, len(expectedSurvivors), running)
}

// verifyPCSGReplicaRecreatedOnly asserts that only the pods of the specified
// PCSG replica got new UIDs while the other PCSG replica and standalone PCLQ
// (pc-a) UIDs remained intact. Used to validate PCSG-level gang termination
// scoped to a single replica.
func verifyPCSGReplicaRecreatedOnly(t *testing.T, tc *testctx.TestContext, pcsgReplicaIndex string, originalPCSG0 map[types.UID]struct{}, originalPCSG1 map[types.UID]struct{}, originalPCA map[types.UID]struct{}) {
	t.Helper()
	deadline := time.Now().Add(tc.Timeout)
	for time.Now().Before(deadline) {
		pods, err := tc.ListPods()
		if err != nil {
			t.Fatalf("Failed to list pods during PCSG-replica check: %v", err)
		}
		currentByPCSGIdx := map[string]map[types.UID]struct{}{}
		currentPCA := map[types.UID]struct{}{}
		for _, p := range pods.Items {
			if p.DeletionTimestamp != nil {
				continue
			}
			if idx, ok := p.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex]; ok {
				if currentByPCSGIdx[idx] == nil {
					currentByPCSGIdx[idx] = map[types.UID]struct{}{}
				}
				currentByPCSGIdx[idx][p.UID] = struct{}{}
				continue
			}
			currentPCA[p.UID] = struct{}{}
		}

		targetRecreated := overlap(currentByPCSGIdx[pcsgReplicaIndex], originalPCSG0) == 0
		otherIntact := overlap(currentByPCSGIdx[otherIndex(pcsgReplicaIndex)], originalPCSG1) == len(originalPCSG1)
		pcAIntact := overlap(currentPCA, originalPCA) == len(originalPCA)

		if targetRecreated && otherIntact && pcAIntact {
			Logger.Infof("PCSG replica %s recreated; PCSG replica %s and pc-a intact", pcsgReplicaIndex, otherIndex(pcsgReplicaIndex))
			return
		}
		time.Sleep(tc.Interval)
	}
	t.Fatalf("expected PCSG replica %s to be the only recreated section; other PCSG replica or pc-a was also disturbed", pcsgReplicaIndex)
}

// otherIndex returns the other PCSG replica index for the two-replica case.
func otherIndex(idx string) string {
	if idx == "0" {
		return "1"
	}
	return "0"
}

// overlap returns the number of UIDs present in both maps.
func overlap(a, b map[types.UID]struct{}) int {
	n := 0
	for uid := range a {
		if _, ok := b[uid]; ok {
			n++
		}
	}
	return n
}
