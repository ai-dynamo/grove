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

package podcliqueset

import (
	"context"
	"maps"
	"sync"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestControllerRevisionInitializesNewPodCliqueSet(t *testing.T) {
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("new", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}
	expectedGenerationHash := computeGenerationHash(pcs)
	expectedCliqueHashes := candidateCliqueHashes(pcs)

	result := r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.False(t, reconcileRequeues(result))
	require.NotNil(t, pcs.Status.CurrentGenerationHash)
	assert.Equal(t, expectedGenerationHash, *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, expectedCliqueHashes, selectedCliqueHashes(ctx, t, fakeClient, pcs))
	assert.Equal(t, 1, controllerRevisionCount(ctx, t, fakeClient))
	assert.Nil(t, pcs.Status.UpdateProgress)
}

func TestControllerRevisionUpgrade(t *testing.T) {
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("legacy", "default", uuid.NewUUID()).
		WithReplicas(1).
		WithPodCliqueParameters("worker", 1, nil).
		WithPodCliqueParameters("sidecar", 1, nil).
		Build()
	pcs.Generation = 7
	pcs.Status.ObservedGeneration = ptr.To(int64(7))
	pcs.Status.CurrentGenerationHash = ptr.To("alpha8-generation")
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers = []corev1.Container{{Name: "worker", Image: "worker:v1"}}
	pcs.Spec.Template.Cliques[1].Spec.PodSpec.Containers = []corev1.Container{{Name: "sidecar", Image: "sidecar:v1"}}

	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: metav1.ObjectMeta{
		Name:            "legacy-0-group",
		Namespace:       pcs.Namespace,
		UID:             uuid.NewUUID(),
		Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet"))},
	}}
	worker := legacyPCLQ(pcs, "legacy-0-group-0-worker", "alpha8-worker", pcsg)
	sidecar := legacyPCLQ(pcs, "legacy-0-sidecar", "alpha8-sidecar", pcs)
	fakeClient := testutils.SetupFakeClient(pcs, pcsg, worker, sidecar)
	originalWorker := getPCLQ(ctx, t, fakeClient, worker)
	originalSidecar := getPCLQ(ctx, t, fakeClient, sidecar)
	r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}

	result := r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.True(t, reconcileRequeues(result))
	require.NotNil(t, pcs.Status.CurrentGenerationHash)
	assert.Equal(t, "alpha8-generation", *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, map[string]string{
		"sidecar": "alpha8-sidecar",
		"worker":  "alpha8-worker",
	}, selectedCliqueHashes(ctx, t, fakeClient, pcs))
	assert.Equal(t, 1, controllerRevisionCount(ctx, t, fakeClient))
	assert.Nil(t, pcs.Status.UpdateProgress)
	assert.True(t, childrenEqual(ctx, t, fakeClient, originalWorker, originalSidecar))

	pcs = getPCS(ctx, t, fakeClient, pcs)
	result = r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.False(t, reconcileRequeues(result))
	assert.Equal(t, "alpha8-generation", *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, map[string]string{
		"sidecar": "alpha8-sidecar",
		"worker":  "alpha8-worker",
	}, selectedCliqueHashes(ctx, t, fakeClient, pcs))
	assert.Equal(t, 1, controllerRevisionCount(ctx, t, fakeClient))
	assert.Nil(t, pcs.Status.UpdateProgress)

	pcs.Generation++
	pcs.Spec.Replicas = 2
	result = r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.False(t, reconcileRequeues(result))
	assert.Equal(t, "alpha8-generation", *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, map[string]string{
		"sidecar": "alpha8-sidecar",
		"worker":  "alpha8-worker",
	}, selectedCliqueHashes(ctx, t, fakeClient, pcs))
	assert.Equal(t, 1, controllerRevisionCount(ctx, t, fakeClient))
	assert.Nil(t, pcs.Status.UpdateProgress)

	pcs.Generation++
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "worker:v2"
	expectedGenerationHash := computeGenerationHash(pcs)
	expectedWorkerHash := candidateCliqueHashes(pcs)["worker"]
	result = r.processRevision(ctx, logr.Discard(), pcs)
	require.False(t, result.HasErrors())
	assert.False(t, reconcileRequeues(result))
	assert.Equal(t, expectedGenerationHash, *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, map[string]string{
		"sidecar": "alpha8-sidecar",
		"worker":  expectedWorkerHash,
	}, selectedCliqueHashes(ctx, t, fakeClient, pcs))
	assert.Equal(t, 2, controllerRevisionCount(ctx, t, fakeClient))
	assert.NotNil(t, pcs.Status.UpdateProgress)
}

