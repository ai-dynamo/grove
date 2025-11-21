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

package clustertopology

import (
	"context"
	"testing"

	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestPodCliqueSetEventHandler_Create(t *testing.T) {
	handler := &podCliqueSetEventHandler{}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()

	tests := []struct {
		name                string
		obj                 client.Object
		expectedQueueLength int
	}{
		{
			name: "PodCliqueSet with topology label - should not enqueue",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("test-topology").
				Build(),
			expectedQueueLength: 0,
		},
		{
			name: "PodCliqueSet without topology label - should not enqueue",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				Build(),
			expectedQueueLength: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset queue
			for queue.Len() > 0 {
				_, _ = queue.Get()
			}

			handler.Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: tt.obj}, queue)
			assert.Equal(t, tt.expectedQueueLength, queue.Len(), "unexpected queue length")
		})
	}
}

func TestPodCliqueSetEventHandler_Delete(t *testing.T) {
	handler := &podCliqueSetEventHandler{}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()

	tests := []struct {
		name                string
		obj                 client.Object
		expectedQueueLength int
		expectedTopology    string
	}{
		{
			name: "PodCliqueSet with topology label - should enqueue topology",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("test-topology").
				Build(),
			expectedQueueLength: 1,
			expectedTopology:    "test-topology",
		},
		{
			name: "PodCliqueSet without topology label - should not enqueue",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				Build(),
			expectedQueueLength: 0,
		},
		{
			name: "PodCliqueSet with empty topology label - should not enqueue",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("").
				Build(),
			expectedQueueLength: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset queue
			for queue.Len() > 0 {
				item, _ := queue.Get()
				queue.Done(item)
			}

			handler.Delete(context.Background(), event.TypedDeleteEvent[client.Object]{Object: tt.obj}, queue)
			assert.Equal(t, tt.expectedQueueLength, queue.Len(), "unexpected queue length")

			if tt.expectedQueueLength > 0 {
				item, shutdown := queue.Get()
				require.False(t, shutdown)
				assert.Equal(t, tt.expectedTopology, item.Name, "unexpected topology name in queue")
				assert.Equal(t, "", item.Namespace, "ClusterTopology should be cluster-scoped")
				queue.Done(item)
			}
		})
	}
}

func TestPodCliqueSetEventHandler_Update(t *testing.T) {
	handler := &podCliqueSetEventHandler{}

	tests := []struct {
		name                string
		oldObj              client.Object
		newObj              client.Object
		expectedQueueLength int
		expectedTopologies  []string // ordered list of expected topology names
		expectedDescription string
	}{
		{
			name: "topology label removed - should enqueue old topology",
			oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("old-topology").
				Build(),
			newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				Build(),
			expectedQueueLength: 1,
			expectedTopologies:  []string{"old-topology"},
			expectedDescription: "old topology should be reconciled to potentially unblock deletion",
		},
		{
			name: "topology label changed - should enqueue both topologies",
			oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("old-topology").
				Build(),
			newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("new-topology").
				Build(),
			expectedQueueLength: 2,
			expectedTopologies:  []string{"old-topology", "new-topology"},
			expectedDescription: "both old and new topologies should be reconciled",
		},
		{
			name: "topology label unchanged - should not enqueue",
			oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("test-topology").
				Build(),
			newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("test-topology").
				Build(),
			expectedQueueLength: 0,
			expectedDescription: "no change to topology label, no reconciliation needed",
		},
		{
			name: "topology label added - should enqueue new topology",
			oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				Build(),
			newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("new-topology").
				Build(),
			expectedQueueLength: 1,
			expectedTopologies:  []string{"new-topology"},
			expectedDescription: "new topology should be reconciled when added",
		},
		{
			name: "no topology label on either object - should not enqueue",
			oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				Build(),
			newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				Build(),
			expectedQueueLength: 0,
			expectedDescription: "no topology involved",
		},
		{
			name: "topology label changed to empty (removed) - should enqueue old topology",
			oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("old-topology").
				Build(),
			newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("").
				Build(),
			expectedQueueLength: 1,
			expectedTopologies:  []string{"old-topology"},
			expectedDescription: "empty label treated as removal, old topology should be reconciled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer queue.ShutDown()

			handler.Update(context.Background(), event.TypedUpdateEvent[client.Object]{
				ObjectOld: tt.oldObj,
				ObjectNew: tt.newObj,
			}, queue)

			assert.Equal(t, tt.expectedQueueLength, queue.Len(), "unexpected queue length: %s", tt.expectedDescription)

			// Collect all enqueued topologies
			enqueuedTopologies := make(map[string]struct{})
			for i := 0; i < tt.expectedQueueLength; i++ {
				item, shutdown := queue.Get()
				require.False(t, shutdown)
				enqueuedTopologies[item.Name] = struct{}{}
				assert.Equal(t, "", item.Namespace, "ClusterTopology should be cluster-scoped")
				queue.Done(item)
			}

			// Verify expected topologies are in the queue
			for _, expectedTopology := range tt.expectedTopologies {
				_, found := enqueuedTopologies[expectedTopology]
				assert.True(t, found, "expected topology %s to be enqueued: %s", expectedTopology, tt.expectedDescription)
			}
			assert.Equal(t, len(tt.expectedTopologies), len(enqueuedTopologies), "unexpected number of unique topologies enqueued")
		})
	}
}

func TestPodCliqueSetEventHandler_Generic(t *testing.T) {
	handler := &podCliqueSetEventHandler{}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()

	tests := []struct {
		name                string
		obj                 client.Object
		expectedQueueLength int
	}{
		{
			name: "PodCliqueSet with topology label - should not enqueue",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("test-topology").
				Build(),
			expectedQueueLength: 0,
		},
		{
			name: "PodCliqueSet without topology label - should not enqueue",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				Build(),
			expectedQueueLength: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset queue
			for queue.Len() > 0 {
				item, _ := queue.Get()
				queue.Done(item)
			}

			handler.Generic(context.Background(), event.TypedGenericEvent[client.Object]{Object: tt.obj}, queue)
			assert.Equal(t, tt.expectedQueueLength, queue.Len(), "unexpected queue length")
		})
	}
}
