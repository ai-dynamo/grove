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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

const (
	// logBufferSize is the size of the buffer for reading logs from the operator (debugging purposes)
	logBufferSize = 64 * 1024 // 64KB

	// eventLookbackDuration is how far back to look for events
	eventLookbackDuration = 10 * time.Minute

	// DiagnosticsModeEnvVar controls diagnostic output mode.
	// Values: "stdout", "file", "both" (default: "file")
	DiagnosticsModeEnvVar = "GROVE_E2E_DIAG_MODE"

	// DiagnosticsDirEnvVar specifies the directory for diagnostic files.
	// Used when mode is "file" or "both". Default: current directory (fallback to /tmp).
	DiagnosticsDirEnvVar = "GROVE_E2E_DIAG_DIR"

	// Diagnostics mode constants
	DiagnosticsModeStdout = "stdout"
	DiagnosticsModeFile   = "file"
	DiagnosticsModeBoth   = "both"
)

// isPodReady checks if a pod is ready
func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

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

// nopCloser wraps an io.Writer to implement io.WriteCloser with a no-op Close.
// Used for os.Stdout which we don't want to actually close.
type nopCloser struct {
	io.Writer
}

func (nopCloser) Close() error { return nil }

// multiWriteCloser wraps multiple io.WriteCloser instances.
// Write calls write to all; Close closes all.
type multiWriteCloser struct {
	writers []io.WriteCloser
}

func newMultiWriteCloser(writers ...io.WriteCloser) *multiWriteCloser {
	return &multiWriteCloser{writers: writers}
}

func (m *multiWriteCloser) Write(p []byte) (n int, err error) {
	for _, w := range m.writers {
		n, err = w.Write(p)
		if err != nil {
			return n, err
		}
	}
	return len(p), nil
}

func (m *multiWriteCloser) Close() error {
	var errs []error
	for _, w := range m.writers {
		if err := w.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// createDiagnosticsOutput creates writer(s) for diagnostics output based on GROVE_E2E_DIAG_MODE.
// Mode values:
//   - "stdout": write to stdout only
//   - "file": write to timestamped file only (default)
//   - "both": write to both stdout and file
//
// When writing to file, GROVE_E2E_DIAG_DIR specifies the directory (default: current dir, fallback to /tmp).
// Returns the writer, filename (empty if stdout-only), and any error.
func createDiagnosticsOutput(testName string) (io.WriteCloser, string, error) {
	mode := os.Getenv(DiagnosticsModeEnvVar)
	if mode == "" {
		mode = DiagnosticsModeFile // default
	}

	var writers []io.WriteCloser
	var filename string

	// Add stdout if mode is stdout or both
	if mode == DiagnosticsModeStdout || mode == DiagnosticsModeBoth {
		writers = append(writers, nopCloser{os.Stdout})
	}

	// Add file if mode is file or both
	if mode == DiagnosticsModeFile || mode == DiagnosticsModeBoth {
		file, name, err := createDiagnosticsFile(testName)
		if err != nil {
			// If we can't create a file but stdout is available, continue with stdout only
			if mode == DiagnosticsModeBoth {
				logger.Infof("[DIAG] Warning: failed to create diagnostics file: %v (continuing with stdout only)", err)
			} else {
				return nil, "", err
			}
		} else {
			filename = name
			writers = append(writers, file)
		}
	}

	if len(writers) == 0 {
		return nil, "", fmt.Errorf("no valid diagnostics output configured (mode=%s)", mode)
	}

	return newMultiWriteCloser(writers...), filename, nil
}

// createDiagnosticsFile creates a timestamped diagnostics file
func createDiagnosticsFile(testName string) (*os.File, string, error) {
	// Sanitize test name for use in filename (replace / with _)
	sanitizedName := strings.ReplaceAll(testName, "/", "_")

	// Create a timestamped file with test name
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	baseFilename := fmt.Sprintf("e2e-diag-%s_%s.log", sanitizedName, timestamp)

	// Determine the directory for the diagnostics file
	diagDir := os.Getenv(DiagnosticsDirEnvVar)
	if diagDir != "" {
		filename := filepath.Join(diagDir, baseFilename)
		file, err := os.Create(filename)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create diagnostics file in %s: %w", diagDir, err)
		}
		return file, filename, nil
	}

	// Try to create the file in the current directory
	file, err := os.Create(baseFilename)
	if err != nil {
		// Fall back to a temp directory if we can't write to current dir
		filename := filepath.Join(os.TempDir(), baseFilename)
		file, err = os.Create(filename)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create diagnostics file: %w", err)
		}
		return file, filename, nil
	}

	return file, baseFilename, nil
}

