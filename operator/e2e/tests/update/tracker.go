//go:build e2e

// /*
// Copyright 2025 The Grove Authors.
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

package update

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

// podEvent represents a pod lifecycle event during an update
type podEvent struct {
	eventType watch.EventType
	pod       *corev1.Pod
	timestamp time.Time
}

// updateTracker tracks pod events during updates (rolling, ondelete, etc.)
type updateTracker struct {
	events  []podEvent
	mu      sync.Mutex
	watcher watch.Interface
	cancel  context.CancelFunc
}

// newUpdateTracker creates a new update tracker
func newUpdateTracker() *updateTracker {
	return &updateTracker{}
}

// start begins watching pod events and blocks until the watcher is ready.
// Uses tc.Ctx, tc.Client, tc.Namespace, and tc.Workload.GetLabelSelector() for watch configuration.
func (t *updateTracker) start(tc *testctx.TestContext) error {
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

// stop stops the watcher and cleans up resources
func (t *updateTracker) stop() {
	if t.watcher != nil {
		t.watcher.Stop()
	}
	if t.cancel != nil {
		t.cancel()
	}
}

func (t *updateTracker) recordEvent(eventType watch.EventType, pod *corev1.Pod) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, podEvent{
		eventType: eventType,
		pod:       pod.DeepCopy(),
		timestamp: time.Now(),
	})
}

// getEvents returns a copy of all recorded events
func (t *updateTracker) getEvents() []podEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]podEvent{}, t.events...)
}
