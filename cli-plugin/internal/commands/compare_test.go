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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/cli-plugin/internal/aic"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// Test plan JSON for compare tests
const comparePlanJSON = `{
  "model": "QWEN3_32B",
  "system": "h200_sxm",
  "serving_mode": "disagg",
  "config": {
    "prefill_workers": 4,
    "decode_workers": 2,
    "prefill_tp": 1,
    "decode_tp": 4
  },
  "expected": {
    "throughput_tokens_per_sec_per_gpu": 804.83,
    "ttft_ms": 200,
    "tpot_ms": 50
  },
  "sla": {
    "ttft_ms": 200,
    "tpot_ms": 50
  }
}`

func TestCompareCmd_LoadPlan(t *testing.T) {
	// Create a ConfigMap with a plan
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-inference-aic-plan",
			Namespace: "default",
			Labels: map[string]string{
				aic.PlanLabelKey: "my-inference",
			},
			Annotations: map[string]string{
				aic.StoredAtAnnotation: "2025-01-15T10:00:00Z",
			},
		},
		Data: map[string]string{
			aic.PlanDataKey: comparePlanJSON,
		},
	}

	clientset := fake.NewSimpleClientset(cm)

	cmd := &CompareCmd{
		Name:      "my-inference",
		Namespace: "default",
	}

	ctx := context.Background()
	plan, storedPlan, err := cmd.loadPlan(ctx, clientset)

	require.NoError(t, err)
	assert.NotNil(t, plan)
	assert.NotNil(t, storedPlan)
	assert.Equal(t, "QWEN3_32B", plan.Model)
	assert.Equal(t, "disagg", plan.Backend)
	assert.Len(t, plan.Roles, 2)

	// Verify roles
	var prefillRole, decodeRole *RolePlan
	for i := range plan.Roles {
		if plan.Roles[i].Name == "prefill" {
			prefillRole = &plan.Roles[i]
		} else if plan.Roles[i].Name == "decode" {
			decodeRole = &plan.Roles[i]
		}
	}

	require.NotNil(t, prefillRole)
	assert.Equal(t, 4, prefillRole.Replicas)
	assert.Equal(t, 1, prefillRole.TensorParallelSize)

	require.NotNil(t, decodeRole)
	assert.Equal(t, 2, decodeRole.Replicas)
	assert.Equal(t, 4, decodeRole.TensorParallelSize)

	// Verify SLA targets
	require.NotNil(t, plan.SLATargets)
	assert.Equal(t, 200.0, plan.SLATargets.TTFTMs)
	assert.Equal(t, 50.0, plan.SLATargets.TPOTMs)
}

func TestCompareCmd_LoadPlan_NotFound(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	cmd := &CompareCmd{
		Name:      "nonexistent",
		Namespace: "default",
	}

	ctx := context.Background()
	_, _, err := cmd.loadPlan(ctx, clientset)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no plan found")
}

func TestCompareCmd_LoadActualState(t *testing.T) {
	scheme := runtime.NewScheme()

	// Create PodCliqueSet
	pcs := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodCliqueSet",
			"metadata": map[string]interface{}{
				"name":      "my-inference",
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"template": map[string]interface{}{
					"cliques": []interface{}{
						map[string]interface{}{
							"name": "prefill",
							"spec": map[string]interface{}{
								"replicas": int64(1),
								"roleName": "prefill",
								"podSpec":  map[string]interface{}{},
							},
						},
					},
				},
			},
		},
	}

	// Create PodClique
	pc := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodClique",
			"metadata": map[string]interface{}{
				"name":      "my-inference-0-prefill",
				"namespace": "default",
				"labels": map[string]interface{}{
					"app.kubernetes.io/part-of": "my-inference",
				},
			},
			"spec": map[string]interface{}{
				"roleName": "prefill",
				"replicas": int64(4),
				"podSpec":  map[string]interface{}{},
			},
			"status": map[string]interface{}{
				"readyReplicas": int64(4),
			},
		},
	}

	// Create PodGang
	score := 0.95
	pg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "scheduler.grove.io/v1alpha1",
			"kind":       "PodGang",
			"metadata": map[string]interface{}{
				"name":      "my-inference-0",
				"namespace": "default",
				"labels": map[string]interface{}{
					"app.kubernetes.io/part-of": "my-inference",
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

	gvrToListKind := map[schema.GroupVersionResource]string{
		podCliqueSetGVR: "PodCliqueSetList",
		podCliqueGVR:    "PodCliqueList",
		podGangGVR:      "PodGangList",
	}

	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, pcs, pc, pg)

	// Create pods
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-inference-0-prefill-0",
			Namespace: "default",
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "my-inference",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "rack1-node-001",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	clientset := fake.NewSimpleClientset(pod1)

	cmd := &CompareCmd{
		Name:      "my-inference",
		Namespace: "default",
	}

	ctx := context.Background()
	state, err := cmd.loadActualState(ctx, dynamicClient, clientset)

	require.NoError(t, err)
	assert.NotNil(t, state)
	assert.NotNil(t, state.PodCliqueSet)
	assert.Len(t, state.PodCliques, 1)
	assert.Len(t, state.PodGangs, 1)
	assert.Len(t, state.Pods, 1)
}

