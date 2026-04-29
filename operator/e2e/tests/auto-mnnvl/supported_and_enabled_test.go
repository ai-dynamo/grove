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
		{"PCS gets auto-mnnvl annotation", testPCSGetsAutoAnnotation},
		{"ComputeDomain created per replica with correct metadata and spec", testComputeDomainCreatedPerReplica},
		{"resourceClaim injection and annotation propagation", testResourceClaimInjection},
		{"scale out and in manages ComputeDomains", testScaleOutAndIn},
		{"PCS deletion cascades to ComputeDomain", testPCSDeletionCascadesToCD},
		{"explicit disabled annotation is honored", testExplicitDisabledAnnotationHonored},
		{"invalid annotation is rejected", testInvalidAnnotationRejected},
		{"annotation is immutable", testAnnotationImmutability},
	}

	// Run all subtests
	for _, tt := range subtests {
		t.Run(tt.description, func(t *testing.T) {
			tt.fn(t, tc)
		})
	}
}

// testPCSGetsAutoAnnotation verifies opt-in semantics: the webhook no longer
// auto-injects the grove.io/auto-mnnvl annotation, so a GPU PCS without an
// explicit annotation does not get MNNVL behaviour (no ComputeDomain).
// The CPU-only sub-test confirms CPU PCSes are likewise unaffected.
func testPCSGetsAutoAnnotation(t *testing.T, tc *testctx.TestContext) {
	t.Run("GPU PCS without annotation does not get MNNVL", func(t *testing.T) {
		pcsName := "test-gpu-no-mnnvl"

		pcs := buildGPUPCS(pcsName, 1)
		err := tc.Client.Create(tc.Ctx, pcs)
		require.NoError(t, err, "Failed to create PCS")
		defer deletePCS(tc, pcsName)

		var createdPCS grovecorev1alpha1.PodCliqueSet
		err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
		require.NoError(t, err, "Failed to get created PCS")

		annotations := createdPCS.GetAnnotations()
		_, hasAnnotation := annotations[mnnvl.AnnotationAutoMNNVL]
		assert.False(t, hasAnnotation, "GPU PCS should NOT receive auto-mnnvl annotation automatically")

		// Wait for PCLQ so the reconciler has processed the PCS,
		// then verify no ComputeDomain was created.
		pclqName := fmt.Sprintf("%s-0-gpu-worker", pcsName)
		_, err = waitForPCLQ(tc, pclqName)
		require.NoError(t, err, "Failed to wait for PCLQ")

		cdName := fmt.Sprintf("%s-0", pcsName)
		err = getComputeDomain(tc, cdName)
		assert.Error(t, err, "No ComputeDomain should exist for a PCS without MNNVL opt-in")
	})

	t.Run("CPU-only PCS does not get annotation", func(t *testing.T) {
		pcsName := "test-cpu-annotation"

		pcs := buildCPUOnlyPCS(pcsName, 1)
		err := tc.Client.Create(tc.Ctx, pcs)
		require.NoError(t, err, "Failed to create PCS")
		defer deletePCS(tc, pcsName)

		var createdPCS grovecorev1alpha1.PodCliqueSet
		err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
		require.NoError(t, err, "Failed to get created PCS")

		annotations := createdPCS.GetAnnotations()
		_, hasAnnotation := annotations[mnnvl.AnnotationAutoMNNVL]
		assert.False(t, hasAnnotation, "CPU-only PCS should NOT have auto-mnnvl annotation")
	})
}

