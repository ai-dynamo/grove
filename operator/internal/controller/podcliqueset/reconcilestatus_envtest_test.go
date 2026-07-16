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

package podcliqueset

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestReconcileConvergesFromLivePCS replays the RU-11 failure against a real API server:
// the persisted PCS status is regressed while the child cache has already converged.
func TestReconcileConvergesFromLivePCS(t *testing.T) {
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
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, "default", uuid.NewUUID()).
		WithReplicas(3).
		WithStandaloneClique("worker").
		Build()
	pcs.UID = ""
	pcs.Finalizers = []string{apicommonconstants.FinalizerPodCliqueSet}
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		cliqueTemplate.Spec.PodSpec = testutils.NewPodWithBuilderWithDefaultSpec("test-name", "default").Build().Spec
	}
	require.NoError(t, liveClient.Create(ctx, pcs))

	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	livePCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, liveClient.Get(ctx, pcsObjectKey, livePCS))
	generationHash := computeGenerationHash(livePCS)
	updateStartedAt := metav1.NewTime(time.Unix(100, 0))
	updateEndedAt := metav1.NewTime(time.Unix(200, 0))
	replicaUpdateStartedAt := metav1.NewTime(time.Unix(150, 0))
	lastErrorObservedAt := metav1.NewTime(time.Unix(175, 0))
	livePCS.Status = grovecorev1alpha1.PodCliqueSetStatus{
		ObservedGeneration:    ptr.To(livePCS.Generation),
		Replicas:              3,
		AvailableReplicas:     3,
		UpdatedReplicas:       2,
		CurrentGenerationHash: &generationHash,
		LastErrors: []grovecorev1alpha1.LastError{
			{
				Code:        "test-error",
				Description: "must survive status convergence",
				ObservedAt:  lastErrorObservedAt,
			},
		},
		UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
			UpdateStartedAt:        updateStartedAt,
			UpdateEndedAt:          &updateEndedAt,
			UpdatedPodCliquesCount: 2,
			TotalPodCliquesCount:   3,
			CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
				{
					ReplicaIndex:    2,
					UpdateStartedAt: replicaUpdateStartedAt,
				},
			},
		},
	}
	require.NoError(t, mutateSelector(livePCS))
	require.NoError(t, liveClient.Status().Update(ctx, livePCS))

	frozenPCS := livePCS.DeepCopy()
	frozenPCS.Status.UpdatedReplicas = 3
	frozenPCS.Status.UpdateProgress.UpdatedPodCliquesCount = 3
	frozenObjects := []client.Object{frozenPCS}
	for replicaIndex := range int32(3) {
		pclq := testutils.NewPodCliqueBuilder(testPCSName, frozenPCS.UID, "worker", "default", replicaIndex).Build()
		frozenObjects = append(frozenObjects, markStandalonePCLQConverged(t, frozenPCS, pclq, generationHash))
	}
	frozenCache := testutils.SetupFakeClient(frozenObjects...)
	apiReader := &countingReader{Reader: liveClient}
	r := &Reconciler{
		client:           &staleCacheClient{Client: liveClient, cache: frozenCache},
		apiReader:        apiReader,
		operatorRegistry: component.NewOperatorRegistry[grovecorev1alpha1.PodCliqueSet](),
	}

	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: pcsObjectKey})
	require.NoError(t, err)
	assert.Equal(t, 1, apiReader.getCalls, "each reconcile should perform one live PCS GET")

	repairedPCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, liveClient.Get(ctx, pcsObjectKey, repairedPCS))
	assert.Equal(t, int32(3), repairedPCS.Status.UpdatedReplicas)
	require.NotNil(t, repairedPCS.Status.UpdateProgress)
	assert.Equal(t, int32(3), repairedPCS.Status.UpdateProgress.UpdatedPodCliquesCount)
	assert.Equal(t, updateEndedAt, *repairedPCS.Status.UpdateProgress.UpdateEndedAt)
	assert.Equal(t, livePCS.Status.UpdateProgress.CurrentlyUpdating, repairedPCS.Status.UpdateProgress.CurrentlyUpdating)
	assert.Equal(t, livePCS.Status.LastErrors, repairedPCS.Status.LastErrors)

	repairedResourceVersion := repairedPCS.ResourceVersion
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: pcsObjectKey})
	require.NoError(t, err)
	assert.Equal(t, 2, apiReader.getCalls, "the second reconcile should perform exactly one more live PCS GET")

	steadyPCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, liveClient.Get(ctx, pcsObjectKey, steadyPCS))
	assert.Equal(t, repairedResourceVersion, steadyPCS.ResourceVersion,
		"steady-state reconcile must not write an unchanged status")
}

// staleCacheClient keeps child reads on a frozen cache while writes go to the API server.
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

type countingReader struct {
	client.Reader
	getCalls int
}

func (r *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.getCalls++
	return r.Reader.Get(ctx, key, obj, opts...)
}
