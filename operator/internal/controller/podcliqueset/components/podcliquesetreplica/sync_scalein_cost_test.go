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

package podcliquesetreplica

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/scalein"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestSyncScaleInCostWiring drives a scale-in through Sync on an operator built by New, with real PodCliques
// and marked pods in a fake client, so the whole chain is exercised: the configured cost source, the pod list,
// the per-replica minimum, the selection and the event. The unit tests of each link cannot see a link left out.
func TestSyncScaleInCostWiring(t *testing.T) {
	const (
		namespace = "default"
		pcsName   = "test-pcs"
	)
	pcsUID := types.UID("test-pcs-uid")
	newPCS := func(replicas int32) *grovecorev1beta1.PodCliqueSet {
		pcs := testutils.NewPodCliqueSetBuilder(pcsName, namespace, pcsUID).
			WithReplicas(replicas).
			WithStandaloneClique("worker").
			Build()
		observedGeneration := pcs.Generation
		pcs.Status.ObservedGeneration = &observedGeneration
		if pcs.Spec.Template.TerminationDelay == nil {
			pcs.Spec.Template.TerminationDelay = &metav1.Duration{}
		}
		return pcs
	}
	newPod := func(name string, replicaIndex int, cost string) *corev1.Pod {
		labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)
		labels[apicommon.LabelPodCliqueSetReplicaIndex] = string(rune('0' + replicaIndex))
		pod := testutils.NewPodBuilder(name, namespace).WithLabels(labels).Build()
		if cost != "" {
			pod.Annotations = map[string]string{corev1.PodDeletionCost: cost}
		}
		return pod
	}
	// Three available replicas, two pods each, one pod of replica 1 marked. Without a cost the highest
	// index, replica 2, leaves. With the cost, replica 1 leaves.
	objects := func(pcs *grovecorev1beta1.PodCliqueSet) []client.Object {
		objects := []client.Object{
			newPod("r0-a", 0, ""), newPod("r0-b", 0, ""),
			newPod("r1-a", 1, ""), newPod("r1-b", 1, "-1000000"),
			newPod("r2-a", 2, ""), newPod("r2-b", 2, ""),
		}
		for replicaIndex := range 3 {
			pclq := pclqWithAvailability(pcs, replicaIndex, true)
			objects = append(objects, &pclq)
		}
		return objects
	}
	newClient := func(t *testing.T, pcs *grovecorev1beta1.PodCliqueSet, objects ...client.Object) client.Client {
		t.Helper()
		scheme := runtime.NewScheme()
		require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
		require.NoError(t, corev1.AddToScheme(scheme))
		return fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(append(objects, pcs)...).
			WithStatusSubresource(pcs).
			Build()
	}
	drainEvents := func(recorder *record.FakeRecorder) []string {
		var events []string
		for {
			select {
			case event := <-recorder.Events:
				events = append(events, event)
			default:
				return events
			}
		}
	}
	requireSyncOK := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			require.True(t, groveerr.IsContinueReconcileAndRequeue(err), "unexpected error: %v", err)
		}
	}

	t.Run("the configured source steers the selection and the event carries the cost", func(t *testing.T) {
		source, err := scalein.NewCostSource(configv1beta1.ScaleInCostSourcePodDeletionCost)
		require.NoError(t, err)
		pcs := newPCS(2)
		recorder := record.NewFakeRecorder(10)
		operator := New(newClient(t, pcs, objects(pcs)...), recorder, &pcssync.ScalingProgressExpectations{}, source)
		state := &pcssync.State{}

		_, err = operator.Sync(context.Background(), logr.Discard(), pcs, state)

		requireSyncOK(t, err)
		assert.Equal(t, []int{1}, state.ScaleInReplicaIndices)
		assert.Equal(t, []int{0, 2}, state.ExpectedReplicaIndices)
		assert.Contains(t, drainEvents(recorder),
			"Normal PodCliqueSetReplicaIndicesSelectedForScaleIn Selected PCS replica indices [1] for scale-in (scale-in cost: 1=-1000000)")
	})

	t.Run("the none source ignores the mark and selects the highest index", func(t *testing.T) {
		source, err := scalein.NewCostSource(configv1beta1.ScaleInCostSourceNone)
		require.NoError(t, err)
		pcs := newPCS(2)
		recorder := record.NewFakeRecorder(10)
		operator := New(newClient(t, pcs, objects(pcs)...), recorder, &pcssync.ScalingProgressExpectations{}, source)
		state := &pcssync.State{}

		_, err = operator.Sync(context.Background(), logr.Discard(), pcs, state)

		requireSyncOK(t, err)
		assert.Equal(t, []int{2}, state.ScaleInReplicaIndices)
		assert.Contains(t, drainEvents(recorder),
			"Normal PodCliqueSetReplicaIndicesSelectedForScaleIn Selected PCS replica indices [2] for scale-in")
	})

	t.Run("a reconcile without a scale-in lists no pods", func(t *testing.T) {
		source, err := scalein.NewCostSource(configv1beta1.ScaleInCostSourcePodDeletionCost)
		require.NoError(t, err)
		pcs := newPCS(3)
		// Every pod list fails, so a sync that lists pods fails with it.
		cl := &failingListClient{Client: newClient(t, pcs, objects(pcs)...), failedObject: &corev1.PodList{}}
		operator := New(cl, record.NewFakeRecorder(10), &pcssync.ScalingProgressExpectations{}, source)
		state := &pcssync.State{}

		_, err = operator.Sync(context.Background(), logr.Discard(), pcs, state)

		requireSyncOK(t, err)
		assert.Empty(t, state.ScaleInReplicaIndices)
		assert.Equal(t, []int{0, 1, 2}, state.ExpectedReplicaIndices)
	})
}