// testComputeDomainCreatedPerReplica verifies that one ComputeDomain is created
// for each PCS replica, with correct metadata (finalizer, ownerRef, labels) and
// spec (numNodes=0, RCT reference).
func testComputeDomainCreatedPerReplica(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-cd-per-replica"
	replicas := 2

	pcs := buildGPUPCSWithMNNVL(pcsName, replicas)
	err := tc.Client.Create(tc.Ctx, pcs)
	require.NoError(t, err, "Failed to create PCS")
	defer deletePCS(tc, pcsName)

	var createdPCS grovecorev1alpha1.PodCliqueSet
	err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
	require.NoError(t, err, "Failed to get created PCS")

	err = waitForComputeDomainCount(tc, pcsName, replicas)
	require.NoError(t, err, "Failed to wait for ComputeDomains")

	// Verify each replica has its own ComputeDomain with correct metadata and spec
	for i := 0; i < replicas; i++ {
		verifyComputeDomainContent(t, tc, pcsName, i, createdPCS.GetUID())
	}

	// Deleting a CD should be blocked while the PCS replica still needs it.
	// The finalizer should prevent deletion until the PCS replica is no longer using it.
	cdName := fmt.Sprintf("%s-0", pcsName)
	cdObj := &unstructured.Unstructured{}
	cdObj.SetGroupVersionKind(gvk.ComputeDomain)
	cdObj.SetName(cdName)
	cdObj.SetNamespace(tc.Namespace)
	err = tc.Client.Delete(tc.Ctx, cdObj)
	require.NoError(t, err, "Delete request should succeed (sets DeletionTimestamp)")

	// CD should not be deleted while PCS still needs it (finalizer blocks deletion)
	err = waitForComputeDomainCount(tc, pcsName, replicas)
	require.NoError(t, err, "Controller should recreate deleted ComputeDomain")

	// Verify the CD has correct content
	verifyComputeDomainContent(t, tc, pcsName, 0, createdPCS.GetUID())
}

// testResourceClaimInjection is a comprehensive test that verifies resourceClaim injection
// and annotation propagation across multiple clique types and scaling groups.
func testResourceClaimInjection(t *testing.T, tc *testctx.TestContext) {
	pcsName := "inj-test"

	pcs := buildComprehensivePCS(pcsName, 1, map[string]string{
		mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled,
	})
	err := tc.Client.Create(tc.Ctx, pcs)
	require.NoError(t, err, "Failed to create PCS")
	defer deletePCS(tc, pcsName)

	// --- Verify standalone cliques ---

	// 1. gpu1: should have claim, GPU container refs it, non-GPU doesn't
	t.Run("standalone GPU mixed clique", func(t *testing.T) {
		pclqName := fmt.Sprintf("%s-0-gpu1", pcsName)
		pclq, err := waitForPCLQ(tc, pclqName)
		require.NoError(t, err, "Failed to wait for PCLQ")

		// Should have resourceClaim
		requirePodSpecMNNVLClaim(t, &pclq.Spec.PodSpec, pcsName, 0)

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

		requirePodSpecMNNVLClaim(t, &pclq.Spec.PodSpec, pcsName, 0)

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

		assert.Equal(t, mnnvl.AnnotationAutoMNNVLEnabled, pcsg.GetAnnotations()[mnnvl.AnnotationAutoMNNVL],
			"sg1 should have auto-mnnvl annotation propagated")
	})

	t.Run("sg2 has annotation", func(t *testing.T) {
		pcsgName := fmt.Sprintf("%s-0-sg2", pcsName)
		pcsg, err := waitForPCSG(tc, pcsgName)
		require.NoError(t, err, "Failed to wait for sg2")

		// Current behavior: all PCSGs get annotation from PCS, regardless of clique GPU content
		assert.Equal(t, mnnvl.AnnotationAutoMNNVLEnabled, pcsg.GetAnnotations()[mnnvl.AnnotationAutoMNNVL],
			"sg2 should have auto-mnnvl annotation propagated (current behavior)")
	})
}

// testScaleOutAndIn verifies that scaling out creates new ComputeDomains with correct content,
// and scaling in deletes excess ComputeDomains.
func testScaleOutAndIn(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-scale-cd"

	pcs := buildGPUPCSWithMNNVL(pcsName, 1)
	err := tc.Client.Create(tc.Ctx, pcs)
	require.NoError(t, err, "Failed to create PCS")
	defer deletePCS(tc, pcsName)

	// Re-fetch to get server-assigned UID
	var createdPCS grovecorev1alpha1.PodCliqueSet
	err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
	require.NoError(t, err, "Failed to get created PCS")

	// Wait for initial ComputeDomain
	err = waitForComputeDomainCount(tc, pcsName, 1)
	require.NoError(t, err, "Failed to wait for initial ComputeDomain")

	// Verify initial CD content
	verifyComputeDomainContent(t, tc, pcsName, 0, createdPCS.GetUID())

	// --- Scale Out: 1 -> 3 replicas ---
	err = scalePCS(tc, pcsName, 3)
	require.NoError(t, err, "Failed to scale out PCS")

	err = waitForComputeDomainCount(tc, pcsName, 3)
	require.NoError(t, err, "Failed to wait for scaled-out ComputeDomains")

	// Verify all 3 CDs exist with correct content
	for i := 0; i < 3; i++ {
		verifyComputeDomainContent(t, tc, pcsName, i, createdPCS.GetUID())
	}

	// --- Scale In: 3 -> 1 replica ---
	err = scalePCS(tc, pcsName, 1)
	require.NoError(t, err, "Failed to scale in PCS")

	err = waitForComputeDomainCount(tc, pcsName, 1)
	require.NoError(t, err, "Failed to wait for scaled-in ComputeDomains")

	// Verify only replica-0 exists with correct content
	verifyComputeDomainContent(t, tc, pcsName, 0, createdPCS.GetUID())

	// Verify replicas 1 and 2 are deleted
	err = getComputeDomain(tc, fmt.Sprintf("%s-1", pcsName))
	assert.Error(t, err, "ComputeDomain 1 should be deleted after scale-in")

	err = getComputeDomain(tc, fmt.Sprintf("%s-2", pcsName))
	assert.Error(t, err, "ComputeDomain 2 should be deleted after scale-in")
}

