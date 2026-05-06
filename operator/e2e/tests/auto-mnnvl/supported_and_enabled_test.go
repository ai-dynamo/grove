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
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/gvk"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// Test_AutoMNNVL_SupportedAndEnabled is the main test suite for Auto-MNNVL functionality
// when the feature is enabled and ComputeDomain CRD is supported.
// All subtests in this suite will be skipped if the cluster doesn't match these conditions.
func Test_AutoMNNVL_SupportedAndEnabled(t *testing.T) {
	ctx := context.Background()

	// Prepare cluster and get clients (0 = no specific worker node requirement)
	tc, cleanup := testctx.PrepareTest(ctx, t, 0)
	defer cleanup()

	// Detect and validate cluster configuration
	clusterConfig := requireClusterConfig(t, ctx, tc.Client)
	clusterConfig.skipUnless(t, crdSupported, featureEnabled)

	// Define all subtests
	subtests := []struct {
		description string
		fn          func(*testing.T, *testctx.TestContext)
	}{
		{"no annotation no MNNVL", testNoAnnotationNoMNNVL},
		{"ComputeDomain and claim injection", testMNNVLComputeDomainAndClaimInjection},
		{"scale out and in manages ComputeDomains", testScaleOutAndIn},
		{"PCS deletion cascades to ComputeDomain", testPCSDeletionCascadesToCD},
		{"explicit opt-out is honored", testExplicitOptOutHonored},
		{"invalid annotation is rejected", testInvalidAnnotationRejected},
		{"annotation is immutable", testAnnotationImmutability},
		{"multiple groups in one PCS", testMultipleGroupsInOnePCS},
		{"PCLQ opt-out overrides parent", testPCLQOptOutOverridesParent},
		{"three-layer all different groups", testThreeLayerPCSPCSGPCLQAllDifferent},
		{"CPU PCLQ ignored even with annotation", testCPUPCLQIgnoredEvenWithAnnotation},
		{"MNNVL end to end", testMNNVLEndToEnd},
	}

	// Run all subtests
	for _, tt := range subtests {
		t.Run(tt.description, func(t *testing.T) {
			tt.fn(t, tc)
		})
	}
}

