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

package resourceclaim

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = resourcev1.AddToScheme(scheme)
	return scheme
}

const (
	pcsName   = "my-pcs"
	pclqName  = "my-pcs-0-worker"
	namespace = "default"
)

// newPCS returns a minimal PCS with one clique template ("worker") that has
// the given ResourceSharing specs and ResourceClaimTemplates.
func newPCS(refs []grovecorev1alpha1.ResourceSharingSpecBase, templates []grovecorev1alpha1.ResourceClaimTemplateConfig) *grovecorev1alpha1.PodCliqueSet {
	return &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: namespace, UID: "pcs-uid"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{
						Name:            "worker",
						ResourceSharing: refs,
					},
				},
				ResourceClaimTemplates: templates,
			},
		},
	}
}

// newPCLQ returns a PCLQ object with the standard labels expected by the component.
func newPCLQ(replicas int32) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pclqName,
			Namespace: namespace,
			UID:       "pclq-uid",
			Labels: map[string]string{
				apicommon.LabelPartOfKey:                pcsName,
				apicommon.LabelManagedByKey:             apicommon.LabelManagedByValue,
				apicommon.LabelPodCliqueSetReplicaIndex: "0",
			},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas: replicas,
			RoleName: "worker",
		},
	}
}

var gpuTemplate = grovecorev1alpha1.ResourceClaimTemplateConfig{
	Name: "gpu-mps",
	TemplateSpec: resourcev1.ResourceClaimTemplateSpec{
		Spec: resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: []resourcev1.DeviceRequest{{Name: "gpu"}},
			},
		},
	},
}

var gpuSharedTemplate = grovecorev1alpha1.ResourceClaimTemplateConfig{
	Name: "gpu-shared",
	TemplateSpec: resourcev1.ResourceClaimTemplateSpec{
		Spec: resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: []resourcev1.DeviceRequest{{Name: "gpu-shared"}},
			},
		},
	},
}

func newRC(name string, labels map[string]string, ownerName string, ownerUID types.UID) *resourcev1.ResourceClaim {
	controller := true
	return &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
					Kind:       "PodClique",
					Name:       ownerName,
					UID:        ownerUID,
					Controller: &controller,
				},
			},
		},
	}
}

// --- pclqResourceClaimLabels ---

func TestPclqResourceClaimLabels(t *testing.T) {
	pclqMeta := metav1.ObjectMeta{
		Name:      pclqName,
		Namespace: namespace,
		Labels: map[string]string{
			apicommon.LabelPartOfKey: pcsName,
		},
	}
	labels := pclqResourceClaimLabels(pclqMeta)

	assert.Equal(t, apicommon.LabelManagedByValue, labels[apicommon.LabelManagedByKey])
	assert.Equal(t, pcsName, labels[apicommon.LabelPartOfKey])
	assert.Equal(t, apicommon.LabelComponentNameResourceClaim, labels[apicommon.LabelComponentKey])
	assert.Equal(t, pclqName, labels[apicommon.LabelPodClique])
}

// --- GetExistingResourceNames ---

