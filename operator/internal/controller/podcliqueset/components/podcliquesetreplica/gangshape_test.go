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
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	gangShapeTestPCSName   = "pcs"
	gangShapeTestNamespace = "default"
)

// gangShapeChild describes the "fe" PodClique of one PCS replica.
type gangShapeChild struct {
	// hash is the gang-shape hash label. "current" is replaced by the current hash, "" omits the label.
	hash      string
	available bool
}

func newGangShapeTestPCS(replicas int32) *grovecorev1beta1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder(gangShapeTestPCSName, gangShapeTestNamespace, "uid").
		WithReplicas(replicas).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("fe").WithReplicas(2).WithMinAvailable(2).Build()).
		Build()
}

func newGangShapeTestClient(t *testing.T, pcs *grovecorev1beta1.PodCliqueSet, children []gangShapeChild) client.Client {
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
	currentHash := componentutils.ComputeGangShapeHash(pcs)
	objects := []client.Object{pcs}
	for replicaIndex, child := range children {
		pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "fe", pcs.Namespace, int32(replicaIndex)).WithReplicas(2).WithMinAvailable(2).Build()
		switch child.hash {
		case "":
		case "current":
			pclq.Labels[apicommon.LabelGangShapeHash] = currentHash
		default:
			pclq.Labels[apicommon.LabelGangShapeHash] = child.hash
		}
		if child.available {
			pclq.Status.ReadyReplicas = 2
		}
		objects = append(objects, pclq)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

// TestGetGangShapeWork pins how stale replicas are found and recreated one at a time.
func TestGetGangShapeWork(t *testing.T) {
	testCases := []struct {
		description           string
		children              []gangShapeChild
		currentlyUpdating     *int32
		pcsIndicesToTerminate []int
		wantStale             []int
		wantRecreateIndex     *int
	}{
		{
			description: "no stale replica, no work",
			children:    []gangShapeChild{{hash: "current", available: true}, {hash: "current", available: true}},
		},
		{
			description: "a child without the label is adopted, not stale",
			children:    []gangShapeChild{{hash: "", available: true}, {hash: "current", available: true}},
		},
		{
			description:       "the lowest stale replica is recreated when every other replica is available",
			children:          []gangShapeChild{{hash: "current", available: true}, {hash: "old"}, {hash: "old"}},
			wantStale:         []int{1, 2},
			wantRecreateIndex: ptr.To(1),
		},
		{
			description: "no recreate while a replica that is not stale is unavailable",
			children:    []gangShapeChild{{hash: "current", available: false}, {hash: "old"}},
			wantStale:   []int{1},
		},
		{
			description:           "no recreate while a replica is being gang-terminated",
			children:              []gangShapeChild{{hash: "current", available: true}, {hash: "old"}},
			pcsIndicesToTerminate: []int{0},
			wantStale:             []int{1},
		},
		{
			description:       "the replica under a rolling update is recreated first",
			children:          []gangShapeChild{{hash: "old"}, {hash: "old"}},
			currentlyUpdating: ptr.To[int32](1),
			wantStale:         []int{0, 1},
			wantRecreateIndex: ptr.To(1),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := newGangShapeTestPCS(int32(len(tc.children)))
			if tc.currentlyUpdating != nil {
				pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: *tc.currentlyUpdating}},
				}
			}
			r := _resource{client: newGangShapeTestClient(t, pcs, tc.children), eventRecorder: record.NewFakeRecorder(10)}

			work, err := r.getGangShapeWork(context.Background(), logr.Discard(), pcs, tc.pcsIndicesToTerminate)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStale, work.staleReplicaIndices)
			if tc.wantRecreateIndex == nil {
				assert.Nil(t, work.recreateTask)
				return
			}
			require.NotNil(t, work.recreateTask)
			assert.Equal(t, *tc.wantRecreateIndex, work.recreateReplicaIndex)
		})
	}
}

// TestGangShapeRecreateDeletesReplicaAndEmitsEvent pins that the recreate deletes the stale replica's
// PodCliques and emits a GangShapeChanged event.
func TestGangShapeRecreateDeletesReplicaAndEmitsEvent(t *testing.T) {
	pcs := newGangShapeTestPCS(2)
	cl := newGangShapeTestClient(t, pcs, []gangShapeChild{{hash: "current", available: true}, {hash: "old"}})
	recorder := record.NewFakeRecorder(10)
	r := _resource{client: cl, eventRecorder: recorder}

	work, err := r.getGangShapeWork(context.Background(), logr.Discard(), pcs, nil)
	require.NoError(t, err)
	require.NotNil(t, work.recreateTask)
	require.NoError(t, work.recreateTask.Fn(context.Background()))

	pclqs := &grovecorev1beta1.PodCliqueList{}
	require.NoError(t, cl.List(context.Background(), pclqs, client.InNamespace(gangShapeTestNamespace)))
	require.Len(t, pclqs.Items, 1)
	assert.Equal(t, "0", pclqs.Items[0].Labels[apicommon.LabelPodCliqueSetReplicaIndex])

	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "GangShapeChanged")
	default:
		t.Fatal("expected a GangShapeChanged event")
	}
}
