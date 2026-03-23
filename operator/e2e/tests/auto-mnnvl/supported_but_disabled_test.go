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

	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test_AutoMNNVL_SupportedButDisabled is the test suite for when Auto-MNNVL feature is disabled
// but the ComputeDomain CRD is available in the cluster.
// This tests the "opt-in" behavior where the feature is available but not automatically enabled.
func Test_AutoMNNVL_SupportedButDisabled(t *testing.T) {
	ctx := context.Background()

	// Prepare cluster and get clients (0 = no specific worker node requirement)
	clientset, restConfig, dynamicClient, groveClient, cleanup := prepareTestCluster(ctx, t, 0)
	defer cleanup()

	// Detect and validate cluster configuration
	clusterConfig := requireClusterConfig(t, ctx, clientset, restConfig)
	clusterConfig.skipUnless(t, crdSupported, featureDisabled)

	// Create test context for subtests
	tc := createTestContext(t, ctx, clientset, restConfig, dynamicClient, groveClient, clusterConfig)

	// Define all subtests
	tests := []struct {
		description string
		fn          func(*testing.T, testContext)
	}{
		{"no auto annotation added", testNoAutoAnnotationAdded},
		{"explicit enabled annotation rejected", testExplicitEnabledAnnotationRejected},
		{"no MNNVL artifacts created", testNoMNNVLArtifactsWhenDisabled},
	}

	// Run all subtests
	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			tt.fn(t, tc)
		})
	}
}

// testNoAutoAnnotationAdded verifies that the webhook doesn't add the auto-mnnvl
// annotation when the feature is disabled.
func testNoAutoAnnotationAdded(t *testing.T, tc testContext) {
	pcsName := "test-no-auto-annotation"

	// Create a PCS with GPU requirement (no annotation)
	pcs := buildGPUPCS(pcsName, 1)
	_, err := tc.groveClient.GroveV1alpha1().PodCliqueSets(tc.namespace).Create(tc.ctx, pcs, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create PCS")
	defer deletePCS(tc, pcsName)

	// Verify the PCS does NOT have the auto-mnnvl annotation
	createdPCS, err := tc.groveClient.GroveV1alpha1().PodCliqueSets(tc.namespace).Get(tc.ctx, pcsName, metav1.GetOptions{})
	require.NoError(t, err, "Failed to get created PCS")

	annotations := createdPCS.GetAnnotations()
	_, hasAnnotation := annotations[mnnvl.AnnotationAutoMNNVL]
	assert.False(t, hasAnnotation,
		"PCS should NOT have auto-mnnvl annotation when feature is disabled")
}

// testExplicitEnabledAnnotationRejected verifies that explicitly setting
// auto-mnnvl: enabled is rejected when the feature is disabled globally.
func testExplicitEnabledAnnotationRejected(t *testing.T, tc testContext) {
	pcsName := "test-explicit-enabled-rejected"

	// Create a PCS with explicit enabled annotation
	pcs := buildGPUPCS(pcsName, 1)
	annotations := pcs.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[mnnvl.AnnotationAutoMNNVL] = mnnvl.AnnotationAutoMNNVLEnabled
	pcs.SetAnnotations(annotations)

	_, err := tc.groveClient.GroveV1alpha1().PodCliqueSets(tc.namespace).Create(tc.ctx, pcs, metav1.CreateOptions{})
	assert.Error(t, err, "PCS with auto-mnnvl: enabled should be rejected when feature is disabled")
}
