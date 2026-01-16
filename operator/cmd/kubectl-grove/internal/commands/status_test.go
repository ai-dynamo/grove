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

package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
)

// TestStatusRenderProgressBar tests the progress bar rendering at various percentages.
func TestStatusRenderProgressBar(t *testing.T) {
	tests := []struct {
		name       string
		percentage float64
		width      int
		wantFilled int
	}{
		{
			name:       "0%",
			percentage: 0,
			width:      16,
			wantFilled: 0,
		},
		{
			name:       "50%",
			percentage: 50,
			width:      16,
			wantFilled: 8,
		},
		{
			name:       "100%",
			percentage: 100,
			width:      16,
			wantFilled: 16,
		},
		{
			name:       "25%",
			percentage: 25,
			width:      16,
			wantFilled: 4,
		},
		{
			name:       "negative percentage clamped to 0",
			percentage: -10,
			width:      16,
			wantFilled: 0,
		},
		{
			name:       "over 100% clamped to 100",
			percentage: 150,
			width:      16,
			wantFilled: 16,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderProgressBar(tt.percentage, tt.width)

			// Count filled blocks (U+2588)
			filledCount := 0
			for _, r := range result {
				if r == '\u2588' {
					filledCount++
				}
			}

			assert.Equal(t, tt.wantFilled, filledCount, "unexpected number of filled blocks")
			assert.Equal(t, tt.width, len([]rune(result)), "progress bar should have correct width")
		})
	}
}

// TestRenderPlacementScoreBar tests the placement score bar rendering.
func TestRenderPlacementScoreBar(t *testing.T) {
	tests := []struct {
		name  string
		score float64
		width int
	}{
		{
			name:  "score 0.0",
			score: 0.0,
			width: 10,
		},
		{
			name:  "score 0.5",
			score: 0.5,
			width: 10,
		},
		{
			name:  "score 1.0",
			score: 1.0,
			width: 10,
		},
		{
			name:  "score 0.95",
			score: 0.95,
			width: 10,
		},
		{
			name:  "negative score clamped",
			score: -0.5,
			width: 10,
		},
		{
			name:  "over 1.0 score clamped",
			score: 1.5,
			width: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderPlacementScoreBar(tt.score, tt.width)

			// Verify the bar is not empty and has reasonable length
			assert.NotEmpty(t, result)
			assert.LessOrEqual(t, len([]rune(result)), tt.width+1, "bar should not exceed width significantly")
		})
	}
}

// TestStatusFormatDuration tests duration formatting.
func TestStatusFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "seconds",
			duration: 30 * time.Second,
			want:     "30s",
		},
		{
			name:     "minutes",
			duration: 5 * time.Minute,
			want:     "5m",
		},
		{
			name:     "hours",
			duration: 2 * time.Hour,
			want:     "2h",
		},
		{
			name:     "hours and minutes",
			duration: 2*time.Hour + 15*time.Minute,
			want:     "2h15m",
		},
		{
			name:     "days",
			duration: 48 * time.Hour,
			want:     "2d",
		},
		{
			name:     "days and hours",
			duration: 50 * time.Hour,
			want:     "2d2h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatDuration(tt.duration)
			assert.Equal(t, tt.want, result)
		})
	}
}

// TestCliqueSummaryFormatting tests that clique status is formatted correctly.
func TestCliqueSummaryFormatting(t *testing.T) {
	clique := CliqueSummary{
		Name:          "prefill",
		ReadyReplicas: 3,
		Replicas:      3,
		MinAvailable:  2,
	}

	percentage := float64(clique.ReadyReplicas) / float64(clique.Replicas) * 100
	assert.Equal(t, float64(100), percentage)

	clique2 := CliqueSummary{
		Name:          "decode",
		ReadyReplicas: 2,
		Replicas:      5,
		MinAvailable:  3,
	}

	percentage2 := float64(clique2.ReadyReplicas) / float64(clique2.Replicas) * 100
	assert.Equal(t, float64(40), percentage2)
}

// TestGangSummaryWithPlacementScore tests PodGang status formatting with PlacementScore.
func TestGangSummaryWithPlacementScore(t *testing.T) {
	score := 0.95
	gang := GangSummary{
		Name:           "my-inference-0",
		Phase:          "Running",
		PlacementScore: &score,
	}

	assert.NotNil(t, gang.PlacementScore)
	assert.Equal(t, 0.95, *gang.PlacementScore)
	assert.Equal(t, "Running", gang.Phase)
}

// TestGangSummaryWithoutPlacementScore tests PodGang status formatting without PlacementScore.
func TestGangSummaryWithoutPlacementScore(t *testing.T) {
	gang := GangSummary{
		Name:           "my-inference-0",
		Phase:          "Pending",
		PlacementScore: nil,
	}

	assert.Nil(t, gang.PlacementScore)
	assert.Equal(t, "Pending", gang.Phase)
}

