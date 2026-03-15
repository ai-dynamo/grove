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
	"sync"
	"time"

	"github.com/go-logr/logr"
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
	EndTime               time.Time   `json:"endTime,omitempty"`
	DurationFromTestStart float64     `json:"durationFromTestStartSeconds"`
	Milestones            []Milestone `json:"milestones"`
}

// AfterPhaseHook is called once after a phase completes.
// Receives the phase name and exact start/end times.
type AfterPhaseHook func(ctx context.Context, phaseName string, start, end time.Time)

// registeredHook pairs a hook function with its execution mode.
type registeredHook struct {
	fn    AfterPhaseHook
	async bool
}

// MilestoneCondition returns whether a milestone has been reached.
type MilestoneCondition interface {
	Met(ctx context.Context) (bool, error)
}

// ProgressReporter may be implemented by a MilestoneCondition to report progress.
type ProgressReporter interface {
	Progress(ctx context.Context) string
}

// MilestoneDefinition pairs a milestone name with its condition.
type MilestoneDefinition struct {
	Name      string
	Condition MilestoneCondition
}

// K8sClientConfig holds the K8s REST client rate-limit settings used by the operator.
type K8sClientConfig struct {
	QPS   float32 `json:"qps"`
	Burst int     `json:"burst"`
}

// ControllerMaxReconcile holds the MaxConcurrentReconciles per controller.
// JSON keys use full CRD names for clarity in archived benchmark artifacts.
type ControllerMaxReconcile struct {
	PodCliqueSet          int `json:"podCliqueSet"`
	PodCliqueScalingGroup int `json:"podCliqueScalingGroup"`
	PodClique             int `json:"podClique"`
}

// TrackerResult accumulates all timeline/measurement data for a single run.
type TrackerResult struct {
	TestName               string                  `json:"testName"`
	RunID                  string                  `json:"runID"`
	Namespace              string                  `json:"namespace"`
	PCSCount               int                     `json:"pcsCount"`
	Phases                 []Phase                 `json:"phases"`
	TestDurationSeconds    float64                 `json:"testDurationSeconds"`
	K8sClient              *K8sClientConfig        `json:"k8sClient,omitempty"`
	ControllerMaxReconcile *ControllerMaxReconcile `json:"controllerMaxReconcile,omitempty"`
}

// TimelineOption configures a TimelineTracker.
type TimelineOption func(*TimelineTracker)

// WithLogger sets the logger for the tracker.
func WithLogger(l logr.Logger) TimelineOption {
	return func(t *TimelineTracker) {
		t.logger = l
	}
}

// WithPollInterval sets the milestone polling interval.
func WithPollInterval(d time.Duration) TimelineOption {
	return func(t *TimelineTracker) {
		if d > 0 {
			t.pollInterval = d
		}
	}
}

// WithAfterPhaseHook registers a synchronous hook invoked after each phase completes.
// Multiple calls add multiple hooks; all are fired in registration order.
func WithAfterPhaseHook(hook AfterPhaseHook) TimelineOption {
	return func(t *TimelineTracker) {
		t.afterPhaseHooks = append(t.afterPhaseHooks, registeredHook{fn: hook})
	}
}

// WithAfterPhaseHookAsync registers an async hook that fires in a background goroutine.
// Call tracker.Wait() after tracker.Run() to ensure all async hooks complete before
// the test exits (prevents truncated pprof files).
func WithAfterPhaseHookAsync(hook AfterPhaseHook) TimelineOption {
	return func(t *TimelineTracker) {
		t.afterPhaseHooks = append(t.afterPhaseHooks, registeredHook{fn: hook, async: true})
	}
}

// PhaseDefinition holds all inputs for a phase to be executed later.
type PhaseDefinition struct {
	Name       string
	ActionFn   func(ctx context.Context) error
	Milestones []MilestoneDefinition
}

// TimelineTracker records ordered phases/milestones for a test.
type TimelineTracker struct {
	testName        string
	runID           string
	namespace       string
	pcsCount        int
	testStart       time.Time
	phases          []Phase
	definitions     []PhaseDefinition
	pollInterval    time.Duration
	logger          logr.Logger
	afterPhaseHooks []registeredHook
	wg              sync.WaitGroup
}

