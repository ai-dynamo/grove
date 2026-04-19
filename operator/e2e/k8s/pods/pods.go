//go:build e2e

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

package pods

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodPhaseCount holds counts of pods by phase.
type PodPhaseCount struct {
	Running int
	Pending int
	Failed  int
	Unknown int
	Total   int
}

// PodManager provides pod operations using pre-created Kubernetes clients.
type PodManager struct {
	clients *clients.Clients
	logger  *log.Logger
}

// NewPodManager creates a PodManager bound to the given clients.
func NewPodManager(c *clients.Clients, logger *log.Logger) *PodManager {
	return &PodManager{clients: c, logger: logger}
}

// List lists pods in a namespace with an optional label selector.
func (pm *PodManager) List(ctx context.Context, namespace, labelSelector string) (*v1.PodList, error) {
	pods, err := pm.clients.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods in namespace %s: %w", namespace, err)
	}
	return pods, nil
}

// WaitForReady waits for pods to be ready in the specified namespaces.
// labelSelector is optional (pass empty string for all pods).
// expectedCount is the expected number of pods (pass 0 to skip count validation).
func (pm *PodManager) WaitForReady(ctx context.Context, namespaces []string, labelSelector string, expectedCount int, timeout, interval time.Duration) error {
	if len(namespaces) == 0 {
		namespaces = []string{"default"}
	}

	pm.logger.Debugf("Waiting for pods to be ready in namespaces: %v", namespaces)

	fetchPods := waiter.FetchFunc[*v1.PodList](func(ctx context.Context) (*v1.PodList, error) {
		var allPods v1.PodList
		for _, namespace := range namespaces {
			pods, err := pm.clients.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: labelSelector,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to list pods in namespace %s: %w", namespace, err)
			}
			allPods.Items = append(allPods.Items, pods.Items...)
		}
		return &allPods, nil
	})
	w := waiter.New[*v1.PodList]().
		WithTimeout(timeout).
		WithInterval(interval).
		WithRetryOnError().
		WithLogger(pm.logger)
	_, err := w.WaitFor(ctx, fetchPods, AllReady(expectedCount))
	return err
}

// WaitForReadyInNamespace waits for pods to be ready in a single namespace.
// expectedCount is the expected number of pods (pass 0 to skip count validation).
func (pm *PodManager) WaitForReadyInNamespace(ctx context.Context, namespace string, expectedCount int, timeout, interval time.Duration) error {
	return pm.WaitForReady(ctx, []string{namespace}, "", expectedCount, timeout, interval)
}

// WaitForCount waits for a specific number of pods to be created with the given label selector.
// Returns the pod list once the expected count is reached.
func (pm *PodManager) WaitForCount(ctx context.Context, namespace, labelSelector string, expectedCount int, timeout, interval time.Duration) (*v1.PodList, error) {
	fetchPods := pm.FetchFunc(ctx, namespace, labelSelector)
	w := waiter.New[*v1.PodList]().
		WithTimeout(timeout).
		WithInterval(interval)
	pods, err := w.WaitFor(ctx, fetchPods, CountEquals(expectedCount))
	if err != nil {
		return nil, fmt.Errorf("failed to wait for %d pods with selector %q in namespace %s: %w", expectedCount, labelSelector, namespace, err)
	}
	return pods, nil
}

// WaitForCountAndPhases waits for pods to reach specific total count and phase counts.
// Pass -1 for any count you want to skip verification.
func (pm *PodManager) WaitForCountAndPhases(ctx context.Context, namespace, labelSelector string, expectedTotal, expectedRunning, expectedPending int, timeout, interval time.Duration) error {
	fetchPods := pm.FetchFunc(ctx, namespace, labelSelector)
	w := waiter.New[*v1.PodList]().
		WithTimeout(timeout).
		WithInterval(interval)
	_, err := w.WaitFor(ctx, fetchPods, MatchPhases(expectedTotal, expectedRunning, expectedPending))
	return err
}

// WaitForPhases waits for pods to reach specific running and pending counts.
func (pm *PodManager) WaitForPhases(ctx context.Context, namespace, labelSelector string, expectedRunning, expectedPending int, timeout, interval time.Duration) error {
	fetchPods := pm.FetchFunc(ctx, namespace, labelSelector)
	w := waiter.New[*v1.PodList]().WithTimeout(timeout).WithInterval(interval)
	_, err := w.WaitFor(ctx, fetchPods, MatchPhases(-1, expectedRunning, expectedPending))
	return err
}

// WaitForReadyCount waits for a specific number of ready pods.
func (pm *PodManager) WaitForReadyCount(ctx context.Context, namespace, labelSelector string, expectedReady int, timeout, interval time.Duration) error {
	fetchPods := pm.FetchFunc(ctx, namespace, labelSelector)
	w := waiter.New[*v1.PodList]().WithTimeout(timeout).WithInterval(interval)
	_, err := w.WaitFor(ctx, fetchPods, ReadyCount(expectedReady))
	return err
}

