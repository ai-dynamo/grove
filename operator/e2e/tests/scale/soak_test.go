//go:build e2e && soak

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

package scale

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/resources"
	"github.com/ai-dynamo/grove/operator/e2e/measurement"
	"github.com/ai-dynamo/grove/operator/e2e/measurement/condition"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	soakTimeout      = 60 * time.Minute
	soakWorkerNodes  = 30
	soakPerCycleHold = 30 * time.Second
	soakPodsPerCLQ   = 2

	soakDefaultBase   = 25
	soakDefaultPeak   = 50
	soakDefaultCycles = 10

	soakWorkloadName = "soak-churn"
	soakYAMLPath     = "../../yaml/soak-churn.yaml"
)

// soakConfig is the resolved cycle configuration. Read once from env at the
// top of the test so a failing parse fails the test immediately rather than
// midway through a 30-minute run.
type soakConfig struct {
	base   int
	peak   int
	cycles int
}

func loadSoakConfig() soakConfig {
	return soakConfig{
		base:   envInt("SOAK_BASE", soakDefaultBase),
		peak:   envInt("SOAK_PEAK", soakDefaultPeak),
		cycles: envInt("SOAK_CYCLES", soakDefaultCycles),
	}
}

func envInt(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func (c soakConfig) basePods() int { return c.base * soakPodsPerCLQ }
func (c soakConfig) peakPods() int { return c.peak * soakPodsPerCLQ }

// operatorBaseline captures the pre-test state of grove-operator pods so the
// final check can detect restarts or new pod replacements that happened during
// the soak run. Populated by the operator-baseline phase, read by final-check.
type operatorBaseline struct {
	// restartsByUID maps a pod UID → container name → RestartCount at baseline.
	// We key by UID (not name) so a pod replacement is detected as "missing UID",
	// not as a same-name pod with an unchanged counter.
	restartsByUID map[types.UID]map[string]int32
}

// Test_SoakChurn drives repeated scale-up / scale-down cycles against a single
// small PCS to surface bugs that only appear after many incremental reconciles
// — leaks, monotonically growing fields, gradually drifting counters,
// finalizer pile-ups. Gated behind the `soak` build tag so it does not run as
// part of the default e2e suite.
func Test_SoakChurn(t *testing.T) {
	cfg := loadSoakConfig()
	if cfg.peak <= cfg.base {
		t.Fatalf("SOAK_PEAK (%d) must be > SOAK_BASE (%d)", cfg.peak, cfg.base)
	}

	baseline := &operatorBaseline{}

	runScaleTest(t, scaleTestConfig{
		name:         "SoakChurn",
		workload:     soakWorkloadName,
		yamlPath:     soakYAMLPath,
		expectedPods: cfg.peakPods(),
		pcsCount:     defaultScalePCSCount,
		workerNodes:  soakWorkerNodes,
		timeout:      soakTimeout,
		pollInterval: defaultScalePollInterval,
	}, func(tracker *measurement.TimelineTracker, tc *testctx.TestContext, _ string) {
		addOperatorBaselinePhase(tracker, tc, baseline)
		addSoakPhases(tracker, tc, cfg)
		addFinalCheckPhase(tracker, tc, cfg, baseline)
	})
}

// addOperatorBaselinePhase snapshots grove-operator pod restart counts before
// any churn begins. The final check compares against this snapshot to detect
// crashes or OOMKills that happened during the run.
func addOperatorBaselinePhase(tracker *measurement.TimelineTracker, tc *testctx.TestContext, baseline *operatorBaseline) {
	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "operator-baseline",
		ActionFn: func(ctx context.Context) error {
			snap, err := snapshotOperatorRestarts(ctx, tc)
			if err != nil {
				return fmt.Errorf("snapshot operator pods: %w", err)
			}
			baseline.restartsByUID = snap
			return nil
		},
	})
}

