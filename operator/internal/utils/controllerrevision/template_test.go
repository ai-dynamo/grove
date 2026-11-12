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

package controllerrevision

import (
	"context"
	"testing"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
)

func TestCliqueTemplatesEqual(t *testing.T) {
	base := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		WithPodCliqueParameters("leader", 1, nil).
		Build().Spec.Template

	tests := []struct {
		name     string
		mutate   func(template *grovecorev1beta1.PodCliqueSetTemplateSpec)
		expected bool
	}{
		{name: "identical", mutate: func(*grovecorev1beta1.PodCliqueSetTemplateSpec) {}, expected: true},
		{
			name: "changed image",
			mutate: func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
				template.Cliques[0].Spec.PodSpec.Containers[0].Image = "other"
			},
			expected: false,
		},
		{
			name: "changed labels",
			mutate: func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
				template.Cliques[0].Labels = map[string]string{"k": "v"}
			},
			expected: false,
		},
		{
			name: "changed annotations",
			mutate: func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
				template.Cliques[0].Annotations = map[string]string{"k": "v"}
			},
			expected: false,
		},
		{
			name:     "changed priority class",
			mutate:   func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) { template.PriorityClassName = "high" },
			expected: false,
		},
		{
			name:     "changed replicas are ignored",
			mutate:   func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) { template.Cliques[0].Spec.Replicas = 7 },
			expected: true,
		},
		{
			name: "changed other clique is ignored",
			mutate: func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
				template.Cliques[1].Spec.PodSpec.Containers[0].Image = "other"
			},
			expected: true,
		},
		{
			name: "empty labels equal missing labels",
			mutate: func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
				template.Cliques[0].Labels = map[string]string{}
				template.Cliques[0].Spec.PodSpec.Tolerations = []corev1.Toleration{}
			},
			expected: true,
		},
		{
			name: "reordered cliques are equal",
			mutate: func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
				template.Cliques[0], template.Cliques[1] = template.Cliques[1], template.Cliques[0]
			},
			expected: true,
		},
		{
			name: "missing clique",
			mutate: func(template *grovecorev1beta1.PodCliqueSetTemplateSpec) {
				template.Cliques = template.Cliques[1:]
			},
			expected: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			changed := base.DeepCopy()
			tc.mutate(changed)
			assert.Equal(t, tc.expected, CliqueTemplatesEqual(&base, changed, base.Cliques[0].Name))
		})
	}
	assert.False(t, CliqueTemplatesEqual(nil, &base, base.Cliques[0].Name))
}

func TestPodTemplatesEqual(t *testing.T) {
	base := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		WithPodCliqueParameters("leader", 1, nil).
		Build().Spec.Template

	replicasOnly := base.DeepCopy()
	replicasOnly.Cliques[1].Spec.Replicas = 3
	assert.True(t, PodTemplatesEqual(&base, replicasOnly))

	imageChanged := base.DeepCopy()
	imageChanged.Cliques[1].Spec.PodSpec.Containers[0].Image = "other"
	assert.False(t, PodTemplatesEqual(&base, imageChanged))

	fewerCliques := base.DeepCopy()
	fewerCliques.Cliques = fewerCliques.Cliques[:1]
	assert.False(t, PodTemplatesEqual(&base, fewerCliques))
}

func TestGetTemplate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	data, err := EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)
	newRevision := func(name string, uid types.UID, owner metav1.Object) *appsv1.ControllerRevision {
		return &appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: pcs.Namespace,
				UID:       uid,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(owner,
					grovecorev1beta1.SchemeGroupVersion.WithKind(apiconstants.KindPodCliqueSet))},
			},
			Data: runtime.RawExtension{Raw: data},
		}
	}
	otherPCS := pcs.DeepCopy()
	otherPCS.UID = uuid.NewUUID()
	cl := testutils.SetupFakeClient(
		newRevision("owned", uuid.NewUUID(), pcs),
		newRevision("foreign", uuid.NewUUID(), otherPCS),
	)

	t.Run("decodes an owned revision and memoizes it", func(t *testing.T) {
		first, err := GetTemplate(context.Background(), cl, pcs, "owned")
		require.NoError(t, err)
		assert.Equal(t, *NormalizeTemplate(&pcs.Spec.Template), *first)
		second, err := GetTemplate(context.Background(), cl, pcs, "owned")
		require.NoError(t, err)
		assert.Same(t, first, second)
	})

	t.Run("revision of another PodCliqueSet is not found", func(t *testing.T) {
		_, err := GetTemplate(context.Background(), cl, pcs, "foreign")
		assert.True(t, apierrors.IsNotFound(err))
	})

	t.Run("missing revision is not found", func(t *testing.T) {
		_, err := GetTemplate(context.Background(), cl, pcs, "missing")
		assert.True(t, apierrors.IsNotFound(err))
	})
}