func TestCompareCmd_CompareConfiguration(t *testing.T) {
	plan := &Plan{
		Model:   "QWEN3_32B",
		Backend: "disagg",
		Roles: []RolePlan{
			{Name: "prefill", Replicas: 4, GPUs: 1, TensorParallelSize: 1},
			{Name: "decode", Replicas: 2, GPUs: 4, TensorParallelSize: 4},
		},
	}

	tests := []struct {
		name        string
		actual      *ActualState
		wantMatches bool
	}{
		{
			name: "all match",
			actual: &ActualState{
				PodCliqueSet: &operatorv1alpha1.PodCliqueSet{
					Spec: operatorv1alpha1.PodCliqueSetSpec{
						Template: operatorv1alpha1.PodCliqueSetTemplateSpec{
							PodCliqueScalingGroupConfigs: []operatorv1alpha1.PodCliqueScalingGroupConfig{
								{Name: "prefill-group", Replicas: ptrInt32(4)},
								{Name: "decode-group", Replicas: ptrInt32(2)},
							},
							Cliques: []*operatorv1alpha1.PodCliqueTemplateSpec{
								{Name: "prefill", Spec: operatorv1alpha1.PodCliqueSpec{Replicas: 1}},
								{Name: "decode", Spec: operatorv1alpha1.PodCliqueSpec{Replicas: 4}},
							},
						},
					},
				},
			},
			wantMatches: true,
		},
		{
			name: "replicas differ",
			actual: &ActualState{
				PodCliqueSet: &operatorv1alpha1.PodCliqueSet{
					Spec: operatorv1alpha1.PodCliqueSetSpec{
						Template: operatorv1alpha1.PodCliqueSetTemplateSpec{
							PodCliqueScalingGroupConfigs: []operatorv1alpha1.PodCliqueScalingGroupConfig{
								{Name: "prefill-group", Replicas: ptrInt32(2)}, // Different
								{Name: "decode-group", Replicas: ptrInt32(2)},
							},
							Cliques: []*operatorv1alpha1.PodCliqueTemplateSpec{
								{Name: "prefill", Spec: operatorv1alpha1.PodCliqueSpec{Replicas: 1}},
								{Name: "decode", Spec: operatorv1alpha1.PodCliqueSpec{Replicas: 4}},
							},
						},
					},
				},
			},
			wantMatches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &CompareCmd{}
			result := cmd.compareConfiguration(plan, tt.actual)

			assert.Equal(t, tt.wantMatches, result.Matches)
			assert.NotEmpty(t, result.Diffs)
		})
	}
}

