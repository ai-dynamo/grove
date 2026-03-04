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

package condition

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"
	ctrl "sigs.k8s.io/controller-runtime"

	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
)

type milestoneTimestamp struct {
	mu    sync.RWMutex
	value time.Time
	valid bool
}

func (m *milestoneTimestamp) set(ts time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.value = ts
	m.valid = true
}

func (m *milestoneTimestamp) get() (time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.value, m.valid
}

type podObservation struct {
	creationTimestamp        time.Time
	ready                    bool
	readyTransitionTimestamp time.Time
	terminating              bool
	deletionTimestamp        time.Time
}

type podMetrics struct {
	totalPods               int
	readyPods               int
	terminatingPods         int
	latestCreationTimestamp time.Time
	latestReadyTransition   time.Time
	earliestReadyTransition time.Time
	latestDeletionTimestamp time.Time
}

type podWatcherState struct {
	clientset     kubernetes.Interface
	namespace     string
	labelSelector string

	startOnce sync.Once
	readyOnce sync.Once
	readyCh   chan struct{}

	mu       sync.RWMutex
	pods     map[string]podObservation
	watchErr error
}

func (w *podWatcherState) ensureStarted(ctx context.Context) error {
	if w.clientset == nil {
		return fmt.Errorf("clientset cannot be nil")
	}

	w.startOnce.Do(func() {
		w.readyCh = make(chan struct{})
		w.pods = make(map[string]podObservation)

		go w.run(ctx)
	})

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for pod watch sync: %w", ctx.Err())
	case <-w.readyCh:
	}

	if err := w.getWatchErr(); err != nil {
		return fmt.Errorf("pod watch startup: %w", err)
	}

	return nil
}

func (w *podWatcherState) run(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx).WithValues("namespace", w.namespace, "labelSelector", w.labelSelector)
	log.V(1).Info("starting pod watcher")

	closeReady := func() {
		w.readyOnce.Do(func() {
			close(w.readyCh)
		})
	}

	lw := &cache.ListWatch{
		ListWithContextFunc: func(c context.Context, opts metav1.ListOptions) (runtime.Object, error) {
			opts.LabelSelector = w.labelSelector
			list, listErr := w.clientset.CoreV1().Pods(w.namespace).List(c, opts)
			if listErr != nil {
				return nil, fmt.Errorf("list pods for watch sync: %w", listErr)
			}
			return list, nil
		},
		WatchFuncWithContext: func(c context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			opts.LabelSelector = w.labelSelector
			stream, watchErr := w.clientset.CoreV1().Pods(w.namespace).Watch(c, opts)
			if watchErr != nil {
				return nil, fmt.Errorf("watch pods: %w", watchErr)
			}
			return stream, nil
		},
	}

	precondition := func(store cache.Store) (bool, error) {
		w.replaceFromStore(store)
		closeReady()
		return false, nil
	}

	condition := func(event watch.Event) (bool, error) {
		if event.Type == watch.Error {
			if event.Object == nil {
				return false, fmt.Errorf("watch event error: nil error object")
			}
			return false, fmt.Errorf("watch event error: %w", apierrors.FromObject(event.Object))
		}

		if err := w.applyEvent(event); err != nil {
			return false, fmt.Errorf("apply watch event: %w", err)
		}
		return false, nil
	}

	_, err := watchtools.UntilWithSync(ctx, lw, &corev1.Pod{}, precondition, condition)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		w.setWatchErr(fmt.Errorf("watch loop terminated: %w", err))
		log.Error(err, "pod watch terminated unexpectedly")
	}

	closeReady()
	log.V(1).Info("pod watcher stopped")
}

func (w *podWatcherState) replaceFromStore(store cache.Store) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pods = make(map[string]podObservation)
	for _, obj := range store.List() {
		pod, ok := obj.(*corev1.Pod)
		if !ok || pod == nil {
			continue
		}
		w.pods[podKey(pod)] = observationFromPod(pod)
	}
}

