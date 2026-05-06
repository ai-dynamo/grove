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
	"fmt"
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/grove/gvk"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// testNoMNNVLArtifactsWhenDisabled verifies that no ComputeDomains or
// resourceClaims are produced for a GPU-capable PCS when the feature is
// disabled. It is shared across test suites because the expected behavior
// is identical regardless of why the feature is inactive.
func testNoMNNVLArtifactsWhenDisabled(t *testing.T, tc *testctx.TestContext) {
	pcsName := "test-no-artifacts"

	err := applyMNNVLYAML(tc, "mnnvl-comprehensive-bare.yaml", pcsName)
	require.NoError(t, err, "Failed to apply YAML")
	defer deletePCS(tc, pcsName)

	// Wait for PCSGs to appear — this proves the reconciler has processed the
	// PCS, so any ComputeDomains would have been created by now if the feature
	// were enabled. This replaces a fixed sleep with a concrete readiness signal.
	pcsgNames := []string{
		fmt.Sprintf("%s-0-sg1", pcsName),
		fmt.Sprintf("%s-0-sg2", pcsName),
	}
	for _, pcsgName := range pcsgNames {
		_, waitErr := waitForPCSG(tc, pcsgName)
		require.NoError(t, waitErr, "Failed to wait for PCSG %s", pcsgName)
	}

	// Verify no ComputeDomain exists.
	// If the CRD itself is not installed (unsupported scenario), the List call returns
	// a "no matches for kind" error — that also means zero ComputeDomains, which is what we want.
	cdList := &unstructured.UnstructuredList{}
	cdList.SetGroupVersionKind(gvk.ComputeDomain.GroupVersion().WithKind(gvk.ComputeDomain.Kind + "List"))
	err = tc.Client.List(tc.Ctx, cdList, client.InNamespace(tc.Namespace))
	if k8serrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		// CRD not installed → no ComputeDomains can exist, which is the expected state.
	} else {
		require.NoError(t, err, "Failed to list ComputeDomains")
		assert.Empty(t, cdList.Items, "Expected 0 ComputeDomains when feature is disabled, got %d", len(cdList.Items))
	}

	// Verify no resourceClaims are injected into any clique's PodSpec
	pclqNames := []string{
		fmt.Sprintf("%s-0-gpu1", pcsName),
		fmt.Sprintf("%s-0-cpu1", pcsName),
		fmt.Sprintf("%s-0-sg1-0-gpu2", pcsName),
		fmt.Sprintf("%s-0-sg1-0-cpu2", pcsName),
		fmt.Sprintf("%s-0-sg2-0-cpu3", pcsName),
	}
	// We specifically don't want an injected claim named MNNVLClaimName or container claim refs,
	// but it's simpler to assert there are no claims at all, which covers the intent.
	for _, pclqName := range pclqNames {
		pclq, waitErr := waitForPCLQ(tc, pclqName)
		require.NoError(t, waitErr, "Failed to wait for PCLQ %s", pclqName)
		assert.Empty(t, pclq.Spec.PodSpec.ResourceClaims, "PCLQ %s should not have resourceClaims", pclqName)
		for _, container := range pclq.Spec.PodSpec.Containers {
			assert.Empty(t, container.Resources.Claims, "PCLQ %s container %s should not have claim reference", pclqName, container.Name)
		}
	}
}