func TestCompareCmd_CompareTopology(t *testing.T) {
	plan := &Plan{
		TopologyPack: "rack",
	}

	tests := []struct {
		name        string
		actual      *ActualState
		wantScore   float64
		wantWarning bool
	}{
		{
			name: "good placement",
			actual: &ActualState{
				PodCliqueSet: &operatorv1alpha1.PodCliqueSet{
					Spec: operatorv1alpha1.PodCliqueSetSpec{
						Template: operatorv1alpha1.PodCliqueSetTemplateSpec{
							TopologyConstraint: &operatorv1alpha1.TopologyConstraint{
								PackDomain: operatorv1alpha1.TopologyDomainRack,
							},
						},
					},
				},
				PodGangs: []*schedulerv1alpha1.PodGang{
					{Status: schedulerv1alpha1.PodGangStatus{PlacementScore: ptrFloat64(0.95)}},
				},
				Pods: []*corev1.Pod{
					{Spec: corev1.PodSpec{NodeName: "rack1-node-001"}},
					{Spec: corev1.PodSpec{NodeName: "rack1-node-002"}},
				},
			},
			wantScore:   0.95,
			wantWarning: false,
		},
		{
			name: "suboptimal placement",
			actual: &ActualState{
				PodCliqueSet: &operatorv1alpha1.PodCliqueSet{
					Spec: operatorv1alpha1.PodCliqueSetSpec{
						Template: operatorv1alpha1.PodCliqueSetTemplateSpec{
							TopologyConstraint: &operatorv1alpha1.TopologyConstraint{
								PackDomain: operatorv1alpha1.TopologyDomainRack,
							},
						},
					},
				},
				PodGangs: []*schedulerv1alpha1.PodGang{
					{Status: schedulerv1alpha1.PodGangStatus{PlacementScore: ptrFloat64(0.72)}},
				},
				Pods: []*corev1.Pod{
					{Spec: corev1.PodSpec{NodeName: "rack1-node-001"}},
					{Spec: corev1.PodSpec{NodeName: "rack2-node-001"}},
				},
			},
			wantScore:   0.72,
			wantWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &CompareCmd{}
			result := cmd.compareTopology(plan, tt.actual)

			assert.InDelta(t, tt.wantScore, result.PlacementScore, 0.01)
			if tt.wantWarning {
				assert.Equal(t, DiffStatusWarning, result.Status)
			}
		})
	}
}

func TestCompareCmd_GenerateDiagnosis(t *testing.T) {
	tests := []struct {
		name                    string
		result                  *ComparisonResult
		actual                  *ActualState
		wantDiagnosisCount      int
		wantRecommendationCount int
	}{
		{
			name: "healthy deployment",
			result: &ComparisonResult{
				Configuration: ConfigComparison{Matches: true},
				Topology: TopologyComparison{
					PlacementScore: 0.95,
					RacksUsed:      1,
					ExpectedRacks:  1,
				},
			},
			actual: &ActualState{
				Pods: []*corev1.Pod{
					{Status: corev1.PodStatus{Phase: corev1.PodRunning}},
				},
			},
			wantDiagnosisCount:      0,
			wantRecommendationCount: 0,
		},
		{
			name: "suboptimal placement",
			result: &ComparisonResult{
				Configuration: ConfigComparison{Matches: true},
				Topology: TopologyComparison{
					PlacementScore: 0.72,
					RacksUsed:      2,
					ExpectedRacks:  1,
				},
			},
			actual: &ActualState{
				Pods: []*corev1.Pod{
					{Status: corev1.PodStatus{Phase: corev1.PodRunning}},
				},
			},
			wantDiagnosisCount:      2, // Low score + rack spread
			wantRecommendationCount: 2,
		},
		{
			name: "config mismatch",
			result: &ComparisonResult{
				Configuration: ConfigComparison{
					Matches: false,
					Diffs: []ConfigDiff{
						{Status: DiffStatusMismatch},
					},
				},
				Topology: TopologyComparison{
					PlacementScore: 0.95,
					RacksUsed:      1,
					ExpectedRacks:  1,
				},
			},
			actual: &ActualState{
				Pods: []*corev1.Pod{
					{Status: corev1.PodStatus{Phase: corev1.PodRunning}},
				},
			},
			wantDiagnosisCount:      1, // Config mismatch
			wantRecommendationCount: 1,
		},
		{
			name: "unhealthy pods",
			result: &ComparisonResult{
				Configuration: ConfigComparison{Matches: true},
				Topology: TopologyComparison{
					PlacementScore: 0.95,
					RacksUsed:      1,
					ExpectedRacks:  1,
				},
			},
			actual: &ActualState{
				Pods: []*corev1.Pod{
					{Status: corev1.PodStatus{Phase: corev1.PodPending}},
					{Status: corev1.PodStatus{Phase: corev1.PodFailed}},
				},
			},
			wantDiagnosisCount:      1, // Unhealthy pods
			wantRecommendationCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &CompareCmd{}
			diagnosis, recommendations := cmd.generateDiagnosisAndRecommendations(tt.result, tt.actual)

			assert.Len(t, diagnosis, tt.wantDiagnosisCount)
			assert.Len(t, recommendations, tt.wantRecommendationCount)
		})
	}
}