func (w *podWatcherState) applyEvent(event watch.Event) error {
	if event.Object == nil {
		return nil
	}

	pod := podFromWatchObject(event.Object)
	if pod == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	key := podKey(pod)
	switch event.Type {
	case watch.Deleted:
		delete(w.pods, key)
	default:
		w.pods[key] = observationFromPod(pod)
	}

	return nil
}

func (w *podWatcherState) setWatchErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.watchErr = err
}

func (w *podWatcherState) getWatchErr() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.watchErr
}

func (w *podWatcherState) metrics() podMetrics {
	w.mu.RLock()
	defer w.mu.RUnlock()

	m := podMetrics{}
	for _, observation := range w.pods {
		m.totalPods++
		if observation.creationTimestamp.After(m.latestCreationTimestamp) {
			m.latestCreationTimestamp = observation.creationTimestamp
		}

		if observation.ready {
			m.readyPods++
			if observation.readyTransitionTimestamp.After(m.latestReadyTransition) {
				m.latestReadyTransition = observation.readyTransitionTimestamp
			}
			if m.earliestReadyTransition.IsZero() || observation.readyTransitionTimestamp.Before(m.earliestReadyTransition) {
				m.earliestReadyTransition = observation.readyTransitionTimestamp
			}
		}

		if observation.terminating {
			m.terminatingPods++
			if observation.deletionTimestamp.After(m.latestDeletionTimestamp) {
				m.latestDeletionTimestamp = observation.deletionTimestamp
			}
		}
	}

	return m
}

func podFromWatchObject(obj any) *corev1.Pod {
	pod, ok := obj.(*corev1.Pod)
	if ok {
		return pod
	}

	tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
	if !ok {
		return nil
	}

	pod, ok = tombstone.Obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	return pod
}

func observationFromPod(pod *corev1.Pod) podObservation {
	ready, transition := podReadyStatus(pod)

	observation := podObservation{
		creationTimestamp:        pod.CreationTimestamp.Time,
		ready:                    ready,
		readyTransitionTimestamp: transition,
	}
	if pod.DeletionTimestamp != nil {
		observation.terminating = true
		observation.deletionTimestamp = pod.DeletionTimestamp.Time
	}
	return observation
}

func podKey(pod *corev1.Pod) string {
	if pod.UID != "" {
		return string(pod.UID)
	}
	return pod.Namespace + "/" + pod.Name
}

func podReadyStatus(pod *corev1.Pod) (bool, time.Time) {
	if !kubeutils.IsPodReady(pod) {
		return false, time.Time{}
	}

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true, condition.LastTransitionTime.Time
		}
	}
	return true, time.Time{}
}

type podConditionBase struct {
	watchOnce sync.Once
	watch     *podWatcherState
	marker    milestoneTimestamp
}

func (b *podConditionBase) ensureWatcher(ctx context.Context, cs kubernetes.Interface, ns, sel string) (podMetrics, error) {
	b.watchOnce.Do(func() {
		b.watch = &podWatcherState{clientset: cs, namespace: ns, labelSelector: sel}
	})

	if err := b.watch.ensureStarted(ctx); err != nil {
		return podMetrics{}, fmt.Errorf("ensure pod watcher started: %w", err)
	}

	return b.watch.metrics(), nil
}

func (b *podConditionBase) milestoneTimestamp() (time.Time, bool) {
	return b.marker.get()
}

// PodsCreatedCondition checks if at least ExpectedCount matching pods exist.
type PodsCreatedCondition struct {
	Clientset     kubernetes.Interface
	Namespace     string
	LabelSelector string
	ExpectedCount int

	base podConditionBase
}

