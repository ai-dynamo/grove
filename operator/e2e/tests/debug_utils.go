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
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// captureOperatorLogs fetches and logs the operator logs from the specified namespace and deployment.
// Uses tc.Ctx and tc.Clientset but takes namespace and deploymentPrefix as parameters since they
// typically differ from the test namespace (e.g., "grove-system" vs "default").
// The filterFn parameter allows filtering which log lines to output - if nil, all lines are logged.
func captureOperatorLogs(tc TestContext, namespace, deploymentPrefix string, filterFn func(string) bool) {
	// List pods in the namespace
	pods, err := tc.Clientset.CoreV1().Pods(namespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		logger.Errorf("Failed to list pods in namespace %s: %v", namespace, err)
		return
	}

	// Find operator pods by prefix
	for _, pod := range pods.Items {
		if len(pod.Name) >= len(deploymentPrefix) && pod.Name[:len(deploymentPrefix)] == deploymentPrefix {
			logger.Debugf("=== Operator Pod: %s (Phase: %s) ===", pod.Name, pod.Status.Phase)

			// Get logs for each container
			for _, container := range pod.Spec.Containers {
				logger.Debugf("--- Container: %s ---", container.Name)

				req := tc.Clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
					Container: container.Name,
					TailLines: func() *int64 { v := int64(200); return &v }(), // Last 200 lines
				})

				logStream, err := req.Stream(tc.Ctx)
				if err != nil {
					logger.Errorf("Failed to get logs for container %s: %v", container.Name, err)
					continue
				}

				// Read logs
				buf := make([]byte, 64*1024) // 64KB buffer
				for {
					n, err := logStream.Read(buf)
					if n > 0 {
						// Print logs line by line, applying the filter function if provided
						lines := string(buf[:n])
						for _, line := range strings.Split(lines, "\n") {
							if len(line) > 0 {
								// Print lines that match the filter (or all lines if no filter)
								if filterFn == nil || filterFn(line) {
									logger.Debugf("[OP-LOG] %s", line)
								}
							}
						}
					}
					if err != nil {
						break
					}
				}
				logStream.Close()
			}
		}
	}
}

// containsRollingUpdateTag checks if a line contains rolling update related tags
func containsRollingUpdateTag(line string) bool {
	tags := []string{"[ROLLING_UPDATE]", "rolling update", "processPendingUpdates", "deleteOldPending", "nextPodToUpdate"}
	for _, tag := range tags {
		if len(line) >= len(tag) {
			for i := 0; i <= len(line)-len(tag); i++ {
				if line[i:i+len(tag)] == tag {
					return true
				}
			}
		}
	}
	return false
}

// capturePodCliqueStatus captures and logs the status of all PodCliques for debugging
func capturePodCliqueStatus(tc TestContext) {
	pclqGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliques"}
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}

	logger.Debug("=== PodCliqueSet Status ===")
	pcsList, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		logger.Errorf("Failed to list PodCliqueSets: %v", err)
	} else {
		for _, pcs := range pcsList.Items {
			status, _, _ := unstructured.NestedMap(pcs.Object, "status")
			logger.Debugf("PCS %s: status=%v", pcs.GetName(), status)
		}
	}

	logger.Debug("=== PodClique Status ===")
	pclqList, err := tc.DynamicClient.Resource(pclqGVR).Namespace(tc.Namespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		logger.Errorf("Failed to list PodCliques: %v", err)
	} else {
		for _, pclq := range pclqList.Items {
			spec, _, _ := unstructured.NestedMap(pclq.Object, "spec")
			status, _, _ := unstructured.NestedMap(pclq.Object, "status")
			replicas, _, _ := unstructured.NestedInt64(spec, "replicas")
			readyReplicas, _, _ := unstructured.NestedInt64(status, "readyReplicas")
			updatedReplicas, _, _ := unstructured.NestedInt64(status, "updatedReplicas")
			logger.Debugf("PCLQ %s: replicas=%d, readyReplicas=%d, updatedReplicas=%d",
				pclq.GetName(), replicas, readyReplicas, updatedReplicas)
		}
	}

	logger.Debug("=== Pod Status ===")
	pods, err := listPods(tc)
	if err != nil {
		logger.Errorf("Failed to list pods: %v", err)
	} else {
		for _, pod := range pods.Items {
			pclq := pod.Labels["grove.io/podclique"]
			logger.Debugf("Pod %s: phase=%s, pclq=%s, ready=%v",
				pod.Name, pod.Status.Phase, pclq, isPodReady(&pod))
		}
	}
}

// isPodReady checks if a pod is ready
func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
