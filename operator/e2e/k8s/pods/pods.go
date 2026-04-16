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

	"github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
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

	return wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		totalPods := 0
		readyPods := 0

		for _, namespace := range namespaces {
			pods, err := pm.clients.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: labelSelector,
			})
			if err != nil {
				pm.logger.Errorf("Failed to list pods in namespace %s: %v", namespace, err)
				return false, nil
			}

			for _, pod := range pods.Items {
				totalPods++
				if kubeutils.IsPodReady(&pod) {
					readyPods++
				}
			}
		}

		if totalPods == 0 {
			pm.logger.Debug("No pods found yet, resources may still be creating pods...")
			return false, nil
		}

		if expectedCount > 0 && totalPods != expectedCount {
			pm.logger.Debugf("Expected %d pods but found %d pods", expectedCount, totalPods)
			return false, nil
		}

		if readyPods < totalPods {
			pm.logger.Debugf("Waiting for %d more pods to become ready...", totalPods-readyPods)
		}

		return readyPods == totalPods, nil
	})
}

// WaitForReadyInNamespace waits for pods to be ready in a single namespace.
// expectedCount is the expected number of pods (pass 0 to skip count validation).
func (pm *PodManager) WaitForReadyInNamespace(ctx context.Context, namespace string, expectedCount int, timeout, interval time.Duration) error {
	return pm.WaitForReady(ctx, []string{namespace}, "", expectedCount, timeout, interval)
}

// WaitForCount waits for a specific number of pods to be created with the given label selector.
// Returns the pod list once the expected count is reached.
func (pm *PodManager) WaitForCount(ctx context.Context, namespace, labelSelector string, expectedCount int, timeout, interval time.Duration) (*v1.PodList, error) {
	var pods *v1.PodList
	err := k8s.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		var err error
		pods, err = pm.List(ctx, namespace, labelSelector)
		if err != nil {
			return false, err
		}
		return len(pods.Items) == expectedCount, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to wait for %d pods with selector %q in namespace %s: %w", expectedCount, labelSelector, namespace, err)
	}
	return pods, nil
}

// WaitForCountAndPhases waits for pods to reach specific total count and phase counts.
// Pass -1 for any count you want to skip verification.
func (pm *PodManager) WaitForCountAndPhases(ctx context.Context, namespace, labelSelector string, expectedTotal, expectedRunning, expectedPending int, timeout, interval time.Duration) error {
	return k8s.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		pods, err := pm.List(ctx, namespace, labelSelector)
		if err != nil {
			return false, err
		}

		count := CountPodsByPhase(pods)

		totalMatch := expectedTotal < 0 || count.Total == expectedTotal
		runningMatch := expectedRunning < 0 || count.Running == expectedRunning
		pendingMatch := expectedPending < 0 || count.Pending == expectedPending

		return totalMatch && runningMatch && pendingMatch, nil
	})
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

// InitContainerNames returns the names of all init containers in a pod.
func InitContainerNames(pod v1.Pod) []string {
	names := make([]string, len(pod.Spec.InitContainers))
	for i, c := range pod.Spec.InitContainers {
		names[i] = c.Name
	}
	return names
}
