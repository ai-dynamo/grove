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

package update

import (
	"context"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/tests"
)

// testConfig holds configuration for update strategy test setup (both RollingUpdate and OnDelete).
// This unified config replaces the separate OnDeleteTestConfig and RollingUpdateTestConfig structs.
type testConfig struct {
	// Required
	workerNodes  int // Number of worker nodes required for the test
	expectedPods int // Expected pods after initial deployment

	// Required - workload identity
	workloadName string // Name of the workload (e.g., "workload1" or "workload-ondelete")
	workloadYAML string // Path to the workload YAML file (e.g., "../../yaml/workload1.yaml")

	// Optional - PCS scaling before tracker starts
	initialPCSReplicas int32 // If > 0, scale PCS to this many replicas before starting tracker
	postScalePods      int   // Expected pods after initial PCS scaling (required if InitialPCSReplicas > 0)

	// Optional - SIGTERM patch (rolling update specific)
	patchSIGTERM bool // If true, patch containers to ignore SIGTERM before scaling

	// Optional - PCSG scaling
	initialPCSGReplicas int32  // If > 0, scale PCSGs to this many replicas
	pcsgName            string // Name of the PCSG scaling group (e.g., "sg-x")
	postPCSGScalePods   int    // Expected pods after PCSG scaling

	// Optional - defaults to "default"
	namespace string // Defaults to "default"
}

// setupTest initializes an update strategy test with the given configuration.
// It handles:
// 1. Cluster preparation with required worker nodes
// 2. TestContext creation with standard parameters
// 3. Workload deployment and pod verification
// 4. Optional SIGTERM patch application (before scaling to apply to original workload)
// 5. Optional PCS scaling to initial replicas
// 6. Optional PCSG scaling
// 7. Tracker creation and startup
//
// Returns:
//   - tc: TestContext for the test
//   - cleanup: Function that should be deferred by the caller (stops tracker and cleans up cluster)
//   - tracker: Started update tracker - caller can use tracker.GetEvents() after stopping
func setupTest(t *testing.T, cfg testConfig) (tests.TestContext, func(), *updateTracker) {
	t.Helper()
	ctx := context.Background()

	// Validate required fields
	if cfg.workloadName == "" {
		t.Fatalf("TestConfig.WorkloadName is required")
	}
	if cfg.workloadYAML == "" {
		t.Fatalf("TestConfig.WorkloadYAML is required")
	}

	// Apply defaults
	if cfg.namespace == "" {
		cfg.namespace = "default"
	}

	// Step 1: Prepare test cluster
	clients, clusterCleanup := tests.PrepareTestCluster(ctx, t, cfg.workerNodes)

	// Step 2: Create TestContext
	tc := tests.TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clients.Clientset,
		RestConfig:    clients.RestConfig,
		DynamicClient: clients.DynamicClient,
		CRClient:      clients.CRClient,
		Namespace:     cfg.namespace,
		Timeout:       tests.DefaultPollTimeout,
		Interval:      tests.DefaultPollInterval,
		Workload: &tests.WorkloadConfig{
			Name:         cfg.workloadName,
			YAMLPath:     cfg.workloadYAML,
			Namespace:    cfg.namespace,
			ExpectedPods: cfg.expectedPods,
		},
	}

	// Step 3: Deploy workload and verify initial pods
	pods, err := tests.DeployAndVerifyWorkload(tc)
	if err != nil {
		clusterCleanup()
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	if err := tests.WaitForPods(tc, cfg.expectedPods); err != nil {
		clusterCleanup()
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	if len(pods.Items) != cfg.expectedPods {
		clusterCleanup()
		t.Fatalf("Expected %d pods, but found %d", cfg.expectedPods, len(pods.Items))
	}

	// Step 4: Optional SIGTERM patch (must happen before scaling to apply to original workload)
	if cfg.patchSIGTERM {
		if err := patchPCSWithSIGTERMIgnoringCommand(tc); err != nil {
			clusterCleanup()
			t.Fatalf("Failed to patch PCS with SIGTERM-ignoring command: %v", err)
		}

		tcLongTimeout := tc
		tcLongTimeout.Timeout = 2 * time.Minute
		if err := waitForRollingUpdateComplete(tcLongTimeout, 1); err != nil {
			clusterCleanup()
			t.Fatalf("Failed to wait for SIGTERM patch rolling update to complete: %v", err)
		}
	}

	// Step 5: Optional PCS scaling
	if cfg.initialPCSReplicas > 0 {
		tests.ScalePCSAndWait(tc, cfg.workloadName, cfg.initialPCSReplicas, cfg.postScalePods, 0)

		if err := tests.WaitForPods(tc, cfg.postScalePods); err != nil {
			clusterCleanup()
			t.Fatalf("Failed to wait for pods to be ready after PCS scaling: %v", err)
		}
	}

	// Step 6: Optional PCSG scaling
	if cfg.initialPCSGReplicas > 0 && cfg.pcsgName != "" {
		pcsReplicas := int32(1)
		if cfg.initialPCSReplicas > 0 {
			pcsReplicas = cfg.initialPCSReplicas
		}
		tests.ScalePCSGAcrossAllReplicasAndWait(tc, cfg.workloadName, cfg.pcsgName, pcsReplicas, cfg.initialPCSGReplicas, cfg.postPCSGScalePods, 0)
	}

	// Step 7: Create and start tracker
	tracker := newUpdateTracker()
	if err := tracker.start(tc); err != nil {
		clusterCleanup()
		t.Fatalf("Failed to start tracker: %v", err)
	}

	// Create combined cleanup function
	// Note: clusterCleanup already handles diagnostics collection on failure
	cleanup := func() {
		tracker.stop()
		clusterCleanup()
	}

	return tc, cleanup, tracker
}
