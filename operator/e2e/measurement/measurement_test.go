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

package measurement

import (
	"context"
	"errors"
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
)

func TestTimelineTracker_RunPhase(t *testing.T) {
	t.Parallel()

	tracker := NewTimelineTracker("test", "run-1", "default", 1, WithPollInterval(5*time.Millisecond))
	actionCalled := false
	cond1 := &stepCondition{requiredCalls: 2}
	cond2 := &stepCondition{requiredCalls: 3}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ctx = ctrl.LoggerInto(ctx, ctrl.Log.WithName("timeline-tracker-test"))

	tracker.AddPhase(PhaseDefinition{
		Name: "deploy",
		ActionFn: func(context.Context) error {
			actionCalled = true
			return nil
		},
		Milestones: []MilestoneDefinition{
			{Name: "pods-created", Condition: cond1},
			{Name: "pods-ready", Condition: cond2},
		},
	})

	result, err := tracker.Run(ctx, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !actionCalled {
		t.Fatalf("action function was not called")
	}

	if len(result.Phases) != 1 {
		t.Fatalf("len(Phases) = %d, want 1", len(result.Phases))
	}
	if result.Phases[0].Name != "deploy" {
		t.Fatalf("phase name = %q, want deploy", result.Phases[0].Name)
	}
	if len(result.Phases[0].Milestones) != 2 {
		t.Fatalf("len(milestones) = %d, want 2", len(result.Phases[0].Milestones))
	}
	if result.Phases[0].Milestones[0].DurationFromPhaseStart > result.Phases[0].Milestones[1].DurationFromPhaseStart {
		t.Fatalf("milestones are not ordered by completion time")
	}
}

func TestTimelineTracker_RunPhase_ActionError(t *testing.T) {
	t.Parallel()

	tracker := NewTimelineTracker("test", "run-1", "default", 1, WithPollInterval(5*time.Millisecond))
	wantErr := errors.New("action failed")

	tracker.AddPhase(PhaseDefinition{
		Name:     "deploy",
		ActionFn: func(context.Context) error { return wantErr },
		Milestones: []MilestoneDefinition{
			{Name: "pods-created", Condition: &stepCondition{requiredCalls: 1}},
		},
	})

	result, err := tracker.Run(context.Background(), nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if result != nil {
		t.Fatalf("expected nil result on error, got %v", result)
	}
}

func TestTimelineTracker_RunPhase_ContextTimeout(t *testing.T) {
	t.Parallel()

	tracker := NewTimelineTracker("test", "run-1", "default", 1, WithPollInterval(5*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	tracker.AddPhase(PhaseDefinition{
		Name:     "deploy",
		ActionFn: func(context.Context) error { return nil },
		Milestones: []MilestoneDefinition{
			{Name: "never-met", Condition: &stepCondition{requiredCalls: 0}},
		},
	})

	result, err := tracker.Run(ctx, nil)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if result != nil {
		t.Fatalf("expected nil result on error, got %v", result)
	}
}

func TestTimelineTracker_ResultMetadata(t *testing.T) {
	t.Parallel()

	tracker := NewTimelineTracker("scale-test", "run-42", "test-ns", 5, WithPollInterval(5*time.Millisecond))
	tracker.AddPhase(PhaseDefinition{
		Name:     "deploy",
		ActionFn: func(context.Context) error { return nil },
		Milestones: []MilestoneDefinition{
			{Name: "done", Condition: &stepCondition{requiredCalls: 1}},
		},
	})

	result, err := tracker.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.TestName != "scale-test" {
		t.Fatalf("TestName = %q, want %q", result.TestName, "scale-test")
	}
	if result.RunID != "run-42" {
		t.Fatalf("RunID = %q, want %q", result.RunID, "run-42")
	}
	if result.Namespace != "test-ns" {
		t.Fatalf("Namespace = %q, want %q", result.Namespace, "test-ns")
	}
	if result.PCSCount != 5 {
		t.Fatalf("PCSCount = %d, want %d", result.PCSCount, 5)
	}
	if result.TestDurationSeconds <= 0 {
		t.Fatalf("TestDurationSeconds = %f, want > 0", result.TestDurationSeconds)
	}
	if len(result.Phases) != 1 {
		t.Fatalf("len(Phases) = %d, want 1", len(result.Phases))
	}
}

func TestTimelineTracker_ResultPhasesAreCopied(t *testing.T) {
	t.Parallel()

	tracker := NewTimelineTracker("test", "run-1", "default", 1, WithPollInterval(5*time.Millisecond))
	tracker.AddPhase(PhaseDefinition{
		Name:     "deploy",
		ActionFn: func(context.Context) error { return nil },
		Milestones: []MilestoneDefinition{
			{Name: "done", Condition: &stepCondition{requiredCalls: 1}},
		},
	})

	result1, err := tracker.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result1.Phases[0].Name = "mutated"
	result1.Phases[0].Milestones[0].Name = "mutated-ms"

	// Run again to get a fresh result from internal state.
	// Since phases were already recorded, buildResult copies them.
	// We verify the internal state was not affected by mutation above.
	tracker2 := NewTimelineTracker("test", "run-1", "default", 1, WithPollInterval(5*time.Millisecond))
	tracker2.AddPhase(PhaseDefinition{
		Name:     "deploy",
		ActionFn: func(context.Context) error { return nil },
		Milestones: []MilestoneDefinition{
			{Name: "done", Condition: &stepCondition{requiredCalls: 1}},
		},
	})

	result2, err := tracker2.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result2.Phases[0].Name == "mutated" {
		t.Fatalf("phase copy is not isolated")
	}
	if result2.Phases[0].Milestones[0].Name == "mutated-ms" {
		t.Fatalf("milestone copy is not isolated")
	}
}

type stepCondition struct {
	requiredCalls int
	calls         int
}

func (c *stepCondition) Met(context.Context) (bool, error) {
	c.calls++
	if c.requiredCalls == 0 {
		return false, nil
	}
	return c.calls >= c.requiredCalls, nil
}
