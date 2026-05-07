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

	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/assert"
)

// Test_AutoMNNVL_SupportedButDisabled is the test suite for when Auto-MNNVL feature is disabled
// but the ComputeDomain CRD is available in the cluster.
// This tests the "opt-in" behavior where the feature is available but not automatically enabled.
func Test_AutoMNNVL_SupportedButDisabled(t *testing.T) {
	ctx := context.Background()

	// Prepare cluster and get clients (0 = no specific worker node requirement)
	tc, cleanup := testctx.PrepareTest(ctx, t, 0)
	defer cleanup()

	// Detect and validate cluster configuration
	clusterConfig := requireClusterConfig(t, ctx, tc.Client)
	clusterConfig.skipUnless(t, crdSupported, featureDisabled)

	// Define all subtests
	subtests := []struct {
		description string
		fn          func(*testing.T, *testctx.TestContext)
	}{
		{"explicit enabled annotation rejected", testExplicitEnabledAnnotationRejected},
		{"no MNNVL artifacts created", testNoMNNVLArtifactsWhenDisabled},
	}

	// Run all subtests
	for _, tt := range subtests {
		t.Run(tt.description, func(t *testing.T) {
			tt.fn(t, tc)
		})
	}
}

// testExplicitEnabledAnnotationRejected verifies that explicitly setting
// mnnvl-group annotation is rejected when the feature is disabled globally.
func testExplicitEnabledAnnotationRejected(t *testing.T, tc *testctx.TestContext) {
	err := applyMNNVLYAML(tc, "mnnvl-gpu-default.yaml", "test-disabled-reject")
	assert.Error(t, err, "PCS with mnnvl-group annotation should be rejected when feature is disabled")
}