func TestControllerRevisionUpgradeRejectsActiveLegacyUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("legacy", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	pcs.Generation = 3
	pcs.Status.ObservedGeneration = ptr.To(int64(3))
	pcs.Status.CurrentGenerationHash = ptr.To("alpha8-generation")
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient, pcsRevisionExpectations: sync.Map{}}

	result := r.processRevision(context.Background(), logr.Discard(), pcs)
	require.True(t, result.HasErrors())
	assert.False(t, reconcileRequeues(result))
	assert.Equal(t, "alpha8-generation", *pcs.Status.CurrentGenerationHash)
	assert.Equal(t, 0, controllerRevisionCount(context.Background(), t, fakeClient))
	assert.NotNil(t, pcs.Status.UpdateProgress)
}

func legacyPCLQ(pcs *grovecorev1alpha1.PodCliqueSet, name, hash string, owner client.Object) *grovecorev1alpha1.PodClique {
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)
	labels[apicommon.LabelPodTemplateHash] = hash
	labels[apicommon.LabelPodCliqueSetReplicaIndex] = "0"
	if pcsg, ok := owner.(*grovecorev1alpha1.PodCliqueScalingGroup); ok {
		labels[apicommon.LabelPodCliqueScalingGroup] = pcsg.Name
		labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex] = "0"
	}
	gvk := grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueSet")
	if _, ok := owner.(*grovecorev1alpha1.PodCliqueScalingGroup); ok {
		gvk = grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueScalingGroup")
	}
	return &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Name:            name,
		Namespace:       pcs.Namespace,
		UID:             uuid.NewUUID(),
		Labels:          labels,
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(owner, gvk)},
	}}
}

func reconcileRequeues(result ctrlcommon.ReconcileStepResult) bool {
	ctrlResult, _ := result.Result()
	return ctrlResult.RequeueAfter > 0
}

func controllerRevisionCount(ctx context.Context, t *testing.T, cl client.Client) int {
	t.Helper()
	list := &appsv1.ControllerRevisionList{}
	if err := cl.List(ctx, list); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func selectedCliqueHashes(ctx context.Context, t *testing.T, cl client.Client, pcs *grovecorev1alpha1.PodCliqueSet) map[string]string {
	t.Helper()
	revision, err := componentutils.GetPodCliqueSetRevisionData(ctx, cl, pcs)
	if err != nil {
		t.Fatal(err)
	}
	return maps.Clone(revision.CliqueHashes)
}

func childrenEqual(ctx context.Context, t *testing.T, cl client.Client, expected ...*grovecorev1alpha1.PodClique) bool {
	t.Helper()
	for _, want := range expected {
		got := &grovecorev1alpha1.PodClique{}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(want), got); err != nil || !equality.Semantic.DeepEqual(want, got) {
			return false
		}
	}
	return true
}

func getPCS(ctx context.Context, t *testing.T, cl client.Client, pcs *grovecorev1alpha1.PodCliqueSet) *grovecorev1alpha1.PodCliqueSet {
	t.Helper()
	updated := &grovecorev1alpha1.PodCliqueSet{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(pcs), updated); err != nil {
		t.Fatal(err)
	}
	return updated
}

func getPCLQ(ctx context.Context, t *testing.T, cl client.Client, pclq *grovecorev1alpha1.PodClique) *grovecorev1alpha1.PodClique {
	t.Helper()
	updated := &grovecorev1alpha1.PodClique{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(pclq), updated); err != nil {
		t.Fatal(err)
	}
	return updated
}
