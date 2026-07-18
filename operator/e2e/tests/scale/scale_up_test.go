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

package scale

import (
	"context"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/resources"
	"github.com/ai-dynamo/grove/operator/e2e/measurement"
	"github.com/ai-dynamo/grove/operator/e2e/measurement/condition"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
)

const (
	scaleUpTimeout = 15 * time.Minute
	// scaleUpWorkerNodes is intentionally lower than defaultScaleWorkerNodes (100)
	// so these tests run on smaller dev clusters. ~1000 kwok pods on 30 nodes
	// (~33 pods/node) is well under the default 110-pod kubelet limit.
	scaleUpWorkerNodes = 30
)

// scaleUpVariant configures a single scale-up scenario. Each variant boots a PCS
// at initialReplicas (encoded in the YAML) and then patches spec.replicas to
// targetReplicas so the timeline isolates the marginal scale-up cost.
type scaleUpVariant struct {
	name            string
	workloadName    string
	yamlPath        string
	initialReplicas int
	initialPods     int
	targetReplicas  int
	targetPods      int
	// workerNodes overrides scaleUpWorkerNodes for variants that need fewer
	// nodes (e.g. the tiny sanity test). Zero means "use the default".
	workerNodes int
}

// Test_ScaleUp_Tiny is a sanity-check variant that scales from 0 to 5 replicas
// (10 pods). It runs the same code paths as the real benchmarks but completes
// in seconds — use it to validate cluster setup and test plumbing before
// running the 500/1000-pod scenarios.
func Test_ScaleUp_Tiny(t *testing.T) {
	runScaleUpTest(t, scaleUpVariant{
		name:            "ScaleUp_Tiny",
		workloadName:    "scale-up-tiny",
		yamlPath:        "../../yaml/scale-up-tiny.yaml",
		initialReplicas: 0,
		initialPods:     0,
		targetReplicas:  5,
		targetPods:      10,
		workerNodes:     5,
	})
}

// Test_ScaleUp_FromZero scales an existing PCS from 0 to 500 replicas (1000 pods).
// Captures the cold-start case where no PCSGs/PodCliques exist yet.
func Test_ScaleUp_FromZero(t *testing.T) {
	runScaleUpTest(t, scaleUpVariant{
		name:            "ScaleUp_FromZero",
		workloadName:    "scale-up-from-zero",
		yamlPath:        "../../yaml/scale-up-from-zero.yaml",
		initialReplicas: 0,
		initialPods:     0,
		targetReplicas:  500,
		targetPods:      1000,
	})
}

// Test_ScaleUp scales an existing 500-pod PCS by 2x (to 1000 pods). Captures
// the burst-growth case where the controller has to create as many new
// replicas as already exist.
func Test_ScaleUp(t *testing.T) {
	runScaleUpTest(t, scaleUpVariant{
		name:            "ScaleUp",
		workloadName:    "scale-up",
		yamlPath:        "../../yaml/scale-up.yaml",
		initialReplicas: 250,
		initialPods:     500,
		targetReplicas:  500,
		targetPods:      1000,
	})
}

// runScaleUpTest builds the deploy → scale-up → delete timeline for a variant.
// The deploy phase brings the PCS up at the YAML's initial replica count; the
// scale-up phase is the measurement of interest and milestones-out at all-pods-ready.
func runScaleUpTest(t *testing.T, v scaleUpVariant) {
	workerNodes := scaleUpWorkerNodes
	if v.workerNodes > 0 {
		workerNodes = v.workerNodes
	}
	runScaleTest(t, scaleTestConfig{
		name:         v.name,
		workload:     v.workloadName,
		yamlPath:     v.yamlPath,
		expectedPods: v.targetPods,
		pcsCount:     defaultScalePCSCount,
		workerNodes:  workerNodes,
		timeout:      scaleUpTimeout,
		pollInterval: defaultScalePollInterval,
	}, func(tracker *measurement.TimelineTracker, tc *testctx.TestContext, _ string) {
		baseline := &operatorBaseline{}
		addOperatorBaselinePhase(tracker, tc, baseline)

		var deployMilestones []measurement.MilestoneDefinition
		if v.initialPods > 0 {
			deployMilestones = append(deployMilestones,
				measurement.MilestoneDefinition{
					Name: "initial-pods-created",
					Condition: &condition.PodsCreatedCondition{
						Client:        tc.Client.Client,
						Namespace:     tc.Namespace,
						LabelSelector: tc.GetLabelSelector(),
						ExpectedCount: v.initialPods,
					},
				},
				measurement.MilestoneDefinition{
					Name: "initial-pods-ready",
					Condition: &condition.PodsReadyCondition{
						Client:        tc.Client.Client,
						Namespace:     tc.Namespace,
						LabelSelector: tc.GetLabelSelector(),
						ExpectedCount: v.initialPods,
					},
				},
			)
		}

		tracker.AddPhase(measurement.PhaseDefinition{
			Name: "deploy",
			ActionFn: func(ctx context.Context) error {
				_, err := resources.NewResourceManager(tc.Client, Logger).ApplyYAMLFile(ctx, tc.Workload.YAMLPath, tc.Namespace)
				return err
			},
			Milestones: deployMilestones,
		})

		tracker.AddPhase(measurement.PhaseDefinition{
			Name: "scale-up",
			ActionFn: func(ctx context.Context) error {
				Logger.Infof("scaling %s from %d to %d PCS replicas (target %d pods)",
					tc.Workload.Name, v.initialReplicas, v.targetReplicas, v.targetPods)
				return workload.NewWorkloadManager(tc.Client, Logger).ScalePCS(ctx, tc.Namespace, tc.Workload.Name, v.targetReplicas)
			},
			Milestones: []measurement.MilestoneDefinition{
				{
					Name: "all-pods-created",
					Condition: &condition.PodsCreatedCondition{
						Client:        tc.Client.Client,
						Namespace:     tc.Namespace,
						LabelSelector: tc.GetLabelSelector(),
						ExpectedCount: v.targetPods,
					},
				},
				{
					Name: "all-pods-ready",
					Condition: &condition.PodsReadyCondition{
						Client:        tc.Client.Client,
						Namespace:     tc.Namespace,
						LabelSelector: tc.GetLabelSelector(),
						ExpectedCount: v.targetPods,
					},
				},
			},
		})

		addFinalCheckAndDeletePhases(tracker, tc, scaleFinalCheckConfig{
			targetReplicas: v.targetReplicas,
			targetPods:     v.targetPods,
			workloadName:   v.workloadName,
		}, baseline)
	})
}
