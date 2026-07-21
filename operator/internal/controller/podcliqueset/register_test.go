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

package podcliqueset

import (
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestMapClusterTopologyToPodCliqueSets(t *testing.T) {
	makePCS := func(namespace, name string, mutate func(*grovecorev1alpha1.PodCliqueSet)) *grovecorev1alpha1.PodCliqueSet {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 1,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{},
			},
		}
		if mutate != nil {
			mutate(pcs)
		}
		return pcs
	}

	ct := &grovecorev1alpha1.ClusterTopologyBinding{ObjectMeta: metav1.ObjectMeta{Name: "selected-topology"}}
	pcsA := makePCS("default", "pcs-a", func(pcs *grovecorev1alpha1.PodCliqueSet) {
		pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
			TopologyName: "selected-topology",
			PackDomain:   grovecorev1alpha1.TopologyDomainRack,
		}
	})
	pcsB := makePCS("team-b", "pcs-b", func(pcs *grovecorev1alpha1.PodCliqueSet) {
		pcs.Spec.Template.Cliques = []*grovecorev1alpha1.PodCliqueTemplateSpec{
			{
				Name: "worker",
				TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
					TopologyName: "selected-topology",
					PackDomain:   grovecorev1alpha1.TopologyDomainHost,
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1},
			},
		}
	})
	pcsOther := makePCS("default", "pcs-other", func(pcs *grovecorev1alpha1.PodCliqueSet) {
		pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
			TopologyName: "other-topology",
			PackDomain:   grovecorev1alpha1.TopologyDomainRack,
		}
	})
	pcsWithoutTopology := makePCS("default", "pcs-no-topology", nil)

	fakeClient := testutils.NewTestClientBuilder().
		WithObjects(ct, pcsA, pcsB, pcsOther, pcsWithoutTopology).
		Build()

	mapFn := mapClusterTopologyToPodCliqueSets(fakeClient)
	requests := mapFn(t.Context(), ct)

	require.Len(t, requests, 2)
	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "pcs-a"}},
		{NamespacedName: types.NamespacedName{Namespace: "team-b", Name: "pcs-b"}},
	}, requests)
}

// TestPodCliqueSetPredicateUpdate verifies that the PodCliqueSet update predicate
// enqueues on deletion, generation bumps, and reconcile-trigger annotation changes,
// and ignores unrelated annotation edits or no-op updates.
func TestPodCliqueSetPredicateUpdate(t *testing.T) {
	pred, ok := podCliqueSetPredicate().(predicate.Funcs)
	require.True(t, ok, "predicate must be predicate.Funcs")

	deletionTimestamp := metav1.Now()
	deletingPCS := podCliqueSetWithGenerationAndAnnotations(1, nil)
	deletingPCS.DeletionTimestamp = &deletionTimestamp

	tests := []struct {
		name string
		old  *grovecorev1alpha1.PodCliqueSet
		new  *grovecorev1alpha1.PodCliqueSet
		want bool
	}{
		{
			name: "generation change enqueues",
			old:  podCliqueSetWithGenerationAndAnnotations(1, nil),
			new:  podCliqueSetWithGenerationAndAnnotations(2, nil),
			want: true,
		},
		{
			name: "deletion timestamp addition enqueues",
			old:  podCliqueSetWithGenerationAndAnnotations(1, nil),
			new:  deletingPCS.DeepCopy(),
			want: true,
		},
		{
			name: "existing deletion timestamp does not enqueue",
			old:  deletingPCS.DeepCopy(),
			new:  deletingPCS.DeepCopy(),
			want: false,
		},
		{
			name: "reconcile trigger annotation change enqueues",
			old: podCliqueSetWithGenerationAndAnnotations(1, map[string]string{
				constants.AnnotationReconcileTrigger: "old",
			}),
			new: podCliqueSetWithGenerationAndAnnotations(1, map[string]string{
				constants.AnnotationReconcileTrigger: "new",
			}),
			want: true,
		},
		{
			name: "unrelated annotation change does not enqueue",
			old: podCliqueSetWithGenerationAndAnnotations(1, map[string]string{
				"example.com/other": "old",
			}),
			new: podCliqueSetWithGenerationAndAnnotations(1, map[string]string{
				"example.com/other": "new",
			}),
			want: false,
		},
		{
			name: "same generation and same trigger value does not enqueue",
			old: podCliqueSetWithGenerationAndAnnotations(1, map[string]string{
				constants.AnnotationReconcileTrigger: "same",
			}),
			new: podCliqueSetWithGenerationAndAnnotations(1, map[string]string{
				constants.AnnotationReconcileTrigger: "same",
			}),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pred.UpdateFunc(event.UpdateEvent{ObjectOld: tt.old, ObjectNew: tt.new}))
		})
	}
}

