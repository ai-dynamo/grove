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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
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
		{"no auto annotation added", testNoAutoAnnotationAdded},
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

// testNoAutoAnnotationAdded verifies that the webhook doesn't add the auto-mnnvl
// annotation when the feature is disabled.
func testNoAutoAnnotationAdded(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-no-auto-annotation"

	// Create a PCS with GPU requirement (no annotation)
	pcs := buildGPUPCS(pcsName, 1)
	err := tc.Client.Create(tc.Ctx, pcs)
	require.NoError(t, err, "Failed to create PCS")
	defer deletePCS(tc, pcsName)

	// Verify the PCS does NOT have the auto-mnnvl annotation
	var createdPCS grovecorev1alpha1.PodCliqueSet
	err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
	require.NoError(t, err, "Failed to get created PCS")

	annotations := createdPCS.GetAnnotations()
	_, hasAnnotation := annotations[mnnvl.AnnotationAutoMNNVL]
	assert.False(t, hasAnnotation,
		"PCS should NOT have auto-mnnvl annotation when feature is disabled")
}

// testExplicitEnabledAnnotationRejected verifies that explicitly setting
// auto-mnnvl: enabled is rejected when the feature is disabled globally.
func testExplicitEnabledAnnotationRejected(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-explicit-enabled-rejected"

	// Create a PCS with explicit enabled annotation
	pcs := buildGPUPCS(pcsName, 1)
	annotations := pcs.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[mnnvl.AnnotationAutoMNNVL] = mnnvl.AnnotationAutoMNNVLEnabled
	pcs.SetAnnotations(annotations)

	err := tc.Client.Create(tc.Ctx, pcs)
	assert.Error(t, err, "PCS with auto-mnnvl: enabled should be rejected when feature is disabled")
}