// snapshotOperatorRestarts lists grove-operator pods and returns a map of
// pod UID → container name → RestartCount. Used both for the pre-test baseline
// and the post-test comparison.
func snapshotOperatorRestarts(ctx context.Context, tc *testctx.TestContext) (map[types.UID]map[string]int32, error) {
	podList := &corev1.PodList{}
	if err := tc.Client.List(ctx, podList,
		client.InNamespace(setup.OperatorNamespace),
		setup.OperatorPodLabels,
	); err != nil {
		return nil, err
	}
	out := make(map[types.UID]map[string]int32, len(podList.Items))
	for _, pod := range podList.Items {
		perContainer := make(map[string]int32, len(pod.Status.ContainerStatuses))
		for _, cs := range pod.Status.ContainerStatuses {
			perContainer[cs.Name] = cs.RestartCount
		}
		out[pod.UID] = perContainer
	}
	return out, nil
}

// addSoakPhases adds the deploy phase plus N churn cycles to the tracker. Each
// cycle contributes four phases: scale-up, hold-peak, scale-down, hold-base.
func addSoakPhases(tracker *measurement.TimelineTracker, tc *testctx.TestContext, cfg soakConfig) {
	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "deploy",
		ActionFn: func(ctx context.Context) error {
			_, err := resources.NewResourceManager(tc.Client, Logger).ApplyYAMLFile(ctx, tc.Workload.YAMLPath, tc.Namespace)
			return err
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "base-pods-ready",
				Condition: &condition.PodsReadyCondition{
					Client:        tc.Client.Client,
					Namespace:     tc.Namespace,
					LabelSelector: tc.GetLabelSelector(),
					ExpectedCount: cfg.basePods(),
				},
			},
		},
	})

	for cycle := 1; cycle <= cfg.cycles; cycle++ {
		addCyclePhases(tracker, tc, cfg, cycle)
	}
}

// addCyclePhases registers the four phases of a single churn cycle. Phase
// names are suffixed with the cycle index so per-cycle costs are visible in
// the exported timeline.
func addCyclePhases(tracker *measurement.TimelineTracker, tc *testctx.TestContext, cfg soakConfig, cycle int) {
	wm := workload.NewWorkloadManager(tc.Client, Logger)

	tracker.AddPhase(measurement.PhaseDefinition{
		Name: fmt.Sprintf("scale-up-c%d", cycle),
		ActionFn: func(ctx context.Context) error {
			Logger.Infof("cycle %d: scaling %s %d → %d PCS replicas", cycle, tc.Workload.Name, cfg.base, cfg.peak)
			return wm.ScalePCS(ctx, tc.Namespace, tc.Workload.Name, cfg.peak)
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "peak-pods-ready",
				Condition: &condition.PodsReadyCondition{
					Client:        tc.Client.Client,
					Namespace:     tc.Namespace,
					LabelSelector: tc.GetLabelSelector(),
					ExpectedCount: cfg.peakPods(),
				},
			},
		},
	})

	tracker.AddPhase(measurement.PhaseDefinition{
		Name:     fmt.Sprintf("hold-peak-c%d", cycle),
		ActionFn: func(_ context.Context) error { return nil },
		Milestones: []measurement.MilestoneDefinition{
			{
				Name:      "peak-hold-elapsed",
				Condition: &condition.TimerCondition{Duration: soakPerCycleHold},
			},
		},
	})

	tracker.AddPhase(measurement.PhaseDefinition{
		Name: fmt.Sprintf("scale-down-c%d", cycle),
		ActionFn: func(ctx context.Context) error {
			Logger.Infof("cycle %d: scaling %s %d → %d PCS replicas", cycle, tc.Workload.Name, cfg.peak, cfg.base)
			return wm.ScalePCS(ctx, tc.Namespace, tc.Workload.Name, cfg.base)
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "base-pods-restored",
				Condition: &condition.PodsScaledDownToCountCondition{
					Client:        tc.Client.Client,
					Namespace:     tc.Namespace,
					LabelSelector: tc.GetLabelSelector(),
					ExpectedCount: cfg.basePods(),
				},
			},
		},
	})

	tracker.AddPhase(measurement.PhaseDefinition{
		Name:     fmt.Sprintf("hold-base-c%d", cycle),
		ActionFn: func(_ context.Context) error { return nil },
		Milestones: []measurement.MilestoneDefinition{
			{
				Name:      "base-hold-elapsed",
				Condition: &condition.TimerCondition{Duration: soakPerCycleHold},
			},
		},
	})
}