// CollectAllDiagnostics collects and prints all diagnostic information at INFO level.
// This should be called when a test fails, before cleanup runs.
// All output is at INFO level to ensure visibility regardless of log level settings.
//
// Output is controlled by GROVE_E2E_DIAG_MODE env var:
//   - "stdout": print to stdout only
//   - "file": write to timestamped file only (default)
//   - "both": write to both stdout and file
//
// Set GROVE_E2E_DIAG_DIR env var to specify the output directory for diagnostics files.
func CollectAllDiagnostics(tc TestContext) {
	// Get test name for the diagnostics file
	testName := "unknown_test"
	if tc.T != nil {
		testName = tc.T.Name()
	}

	// Create diagnostics output
	output, filename, err := createDiagnosticsOutput(testName)
	if err != nil {
		logger.Errorf("Failed to create diagnostics output, falling back to stdout: %v", err)
		output = nopCloser{os.Stdout}
	}
	defer output.Close()

	// Save reference to stdout logger, then shadow with diagnostics logger
	stdoutLogger := logger
	logger := utils.NewTestLoggerWithOutput(utils.InfoLevel, output)

	// Log where diagnostics are being written (to main test output)
	if filename != "" {
		stdoutLogger.Infof("Writing diagnostics to file: %s", filename)
	}

	logger.Info("================================================================================")
	logger.Info("=== COLLECTING FAILURE DIAGNOSTICS ===")
	logger.Info("================================================================================")

	// Collect each type of diagnostic, continuing even if one fails
	dumpOperatorLogs(tc, logger)
	dumpGroveResources(tc, logger)
	dumpPodDetails(tc, logger)
	dumpRecentEvents(tc, logger)

	logger.Info("================================================================================")
	logger.Info("=== END OF FAILURE DIAGNOSTICS ===")
	logger.Info("================================================================================")

	// Log completion message (to main test output)
	if filename != "" {
		stdoutLogger.Infof("Diagnostics collection complete. Output written to: %s", filename)
	}
}

// dumpOperatorLogs captures and prints operator logs at INFO level.
// Captures all logs from all containers in the operator pod.
func dumpOperatorLogs(tc TestContext, logger *utils.Logger) {
	logger.Info("================================================================================")
	logger.Info("=== OPERATOR LOGS (all) ===")
	logger.Info("================================================================================")

	// List pods in the operator namespace
	pods, err := tc.Clientset.CoreV1().Pods(setup.OperatorNamespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		logger.Infof("[DIAG] Failed to list pods in namespace %s: %v", setup.OperatorNamespace, err)
		return
	}

	foundOperator := false
	for _, pod := range pods.Items {
		if !strings.HasPrefix(pod.Name, setup.OperatorDeploymentName) {
			continue
		}
		foundOperator = true

		// Calculate total restart count across all containers
		totalRestarts := int32(0)
		for _, cs := range pod.Status.ContainerStatuses {
			totalRestarts += cs.RestartCount
		}

		logger.Infof("--- Operator Pod: %s (Phase: %s, Restarts: %d) ---", pod.Name, pod.Status.Phase, totalRestarts)

		// Log detailed container status information
		for _, cs := range pod.Status.ContainerStatuses {
			stateStr := "Unknown"
			if cs.State.Running != nil {
				stateStr = fmt.Sprintf("Running (started: %s)", cs.State.Running.StartedAt.Format("15:04:05"))
			} else if cs.State.Waiting != nil {
				stateStr = fmt.Sprintf("Waiting (%s: %s)", cs.State.Waiting.Reason, cs.State.Waiting.Message)
			} else if cs.State.Terminated != nil {
				stateStr = fmt.Sprintf("Terminated (%s, exit: %d)", cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
			}
			logger.Infof("  Container %s: Ready=%v, RestartCount=%d, State=%s",
				cs.Name, cs.Ready, cs.RestartCount, stateStr)

			// Log last termination state if there were restarts
			if cs.RestartCount > 0 && cs.LastTerminationState.Terminated != nil {
				lt := cs.LastTerminationState.Terminated
				logger.Infof("    LastTermination: %s (exit: %d) at %s, reason: %s",
					lt.Reason, lt.ExitCode, lt.FinishedAt.Format("15:04:05"), lt.Message)
			}
		}

		// Get logs for each container
		for _, container := range pod.Spec.Containers {
			logger.Infof("--- Container: %s Logs ---", container.Name)

			req := tc.Clientset.CoreV1().Pods(setup.OperatorNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: container.Name,
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
		logger.Infof("[DIAG] No operator pods found with prefix %s in namespace %s", setup.OperatorDeploymentName, setup.OperatorNamespace)
	}
}

// dumpGroveResources dumps all Grove resources as YAML at INFO level.
func dumpGroveResources(tc TestContext, logger *utils.Logger) {
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
func dumpPodDetails(tc TestContext, logger *utils.Logger) {
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
func dumpRecentEvents(tc TestContext, logger *utils.Logger) {
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
