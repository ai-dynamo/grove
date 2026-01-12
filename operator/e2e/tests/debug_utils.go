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
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

const logBufferSize = 64 * 1024 // 64KB

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

				buf := make([]byte, logBufferSize)
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
	tags := []string{
		"[ROLLING_UPDATE]",
		"rolling update",
		"processPendingUpdates",
		"deleteOldPending",
		"nextPodToUpdate",
	}

	for _, tag := range tags {
		if strings.Contains(line, tag) {
			return true
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

// ============================================================================
// Failure Diagnostics Functions
// These functions dump diagnostic information at INFO level when tests fail.
// ============================================================================

const (
	// operatorNamespace is the namespace where the Grove operator runs
	operatorNamespace = "grove-system"
	// operatorDeploymentPrefix is the prefix for operator pod names
	operatorDeploymentPrefix = "grove-operator"
	// operatorLogLines is the number of log lines to capture from the operator.
	// Set to 2000 to ensure we capture logs from before the failure occurred,
	// not just the steady-state logs after the failure.
	operatorLogLines = 2000
	// eventLookbackDuration is how far back to look for events
	eventLookbackDuration = 10 * time.Minute
)

// groveResourceType defines a Grove resource type for diagnostics
type groveResourceType struct {
	name     string
	gvr      schema.GroupVersionResource
	singular string
}

// groveResourceTypes lists all Grove resource types to dump on failure
var groveResourceTypes = []groveResourceType{
	{"PodCliqueSets", schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}, "PODCLIQUESET"},
	{"PodCliques", schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliques"}, "PODCLIQUE"},
	{"PodCliqueScalingGroups", schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquescalinggroups"}, "PODCLIQUESCALINGGROUP"},
	{"PodGangs", schema.GroupVersionResource{Group: "scheduler.grove.io", Version: "v1alpha1", Resource: "podgangs"}, "PODGANG"},
}

// CollectAllDiagnostics collects and prints all diagnostic information at INFO level.
// This should be called when a test fails, before cleanup runs.
// All output is at INFO level to ensure visibility regardless of log level settings.
func CollectAllDiagnostics(tc TestContext) {
	logger.Info("================================================================================")
	logger.Info("=== COLLECTING FAILURE DIAGNOSTICS ===")
	logger.Info("================================================================================")

	// Collect each type of diagnostic, continuing even if one fails
	dumpOperatorLogs(tc)
	dumpGroveResources(tc)
	dumpPodDetails(tc)
	dumpRecentEvents(tc)

	logger.Info("================================================================================")
	logger.Info("=== END OF FAILURE DIAGNOSTICS ===")
	logger.Info("================================================================================")
}

// dumpOperatorLogs captures and prints operator logs at INFO level.
// Captures the last operatorLogLines lines from all containers in the operator pod.
func dumpOperatorLogs(tc TestContext) {
	logger.Info("================================================================================")
	logger.Infof("=== OPERATOR LOGS (last %d lines) ===", operatorLogLines)
	logger.Info("================================================================================")

	// List pods in the operator namespace
	pods, err := tc.Clientset.CoreV1().Pods(operatorNamespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		logger.Infof("[DIAG] Failed to list pods in namespace %s: %v", operatorNamespace, err)
		return
	}

	foundOperator := false
	for _, pod := range pods.Items {
		if !strings.HasPrefix(pod.Name, operatorDeploymentPrefix) {
			continue
		}
		foundOperator = true
		logger.Infof("--- Operator Pod: %s (Phase: %s) ---", pod.Name, pod.Status.Phase)

		// Get logs for each container
		for _, container := range pod.Spec.Containers {
			logger.Infof("--- Container: %s ---", container.Name)

			tailLines := int64(operatorLogLines)
			req := tc.Clientset.CoreV1().Pods(operatorNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: container.Name,
				TailLines: &tailLines,
			})

			logStream, err := req.Stream(tc.Ctx)
			if err != nil {
				logger.Infof("[DIAG] Failed to get logs for container %s: %v", container.Name, err)
				continue
			}

			buf := make([]byte, logBufferSize)
			var allLogs strings.Builder
			for {
				n, err := logStream.Read(buf)
				if n > 0 {
					allLogs.Write(buf[:n])
				}
				if err != nil {
					break
				}
			}
			logStream.Close()

			// Print logs line by line at INFO level
			for _, line := range strings.Split(allLogs.String(), "\n") {
				if len(line) > 0 {
					logger.Infof("[OP-LOG] %s", line)
				}
			}
		}
	}

	if !foundOperator {
		logger.Infof("[DIAG] No operator pods found with prefix %s in namespace %s", operatorDeploymentPrefix, operatorNamespace)
	}
}

// dumpGroveResources dumps all Grove resources as YAML at INFO level.
func dumpGroveResources(tc TestContext) {
	logger.Info("================================================================================")
	logger.Info("=== GROVE RESOURCES ===")
	logger.Info("================================================================================")

	if tc.DynamicClient == nil {
		logger.Info("[DIAG] DynamicClient is nil, cannot list Grove resources")
		return
	}

	for _, rt := range groveResourceTypes {
		logger.Infof("[DIAG] Listing %s in namespace %s...", rt.name, tc.Namespace)
		resources, err := tc.DynamicClient.Resource(rt.gvr).Namespace(tc.Namespace).List(tc.Ctx, metav1.ListOptions{})
		if err != nil {
			logger.Infof("[DIAG] Failed to list %s: %v", rt.name, err)
			continue
		}

		if len(resources.Items) == 0 {
			logger.Infof("[DIAG] No %s found in namespace %s", rt.name, tc.Namespace)
			continue
		}

		logger.Infof("[DIAG] Found %d %s", len(resources.Items), rt.name)
		for _, resource := range resources.Items {
			logger.Info("--------------------------------------------------------------------------------")
			logger.Infof("--- %s: %s ---", rt.singular, resource.GetName())
			logger.Info("--------------------------------------------------------------------------------")

			yamlBytes, err := yaml.Marshal(resource.Object)
			if err != nil {
				logger.Infof("[DIAG] Failed to marshal %s %s: %v", rt.singular, resource.GetName(), err)
				continue
			}

			// Print YAML line by line for better log formatting
			for _, line := range strings.Split(string(yamlBytes), "\n") {
				logger.Info(line)
			}
		}
	}
}

// dumpPodDetails dumps detailed pod information at INFO level.
// Lists ALL pods in the namespace (not filtered by workload label selector)
// to ensure we capture all relevant pods during failure diagnostics.
func dumpPodDetails(tc TestContext) {
	logger.Info("================================================================================")
	logger.Info("=== POD DETAILS ===")
	logger.Info("================================================================================")

	if tc.Clientset == nil {
		logger.Info("[DIAG] Clientset is nil, cannot list pods")
		return
	}

	// List ALL pods in the namespace, not just workload pods
	// This ensures we capture all relevant pods during failure diagnostics
	logger.Infof("[DIAG] Listing all pods in namespace %s...", tc.Namespace)
	pods, err := tc.Clientset.CoreV1().Pods(tc.Namespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		logger.Infof("[DIAG] Failed to list pods: %v", err)
		return
	}

	if len(pods.Items) == 0 {
		logger.Infof("[DIAG] No pods found in namespace %s", tc.Namespace)
		return
	}

	logger.Infof("[DIAG] Found %d pods in namespace %s", len(pods.Items), tc.Namespace)

	// Print header
	logger.Infof("%-40s %-12s %-10s %-45s %s", "NAME", "PHASE", "READY", "NODE", "CONDITIONS")
	logger.Info(strings.Repeat("-", 140))

	for _, pod := range pods.Items {
		// Get container ready count
		readyContainers := 0
		totalContainers := len(pod.Spec.Containers)
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Ready {
				readyContainers++
			}
		}
		readyStr := fmt.Sprintf("%d/%d", readyContainers, totalContainers)

		// Summarize conditions
		var conditionSummary []string
		for _, cond := range pod.Status.Conditions {
			if cond.Status == corev1.ConditionFalse && cond.Reason != "" {
				conditionSummary = append(conditionSummary, fmt.Sprintf("%s:%s", cond.Type, cond.Reason))
			}
		}
		condStr := strings.Join(conditionSummary, ", ")
		if condStr == "" {
			condStr = "OK"
		}

		nodeName := pod.Spec.NodeName
		if nodeName == "" {
			nodeName = "<unscheduled>"
		}

		logger.Infof("%-40s %-12s %-10s %-45s %s",
			truncateString(pod.Name, 40),
			pod.Status.Phase,
			readyStr,
			truncateString(nodeName, 45),
			condStr)

		// If pod has issues, print more details
		if pod.Status.Phase != corev1.PodRunning || !isPodReady(&pod) {
			// Print container statuses
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.State.Waiting != nil {
					logger.Infof("  └─ Container %s: Waiting - %s: %s", cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message)
				}
				if cs.State.Terminated != nil {
					logger.Infof("  └─ Container %s: Terminated - %s (exit %d)", cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
				}
				if cs.RestartCount > 0 {
					logger.Infof("  └─ Container %s: Restarts=%d", cs.Name, cs.RestartCount)
				}
			}
		}
	}
}

// dumpRecentEvents dumps Kubernetes events from the last eventLookbackDuration at INFO level.
func dumpRecentEvents(tc TestContext) {
	logger.Info("================================================================================")
	logger.Infof("=== KUBERNETES EVENTS (last %v) ===", eventLookbackDuration)
	logger.Info("================================================================================")

	if tc.Clientset == nil {
		logger.Info("[DIAG] Clientset is nil, cannot list events")
		return
	}

	logger.Infof("[DIAG] Listing events in namespace %s...", tc.Namespace)
	events, err := tc.Clientset.CoreV1().Events(tc.Namespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		logger.Infof("[DIAG] Failed to list events: %v", err)
		return
	}

	// Filter to recent events
	cutoff := time.Now().Add(-eventLookbackDuration)
	var recentEvents []corev1.Event
	for _, event := range events.Items {
		eventTime := event.LastTimestamp.Time
		if eventTime.IsZero() {
			eventTime = event.EventTime.Time
		}
		if eventTime.After(cutoff) {
			recentEvents = append(recentEvents, event)
		}
	}

	if len(recentEvents) == 0 {
		logger.Infof("[DIAG] No events found in namespace %s within last %v", tc.Namespace, eventLookbackDuration)
		return
	}

	// Sort by timestamp (oldest first)
	sort.Slice(recentEvents, func(i, j int) bool {
		ti := recentEvents[i].LastTimestamp.Time
		if ti.IsZero() {
			ti = recentEvents[i].EventTime.Time
		}
		tj := recentEvents[j].LastTimestamp.Time
		if tj.IsZero() {
			tj = recentEvents[j].EventTime.Time
		}
		return ti.Before(tj)
	})

	// Print header
	logger.Infof("%-24s %-8s %-25s %-35s %s", "TIME", "TYPE", "REASON", "OBJECT", "MESSAGE")
	logger.Info(strings.Repeat("-", 140))

	for _, event := range recentEvents {
		eventTime := event.LastTimestamp.Time
		if eventTime.IsZero() {
			eventTime = event.EventTime.Time
		}

		timeStr := eventTime.Format(time.RFC3339)
		objectRef := fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name)

		// Truncate message if too long
		message := event.Message
		if len(message) > 80 {
			message = message[:77] + "..."
		}

		logger.Infof("%-24s %-8s %-25s %-35s %s",
			timeStr,
			event.Type,
			truncateString(event.Reason, 25),
			truncateString(objectRef, 35),
			message)
	}
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
