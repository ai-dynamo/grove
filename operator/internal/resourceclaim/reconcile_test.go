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
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// --- RCName ---

func TestRCName(t *testing.T) {
	t.Run("AllReplicas scope", func(t *testing.T) {
		base := &grovecorev1alpha1.ResourceSharingSpec{
			Name:  "gpu-mps",
			Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
		}
		assert.Equal(t, "owner-all-gpu-mps", RCName("owner", base, nil))
	})

	t.Run("PerReplica scope", func(t *testing.T) {
		base := &grovecorev1alpha1.ResourceSharingSpec{
			Name:  "gpu-mps",
			Scope: grovecorev1alpha1.ResourceSharingScopePerReplica,
		}
		idx := 3
		assert.Equal(t, "owner-3-gpu-mps", RCName("owner", base, &idx))
	})
}

// --- ResourceClaimLabels ---

func TestResourceClaimLabels(t *testing.T) {
	labels := ResourceClaimLabels("my-pcs")
	assert.Equal(t, apicommon.LabelManagedByValue, labels[apicommon.LabelManagedByKey])
	assert.Equal(t, "my-pcs", labels[apicommon.LabelPartOfKey])
	assert.Equal(t, apicommon.LabelComponentNameResourceClaim, labels[apicommon.LabelComponentKey])
}

// --- FilterMatches (methods on API types) ---

func TestFilterMatches(t *testing.T) {
	t.Run("ResourceSharingSpec always matches", func(t *testing.T) {
		base := &grovecorev1alpha1.ResourceSharingSpec{}
		assert.True(t, base.FilterMatches())
		assert.True(t, base.FilterMatches("anything"))
	})

	t.Run("PCS no matchNames always matches", func(t *testing.T) {
		ref := &grovecorev1alpha1.PCSResourceSharingSpec{
			Filter: &grovecorev1alpha1.PCSResourceSharingFilter{ChildCliqueNames: []string{"a"}},
		}
		assert.True(t, ref.FilterMatches())
	})

	t.Run("PCS nil filter (broadcast) always matches", func(t *testing.T) {
		ref := &grovecorev1alpha1.PCSResourceSharingSpec{}
		assert.True(t, ref.FilterMatches("any-name"))
	})

	t.Run("PCS filter with cliqueNames match", func(t *testing.T) {
		ref := &grovecorev1alpha1.PCSResourceSharingSpec{
			Filter: &grovecorev1alpha1.PCSResourceSharingFilter{ChildCliqueNames: []string{"worker", "router"}},
		}
		assert.True(t, ref.FilterMatches("worker"))
		assert.True(t, ref.FilterMatches("router"))
		assert.False(t, ref.FilterMatches("coordinator"))
	})

	t.Run("PCS filter with groupNames match", func(t *testing.T) {
		ref := &grovecorev1alpha1.PCSResourceSharingSpec{
			Filter: &grovecorev1alpha1.PCSResourceSharingFilter{ChildScalingGroupNames: []string{"sga", "sgb"}},
		}
		assert.True(t, ref.FilterMatches("sga"))
		assert.False(t, ref.FilterMatches("sgc"))
	})

	t.Run("PCS filter with mixed match", func(t *testing.T) {
		ref := &grovecorev1alpha1.PCSResourceSharingSpec{
			Filter: &grovecorev1alpha1.PCSResourceSharingFilter{
				ChildCliqueNames:       []string{"worker"},
				ChildScalingGroupNames: []string{"sga"},
			},
		}
		assert.True(t, ref.FilterMatches("worker"))
		assert.True(t, ref.FilterMatches("sga"))
		assert.False(t, ref.FilterMatches("unknown"))
	})

	t.Run("PCSG filter with cliqueNames match", func(t *testing.T) {
		ref := &grovecorev1alpha1.PCSGResourceSharingSpec{
			Filter: &grovecorev1alpha1.PCSGResourceSharingFilter{ChildCliqueNames: []string{"worker"}},
		}
		assert.True(t, ref.FilterMatches("worker"))
		assert.False(t, ref.FilterMatches("router"))
	})

	t.Run("PCSG nil filter (broadcast) always matches", func(t *testing.T) {
		ref := &grovecorev1alpha1.PCSGResourceSharingSpec{}
		assert.True(t, ref.FilterMatches("any-name"))
	})
}

// --- InjectResourceClaimRefs ---