// NewTimelineTracker constructs a new timeline tracker with required metadata.
func NewTimelineTracker(testName, runID, namespace string, pcsCount int, opts ...TimelineOption) *TimelineTracker {
	t := &TimelineTracker{
		testName:     testName,
		runID:        runID,
		namespace:    namespace,
		pcsCount:     pcsCount,
		testStart:    time.Now(),
		pollInterval: defaultTimelinePollInterval,
		logger:       logr.Discard(),
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

// Run executes all defined phases in order and returns the complete result.
func (t *TimelineTracker) Run(ctx context.Context) (*TrackerResult, error) {
	t.logger.Info("timeline tracker started",
		"test", t.testName, "runID", t.runID, "namespace", t.namespace,
		"pcsCount", t.pcsCount, "phases", len(t.definitions))

	for i, def := range t.definitions {
		t.logger.Info("executing phase",
			"phaseIndex", fmt.Sprintf("%d/%d", i+1, len(t.definitions)),
			"phase", def.Name, "milestones", len(def.Milestones))
		if err := t.runPhase(ctx, def); err != nil {
			return nil, err
		}
	}

	result := t.buildResult()
	t.logger.Info("timeline tracker finished",
		"test", t.testName, "totalDuration", fmt.Sprintf("%.1fs", result.TestDurationSeconds))
	return result, nil
}

// Wait blocks until all async after-phase hooks have completed.
// Call this after Run() returns to ensure no background goroutines are truncated.
func (t *TimelineTracker) Wait() {
	t.wg.Wait()
}

// fireHook invokes a single registered hook, recovering from any panics to keep
// the test running even if a hook misbehaves.
func (t *TimelineTracker) fireHook(ctx context.Context, h registeredHook, phaseName string, start, end time.Time) {
	defer func() {
		if r := recover(); r != nil {
			t.logger.Error(fmt.Errorf("panic: %v", r), "after-phase hook panicked", "phase", phaseName)
		}
	}()
	h.fn(ctx, phaseName, start, end)
}

// buildResult assembles a TrackerResult from the tracker's metadata and recorded phases.
func (t *TimelineTracker) buildResult() *TrackerResult {
	return &TrackerResult{
		TestName:            t.testName,
		RunID:               t.runID,
		Namespace:           t.namespace,
		PCSCount:            t.pcsCount,
		Phases:              t.copyPhases(),
		TestDurationSeconds: time.Since(t.testStart).Seconds(),
	}
}

// copyPhases returns a deep copy of recorded phases.
func (t *TimelineTracker) copyPhases() []Phase {
	out := make([]Phase, len(t.phases))
	for i := range t.phases {
		out[i] = t.phases[i]
		if len(t.phases[i].Milestones) > 0 {
			out[i].Milestones = append([]Milestone(nil), t.phases[i].Milestones...)
		}
	}
	return out
}

// runPhase executes a single phase definition and records its milestones.
func (t *TimelineTracker) runPhase(ctx context.Context, def PhaseDefinition) error {
	log := t.logger.WithValues("phase", def.Name)

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

	log.Info("phase started", "milestoneCount", len(def.Milestones))

	log.Info("executing action")
	if err := def.ActionFn(ctx); err != nil {
		return fmt.Errorf("phase %q: action failed: %w", def.Name, err)
	}
	log.Info("action completed", "elapsed", fmt.Sprintf("%.1fs", time.Since(phaseStart).Seconds()))

	if len(def.Milestones) > 0 {
		log.Info("waiting for milestones", "milestones", milestoneNames(def.Milestones))
	}

	reached, err := t.pollMilestones(ctx, def.Name, phaseStart, def.Milestones)
	if err != nil {
		return err
	}
	phase.Milestones = reached
	phase.EndTime = time.Now()

	t.phases = append(t.phases, phase)
	log.Info("phase completed",
		"milestoneCount", len(phase.Milestones),
		"phaseDuration", fmt.Sprintf("%.1fs", time.Since(phaseStart).Seconds()))

	for _, h := range t.afterPhaseHooks {
		if h.async {
			t.wg.Add(1)
			go func(rh registeredHook) {
				defer t.wg.Done()
				t.fireHook(ctx, rh, phase.Name, phase.StartTime, phase.EndTime)
			}(h)
		} else {
			t.fireHook(ctx, h, phase.Name, phase.StartTime, phase.EndTime)
		}
	}

	return nil
}

// milestoneNames extracts the Name field from a slice of MilestoneDefinitions.
func milestoneNames(defs []MilestoneDefinition) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}

// pollMilestones polls milestone conditions until all are met or the context is cancelled.
func (t *TimelineTracker) pollMilestones(
	ctx context.Context,
	phaseName string,
	phaseStart time.Time,
	milestones []MilestoneDefinition,
) ([]Milestone, error) {
	log := t.logger.WithValues("phase", phaseName)
	remaining := append([]MilestoneDefinition{}, milestones...)
	reached := make([]Milestone, 0, len(milestones))
	pollCount := 0

	for len(remaining) > 0 {
		select {
		case <-ctx.Done():
			log.Info("context cancelled while waiting for milestones",
				"pending", milestoneNames(remaining),
				"elapsed", fmt.Sprintf("%.1fs", time.Since(phaseStart).Seconds()))
			return nil, ctx.Err()
		case <-time.After(t.pollInterval):
		}

		pollCount++
		elapsed := fmt.Sprintf("%.1fs", time.Since(phaseStart).Seconds())
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
				log.Info("milestone reached", "milestone", def.Name,
					"elapsed", elapsed,
					"remaining", len(remaining)-1)
			} else {
				if reporter, ok := def.Condition.(ProgressReporter); ok && pollCount%5 == 0 {
					log.Info("milestone pending", "milestone", def.Name,
						"progress", reporter.Progress(ctx),
						"elapsed", elapsed)
				}
				stillPending = append(stillPending, def)
			}
		}
		remaining = stillPending
	}

	return reached, nil
}