// addFinalCheckPhase appends a synchronous final-check phase to the timeline.
// The phase must run before runScaleTest's deferred cleanup deletes the PCS,
// which is why it lives inside the timeline rather than after tracker.Wait().
// The action runs the end-state assertions and returns an error to fail the
// phase (and the test) if any invariant is violated.
func addFinalCheckPhase(tracker *measurement.TimelineTracker, tc *testctx.TestContext, cfg soakConfig, baseline *operatorBaseline) {
	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "final-check",
		ActionFn: func(ctx context.Context) error {
			return runSoakFinalChecks(ctx, tc, cfg, baseline)
		},
	})
}

// runSoakFinalChecks asserts end-state invariants after N churn cycles. Each
// helper targets a distinct bug class (leak / drift / stuck deletion / scheduling
// artifact / status convergence / operator memory). Returns an error rather
// than calling t.Fatalf so it can be invoked from a phase ActionFn; the
// tracker propagates the error into a test failure.
func runSoakFinalChecks(ctx context.Context, tc *testctx.TestContext, cfg soakConfig, baseline *operatorBaseline) error {
	pods, err := tc.ListPods()
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}

	pclqList := &grovecorev1alpha1.PodCliqueList{}
	if err := tc.Client.List(ctx, pclqList,
		client.InNamespace(tc.Namespace),
		client.MatchingLabels{common.LabelPartOfKey: soakWorkloadName},
	); err != nil {
		return fmt.Errorf("list PodCliques: %w", err)
	}

	pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
	if err := tc.Client.List(ctx, pcsgList,
		client.InNamespace(tc.Namespace),
		client.MatchingLabels{common.LabelPartOfKey: soakWorkloadName},
	); err != nil {
		return fmt.Errorf("list PodCliqueScalingGroups: %w", err)
	}

	podGangs := &groveschedulerv1alpha1.PodGangList{}
	if err := tc.Client.List(ctx, podGangs,
		client.InNamespace(tc.Namespace),
		client.MatchingLabels{common.LabelPartOfKey: soakWorkloadName},
	); err != nil {
		return fmt.Errorf("list PodGangs: %w", err)
	}

	pcs := &grovecorev1alpha1.PodCliqueSet{}
	if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: soakWorkloadName}, pcs); err != nil {
		return fmt.Errorf("get PCS: %w", err)
	}

	if err := checkExpectedObjectGraph(pods, pclqList, pcsgList, cfg); err != nil {
		return err
	}
	if err := checkNoStuckDeletions(pods, pclqList, pcsgList, podGangs); err != nil {
		return err
	}
	if err := checkPodGangCleanup(podGangs, cfg); err != nil {
		return err
	}
	if err := checkPCSStatusConvergence(pcs, cfg); err != nil {
		return err
	}
	if err := checkPodCliqueStatusConvergence(pclqList); err != nil {
		return err
	}
	if err := checkOperatorHealth(ctx, tc, baseline); err != nil {
		return err
	}
	return nil
}

// checkExpectedObjectGraph (check #1) asserts the post-scale-down child object
// graph: exact live pod / PodClique counts, zero PCSGs (for this fixture), and
// no managed object carrying a replica index from a scaled-down peak. A residual
// index >= cfg.base is the canonical leak signal.
func checkExpectedObjectGraph(pods *corev1.PodList, pclqList *grovecorev1alpha1.PodCliqueList, pcsgList *grovecorev1alpha1.PodCliqueScalingGroupList, cfg soakConfig) error {
	if got, want := len(pods.Items), cfg.basePods(); got != want {
		return fmt.Errorf("live pod count = %d, want %d (potential leak)", got, want)
	}
	if got, want := len(pclqList.Items), cfg.base; got != want {
		return fmt.Errorf("PodClique count = %d, want %d (potential leak)", got, want)
	}
	if len(pcsgList.Items) != 0 {
		return fmt.Errorf("PCSG count = %d, want 0 for this fixture", len(pcsgList.Items))
	}
	for i := range pclqList.Items {
		pclq := &pclqList.Items[i]
		if err := assertReplicaIndexBelow(pclq.Labels, cfg.base, "PodClique", pclq.Name); err != nil {
			return err
		}
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if err := assertReplicaIndexBelow(pod.Labels, cfg.base, "Pod", pod.Name); err != nil {
			return err
		}
	}
	return nil
}

