//go:build e2e

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

package pods

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// PodCountSampler polls a pod list on an interval and records the maximum number of pods matching a
// predicate. Sampling via a list gives a consistent snapshot per tick, so unlike a watch stream it is
// immune to event reordering. It is useful for observing a transient state, for example how many pods
// are Terminating, when that state persists longer than the sample interval.
type PodCountSampler struct {
	mu       sync.Mutex
	maxCount int
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewPodCountSampler creates a PodCountSampler.
func NewPodCountSampler() *PodCountSampler {
	return &PodCountSampler{done: make(chan struct{})}
}

// Start begins sampling in the background until Stop is called. On each tick it lists pods and counts
// those for which match returns true, tracking the maximum. A list error skips that tick.
func (s *PodCountSampler) Start(ctx context.Context, interval time.Duration, list func(context.Context) ([]corev1.Pod, error), match func(*corev1.Pod) bool) {
	sampleCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-sampleCtx.Done():
				return
			case <-ticker.C:
				podList, err := list(sampleCtx)
				if err != nil {
					continue
				}
				count := 0
				for i := range podList {
					if match(&podList[i]) {
						count++
					}
				}
				s.mu.Lock()
				if count > s.maxCount {
					s.maxCount = count
				}
				s.mu.Unlock()
			}
		}
	}()
}

// Stop ends sampling and returns the maximum matching count observed.
func (s *PodCountSampler) Stop() int {
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxCount
}
