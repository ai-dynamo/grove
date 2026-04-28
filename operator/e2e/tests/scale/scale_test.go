//go:build e2e

package scale

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

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/utils/ptr"

	"github.com/ai-dynamo/grove/operator/e2e/diagnostics"
	"github.com/ai-dynamo/grove/operator/e2e/grove/config"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/resources"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"

	"github.com/ai-dynamo/grove/operator/e2e/measurement"
	"github.com/ai-dynamo/grove/operator/e2e/measurement/condition"
	"github.com/ai-dynamo/grove/operator/e2e/measurement/exporter"
)

// toOperatorMetadata converts GroveMetadata to the measurement package type.
func toOperatorMetadata(m *config.GroveMetadata) *measurement.OperatorMetadata {
	return &measurement.OperatorMetadata{
		GroveImage: m.Image,
		K8sClient: &measurement.K8sClientConfig{
			QPS:   m.Config.ClientConnection.QPS,
			Burst: m.Config.ClientConnection.Burst,
		},
		ControllerMaxReconcile: &measurement.ControllerMaxReconcile{
			PodCliqueSet:          ptr.Deref(m.Config.Controllers.PodCliqueSet.ConcurrentSyncs, 1),
			PodCliqueScalingGroup: ptr.Deref(m.Config.Controllers.PodCliqueScalingGroup.ConcurrentSyncs, 1),
			PodClique:             ptr.Deref(m.Config.Controllers.PodClique.ConcurrentSyncs, 1),
		},
	}
}

// Logger for the scale tests.
var Logger = log.NewTestLogger(log.InfoLevel)

const (
	scaleTestExpectedPods     = 1000
	scaleTestExpectedReplicas = 1
	scaleTestPCSCount         = 1
	scaleTestWorkerNodes      = 100
	scaleTestPollInterval     = 100 * time.Millisecond
	scaleTestTimeout          = 15 * time.Minute

	scaleTestName      = "ScaleTest_1000"
	scaleTestWorkload  = "scale-test-1000"
	scaleTestYAMLPath  = "../../yaml/scale-test-1000.yaml"
	scaleTestNamespace = "default"

	runIDTimeFormat   = "20060102-150405"
	outputResultsFile = "scale-test-results.json"

	// steadyStateWindow keeps the pprof/measurement window open after a no-op reconcile
	// trigger so the full ~500-PodClique spec-hash-short-circuit burst has time to run.
	steadyStateWindow = 30 * time.Second
)

