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

//go:build e2e

package utils

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/testctx"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// PodEvent represents a pod lifecycle event during an update.
type PodEvent struct {
	// EventType is the Kubernetes watch event kind.
	EventType watch.EventType
	// Pod is the pod snapshot recorded for the event.
	Pod *corev1.Pod
	// Timestamp is when the watch event was recorded.
	Timestamp time.Time
}

// UpdateTracker tracks pod events during updates (rolling, ondelete, etc.).
type UpdateTracker struct {
	events  []PodEvent
	mu      sync.Mutex // mu protects events.
	watcher watch.Interface
	cancel  context.CancelFunc
}

// NewUpdateTracker creates a new update tracker.
func NewUpdateTracker() *UpdateTracker {
	return &UpdateTracker{}
}

// Start begins watching pod events and blocks until the watcher is ready.
// Uses tc.Ctx, tc.Client, tc.Namespace, and tc.Workload.GetLabelSelector() for watch configuration.
// The caller must call Stop after a successful Start to clean up the watch.
func (t *UpdateTracker) Start(tc *testctx.TestContext) error {
	watcherCtx, cancel := context.WithCancel(tc.Ctx)
	t.cancel = cancel

	watcher, err := tc.Client.WatchPods(watcherCtx, tc.Namespace, metav1.ListOptions{
		LabelSelector: tc.Workload.GetLabelSelector(),
	})
	if err != nil {
		cancel()
		return err
	}
	t.watcher = watcher

	ready := make(chan struct{})
	go func() {
		// Signal that the watcher goroutine is running and ready to receive events
		close(ready)

		for {
			select {
			case <-watcherCtx.Done():
				return
			case event, ok := <-watcher.ResultChan():
				if !ok {
					return
				}
				if pod, ok := event.Object.(*corev1.Pod); ok {
					t.recordEvent(event.Type, pod)
				}
			}
		}
	}()

	// Wait for the watcher goroutine to be ready
	select {
	case <-ready:
		return nil
	case <-time.After(5 * time.Second):
		cancel()
		return fmt.Errorf("timeout waiting for watcher to be ready")
	}
}

func (t *UpdateTracker) recordEvent(eventType watch.EventType, pod *corev1.Pod) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, PodEvent{
		EventType: eventType,
		Pod:       pod.DeepCopy(),
		Timestamp: time.Now(),
	})
}

// Stop stops the watcher and cleans up resources.
func (t *UpdateTracker) Stop() {
	if t.watcher != nil {
		t.watcher.Stop()
	}
	if t.cancel != nil {
		t.cancel()
	}
}

// Events returns a copy of the recorded event slice. Pod snapshots are shared.
func (t *UpdateTracker) Events() []PodEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]PodEvent{}, t.events...)
}