func TestInjectResourceClaimRefs(t *testing.T) {
	t.Run("AllReplicas only", func(t *testing.T) {
		podSpec := &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main"}},
		}
		resourceSharers := ResourceSharersFromPCLQ([]grovecorev1alpha1.ResourceSharingSpec{
			{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
			{Name: "per-rep", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
		})
		InjectResourceClaimRefs(podSpec, "pcs", resourceSharers, nil)

		require.Len(t, podSpec.ResourceClaims, 1)
		assert.Equal(t, "pcs-all-gpu-mps", podSpec.ResourceClaims[0].Name)
		require.Len(t, podSpec.Containers[0].Resources.Claims, 1)
	})

	t.Run("PerReplica only", func(t *testing.T) {
		podSpec := &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main"}},
		}
		resourceSharers := ResourceSharersFromPCLQ([]grovecorev1alpha1.ResourceSharingSpec{
			{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
			{Name: "per-rep", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
		})
		idx := 2
		InjectResourceClaimRefs(podSpec, "pcs", resourceSharers, &idx)

		require.Len(t, podSpec.ResourceClaims, 1)
		assert.Equal(t, "pcs-2-per-rep", podSpec.ResourceClaims[0].Name)
	})

	t.Run("with filter match", func(t *testing.T) {
		podSpec := &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main"}},
		}
		resourceSharers := ResourceSharersFromPCS([]grovecorev1alpha1.PCSResourceSharingSpec{
			{
				ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
				Filter:              &grovecorev1alpha1.PCSResourceSharingFilter{ChildCliqueNames: []string{"worker"}},
			},
		})
		InjectResourceClaimRefs(podSpec, "pcs", resourceSharers, nil, "worker")
		assert.Len(t, podSpec.ResourceClaims, 1)
	})

	t.Run("with filter no match", func(t *testing.T) {
		podSpec := &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main"}},
		}
		resourceSharers := ResourceSharersFromPCS([]grovecorev1alpha1.PCSResourceSharingSpec{
			{
				ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
				Filter:              &grovecorev1alpha1.PCSResourceSharingFilter{ChildCliqueNames: []string{"worker"}},
			},
		})
		InjectResourceClaimRefs(podSpec, "pcs", resourceSharers, nil, "coordinator")
		assert.Empty(t, podSpec.ResourceClaims)
	})

	t.Run("injects into all containers and init containers", func(t *testing.T) {
		podSpec := &corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "main"}, {Name: "sidecar"}},
			InitContainers: []corev1.Container{{Name: "init"}},
		}
		resourceSharers := ResourceSharersFromPCLQ([]grovecorev1alpha1.ResourceSharingSpec{
			{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
		})
		InjectResourceClaimRefs(podSpec, "pcs", resourceSharers, nil)

		assert.Len(t, podSpec.Containers[0].Resources.Claims, 1)
		assert.Len(t, podSpec.Containers[1].Resources.Claims, 1)
		assert.Len(t, podSpec.InitContainers[0].Resources.Claims, 1)
	})
}

// --- FindPCSGConfig ---

func TestFindPCSGConfig(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pcs"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "sga"},
					{Name: "sgb"},
				},
			},
		},
	}

	t.Run("match found", func(t *testing.T) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-sga"},
		}
		cfg := FindPCSGConfig(pcs, pcsg, 0)
		require.NotNil(t, cfg)
		assert.Equal(t, "sga", cfg.Name)
	})

	t.Run("different replica index", func(t *testing.T) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-1-sgb"},
		}
		cfg := FindPCSGConfig(pcs, pcsg, 1)
		require.NotNil(t, cfg)
		assert.Equal(t, "sgb", cfg.Name)
	})

	t.Run("no match", func(t *testing.T) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-nonexistent"},
		}
		cfg := FindPCSGConfig(pcs, pcsg, 0)
		assert.Nil(t, cfg)
	})

	t.Run("wrong replica index", func(t *testing.T) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-0-sga"},
		}
		cfg := FindPCSGConfig(pcs, pcsg, 1)
		assert.Nil(t, cfg)
	})
}

// --- FindPCSGConfigByName ---

func TestFindPCSGConfigByName(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pcs"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "sga"},
					{Name: "sgb"},
				},
			},
		},
	}

	t.Run("match found by name", func(t *testing.T) {
		cfg := FindPCSGConfigByName(pcs, "my-pcs-0-sga", 0)
		require.NotNil(t, cfg)
		assert.Equal(t, "sga", cfg.Name)
	})

	t.Run("no match", func(t *testing.T) {
		cfg := FindPCSGConfigByName(pcs, "my-pcs-0-nonexistent", 0)
		assert.Nil(t, cfg)
	})

	t.Run("consistent with FindPCSGConfig", func(t *testing.T) {
		pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs-1-sgb"},
		}
		byObj := FindPCSGConfig(pcs, pcsg, 1)
		byName := FindPCSGConfigByName(pcs, "my-pcs-1-sgb", 1)
		require.NotNil(t, byObj)
		require.NotNil(t, byName)
		assert.Equal(t, byObj.Name, byName.Name)
	})
}

