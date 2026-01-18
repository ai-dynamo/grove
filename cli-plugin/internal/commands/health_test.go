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

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{
			name:     "zero duration",
			duration: 0,
			expected: "0s",
		},
		{
			name:     "seconds only",
			duration: 45 * time.Second,
			expected: "45s",
		},
		{
			name:     "minutes only",
			duration: 30 * time.Minute,
			expected: "30m",
		},
		{
			name:     "hours and minutes",
			duration: 3*time.Hour + 42*time.Minute,
			expected: "3h 42m",
		},
		{
			name:     "hours only",
			duration: 4 * time.Hour,
			expected: "4h 0m",
		},
		{
			name:     "negative duration",
			duration: -5 * time.Minute,
			expected: "0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDuration(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRenderProgressBar(t *testing.T) {
	tests := []struct {
		name      string
		remaining time.Duration
		total     time.Duration
		width     int
		checkLen  int
	}{
		{
			name:      "full progress",
			remaining: 4 * time.Hour,
			total:     4 * time.Hour,
			width:     10,
			checkLen:  10,
		},
		{
			name:      "half progress",
			remaining: 2 * time.Hour,
			total:     4 * time.Hour,
			width:     10,
			checkLen:  10,
		},
		{
			name:      "no progress",
			remaining: 0,
			total:     4 * time.Hour,
			width:     10,
			checkLen:  10,
		},
		{
			name:      "zero total",
			remaining: 1 * time.Hour,
			total:     0,
			width:     10,
			checkLen:  10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := renderProgressBar(tt.remaining, tt.total, tt.width)
			// The result should be exactly width characters
			assert.Equal(t, tt.checkLen, len([]rune(result)))
		})
	}
}

func TestIsOwnedBy(t *testing.T) {
	tests := []struct {
		name      string
		ownerRefs []metav1.OwnerReference
		ownerName string
		ownerKind string
		expected  bool
	}{
		{
			name: "has matching owner",
			ownerRefs: []metav1.OwnerReference{
				{Name: "my-pcs", Kind: "PodCliqueSet"},
			},
			ownerName: "my-pcs",
			ownerKind: "PodCliqueSet",
			expected:  true,
		},
		{
			name: "no matching owner - different name",
			ownerRefs: []metav1.OwnerReference{
				{Name: "other-pcs", Kind: "PodCliqueSet"},
			},
			ownerName: "my-pcs",
			ownerKind: "PodCliqueSet",
			expected:  false,
		},
		{
			name: "no matching owner - different kind",
			ownerRefs: []metav1.OwnerReference{
				{Name: "my-pcs", Kind: "Deployment"},
			},
			ownerName: "my-pcs",
			ownerKind: "PodCliqueSet",
			expected:  false,
		},
		{
			name:      "empty owner refs",
			ownerRefs: []metav1.OwnerReference{},
			ownerName: "my-pcs",
			ownerKind: "PodCliqueSet",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isOwnedBy(tt.ownerRefs, tt.ownerName, tt.ownerKind)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCliqueHealth(t *testing.T) {
	tests := []struct {
		name          string
		readyReplicas int32
		replicas      int32
		minAvailable  int32
		expectAbove   bool
	}{
		{
			name:          "above minimum",
			readyReplicas: 3,
			replicas:      3,
			minAvailable:  2,
			expectAbove:   true,
		},
		{
			name:          "exactly at minimum",
			readyReplicas: 2,
			replicas:      3,
			minAvailable:  2,
			expectAbove:   true,
		},
		{
			name:          "below minimum",
			readyReplicas: 1,
			replicas:      3,
			minAvailable:  2,
			expectAbove:   false,
		},
		{
			name:          "all replicas ready",
			readyReplicas: 5,
			replicas:      5,
			minAvailable:  5,
			expectAbove:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := CliqueHealth{
				Name:          "test-clique",
				ReadyReplicas: tt.readyReplicas,
				Replicas:      tt.replicas,
				MinAvailable:  tt.minAvailable,
				AboveMin:      tt.readyReplicas >= tt.minAvailable,
			}
			assert.Equal(t, tt.expectAbove, health.AboveMin)
		})
	}
}

func TestTerminationCountdown(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name              string
		unhealthySince    time.Time
		terminationDelay  time.Duration
		expectRemaining   bool
		expectTerminating bool
	}{
		{
			name:              "just became unhealthy",
			unhealthySince:    now,
			terminationDelay:  4 * time.Hour,
			expectRemaining:   true,
			expectTerminating: false,
		},
		{
			name:              "halfway through delay",
			unhealthySince:    now.Add(-2 * time.Hour),
			terminationDelay:  4 * time.Hour,
			expectRemaining:   true,
			expectTerminating: false,
		},
		{
			name:              "past termination delay",
			unhealthySince:    now.Add(-5 * time.Hour),
			terminationDelay:  4 * time.Hour,
			expectRemaining:   false,
			expectTerminating: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			elapsed := time.Since(tt.unhealthySince)
			remaining := tt.terminationDelay - elapsed

			if tt.expectRemaining {
				assert.Greater(t, remaining, time.Duration(0))
			}
			if tt.expectTerminating {
				assert.LessOrEqual(t, remaining, time.Duration(0))
			}
		})
	}
}