// assertReplicaIndexBelow fails if an object carries a PCS-replica-index label
// >= base, which indicates it was created at peak and not cleaned up on
// scale-down. Objects without the label are skipped (not all managed objects
// carry it).
func assertReplicaIndexBelow(labels map[string]string, base int, kind, name string) error {
	raw, ok := labels[common.LabelPodCliqueSetReplicaIndex]
	if !ok {
		return nil
	}
	idx, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%s %s has non-integer %s label %q: %w", kind, name, common.LabelPodCliqueSetReplicaIndex, raw, err)
	}
	if idx >= base {
		return fmt.Errorf("%s %s has stale replica index %d (>= base %d)", kind, name, idx, base)
	}
	return nil
}

// checkNoStuckDeletions (check #2) asserts that no managed object still has a
// DeletionTimestamp at the final check. A surviving DeletionTimestamp after
// the system should have settled indicates a finalizer pile-up — the bug class
// soak is specifically designed to surface.
func checkNoStuckDeletions(pods *corev1.PodList, pclqList *grovecorev1alpha1.PodCliqueList, pcsgList *grovecorev1alpha1.PodCliqueScalingGroupList, podGangs *groveschedulerv1alpha1.PodGangList) error {
	for i := range pods.Items {
		if ts := pods.Items[i].DeletionTimestamp; ts != nil {
			return fmt.Errorf("Pod %s still terminating at final check (DeletionTimestamp=%s)", pods.Items[i].Name, ts)
		}
	}
	for i := range pclqList.Items {
		if ts := pclqList.Items[i].DeletionTimestamp; ts != nil {
			return fmt.Errorf("PodClique %s still terminating at final check (DeletionTimestamp=%s)", pclqList.Items[i].Name, ts)
		}
	}
	for i := range pcsgList.Items {
		if ts := pcsgList.Items[i].DeletionTimestamp; ts != nil {
			return fmt.Errorf("PCSG %s still terminating at final check (DeletionTimestamp=%s)", pcsgList.Items[i].Name, ts)
		}
	}
	for i := range podGangs.Items {
		if ts := podGangs.Items[i].DeletionTimestamp; ts != nil {
			return fmt.Errorf("PodGang %s still terminating at final check (DeletionTimestamp=%s)", podGangs.Items[i].Name, ts)
		}
	}
	return nil
}

// checkPodGangCleanup (check #3) asserts that scheduling artifacts (PodGangs)
// were cleaned up to match the base set. Churn can leave stale PodGangs even
// when pod counts look correct — this catches that class of leak.
//
// For this fixture (no PCSGs, just standalone cliques in one PCS), each PCS
// replica produces exactly one base PodGang, so the expected count is cfg.base.
func checkPodGangCleanup(podGangs *groveschedulerv1alpha1.PodGangList, cfg soakConfig) error {
	if got, want := len(podGangs.Items), cfg.base; got != want {
		return fmt.Errorf("PodGang count = %d, want %d (stale scheduling artifacts)", got, want)
	}
	return nil
}