// --- EnsureResourceClaim ---

func TestEnsureResourceClaim(t *testing.T) {
	scheme := newTestScheme()

	t.Run("creates RC with correct spec, labels, and owner", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		owner := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs", Namespace: "default", UID: "pcs-uid"},
		}
		spec := &resourcev1.ResourceClaimTemplateSpec{
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{{Name: "gpu"}},
				},
			},
		}
		labels := ResourceClaimLabels("my-pcs")

		err := EnsureResourceClaim(context.Background(), cl, "my-rc", "default", spec, labels, owner, scheme)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		err = cl.Get(context.Background(), types.NamespacedName{Name: "my-rc", Namespace: "default"}, rc)
		require.NoError(t, err)

		assert.Equal(t, "gpu", rc.Spec.Devices.Requests[0].Name)
		assert.Equal(t, apicommon.LabelComponentNameResourceClaim, rc.Labels[apicommon.LabelComponentKey])
		assert.Equal(t, "my-pcs", rc.Labels[apicommon.LabelPartOfKey])
		require.Len(t, rc.OwnerReferences, 1)
		assert.Equal(t, "my-pcs", rc.OwnerReferences[0].Name)
	})

	t.Run("updates only metadata on existing RC", func(t *testing.T) {
		existingRC := &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "my-rc", Namespace: "default"},
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{{Name: "original-gpu"}},
				},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingRC).Build()
		owner := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pcs", Namespace: "default", UID: "pcs-uid"},
		}
		spec := &resourcev1.ResourceClaimTemplateSpec{
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{{Name: "updated-gpu"}},
				},
			},
		}
		labels := ResourceClaimLabels("my-pcs")

		err := EnsureResourceClaim(context.Background(), cl, "my-rc", "default", spec, labels, owner, scheme)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		err = cl.Get(context.Background(), types.NamespacedName{Name: "my-rc", Namespace: "default"}, rc)
		require.NoError(t, err)
		assert.Equal(t, "original-gpu", rc.Spec.Devices.Requests[0].Name, "spec must not be mutated")
		assert.Equal(t, apicommon.LabelComponentNameResourceClaim, rc.Labels[apicommon.LabelComponentKey])
		require.Len(t, rc.OwnerReferences, 1)
		assert.Equal(t, "my-pcs", rc.OwnerReferences[0].Name)
	})
}

// --- EnsureResourceClaims ---

func TestEnsureResourceClaims(t *testing.T) {
	scheme := newTestScheme()

	templates := []grovecorev1alpha1.ResourceClaimTemplateConfig{
		{
			Name: "gpu-mps",
			TemplateSpec: resourcev1.ResourceClaimTemplateSpec{
				Spec: resourcev1.ResourceClaimSpec{
					Devices: resourcev1.DeviceClaim{
						Requests: []resourcev1.DeviceRequest{{Name: "gpu"}},
					},
				},
			},
		},
		{
			Name: "shared-mem",
			TemplateSpec: resourcev1.ResourceClaimTemplateSpec{
				Spec: resourcev1.ResourceClaimSpec{
					Devices: resourcev1.DeviceClaim{
						Requests: []resourcev1.DeviceRequest{{Name: "mem"}},
					},
				},
			},
		},
	}

	owner := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pcs", Namespace: "default", UID: "pcs-uid"},
	}
	labels := ResourceClaimLabels("my-pcs")

	t.Run("creates AllReplicas RCs only when replicaIndex is nil", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		resourceSharers := ResourceSharersFromPCLQ([]grovecorev1alpha1.ResourceSharingSpec{
			{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
			{Name: "shared-mem", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
		})

		err := EnsureResourceClaims(context.Background(), cl, "my-pcs", "default", resourceSharers, templates, labels, owner, scheme, nil)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		err = cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-all-gpu-mps", Namespace: "default"}, rc)
		require.NoError(t, err)

		err = cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-0-shared-mem", Namespace: "default"}, rc)
		assert.Error(t, err, "PerReplica RC should not be created when replicaIndex is nil")
	})

	t.Run("creates PerReplica RCs only when replicaIndex is set", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		resourceSharers := ResourceSharersFromPCLQ([]grovecorev1alpha1.ResourceSharingSpec{
			{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
			{Name: "shared-mem", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
		})

		idx := 1
		err := EnsureResourceClaims(context.Background(), cl, "my-pcs", "default", resourceSharers, templates, labels, owner, scheme, &idx)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		err = cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-1-shared-mem", Namespace: "default"}, rc)
		require.NoError(t, err)

		err = cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-all-gpu-mps", Namespace: "default"}, rc)
		assert.Error(t, err, "AllReplicas RC should not be created when replicaIndex is set")
	})
}

// --- DeletePerReplicaRCs ---

func TestDeletePerReplicaRCs(t *testing.T) {
	scheme := newTestScheme()

	t.Run("deletes only PerReplica RCs for given index", func(t *testing.T) {
		rc0 := &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "owner-0-gpu-mps", Namespace: "default"},
		}
		rc1 := &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "owner-1-gpu-mps", Namespace: "default"},
		}
		rcAll := &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "owner-all-gpu-mps", Namespace: "default"},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rc0, rc1, rcAll).Build()

		resourceSharers := ResourceSharersFromPCLQ([]grovecorev1alpha1.ResourceSharingSpec{
			{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
			{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas},
		})

		err := DeletePerReplicaRCs(context.Background(), cl, "owner", "default", resourceSharers, 1)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		assert.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "owner-0-gpu-mps", Namespace: "default"}, rc))
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{Name: "owner-1-gpu-mps", Namespace: "default"}, rc))
		assert.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "owner-all-gpu-mps", Namespace: "default"}, rc))
	})

	t.Run("tolerates already-deleted RCs", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		resourceSharers := ResourceSharersFromPCLQ([]grovecorev1alpha1.ResourceSharingSpec{
			{Name: "gpu-mps", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
		})
		err := DeletePerReplicaRCs(context.Background(), cl, "owner", "default", resourceSharers, 0)
		require.NoError(t, err)
	})
}

