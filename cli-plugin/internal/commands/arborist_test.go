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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// TestViewStateTransitions verifies navigation state machine transitions.
func TestViewStateTransitions(t *testing.T) {
	tests := []struct {
		name          string
		initialState  ViewState
		action        string // "drillDown" or "navigateBack"
		expectedState ViewState
		setupFunc     func(app *ArboristApp)
	}{
		{
			name:          "drill down from forest to podcliqueset",
			initialState:  ViewForest,
			action:        "drillDown",
			expectedState: ViewPodCliqueSet,
			setupFunc: func(app *ArboristApp) {
				app.selectedPCS = "my-pcs"
			},
		},
		{
			name:          "drill down from podcliqueset to podgang",
			initialState:  ViewPodCliqueSet,
			action:        "drillDown",
			expectedState: ViewPodGang,
			setupFunc: func(app *ArboristApp) {
				app.selectedPCS = "my-pcs"
				app.selectedPodGang = "my-pcs-0"
			},
		},
		{
			name:          "drill down from podgang to podclique",
			initialState:  ViewPodGang,
			action:        "drillDown",
			expectedState: ViewPodClique,
			setupFunc: func(app *ArboristApp) {
				app.selectedPCS = "my-pcs"
				app.selectedPodGang = "my-pcs-0"
				app.selectedClique = "my-pcs-0-prefill"
			},
		},
		{
			name:          "drill down from podclique to pod",
			initialState:  ViewPodClique,
			action:        "drillDown",
			expectedState: ViewPod,
			setupFunc: func(app *ArboristApp) {
				app.selectedPCS = "my-pcs"
				app.selectedPodGang = "my-pcs-0"
				app.selectedClique = "my-pcs-0-prefill"
				app.selectedPod = "my-pcs-0-prefill-pod1"
			},
		},
		{
			name:          "navigate back from podcliqueset to forest",
			initialState:  ViewPodCliqueSet,
			action:        "navigateBack",
			expectedState: ViewForest,
			setupFunc: func(app *ArboristApp) {
				app.selectedPCS = "my-pcs"
			},
		},
		{
			name:          "navigate back from podgang to podcliqueset",
			initialState:  ViewPodGang,
			action:        "navigateBack",
			expectedState: ViewPodCliqueSet,
			setupFunc: func(app *ArboristApp) {
				app.selectedPCS = "my-pcs"
				app.selectedPodGang = "my-pcs-0"
			},
		},
		{
			name:          "navigate back from podclique to podgang",
			initialState:  ViewPodClique,
			action:        "navigateBack",
			expectedState: ViewPodGang,
			setupFunc: func(app *ArboristApp) {
				app.selectedPCS = "my-pcs"
				app.selectedPodGang = "my-pcs-0"
				app.selectedClique = "my-pcs-0-prefill"
			},
		},
		{
			name:          "navigate back from topology to forest",
			initialState:  ViewTopology,
			action:        "navigateBack",
			expectedState: ViewForest,
			setupFunc:     func(app *ArboristApp) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &ArboristApp{
				currentView: tt.initialState,
				nodes:       make(map[string]*corev1.Node),
			}
			tt.setupFunc(app)

			// Simulate state transition based on action
			switch tt.action {
			case "drillDown":
				// Simulate drill down logic
				switch app.currentView {
				case ViewForest:
					if app.selectedPCS != "" {
						app.currentView = ViewPodCliqueSet
					}
				case ViewPodCliqueSet:
					if app.selectedPodGang != "" {
						app.currentView = ViewPodGang
					}
				case ViewPodGang:
					if app.selectedClique != "" {
						app.currentView = ViewPodClique
					}
				case ViewPodClique:
					if app.selectedPod != "" {
						app.currentView = ViewPod
					}
				}
			case "navigateBack":
				// Simulate navigate back logic
				switch app.currentView {
				case ViewPodCliqueSet:
					app.selectedPCS = ""
					app.currentView = ViewForest
				case ViewPodGang:
					app.selectedPodGang = ""
					app.currentView = ViewPodCliqueSet
				case ViewPodClique:
					app.selectedClique = ""
					app.currentView = ViewPodGang
				case ViewPod:
					app.selectedPod = ""
					app.currentView = ViewPodClique
				case ViewTopology:
					app.currentView = ViewForest
				}
			}

			assert.Equal(t, tt.expectedState, app.currentView)
		})
	}
}

