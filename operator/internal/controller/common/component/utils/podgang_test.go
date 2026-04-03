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

package utils

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestGetPodGangSelectorLabels tests generating label selector for PodGangs.
func TestGetPodGangSelectorLabels(t *testing.T) {
	// Test with basic PodCliqueSet metadata
	t.Run("basic metadata", func(t *testing.T) {
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		}

		labels := GetPodGangSelectorLabels(pcsObjMeta)

		// Should include part-of label
		assert.Equal(t, "test-pcs", labels[apicommon.LabelPartOfKey])
		// Should include component label
		assert.Equal(t, apicommon.LabelComponentNamePodGang, labels[apicommon.LabelComponentKey])
		// Should include managed-by label
		assert.Equal(t, apicommon.LabelManagedByValue, labels[apicommon.LabelManagedByKey])
	})

	// Test with different PodCliqueSet name
	t.Run("different pcs name", func(t *testing.T) {
		pcsObjMeta := metav1.ObjectMeta{
			Name:      "my-workload",
			Namespace: "production",
		}

		labels := GetPodGangSelectorLabels(pcsObjMeta)

		assert.Equal(t, "my-workload", labels[apicommon.LabelPartOfKey])
		assert.Equal(t, apicommon.LabelComponentNamePodGang, labels[apicommon.LabelComponentKey])
	})
}

// TestGetPodGang tests fetching a PodGang by name and namespace.
func TestGetPodGang(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = groveschedulerv1alpha1.AddToScheme(scheme)

	// Test successful retrieval
	t.Run("successful retrieval", func(t *testing.T) {
		podGang := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-podgang",
				Namespace: "default",
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(podGang).
			Build()

		result, err := GetPodGang(context.Background(), cl, "test-podgang", "default")

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "test-podgang", result.Name)
		assert.Equal(t, "default", result.Namespace)
	})

	// Test not found error
	t.Run("podgang not found", func(t *testing.T) {
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		result, err := GetPodGang(context.Background(), cl, "nonexistent", "default")

		assert.Error(t, err)
		assert.Nil(t, result)
	})

	// Test in different namespace
	t.Run("different namespace", func(t *testing.T) {
		podGang := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "prod-podgang",
				Namespace: "production",
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(podGang).
			Build()

		result, err := GetPodGang(context.Background(), cl, "prod-podgang", "production")

		require.NoError(t, err)
		assert.Equal(t, "production", result.Namespace)
	})

	// Test wrong namespace
	t.Run("wrong namespace", func(t *testing.T) {
		podGang := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-podgang",
				Namespace: "default",
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(podGang).
			Build()

		// Try to fetch from wrong namespace
		result, err := GetPodGang(context.Background(), cl, "test-podgang", "production")

		assert.Error(t, err)
		assert.Nil(t, result)
	})
}

// TestGetExistingPodGangs tests fetching PodGangs using server-side label filtering.
func TestGetExistingPodGangs(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = groveschedulerv1alpha1.AddToScheme(scheme)

	pcsName := "test-pcs"
	namespace := "default"
	pcsObjectMeta := metav1.ObjectMeta{
		Name:      pcsName,
		Namespace: namespace,
	}
	matchingLabels := GetPodGangSelectorLabels(pcsObjectMeta)

	t.Run("returns matching podgangs", func(t *testing.T) {
		managed := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pg-1",
				Namespace: namespace,
				Labels:    matchingLabels,
			},
		}
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(managed).
			Build()

		result, err := GetExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, "pg-1", result[0].Name)
	})

	t.Run("returns multiple matching podgangs", func(t *testing.T) {
		pg1 := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pg-1",
				Namespace: namespace,
				Labels:    matchingLabels,
			},
		}
		pg2 := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pg-2",
				Namespace: namespace,
				Labels:    matchingLabels,
			},
		}
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pg1, pg2).
			Build()

		result, err := GetExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		assert.Len(t, result, 2)
	})

	t.Run("excludes podgangs belonging to a different PodCliqueSet", func(t *testing.T) {
		ownedPG := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pg-owned",
				Namespace: namespace,
				Labels:    matchingLabels,
			},
		}
		otherLabels := GetPodGangSelectorLabels(metav1.ObjectMeta{Name: "other-pcs"})
		otherPG := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pg-other",
				Namespace: namespace,
				Labels:    otherLabels,
			},
		}
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(ownedPG, otherPG).
			Build()

		result, err := GetExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, "pg-owned", result[0].Name)
	})

	t.Run("excludes podgangs without managed-by label", func(t *testing.T) {
		unmanagedPG := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pg-unmanaged",
				Namespace: namespace,
				Labels: map[string]string{
					apicommon.LabelPartOfKey:    pcsName,
					apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
				},
			},
		}
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(unmanagedPG).
			Build()

		result, err := GetExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("excludes podgangs with wrong component label", func(t *testing.T) {
		wrongComponentPG := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pg-wrong-component",
				Namespace: namespace,
				Labels: map[string]string{
					apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
					apicommon.LabelPartOfKey:    pcsName,
					apicommon.LabelComponentKey: "something-else",
				},
			},
		}
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(wrongComponentPG).
			Build()

		result, err := GetExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("excludes podgangs in a different namespace", func(t *testing.T) {
		pgOtherNS := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pg-other-ns",
				Namespace: "other-namespace",
				Labels:    matchingLabels,
			},
		}
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pgOtherNS).
			Build()

		result, err := GetExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("returns empty when no podgangs exist", func(t *testing.T) {
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		result, err := GetExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		assert.Empty(t, result)
	})
}
