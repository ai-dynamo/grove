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
)

// Test_AutoMNNVL_UnsupportedAndDisabled is the test suite for when Auto-MNNVL feature is disabled
// and the ComputeDomain CRD is NOT available in the cluster.
// This is the default/baseline configuration where MNNVL has no effect.
func Test_AutoMNNVL_UnsupportedAndDisabled(t *testing.T) {
	ctx := context.Background()

	// Prepare cluster and get clients (0 = no specific worker node requirement)
	clientset, restConfig, dynamicClient, groveClient, cleanup := prepareTestCluster(ctx, t, 0)
	defer cleanup()

	// Detect and validate cluster configuration
	clusterConfig := requireClusterConfig(t, ctx, clientset, restConfig)
	clusterConfig.skipUnless(t, crdUnsupported, featureDisabled)

	// Create test context for subtests
	tc := createTestContext(t, ctx, clientset, restConfig, dynamicClient, groveClient, clusterConfig)

	// Define all subtests
	tests := []struct {
		description string
		fn          func(*testing.T, testContext)
	}{
		{"no MNNVL artifacts created", testNoMNNVLArtifactsWhenDisabled},
	}

	// Run all subtests
	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			tt.fn(t, tc)
		})
	}
}
