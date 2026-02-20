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
	"strings"
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test_AutoMNNVL_UnsupportedButEnabled is the test suite for when Auto-MNNVL feature is enabled
// but the ComputeDomain CRD is NOT available in the cluster.
// This tests that the operator detects the invalid configuration and exits.
func Test_AutoMNNVL_UnsupportedButEnabled(t *testing.T) {
	ctx := context.Background()

	// Prepare cluster and get clients (0 = no specific worker node requirement)
	clientset, restConfig, dynamicClient, groveClient, cleanup := prepareTestCluster(ctx, t, 0)
	defer cleanup()

	// Detect and validate cluster configuration
	clusterConfig := requireClusterConfig(t, ctx, clientset, restConfig)
	clusterConfig.skipUnless(t, crdUnsupported, featureEnabled)

	// Create test context for subtests
	tc := createTestContext(t, ctx, clientset, restConfig, dynamicClient, groveClient, clusterConfig)

	// Define all subtests
	tests := []struct {
		description string
		fn          func(*testing.T, testContext)
	}{
		{"operator exits when CD CRD is missing", testOperatorExitsWithoutCDCRD},
	}

	// Run all subtests
	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			tt.fn(t, tc)
		})
	}
}

// testOperatorExitsWithoutCDCRD verifies that the operator fails preflight
// when MNNVL is enabled but the ComputeDomain CRD is missing.
func testOperatorExitsWithoutCDCRD(t *testing.T, tc testContext) {
	pod, err := waitForOperatorPod(tc)
	require.NoError(t, err, "Failed to find grove-operator pod")

	hasTerminated := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Terminated != nil || status.LastTerminationState.Terminated != nil {
			hasTerminated = true
			break
		}
	}
	assert.True(t, hasTerminated, "Operator pod should terminate on preflight failure")

	// Verify logs show preflight failure due to missing CRD
	err = utils.PollForCondition(tc.ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		logs, logErr := tc.clientset.CoreV1().Pods(groveOperatorNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).DoRaw(tc.ctx)
		if logErr != nil {
			return false, nil
		}
		logText := string(logs)
		return strings.Contains(logText, "MNNVL preflight check failed") &&
			strings.Contains(logText, "ComputeDomain CRD"), nil
	})
	assert.NoError(t, err, "Operator logs should show preflight failure due to missing CRD")
}

func waitForOperatorPod(tc testContext) (*corev1.Pod, error) {
	var operatorPod *corev1.Pod
	err := utils.PollForCondition(tc.ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, listErr := tc.clientset.CoreV1().Pods(groveOperatorNamespace).List(tc.ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=grove-operator",
		})
		if listErr != nil || len(pods.Items) == 0 {
			return false, nil
		}
		operatorPod = &pods.Items[0]
		return true, nil
	})
	return operatorPod, err
}
