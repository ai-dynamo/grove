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

package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/grove/gvk"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	logBufferSize         = 64 * 1024 // 64KB
	eventLookbackDuration = 10 * time.Minute

	// ModeEnvVar controls diagnostic output mode.
	// Values: "stdout", "file", "both" (default: "file")
	ModeEnvVar = "GROVE_E2E_DIAG_MODE"

	// DirEnvVar specifies the directory for diagnostic files.
	DirEnvVar = "GROVE_E2E_DIAG_DIR"

	ModeStdout = "stdout"
	ModeFile   = "file"
	ModeBoth   = "both"
)

// groveResourceType defines a Grove resource type for diagnostics.
type groveResourceType struct {
	name     string
	gvk      schema.GroupVersionKind
	singular string
}

var groveResourceTypes = []groveResourceType{
	{"PodCliqueSets", gvk.PodCliqueSet.GroupVersion().WithKind(gvk.PodCliqueSet.Kind + "List"), "PODCLIQUESET"},
	{"PodCliques", gvk.PodClique.GroupVersion().WithKind(gvk.PodClique.Kind + "List"), "PODCLIQUE"},
	{"PodCliqueScalingGroups", gvk.PodCliqueScalingGroup.GroupVersion().WithKind(gvk.PodCliqueScalingGroup.Kind + "List"), "PODCLIQUESCALINGGROUP"},
	{"PodGangs", gvk.PodGang.GroupVersion().WithKind(gvk.PodGang.Kind + "List"), "PODGANG"},
}

// DiagCollector collects diagnostics from a Kubernetes cluster on test failure.
type DiagCollector struct {
	k8s       *k8sclient.Client
	namespace string
	logger    *log.Logger
	mode      string
	dir       string
}

// NewDiagCollector creates a DiagCollector.
func NewDiagCollector(k8sClient *k8sclient.Client, namespace, mode, dir string, logger *log.Logger) *DiagCollector {
	if mode == "" {
		mode = ModeFile
	}
	if logger == nil {
		logger = log.NewTestLogger(log.InfoLevel)
	}
	return &DiagCollector{
		k8s:       k8sClient,
		namespace: namespace,
		logger:    logger,
		mode:      mode,
		dir:       dir,
	}
}

// CollectAll collects and outputs all diagnostic information.
func (dc *DiagCollector) CollectAll(ctx context.Context, testName string) {
	output, filename, err := dc.createOutput(testName)
	if err != nil {
		dc.logger.Errorf("Failed to create diagnostics output, falling back to stdout: %v", err)
		output = nopCloser{os.Stdout}
	}
	defer output.Close()

	diagLogger := log.NewTestLoggerWithOutput(log.InfoLevel, output)

	if filename != "" {
		dc.logger.Infof("Writing diagnostics to file: %s", filename)
	}

	diagLogger.Info("================================================================================")
	diagLogger.Info("=== COLLECTING FAILURE DIAGNOSTICS ===")
	diagLogger.Info("================================================================================")

	dc.dumpOperatorLogs(ctx, diagLogger)
	dc.dumpGroveResources(ctx, diagLogger)
	dc.dumpPodDetails(ctx, diagLogger)
	dc.dumpRecentEvents(ctx, diagLogger)

	diagLogger.Info("================================================================================")
	diagLogger.Info("=== END OF FAILURE DIAGNOSTICS ===")
	diagLogger.Info("================================================================================")

	if filename != "" {
		dc.logger.Infof("Diagnostics collection complete. Output written to: %s", filename)
	}
}

