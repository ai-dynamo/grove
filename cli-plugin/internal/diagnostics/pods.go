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

package diagnostics

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CollectPodDetails dumps detailed pod information.
// Lists ALL pods in the namespace to ensure we capture all relevant pods.
func CollectPodDetails(dc *DiagnosticContext, output DiagnosticOutput) error {
	if dc.Clientset == nil {
		return fmt.Errorf("clientset is nil, cannot list pods")
	}

	if err := output.WriteSection("POD DETAILS"); err != nil {
		return err
	}

	// List ALL pods in the namespace
	_ = output.WriteLinef("[INFO] Listing all pods in namespace %s...", dc.Namespace)
	pods, err := dc.Clientset.CoreV1().Pods(dc.Namespace).List(dc.Ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		_ = output.WriteLinef("[INFO] No pods found in namespace %s", dc.Namespace)
		return nil
	}

	_ = output.WriteLinef("[INFO] Found %d pods in namespace %s", len(pods.Items), dc.Namespace)

	// Build table data
	headers := []string{"NAME", "PHASE", "READY", "NODE", "CONDITIONS"}
	rows := make([][]string, 0, len(pods.Items))

	for _, pod := range pods.Items {
		row := buildPodRow(&pod)
		rows = append(rows, row)
	}

	if err := output.WriteTable(headers, rows); err != nil {
		return fmt.Errorf("failed to write pod table: %w", err)
	}

	// Print detailed info for pods with issues
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning || !isPodReady(&pod) {
			writePodIssueDetails(output, &pod)
		}
	}

	return nil
}

// buildPodRow builds a table row for a pod
func buildPodRow(pod *corev1.Pod) []string {
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
	condStr := summarizeConditions(pod)

	nodeName := pod.Spec.NodeName
	if nodeName == "" {
		nodeName = "<unscheduled>"
	}

	return []string{
		truncateString(pod.Name, 40),
		string(pod.Status.Phase),
		readyStr,
		truncateString(nodeName, 45),
		condStr,
	}
}

// summarizeConditions summarizes pod conditions for display
func summarizeConditions(pod *corev1.Pod) string {
	var conditionSummary []string
	for _, cond := range pod.Status.Conditions {
		if cond.Status == corev1.ConditionFalse && cond.Reason != "" {
			conditionSummary = append(conditionSummary, fmt.Sprintf("%s:%s", cond.Type, cond.Reason))
		}
	}
	if len(conditionSummary) == 0 {
		return "OK"
	}
	return strings.Join(conditionSummary, ", ")
}

// writePodIssueDetails writes detailed information for pods with issues
func writePodIssueDetails(output DiagnosticOutput, pod *corev1.Pod) {
	_ = output.WriteLinef("  Pod %s details:", pod.Name)

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			_ = output.WriteLinef("    └─ Container %s: Waiting - %s: %s",
				cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message)
		}
		if cs.State.Terminated != nil {
			_ = output.WriteLinef("    └─ Container %s: Terminated - %s (exit %d)",
				cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
		}
		if cs.RestartCount > 0 {
			_ = output.WriteLinef("    └─ Container %s: Restarts=%d", cs.Name, cs.RestartCount)
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
