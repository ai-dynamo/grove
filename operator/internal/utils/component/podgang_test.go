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

package component

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	const pcsUID = types.UID("pcs-uid")
	pcsObjectMeta := metav1.ObjectMeta{
		Name:      pcsName,
		Namespace: namespace,
		UID:       pcsUID,
	}
	matchingLabels := GetPodGangSelectorLabels(pcsObjectMeta)

	t.Run("returns matching podgangs", func(t *testing.T) {
		managed := testutils.NewPodGangBuilder("pg-1", namespace).
			WithLabels(matchingLabels).
			WithOwnerReference(constants.KindPodCliqueSet, pcsName, pcsUID).
			Build()
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
		pg1 := testutils.NewPodGangBuilder("pg-1", namespace).
			WithLabels(matchingLabels).
			WithOwnerReference(constants.KindPodCliqueSet, pcsName, pcsUID).
			Build()
		pg2 := testutils.NewPodGangBuilder("pg-2", namespace).
			WithLabels(matchingLabels).
			WithOwnerReference(constants.KindPodCliqueSet, pcsName, pcsUID).
			Build()
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pg1, pg2).
			Build()

		result, err := GetExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		assert.Len(t, result, 2)
	})

	t.Run("excludes podgangs belonging to a different PodCliqueSet", func(t *testing.T) {
		ownedPG := testutils.NewPodGangBuilder("pg-owned", namespace).
			WithLabels(matchingLabels).
			WithOwnerReference(constants.KindPodCliqueSet, pcsName, pcsUID).
			Build()
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

	t.Run("excludes podgangs controlled by a same-named PodCliqueSet of a different UID", func(t *testing.T) {
		// A PodCliqueSet deleted and recreated with the same name reuses the name labels, so a
		// leftover PodGang from the old instance still matches. It is controlled by the old UID and
		// must be excluded so its stale counts and epochs do not enter the new PodGangMap.
		currentPG := testutils.NewPodGangBuilder("pg-current", namespace).
			WithLabels(matchingLabels).
			WithOwnerReference(constants.KindPodCliqueSet, pcsName, pcsUID).
			Build()
		stalePG := testutils.NewPodGangBuilder("pg-stale", namespace).
			WithLabels(matchingLabels).
			WithOwnerReference(constants.KindPodCliqueSet, pcsName, types.UID("old-pcs-uid")).
			Build()
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(currentPG, stalePG).
			Build()

		result, err := GetExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Equal(t, "pg-current", result[0].Name)
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

// TestAllPodGangsAtEpochEverScheduled verifies an epoch counts as scheduled only when every PodGang
// carrying it has LastScheduled set, and an epoch with no PodGangs is not scheduled.
func TestAllPodGangsAtEpochEverScheduled(t *testing.T) {
	const (
		pcsName   = "test-pcs"
		namespace = "default"
		epoch     = "1000"
	)
	pcsObjectKey := client.ObjectKey{Namespace: namespace, Name: pcsName}

	podGangAtEpoch := func(name string, scheduled bool) *groveschedulerv1alpha1.PodGang {
		builder := testutils.NewPodGangBuilder(name, namespace).
			WithLabels(map[string]string{
				apicommon.LabelPartOfKey:                pcsName,
				apicommon.LabelPodCliqueSetReplicaIndex: "0",
				apicommon.LabelEpoch:                    epoch,
			})
		if scheduled {
			builder = builder.WithLastScheduled()
		}
		return builder.Build()
	}

	tests := []struct {
		name     string
		podGangs []*groveschedulerv1alpha1.PodGang
		expected bool
	}{
		{"epoch with no PodGangs is not scheduled", nil, false},
		{"epoch is scheduled when all its PodGangs are scheduled", []*groveschedulerv1alpha1.PodGang{podGangAtEpoch("pg-0", true), podGangAtEpoch("pg-1", true)}, true},
		{"epoch is not scheduled when any of its PodGangs is unscheduled", []*groveschedulerv1alpha1.PodGang{podGangAtEpoch("pg-0", true), podGangAtEpoch("pg-1", false)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			builder := testutils.NewTestClientBuilder()
			for _, pg := range tc.podGangs {
				builder = builder.WithObjects(pg)
			}
			cl := builder.Build()

			actual, err := AllPodGangsAtEpochEverScheduled(t.Context(), cl, pcsObjectKey, 0, epoch)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, actual)
		})
	}
}
