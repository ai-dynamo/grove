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

// CollectOperatorLogs captures and outputs operator logs.
// Captures the last OperatorLogLines lines from all containers in the operator pod.
func CollectOperatorLogs(dc *DiagnosticContext, output DiagnosticOutput) error {
	if dc.Clientset == nil {
		return fmt.Errorf("clientset is nil, cannot collect operator logs")
	}

	if err := output.WriteSection(fmt.Sprintf("OPERATOR LOGS (last %d lines)", OperatorLogLines)); err != nil {
		return err
	}

	// List pods in the operator namespace
	pods, err := dc.Clientset.CoreV1().Pods(dc.OperatorNamespace).List(dc.Ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods in namespace %s: %w", dc.OperatorNamespace, err)
	}

	foundOperator := false
	for _, pod := range pods.Items {
		if !strings.HasPrefix(pod.Name, dc.OperatorDeploymentPrefix) {
			continue
		}
		foundOperator = true

		if err := collectPodLogs(dc, output, &pod); err != nil {
			// Log error but continue with other pods
			_ = output.WriteLinef("[ERROR] Failed to collect logs for pod %s: %v", pod.Name, err)
		}
	}

	if !foundOperator {
		_ = output.WriteLinef("[INFO] No operator pods found with prefix %s in namespace %s",
			dc.OperatorDeploymentPrefix, dc.OperatorNamespace)
	}

	return nil
}

// collectPodLogs collects logs from a single pod
func collectPodLogs(dc *DiagnosticContext, output DiagnosticOutput, pod *corev1.Pod) error {
	// Calculate total restart count across all containers
	totalRestarts := int32(0)
	for _, cs := range pod.Status.ContainerStatuses {
		totalRestarts += cs.RestartCount
	}

	if err := output.WriteSubSection(fmt.Sprintf("Operator Pod: %s (Phase: %s, Restarts: %d)",
		pod.Name, pod.Status.Phase, totalRestarts)); err != nil {
		return err
	}

	// Log detailed container status information
	for _, cs := range pod.Status.ContainerStatuses {
		stateStr := formatContainerState(&cs)
		_ = output.WriteLinef("  Container %s: Ready=%v, RestartCount=%d, State=%s",
			cs.Name, cs.Ready, cs.RestartCount, stateStr)

		// Log last termination state if there were restarts
		if cs.RestartCount > 0 && cs.LastTerminationState.Terminated != nil {
			lt := cs.LastTerminationState.Terminated
			_ = output.WriteLinef("    LastTermination: %s (exit: %d) at %s, reason: %s",
				lt.Reason, lt.ExitCode, lt.FinishedAt.Format("15:04:05"), lt.Message)
		}
	}

	// Get logs for each container
	for _, container := range pod.Spec.Containers {
		if err := collectContainerLogs(dc, output, pod.Name, container.Name); err != nil {
			_ = output.WriteLinef("[ERROR] Failed to get logs for container %s: %v", container.Name, err)
		}
	}

	return nil
}

// collectContainerLogs collects logs from a single container
func collectContainerLogs(dc *DiagnosticContext, output DiagnosticOutput, podName, containerName string) error {
	_ = output.WriteLinef("--- Container: %s Logs ---", containerName)

	tailLines := int64(OperatorLogLines)
	req := dc.Clientset.CoreV1().Pods(dc.OperatorNamespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &tailLines,
	})

	logStream, err := req.Stream(dc.Ctx)
	if err != nil {
		return fmt.Errorf("failed to stream logs: %w", err)
	}
	defer logStream.Close()

	buf := make([]byte, LogBufferSize)
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

	// Output logs line by line
	for _, line := range strings.Split(allLogs.String(), "\n") {
		if len(line) > 0 {
			_ = output.WriteLinef("[OP-LOG] %s", line)
		}
	}

	return nil
}

// formatContainerState formats a container state for display
func formatContainerState(cs *corev1.ContainerStatus) string {
	if cs.State.Running != nil {
		return fmt.Sprintf("Running (started: %s)", cs.State.Running.StartedAt.Format("15:04:05"))
	}
	if cs.State.Waiting != nil {
		return fmt.Sprintf("Waiting (%s: %s)", cs.State.Waiting.Reason, cs.State.Waiting.Message)
	}
	if cs.State.Terminated != nil {
		return fmt.Sprintf("Terminated (%s, exit: %d)", cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
	}
	return "Unknown"
}
