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

package tests

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// podEvent represents a pod lifecycle event during rolling update
type podEvent struct {
	Type      watch.EventType
	Pod       *corev1.Pod
	Timestamp time.Time
}

// rollingUpdateTracker tracks pod events during rolling update
type rollingUpdateTracker struct {
	events  []podEvent
	mu      sync.Mutex
	watcher watch.Interface
	cancel  context.CancelFunc
	ready   chan struct{}
}

// newRollingUpdateTracker creates a new rolling update tracker
func newRollingUpdateTracker() *rollingUpdateTracker {
	return &rollingUpdateTracker{
		ready: make(chan struct{}),
	}
}

// Start begins watching pod events.
// Uses tc.Ctx, tc.Clientset, tc.Namespace, and tc.getLabelSelector() for watch configuration.
func (t *rollingUpdateTracker) Start(tc TestContext) error {
	watcherCtx, cancel := context.WithCancel(tc.Ctx)
	t.cancel = cancel

	watcher, err := tc.Clientset.CoreV1().Pods(tc.Namespace).Watch(watcherCtx, metav1.ListOptions{
		LabelSelector: tc.getLabelSelector(),
	})
	if err != nil {
		cancel()
		return err
	}
	t.watcher = watcher

	go func() {
		// Signal that the watcher goroutine is running and ready to receive events
		close(t.ready)

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

	return nil
}

// Stop stops the watcher and cleans up resources
func (t *rollingUpdateTracker) Stop() {
	if t.watcher != nil {
		t.watcher.Stop()
	}
	if t.cancel != nil {
		t.cancel()
	}
}

// WaitForReady waits for the watcher goroutine to be running and ready to receive events
// This prevents a race condition where early pod events could be missed
func (t *rollingUpdateTracker) WaitForReady() error {
	select {
	case <-t.ready:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout waiting for watcher to be ready")
	}
}

func (t *rollingUpdateTracker) recordEvent(eventType watch.EventType, pod *corev1.Pod) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, podEvent{
		Type:      eventType,
		Pod:       pod.DeepCopy(),
		Timestamp: time.Now(),
	})
}

func (t *rollingUpdateTracker) getEvents() []podEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]podEvent{}, t.events...)
}