func TestCompareCmd_OutputJSON(t *testing.T) {
	cmd := &CompareCmd{JSON: true}
	result := &ComparisonResult{
		Name:          "my-inference",
		Namespace:     "default",
		PlanTimestamp: "2025-01-15T10:00:00Z",
		Configuration: ConfigComparison{
			Matches: true,
			Diffs: []ConfigDiff{
				{Setting: "Prefill Replicas", Planned: "4", Actual: "4", Status: DiffStatusMatch},
			},
		},
		Topology: TopologyComparison{
			PlannedConstraint: "rack",
			ActualConstraint:  "rack",
			PlacementScore:    0.95,
			RacksUsed:         1,
			ExpectedRacks:     1,
			Status:            DiffStatusMatch,
		},
		Diagnosis:       []string{},
		Recommendations: []string{},
	}

	var buf bytes.Buffer
	err := cmd.outputJSON(&buf, result)

	require.NoError(t, err)

	// Parse the JSON output
	var output ComparisonResult
	err = json.Unmarshal(buf.Bytes(), &output)
	require.NoError(t, err)

	assert.Equal(t, "my-inference", output.Name)
	assert.Equal(t, "default", output.Namespace)
	assert.True(t, output.Configuration.Matches)
	assert.InDelta(t, 0.95, output.Topology.PlacementScore, 0.01)
}

func TestCompareCmd_OutputTable(t *testing.T) {
	cmd := &CompareCmd{}
	result := &ComparisonResult{
		Name:          "my-inference",
		Namespace:     "default",
		PlanTimestamp: "2025-01-15T10:00:00Z",
		Configuration: ConfigComparison{
			Matches: false,
			Diffs: []ConfigDiff{
				{Setting: "Prefill Replicas", Planned: "4", Actual: "4", Status: DiffStatusMatch},
				{Setting: "Decode Replicas", Planned: "2", Actual: "3", Status: DiffStatusMismatch},
			},
		},
		Topology: TopologyComparison{
			PlannedConstraint: "rack",
			ActualConstraint:  "rack",
			PlacementScore:    0.72,
			RacksUsed:         2,
			ExpectedRacks:     1,
			Status:            DiffStatusWarning,
		},
		Diagnosis:       []string{"PlacementScore 0.72 indicates suboptimal placement"},
		Recommendations: []string{"Consider rescheduling to consolidate pods on single rack"},
	}

	var buf bytes.Buffer
	err := cmd.outputTable(&buf, result)

	require.NoError(t, err)
	output := buf.String()

	// Verify key elements
	assert.Contains(t, output, "Plan vs Actual: my-inference")
	assert.Contains(t, output, "namespace: default")
	assert.Contains(t, output, "Plan stored: 2025-01-15T10:00:00Z")
	assert.Contains(t, output, "Configuration:")
	assert.Contains(t, output, "Topology:")
	assert.Contains(t, output, "Prefill Replicas")
	assert.Contains(t, output, "Decode Replicas")
	assert.Contains(t, output, "Diagnosis:")
	assert.Contains(t, output, "Recommendations:")
	assert.Contains(t, output, "PlacementScore 0.72")
}