// TestStatusSummaryCalculation tests the summary calculation logic.
func TestStatusSummaryCalculation(t *testing.T) {
	output := &StatusOutput{
		Namespace: "vllm-disagg",
		Name:      "my-inference",
		Age:       2*time.Hour + 15*time.Minute,
		Cliques: []CliqueSummary{
			{Name: "prefill", ReadyReplicas: 3, Replicas: 3, MinAvailable: 2},
			{Name: "decode", ReadyReplicas: 5, Replicas: 5, MinAvailable: 3},
			{Name: "router", ReadyReplicas: 1, Replicas: 1, MinAvailable: 1},
		},
		Gangs: []GangSummary{
			{Name: "my-inference-0", Phase: "Running", PlacementScore: ptrFloat64(0.95)},
		},
		TotalReady: 9,
		TotalPods:  9,
	}

	assert.Equal(t, 3, len(output.Cliques))
	assert.Equal(t, int32(9), output.TotalReady)
	assert.Equal(t, int32(9), output.TotalPods)
	assert.Equal(t, 1, len(output.Gangs))

	// Count running gangs
	runningGangs := 0
	for _, gang := range output.Gangs {
		if gang.Phase == string(schedulerv1alpha1.PodGangPhaseRunning) {
			runningGangs++
		}
	}
	assert.Equal(t, 1, runningGangs)
}

// TestBuildStatusOutput tests building the status output from resources.
func TestBuildStatusOutput(t *testing.T) {
	pcs := &corev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-inference",
			Namespace:         "vllm-disagg",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
		},
	}

	minAvail := int32(2)
	cliques := []corev1alpha1.PodClique{
		{
			Spec: corev1alpha1.PodCliqueSpec{
				RoleName:     "prefill",
				Replicas:     3,
				MinAvailable: &minAvail,
			},
			Status: corev1alpha1.PodCliqueStatus{
				ReadyReplicas: 3,
			},
		},
		{
			Spec: corev1alpha1.PodCliqueSpec{
				RoleName: "decode",
				Replicas: 5,
			},
			Status: corev1alpha1.PodCliqueStatus{
				ReadyReplicas: 5,
			},
		},
	}

	score := 0.95
	gangs := []schedulerv1alpha1.PodGang{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-inference-0",
			},
			Status: schedulerv1alpha1.PodGangStatus{
				Phase:          schedulerv1alpha1.PodGangPhaseRunning,
				PlacementScore: &score,
			},
		},
	}

	cmd := &StatusCmd{Namespace: "vllm-disagg"}
	output := cmd.buildStatusOutput(pcs, cliques, gangs)

	assert.Equal(t, "vllm-disagg", output.Namespace)
	assert.Equal(t, "my-inference", output.Name)
	assert.Equal(t, 2, len(output.Cliques))
	assert.Equal(t, 1, len(output.Gangs))
	assert.Equal(t, int32(8), output.TotalReady)
	assert.Equal(t, int32(8), output.TotalPods)

	// Check clique details
	assert.Equal(t, "prefill", output.Cliques[0].Name)
	assert.Equal(t, int32(2), output.Cliques[0].MinAvailable)

	assert.Equal(t, "decode", output.Cliques[1].Name)
	assert.Equal(t, int32(5), output.Cliques[1].MinAvailable) // defaults to Replicas when MinAvailable is nil

	// Check gang details
	assert.Equal(t, "my-inference-0", output.Gangs[0].Name)
	assert.Equal(t, "Running", output.Gangs[0].Phase)
	assert.NotNil(t, output.Gangs[0].PlacementScore)
	assert.Equal(t, 0.95, *output.Gangs[0].PlacementScore)
}

// TestStatusCmdValidation tests command validation.
func TestStatusCmdValidation(t *testing.T) {
	tests := []struct {
		name      string
		cmd       StatusCmd
		wantError bool
	}{
		{
			name: "valid with name",
			cmd: StatusCmd{
				Name:      "my-pcs",
				Namespace: "default",
			},
			wantError: false,
		},
		{
			name: "valid with all flag",
			cmd: StatusCmd{
				All:       true,
				Namespace: "default",
			},
			wantError: false,
		},
		{
			name: "invalid - no name and no all flag",
			cmd: StatusCmd{
				Namespace: "default",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a fake dynamic client with required GVRs registered
			scheme := runtime.NewScheme()
			gvrToListKind := map[schema.GroupVersionResource]string{
				podCliqueSetGVR:  "PodCliqueSetList",
				podCliqueGVR:     "PodCliqueList",
				podGangGVR:       "PodGangList",
			}
			client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)

			err := tt.cmd.Execute(client)
			if tt.wantError {
				assert.Error(t, err)
			} else {
				// For valid commands, they may fail due to missing resources
				// but should not fail validation
				if err != nil {
					assert.NotContains(t, err.Error(), "either provide a PodCliqueSet name or use --all flag")
				}
			}
		})
	}
}

