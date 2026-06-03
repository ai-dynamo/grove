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

package k8s

import (
	"context"
	"strings"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// FetchPodLogs returns a FetchFunc that retrieves all container logs for a pod,
// including both current and previous (terminated) container logs.
func FetchPodLogs(clientset kubernetes.Interface, namespace, podName string) waiter.FetchFunc[[]byte] {
	return func(ctx context.Context) ([]byte, error) {
		var allLogs []byte
		for _, previous := range []bool{false, true} {
			logs, err := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
				Previous: previous,
			}).DoRaw(ctx)
			if err == nil {
				allLogs = append(allLogs, logs...)
			}
		}
		return allLogs, nil
	}
}

// LogContains returns a Predicate that checks whether the log output contains all specified substrings.
func LogContains(substrings ...string) waiter.Predicate[[]byte] {
	return func(logs []byte) bool {
		logText := string(logs)
		for _, s := range substrings {
			if !strings.Contains(logText, s) {
				return false
			}
		}
		return true
	}
}

// WaitForPodLogContains polls pod logs until all specified substrings are found.
func WaitForPodLogContains(ctx context.Context, clientset kubernetes.Interface, namespace, podName string, timeout, interval time.Duration, substrings ...string) error {
	w := waiter.New[[]byte]().WithTimeout(timeout).WithInterval(interval)
	return w.WaitUntil(ctx, FetchPodLogs(clientset, namespace, podName), LogContains(substrings...))
}
