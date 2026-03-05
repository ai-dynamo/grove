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

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/ai-dynamo/grove/operator/e2e/utils/measurement"
	"github.com/ai-dynamo/grove/operator/e2e/utils/measurement/condition"
	"github.com/ai-dynamo/grove/operator/e2e/utils/measurement/exporter"
)

const (
	scaleTestExpectedPods = 5000
	scaleTestPollInterval = 2 * time.Second
	scaleTestTimeout      = 15 * time.Minute
)

func Test_ScaleTest_5000_MoE(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scaleTestTimeout)
	defer cancel()

	_, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 1000)
	defer cleanup()

	crClient, err := utils.NewCRClient(restConfig)
	if err != nil {
		t.Fatalf("Failed to create controller-runtime client: %v", err)
	}

	runID := fmt.Sprintf("run-%s", time.Now().Format("20060102-150405"))
	namespace := "default"
	pcsName := "scale-test-5000-moe"
	labelSelector := "app.kubernetes.io/part-of=scale-test-5000-moe"

	tracker := measurement.NewTimelineTracker(
		"ScaleTest_5000_MoE",
		runID,
		namespace,
		1,
		measurement.WithPollInterval(scaleTestPollInterval),
	)

	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "deploy",
		ActionFn: func(ctx context.Context) error {
			_, err := utils.ApplyYAMLFile(ctx, "../yaml/scale-test-5000-moe.yaml", namespace, restConfig, logger)
			return err
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "pods-created",
				Condition: &condition.PodsCreatedCondition{
					Client:        crClient,
					Namespace:     namespace,
					LabelSelector: labelSelector,
					ExpectedCount: scaleTestExpectedPods,
				},
			},
			{
				Name: "pods-ready",
				Condition: &condition.PodsReadyCondition{
					Client:        crClient,
					Namespace:     namespace,
					LabelSelector: labelSelector,
					ExpectedCount: scaleTestExpectedPods,
				},
			},
		},
	})

	tracker.AddPhase(measurement.PhaseDefinition{
		Name: "delete",
		ActionFn: func(ctx context.Context) error {
			return utils.DeletePodCliqueSet(ctx, dynamicClient, namespace, pcsName)
		},
		Milestones: []measurement.MilestoneDefinition{
			{
				Name: "pcs-deleted",
				Condition: &condition.PCSDeletedCondition{
					Client:    crClient,
					Name:      pcsName,
					Namespace: namespace,
				},
			},
		},
	})

	result, err := tracker.Run(ctx)
	if err != nil {
		t.Fatalf("Timeline tracker run failed: %v", err)
	}

	exportResult(t, result, runID)
	assertResult(t, result)
}

func exportResult(t *testing.T, result *measurement.TrackerResult, runID string) {
	t.Helper()

	jsonFile, err := os.Create(fmt.Sprintf("scale-test-5000-moe-%s.json", runID))
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

func assertResult(t *testing.T, result *measurement.TrackerResult) {
	t.Helper()

	if len(result.Phases) != 2 {
		t.Errorf("expected 2 phases, got %d", len(result.Phases))
	}

	if result.TestDurationSeconds <= 0 {
		t.Errorf("expected positive test duration, got %.3f", result.TestDurationSeconds)
	}

	for _, phase := range result.Phases {
		for _, milestone := range phase.Milestones {
			if milestone.DurationFromPhaseStart <= 0 {
				t.Errorf("phase %q milestone %q: expected positive duration, got %.3f",
					phase.Name, milestone.Name, milestone.DurationFromPhaseStart)
			}
		}
	}
}