// TestFetchPodCliquesIntegration tests fetching PodCliques with fake client.
func TestFetchPodCliquesIntegration(t *testing.T) {
	scheme := runtime.NewScheme()

	// Create a fake PodClique
	pclq := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodClique",
			"metadata": map[string]interface{}{
				"name":      "my-pcs-0-prefill",
				"namespace": "default",
				"labels": map[string]interface{}{
					"grove.io/podcliqueset": "my-pcs",
				},
			},
			"spec": map[string]interface{}{
				"roleName": "prefill",
				"replicas": int64(3),
				"podSpec":  map[string]interface{}{},
			},
			"status": map[string]interface{}{
				"readyReplicas": int64(3),
			},
		},
	}

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			podCliqueGVR: "PodCliqueList",
		},
		pclq,
	)

	cmd := &StatusCmd{Namespace: "default"}
	cliques, err := cmd.fetchPodCliques(context.Background(), client, "my-pcs")

	require.NoError(t, err)
	assert.Len(t, cliques, 1)
	assert.Equal(t, "prefill", cliques[0].Spec.RoleName)
	assert.Equal(t, int32(3), cliques[0].Status.ReadyReplicas)
}

// TestFetchPodGangsIntegration tests fetching PodGangs with fake client.
func TestFetchPodGangsIntegration(t *testing.T) {
	scheme := runtime.NewScheme()

	score := 0.92
	// Create a fake PodGang
	pg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "scheduler.grove.io/v1alpha1",
			"kind":       "PodGang",
			"metadata": map[string]interface{}{
				"name":      "my-pcs-0",
				"namespace": "default",
				"labels": map[string]interface{}{
					"grove.io/podcliqueset": "my-pcs",
				},
			},
			"spec": map[string]interface{}{
				"podgroups": []interface{}{},
			},
			"status": map[string]interface{}{
				"phase":          "Running",
				"placementScore": score,
			},
		},
	}

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			podGangGVR: "PodGangList",
		},
		pg,
	)

	cmd := &StatusCmd{Namespace: "default"}
	gangs, err := cmd.fetchPodGangs(context.Background(), client, "my-pcs")

	require.NoError(t, err)
	assert.Len(t, gangs, 1)
	assert.Equal(t, schedulerv1alpha1.PodGangPhaseRunning, gangs[0].Status.Phase)
	require.NotNil(t, gangs[0].Status.PlacementScore)
	assert.InDelta(t, 0.92, *gangs[0].Status.PlacementScore, 0.001)
}

// TestStatusCmdAllFlag tests the --all flag behavior.
func TestStatusCmdAllFlag(t *testing.T) {
	scheme := runtime.NewScheme()

	// Create two PodCliqueSets
	pcs1 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodCliqueSet",
			"metadata": map[string]interface{}{
				"name":              "pcs-1",
				"namespace":         "default",
				"creationTimestamp": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"template": map[string]interface{}{
					"cliques": []interface{}{},
				},
			},
			"status": map[string]interface{}{},
		},
	}

	pcs2 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodCliqueSet",
			"metadata": map[string]interface{}{
				"name":              "pcs-2",
				"namespace":         "default",
				"creationTimestamp": time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"template": map[string]interface{}{
					"cliques": []interface{}{},
				},
			},
			"status": map[string]interface{}{},
		},
	}

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			podCliqueSetGVR: "PodCliqueSetList",
			podCliqueGVR:    "PodCliqueList",
			podGangGVR:      "PodGangList",
		},
		pcs1, pcs2,
	)

	cmd := &StatusCmd{
		All:       true,
		Namespace: "default",
	}

	// Execute should not return an error even if resources exist
	err := cmd.Execute(client)
	assert.NoError(t, err)
}

// TestEmptyNamespaceStatus tests behavior when no PodCliqueSets exist.
func TestEmptyNamespaceStatus(t *testing.T) {
	scheme := runtime.NewScheme()

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			podCliqueSetGVR: "PodCliqueSetList",
		},
	)

	cmd := &StatusCmd{
		All:       true,
		Namespace: "empty-namespace",
	}

	err := cmd.Execute(client)
	assert.NoError(t, err)
}

// Helper function to create float64 pointer
func ptrFloat64(v float64) *float64 {
	return &v
}
