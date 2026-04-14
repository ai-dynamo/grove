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

	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// FetchFunc returns a FetchFunc that lists pods in the given namespace with the given label selector.
func (pm *PodManager) FetchFunc(ctx context.Context, namespace, labelSelector string) waiter.FetchFunc[*v1.PodList] {
	return func(_ context.Context) (*v1.PodList, error) {
		return pm.List(ctx, namespace, labelSelector)
	}
}

// AllReady returns a Predicate that checks all pods are ready.
// Pass expectedCount > 0 to also validate the total pod count; pass 0 to skip count validation.
func AllReady(expectedCount int) waiter.Predicate[*v1.PodList] {
	return func(pods *v1.PodList) bool {
		if len(pods.Items) == 0 {
			return false
		}
		if expectedCount > 0 && len(pods.Items) != expectedCount {
			return false
		}
		for i := range pods.Items {
			if !kubeutils.IsPodReady(&pods.Items[i]) {
				return false
			}
		}
		return true
	}
}

// MatchPhases returns a Predicate that checks pod phase counts.
// Pass -1 for any count to skip that validation.
func MatchPhases(expectedTotal, expectedRunning, expectedPending int) waiter.Predicate[*v1.PodList] {
	return func(pods *v1.PodList) bool {
		count := CountPodsByPhase(pods)
		totalMatch := expectedTotal < 0 || count.Total == expectedTotal
		runningMatch := expectedRunning < 0 || count.Running == expectedRunning
		pendingMatch := expectedPending < 0 || count.Pending == expectedPending
		return totalMatch && runningMatch && pendingMatch
	}
}

// ReadyCount returns a Predicate that checks exactly n pods are ready.
func ReadyCount(expected int) waiter.Predicate[*v1.PodList] {
	return func(pods *v1.PodList) bool {
		return CountReady(pods) == expected
	}
}

// AllPending returns a Predicate that checks all pods are in Pending phase.
func AllPending() waiter.Predicate[*v1.PodList] {
	return func(pods *v1.PodList) bool {
		if len(pods.Items) == 0 {
			return false
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase != v1.PodPending {
				return false
			}
		}
		return true
	}
}

// CountEquals returns a Predicate that checks the pod list has exactly n items.
func CountEquals(n int) waiter.Predicate[*v1.PodList] {
	return func(pods *v1.PodList) bool { return len(pods.Items) == n }
}

// ExcludesPod returns a Predicate that checks a specific pod is NOT in the list.
func ExcludesPod(podName string) waiter.Predicate[*v1.PodList] {
	return func(pods *v1.PodList) bool {
		for _, pod := range pods.Items {
			if pod.Name == podName {
				return false
			}
		}
		return true
	}
}

// HasUnschedulableEvents returns a Predicate that checks all pending pods have
// Unschedulable or PodGrouperWarning events. Pass expectedPendingCount > 0 to
// also validate the pending pod count; pass 0 to skip that validation.
func HasUnschedulableEvents(ctx context.Context, clientset kubernetes.Interface, namespace string, expectedPendingCount int) waiter.Predicate[*v1.PodList] {
	return func(podList *v1.PodList) bool {
		podsWithUnschedulableEvent := 0
		pendingCount := 0
		for _, pod := range podList.Items {
			if pod.Status.Phase != v1.PodPending {
				continue
			}
			pendingCount++
			events, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
				FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", pod.Name),
			})
			if err != nil {
				return false
			}
			var mostRecentEvent *v1.Event
			for i := range events.Items {
				event := &events.Items[i]
				if mostRecentEvent == nil || event.LastTimestamp.After(mostRecentEvent.LastTimestamp.Time) {
					mostRecentEvent = event
				}
			}
			if mostRecentEvent != nil &&
				mostRecentEvent.Type == v1.EventTypeWarning &&
				((mostRecentEvent.Reason == "Unschedulable" && mostRecentEvent.Source.Component == "kai-scheduler") ||
					(mostRecentEvent.Reason == "PodGrouperWarning" && mostRecentEvent.Source.Component == "pod-grouper")) {
				podsWithUnschedulableEvent++
			}
		}
		if expectedPendingCount > 0 && pendingCount != expectedPendingCount {
			return false
		}
		return podsWithUnschedulableEvent == pendingCount
	}
}