// testPCSDeletionCascadesToCD verifies that deleting PCS also deletes ComputeDomains.
func testPCSDeletionCascadesToCD(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-pcs-deletion-cascade"

	pcs := buildGPUPCSWithMNNVL(pcsName, 2)
	err := tc.Client.Create(tc.Ctx, pcs)
	require.NoError(t, err, "Failed to create PCS")

	// Wait for ComputeDomains
	err = waitForComputeDomainCount(tc, pcsName, 2)
	require.NoError(t, err, "Failed to wait for ComputeDomains")

	// Delete the PCS
	deletePCS(tc, pcsName)

	// Wait for ComputeDomains to be deleted
	err = waitForComputeDomainCount(tc, pcsName, 0)
	assert.NoError(t, err, "ComputeDomains should be deleted when PCS is deleted")
}

// testExplicitDisabledAnnotationHonored verifies that auto-mnnvl: disabled prevents injection.
func testExplicitDisabledAnnotationHonored(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-explicit-disabled"

	// Create a PCS with GPU requirement but explicit disabled annotation
	pcs := buildGPUPCS(pcsName, 1)
	annotations := pcs.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[mnnvl.AnnotationAutoMNNVL] = mnnvl.AnnotationAutoMNNVLDisabled
	pcs.SetAnnotations(annotations)

	err := tc.Client.Create(tc.Ctx, pcs)
	require.NoError(t, err, "Failed to create PCS")
	defer deletePCS(tc, pcsName)

	// Wait for PCLQ to exist — this proves the reconciler has processed the PCS,
	// so any ComputeDomains would have been created by now if the annotation
	// were honoured incorrectly.
	pclqName := fmt.Sprintf("%s-0-gpu-worker", pcsName)
	pclq, err := waitForPCLQ(tc, pclqName)
	require.NoError(t, err, "Failed to wait for PCLQ")

	// Verify no ComputeDomain was created
	cdName := fmt.Sprintf("%s-0", pcsName)
	err = getComputeDomain(tc, cdName)
	assert.Error(t, err, "No ComputeDomain should be created when annotation is 'disabled'")

	// Verify no MNNVL claims are injected into the clique or containers.
	for _, claim := range pclq.Spec.PodSpec.ResourceClaims {
		assert.NotEqual(t, mnnvl.MNNVLClaimName, claim.Name, "PCLQ should not have MNNVL resourceClaim")
	}
	for i := range pclq.Spec.PodSpec.Containers {
		requireNoContainerMNNVLClaim(t, &pclq.Spec.PodSpec.Containers[i])
	}
}

// testInvalidAnnotationRejected verifies that invalid annotation values are rejected.
func testInvalidAnnotationRejected(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-invalid-annotation"

	// Create a PCS with invalid annotation value
	pcs := buildGPUPCS(pcsName, 1)
	annotations := pcs.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[mnnvl.AnnotationAutoMNNVL] = "invalid-value"
	pcs.SetAnnotations(annotations)

	err := tc.Client.Create(tc.Ctx, pcs)
	assert.Error(t, err, "PCS with invalid annotation value should be rejected")
}

