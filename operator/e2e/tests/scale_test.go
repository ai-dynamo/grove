//go:build e2e

package tests

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
	"testing"
	"time"

	"k8s.io/utils/ptr"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/ai-dynamo/grove/operator/e2e/utils/measurement"
	"github.com/ai-dynamo/grove/operator/e2e/utils/measurement/condition"
	"github.com/ai-dynamo/grove/operator/e2e/utils/measurement/exporter"
)

// toOperatorMetadata converts GroveMetadata (utils package) to the measurement package type.
func toOperatorMetadata(m *utils.GroveMetadata) *measurement.OperatorMetadata {
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

const (
	scaleTestExpectedPods     = 1000
	scaleTestExpectedReplicas = 500
)

func Test_ScaleTest_1000(t *testing.T) {
	diagDir := os.Getenv(DiagnosticsDirEnvVar)
	logger.Infof("starting scale test: %d expected pods, timeout %v", scaleTestExpectedPods, scaleTestTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), scaleTestTimeout)
	defer cancel()

	logger.Info("preparing test cluster with 100 worker nodes")
	clients, cleanup := prepareTestCluster(ctx, t, 100)
	defer cleanup()

	metadata, err := utils.ReadGroveMetadata(ctx, clients.crClient)
	if err != nil {
		t.Fatalf("failed to read grove metadata: %v", err)
	}

	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clients.clientset,
		RestConfig:    clients.restConfig,
		DynamicClient: clients.dynamicClient,
		CRClient:      clients.crClient,
		Namespace:     "default",
		Timeout:       scaleTestTimeout,
		Interval:      scaleTestPollInterval,
		Workload: &WorkloadConfig{
			Name:         "scale-test-1000",
			YAMLPath:     "../yaml/scale-test-1000.yaml",
			Namespace:    "default",
			ExpectedPods: scaleTestExpectedPods,
		},
	}

	runID := fmt.Sprintf("run-%s", time.Now().Format("20060102-150405"))
	logger.Infof("test config: runID=%s, namespace=%s, pcsName=%s", runID, tc.Namespace, tc.Workload.Name)

	tracker := measurement.NewTimelineTracker(
		"ScaleTest_1000",
		runID,
		tc.Namespace,
		1,
		measurement.WithPollInterval(scaleTestPollInterval),
		measurement.WithLogger(logger.GetLogr()),
	)

	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "deploy",
		ActionFn: func(ctx context.Context) error {
			_, err := utils.ApplyYAMLFile(ctx, tc.Workload.YAMLPath, tc.Namespace, tc.RestConfig, logger)
			return err
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "pods-created",
				Condition: &condition.PodsCreatedCondition{
					Client:        tc.CRClient,
					Namespace:     tc.Namespace,
					LabelSelector: tc.getLabelSelector(),
					ExpectedCount: scaleTestExpectedPods,
				},
			},
			{
				Name: "pods-ready",
				Condition: &condition.PodsReadyCondition{
					Client:        tc.CRClient,
					Namespace:     tc.Namespace,
					LabelSelector: tc.getLabelSelector(),
					ExpectedCount: scaleTestExpectedPods,
				},
			},
			{
				Name: "pcs-available",
				Condition: &condition.PCSAvailableCondition{
					Client:        tc.CRClient,
					Name:          tc.Workload.Name,
					Namespace:     tc.Namespace,
					ExpectedCount: scaleTestExpectedReplicas,
				},
			},
		},
	})

	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "delete",
		ActionFn: func(ctx context.Context) error {
			return utils.DeletePodCliqueSet(ctx, tc.DynamicClient, tc.Namespace, tc.Workload.Name)
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "pcs-deleted",
				Condition: &condition.PCSDeletedCondition{
					Client:    tc.CRClient,
					Name:      tc.Workload.Name,
					Namespace: tc.Namespace,
				},
			},
		},
	})

	logger.Info("running timeline tracker")
	result, err := tracker.Run(ctx, toOperatorMetadata(metadata))
	if err != nil {
		t.Fatalf("Timeline tracker run failed: %v", err)
	}

	logger.Info("exporting results")
	exportResult(t, result, diagDir)
	logger.Infof("scale test completed successfully in %.1fs", result.TestDurationSeconds)
}

func exportResult(t *testing.T, result *measurement.TrackerResult, diagDir string) {
	t.Helper()

	filename := fmt.Sprintf("%s-%s.json", result.TestName, result.RunID)
	path := resolveOutputPath(filename, diagDir)

	multi := exporter.NewMultiExporter(
		exporter.NewSummaryExporter(os.Stdout),
		exporter.NewJSONFileExporter(path),
	)
	if err := multi.Export(result); err != nil {
		t.Fatalf("Failed to export results: %v", err)
	}
}
