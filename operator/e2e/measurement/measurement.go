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
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
)

const defaultTimelinePollInterval = 500 * time.Millisecond

// Milestone is a named point in a test phase timeline.
type Milestone struct {
	Name                   string    `json:"name"`
	Timestamp              time.Time `json:"timestamp"`
	DurationFromPhaseStart float64   `json:"durationFromPhaseStartSeconds"`
}

// Phase is a named timeline segment containing milestones.
type Phase struct {
	Name                  string      `json:"name"`
	StartTime             time.Time   `json:"startTime"`
	DurationFromTestStart float64     `json:"durationFromTestStartSeconds"`
	Milestones            []Milestone `json:"milestones"`
}

// MilestoneCondition returns whether a milestone has been reached.
type MilestoneCondition interface {
	Met(ctx context.Context) (bool, error)
}

// MilestoneDefinition pairs a milestone name with its condition.
type MilestoneDefinition struct {
	Name      string
	Condition MilestoneCondition
}

// ScaleTestResult accumulates all timeline/raw measurement data for a single run.
// It is intended for sequential writes in tests and is not thread-safe.
type ScaleTestResult struct {
	// Test identity.
	TestName  string `json:"testName"`
	RunID     string `json:"runID"`
	Namespace string `json:"namespace"`

	// Scale metadata.
	TotalExpectedPods int    `json:"totalExpectedPods"`
	PCSCount          int    `json:"pcsCount"`
	NodeCount         int    `json:"nodeCount"`
	ClusterProfile    string `json:"clusterProfile,omitempty"`

	// Timeline data from TimelineTracker.
	Phases              []Phase `json:"phases"`
	TestDurationSeconds float64 `json:"testDurationSeconds"`
}

// TimelineOption configures a TimelineTracker.
type TimelineOption func(*TimelineTracker)

// WithPollInterval sets the milestone polling interval.
func WithPollInterval(d time.Duration) TimelineOption {
	return func(t *TimelineTracker) {
		if d > 0 {
			t.pollInterval = d
		}
	}
}

// PhaseDefinition holds all inputs for a phase to be executed later.
type PhaseDefinition struct {
	Name       string
	ActionFn   func(ctx context.Context) error
	Milestones []MilestoneDefinition
}

// TimelineTracker records ordered phases/milestones for a scale test.
type TimelineTracker struct {
	testStart    time.Time
	phases       []Phase
	definitions  []PhaseDefinition
	pollInterval time.Duration
}

// NewTimelineTracker constructs a new timeline tracker.
func NewTimelineTracker(opts ...TimelineOption) *TimelineTracker {
	t := &TimelineTracker{
		testStart:    time.Now(),
		pollInterval: defaultTimelinePollInterval,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// AddPhase registers a phase definition for later execution.
func (t *TimelineTracker) AddPhase(def PhaseDefinition) {
	t.definitions = append(t.definitions, def)
}

// Run executes all defined phases in order.
func (t *TimelineTracker) Run(ctx context.Context) error {
	for _, def := range t.definitions {
		if err := t.runPhase(ctx, def); err != nil {
			return err
		}
	}
	return nil
}

// runPhase executes a single phase definition and records its milestones.
func (t *TimelineTracker) runPhase(ctx context.Context, def PhaseDefinition) error {
	log := ctrl.LoggerFrom(ctx).WithValues("phase", def.Name)

	if def.ActionFn == nil {
		return fmt.Errorf("phase %q: action cannot be nil", def.Name)
	}

	phaseStart := time.Now()
	phase := Phase{
		Name:                  def.Name,
		StartTime:             phaseStart,
		DurationFromTestStart: phaseStart.Sub(t.testStart).Seconds(),
		Milestones:            make([]Milestone, 0, len(def.Milestones)),
	}

	log.V(1).Info("phase started")

	if err := def.ActionFn(ctx); err != nil {
		return fmt.Errorf("phase %q: action failed: %w", def.Name, err)
	}

	reached, err := t.pollMilestones(ctx, def.Name, phaseStart, def.Milestones)
	if err != nil {
		return err
	}
	phase.Milestones = reached

	t.phases = append(t.phases, phase)
	log.V(1).Info("phase completed", "milestoneCount", len(phase.Milestones))

	return nil
}

// pollMilestones polls milestone conditions until all are met or the context is cancelled.
func (t *TimelineTracker) pollMilestones(
	ctx context.Context,
	phaseName string,
	phaseStart time.Time,
	milestones []MilestoneDefinition,
) ([]Milestone, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("phase", phaseName)
	remaining := append([]MilestoneDefinition{}, milestones...)
	reached := make([]Milestone, 0, len(milestones))

	for len(remaining) > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(t.pollInterval):
		}

		var stillPending []MilestoneDefinition
		for _, def := range remaining {
			ok, err := def.Condition.Met(ctx)
			if err != nil {
				return nil, fmt.Errorf("phase %q: milestone %q: %w", phaseName, def.Name, err)
			}
			if ok {
				ts := time.Now()
				reached = append(reached, Milestone{
					Name:                   def.Name,
					Timestamp:              ts,
					DurationFromPhaseStart: ts.Sub(phaseStart).Seconds(),
				})
				log.V(1).Info("milestone reached", "milestone", def.Name,
					"durationFromPhaseStartSeconds", ts.Sub(phaseStart).Seconds())
			} else {
				stillPending = append(stillPending, def)
			}
		}
		remaining = stillPending
	}

	return reached, nil
}

// Phases returns a copy of recorded phases and milestones.
func (t *TimelineTracker) Phases() []Phase {
	out := make([]Phase, len(t.phases))
	for i := range t.phases {
		out[i] = t.phases[i]
		if len(t.phases[i].Milestones) > 0 {
			out[i].Milestones = append([]Milestone(nil), t.phases[i].Milestones...)
		}
	}
	return out
}
