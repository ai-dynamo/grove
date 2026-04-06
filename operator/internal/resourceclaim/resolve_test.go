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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = resourcev1.AddToScheme(scheme)
	return scheme
}

func TestResolveTemplateSpec_InternalMatch(t *testing.T) {
	scheme := newTestScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	ref := &grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps"}
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
	}

	spec, err := ResolveTemplateSpec(context.Background(), cl, ref, templates, "default")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Len(t, spec.Spec.Devices.Requests, 1)
	assert.Equal(t, "gpu", spec.Spec.Devices.Requests[0].Name)
}

func TestResolveTemplateSpec_InternalShadowsExternal(t *testing.T) {
	scheme := newTestScheme()

	externalRCT := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-mps", Namespace: "default"},
		Spec: resourcev1.ResourceClaimTemplateSpec{
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{{Name: "external-gpu"}},
				},
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(externalRCT).Build()

	ref := &grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps"}
	internalTemplates := []grovecorev1alpha1.ResourceClaimTemplateConfig{
		{
			Name: "gpu-mps",
			TemplateSpec: resourcev1.ResourceClaimTemplateSpec{
				Spec: resourcev1.ResourceClaimSpec{
					Devices: resourcev1.DeviceClaim{
						Requests: []resourcev1.DeviceRequest{{Name: "internal-gpu"}},
					},
				},
			},
		},
	}

	spec, err := ResolveTemplateSpec(context.Background(), cl, ref, internalTemplates, "default")
	require.NoError(t, err)
	assert.Equal(t, "internal-gpu", spec.Spec.Devices.Requests[0].Name)
}

func TestResolveTemplateSpec_ExternalFallback(t *testing.T) {
	scheme := newTestScheme()

	externalRCT := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-mps", Namespace: "default"},
		Spec: resourcev1.ResourceClaimTemplateSpec{
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{{Name: "external-gpu"}},
				},
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(externalRCT).Build()

	ref := &grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps"}
	emptyInternal := []grovecorev1alpha1.ResourceClaimTemplateConfig{}

	spec, err := ResolveTemplateSpec(context.Background(), cl, ref, emptyInternal, "default")
	require.NoError(t, err)
	assert.Equal(t, "external-gpu", spec.Spec.Devices.Requests[0].Name)
}

func TestResolveTemplateSpec_ExternalWithExplicitNamespace(t *testing.T) {
	scheme := newTestScheme()

	externalRCT := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-mps", Namespace: "other-ns"},
		Spec: resourcev1.ResourceClaimTemplateSpec{
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{{Name: "cross-ns-gpu"}},
				},
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(externalRCT).Build()

	ref := &grovecorev1alpha1.ResourceSharingSpec{Name: "gpu-mps", Namespace: "other-ns"}
	emptyInternal := []grovecorev1alpha1.ResourceClaimTemplateConfig{}

	spec, err := ResolveTemplateSpec(context.Background(), cl, ref, emptyInternal, "default")
	require.NoError(t, err)
	assert.Equal(t, "cross-ns-gpu", spec.Spec.Devices.Requests[0].Name)
}

func TestResolveTemplateSpec_NotFound(t *testing.T) {
	scheme := newTestScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	ref := &grovecorev1alpha1.ResourceSharingSpec{Name: "nonexistent"}
	emptyInternal := []grovecorev1alpha1.ResourceClaimTemplateConfig{}

	spec, err := ResolveTemplateSpec(context.Background(), cl, ref, emptyInternal, "default")
	assert.Error(t, err)
	assert.Nil(t, spec)
	assert.Contains(t, err.Error(), "nonexistent")
}
