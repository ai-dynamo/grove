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
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

	ct := &grovecorev1alpha1.ClusterTopology{ObjectMeta: metav1.ObjectMeta{Name: "selected-topology"}}
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
