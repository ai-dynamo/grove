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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestMapPodCliqueSetToClusterTopology(t *testing.T) {
	tests := []struct {
		name              string
		obj               client.Object
		expectedRequests  int
		expectedTopology  string
		expectedNamespace string
	}{
		{
			name: "PodCliqueSet with topology label - should map to ClusterTopology",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("test-topology").
				Build(),
			expectedRequests:  1,
			expectedTopology:  "test-topology",
			expectedNamespace: "", // ClusterTopology is cluster-scoped
		},
		{
			name: "PodCliqueSet without topology label - should not map",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				Build(),
			expectedRequests: 0,
		},
		{
			name: "PodCliqueSet with empty topology label - should not map",
			obj: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "test-ns",
					Labels: map[string]string{
						apicommon.LabelClusterTopologyName: "",
					},
				},
			},
			expectedRequests: 0,
		},
		{
			name: "Non-PodCliqueSet object - should not map",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels: map[string]string{
						apicommon.LabelClusterTopologyName: "test-topology",
					},
				},
			},
			expectedRequests: 0,
		},
		{
			name: "PodCliqueSet with custom topology name",
			obj: testutils.NewPodCliqueSetBuilder("my-pcs", "my-ns", uuid.NewUUID()).
				WithTopologyLabel("custom-topology-name").
				Build(),
			expectedRequests:  1,
			expectedTopology:  "custom-topology-name",
			expectedNamespace: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapFunc := mapPodCliqueSetToClusterTopology()
			requests := mapFunc(context.Background(), tt.obj)

			assert.Len(t, requests, tt.expectedRequests, "unexpected number of reconcile requests")

			if tt.expectedRequests > 0 {
				assert.Equal(t, tt.expectedTopology, requests[0].Name, "unexpected topology name")
				assert.Equal(t, tt.expectedNamespace, requests[0].Namespace, "ClusterTopology should be cluster-scoped (empty namespace)")
			}
		})
	}
}

func TestPodCliqueSetDeletionPredicate(t *testing.T) {
	predicate := podCliqueSetDeletionPredicate()

	t.Run("CreateFunc", func(t *testing.T) {
		tests := []struct {
			name     string
			obj      client.Object
			expected bool
		}{
			{
				name: "PodCliqueSet with topology label - should not reconcile on create",
				obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
				expected: false,
			},
			{
				name: "PodCliqueSet without topology label - should not reconcile on create",
				obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					Build(),
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := predicate.Create(event.CreateEvent{Object: tt.obj})
				assert.Equal(t, tt.expected, result, "unexpected create predicate result")
			})
		}
	})

	t.Run("DeleteFunc", func(t *testing.T) {
		tests := []struct {
			name     string
			obj      client.Object
			expected bool
		}{
			{
				name: "PodCliqueSet with topology label - should reconcile on delete",
				obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
				expected: true,
			},
			{
				name: "PodCliqueSet without topology label - should not reconcile on delete",
				obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					Build(),
				expected: false,
			},
			{
				name: "PodCliqueSet with empty topology label - should not reconcile on delete",
				obj: &grovecorev1alpha1.PodCliqueSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs",
						Namespace: "test-ns",
						Labels: map[string]string{
							apicommon.LabelClusterTopologyName: "",
						},
					},
				},
				expected: false,
			},
			{
				name:     "nil object - should not reconcile on delete",
				obj:      nil,
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := predicate.Delete(event.DeleteEvent{Object: tt.obj})
				assert.Equal(t, tt.expected, result, "unexpected delete predicate result")
			})
		}
	})

	t.Run("UpdateFunc", func(t *testing.T) {
		tests := []struct {
			name     string
			oldObj   client.Object
			newObj   client.Object
			expected bool
		}{
			{
				name: "topology label removed - should reconcile",
				oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
				newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					Build(),
				expected: true,
			},
			{
				name: "topology label changed - should reconcile",
				oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("old-topology").
					Build(),
				newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("new-topology").
					Build(),
				expected: true,
			},
			{
				name: "topology label unchanged - should not reconcile",
				oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
				newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
				expected: false,
			},
			{
				name: "topology label added - should not reconcile",
				oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					Build(),
				newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
				expected: false,
			},
			{
				name: "no topology label on either object - should not reconcile",
				oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					Build(),
				newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					Build(),
				expected: false,
			},
			{
				name: "topology label removed (changed to empty) - should reconcile",
				oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
				newObj: &grovecorev1alpha1.PodCliqueSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs",
						Namespace: "test-ns",
						Labels: map[string]string{
							apicommon.LabelClusterTopologyName: "",
						},
					},
				},
				expected: true, // Empty label is treated as removed, so should reconcile
			},
			{
				name: "both labels empty - should not reconcile",
				oldObj: &grovecorev1alpha1.PodCliqueSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs",
						Namespace: "test-ns",
						Labels: map[string]string{
							apicommon.LabelClusterTopologyName: "",
						},
					},
				},
				newObj: &grovecorev1alpha1.PodCliqueSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pcs",
						Namespace: "test-ns",
						Labels: map[string]string{
							apicommon.LabelClusterTopologyName: "",
						},
					},
				},
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := predicate.Update(event.UpdateEvent{
					ObjectOld: tt.oldObj,
					ObjectNew: tt.newObj,
				})
				assert.Equal(t, tt.expected, result, "unexpected update predicate result")
			})
		}
	})

	t.Run("GenericFunc", func(t *testing.T) {
		tests := []struct {
			name     string
			obj      client.Object
			expected bool
		}{
			{
				name: "PodCliqueSet with topology label - should not reconcile on generic event",
				obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithTopologyLabel("test-topology").
					Build(),
				expected: false,
			},
			{
				name: "PodCliqueSet without topology label - should not reconcile on generic event",
				obj: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					Build(),
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := predicate.Generic(event.GenericEvent{Object: tt.obj})
				assert.Equal(t, tt.expected, result, "unexpected generic predicate result")
			})
		}
	})
}
