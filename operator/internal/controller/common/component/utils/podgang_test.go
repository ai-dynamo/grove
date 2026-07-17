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
	"strconv"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

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

		result, err := ListExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

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

		result, err := ListExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

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

		result, err := ListExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

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

		result, err := ListExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

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

		result, err := ListExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

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

		result, err := ListExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("returns empty when no podgangs exist", func(t *testing.T) {
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		result, err := ListExistingPodGangs(t.Context(), cl, pcsObjectMeta, namespace)

		require.NoError(t, err)
		assert.Empty(t, result)
	})
}

// TestArePodGangsReady verifies the per-batch readiness check used by both the
// coherent-update orchestrator and the PodGangMap iteration gate.
func TestArePodGangsReady(t *testing.T) {
	const namespace = "default"
	scheme := runtime.NewScheme()
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))

	mkPG := func(name string, available bool) *groveschedulerv1alpha1.PodGang {
		conditionStatus := metav1.ConditionFalse
		if available {
			conditionStatus = metav1.ConditionTrue
		}
		return &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Status: groveschedulerv1alpha1.PodGangStatus{
				Conditions: []metav1.Condition{{
					Type:               string(groveschedulerv1alpha1.PodGangConditionTypeReady),
					Status:             conditionStatus,
					LastTransitionTime: metav1.Now(),
					Reason:             "Test",
				}},
			},
		}
	}

	t.Run("empty list returns true", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		ok, err := ArePodGangsReady(context.Background(), cl, namespace, nil)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("single Available PodGang returns true", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mkPG("pg-0", true)).Build()
		ok, err := ArePodGangsReady(context.Background(), cl, namespace, []string{"pg-0"})
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("single not-Available PodGang returns false", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mkPG("pg-0", false)).Build()
		ok, err := ArePodGangsReady(context.Background(), cl, namespace, []string{"pg-0"})
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("missing PodGang returns false (treated as not yet Available)", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		ok, err := ArePodGangsReady(context.Background(), cl, namespace, []string{"pg-missing"})
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("all of multiple PodGangs Available returns true", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mkPG("pg-0", true), mkPG("pg-1", true)).Build()
		ok, err := ArePodGangsReady(context.Background(), cl, namespace, []string{"pg-0", "pg-1"})
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("any one of multiple PodGangs not Available returns false", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mkPG("pg-0", true), mkPG("pg-1", false)).Build()
		ok, err := ArePodGangsReady(context.Background(), cl, namespace, []string{"pg-0", "pg-1"})
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("PodGang with no Available condition returns false", func(t *testing.T) {
		pgNoCondition := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{Name: "pg-0", Namespace: namespace},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pgNoCondition).Build()
		ok, err := ArePodGangsReady(context.Background(), cl, namespace, []string{"pg-0"})
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

// TestAllPodGangsAtEpochEverScheduled verifies the monotonic "ever scheduled"
// check used by the pod component's scheduling-gate removal path.
func TestAllPodGangsAtEpochEverScheduled(t *testing.T) {
	const (
		namespace              = "default"
		pcsName                = "test-pcs"
		defaultPCSReplicaIndex = int32(0)
		defaultEpoch           = "1700000000000000000"
	)
	scheme := groveclientscheme.Scheme

	type podGangSpec struct {
		name            string
		epoch           string
		pcsReplicaIndex int32
		everScheduled   bool
	}

	mkPG := func(s podGangSpec) *groveschedulerv1alpha1.PodGang {
		return newTestPodGangForEpoch(s.name, namespace, pcsName, s.pcsReplicaIndex, s.epoch, s.everScheduled, false)
	}

	tests := []struct {
		name  string
		specs []podGangSpec
		want  bool
	}{
		{
			name:  "no matching PodGangs returns false",
			specs: nil,
			want:  false,
		},
		{
			name:  "single PodGang with LastScheduled set returns true",
			specs: []podGangSpec{{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everScheduled: true}},
			want:  true,
		},
		{
			name:  "single PodGang with LastScheduled nil returns false",
			specs: []podGangSpec{{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everScheduled: false}},
			want:  false,
		},
		{
			name: "all of multiple PodGangs with LastScheduled set returns true",
			specs: []podGangSpec{
				{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everScheduled: true},
				{name: "pg-1", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everScheduled: true},
			},
			want: true,
		},
		{
			name: "any one of multiple PodGangs with LastScheduled nil returns false",
			specs: []podGangSpec{
				{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everScheduled: true},
				{name: "pg-1", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everScheduled: false},
			},
			want: false,
		},
		{
			name: "PodGang at a different epoch is filtered out even if not scheduled",
			specs: []podGangSpec{
				{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everScheduled: true},
				{name: "pg-other-epoch", epoch: "1700000000000000001", pcsReplicaIndex: defaultPCSReplicaIndex, everScheduled: false},
			},
			want: true,
		},
		{
			name: "PodGang at a different replica index is filtered out even if not scheduled",
			specs: []podGangSpec{
				{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everScheduled: true},
				{name: "pg-other-replica", epoch: defaultEpoch, pcsReplicaIndex: 1, everScheduled: false},
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			for _, s := range tc.specs {
				builder = builder.WithObjects(mkPG(s))
			}
			cl := builder.Build()
			ok, err := AllPodGangsAtEpochEverScheduled(t.Context(), cl, namespace, pcsName, defaultPCSReplicaIndex, defaultEpoch)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ok)
		})
	}
}

// TestAllPodGangsAtEpochEverReady verifies the monotonic "ever ready"
// check used by the orchestrator's sub-step advance precondition.
func TestAllPodGangsAtEpochEverReady(t *testing.T) {
	const (
		namespace              = "default"
		pcsName                = "test-pcs"
		defaultPCSReplicaIndex = int32(0)
		defaultEpoch           = "1700000000000000000"
	)
	scheme := groveclientscheme.Scheme

	type podGangSpec struct {
		name            string
		epoch           string
		pcsReplicaIndex int32
		everReady       bool
	}

	mkPG := func(s podGangSpec) *groveschedulerv1alpha1.PodGang {
		return newTestPodGangForEpoch(s.name, namespace, pcsName, s.pcsReplicaIndex, s.epoch, false, s.everReady)
	}

	tests := []struct {
		name  string
		specs []podGangSpec
		want  bool
	}{
		{
			name:  "no matching PodGangs returns false",
			specs: nil,
			want:  false,
		},
		{
			name:  "single PodGang with LastReady set returns true",
			specs: []podGangSpec{{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everReady: true}},
			want:  true,
		},
		{
			name:  "single PodGang with LastReady nil returns false",
			specs: []podGangSpec{{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everReady: false}},
			want:  false,
		},
		{
			name: "all of multiple PodGangs with LastReady set returns true",
			specs: []podGangSpec{
				{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everReady: true},
				{name: "pg-1", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everReady: true},
			},
			want: true,
		},
		{
			name: "any one of multiple PodGangs with LastReady nil returns false",
			specs: []podGangSpec{
				{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everReady: true},
				{name: "pg-1", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everReady: false},
			},
			want: false,
		},
		{
			name: "PodGang at a different epoch is filtered out even if not ready",
			specs: []podGangSpec{
				{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everReady: true},
				{name: "pg-other-epoch", epoch: "1700000000000000001", pcsReplicaIndex: defaultPCSReplicaIndex, everReady: false},
			},
			want: true,
		},
		{
			name: "PodGang at a different replica index is filtered out even if not ready",
			specs: []podGangSpec{
				{name: "pg-0", epoch: defaultEpoch, pcsReplicaIndex: defaultPCSReplicaIndex, everReady: true},
				{name: "pg-other-replica", epoch: defaultEpoch, pcsReplicaIndex: 1, everReady: false},
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			for _, s := range tc.specs {
				builder = builder.WithObjects(mkPG(s))
			}
			cl := builder.Build()
			ok, err := AllPodGangsAtEpochEverReady(t.Context(), cl, namespace, pcsName, defaultPCSReplicaIndex, defaultEpoch)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ok)
		})
	}
}

// newTestPodGangForEpoch builds a PodGang stamped with the three labels used
// by the AllPodGangsAtEpoch* filter helpers (part-of, pcs-replica-index, epoch).
// Optionally sets Status.LastScheduled and/or Status.LastReady to the current
// time when the respective flags are true.
func newTestPodGangForEpoch(name, namespace, pcsName string, pcsReplicaIndex int32, epoch string, scheduled, ready bool) *groveschedulerv1alpha1.PodGang {
	b := testutils.NewPodGangBuilder(name, namespace).
		WithLabel(apicommon.LabelPartOfKey, pcsName).
		WithLabel(apicommon.LabelPodCliqueSetReplicaIndex, strconv.Itoa(int(pcsReplicaIndex))).
		WithLabel(apicommon.LabelEpoch, epoch)
	if scheduled {
		b = b.WithStatusLastScheduled(metav1.Now())
	}
	if ready {
		b = b.WithStatusLastReady(metav1.Now())
	}
	return b.Build()
}
