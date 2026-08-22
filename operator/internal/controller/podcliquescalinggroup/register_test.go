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

package podcliquescalinggroup

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestMapComponentToSiblingPCSGs(t *testing.T) {
	const (
		namespace = "default"
		pcsName   = "inference"
	)
	pcsgA := newManagedPCSG("inference-0-workers-a", pcsName)
	pcsgB := newManagedPCSG("inference-0-workers-b", pcsName)
	foreignPCSG := newManagedPCSG("inference-0-foreign", "other")
	foreignPCSG.Labels = apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName)

	cl := testutils.NewTestClientBuilder().
		WithObjects(pcsgA, pcsgB, foreignPCSG).
		Build()
	mapFn := mapComponentToSiblingPCSGs(cl)

	tests := []struct {
		name      string
		component client.Object
	}{
		{
			name: "standalone PodClique change",
			component: &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
				Name:            "inference-0-router",
				Namespace:       namespace,
				Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
				OwnerReferences: []metav1.OwnerReference{controllerOwner(constants.KindPodCliqueSet, pcsName)},
			}},
		},
		{
			name:      "PodCliqueScalingGroup change",
			component: pcsgA,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := mapFn(t.Context(), tt.component)
			assert.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: namespace, Name: pcsgA.Name}},
				{NamespacedName: types.NamespacedName{Namespace: namespace, Name: pcsgB.Name}},
			}, requests)
		})
	}
}

func TestComponentIdleStateChangedPredicate(t *testing.T) {
	pred, ok := componentIdleStateChangedPredicate().(predicate.Funcs)
	require.True(t, ok, "predicate must be predicate.Funcs")

	for _, component := range []client.Object{newManagedPCLQ(0), newManagedPCSGWithReplicas(0)} {
		assert.False(t, pred.CreateFunc(event.CreateEvent{Object: component}))
		assert.True(t, pred.DeleteFunc(event.DeleteEvent{Object: component}))
	}
	assert.False(t, pred.DeleteFunc(event.DeleteEvent{Object: &grovecorev1alpha1.PodCliqueScalingGroup{}}))

	tests := []struct {
		name string
		old  client.Object
		new  client.Object
		want bool
	}{
		{
			name: "standalone PodClique enters idle",
			old:  newManagedPCLQ(1),
			new:  newManagedPCLQ(0),
			want: true,
		},
		{
			name: "PodClique wakes from idle",
			old:  newManagedPCLQ(0),
			new:  newManagedPCLQ(1),
			want: true,
		},
		{
			name: "positive PodClique scale does not change dependency state",
			old:  newManagedPCLQ(1),
			new:  newManagedPCLQ(2),
		},
		{
			name: "PodCliqueScalingGroup enters idle",
			old:  newManagedPCSGWithReplicas(1),
			new:  newManagedPCSGWithReplicas(0),
			want: true,
		},
		{
			name: "unowned PodCliqueScalingGroup",
			old: &grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: metav1.ObjectMeta{
				Generation: 1,
				Labels:     apicommon.GetDefaultLabelsForPodCliqueSetManagedResources("inference"),
			}},
			new: &grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: metav1.ObjectMeta{
				Generation: 2,
				Labels:     apicommon.GetDefaultLabelsForPodCliqueSetManagedResources("inference"),
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pred.UpdateFunc(event.UpdateEvent{
				ObjectOld: tt.old,
				ObjectNew: tt.new,
			}))
		})
	}
}

func newManagedPCLQ(replicas int32) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "inference-0-router",
			Namespace:       "default",
			Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources("inference"),
			OwnerReferences: []metav1.OwnerReference{controllerOwner(constants.KindPodCliqueSet, "inference")},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: replicas},
	}
}

func newManagedPCSGWithReplicas(replicas int32) *grovecorev1alpha1.PodCliqueScalingGroup {
	pcsg := newManagedPCSG("inference-0-workers", "inference")
	pcsg.Spec.Replicas = replicas
	return pcsg
}

func newManagedPCSG(name, pcsName string) *grovecorev1alpha1.PodCliqueScalingGroup {
	return &grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: metav1.ObjectMeta{
		Name:            name,
		Namespace:       "default",
		Labels:          apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		OwnerReferences: []metav1.OwnerReference{controllerOwner(constants.KindPodCliqueSet, pcsName)},
	}}
}

func controllerOwner(kind, name string) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{
		Kind:       kind,
		Name:       name,
		Controller: &controller,
	}
}