// Met returns true once the expected pod count exists.
func (c *PodsCreatedCondition) Met(ctx context.Context) (bool, error) {
	if c.ExpectedCount < 0 {
		return false, fmt.Errorf("expected count cannot be negative")
	}

	metrics, err := c.base.ensureWatcher(ctx, c.Clientset, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	if metrics.totalPods < c.ExpectedCount {
		return false, nil
	}

	ts := metrics.latestCreationTimestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	c.base.marker.set(ts)
	return true, nil
}

// MilestoneTimestamp returns the timestamp when this condition was met.
func (c *PodsCreatedCondition) MilestoneTimestamp() (time.Time, bool) {
	return c.base.milestoneTimestamp()
}

// PodsReadyCondition checks if at least ExpectedCount matching pods are Ready.
type PodsReadyCondition struct {
	Clientset     kubernetes.Interface
	Namespace     string
	LabelSelector string
	ExpectedCount int

	base podConditionBase
}

// Met returns true once the expected number of pods are Ready.
func (c *PodsReadyCondition) Met(ctx context.Context) (bool, error) {
	if c.ExpectedCount < 0 {
		return false, fmt.Errorf("expected count cannot be negative")
	}

	metrics, err := c.base.ensureWatcher(ctx, c.Clientset, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	if metrics.readyPods < c.ExpectedCount {
		return false, nil
	}

	ts := metrics.latestReadyTransition
	if ts.IsZero() {
		ts = time.Now()
	}
	c.base.marker.set(ts)
	return true, nil
}

// MilestoneTimestamp returns the timestamp when this condition was met.
func (c *PodsReadyCondition) MilestoneTimestamp() (time.Time, bool) {
	return c.base.milestoneTimestamp()
}

// FirstPodReadyCondition checks if at least one matching pod is Ready.
type FirstPodReadyCondition struct {
	Clientset     kubernetes.Interface
	Namespace     string
	LabelSelector string

	base podConditionBase
}

// Met returns true once any pod is Ready.
func (c *FirstPodReadyCondition) Met(ctx context.Context) (bool, error) {
	metrics, err := c.base.ensureWatcher(ctx, c.Clientset, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	if metrics.readyPods == 0 {
		return false, nil
	}

	ts := metrics.earliestReadyTransition
	if ts.IsZero() {
		ts = time.Now()
	}
	c.base.marker.set(ts)
	return true, nil
}

// MilestoneTimestamp returns the timestamp when this condition was met.
func (c *FirstPodReadyCondition) MilestoneTimestamp() (time.Time, bool) {
	return c.base.milestoneTimestamp()
}

// PodsGoneCondition checks that no matching pods remain.
type PodsGoneCondition struct {
	Clientset     kubernetes.Interface
	Namespace     string
	LabelSelector string

	base podConditionBase
}

// Met returns true once zero pods match the selector.
func (c *PodsGoneCondition) Met(ctx context.Context) (bool, error) {
	metrics, err := c.base.ensureWatcher(ctx, c.Clientset, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	if metrics.totalPods != 0 {
		return false, nil
	}

	c.base.marker.set(time.Now())
	return true, nil
}

// MilestoneTimestamp returns the timestamp when this condition was met.
func (c *PodsGoneCondition) MilestoneTimestamp() (time.Time, bool) {
	return c.base.milestoneTimestamp()
}

// PodsTerminatingCondition checks if at least ExpectedCount pods are terminating.
type PodsTerminatingCondition struct {
	Clientset     kubernetes.Interface
	Namespace     string
	LabelSelector string
	ExpectedCount int

	base podConditionBase
}

// Met returns true once expected pods have DeletionTimestamp set.
func (c *PodsTerminatingCondition) Met(ctx context.Context) (bool, error) {
	if c.ExpectedCount < 0 {
		return false, fmt.Errorf("expected count cannot be negative")
	}

	metrics, err := c.base.ensureWatcher(ctx, c.Clientset, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	if metrics.terminatingPods < c.ExpectedCount {
		return false, nil
	}

	ts := metrics.latestDeletionTimestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	c.base.marker.set(ts)
	return true, nil
}

// MilestoneTimestamp returns the timestamp when this condition was met.
func (c *PodsTerminatingCondition) MilestoneTimestamp() (time.Time, bool) {
	return c.base.milestoneTimestamp()
}