func TestCompareCmd_VerboseOutput(t *testing.T) {
	cmd := &CompareCmd{Verbose: true}
	result := &ComparisonResult{
		Name:      "my-inference",
		Namespace: "default",
		Configuration: ConfigComparison{
			Matches: true,
			Diffs:   []ConfigDiff{},
		},
		Topology: TopologyComparison{
			PlannedConstraint: "rack",
			PlacementScore:    0.95,
			RacksUsed:         1,
			ExpectedRacks:     1,
			PodPlacements: []PodPlacementInfo{
				{PodName: "pod-1", NodeName: "node-1", Rack: "rack1", Status: "Running"},
				{PodName: "pod-2", NodeName: "node-2", Rack: "rack1", Status: "Running"},
			},
		},
	}

	var buf bytes.Buffer
	err := cmd.outputTable(&buf, result)

	require.NoError(t, err)
	output := buf.String()

	assert.Contains(t, output, "Pod Placements:")
	assert.Contains(t, output, "pod-1")
	assert.Contains(t, output, "pod-2")
	assert.Contains(t, output, "node-1")
	assert.Contains(t, output, "node-2")
	assert.Contains(t, output, "rack1")
}

func TestCompareCmd_ExtractRackFromNodeName(t *testing.T) {
	tests := []struct {
		nodeName string
		wantRack string
	}{
		{nodeName: "rack1-node-001", wantRack: "rack1"},
		{nodeName: "node-rack2-001", wantRack: "rack2"},
		{nodeName: "worker-001", wantRack: "worker"},
		{nodeName: "node001", wantRack: "node001"},
		{nodeName: "", wantRack: ""},
	}

	for _, tt := range tests {
		t.Run(tt.nodeName, func(t *testing.T) {
			rack := extractRackFromNodeName(tt.nodeName)
			assert.Equal(t, tt.wantRack, rack)
		})
	}
}