// testAnnotationImmutability verifies that the auto-mnnvl annotation cannot be changed after creation.
func testAnnotationImmutability(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-annotation-immutable"

	pcs := buildGPUPCSWithMNNVL(pcsName, 1)
	err := tc.Client.Create(tc.Ctx, pcs)
	require.NoError(t, err, "Failed to create PCS")
	defer deletePCS(tc, pcsName)

	// Re-fetch to get server-applied annotations
	var createdPCS grovecorev1alpha1.PodCliqueSet
	err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pcsName}, &createdPCS)
	require.NoError(t, err, "Failed to get created PCS")

	// Verify it has the auto-mnnvl annotation
	annotations := createdPCS.GetAnnotations()
	require.Equal(t, mnnvl.AnnotationAutoMNNVLEnabled, annotations[mnnvl.AnnotationAutoMNNVL],
		"PCS should have auto-mnnvl annotation set to 'enabled'")

	// Try to change annotation from "enabled" to "disabled"
	createdPCS.Annotations[mnnvl.AnnotationAutoMNNVL] = mnnvl.AnnotationAutoMNNVLDisabled
	err = tc.Client.Update(tc.Ctx, &createdPCS)
	assert.Error(t, err, "Changing auto-mnnvl annotation should be rejected")
	assert.Contains(t, err.Error(), "immutable", "Error should mention immutability")
}

// requirePodSpecMNNVLClaim asserts the PodSpec includes the MNNVL claim
// and the expected ResourceClaimTemplate reference for the PCS replica.
func requirePodSpecMNNVLClaim(t *testing.T, podSpec *corev1.PodSpec, pcsName string, replicaIndex int) {
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

	expectedRCTName := mnnvl.GenerateRCTName(apicommon.ResourceNameReplica{Name: pcsName, Replica: replicaIndex}, "")
	assert.Equal(t, expectedRCTName, *mnnvlClaim.ResourceClaimTemplateName)

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

// getComputeDomain attempts to get a ComputeDomain by name. Returns error if not found.
func getComputeDomain(tc *testctx.TestContext, name string) error {
	cd := &unstructured.Unstructured{}
	cd.SetGroupVersionKind(gvk.ComputeDomain)
	return tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: name}, cd)
}

// verifyComputeDomainContent verifies that a ComputeDomain exists with correct metadata and spec.
func verifyComputeDomainContent(t *testing.T, tc *testctx.TestContext, pcsName string, replicaIndex int, pcsUID types.UID) {
	t.Helper()

	cdName := fmt.Sprintf("%s-%d", pcsName, replicaIndex)
	cd := &unstructured.Unstructured{}
	cd.SetGroupVersionKind(gvk.ComputeDomain)
	err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: cdName}, cd)
	require.NoError(t, err, "ComputeDomain %s should exist", cdName)

	// Verify finalizer
	finalizers := cd.GetFinalizers()
	assert.Contains(t, finalizers, mnnvl.FinalizerComputeDomain,
		"ComputeDomain %s should have Grove finalizer", cdName)

	// Verify owner reference
	ownerRefs := cd.GetOwnerReferences()
	require.Len(t, ownerRefs, 1, "ComputeDomain %s should have exactly one owner reference", cdName)
	assert.Equal(t, "PodCliqueSet", ownerRefs[0].Kind)
	assert.Equal(t, pcsName, ownerRefs[0].Name)
	assert.Equal(t, pcsUID, ownerRefs[0].UID)

	// Verify labels
	labels := cd.GetLabels()
	assert.Equal(t, pcsName, labels[apicommon.LabelPartOfKey])
	assert.Equal(t, fmt.Sprintf("%d", replicaIndex), labels[apicommon.LabelPodCliqueSetReplicaIndex])

	// Verify numNodes is 0 (elastic mode)
	numNodes, found, err := unstructured.NestedInt64(cd.Object, "spec", "numNodes")
	require.NoError(t, err)
	assert.True(t, found, "numNodes should be set for ComputeDomain %s", cdName)
	assert.Equal(t, int64(0), numNodes, "numNodes should be 0 for elastic mode")

	// Verify RCT reference in spec
	rctName, found, err := unstructured.NestedString(cd.Object, "spec", "channel", "resourceClaimTemplate", "name")
	require.NoError(t, err)
	assert.True(t, found, "resourceClaimTemplate.name should be set for ComputeDomain %s", cdName)
	expectedRCTName := fmt.Sprintf("%s-%d", pcsName, replicaIndex)
	assert.Equal(t, expectedRCTName, rctName, "ComputeDomain %s should reference correct RCT", cdName)
}
