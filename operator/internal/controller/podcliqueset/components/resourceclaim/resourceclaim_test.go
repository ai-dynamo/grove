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
	"strconv"
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
	namespace = "default"
)

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

func newPCS(replicas int32, refs []grovecorev1alpha1.PCSResourceSharingSpec, templates []grovecorev1alpha1.ResourceClaimTemplateConfig) *grovecorev1alpha1.PodCliqueSet {
	return &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: namespace, UID: "pcs-uid"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: replicas,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker"},
				},
				ResourceClaimTemplates: templates,
				ResourceSharing:        refs,
			},
		},
	}
}

func newRC(name string, labels map[string]string) *resourcev1.ResourceClaim {
	return &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace, Labels: labels,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
					Kind:       "PodCliqueSet",
					Name:       pcsName,
					UID:        "pcs-uid",
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
	}
}

func pcsRCLabels() map[string]string {
	return resourceclaim.ResourceClaimLabels(pcsName)
}

// --- GetExistingResourceNames ---

func TestGetExistingResourceNames(t *testing.T) {
	scheme := newTestScheme()
	pcsMeta := metav1.ObjectMeta{Name: pcsName, Namespace: namespace, UID: "pcs-uid"}
	labels := pcsRCLabels()

	t.Run("returns matching RCs", func(t *testing.T) {
		rc1 := newRC(pcsName+"-all-gpu-mps", labels)
		rc2 := newRC(pcsName+"-0-gpu-mps", labels)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rc1, rc2).Build()
		r := _resource{client: cl, scheme: scheme}

		names, err := r.GetExistingResourceNames(context.Background(), logr.Discard(), pcsMeta)
		require.NoError(t, err)
		assert.Len(t, names, 2)
		assert.ElementsMatch(t, []string{pcsName + "-all-gpu-mps", pcsName + "-0-gpu-mps"}, names)
	})

	t.Run("returns empty when no RCs exist", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := _resource{client: cl, scheme: scheme}

		names, err := r.GetExistingResourceNames(context.Background(), logr.Discard(), pcsMeta)
		require.NoError(t, err)
		assert.Empty(t, names)
	})
}

// --- Sync ---

