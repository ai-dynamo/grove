// /*
// Copyright 2025 The Grove Authors.
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

package podcliqueset

import (
	"context"
	"path/filepath"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/resourceversion"
	"k8s.io/apimachinery/pkg/util/uuid"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestReconcileStatusConvergesAgainstRealAPIServer replays the stale-informer scenario
// behind the status no-op optimization against a real API server (envtest):
//
//  1. the reconciler persisted a status write W and recorded an expectation, but the
//     informer cache is frozen at an older, self-consistent state O (recomputing status
//     on O's children reproduces O's status byte-for-byte — a cache-level no-op);
//  2. an out-of-band writer regresses the status on the API Server;
//  3. the reconciler runs from the frozen cache. Without the expectation the no-op check
//     would skip the write forever; the pending expectation must force an API-reader
//     verification that repairs the regressed status on the API Server.
func TestReconcileStatusConvergesAgainstRealAPIServer(t *testing.T) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "api", "core", "v1alpha1", "crds"),
		},
	}
	envCfg, err := testEnv.Start()
	if err != nil {
		t.Skipf("Skipping test: kubebuilder test environment not available: %v", err)
		return
	}
	defer func() {
		require.NoError(t, testEnv.Stop())
	}()

	testScheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(testScheme))
	require.NoError(t, grovecorev1alpha1.AddToScheme(testScheme))
	liveClient, err := client.New(envCfg, client.Options{Scheme: testScheme})
	require.NoError(t, err)

	ctx := context.Background()
	pcsHash := "gen-hash-current"
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, "default", uuid.NewUUID()).
		WithReplicas(1).
		WithStandaloneClique("worker").
		Build()
	pcs.UID = "" // the API server assigns the UID
	// The builder leaves the pod spec empty; a real API server enforces CRD validation.
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		cliqueTemplate.Spec.PodSpec = testutils.NewPodWithBuilderWithDefaultSpec("test-name", "default").Build().Spec
	}
	require.NoError(t, liveClient.Create(ctx, pcs))
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	livePCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, liveClient.Get(ctx, pcsObjectKey, livePCS))
	livePCS.Status.CurrentGenerationHash = &pcsHash
	require.NoError(t, liveClient.Status().Update(ctx, livePCS))

	// Build the frozen informer snapshot O: the PCS plus a converged child, with the
	// status derived from exactly those children so a reconcile over the snapshot is a
	// cache-level no-op.
	frozenPCS := livePCS.DeepCopy()
	frozenChild := markStandalonePCLQConverged(t, frozenPCS,
		testutils.NewPodCliqueBuilder(testPCSName, frozenPCS.UID, "worker", "default", 0).Build(), pcsHash)
	frozenChild.Namespace = "default"
	derivationClient := testutils.SetupFakeClient(frozenChild)
	cacheReconciler := &Reconciler{client: derivationClient}
	frozenResult := cacheReconciler.mutateStatus(ctx, logr.Discard(), frozenPCS)
	_, err = frozenResult.Result()
	require.NoError(t, err)
	require.Equal(t, int32(1), frozenPCS.Status.AvailableReplicas,
		"precondition: the frozen snapshot sees the converged child")
	goodStatus := frozenPCS.Status.DeepCopy()

	// Keep the frozen snapshot self-consistent but older than W. Build the fake cache
	// only after deriving the status so it preserves the real API Server resourceVersion
	// instead of assigning a resourceVersion from the fake client's independent sequence.
	frozenPCS.Status.UpdatedReplicas = 0
	frozenChild.Status.UpdatedReplicas = 0
	frozenCache := testutils.SetupFakeClient(frozenPCS.DeepCopy(), frozenChild)
	cacheReconciler = &Reconciler{client: frozenCache}

	// Step 1 (simulated): the reconciler last wrote W to the API Server. Persist W and
	// record the expectation, exactly as updateStatus does.
	r := &Reconciler{
		client:    &staleCacheClient{Client: liveClient, cache: frozenCache},
		apiReader: liveClient,
	}
	goodPCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, liveClient.Get(ctx, pcsObjectKey, goodPCS))
	goodPCS.Status = *goodStatus
	writeResult := r.updateStatus(ctx, goodPCS)
	_, err = writeResult.Result()
	require.NoError(t, err)
	require.NotNil(t, goodPCS.Status.Selector, "precondition: the good status carries a selector")
	_, ok := r.pcsStatusUpdateExpectations.Load(pcsObjectKey)
	require.True(t, ok, "the status write must record an expectation")
	comparison, err := resourceversion.CompareResourceVersion(frozenPCS.ResourceVersion, goodPCS.ResourceVersion)
	require.NoError(t, err)
	require.Negative(t, comparison, "precondition: the frozen cache must predate the expected write")

	// Step 2: an out-of-band writer regresses the status on the API Server before the
	// informer observes W.
	regressedPCS := goodPCS.DeepCopy()
	regressedPCS.Status.AvailableReplicas = 0
	regressedPCS.Status.Selector = nil
	require.NoError(t, liveClient.Status().Update(ctx, regressedPCS))

	// Verify the snapshot really is a cache-level no-op before relying on it.
	verifyPCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, frozenCache.Get(ctx, pcsObjectKey, verifyPCS))
	recomputed := verifyPCS.DeepCopy()
	verifyResult := cacheReconciler.mutateStatus(ctx, logr.Discard(), recomputed)
	_, err = verifyResult.Result()
	require.NoError(t, err)
	require.Equal(t, verifyPCS.Status, recomputed.Status,
		"precondition: recomputing on the frozen snapshot must be a no-op")

	// Step 3: reconcile from the frozen cache. The no-op optimization alone would skip
	// the write forever; the pending expectation forces API-reader verification, which
	// recomputes on the fresh object (children still from the frozen cache) and repairs
	// the regression.
	result := r.reconcileStatus(ctx, logr.Discard(), verifyPCS.DeepCopy())
	_, err = result.Result()
	require.NoError(t, err)

	repairedPCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, liveClient.Get(ctx, pcsObjectKey, repairedPCS))
	assert.Equal(t, int32(1), repairedPCS.Status.AvailableReplicas,
		"the regressed availability must be repaired on the API Server")
	require.NotNil(t, repairedPCS.Status.Selector, "the regressed selector must be restored")
	assert.Equal(t, *goodPCS.Status.Selector, *repairedPCS.Status.Selector)

	// Step 4: once the informer cache observes the repaired status, the fast path
	// resumes and no further writes happen.
	observedPCS := repairedPCS.DeepCopy()
	result = r.reconcileStatus(ctx, logr.Discard(), observedPCS)
	_, err = result.Result()
	require.NoError(t, err)
	finalPCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, liveClient.Get(ctx, pcsObjectKey, finalPCS))
	assert.Equal(t, repairedPCS.ResourceVersion, finalPCS.ResourceVersion,
		"a converged status must not be written again")
}

// staleCacheClient simulates the manager's cache-backed client during an informer stall:
// reads (Get/List) are served from a frozen fake snapshot, while writes go straight to
// the real API server, matching the production client's read/write split.
type staleCacheClient struct {
	client.Client
	cache client.Reader
}

func (c *staleCacheClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.cache.Get(ctx, key, obj, opts...)
}

func (c *staleCacheClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.cache.List(ctx, list, opts...)
}