// testNoAnnotationNoMNNVL verifies that a GPU PCS without an mnnvl-group
// annotation does not get MNNVL behaviour: no ComputeDomain, no RCT in PodSpec.
func testNoAnnotationNoMNNVL(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-gpu-no-mnnvl"

	err := applyMNNVLYAML(tc, "mnnvl-gpu-bare.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	var createdPCS grovecorev1alpha1.PodCliqueSet
	err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
	require.NoError(t, err, "Failed to get created PCS")

	annotations := createdPCS.GetAnnotations()
	_, hasAnnotation := annotations[mnnvl.AnnotationMNNVLGroup]
	assert.False(t, hasAnnotation, "GPU PCS should NOT receive mnnvl-group annotation automatically")

	pclqName := fmt.Sprintf("%s-0-gpu-worker", pcsName)
	pclq, err := waitForPCLQ(tc, pclqName)
	require.NoError(t, err, "Failed to wait for PCLQ")

	assert.Empty(t, pclq.Spec.PodSpec.ResourceClaims, "PCLQ should not have resourceClaims without mnnvl-group annotation")

	cdName := fmt.Sprintf("%s-0-default", pcsName)
	err = getComputeDomain(tc, cdName)
	assert.Error(t, err, "No ComputeDomain should exist for a PCS without MNNVL opt-in")
}

// testMNNVLComputeDomainAndClaimInjection verifies CD creation per replica with correct
// metadata, RCT injection across standalone/SG cliques, and annotation propagation to PCSGs.
func testMNNVLComputeDomainAndClaimInjection(t *testing.T, tc *testctx.TestContext) {
	pcsName := "inj-test"
	desiredReplicas := 2

	err := applyMNNVLYAML(tc, "mnnvl-comprehensive.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	err = scalePCS(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to scale PCS to %d replicas", desiredReplicas)

	var createdPCS grovecorev1alpha1.PodCliqueSet
	err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
	require.NoError(t, err, "Failed to get created PCS")

	// --- Verify ComputeDomains ---

	err = waitForComputeDomainCount(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to wait for ComputeDomains")

	for i := 0; i < desiredReplicas; i++ {
		verifyComputeDomainContent(t, tc, pcsName, i, "default", createdPCS.GetUID())
	}

	// Verify finalizer blocks deletion
	cdName := fmt.Sprintf("%s-0-default", pcsName)
	cdObj := &unstructured.Unstructured{}
	cdObj.SetGroupVersionKind(gvk.ComputeDomain)
	cdObj.SetName(cdName)
	cdObj.SetNamespace(tc.Namespace)
	err = tc.Client.Delete(tc.Ctx, cdObj)
	require.NoError(t, err, "Delete request should succeed (sets DeletionTimestamp)")

	err = waitForComputeDomainCount(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Controller should recreate deleted ComputeDomain")
	verifyComputeDomainContent(t, tc, pcsName, 0, "default", createdPCS.GetUID())

	// --- Verify standalone cliques ---

	// 1. gpu1: should have claim, GPU container refs it, non-GPU doesn't
	t.Run("standalone GPU mixed clique", func(t *testing.T) {
		pclqName := fmt.Sprintf("%s-0-gpu1", pcsName)
		pclq, err := waitForPCLQ(tc, pclqName)
		require.NoError(t, err, "Failed to wait for PCLQ")

		// Should have resourceClaim
		requirePodSpecMNNVLClaim(t, &pclq.Spec.PodSpec, pcsName, 0, "default")

		// Check containers
		for i := range pclq.Spec.PodSpec.Containers {
			container := &pclq.Spec.PodSpec.Containers[i]
			if container.Name == "gpu" {
				requireContainerMNNVLClaim(t, container)
			} else if container.Name == "cpu" {
				requireNoContainerMNNVLClaim(t, container)
			}
		}
	})

	// 2. cpu1: no claims
	t.Run("standalone CPU only clique", func(t *testing.T) {
		pclqName := fmt.Sprintf("%s-0-cpu1", pcsName)
		pclq, err := waitForPCLQ(tc, pclqName)
		require.NoError(t, err, "Failed to wait for PCLQ")

		assert.Empty(t, pclq.Spec.PodSpec.ResourceClaims, "CPU-only clique should not have resourceClaims")
		for i := range pclq.Spec.PodSpec.Containers {
			requireNoContainerMNNVLClaim(t, &pclq.Spec.PodSpec.Containers[i])
		}
	})

	// --- Verify sg1 cliques ---

	// 3. gpu2: should have claim, GPU container refs it, non-GPU doesn't
	t.Run("sg1 GPU mixed clique", func(t *testing.T) {
		pclqName := fmt.Sprintf("%s-0-sg1-0-gpu2", pcsName)
		pclq, err := waitForPCLQ(tc, pclqName)
		require.NoError(t, err, "Failed to wait for PCLQ")

		requirePodSpecMNNVLClaim(t, &pclq.Spec.PodSpec, pcsName, 0, "default")

		for i := range pclq.Spec.PodSpec.Containers {
			container := &pclq.Spec.PodSpec.Containers[i]
			if container.Name == "gpu" {
				requireContainerMNNVLClaim(t, container)
			} else if container.Name == "cpu" {
				requireNoContainerMNNVLClaim(t, container)
			}
		}
	})

	// 4. cpu2: no claims
	t.Run("sg1 CPU only clique", func(t *testing.T) {
		pclqName := fmt.Sprintf("%s-0-sg1-0-cpu2", pcsName)
		pclq, err := waitForPCLQ(tc, pclqName)
		require.NoError(t, err, "Failed to wait for PCLQ")

		assert.Empty(t, pclq.Spec.PodSpec.ResourceClaims, "CPU-only clique should not have resourceClaims")
		for i := range pclq.Spec.PodSpec.Containers {
			requireNoContainerMNNVLClaim(t, &pclq.Spec.PodSpec.Containers[i])
		}
	})

	// --- Verify sg2 clique ---

	// 5. cpu3: no claims
	t.Run("sg2 CPU only clique", func(t *testing.T) {
		pclqName := fmt.Sprintf("%s-0-sg2-0-cpu3", pcsName)
		pclq, err := waitForPCLQ(tc, pclqName)
		require.NoError(t, err, "Failed to wait for PCLQ")

		assert.Empty(t, pclq.Spec.PodSpec.ResourceClaims, "CPU-only clique should not have resourceClaims")
		for i := range pclq.Spec.PodSpec.Containers {
			requireNoContainerMNNVLClaim(t, &pclq.Spec.PodSpec.Containers[i])
		}
	})

	// --- Verify PCSGs get annotation propagated ---

	t.Run("sg1 has annotation", func(t *testing.T) {
		pcsgName := fmt.Sprintf("%s-0-sg1", pcsName)
		pcsg, err := waitForPCSG(tc, pcsgName)
		require.NoError(t, err, "Failed to wait for sg1")

		assert.Equal(t, "default", pcsg.GetAnnotations()[mnnvl.AnnotationMNNVLGroup],
			"sg1 should have mnnvl-group annotation propagated")
	})

	t.Run("sg2 has annotation", func(t *testing.T) {
		pcsgName := fmt.Sprintf("%s-0-sg2", pcsName)
		pcsg, err := waitForPCSG(tc, pcsgName)
		require.NoError(t, err, "Failed to wait for sg2")

		assert.Equal(t, "default", pcsg.GetAnnotations()[mnnvl.AnnotationMNNVLGroup],
			"sg2 should have mnnvl-group annotation propagated")
	})
}

// testScaleOutAndIn verifies that scaling out creates new ComputeDomains with correct content,
// and scaling in deletes excess ComputeDomains.
func testScaleOutAndIn(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-scale-cd"

	err := applyMNNVLYAML(tc, "mnnvl-gpu-default.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	var createdPCS grovecorev1alpha1.PodCliqueSet
	err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
	require.NoError(t, err, "Failed to get created PCS")

	desiredReplicas := 1
	err = waitForComputeDomainCount(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to wait for initial ComputeDomain")

	verifyComputeDomainContent(t, tc, pcsName, 0, "default", createdPCS.GetUID())

	// Scale Out: 1 -> 3
	previousReplicas := desiredReplicas
	desiredReplicas = 3
	err = scalePCS(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to scale out PCS")

	err = waitForComputeDomainCount(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to wait for scaled-out ComputeDomains")

	for i := 0; i < desiredReplicas; i++ {
		verifyComputeDomainContent(t, tc, pcsName, i, "default", createdPCS.GetUID())
	}

	// Scale In: 3 -> 1
	previousReplicas = desiredReplicas
	desiredReplicas = 1
	err = scalePCS(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to scale in PCS")

	err = waitForComputeDomainCount(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to wait for scaled-in ComputeDomains")

	verifyComputeDomainContent(t, tc, pcsName, 0, "default", createdPCS.GetUID())

	for i := desiredReplicas; i < previousReplicas; i++ {
		err = getComputeDomain(tc, fmt.Sprintf("%s-%d-default", pcsName, i))
		assert.Error(t, err, "ComputeDomain %d should be deleted after scale-in", i)
	}
}

// testPCSDeletionCascadesToCD verifies that deleting PCS also deletes ComputeDomains.
func testPCSDeletionCascadesToCD(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-del-cascade"
	desiredReplicas := 2

	err := applyMNNVLYAML(tc, "mnnvl-gpu-default.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")

	err = scalePCS(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to scale PCS to %d replicas", desiredReplicas)

	err = waitForComputeDomainCount(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to wait for ComputeDomains")

	deletePCS(tc, pcsName)

	err = waitForComputeDomainCount(tc, pcsName, 0)
	assert.NoError(t, err, "ComputeDomains should be deleted when PCS is deleted")
}

// testExplicitOptOutHonored verifies that mnnvl-group: "none" prevents injection.
func testExplicitOptOutHonored(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-explicit-optout"

	err := applyMNNVLYAML(tc, "mnnvl-gpu-optout.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	pclqName := fmt.Sprintf("%s-0-gpu-worker", pcsName)
	pclq, err := waitForPCLQ(tc, pclqName)
	require.NoError(t, err, "Failed to wait for PCLQ")

	cdName := fmt.Sprintf("%s-0-none", pcsName)
	err = getComputeDomain(tc, cdName)
	assert.Error(t, err, "No ComputeDomain should be created when mnnvl-group is 'none'")

	for _, claim := range pclq.Spec.PodSpec.ResourceClaims {
		assert.NotEqual(t, mnnvl.MNNVLClaimName, claim.Name, "PCLQ should not have MNNVL resourceClaim")
	}
	for i := range pclq.Spec.PodSpec.Containers {
		requireNoContainerMNNVLClaim(t, &pclq.Spec.PodSpec.Containers[i])
	}
}

// testInvalidAnnotationRejected verifies that invalid annotation values are rejected.
func testInvalidAnnotationRejected(t *testing.T, tc *testctx.TestContext) {
	err := applyMNNVLYAML(tc, "mnnvl-gpu-invalid.yaml", "test-invalid-annot")
	assert.Error(t, err, "PCS with invalid mnnvl-group value should be rejected")
}

// testAnnotationImmutability verifies that the mnnvl-group annotation cannot be changed after creation.
// Retries on conflict (409) since controllers may update the PCS between our Get and Update.
func testAnnotationImmutability(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-annot-immut"

	err := applyMNNVLYAML(tc, "mnnvl-gpu-default.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	// Retry on conflict: applyMNNVLYAML doesn't return the created object, so we
	// need a separate Get. Between Get and Update a controller may reconcile the
	// PCS, bumping its resourceVersion and causing a 409 Conflict before the
	// validating webhook gets a chance to reject the mutation.
	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		var pcs grovecorev1alpha1.PodCliqueSet
		err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &pcs)
		require.NoError(t, err, "Failed to get PCS")

		require.Equal(t, "default", pcs.GetAnnotations()[mnnvl.AnnotationMNNVLGroup])

		pcs.Annotations[mnnvl.AnnotationMNNVLGroup] = "other"
		err = tc.Client.Update(tc.Ctx, &pcs)

		if k8serrors.IsConflict(err) {
			continue
		}

		assert.Error(t, err, "Changing mnnvl-group annotation should be rejected")
		assert.Contains(t, err.Error(), "immutable", "Error should mention immutability")
		return
	}
	t.Fatalf("Update kept hitting resource version conflicts after %d retries", maxRetries)
}

// testMultipleGroupsInOnePCS verifies that multiple MNNVL groups in one PCS produce
// separate CDs per replica.
func testMultipleGroupsInOnePCS(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-multi-grp"
	desiredReplicas := 2
	groups := []string{"default", "train"}

	err := applyMNNVLYAML(tc, "mnnvl-two-groups.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	var createdPCS grovecorev1alpha1.PodCliqueSet
	err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
	require.NoError(t, err)

	err = waitForComputeDomainCount(tc, pcsName, desiredReplicas*len(groups))
	require.NoError(t, err, "Expected %d CDs (%d replicas x %d groups)", desiredReplicas*len(groups), desiredReplicas, len(groups))

	for i := 0; i < desiredReplicas; i++ {
		for _, grp := range groups {
			verifyComputeDomainContent(t, tc, pcsName, i, grp, createdPCS.GetUID())
		}
	}

	// Verify g1 inherits "default"
	pclq1, err := waitForPCLQ(tc, fmt.Sprintf("%s-0-g1", pcsName))
	require.NoError(t, err)
	requirePodSpecMNNVLClaim(t, &pclq1.Spec.PodSpec, pcsName, 0, "default")

	// Verify g2 uses "train"
	pclq2, err := waitForPCLQ(tc, fmt.Sprintf("%s-0-g2", pcsName))
	require.NoError(t, err)
	requirePodSpecMNNVLClaim(t, &pclq2.Spec.PodSpec, pcsName, 0, "train")
}

// testPCLQOptOutOverridesParent verifies PCLQ "none" overrides PCS enrollment.
func testPCLQOptOutOverridesParent(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-pclq-optout"

	err := applyMNNVLYAML(tc, "mnnvl-pclq-optout.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	err = waitForComputeDomainCount(tc, pcsName, 1)
	require.NoError(t, err, "Only 1 CD for the 'default' group")

	// g1 inherits, gets claims
	pclq1, err := waitForPCLQ(tc, fmt.Sprintf("%s-0-g1", pcsName))
	require.NoError(t, err)
	requirePodSpecMNNVLClaim(t, &pclq1.Spec.PodSpec, pcsName, 0, "default")

	// g2 opts out, no claims
	pclq2, err := waitForPCLQ(tc, fmt.Sprintf("%s-0-g2", pcsName))
	require.NoError(t, err)
	requireNoPodSpecMNNVLClaim(t, &pclq2.Spec.PodSpec)
}

// testThreeLayerPCSPCSGPCLQAllDifferent verifies each layer fully overrides parent,
// producing 3 distinct groups.
func testThreeLayerPCSPCSGPCLQAllDifferent(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-3layer"

	err := applyMNNVLYAML(tc, "mnnvl-three-layer.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	var createdPCS grovecorev1alpha1.PodCliqueSet
	err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
	require.NoError(t, err)

	err = waitForComputeDomainCount(tc, pcsName, 3)
	require.NoError(t, err, "Expected 3 CDs (default, train, infer)")

	verifyComputeDomainContent(t, tc, pcsName, 0, "default", createdPCS.GetUID())
	verifyComputeDomainContent(t, tc, pcsName, 0, "train", createdPCS.GetUID())
	verifyComputeDomainContent(t, tc, pcsName, 0, "infer", createdPCS.GetUID())

	// Standalone: PCS "default"
	saClq, err := waitForPCLQ(tc, fmt.Sprintf("%s-0-sa", pcsName))
	require.NoError(t, err)
	requirePodSpecMNNVLClaim(t, &saClq.Spec.PodSpec, pcsName, 0, "default")

	// gi: inherits PCSG "train"
	giClq, err := waitForPCLQ(tc, fmt.Sprintf("%s-0-sg-0-gi", pcsName))
	require.NoError(t, err)
	requirePodSpecMNNVLClaim(t, &giClq.Spec.PodSpec, pcsName, 0, "train")

	// go: overrides PCSG to "infer"
	goClq, err := waitForPCLQ(tc, fmt.Sprintf("%s-0-sg-0-go", pcsName))
	require.NoError(t, err)
	requirePodSpecMNNVLClaim(t, &goClq.Spec.PodSpec, pcsName, 0, "infer")
}

// testCPUPCLQIgnoredEvenWithAnnotation verifies CPU PCLQs are silently skipped.
func testCPUPCLQIgnoredEvenWithAnnotation(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-cpu-ignored"

	err := applyMNNVLYAML(tc, "mnnvl-cpu-with-annotation.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	err = waitForComputeDomainCount(tc, pcsName, 1)
	require.NoError(t, err, "Only 1 CD for the GPU PCLQ")

	// GPU PCLQ gets claims
	gpuPclq, err := waitForPCLQ(tc, fmt.Sprintf("%s-0-gp", pcsName))
	require.NoError(t, err)
	requirePodSpecMNNVLClaim(t, &gpuPclq.Spec.PodSpec, pcsName, 0, "default")

	// CPU PCLQ does not get claims despite having the annotation
	cpuPclq, err := waitForPCLQ(tc, fmt.Sprintf("%s-0-cp", pcsName))
	require.NoError(t, err)
	requireNoPodSpecMNNVLClaim(t, &cpuPclq.Spec.PodSpec)
}

// testMNNVLEndToEnd exercises all inherit/override/none combinations at every
// layer (PCS, PCSG, PCLQ), then scales out, scales in, and deletes the PCS,
// verifying ComputeDomains and claims throughout the full lifecycle.
func testMNNVLEndToEnd(t *testing.T, tc *testctx.TestContext) {
	pcsName := "mnnvl-e2e"

	err := applyMNNVLYAML(tc, "mnnvl-comprehensive-mix.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	var createdPCS grovecorev1alpha1.PodCliqueSet
	err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
	require.NoError(t, err)

	groups := []string{"default", "train", "infer", "batch", "solo"}

	err = waitForComputeDomainCount(tc, pcsName, len(groups))
	require.NoError(t, err, "Expected %d CDs for 1 replica", len(groups))

	for _, grp := range groups {
		verifyComputeDomainContent(t, tc, pcsName, 0, grp, createdPCS.GetUID())
	}

	// --- Verify PCLQs with claims (7 total) ---
	type claimCheck struct {
		name      string
		groupName string
	}
	withClaims := []claimCheck{
		{fmt.Sprintf("%s-0-sa1", pcsName), "default"},     // standalone, inherit
		{fmt.Sprintf("%s-0-sa2", pcsName), "train"},       // standalone, override
		{fmt.Sprintf("%s-0-g1-0-p1", pcsName), "default"}, // g1 (inherit), p1 inherit
		{fmt.Sprintf("%s-0-g1-0-p2", pcsName), "infer"},   // g1 (inherit), p2 override
		{fmt.Sprintf("%s-0-g2-0-p4", pcsName), "train"},   // g2 (train), p4 inherit
		{fmt.Sprintf("%s-0-g2-0-p5", pcsName), "batch"},   // g2 (train), p5 override
		{fmt.Sprintf("%s-0-g3-0-p8", pcsName), "solo"},    // g3 (none), p8 override
	}
	for _, cc := range withClaims {
		t.Run(fmt.Sprintf("claims-%s", cc.name), func(t *testing.T) {
			pclq, err := waitForPCLQ(tc, cc.name)
			require.NoError(t, err, "Failed to wait for PCLQ %s", cc.name)
			requirePodSpecMNNVLClaim(t, &pclq.Spec.PodSpec, pcsName, 0, cc.groupName)
		})
	}

	// --- Verify PCLQs without claims (5 total) ---
	noClaims := []string{
		fmt.Sprintf("%s-0-sa3", pcsName),     // standalone, none
		fmt.Sprintf("%s-0-g1-0-p3", pcsName), // g1 (inherit), p3 none
		fmt.Sprintf("%s-0-g2-0-p6", pcsName), // g2 (train), p6 none
		fmt.Sprintf("%s-0-g3-0-p7", pcsName), // g3 (none), p7 inherit -> none
		fmt.Sprintf("%s-0-g3-0-p9", pcsName), // g3 (none), p9 none
	}
	for _, pclqName := range noClaims {
		t.Run(fmt.Sprintf("no-claims-%s", pclqName), func(t *testing.T) {
			pclq, err := waitForPCLQ(tc, pclqName)
			require.NoError(t, err, "Failed to wait for PCLQ %s", pclqName)
			requireNoPodSpecMNNVLClaim(t, &pclq.Spec.PodSpec)
		})
	}

	// --- Scale Out: 1 -> 4 replicas ---
	numGroups := len(groups)
	desiredReplicas := 4
	err = scalePCS(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to scale out PCS")

	err = waitForComputeDomainCount(tc, pcsName, numGroups*desiredReplicas)
	require.NoError(t, err, "Expected %d CDs after scale-out to %d replicas", numGroups*desiredReplicas, desiredReplicas)

	// --- Scale In: 4 -> 2 replicas ---
	previousReplicas := desiredReplicas
	desiredReplicas = 2
	err = scalePCS(tc, pcsName, desiredReplicas)
	require.NoError(t, err, "Failed to scale in PCS")

	err = waitForComputeDomainCount(tc, pcsName, numGroups*desiredReplicas)
	require.NoError(t, err, "Expected %d CDs after scale-in to %d replicas", numGroups*desiredReplicas, desiredReplicas)

	// Verify excess replica CDs are gone
	for i := desiredReplicas; i < previousReplicas; i++ {
		for _, grp := range groups {
			err = getComputeDomain(tc, fmt.Sprintf("%s-%d-%s", pcsName, i, grp))
			assert.Error(t, err, "CD for replica %d group %s should be deleted", i, grp)
		}
	}

	// --- Delete PCS -> 0 CDs ---
	deletePCS(tc, pcsName)
	err = waitForComputeDomainCount(tc, pcsName, 0)
	assert.NoError(t, err, "All CDs should be deleted when PCS is deleted")
}

// --- Helper / assertion functions ---

// getComputeDomain attempts to get a ComputeDomain by name. Returns error if not found.
func getComputeDomain(tc *testctx.TestContext, name string) error {
	cd := &unstructured.Unstructured{}
	cd.SetGroupVersionKind(gvk.ComputeDomain)
	return tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: name}, cd)
}

// verifyComputeDomainContent verifies that a ComputeDomain exists with correct metadata and spec.
func verifyComputeDomainContent(t *testing.T, tc *testctx.TestContext, pcsName string, replicaIndex int, groupName string, pcsUID types.UID) {
	t.Helper()

	cdName := fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, groupName)
	cd := &unstructured.Unstructured{}
	cd.SetGroupVersionKind(gvk.ComputeDomain)
	err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: cdName}, cd)
	require.NoError(t, err, "ComputeDomain %s should exist", cdName)

	finalizers := cd.GetFinalizers()
	assert.Contains(t, finalizers, mnnvl.FinalizerComputeDomain,
		"ComputeDomain %s should have Grove finalizer", cdName)

	ownerRefs := cd.GetOwnerReferences()
	require.Len(t, ownerRefs, 1, "ComputeDomain %s should have exactly one owner reference", cdName)
	assert.Equal(t, "PodCliqueSet", ownerRefs[0].Kind)
	assert.Equal(t, pcsName, ownerRefs[0].Name)
	assert.Equal(t, pcsUID, ownerRefs[0].UID)

	labels := cd.GetLabels()
	assert.Equal(t, pcsName, labels[apicommon.LabelPartOfKey])
	assert.Equal(t, fmt.Sprintf("%d", replicaIndex), labels[apicommon.LabelPodCliqueSetReplicaIndex])

	numNodes, found, err := unstructured.NestedInt64(cd.Object, "spec", "numNodes")
	require.NoError(t, err)
	assert.True(t, found, "numNodes should be set for ComputeDomain %s", cdName)
	assert.Equal(t, int64(0), numNodes, "numNodes should be 0 for elastic mode")

	rctName, found, err := unstructured.NestedString(cd.Object, "spec", "channel", "resourceClaimTemplate", "name")
	require.NoError(t, err)
	assert.True(t, found, "resourceClaimTemplate.name should be set for ComputeDomain %s", cdName)
	expectedRCTName := fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, groupName)
	assert.Equal(t, expectedRCTName, rctName, "ComputeDomain %s should reference correct RCT", cdName)
}

// requirePodSpecMNNVLClaim asserts the PodSpec includes the MNNVL claim
// and the expected ResourceClaimTemplate reference for the PCS replica and group.
func requirePodSpecMNNVLClaim(t *testing.T, podSpec *corev1.PodSpec, pcsName string, replicaIndex int, groupName string) {
	t.Helper()

	require.NotNil(t, podSpec, "PodSpec should not be nil")
	require.NotEmpty(t, podSpec.ResourceClaims, "GPU clique should have resourceClaims")

	var mnnvlClaim *corev1.PodResourceClaim
	for i := range podSpec.ResourceClaims {
		if podSpec.ResourceClaims[i].Name == mnnvl.MNNVLClaimName {
			mnnvlClaim = &podSpec.ResourceClaims[i]
			break
		}
	}
	require.NotNil(t, mnnvlClaim, "GPU clique should include MNNVL resourceClaim")
	require.NotNil(t, mnnvlClaim.ResourceClaimTemplateName,
		"GPU clique should reference a ResourceClaimTemplate")

	expectedRCTName := mnnvl.GenerateRCTName(apicommon.ResourceNameReplica{Name: pcsName, Replica: replicaIndex}, groupName)
	assert.Equal(t, expectedRCTName, *mnnvlClaim.ResourceClaimTemplateName)
}

// requireNoPodSpecMNNVLClaim asserts the PodSpec does NOT include the MNNVL claim.
func requireNoPodSpecMNNVLClaim(t *testing.T, podSpec *corev1.PodSpec) {
	t.Helper()
	for _, claim := range podSpec.ResourceClaims {
		assert.NotEqual(t, mnnvl.MNNVLClaimName, claim.Name, "PodSpec should not have MNNVL resourceClaim")
	}
	for i := range podSpec.Containers {
		requireNoContainerMNNVLClaim(t, &podSpec.Containers[i])
	}
}

// requireContainerMNNVLClaim asserts the container references the MNNVL claim.
func requireContainerMNNVLClaim(t *testing.T, container *corev1.Container) {
	t.Helper()

	require.NotNil(t, container, "Container should not be nil")
	require.NotEmpty(t, container.Resources.Claims, "GPU container should have claim reference")

	for _, claim := range container.Resources.Claims {
		if claim.Name == mnnvl.MNNVLClaimName {
			return
		}
	}

	assert.Fail(t, "GPU container should reference the MNNVL claim")
}

// requireNoContainerMNNVLClaim asserts the container does not reference the MNNVL claim.
func requireNoContainerMNNVLClaim(t *testing.T, container *corev1.Container) {
	t.Helper()

	require.NotNil(t, container, "Container should not be nil")
	for _, claim := range container.Resources.Claims {
		if claim.Name == mnnvl.MNNVLClaimName {
			assert.Fail(t, "Non-GPU container should not reference the MNNVL claim")
			return
		}
	}
}