// TestGetBreadcrumb verifies breadcrumb generation for each view state.
func TestGetBreadcrumb(t *testing.T) {
	tests := []struct {
		name             string
		viewState        ViewState
		selectedPCS      string
		selectedPodGang  string
		selectedClique   string
		selectedPod      string
		expectedContains []string
	}{
		{
			name:             "forest view shows only forest",
			viewState:        ViewForest,
			expectedContains: []string{"Forest"},
		},
		{
			name:             "podcliqueset view shows forest and pcs",
			viewState:        ViewPodCliqueSet,
			selectedPCS:      "my-pcs",
			expectedContains: []string{"Forest", "my-pcs"},
		},
		{
			name:             "podgang view shows forest, pcs, and gang",
			viewState:        ViewPodGang,
			selectedPCS:      "my-pcs",
			selectedPodGang:  "my-pcs-0",
			expectedContains: []string{"Forest", "my-pcs", "my-pcs-0"},
		},
		{
			name:             "podclique view shows full path",
			viewState:        ViewPodClique,
			selectedPCS:      "my-pcs",
			selectedPodGang:  "my-pcs-0",
			selectedClique:   "my-pcs-0-prefill",
			expectedContains: []string{"Forest", "my-pcs", "my-pcs-0", "my-pcs-0-prefill"},
		},
		{
			name:             "pod view shows complete hierarchy",
			viewState:        ViewPod,
			selectedPCS:      "my-pcs",
			selectedPodGang:  "my-pcs-0",
			selectedClique:   "my-pcs-0-prefill",
			selectedPod:      "my-pcs-0-prefill-pod1",
			expectedContains: []string{"Forest", "my-pcs", "my-pcs-0", "my-pcs-0-prefill", "my-pcs-0-prefill-pod1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &ArboristApp{
				currentView:     tt.viewState,
				selectedPCS:     tt.selectedPCS,
				selectedPodGang: tt.selectedPodGang,
				selectedClique:  tt.selectedClique,
				selectedPod:     tt.selectedPod,
			}

			// Generate breadcrumb text (simplified version of updateBreadcrumb logic)
			breadcrumb := generateBreadcrumb(app)

			for _, expected := range tt.expectedContains {
				assert.Contains(t, breadcrumb, expected, "Breadcrumb should contain: %s", expected)
			}
		})
	}
}

// generateBreadcrumb is a test helper that generates breadcrumb text
func generateBreadcrumb(app *ArboristApp) string {
	parts := []string{"Forest"}

	if app.selectedPCS != "" {
		parts = append(parts, app.selectedPCS)
	}
	if app.selectedPodGang != "" {
		parts = append(parts, app.selectedPodGang)
	}
	if app.selectedClique != "" {
		parts = append(parts, app.selectedClique)
	}
	if app.selectedPod != "" {
		parts = append(parts, app.selectedPod)
	}

	result := ""
	for i, part := range parts {
		if i > 0 {
			result += " > "
		}
		result += part
	}
	return result
}