// --- DeleteResourceClaim ---

func TestDeleteResourceClaim(t *testing.T) {
	scheme := newTestScheme()

	t.Run("deletes existing RC", func(t *testing.T) {
		rc := &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "my-rc", Namespace: "default"},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rc).Build()
		err := DeleteResourceClaim(context.Background(), cl, "my-rc", "default")
		require.NoError(t, err)
	})

	t.Run("ignores NotFound", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		err := DeleteResourceClaim(context.Background(), cl, "nonexistent", "default")
		require.NoError(t, err)
	})
}

// --- CleanupStalePerReplicaRCs ---

func TestCleanupStalePerReplicaRCs(t *testing.T) {
	scheme := newTestScheme()
	baseLabels := ResourceClaimLabels("my-pcs")

	rcWithIndex := func(name, ns string, replicaIndex int) *resourcev1.ResourceClaim {
		l := make(map[string]string, len(baseLabels)+1)
		for k, v := range baseLabels {
			l[k] = v
		}
		l[apicommon.LabelPodCliqueSetReplicaIndex] = fmt.Sprintf("%d", replicaIndex)
		return &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: l}}
	}
	rcAllReplicas := func(name, ns string) *resourcev1.ResourceClaim {
		return &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: baseLabels}}
	}

	t.Run("deletes stale PerReplica RCs after scale-in", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			rcAllReplicas("my-pcs-all-gpu", "ns"),
			rcWithIndex("my-pcs-0-gpu", "ns", 0),
			rcWithIndex("my-pcs-1-gpu", "ns", 1),
			rcWithIndex("my-pcs-2-gpu", "ns", 2),
		).Build()

		err := CleanupStalePerReplicaRCs(context.Background(), cl, "ns", baseLabels, 2, apicommon.LabelPodCliqueSetReplicaIndex)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		assert.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-all-gpu", Namespace: "ns"}, rc))
		assert.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-0-gpu", Namespace: "ns"}, rc))
		assert.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-1-gpu", Namespace: "ns"}, rc))
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-2-gpu", Namespace: "ns"}, rc))
	})

	t.Run("handles scale-to-zero", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			rcAllReplicas("my-pcs-all-gpu", "ns"),
			rcWithIndex("my-pcs-0-gpu", "ns", 0),
			rcWithIndex("my-pcs-1-gpu", "ns", 1),
		).Build()

		err := CleanupStalePerReplicaRCs(context.Background(), cl, "ns", baseLabels, 0, apicommon.LabelPodCliqueSetReplicaIndex)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		assert.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-all-gpu", Namespace: "ns"}, rc), "AllReplicas RC should survive")
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-0-gpu", Namespace: "ns"}, rc))
		assert.Error(t, cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-1-gpu", Namespace: "ns"}, rc))
	})

	t.Run("noop when no stale RCs exist", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			rcWithIndex("my-pcs-0-gpu", "ns", 0),
		).Build()

		err := CleanupStalePerReplicaRCs(context.Background(), cl, "ns", baseLabels, 5, apicommon.LabelPodCliqueSetReplicaIndex)
		require.NoError(t, err)

		rc := &resourcev1.ResourceClaim{}
		assert.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "my-pcs-0-gpu", Namespace: "ns"}, rc))
	})
}

// Ensure ptr.To works for tests that need int pointer
var _ = ptr.To(0)