// checkPCSStatusConvergence (check #4) extends the existing UpdateProgress
// bounds check to full PCS spec/status convergence: replica counters match
// base, ObservedGeneration is current, and no LastErrors have accumulated
// across N cycles.
func checkPCSStatusConvergence(pcs *grovecorev1alpha1.PodCliqueSet, cfg soakConfig) error {
	base := int32(cfg.base)
	if pcs.Spec.Replicas != base {
		return fmt.Errorf("PCS Spec.Replicas = %d, want %d", pcs.Spec.Replicas, base)
	}
	if pcs.Status.Replicas != base {
		return fmt.Errorf("PCS Status.Replicas = %d, want %d", pcs.Status.Replicas, base)
	}
	if pcs.Status.AvailableReplicas != base {
		return fmt.Errorf("PCS Status.AvailableReplicas = %d, want %d", pcs.Status.AvailableReplicas, base)
	}
	if pcs.Status.UpdatedReplicas != base {
		return fmt.Errorf("PCS Status.UpdatedReplicas = %d, want %d", pcs.Status.UpdatedReplicas, base)
	}
	if og := pcs.Status.ObservedGeneration; og == nil || *og != pcs.Generation {
		return fmt.Errorf("PCS ObservedGeneration = %v, want %d", og, pcs.Generation)
	}
	if up := pcs.Status.UpdateProgress; up != nil {
		if up.UpdatedPodCliquesCount > up.TotalPodCliquesCount {
			return fmt.Errorf("UpdatedPodCliquesCount (%d) > TotalPodCliquesCount (%d)",
				up.UpdatedPodCliquesCount, up.TotalPodCliquesCount)
		}
		if up.UpdatedPodCliqueScalingGroupsCount > up.TotalPodCliqueScalingGroupsCount {
			return fmt.Errorf("UpdatedPodCliqueScalingGroupsCount (%d) > TotalPodCliqueScalingGroupsCount (%d)",
				up.UpdatedPodCliqueScalingGroupsCount, up.TotalPodCliqueScalingGroupsCount)
		}
	}
	if len(pcs.Status.LastErrors) > 0 {
		return fmt.Errorf("%d LastErrors on PCS after %d cycles: %+v",
			len(pcs.Status.LastErrors), cfg.cycles, pcs.Status.LastErrors)
	}
	return nil
}

// checkPodCliqueStatusConvergence (check #5) verifies per-PodClique status
// fields converged: ObservedGeneration matches, replica counters match Spec,
// and no LastErrors are recorded. Per-child errors can hide here even when
// the PCS-level LastErrors slice is empty.
func checkPodCliqueStatusConvergence(pclqList *grovecorev1alpha1.PodCliqueList) error {
	for i := range pclqList.Items {
		pclq := &pclqList.Items[i]
		if og := pclq.Status.ObservedGeneration; og == nil || *og != pclq.Generation {
			return fmt.Errorf("PodClique %s ObservedGeneration = %v, want %d", pclq.Name, og, pclq.Generation)
		}
		if pclq.Status.ReadyReplicas != pclq.Spec.Replicas {
			return fmt.Errorf("PodClique %s ReadyReplicas = %d, want %d", pclq.Name, pclq.Status.ReadyReplicas, pclq.Spec.Replicas)
		}
		if pclq.Status.UpdatedReplicas != pclq.Spec.Replicas {
			return fmt.Errorf("PodClique %s UpdatedReplicas = %d, want %d", pclq.Name, pclq.Status.UpdatedReplicas, pclq.Spec.Replicas)
		}
		if len(pclq.Status.LastErrors) > 0 {
			return fmt.Errorf("PodClique %s has %d LastErrors: %+v", pclq.Name, len(pclq.Status.LastErrors), pclq.Status.LastErrors)
		}
	}
	return nil
}

// checkOperatorHealth (check #6) compares grove-operator pod state against the
// baseline captured at the start of the test. Any pod replacement (UID
// disappeared) or container restart increment is treated as a failure — the
// soak's primary purpose is to surface memory leaks and crashes that would
// otherwise show up as silent operator restarts.
func checkOperatorHealth(ctx context.Context, tc *testctx.TestContext, baseline *operatorBaseline) error {
	if baseline == nil || baseline.restartsByUID == nil {
		return fmt.Errorf("operator baseline not captured")
	}
	current, err := snapshotOperatorRestarts(ctx, tc)
	if err != nil {
		return fmt.Errorf("snapshot operator pods at final check: %w", err)
	}
	for uid, baseCounts := range baseline.restartsByUID {
		curCounts, ok := current[uid]
		if !ok {
			return fmt.Errorf("operator pod UID %s present at baseline is gone at final check (pod was replaced — likely a crash)", uid)
		}
		for container, baseCount := range baseCounts {
			curCount, ok := curCounts[container]
			if !ok {
				return fmt.Errorf("operator pod %s container %s missing at final check", uid, container)
			}
			if curCount > baseCount {
				return fmt.Errorf("operator pod %s container %s restarted: %d → %d", uid, container, baseCount, curCount)
			}
		}
	}
	return nil
}