func TestGetExistingResourceNames(t *testing.T) {
	scheme := newTestScheme()
	pclqMeta := metav1.ObjectMeta{
		Name:      pclqName,
		Namespace: namespace,
		UID:       "pclq-uid",
		Labels:    map[string]string{apicommon.LabelPartOfKey: pcsName},
	}
	pclqLabels := pclqResourceClaimLabels(pclqMeta)

	t.Run("returns matching RCs", func(t *testing.T) {
		rc1 := newRC(pclqName+"-all-gpu-mps", pclqLabels, pclqName, "pclq-uid")
		rc2 := newRC(pclqName+"-0-gpu-mps", pclqLabels, pclqName, "pclq-uid")
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rc1, rc2).Build()
		r := _resource{client: cl, scheme: scheme}

		names, err := r.GetExistingResourceNames(context.Background(), logr.Discard(), pclqMeta)
		require.NoError(t, err)
		assert.Len(t, names, 2)
		assert.ElementsMatch(t, []string{pclqName + "-all-gpu-mps", pclqName + "-0-gpu-mps"}, names)
	})

	t.Run("returns empty when no RCs exist", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := _resource{client: cl, scheme: scheme}

		names, err := r.GetExistingResourceNames(context.Background(), logr.Discard(), pclqMeta)
		require.NoError(t, err)
		assert.Empty(t, names)
	})

	t.Run("does not return RCs from another PCLQ", func(t *testing.T) {
		otherLabels := resourceclaim.ResourceClaimLabels(pcsName)
		otherLabels[apicommon.LabelPodClique] = "my-pcs-0-router"

		ownRC := newRC(pclqName+"-all-gpu-mps", pclqLabels, pclqName, "pclq-uid")
		otherRC := newRC("my-pcs-0-router-all-gpu-mps", otherLabels, "my-pcs-0-router", "other-uid")
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ownRC, otherRC).Build()
		r := _resource{client: cl, scheme: scheme}

		names, err := r.GetExistingResourceNames(context.Background(), logr.Discard(), pclqMeta)
		require.NoError(t, err)
		assert.Equal(t, []string{pclqName + "-all-gpu-mps"}, names)
	})
}

// --- Sync ---

func TestSync(t *testing.T) {
	scheme := newTestScheme()

	refs := []grovecorev1alpha1.ResourceSharingSpecBase{
		{Name: "gpu-shared", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
		{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
	}
	templates := []grovecorev1alpha1.ResourceClaimTemplateConfig{gpuTemplate, gpuSharedTemplate}

	t.Run("creates AllReplicas and PerReplica RCs", func(t *testing.T) {
		pcs := newPCS(refs, templates)
		pclq := newPCLQ(2)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcs).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Sync(context.Background(), logr.Discard(), pclq)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}

		// AllReplicas RC
		require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
			Name: pclqName + "-all-gpu-shared", Namespace: namespace,
		}, rc))
		assert.Equal(t, pclqName, rc.Labels[apicommon.LabelPodClique])

		// PerReplica RC for index 0
		require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
			Name: pclqName + "-0-gpu-mps", Namespace: namespace,
		}, rc))
		assert.Equal(t, "0", rc.Labels[apicommon.LabelPodCliquePodIndex])
		assert.Equal(t, pclqName, rc.Labels[apicommon.LabelPodClique])

		// PerReplica RC for index 1
		require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
			Name: pclqName + "-1-gpu-mps", Namespace: namespace,
		}, rc))
		assert.Equal(t, "1", rc.Labels[apicommon.LabelPodCliquePodIndex])
	})

	t.Run("PerReplica RCs have ownerRef pointing to PCLQ", func(t *testing.T) {
		pcs := newPCS(refs, templates)
		pclq := newPCLQ(1)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcs).Build()
		r := _resource{client: cl, scheme: scheme}

		require.NoError(t, r.Sync(context.Background(), logr.Discard(), pclq))

		rc := &resourcev1.ResourceClaim{}
		require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
			Name: pclqName + "-0-gpu-mps", Namespace: namespace,
		}, rc))
		require.Len(t, rc.OwnerReferences, 1)
		assert.Equal(t, pclqName, rc.OwnerReferences[0].Name)
		assert.Equal(t, types.UID("pclq-uid"), rc.OwnerReferences[0].UID)
	})

	t.Run("cleans up stale PerReplica RCs after scale-in", func(t *testing.T) {
		pcs := newPCS(refs, templates)
		pclq := newPCLQ(1)

		pclqLabels := pclqResourceClaimLabels(pclq.ObjectMeta)

		staleLabels := make(map[string]string, len(pclqLabels)+1)
		for k, v := range pclqLabels {
			staleLabels[k] = v
		}
		staleLabels[apicommon.LabelPodCliquePodIndex] = "2"
		staleRC := newRC(pclqName+"-2-gpu-mps", staleLabels, pclqName, "pclq-uid")

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcs, staleRC).Build()
		r := _resource{client: cl, scheme: scheme}

		require.NoError(t, r.Sync(context.Background(), logr.Discard(), pclq))

		rc := &resourcev1.ResourceClaim{}
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{
			Name: pclqName + "-2-gpu-mps", Namespace: namespace,
		}, rc), "stale PerReplica RC should be deleted")
	})

	t.Run("no-op when no ResourceSharing specs", func(t *testing.T) {
		pcs := newPCS(nil, nil)
		pclq := newPCLQ(2)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcs).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Sync(context.Background(), logr.Discard(), pclq)
		require.NoError(t, err)

		rcList := &resourcev1.ResourceClaimList{}
		require.NoError(t, cl.List(context.Background(), rcList))
		assert.Empty(t, rcList.Items)
	})

	t.Run("no-op when clique template not found", func(t *testing.T) {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: namespace, UID: "pcs-uid"},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 1,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
						{Name: "router"},
					},
					ResourceClaimTemplates: templates,
				},
			},
		}
		pclq := newPCLQ(2)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcs).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Sync(context.Background(), logr.Discard(), pclq)
		require.NoError(t, err)

		rcList := &resourcev1.ResourceClaimList{}
		require.NoError(t, cl.List(context.Background(), rcList))
		assert.Empty(t, rcList.Items)
	})

	t.Run("errors when PCS not found", func(t *testing.T) {
		pclq := newPCLQ(1)
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Sync(context.Background(), logr.Discard(), pclq)
		assert.Error(t, err)
	})
}

