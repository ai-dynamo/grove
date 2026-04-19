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

package automnnvl

import (
	"context"
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

// Test_AutoMNNVL_UnsupportedButEnabled is the test suite for when Auto-MNNVL feature is enabled
// but the ComputeDomain CRD is NOT available in the cluster.
// This tests that the operator detects the invalid configuration and exits.
func Test_AutoMNNVL_UnsupportedButEnabled(t *testing.T) {
	ctx := context.Background()

	// Prepare cluster and get clients (0 = no specific worker node requirement)
	tc, cleanup := testctx.PrepareTest(ctx, t, 0)
	defer cleanup()

	// Detect and validate cluster configuration
	clusterConfig := requireClusterConfig(t, ctx, tc.Clients)
	clusterConfig.skipUnless(t, crdUnsupported, featureEnabled)

	// Define all subtests
	subtests := []struct {
		description string
		fn          func(*testing.T, *testctx.TestContext)
	}{
		{"operator exits when CD CRD is missing", testOperatorExitsWithoutCDCRD},
	}

	// Run all subtests
	for _, tt := range subtests {
		t.Run(tt.description, func(t *testing.T) {
			tt.fn(t, tc)
		})
	}
}

// testOperatorExitsWithoutCDCRD verifies that the operator fails preflight
// when MNNVL is enabled but the ComputeDomain CRD is missing.
func testOperatorExitsWithoutCDCRD(t *testing.T, tc *testctx.TestContext) {
	pod, err := waitForFailedOperatorPod(tc)
	require.NoError(t, err, "Failed to find grove-operator pod")

	hasTerminated := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Terminated != nil || status.LastTerminationState.Terminated != nil {
			hasTerminated = true
			break
		}
	}
	assert.True(t, hasTerminated, "Operator pod should terminate on preflight failure")

	// Verify logs show preflight failure due to missing CRD.
	// Check both current and previous container logs because the operator
	// crashes on preflight failure and the error message may only appear
	// in the previous (terminated) container's logs.
	err = k8s.WaitForPodLogContains(tc.Ctx, tc.Clients.Clientset, groveOperatorNamespace, pod.Name,
		defaultPollTimeout, defaultPollInterval,
		"MNNVL preflight check failed", "ComputeDomain CRD")
	assert.NoError(t, err, "Operator logs should show preflight failure due to missing CRD")
}

// waitForFailedOperatorPod polls until it finds an operator pod that is NOT
// Ready and has terminated or restarted.
func waitForFailedOperatorPod(tc *testctx.TestContext) (*corev1.Pod, error) {
	pm := pods.NewPodManager(tc.Clients, testctx.Logger)
	return pm.WaitForFailedPod(tc.Ctx, groveOperatorNamespace, "app.kubernetes.io/name=grove-operator",
		defaultPollTimeout, defaultPollInterval)
}
