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

package resourceclaim

import (
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGenerateResourceClaimName(t *testing.T) {
	tests := []struct {
		description    string
		rctName        string
		instanceName   string
		expectedResult string
	}{
		{
			description:    "PCLQ-scoped claim name",
			rctName:        "gpu-claim-template",
			instanceName:   "my-pcs-0-worker",
			expectedResult: "my-pcs-0-worker-gpu-claim-template",
		},
		{
			description:    "PCSG-scoped claim name",
			rctName:        "shared-gpu",
			instanceName:   "my-pcs-0-sga-1",
			expectedResult: "my-pcs-0-sga-1-shared-gpu",
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			result := GenerateResourceClaimName(tc.rctName, tc.instanceName)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}

func TestInjectResourceClaimsIntoPodSpec(t *testing.T) {
	tests := []struct {
		description               string
		podSpec                   *corev1.PodSpec
		claimNames                []string
		expectedPodResourceClaims int
		expectedContainerClaims   int
		expectedInitClaims        int
	}{
		{
			description:               "nil PodSpec does not panic",
			podSpec:                   nil,
			claimNames:                []string{"claim-1"},
			expectedPodResourceClaims: 0,
		},
		{
			description: "empty claimNames is a no-op",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main"}},
			},
			claimNames:                nil,
			expectedPodResourceClaims: 0,
			expectedContainerClaims:   0,
		},
		{
			description: "injects single claim into containers and init containers",
			podSpec: &corev1.PodSpec{
				Containers:     []corev1.Container{{Name: "main"}, {Name: "sidecar"}},
				InitContainers: []corev1.Container{{Name: "init"}},
			},
			claimNames:                []string{"gpu-claim"},
			expectedPodResourceClaims: 1,
			expectedContainerClaims:   1,
			expectedInitClaims:        1,
		},
		{
			description: "injects multiple claims",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main"}},
			},
			claimNames:                []string{"gpu-claim", "nic-claim"},
			expectedPodResourceClaims: 2,
			expectedContainerClaims:   2,
		},
		{
			description: "idempotent on duplicate injection",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main"}},
				ResourceClaims: []corev1.PodResourceClaim{
					{Name: "gpu-claim", ResourceClaimName: ptr.To("gpu-claim")},
				},
			},
			claimNames:                []string{"gpu-claim"},
			expectedPodResourceClaims: 1,
			expectedContainerClaims:   0, // already existing claim is not re-added to container
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			InjectResourceClaimsIntoPodSpec(tc.podSpec, tc.claimNames)

			if tc.podSpec == nil {
				return
			}

			assert.Len(t, tc.podSpec.ResourceClaims, tc.expectedPodResourceClaims)

			if tc.expectedContainerClaims > 0 {
				for _, container := range tc.podSpec.Containers {
					assert.Len(t, container.Resources.Claims, tc.expectedContainerClaims,
						"container %s should have %d claims", container.Name, tc.expectedContainerClaims)
				}
			}

			if tc.expectedInitClaims > 0 {
				for _, container := range tc.podSpec.InitContainers {
					assert.Len(t, container.Resources.Claims, tc.expectedInitClaims,
						"init container %s should have %d claims", container.Name, tc.expectedInitClaims)
				}
			}

			// Verify claim references point to the right names
			for _, podClaim := range tc.podSpec.ResourceClaims {
				require.NotNil(t, podClaim.ResourceClaimName,
					"PodResourceClaim %s should reference a ResourceClaimName", podClaim.Name)
				assert.Equal(t, podClaim.Name, *podClaim.ResourceClaimName)
			}
		})
	}
}

func TestCreateOrGetResourceClaim(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, resourcev1.AddToScheme(scheme))

	const (
		namespace = "test-ns"
		rctName   = "gpu-template"
		claimName = "my-pcs-0-worker-gpu-template"
	)

	rct := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rctName,
			Namespace: namespace,
		},
		Spec: resourcev1.ResourceClaimTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      map[string]string{"app": "test"},
				Annotations: map[string]string{"note": "from-template"},
			},
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{
						{
							Name:    "gpu",
							Exactly: &resourcev1.ExactDeviceRequest{DeviceClassName: "gpu.nvidia.com"},
						},
					},
				},
			},
		},
	}

	owner := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-pcs",
			Namespace: namespace,
			UID:       types.UID("owner-uid"),
		},
	}

	t.Run("creates ResourceClaim from existing template", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rct.DeepCopy(), owner).Build()

		name, err := CreateOrGetResourceClaim(context.Background(), logr.Discard(), cl, scheme, rctName, claimName, namespace, owner)
		require.NoError(t, err)
		assert.Equal(t, claimName, name)

		// Verify the claim was actually created with correct spec.
		created := &resourcev1.ResourceClaim{}
		require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: claimName, Namespace: namespace}, created))
		assert.Equal(t, rct.Spec.Labels, created.Labels)
		assert.Equal(t, rct.Spec.Annotations, created.Annotations)
		assert.Len(t, created.Spec.Devices.Requests, 1)
		assert.Equal(t, "gpu", created.Spec.Devices.Requests[0].Name)

		// Verify owner reference was set.
		require.Len(t, created.OwnerReferences, 1)
		assert.Equal(t, owner.Name, created.OwnerReferences[0].Name)
	})

	t.Run("returns existing claim without recreating", func(t *testing.T) {
		existingClaim := &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      claimName,
				Namespace: namespace,
			},
			Spec: resourcev1.ResourceClaimSpec{},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rct.DeepCopy(), existingClaim).Build()

		name, err := CreateOrGetResourceClaim(context.Background(), logr.Discard(), cl, scheme, rctName, claimName, namespace, owner)
		require.NoError(t, err)
		assert.Equal(t, claimName, name)
	})

	t.Run("returns error when template does not exist", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()

		_, err := CreateOrGetResourceClaim(context.Background(), logr.Discard(), cl, scheme, "nonexistent-template", claimName, namespace, owner)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get ResourceClaimTemplate nonexistent-template")
	})
}