// --- Delete ---

func TestDelete(t *testing.T) {
	scheme := newTestScheme()

	t.Run("deletes all PCLQ-level RCs", func(t *testing.T) {
		pclqMeta := metav1.ObjectMeta{
			Name:      pclqName,
			Namespace: namespace,
			Labels:    map[string]string{apicommon.LabelPartOfKey: pcsName},
		}
		pclqLabels := pclqResourceClaimLabels(pclqMeta)

		rc1 := newRC(pclqName+"-all-gpu-mps", pclqLabels, pclqName, "pclq-uid")
		rc2 := newRC(pclqName+"-0-gpu-mps", pclqLabels, pclqName, "pclq-uid")
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rc1, rc2).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Delete(context.Background(), logr.Discard(), pclqMeta)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{Name: pclqName + "-all-gpu-mps", Namespace: namespace}, rc))
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{Name: pclqName + "-0-gpu-mps", Namespace: namespace}, rc))
	})

	t.Run("does not delete RCs from another PCLQ", func(t *testing.T) {
		pclqMeta := metav1.ObjectMeta{
			Name:      pclqName,
			Namespace: namespace,
			Labels:    map[string]string{apicommon.LabelPartOfKey: pcsName},
		}
		pclqLabels := pclqResourceClaimLabels(pclqMeta)

		otherLabels := resourceclaim.ResourceClaimLabels(pcsName)
		otherLabels[apicommon.LabelPodClique] = "my-pcs-0-router"

		ownRC := newRC(pclqName+"-all-gpu-mps", pclqLabels, pclqName, "pclq-uid")
		otherRC := newRC("my-pcs-0-router-all-gpu-mps", otherLabels, "my-pcs-0-router", "other-uid")
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ownRC, otherRC).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Delete(context.Background(), logr.Discard(), pclqMeta)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{Name: pclqName + "-all-gpu-mps", Namespace: namespace}, rc))
		assert.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-0-router-all-gpu-mps", Namespace: namespace}, rc))
	})

	t.Run("no-op when no RCs exist", func(t *testing.T) {
		pclqMeta := metav1.ObjectMeta{
			Name:      pclqName,
			Namespace: namespace,
			Labels:    map[string]string{apicommon.LabelPartOfKey: pcsName},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Delete(context.Background(), logr.Discard(), pclqMeta)
		require.NoError(t, err)
	})
}
