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

package scale

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/measurement"
	"github.com/ai-dynamo/grove/operator/e2e/measurement/condition"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// operatorBaseline captures the pre-test state of grove-operator pods so the
// final check can detect restarts or new pod replacements that happened during
// the run. Populated by addOperatorBaselinePhase, read by addFinalCheckPhase.
type operatorBaseline struct {
	// restartsByUID maps pod UID → container name → RestartCount at baseline.
	// Keyed by UID (not name) so a pod replacement is detected as "missing UID",
	// not as a same-name pod with an unchanged counter.
	restartsByUID map[types.UID]map[string]int32
}

// scaleFinalCheckConfig parameterizes the post-scale invariant checks.
type scaleFinalCheckConfig struct {
	targetReplicas int
	targetPods     int
	workloadName   string
}

// addOperatorBaselinePhase snapshots grove-operator pod restart counts before
// any work begins. Must be the first phase added to the tracker.
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

// addFinalCheckPhase appends a synchronous final-check phase to the timeline.
// Must be placed before the delete phase so assertions run against live objects.
func addFinalCheckPhase(tracker *measurement.TimelineTracker, tc *testctx.TestContext, cfg scaleFinalCheckConfig, baseline *operatorBaseline) {
	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "final-check",
		ActionFn: func(ctx context.Context) error {
			return runScaleFinalChecks(ctx, tc, cfg, baseline)
		},
	})
}

// addFinalCheckAndDeletePhases appends the final-check and delete phases shared
// by all scale-up and scale-down test variants.
func addFinalCheckAndDeletePhases(tracker *measurement.TimelineTracker, tc *testctx.TestContext, cfg scaleFinalCheckConfig, baseline *operatorBaseline) {
	addFinalCheckPhase(tracker, tc, cfg, baseline)
	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "delete",
		ActionFn: func(ctx context.Context) error {
			return workload.NewWorkloadManager(tc.Client, Logger).DeletePCS(ctx, tc.Namespace, tc.Workload.Name)
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "pcs-deleted",
				Condition: &condition.PCSDeletedCondition{
					Client:    tc.Client.Client,
					Name:      tc.Workload.Name,
					Namespace: tc.Namespace,
				},
			},
		},
	})
}

// runScaleFinalChecks asserts post-scale end-state invariants. Each helper
// targets a distinct bug class: leak, stuck deletion, stale scheduling artifact,
// status drift, and operator crash. Returns an error to fail the calling phase.
func runScaleFinalChecks(ctx context.Context, tc *testctx.TestContext, cfg scaleFinalCheckConfig, baseline *operatorBaseline) error {
	pods, err := tc.ListPods()
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}

	pclqList := &grovecorev1alpha1.PodCliqueList{}
	if err := tc.Client.List(ctx, pclqList,
		client.InNamespace(tc.Namespace),
		client.MatchingLabels{common.LabelPartOfKey: cfg.workloadName},
	); err != nil {
		return fmt.Errorf("list PodCliques: %w", err)
	}

	pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
	if err := tc.Client.List(ctx, pcsgList,
		client.InNamespace(tc.Namespace),
		client.MatchingLabels{common.LabelPartOfKey: cfg.workloadName},
	); err != nil {
		return fmt.Errorf("list PodCliqueScalingGroups: %w", err)
	}

	podGangs := &groveschedulerv1alpha1.PodGangList{}
	if err := tc.Client.List(ctx, podGangs,
		client.InNamespace(tc.Namespace),
		client.MatchingLabels{common.LabelPartOfKey: cfg.workloadName},
	); err != nil {
		return fmt.Errorf("list PodGangs: %w", err)
	}

	pcs := &grovecorev1alpha1.PodCliqueSet{}
	if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: cfg.workloadName}, pcs); err != nil {
		return fmt.Errorf("get PCS: %w", err)
	}

	if err := checkExpectedObjectGraph(pods, pclqList, pcsgList, cfg.targetReplicas, cfg.targetPods); err != nil {
		return err
	}
	if err := checkNoStuckDeletions(pods, pclqList, pcsgList, podGangs); err != nil {
		return err
	}
	if err := checkPodGangCleanup(podGangs, cfg.targetReplicas); err != nil {
		return err
	}
	// PCS and PodClique status fields are updated asynchronously by the controller.
	// Poll until they converge rather than checking once immediately after pod readiness.
	if err := waitForStatusConvergence(ctx, tc, cfg); err != nil {
		return err
	}
	if err := checkOperatorHealth(ctx, tc, baseline); err != nil {
		return err
	}
	return nil
}

const (
	statusConvergenceTimeout  = 60 * time.Second
	statusConvergenceInterval = 2 * time.Second
)