func TestCompareCmd_DiffStatusFormatting(t *testing.T) {
	tests := []struct {
		status DiffStatus
		want   string
	}{
		{DiffStatusMatch, "Match"},
		{DiffStatusMismatch, "Differs"},
		{DiffStatusWarning, "Warning"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			result := formatDiffStatus(tt.status)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestCompareCmd_ScoreToStatus(t *testing.T) {
	tests := []struct {
		score float64
		want  DiffStatus
	}{
		{1.0, DiffStatusMatch},
		{0.95, DiffStatusMatch},
		{0.9, DiffStatusMatch},
		{0.85, DiffStatusWarning},
		{0.7, DiffStatusWarning},
		{0.65, DiffStatusMismatch},
		{0.5, DiffStatusMismatch},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("score_%.2f", tt.score), func(t *testing.T) {
			result := scoreToStatus(tt.score)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestCompareCmd_ExtractActualConfig(t *testing.T) {
	cmd := &CompareCmd{}

	actual := &ActualState{
		PodCliqueSet: &operatorv1alpha1.PodCliqueSet{
			Spec: operatorv1alpha1.PodCliqueSetSpec{
				Template: operatorv1alpha1.PodCliqueSetTemplateSpec{
					PodCliqueScalingGroupConfigs: []operatorv1alpha1.PodCliqueScalingGroupConfig{
						{Name: "prefill-group", Replicas: ptrInt32(4)},
						{Name: "decode-group", Replicas: ptrInt32(2)},
					},
					Cliques: []*operatorv1alpha1.PodCliqueTemplateSpec{
						{Name: "prefill-worker", Spec: operatorv1alpha1.PodCliqueSpec{Replicas: 1}},
						{Name: "decode-worker", Spec: operatorv1alpha1.PodCliqueSpec{Replicas: 4}},
					},
				},
			},
		},
	}

	config := cmd.extractActualConfig(actual)

	assert.Equal(t, 4, config["prefill_replicas"])
	assert.Equal(t, 2, config["decode_replicas"])
	assert.Equal(t, 1, config["prefill_tp"])
	assert.Equal(t, 4, config["decode_tp"])
}

func TestCompareCmd_IntegrationWorkflow(t *testing.T) {
	// Setup: Create a plan ConfigMap
	planCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-inference-aic-plan",
			Namespace: "default",
			Labels: map[string]string{
				aic.PlanLabelKey: "my-inference",
			},
			Annotations: map[string]string{
				aic.StoredAtAnnotation: time.Now().Format(time.RFC3339),
			},
		},
		Data: map[string]string{
			aic.PlanDataKey: comparePlanJSON,
		},
	}

	clientset := fake.NewSimpleClientset(planCM)

	// Setup: Create PodCliqueSet
	pcs := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodCliqueSet",
			"metadata": map[string]interface{}{
				"name":      "my-inference",
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"template": map[string]interface{}{
					"topologyConstraint": map[string]interface{}{
						"packDomain": "rack",
					},
					"cliques": []interface{}{
						map[string]interface{}{
							"name": "prefill",
							"spec": map[string]interface{}{
								"replicas": int64(1),
								"roleName": "prefill",
								"podSpec":  map[string]interface{}{},
							},
						},
						map[string]interface{}{
							"name": "decode",
							"spec": map[string]interface{}{
								"replicas": int64(4),
								"roleName": "decode",
								"podSpec":  map[string]interface{}{},
							},
						},
					},
					"podCliqueScalingGroups": []interface{}{
						map[string]interface{}{
							"name":     "prefill-group",
							"replicas": int64(4),
						},
						map[string]interface{}{
							"name":     "decode-group",
							"replicas": int64(2),
						},
					},
				},
			},
		},
	}

	// Setup: Create PodGang
	score := 0.95
	pg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "scheduler.grove.io/v1alpha1",
			"kind":       "PodGang",
			"metadata": map[string]interface{}{
				"name":      "my-inference-0",
				"namespace": "default",
				"labels": map[string]interface{}{
					"app.kubernetes.io/part-of": "my-inference",
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

	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		podCliqueSetGVR: "PodCliqueSetList",
		podCliqueGVR:    "PodCliqueList",
		podGangGVR:      "PodGangList",
	}

	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, pcs, pg)

	// Execute compare command
	cmd := &CompareCmd{
		Name:      "my-inference",
		Namespace: "default",
	}

	var buf bytes.Buffer
	err := cmd.ExecuteWithWriter(clientset, dynamicClient, &buf)

	require.NoError(t, err)
	output := buf.String()

	// Verify output
	assert.Contains(t, output, "Plan vs Actual: my-inference")
	assert.Contains(t, output, "Configuration:")
	assert.Contains(t, output, "Topology:")
}

func TestCompareCmd_NoPodCliqueSet(t *testing.T) {
	// Setup: Create a plan ConfigMap but no PodCliqueSet
	planCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-inference-aic-plan",
			Namespace: "default",
			Labels: map[string]string{
				aic.PlanLabelKey: "my-inference",
			},
		},
		Data: map[string]string{
			aic.PlanDataKey: comparePlanJSON,
		},
	}

	clientset := fake.NewSimpleClientset(planCM)

	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		podCliqueSetGVR: "PodCliqueSetList",
		podCliqueGVR:    "PodCliqueList",
		podGangGVR:      "PodGangList",
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)

	cmd := &CompareCmd{
		Name:      "my-inference",
		Namespace: "default",
	}

	var buf bytes.Buffer
	err := cmd.ExecuteWithWriter(clientset, dynamicClient, &buf)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load actual state")
}

func TestPrintCompareTableBorder(t *testing.T) {
	var buf bytes.Buffer
	printCompareTableBorder(&buf, 10, 10, 10, 10, "top")
	output := buf.String()

	assert.Contains(t, output, "+")
	assert.Contains(t, output, "-")
}

func TestPrintCompareTableRow(t *testing.T) {
	var buf bytes.Buffer
	printCompareTableRow(&buf, 10, 10, 10, 10, "A", "B", "C", "D")
	output := buf.String()

	assert.Contains(t, output, "A")
	assert.Contains(t, output, "B")
	assert.Contains(t, output, "C")
	assert.Contains(t, output, "D")
	assert.Contains(t, output, "|")
}

func TestBoolToDiffStatus(t *testing.T) {
	assert.Equal(t, DiffStatusMatch, boolToDiffStatus(true))
	assert.Equal(t, DiffStatusMismatch, boolToDiffStatus(false))
}

// Helper function
func ptrInt32(v int32) *int32 {
	return &v
}
