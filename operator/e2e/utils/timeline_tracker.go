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

package utils

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
)

const defaultTimelinePollInterval = 2 * time.Second

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

// TimelineTracker records ordered phases/milestones for a scale test.
type TimelineTracker struct {
	testStart    time.Time
	phases       []Phase
	pollInterval time.Duration
	logger       *Logger
}

// NewTimelineTracker constructs a new timeline tracker.
func NewTimelineTracker(logger *Logger) *TimelineTracker {
	return &TimelineTracker{
		testStart:    time.Now(),
		pollInterval: defaultTimelinePollInterval,
		logger:       logger,
	}
}

// WithPollInterval overrides milestone polling interval.
func (t *TimelineTracker) WithPollInterval(d time.Duration) *TimelineTracker {
	if d > 0 {
		t.pollInterval = d
	}
	return t
}

// RunPhase executes an action and records configured milestones in order.
func (t *TimelineTracker) RunPhase(
	ctx context.Context,
	name string,
	actionFn func(ctx context.Context) error,
	milestones ...MilestoneDefinition,
) error {
	if actionFn == nil {
		return fmt.Errorf("phase %q: action cannot be nil", name)
	}

	phaseStart := time.Now()
	phase := Phase{
		Name:                  name,
		StartTime:             phaseStart,
		DurationFromTestStart: phaseStart.Sub(t.testStart).Seconds(),
		Milestones:            make([]Milestone, 0, len(milestones)),
	}

	if t.logger != nil {
		t.logger.Debugf("Starting phase %q", name)
	}

	if err := actionFn(ctx); err != nil {
		return fmt.Errorf("phase %q: action failed: %w", name, err)
	}

	for _, def := range milestones {
		if def.Condition == nil {
			return fmt.Errorf("phase %q: milestone %q has nil condition", name, def.Name)
		}

		milestoneTS, err := t.waitForMilestone(ctx, def)
		if err != nil {
			return fmt.Errorf("phase %q: milestone %q: %w", name, def.Name, err)
		}

		phase.Milestones = append(phase.Milestones, Milestone{
			Name:                   def.Name,
			Timestamp:              milestoneTS,
			DurationFromPhaseStart: milestoneTS.Sub(phaseStart).Seconds(),
		})

		if t.logger != nil {
			t.logger.Debugf("Phase %q reached milestone %q at +%.3fs", name, def.Name, milestoneTS.Sub(phaseStart).Seconds())
		}
	}

	t.phases = append(t.phases, phase)
	return nil
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

type milestoneTimestampProvider interface {
	milestoneTimestamp() (time.Time, bool)
}

func (t *TimelineTracker) waitForMilestone(ctx context.Context, def MilestoneDefinition) (time.Time, error) {
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()

	for {
		met, err := def.Condition.Met(ctx)
		if err != nil {
			return time.Time{}, err
		}

		if met {
			if tsProvider, ok := def.Condition.(milestoneTimestampProvider); ok {
				if ts, ok := tsProvider.milestoneTimestamp(); ok {
					return ts, nil
				}
			}
			return time.Now(), nil
		}

		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// PodsCreatedCondition checks if at least ExpectedCount matching pods exist.
type PodsCreatedCondition struct {
	Clientset     kubernetes.Interface
	Namespace     string
	LabelSelector string
	ExpectedCount int

	mu         sync.RWMutex
	lastMetTS  time.Time
	hasLastMet bool
}

// Met returns true once the expected pod count exists.
func (c *PodsCreatedCondition) Met(ctx context.Context) (bool, error) {
	if c.Clientset == nil {
		return false, fmt.Errorf("clientset cannot be nil")
	}
	if c.ExpectedCount < 0 {
		return false, fmt.Errorf("expected count cannot be negative")
	}

	pods, err := ListPods(ctx, c.Clientset, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	if len(pods.Items) < c.ExpectedCount {
		return false, nil
	}

	c.setMilestoneTimestamp(latestPodCreationTime(pods.Items, time.Now()))
	return true, nil
}

func (c *PodsCreatedCondition) milestoneTimestamp() (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastMetTS, c.hasLastMet
}

func (c *PodsCreatedCondition) setMilestoneTimestamp(ts time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastMetTS = ts
	c.hasLastMet = true
}

// PodsReadyCondition checks if at least ExpectedCount matching pods are Ready.
type PodsReadyCondition struct {
	Clientset     kubernetes.Interface
	Namespace     string
	LabelSelector string
	ExpectedCount int

	mu         sync.RWMutex
	lastMetTS  time.Time
	hasLastMet bool
}

// Met returns true once the expected number of pods are Ready.
func (c *PodsReadyCondition) Met(ctx context.Context) (bool, error) {
	if c.Clientset == nil {
		return false, fmt.Errorf("clientset cannot be nil")
	}
	if c.ExpectedCount < 0 {
		return false, fmt.Errorf("expected count cannot be negative")
	}

	pods, err := ListPods(ctx, c.Clientset, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	readyCount := 0
	latestReadyTransition := time.Time{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !kubeutils.IsPodReady(pod) {
			continue
		}
		readyCount++

		transition := readyConditionLastTransitionTime(pod)
		if transition.IsZero() {
			transition = time.Now()
		}
		if transition.After(latestReadyTransition) {
			latestReadyTransition = transition
		}
	}

	if readyCount < c.ExpectedCount {
		return false, nil
	}

	if latestReadyTransition.IsZero() {
		latestReadyTransition = time.Now()
	}
	c.setMilestoneTimestamp(latestReadyTransition)
	return true, nil
}

func (c *PodsReadyCondition) milestoneTimestamp() (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastMetTS, c.hasLastMet
}

func (c *PodsReadyCondition) setMilestoneTimestamp(ts time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastMetTS = ts
	c.hasLastMet = true
}

// FirstPodReadyCondition checks if at least one matching pod is Ready.
type FirstPodReadyCondition struct {
	Clientset     kubernetes.Interface
	Namespace     string
	LabelSelector string

	mu         sync.RWMutex
	lastMetTS  time.Time
	hasLastMet bool
}

// Met returns true once any pod is Ready.
func (c *FirstPodReadyCondition) Met(ctx context.Context) (bool, error) {
	if c.Clientset == nil {
		return false, fmt.Errorf("clientset cannot be nil")
	}

	pods, err := ListPods(ctx, c.Clientset, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	firstReady := time.Time{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !kubeutils.IsPodReady(pod) {
			continue
		}

		transition := readyConditionLastTransitionTime(pod)
		if transition.IsZero() {
			transition = time.Now()
		}

		if firstReady.IsZero() || transition.Before(firstReady) {
			firstReady = transition
		}
	}

	if firstReady.IsZero() {
		return false, nil
	}

	c.setMilestoneTimestamp(firstReady)
	return true, nil
}

func (c *FirstPodReadyCondition) milestoneTimestamp() (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastMetTS, c.hasLastMet
}

func (c *FirstPodReadyCondition) setMilestoneTimestamp(ts time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastMetTS = ts
	c.hasLastMet = true
}

// PodsGoneCondition checks that no matching pods remain.
type PodsGoneCondition struct {
	Clientset     kubernetes.Interface
	Namespace     string
	LabelSelector string

	mu         sync.RWMutex
	lastMetTS  time.Time
	hasLastMet bool
}

// Met returns true once zero pods match the selector.
func (c *PodsGoneCondition) Met(ctx context.Context) (bool, error) {
	if c.Clientset == nil {
		return false, fmt.Errorf("clientset cannot be nil")
	}

	pods, err := ListPods(ctx, c.Clientset, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	if len(pods.Items) != 0 {
		return false, nil
	}

	c.setMilestoneTimestamp(time.Now())
	return true, nil
}

func (c *PodsGoneCondition) milestoneTimestamp() (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastMetTS, c.hasLastMet
}

func (c *PodsGoneCondition) setMilestoneTimestamp(ts time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastMetTS = ts
	c.hasLastMet = true
}

// PodsTerminatingCondition checks if at least ExpectedCount pods are terminating.
type PodsTerminatingCondition struct {
	Clientset     kubernetes.Interface
	Namespace     string
	LabelSelector string
	ExpectedCount int

	mu         sync.RWMutex
	lastMetTS  time.Time
	hasLastMet bool
}

// Met returns true once expected pods have DeletionTimestamp set.
func (c *PodsTerminatingCondition) Met(ctx context.Context) (bool, error) {
	if c.Clientset == nil {
		return false, fmt.Errorf("clientset cannot be nil")
	}
	if c.ExpectedCount < 0 {
		return false, fmt.Errorf("expected count cannot be negative")
	}

	pods, err := ListPods(ctx, c.Clientset, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	terminating := 0
	latestDeletionTS := time.Time{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp == nil {
			continue
		}

		terminating++
		ts := pod.DeletionTimestamp.Time
		if ts.After(latestDeletionTS) {
			latestDeletionTS = ts
		}
	}

	if terminating < c.ExpectedCount {
		return false, nil
	}

	if latestDeletionTS.IsZero() {
		latestDeletionTS = time.Now()
	}
	c.setMilestoneTimestamp(latestDeletionTS)
	return true, nil
}

func (c *PodsTerminatingCondition) milestoneTimestamp() (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastMetTS, c.hasLastMet
}

func (c *PodsTerminatingCondition) setMilestoneTimestamp(ts time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastMetTS = ts
	c.hasLastMet = true
}

func readyConditionLastTransitionTime(pod *corev1.Pod) time.Time {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return cond.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

func latestPodCreationTime(pods []corev1.Pod, fallback time.Time) time.Time {
	latest := time.Time{}
	for i := range pods {
		ts := pods[i].CreationTimestamp.Time
		if ts.IsZero() {
			continue
		}
		if ts.After(latest) {
			latest = ts
		}
	}
	if latest.IsZero() {
		return fallback
	}
	return latest
}