// waitForStatusConvergence polls until PCS and PodClique status fields reflect
// the target replica count. The controller reconciles asynchronously, so status
// may lag pod readiness by several seconds.
func waitForStatusConvergence(ctx context.Context, tc *testctx.TestContext, cfg scaleFinalCheckConfig) error {
	var lastErr error
	pollErr := wait.PollUntilContextTimeout(ctx, statusConvergenceInterval, statusConvergenceTimeout, true,
		func(ctx context.Context) (bool, error) {
			pcs := &grovecorev1alpha1.PodCliqueSet{}
			if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: cfg.workloadName}, pcs); err != nil {
				return false, fmt.Errorf("get PCS: %w", err)
			}
			if err := checkPCSStatusConvergence(pcs, cfg.targetReplicas); err != nil {
				lastErr = err
				return false, nil
			}
			pclqList := &grovecorev1alpha1.PodCliqueList{}
			if err := tc.Client.List(ctx, pclqList,
				client.InNamespace(tc.Namespace),
				client.MatchingLabels{common.LabelPartOfKey: cfg.workloadName},
			); err != nil {
				return false, fmt.Errorf("list PodCliques: %w", err)
			}
			if err := checkPodCliqueStatusConvergence(pclqList); err != nil {
				lastErr = err
				return false, nil
			}
			return true, nil
		},
	)
	if pollErr != nil {
		if lastErr != nil {
			return fmt.Errorf("status did not converge within %s: %w", statusConvergenceTimeout, lastErr)
		}
		return pollErr
	}
	return nil
}

// snapshotOperatorRestarts lists grove-operator pods and returns a map of
// pod UID → container name → RestartCount.
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

// checkExpectedObjectGraph (check #1) asserts the post-scale child object graph:
// exact live pod and PodClique counts match the target, zero PCSGs (all scale
// fixtures use standalone cliques only), and no object carries a replica index
// from beyond the target — the canonical leak signal.
func checkExpectedObjectGraph(pods *corev1.PodList, pclqList *grovecorev1alpha1.PodCliqueList, pcsgList *grovecorev1alpha1.PodCliqueScalingGroupList, targetReplicas, targetPods int) error {
	if got, want := len(pods.Items), targetPods; got != want {
		return fmt.Errorf("live pod count = %d, want %d (potential leak)", got, want)
	}
	if got, want := len(pclqList.Items), targetReplicas; got != want {
		return fmt.Errorf("PodClique count = %d, want %d (potential leak)", got, want)
	}
	if len(pcsgList.Items) != 0 {
		return fmt.Errorf("PCSG count = %d, want 0 for this fixture", len(pcsgList.Items))
	}
	for i := range pclqList.Items {
		pclq := &pclqList.Items[i]
		if err := assertReplicaIndexBelow(pclq.Labels, targetReplicas, "PodClique", pclq.Name); err != nil {
			return err
		}
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if err := assertReplicaIndexBelow(pod.Labels, targetReplicas, "Pod", pod.Name); err != nil {
			return err
		}
	}
	return nil
}

// assertReplicaIndexBelow fails if an object carries a PCS-replica-index label
// >= target, indicating it was created at a higher replica count and not cleaned
// up. Objects without the label are skipped.
func assertReplicaIndexBelow(labels map[string]string, target int, kind, name string) error {
	raw, ok := labels[common.LabelPodCliqueSetReplicaIndex]
	if !ok {
		return nil
	}
	idx, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%s %s has non-integer %s label %q: %w", kind, name, common.LabelPodCliqueSetReplicaIndex, raw, err)
	}
	if idx >= target {
		return fmt.Errorf("%s %s has stale replica index %d (>= target %d)", kind, name, idx, target)
	}
	return nil
}

// checkNoStuckDeletions (check #2) asserts that no managed object still has a
// DeletionTimestamp. A surviving DeletionTimestamp after the system should have
// settled indicates a finalizer pile-up.
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

// checkPodGangCleanup (check #3) asserts scheduling artifacts (PodGangs) were
// cleaned up to match the target replica count. Each PCS replica produces exactly
// one PodGang for these fixtures (no PCSGs, standalone cliques only).
func checkPodGangCleanup(podGangs *groveschedulerv1alpha1.PodGangList, targetReplicas int) error {
	if got, want := len(podGangs.Items), targetReplicas; got != want {
		return fmt.Errorf("PodGang count = %d, want %d (stale scheduling artifacts)", got, want)
	}
	return nil
}

// checkPCSStatusConvergence (check #4) verifies full PCS spec/status convergence:
// replica counters match the target, ObservedGeneration is current, and no
// LastErrors have accumulated.
func checkPCSStatusConvergence(pcs *grovecorev1alpha1.PodCliqueSet, targetReplicas int) error {
	target := int32(targetReplicas)
	if pcs.Spec.Replicas != target {
		return fmt.Errorf("PCS Spec.Replicas = %d, want %d", pcs.Spec.Replicas, target)
	}
	if pcs.Status.Replicas != target {
		return fmt.Errorf("PCS Status.Replicas = %d, want %d", pcs.Status.Replicas, target)
	}
	if pcs.Status.AvailableReplicas != target {
		return fmt.Errorf("PCS Status.AvailableReplicas = %d, want %d", pcs.Status.AvailableReplicas, target)
	}
	if pcs.Status.UpdatedReplicas != target {
		return fmt.Errorf("PCS Status.UpdatedReplicas = %d, want %d", pcs.Status.UpdatedReplicas, target)
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
		return fmt.Errorf("%d LastErrors on PCS: %+v", len(pcs.Status.LastErrors), pcs.Status.LastErrors)
	}
	return nil
}

// checkPodCliqueStatusConvergence (check #5) verifies per-PodClique status fields
// converged: ObservedGeneration matches, replica counters match Spec, and no
// LastErrors are recorded. Per-child errors can hide here even when the PCS-level
// LastErrors slice is empty.
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
// baseline captured at the start of the test. Any pod replacement (UID disappeared)
// or container restart increment is treated as a failure.
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
