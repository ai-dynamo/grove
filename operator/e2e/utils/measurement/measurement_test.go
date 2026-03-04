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

	tracker := NewTimelineTracker(WithPollInterval(5 * time.Millisecond))
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

	err := tracker.Run(ctx)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !actionCalled {
		t.Fatalf("action function was not called")
	}

	phases := tracker.Phases()
	if len(phases) != 1 {
		t.Fatalf("len(Phases()) = %d, want 1", len(phases))
	}
	if phases[0].Name != "deploy" {
		t.Fatalf("phase name = %q, want deploy", phases[0].Name)
	}
	if len(phases[0].Milestones) != 2 {
		t.Fatalf("len(milestones) = %d, want 2", len(phases[0].Milestones))
	}
	if phases[0].Milestones[0].DurationFromPhaseStart > phases[0].Milestones[1].DurationFromPhaseStart {
		t.Fatalf("milestones are not ordered by completion time")
	}
}

func TestTimelineTracker_RunPhase_ActionError(t *testing.T) {
	t.Parallel()

	tracker := NewTimelineTracker(WithPollInterval(5 * time.Millisecond))
	wantErr := errors.New("action failed")

	tracker.AddPhase(PhaseDefinition{
		Name:     "deploy",
		ActionFn: func(context.Context) error { return wantErr },
		Milestones: []MilestoneDefinition{
			{Name: "pods-created", Condition: &stepCondition{requiredCalls: 1}},
		},
	})

	err := tracker.Run(context.Background())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestTimelineTracker_RunPhase_ContextTimeout(t *testing.T) {
	t.Parallel()

	tracker := NewTimelineTracker(WithPollInterval(5 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	tracker.AddPhase(PhaseDefinition{
		Name:     "deploy",
		ActionFn: func(context.Context) error { return nil },
		Milestones: []MilestoneDefinition{
			{Name: "never-met", Condition: &stepCondition{requiredCalls: 0}},
		},
	})

	err := tracker.Run(ctx)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}

func TestTimelineTracker_PhasesReturnsCopy(t *testing.T) {
	t.Parallel()

	tracker := NewTimelineTracker(WithPollInterval(5 * time.Millisecond))
	tracker.AddPhase(PhaseDefinition{
		Name:     "deploy",
		ActionFn: func(context.Context) error { return nil },
		Milestones: []MilestoneDefinition{
			{Name: "done", Condition: &stepCondition{requiredCalls: 1}},
		},
	})

	err := tracker.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	phases := tracker.Phases()
	phases[0].Name = "mutated"
	phases[0].Milestones[0].Name = "mutated-ms"

	phasesAgain := tracker.Phases()
	if phasesAgain[0].Name == "mutated" {
		t.Fatalf("phase copy is not isolated")
	}
	if phasesAgain[0].Milestones[0].Name == "mutated-ms" {
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