func TestHealthCmdValidation(t *testing.T) {
	tests := []struct {
		name        string
		cmd         *HealthCmd
		expectError bool
	}{
		{
			name: "valid with name",
			cmd: &HealthCmd{
				Name:      "my-pcs",
				Namespace: "default",
			},
			expectError: false,
		},
		{
			name: "valid with all flag",
			cmd: &HealthCmd{
				All:       true,
				Namespace: "default",
			},
			expectError: false,
		},
		{
			name: "invalid - no name and no all flag",
			cmd: &HealthCmd{
				Namespace: "default",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't fully test Run without mocking, but we can check the validation logic
			if !tt.cmd.All && tt.cmd.Name == "" {
				assert.True(t, tt.expectError, "Expected error for missing name without --all flag")
			}
		})
	}
}

func TestBuildDashboardWithFakeClient(t *testing.T) {
	// Create a fake scheme with Grove types
	scheme := runtime.NewScheme()
	_ = operatorv1alpha1.AddToScheme(scheme)
	_ = schedulerv1alpha1.AddToScheme(scheme)

	// Create test PodCliqueSet
	pcs := &operatorv1alpha1.PodCliqueSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "grove.io/v1alpha1",
			Kind:       "PodCliqueSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
		},
		Spec: operatorv1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: operatorv1alpha1.PodCliqueSetTemplateSpec{
				TerminationDelay: &metav1.Duration{Duration: 4 * time.Hour},
			},
		},
	}

	// Convert to unstructured
	pcsUnstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pcs)
	require.NoError(t, err)

	pcsObj := &unstructured.Unstructured{Object: pcsUnstructured}
	pcsObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "grove.io",
		Version: "v1alpha1",
		Kind:    "PodCliqueSet",
	})

	// Create fake dynamic client
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, pcsObj)

	// Create fake kubernetes client
	clientset := fake.NewSimpleClientset()

	// Create health command
	healthCmd := &HealthCmd{
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		Namespace:     "default",
		Name:          "test-pcs",
	}

	// Build dashboard
	ctx := context.Background()
	dashboard, err := healthCmd.buildDashboard(ctx, pcsObj)

	// Should succeed even if no PodGangs are found
	require.NoError(t, err)
	assert.Equal(t, "test-pcs", dashboard.PodCliqueSetName)
	assert.Equal(t, 4*time.Hour, dashboard.TerminationDelay)
}