// WaitForAllPending waits until all pods are in Pending phase.
func (pm *PodManager) WaitForAllPending(ctx context.Context, namespace, labelSelector string, timeout, interval time.Duration) error {
	fetchPods := pm.FetchFunc(ctx, namespace, labelSelector)
	w := waiter.New[*v1.PodList]().WithTimeout(timeout).WithInterval(interval)
	_, err := w.WaitFor(ctx, fetchPods, AllPending())
	return err
}

// WaitForUnschedulableEvents waits until all pending pods have Unschedulable or PodGrouperWarning events.
// Pass expectedPendingCount > 0 to validate the pending pod count; pass 0 to skip.
func (pm *PodManager) WaitForUnschedulableEvents(ctx context.Context, namespace, labelSelector string, expectedPendingCount int, timeout, interval time.Duration) error {
	fetchPods := pm.FetchFunc(ctx, namespace, labelSelector)
	w := waiter.New[*v1.PodList]().WithTimeout(timeout).WithInterval(interval)
	_, err := w.WaitFor(ctx, fetchPods, HasUnschedulableEvents(ctx, pm.clients.Clientset, namespace, expectedPendingCount))
	return err
}

// WaitForMatchingPhases waits for pods to reach expected total and pending counts, and returns the phase counts.
func (pm *PodManager) WaitForMatchingPhases(ctx context.Context, namespace, labelSelector string, expectedTotal, expectedPending int, timeout, interval time.Duration) (PodPhaseCount, error) {
	fetchPods := pm.FetchFunc(ctx, namespace, labelSelector)
	w := waiter.New[*v1.PodList]().WithTimeout(timeout).WithInterval(interval)
	podList, err := w.WaitFor(ctx, fetchPods, MatchPhases(expectedTotal, -1, expectedPending))
	if err != nil {
		// Return last known state for error reporting.
		if lastPods, listErr := pm.List(ctx, namespace, labelSelector); listErr == nil {
			return CountPodsByPhase(lastPods), err
		}
		return PodPhaseCount{}, err
	}
	return CountPodsByPhase(podList), nil
}

// CountPodsByPhase counts pods by their phase and returns a PodPhaseCount.
func CountPodsByPhase(pods *v1.PodList) PodPhaseCount {
	count := PodPhaseCount{
		Total: len(pods.Items),
	}
	for _, pod := range pods.Items {
		switch pod.Status.Phase {
		case v1.PodRunning:
			count.Running++
		case v1.PodPending:
			count.Pending++
		case v1.PodFailed:
			count.Failed++
		case v1.PodUnknown:
			count.Unknown++
		}
	}
	return count
}

// CountReady counts the number of ready pods (Running + Ready condition).
func CountReady(pods *v1.PodList) int {
	readyCount := 0
	for _, pod := range pods.Items {
		if kubeutils.IsPodReady(&pod) {
			readyCount++
		}
	}
	return readyCount
}

// VerifyPhases verifies that the pod counts match the expected values.
// Pass -1 for any count you want to skip verification.
func VerifyPhases(pods *v1.PodList, expectedRunning, expectedPending int) error {
	count := CountPodsByPhase(pods)

	if expectedRunning >= 0 && count.Running != expectedRunning {
		return fmt.Errorf("expected %d running pods but found %d", expectedRunning, count.Running)
	}

	if expectedPending >= 0 && count.Pending != expectedPending {
		return fmt.Errorf("expected %d pending pods but found %d", expectedPending, count.Pending)
	}

	return nil
}

// WaitForFailedPod polls until a pod matching the label selector is found that is NOT Ready
// and has a terminated or restarted container. Returns the failed pod.
func (pm *PodManager) WaitForFailedPod(ctx context.Context, namespace, labelSelector string, timeout, interval time.Duration) (*v1.Pod, error) {
	fetchFailedPod := waiter.FetchFunc[*v1.Pod](func(ctx context.Context) (*v1.Pod, error) {
		podList, err := pm.List(ctx, namespace, labelSelector)
		if err != nil || len(podList.Items) == 0 {
			return nil, nil
		}
		for i := range podList.Items {
			pod := &podList.Items[i]
			if kubeutils.IsPodReady(pod) {
				continue
			}
			for _, status := range pod.Status.ContainerStatuses {
				if status.State.Terminated != nil ||
					status.LastTerminationState.Terminated != nil ||
					status.RestartCount > 0 {
					return pod, nil
				}
			}
		}
		return nil, nil
	})
	w := waiter.New[*v1.Pod]().
		WithTimeout(timeout).
		WithInterval(interval)
	return w.WaitFor(ctx, fetchFailedPod, waiter.IsNotZero[*v1.Pod])
}

// InitContainerNames returns the names of all init containers in a pod.
func InitContainerNames(pod v1.Pod) []string {
	names := make([]string, len(pod.Spec.InitContainers))
	for i, c := range pod.Spec.InitContainers {
		names[i] = c.Name
	}
	return names
}