func (dc *DiagCollector) dumpOperatorLogs(ctx context.Context, logger *log.Logger) {
	logger.Info("================================================================================")
	logger.Info("=== OPERATOR LOGS (all) ===")
	logger.Info("================================================================================")

	var podList corev1.PodList
	if err := dc.k8s.List(ctx, &podList, client.InNamespace(setup.OperatorNamespace)); err != nil {
		logger.Infof("[DIAG] Failed to list pods in namespace %s: %v", setup.OperatorNamespace, err)
		return
	}

	foundOperator := false
	for _, pod := range podList.Items {
		if !strings.HasPrefix(pod.Name, setup.OperatorDeploymentName) {
			continue
		}
		foundOperator = true

		totalRestarts := int32(0)
		for _, cs := range pod.Status.ContainerStatuses {
			totalRestarts += cs.RestartCount
		}

		logger.Infof("--- Operator Pod: %s (Phase: %s, Restarts: %d) ---", pod.Name, pod.Status.Phase, totalRestarts)

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

			if cs.RestartCount > 0 && cs.LastTerminationState.Terminated != nil {
				lt := cs.LastTerminationState.Terminated
				logger.Infof("    LastTermination: %s (exit: %d) at %s, reason: %s",
					lt.Reason, lt.ExitCode, lt.FinishedAt.Format("15:04:05"), lt.Message)
			}
		}

		for _, container := range pod.Spec.Containers {
			logger.Infof("--- Container: %s Logs ---", container.Name)

			req := dc.k8s.GetLogs(setup.OperatorNamespace, pod.Name, &corev1.PodLogOptions{
				Container: container.Name,
			})

			logStream, err := req.Stream(ctx)
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

func (dc *DiagCollector) dumpGroveResources(ctx context.Context, logger *log.Logger) {
	logger.Info("================================================================================")
	logger.Info("=== GROVE RESOURCES ===")
	logger.Info("================================================================================")

	for _, rt := range groveResourceTypes {
		logger.Infof("[DIAG] Listing %s in namespace %s...", rt.name, dc.namespace)

		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(rt.gvk)
		if err := dc.k8s.List(ctx, list, client.InNamespace(dc.namespace)); err != nil {
			logger.Infof("[DIAG] Failed to list %s: %v", rt.name, err)
			continue
		}

		if len(list.Items) == 0 {
			logger.Infof("[DIAG] No %s found in namespace %s", rt.name, dc.namespace)
			continue
		}

		logger.Infof("[DIAG] Found %d %s", len(list.Items), rt.name)
		for _, resource := range list.Items {
			logger.Info("--------------------------------------------------------------------------------")
			logger.Infof("--- %s: %s ---", rt.singular, resource.GetName())
			logger.Info("--------------------------------------------------------------------------------")

			yamlBytes, err := yaml.Marshal(resource.Object)
			if err != nil {
				logger.Infof("[DIAG] Failed to marshal %s %s: %v", rt.singular, resource.GetName(), err)
				continue
			}

			for _, line := range strings.Split(string(yamlBytes), "\n") {
				logger.Info(line)
			}
		}
	}
}

func (dc *DiagCollector) dumpPodDetails(ctx context.Context, logger *log.Logger) {
	logger.Info("================================================================================")
	logger.Info("=== POD DETAILS ===")
	logger.Info("================================================================================")

	logger.Infof("[DIAG] Listing all pods in namespace %s...", dc.namespace)

	var podList corev1.PodList
	if err := dc.k8s.List(ctx, &podList, client.InNamespace(dc.namespace)); err != nil {
		logger.Infof("[DIAG] Failed to list pods: %v", err)
		return
	}

	if len(podList.Items) == 0 {
		logger.Infof("[DIAG] No pods found in namespace %s", dc.namespace)
		return
	}

	logger.Infof("[DIAG] Found %d pods in namespace %s", len(podList.Items), dc.namespace)
	logger.Infof("%-40s %-12s %-10s %-45s %s", "NAME", "PHASE", "READY", "NODE", "CONDITIONS")
	logger.Info(strings.Repeat("-", 140))

	for _, pod := range podList.Items {
		readyContainers := 0
		totalContainers := len(pod.Spec.Containers)
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Ready {
				readyContainers++
			}
		}
		readyStr := fmt.Sprintf("%d/%d", readyContainers, totalContainers)

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

		if pod.Status.Phase != corev1.PodRunning || !isPodReady(&pod) {
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.State.Waiting != nil {
					logger.Infof("  Container %s: Waiting - %s: %s", cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message)
				}
				if cs.State.Terminated != nil {
					logger.Infof("  Container %s: Terminated - %s (exit %d)", cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
				}
				if cs.RestartCount > 0 {
					logger.Infof("  Container %s: Restarts=%d", cs.Name, cs.RestartCount)
				}
			}
		}
	}
}

func (dc *DiagCollector) dumpRecentEvents(ctx context.Context, logger *log.Logger) {
	logger.Info("================================================================================")
	logger.Infof("=== KUBERNETES EVENTS (last %v) ===", eventLookbackDuration)
	logger.Info("================================================================================")

	logger.Infof("[DIAG] Listing events in namespace %s...", dc.namespace)

	var eventList corev1.EventList
	if err := dc.k8s.List(ctx, &eventList, client.InNamespace(dc.namespace)); err != nil {
		logger.Infof("[DIAG] Failed to list events: %v", err)
		return
	}

	cutoff := time.Now().Add(-eventLookbackDuration)
	var recentEvents []corev1.Event
	for _, event := range eventList.Items {
		eventTime := event.LastTimestamp.Time
		if eventTime.IsZero() {
			eventTime = event.EventTime.Time
		}
		if eventTime.After(cutoff) {
			recentEvents = append(recentEvents, event)
		}
	}

	if len(recentEvents) == 0 {
		logger.Infof("[DIAG] No events found in namespace %s within last %v", dc.namespace, eventLookbackDuration)
		return
	}

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

	logger.Infof("%-24s %-8s %-25s %-35s %s", "TIME", "TYPE", "REASON", "OBJECT", "MESSAGE")
	logger.Info(strings.Repeat("-", 140))

	for _, event := range recentEvents {
		eventTime := event.LastTimestamp.Time
		if eventTime.IsZero() {
			eventTime = event.EventTime.Time
		}

		timeStr := eventTime.Format(time.RFC3339)
		objectRef := fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name)

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

// Helper functions

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// Output helpers

type nopCloser struct {
	io.Writer
}

func (nopCloser) Close() error { return nil }

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

func (dc *DiagCollector) createOutput(testName string) (io.WriteCloser, string, error) {
	var writers []io.WriteCloser
	var filename string

	if dc.mode == ModeStdout || dc.mode == ModeBoth {
		writers = append(writers, nopCloser{os.Stdout})
	}

	if dc.mode == ModeFile || dc.mode == ModeBoth {
		file, name, err := createDiagnosticsFile(testName, dc.dir)
		if err != nil {
			if dc.mode == ModeBoth {
				dc.logger.Infof("[DIAG] Warning: failed to create diagnostics file: %v (continuing with stdout only)", err)
			} else {
				return nil, "", err
			}
		} else {
			filename = name
			writers = append(writers, file)
		}
	}

	if len(writers) == 0 {
		return nil, "", fmt.Errorf("no valid diagnostics output configured (mode=%s)", dc.mode)
	}

	return newMultiWriteCloser(writers...), filename, nil
}

func createDiagnosticsFile(testName, diagDir string) (*os.File, string, error) {
	sanitizedName := strings.ReplaceAll(testName, "/", "_")
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	baseFilename := fmt.Sprintf("e2e-diag-%s_%s.log", sanitizedName, timestamp)

	if diagDir != "" {
		filename := filepath.Join(diagDir, baseFilename)
		file, err := os.Create(filename)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create diagnostics file in %s: %w", diagDir, err)
		}
		return file, filename, nil
	}

	file, err := os.Create(baseFilename)
	if err != nil {
		filename := filepath.Join(os.TempDir(), baseFilename)
		file, err = os.Create(filename)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create diagnostics file: %w", err)
		}
		return file, filename, nil
	}

	return file, baseFilename, nil
}
