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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/resources"
	"github.com/ai-dynamo/grove/operator/e2e/measurement"
	"github.com/ai-dynamo/grove/operator/e2e/measurement/condition"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"

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
		addSoakPhases(tracker, tc, cfg)
		addFinalCheckPhase(tracker, tc, cfg)
	})
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
				Condition: &condition.PodsAtCountCondition{
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
func addFinalCheckPhase(tracker *measurement.TimelineTracker, tc *testctx.TestContext, cfg soakConfig) {
	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "final-check",
		ActionFn: func(ctx context.Context) error {
			return runSoakFinalChecks(ctx, tc, cfg)
		},
	})
}

// runSoakFinalChecks asserts end-state invariants. Returns an error rather
// than calling t.Fatalf so it can be invoked from a phase ActionFn; the
// tracker propagates the error into a test failure.
func runSoakFinalChecks(ctx context.Context, tc *testctx.TestContext, cfg soakConfig) error {
	// 1. Live pod count is back at base.
	pods, err := tc.ListPods()
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	if got, want := len(pods.Items), cfg.basePods(); got != want {
		return fmt.Errorf("live pod count = %d, want %d (potential leak)", got, want)
	}

	// 2. Fetch the PCS once for the next two checks.
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	if err := tc.Client.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: soakWorkloadName}, pcs); err != nil {
		return fmt.Errorf("get PCS: %w", err)
	}

	// 3. UpdateProgress counters stay bounded (the regression #567 protects against).
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

	// 4. lastErrors is empty — any error accumulated across N cycles is a real bug.
	if len(pcs.Status.LastErrors) > 0 {
		return fmt.Errorf("%d LastErrors on PCS after %d cycles: %+v",
			len(pcs.Status.LastErrors), cfg.cycles, pcs.Status.LastErrors)
	}

	return nil
}