func podCliqueSetWithGenerationAndAnnotations(generation int64, annotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	return &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Generation:  generation,
			Annotations: annotations,
		},
	}
}

// TestPodCliquePredicateStatusChangesAffectingUpdatedAccounting asserts that the
// PodClique predicate enqueues when status fields feeding rolling-update
// accounting (current hashes, updated replica count, update progress) change.
func TestPodCliquePredicateStatusChangesAffectingUpdatedAccounting(t *testing.T) {
	pred, ok := podCliquePredicate().(predicate.Funcs)
	require.True(t, ok, "predicate must be predicate.Funcs")

	tests := []struct {
		name   string
		mutate func(*grovecorev1alpha1.PodClique)
	}{
		{
			name: "current pod template hash changes",
			mutate: func(pclq *grovecorev1alpha1.PodClique) {
				pclq.Status.CurrentPodTemplateHash = ptr.To("new-template-hash")
			},
		},
		{
			name: "current PCS generation hash changes",
			mutate: func(pclq *grovecorev1alpha1.PodClique) {
				pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To("new-generation-hash")
			},
		},
		{
			name: "updated replicas changes",
			mutate: func(pclq *grovecorev1alpha1.PodClique) {
				pclq.Status.UpdatedReplicas = 1
			},
		},
		{
			name: "update progress changes",
			mutate: func(pclq *grovecorev1alpha1.PodClique) {
				pclq.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueUpdateProgress{
					PodCliqueSetGenerationHash: "generation-hash",
					PodTemplateHash:            "template-hash",
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldPCLQ := testutils.NewPodCliqueBuilder("pcs", uuid.NewUUID(), "worker", "default", 0).Build()
			newPCLQ := oldPCLQ.DeepCopy()
			tt.mutate(newPCLQ)

			assert.True(t, pred.UpdateFunc(event.UpdateEvent{ObjectOld: oldPCLQ, ObjectNew: newPCLQ}))
		})
	}
}

// TestPodCliqueScalingGroupPredicateStatusChangesAffectingUpdatedAccounting asserts
// that the PodCliqueScalingGroup predicate enqueues on status changes relevant to
// rolling-update accounting (generation hash, updated replicas, update progress).
func TestPodCliqueScalingGroupPredicateStatusChangesAffectingUpdatedAccounting(t *testing.T) {
	pred, ok := podCliqueScalingGroupPredicate().(predicate.Funcs)
	require.True(t, ok, "predicate must be predicate.Funcs")

	tests := []struct {
		name   string
		mutate func(*grovecorev1alpha1.PodCliqueScalingGroup)
	}{
		{
			name: "current PCS generation hash changes",
			mutate: func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
				pcsg.Status.CurrentPodCliqueSetGenerationHash = ptr.To("new-generation-hash")
			},
		},
		{
			name: "updated replicas changes",
			mutate: func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
				pcsg.Status.UpdatedReplicas = 1
			},
		},
		{
			name: "update progress changes",
			mutate: func(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) {
				pcsg.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
					PodCliqueSetGenerationHash: "generation-hash",
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldPCSG := &grovecorev1alpha1.PodCliqueScalingGroup{}
			newPCSG := oldPCSG.DeepCopy()
			tt.mutate(newPCSG)

			assert.True(t, pred.UpdateFunc(event.UpdateEvent{ObjectOld: oldPCSG, ObjectNew: newPCSG}))
		})
	}
}