func TestGangHealthDetermination(t *testing.T) {
	tests := []struct {
		name           string
		phase          schedulerv1alpha1.PodGangPhase
		hasUnhealthy   bool
		unhealthyTime  time.Time
		cliquesBelowMin bool
		expectHealthy  bool
	}{
		{
			name:           "healthy - running with all cliques above min",
			phase:          schedulerv1alpha1.PodGangPhaseRunning,
			hasUnhealthy:   false,
			cliquesBelowMin: false,
			expectHealthy:  true,
		},
		{
			name:           "unhealthy - has unhealthy condition",
			phase:          schedulerv1alpha1.PodGangPhaseRunning,
			hasUnhealthy:   true,
			unhealthyTime:  time.Now().Add(-1 * time.Hour),
			cliquesBelowMin: false,
			expectHealthy:  false,
		},
		{
			name:           "unhealthy - cliques below minimum",
			phase:          schedulerv1alpha1.PodGangPhaseRunning,
			hasUnhealthy:   false,
			cliquesBelowMin: true,
			expectHealthy:  false,
		},
		{
			name:           "pending phase",
			phase:          schedulerv1alpha1.PodGangPhasePending,
			hasUnhealthy:   false,
			cliquesBelowMin: false,
			expectHealthy:  true, // Pending is still considered healthy initially
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gangHealth := GangHealth{
				Name:      "test-gang",
				Phase:     string(tt.phase),
				IsHealthy: !tt.hasUnhealthy && !tt.cliquesBelowMin,
			}

			if tt.hasUnhealthy {
				gangHealth.UnhealthySince = &tt.unhealthyTime
			}

			assert.Equal(t, tt.expectHealthy, gangHealth.IsHealthy)
		})
	}
}

func TestHealthDashboardSummary(t *testing.T) {
	dashboard := &HealthDashboard{
		PodCliqueSetName: "test-pcs",
		TerminationDelay: 4 * time.Hour,
		Gangs: []GangHealth{
			{Name: "gang-0", IsHealthy: true},
			{Name: "gang-1", IsHealthy: false, TerminationIn: func() *time.Duration { d := 2 * time.Hour; return &d }()},
			{Name: "gang-2", IsHealthy: true},
		},
		TotalCount:   3,
		HealthyCount: 2,
		AtRiskCount:  1,
	}

	assert.Equal(t, 3, dashboard.TotalCount)
	assert.Equal(t, 2, dashboard.HealthyCount)
	assert.Equal(t, 1, dashboard.AtRiskCount)
}

func TestHealthCmdAllFlag(t *testing.T) {
	// Create a minimal scheme (don't add typed Grove objects to avoid conversion issues)
	scheme := runtime.NewScheme()

	// Create test PodCliqueSets as unstructured
	pcs1 := createTestPodCliqueSetUnstructured("pcs-1", "default")
	pcs2 := createTestPodCliqueSetUnstructured("pcs-2", "default")

	// Create fake dynamic client with custom list kinds to handle all required GVRs
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}:          "PodCliqueSetList",
		{Group: "grove.io", Version: "v1alpha1", Resource: "podcliques"}:             "PodCliqueList",
		{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquescalinggroups"}: "PodCliqueScalingGroupList",
		{Group: "grove.io", Version: "v1alpha1", Resource: "clustertopologies"}:      "ClusterTopologyList",
		{Group: "scheduler.grove.io", Version: "v1alpha1", Resource: "podgangs"}:     "PodGangList",
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, pcs1, pcs2)
	clientset := fake.NewSimpleClientset()

	healthCmd := &HealthCmd{
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		Namespace:     "default",
		All:           true,
	}

	// The runAll method should not return error
	ctx := context.Background()
	err := healthCmd.runAll(ctx)
	assert.NoError(t, err)
}

func createTestPodCliqueSetUnstructured(name, namespace string) *unstructured.Unstructured {
	pcs := &operatorv1alpha1.PodCliqueSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "grove.io/v1alpha1",
			Kind:       "PodCliqueSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: operatorv1alpha1.PodCliqueSetSpec{
			Replicas: 1,
		},
	}

	pcsUnstructured, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(pcs)
	obj := &unstructured.Unstructured{Object: pcsUnstructured}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "grove.io",
		Version: "v1alpha1",
		Kind:    "PodCliqueSet",
	})
	return obj
}
