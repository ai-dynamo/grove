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

	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	clusterConfig := requireClusterConfig(t, ctx, tc.Client)
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
	pod, err := tc.WaitForFailedPod(groveOperatorNamespace, setup.OperatorPodLabelSelector)
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
	fetchLogs := waiter.FetchFunc[[]byte](func(ctx context.Context) ([]byte, error) {
		var allLogs []byte
		for _, previous := range []bool{false, true} {
			logs, logErr := tc.Client.GetLogs(groveOperatorNamespace, pod.Name, &corev1.PodLogOptions{
				Previous: previous,
			}).DoRaw(ctx)
			if logErr == nil {
				allLogs = append(allLogs, logs...)
			}
		}
		return allLogs, nil
	})
	containsPreflightError := waiter.Predicate[[]byte](func(logs []byte) bool {
		logText := string(logs)
		return strings.Contains(logText, "MNNVL preflight check failed") &&
			strings.Contains(logText, "ComputeDomain CRD")
	})
	w := waiter.New[[]byte]().
		WithTimeout(defaultPollTimeout).
		WithInterval(defaultPollInterval)
	err = w.WaitUntil(tc.Ctx, fetchLogs, containsPreflightError)
	assert.NoError(t, err, "Operator logs should show preflight failure due to missing CRD")
}

// waitForFailedOperatorPod polls until it finds an operator pod that is NOT
// Ready and has terminated or restarted. During a rolling deployment
// (maxUnavailable=0) both the old healthy pod and the new crashing pod coexist.
// The old pod may have RestartCount > 0 from cert-refresh restarts, so we
// filter by !Ready to ensure we return the actually-crashing pod whose logs
// contain the preflight failure.
func waitForFailedOperatorPod(tc *testctx.TestContext) (*corev1.Pod, error) {
	w := waiter.New[*corev1.Pod]().
		WithTimeout(defaultPollTimeout).
		WithInterval(defaultPollInterval)
	fetchFailedPod := waiter.FetchFunc[*corev1.Pod](func(ctx context.Context) (*corev1.Pod, error) {
		var podList corev1.PodList
		listErr := tc.Client.List(ctx, &podList, client.InNamespace(groveOperatorNamespace), setup.OperatorPodLabels)
		pods := &podList
		if listErr != nil || len(pods.Items) == 0 {
			return nil, nil
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			if kubeutils.IsPodReady(pod) {
				continue
			}
			for _, status := range pod.Status.ContainerStatuses {
				if status.State.Terminated != nil ||
					status.LastTerminationState.Terminated != nil ||
					status.RestartCount > 0 {
					return pod, nil
				}
			}
		}
		return nil, nil
	})
	operatorPod, err := w.WaitFor(tc.Ctx, fetchFailedPod, waiter.IsNotZero[*corev1.Pod])
	return operatorPod, err
}