func TestSync(t *testing.T) {
	scheme := newTestScheme()

	refs := []grovecorev1alpha1.PCSResourceSharingSpec{
		{ResourceSharingSpecBase: grovecorev1alpha1.ResourceSharingSpecBase{Name: "gpu-shared", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas}},
		{ResourceSharingSpecBase: grovecorev1alpha1.ResourceSharingSpecBase{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica}},
	}
	templates := []grovecorev1alpha1.ResourceClaimTemplateConfig{gpuTemplate, gpuSharedTemplate}

	t.Run("creates AllReplicas and PerReplica RCs", func(t *testing.T) {
		pcs := newPCS(2, refs, templates)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcs).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Sync(context.Background(), logr.Discard(), pcs)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}

		require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
			Name: pcsName + "-all-gpu-shared", Namespace: namespace,
		}, rc))

		require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
			Name: pcsName + "-0-gpu-mps", Namespace: namespace,
		}, rc))
		assert.Equal(t, "0", rc.Labels[apicommon.LabelPodCliqueSetReplicaIndex])

		require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
			Name: pcsName + "-1-gpu-mps", Namespace: namespace,
		}, rc))
		assert.Equal(t, "1", rc.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
	})

	t.Run("RCs have ownerRef pointing to PCS", func(t *testing.T) {
		pcs := newPCS(1, refs, templates)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcs).Build()
		r := _resource{client: cl, scheme: scheme}

		require.NoError(t, r.Sync(context.Background(), logr.Discard(), pcs))

		rc := &resourcev1.ResourceClaim{}
		require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
			Name: pcsName + "-all-gpu-shared", Namespace: namespace,
		}, rc))
		require.Len(t, rc.OwnerReferences, 1)
		assert.Equal(t, pcsName, rc.OwnerReferences[0].Name)
		assert.Equal(t, types.UID("pcs-uid"), rc.OwnerReferences[0].UID)
	})

	t.Run("cleans up stale PerReplica RCs after scale-in", func(t *testing.T) {
		pcs := newPCS(1, refs, templates)

		staleLabels := pcsRCLabels()
		staleLabels[apicommon.LabelPodCliqueSetReplicaIndex] = "2"
		staleRC := newRC(pcsName+"-2-gpu-mps", staleLabels)

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcs, staleRC).Build()
		r := _resource{client: cl, scheme: scheme}

		require.NoError(t, r.Sync(context.Background(), logr.Discard(), pcs))

		rc := &resourcev1.ResourceClaim{}
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{
			Name: pcsName + "-2-gpu-mps", Namespace: namespace,
		}, rc), "stale PerReplica RC should be deleted")
	})

	t.Run("no-op when no ResourceSharing specs", func(t *testing.T) {
		pcs := newPCS(2, nil, nil)
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Sync(context.Background(), logr.Discard(), pcs)
		require.NoError(t, err)

		rcList := &resourcev1.ResourceClaimList{}
		require.NoError(t, cl.List(context.Background(), rcList))
		assert.Empty(t, rcList.Items)
	})

	t.Run("AllReplicas RC is shared across replicas", func(t *testing.T) {
		allReplicasOnly := []grovecorev1alpha1.PCSResourceSharingSpec{
			{ResourceSharingSpecBase: grovecorev1alpha1.ResourceSharingSpecBase{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas}},
		}
		pcs := newPCS(3, allReplicasOnly, templates)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcs).Build()
		r := _resource{client: cl, scheme: scheme}

		require.NoError(t, r.Sync(context.Background(), logr.Discard(), pcs))

		rcList := &resourcev1.ResourceClaimList{}
		require.NoError(t, cl.List(context.Background(), rcList))

		allReplicasCount := 0
		for _, rc := range rcList.Items {
			if rc.Name == pcsName+"-all-gpu-mps" {
				allReplicasCount++
			}
		}
		assert.Equal(t, 1, allReplicasCount, "AllReplicas should produce exactly one RC regardless of replica count")
	})

	t.Run("PerReplica creates one RC per replica", func(t *testing.T) {
		perReplicaOnly := []grovecorev1alpha1.PCSResourceSharingSpec{
			{ResourceSharingSpecBase: grovecorev1alpha1.ResourceSharingSpecBase{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica}},
		}
		pcs := newPCS(3, perReplicaOnly, templates)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pcs).Build()
		r := _resource{client: cl, scheme: scheme}

		require.NoError(t, r.Sync(context.Background(), logr.Discard(), pcs))

		rc := &resourcev1.ResourceClaim{}
		for i := range 3 {
			require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
				Name: pcsName + "-" + strconv.Itoa(i) + "-gpu-mps", Namespace: namespace,
			}, rc))
		}
	})
}

// --- Delete ---

func TestDelete(t *testing.T) {
	scheme := newTestScheme()
	labels := pcsRCLabels()

	t.Run("deletes all PCS-level RCs", func(t *testing.T) {
		pcsMeta := metav1.ObjectMeta{Name: pcsName, Namespace: namespace}

		rc1 := newRC(pcsName+"-all-gpu-shared", labels)
		rc2 := newRC(pcsName+"-0-gpu-mps", labels)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rc1, rc2).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Delete(context.Background(), logr.Discard(), pcsMeta)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{Name: pcsName + "-all-gpu-shared", Namespace: namespace}, rc))
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{Name: pcsName + "-0-gpu-mps", Namespace: namespace}, rc))
	})

	t.Run("no-op when no RCs exist", func(t *testing.T) {
		pcsMeta := metav1.ObjectMeta{Name: pcsName, Namespace: namespace}
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := _resource{client: cl, scheme: scheme}

		err := r.Delete(context.Background(), logr.Discard(), pcsMeta)
		require.NoError(t, err)
	})
}