func Test_ScaleTest_1000(t *testing.T) {
	diagDir := os.Getenv(diagnostics.DirEnvVar)
	Logger.Infof("starting scale test: %d expected pods, timeout %v", scaleTestExpectedPods, scaleTestTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), scaleTestTimeout)
	defer cancel()

	Logger.Infof("preparing test cluster with %d worker nodes", scaleTestWorkerNodes)
	tc, cleanup := testctx.PrepareTest(ctx, t, scaleTestWorkerNodes,
		testctx.WithTimeout(scaleTestTimeout),
		testctx.WithInterval(scaleTestPollInterval),
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         scaleTestWorkload,
			YAMLPath:     scaleTestYAMLPath,
			Namespace:    scaleTestNamespace,
			ExpectedPods: scaleTestExpectedPods,
		}),
	)
	defer cleanup()

	metadata, err := config.NewOperatorConfig(tc.Client).ReadGroveMetadata(ctx)
	if err != nil {
		t.Fatalf("failed to read grove metadata: %v", err)
	}

	runID := fmt.Sprintf("run-%s", time.Now().Format(runIDTimeFormat))
	Logger.Infof("test config: runID=%s, namespace=%s, pcsName=%s", runID, tc.Namespace, tc.Workload.Name)

	outputDir := filepath.Join(scaleTestName, runID)
	if diagDir != "" {
		outputDir = filepath.Join(diagDir, scaleTestName, runID)
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatalf("failed to create output directory: %v", err)
	}

	pprofOpt, pprofCleanup := setupPprofHook(ctx, tc.Client, runID, outputDir, loadPyroscopeConfig())
	defer pprofCleanup()

	opts := []measurement.TimelineOption{
		measurement.WithPollInterval(scaleTestPollInterval),
		measurement.WithLogger(Logger.GetLogr()),
	}
	if pprofOpt != nil {
		opts = append(opts, pprofOpt)
	}

	tracker := measurement.NewTimelineTracker(
		scaleTestName,
		runID,
		tc.Namespace,
		scaleTestPCSCount,
		opts...,
	)

	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "deploy",
		ActionFn: func(ctx context.Context) error {
			_, err := resources.NewResourceManager(tc.Client, Logger).ApplyYAMLFile(ctx, tc.Workload.YAMLPath, tc.Namespace)
			return err
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "pods-created",
				Condition: &condition.PodsCreatedCondition{
					Client:        tc.Client.Client,
					Namespace:     tc.Namespace,
					LabelSelector: tc.GetLabelSelector(),
					ExpectedCount: scaleTestExpectedPods,
				},
			},
			{
				Name: "pods-ready",
				Condition: &condition.PodsReadyCondition{
					Client:        tc.Client.Client,
					Namespace:     tc.Namespace,
					LabelSelector: tc.GetLabelSelector(),
					ExpectedCount: scaleTestExpectedPods,
				},
			},
			{
				Name: "pcs-available",
				Condition: &condition.PCSAvailableCondition{
					Client:        tc.Client.Client,
					Name:          tc.Workload.Name,
					Namespace:     tc.Namespace,
					ExpectedCount: scaleTestExpectedReplicas,
				},
			},
		},
	})

	// steady-state-reconcile: patch a metadata annotation to force one reconcile cycle
	// without touching spec. With the spec-hash short-circuit in place, the PCS→PodClique
	// update path should fire cache hits for every PodClique. pprof captured during this
	// window isolates the no-op reconcile cost.
	steadyStateTriggerID := fmt.Sprintf("steady-%s", runID)
	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "steady-state-reconcile",
		ActionFn: func(ctx context.Context) error {
			Logger.Info("triggering no-op PCS reconcile")
			return workload.NewWorkloadManager(tc.Client, Logger).TriggerPCSReconcile(ctx, tc.Namespace, tc.Workload.Name, steadyStateTriggerID)
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "pcs-still-available",
				Condition: &condition.PCSAvailableCondition{
					Client:        tc.Client.Client,
					Name:          tc.Workload.Name,
					Namespace:     tc.Namespace,
					ExpectedCount: scaleTestExpectedReplicas,
				},
			},
			{
				Name:      "steady-state-window",
				Condition: &condition.TimerCondition{Duration: steadyStateWindow},
			},
		},
	})

	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "delete",
		ActionFn: func(ctx context.Context) error {
			return workload.NewWorkloadManager(tc.Client, Logger).DeletePCS(ctx, tc.Namespace, tc.Workload.Name)
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "pcs-deleted",
				Condition: &condition.PCSDeletedCondition{
					Client:    tc.Client.Client,
					Name:      tc.Workload.Name,
					Namespace: tc.Namespace,
				},
			},
		},
	})

	Logger.Info("running timeline tracker")
	result, err := tracker.Run(ctx, toOperatorMetadata(metadata))
	if err != nil {
		t.Fatalf("Timeline tracker run failed: %v", err)
	}
	tracker.Wait()

	Logger.Info("exporting results")
	exportResult(t, result, outputDir)
	Logger.Infof("scale test completed successfully in %.1fs", result.TestDurationSeconds)
}

func exportResult(t *testing.T, result *measurement.TrackerResult, outputDir string) {
	t.Helper()

	outputPath := filepath.Join(outputDir, outputResultsFile)
	jsonFile, err := os.Create(outputPath)
	if err != nil {
		t.Fatalf("Failed to create JSON output file: %v", err)
	}
	defer jsonFile.Close()

	multi := exporter.NewMultiExporter(
		exporter.NewSummaryExporter(os.Stdout),
		exporter.NewJSONExporter(jsonFile),
	)

	if err := multi.Export(result); err != nil {
		t.Fatalf("Failed to export results: %v", err)
	}
}