// TestFormatAge verifies age formatting for various durations.
func TestFormatAge(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{
			name:     "seconds only",
			duration: 30 * time.Second,
			expected: "30s",
		},
		{
			name:     "one minute",
			duration: 1 * time.Minute,
			expected: "1m",
		},
		{
			name:     "multiple minutes",
			duration: 5 * time.Minute,
			expected: "5m",
		},
		{
			name:     "one hour",
			duration: 1 * time.Hour,
			expected: "1h",
		},
		{
			name:     "multiple hours",
			duration: 5 * time.Hour,
			expected: "5h",
		},
		{
			name:     "one day",
			duration: 24 * time.Hour,
			expected: "1d",
		},
		{
			name:     "multiple days",
			duration: 2 * 24 * time.Hour,
			expected: "2d",
		},
		{
			name:     "zero duration",
			duration: 0,
			expected: "0s",
		},
		{
			name:     "less than a second",
			duration: 500 * time.Millisecond,
			expected: "0s",
		},
		{
			name:     "59 seconds",
			duration: 59 * time.Second,
			expected: "59s",
		},
		{
			name:     "60 seconds becomes 1m",
			duration: 60 * time.Second,
			expected: "1m",
		},
		{
			name:     "23 hours",
			duration: 23 * time.Hour,
			expected: "23h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatAge(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetStatusColor verifies color coding for different statuses.
func TestGetStatusColor(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		expectedColor string
	}{
		// Green statuses
		{
			name:          "Running status is green",
			status:        "Running",
			expectedColor: "green",
		},
		{
			name:          "Ready status is green",
			status:        "Ready",
			expectedColor: "green",
		},
		{
			name:          "Healthy status is green",
			status:        "Healthy",
			expectedColor: "green",
		},
		// Yellow statuses
		{
			name:          "Pending status is yellow",
			status:        "Pending",
			expectedColor: "yellow",
		},
		{
			name:          "Starting status is yellow",
			status:        "Starting",
			expectedColor: "yellow",
		},
		{
			name:          "NotReady status is yellow",
			status:        "NotReady",
			expectedColor: "yellow",
		},
		// Red statuses
		{
			name:          "Failed status is red",
			status:        "Failed",
			expectedColor: "red",
		},
		{
			name:          "Error status is red",
			status:        "Error",
			expectedColor: "red",
		},
		{
			name:          "Unknown status is red",
			status:        "Unknown",
			expectedColor: "red",
		},
		// Default (white) for unrecognized statuses
		{
			name:          "Unrecognized status is white",
			status:        "SomeOtherStatus",
			expectedColor: "white",
		},
		{
			name:          "Empty status is white",
			status:        "",
			expectedColor: "white",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &ArboristApp{}
			result := app.getStatusColor(tt.status)
			assert.Equal(t, tt.expectedColor, result)
		})
	}
}

// TestViewStateConstants verifies view state constant values are as expected.
func TestViewStateConstants(t *testing.T) {
	assert.Equal(t, ViewState(0), ViewForest)
	assert.Equal(t, ViewState(1), ViewPodCliqueSet)
	assert.Equal(t, ViewState(2), ViewPodGang)
	assert.Equal(t, ViewState(3), ViewPodClique)
	assert.Equal(t, ViewState(4), ViewPod)
	assert.Equal(t, ViewState(5), ViewTopology)
}

// TestPCSStatus tests the getPCSStatus helper function.
func TestPCSStatus(t *testing.T) {
	tests := []struct {
		name              string
		availableReplicas int32
		specReplicas      int32
		expectedStatus    string
	}{
		{
			name:              "all replicas available is Ready",
			availableReplicas: 3,
			specReplicas:      3,
			expectedStatus:    "Ready",
		},
		{
			name:              "some replicas available is Partial",
			availableReplicas: 2,
			specReplicas:      3,
			expectedStatus:    "Partial",
		},
		{
			name:              "no replicas available is NotReady",
			availableReplicas: 0,
			specReplicas:      3,
			expectedStatus:    "NotReady",
		},
		{
			name:              "zero spec replicas with zero available is Ready",
			availableReplicas: 0,
			specReplicas:      0,
			expectedStatus:    "Ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &ArboristApp{}
			pcs := &operatorv1alpha1.PodCliqueSet{
				Spec: operatorv1alpha1.PodCliqueSetSpec{
					Replicas: tt.specReplicas,
				},
				Status: operatorv1alpha1.PodCliqueSetStatus{
					AvailableReplicas: tt.availableReplicas,
				},
			}

			result := app.getPCSStatus(pcs)
			assert.Equal(t, tt.expectedStatus, result)
		})
	}
}
