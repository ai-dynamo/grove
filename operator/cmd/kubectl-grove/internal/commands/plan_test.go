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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-dynamo/grove/operator/cmd/kubectl-grove/internal/aic"
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

// Sample plan JSON for testing
const samplePlanJSON = `{
  "model": "QWEN3_32B",
  "system": "h200_sxm",
  "serving_mode": "disagg",
  "config": {
    "prefill_workers": 4,
    "decode_workers": 1,
    "prefill_tp": 1,
    "decode_tp": 4
  },
  "expected": {
    "throughput_tokens_per_sec_per_gpu": 804.83,
    "ttft_ms": 486.53,
    "tpot_ms": 9.16
  },
  "sla": {
    "ttft_ms": 300,
    "tpot_ms": 10
  }
}`

func TestParsePlanFromFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    *aic.StoragePlan
		wantErr bool
	}{
		{
			name:    "valid JSON plan",
			content: samplePlanJSON,
			want: &aic.StoragePlan{
				Model:       "QWEN3_32B",
				System:      "h200_sxm",
				ServingMode: "disagg",
				Config: aic.StoragePlanConfig{
					PrefillWorkers: 4,
					DecodeWorkers:  1,
					PrefillTP:      1,
					DecodeTP:       4,
				},
				Expected: aic.StoragePlanExpected{
					ThroughputTokensPerSecPerGPU: 804.83,
					TTFTMs:                       486.53,
					TPOTMs:                       9.16,
				},
				SLA: aic.StoragePlanSLA{
					TTFTMs: 300,
					TPOTMs: 10,
				},
			},
			wantErr: false,
		},
		{
			name:    "minimal plan",
			content: `{"model": "TEST", "system": "test", "serving_mode": "agg", "config": {"prefill_workers": 1, "decode_workers": 1, "prefill_tp": 1, "decode_tp": 1}}`,
			want: &aic.StoragePlan{
				Model:       "TEST",
				System:      "test",
				ServingMode: "agg",
				Config: aic.StoragePlanConfig{
					PrefillWorkers: 1,
					DecodeWorkers:  1,
					PrefillTP:      1,
					DecodeTP:       1,
				},
			},
			wantErr: false,
		},
		{
			name:    "invalid JSON",
			content: `{invalid json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := aic.ParsePlanFromFile([]byte(tt.content))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want.Model, got.Model)
			assert.Equal(t, tt.want.System, got.System)
			assert.Equal(t, tt.want.ServingMode, got.ServingMode)
			assert.Equal(t, tt.want.Config, got.Config)
		})
	}
}

func TestPlanStore_StoreAndGet(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()

	store := aic.NewPlanStore(clientset)

	plan := &aic.StoragePlan{
		Model:       "QWEN3_32B",
		System:      "h200_sxm",
		ServingMode: "disagg",
		Config: aic.StoragePlanConfig{
			PrefillWorkers: 4,
			DecodeWorkers:  1,
			PrefillTP:      1,
			DecodeTP:       4,
		},
	}

	// Store the plan
	err := store.Store(ctx, "my-inference", "default", plan)
	require.NoError(t, err)

	// Verify ConfigMap was created
	cm, err := clientset.CoreV1().ConfigMaps("default").Get(ctx, "my-inference-aic-plan", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "my-inference", cm.Labels[aic.PlanLabelKey])
	assert.Contains(t, cm.Data, aic.PlanDataKey)

	// Retrieve the plan
	storedPlan, err := store.Get(ctx, "my-inference", "default")
	require.NoError(t, err)
	assert.Equal(t, "my-inference", storedPlan.Name)
	assert.Equal(t, "QWEN3_32B", storedPlan.Plan.Model)
	assert.Equal(t, 4, storedPlan.Plan.Config.PrefillWorkers)
}

func TestPlanStore_Update(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()

	store := aic.NewPlanStore(clientset)

	plan := &aic.StoragePlan{
		Model:       "QWEN3_32B",
		System:      "h200_sxm",
		ServingMode: "disagg",
		Config: aic.StoragePlanConfig{
			PrefillWorkers: 4,
			DecodeWorkers:  1,
			PrefillTP:      1,
			DecodeTP:       4,
		},
	}

	// Store the plan
	err := store.Store(ctx, "my-inference", "default", plan)
	require.NoError(t, err)

	// Update the plan
	plan.Config.PrefillWorkers = 8
	err = store.Store(ctx, "my-inference", "default", plan)
	require.NoError(t, err)

	// Retrieve and verify update
	storedPlan, err := store.Get(ctx, "my-inference", "default")
	require.NoError(t, err)
	assert.Equal(t, 8, storedPlan.Plan.Config.PrefillWorkers)
}

func TestPlanStore_List(t *testing.T) {
	ctx := context.Background()

	// Create ConfigMaps with plans
	cm1 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plan1-aic-plan",
			Namespace: "default",
			Labels: map[string]string{
				aic.PlanLabelKey: "plan1",
			},
		},
		Data: map[string]string{
			aic.PlanDataKey: `{"model": "MODEL1", "system": "sys1", "serving_mode": "agg", "config": {"prefill_workers": 1, "decode_workers": 1, "prefill_tp": 1, "decode_tp": 1}}`,
		},
	}
	cm2 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plan2-aic-plan",
			Namespace: "default",
			Labels: map[string]string{
				aic.PlanLabelKey: "plan2",
			},
		},
		Data: map[string]string{
			aic.PlanDataKey: `{"model": "MODEL2", "system": "sys2", "serving_mode": "disagg", "config": {"prefill_workers": 2, "decode_workers": 2, "prefill_tp": 2, "decode_tp": 2}}`,
		},
	}

	clientset := fake.NewSimpleClientset(cm1, cm2)
	store := aic.NewPlanStore(clientset)

	plans, err := store.List(ctx, "default")
	require.NoError(t, err)
	assert.Len(t, plans, 2)
}

func TestPlanStore_Delete(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()

	store := aic.NewPlanStore(clientset)

	plan := &aic.StoragePlan{
		Model:       "QWEN3_32B",
		System:      "h200_sxm",
		ServingMode: "disagg",
		Config: aic.StoragePlanConfig{
			PrefillWorkers: 4,
			DecodeWorkers:  1,
			PrefillTP:      1,
			DecodeTP:       4,
		},
	}

	// Store the plan
	err := store.Store(ctx, "my-inference", "default", plan)
	require.NoError(t, err)

	// Delete the plan
	err = store.Delete(ctx, "my-inference", "default")
	require.NoError(t, err)

	// Verify it's gone
	_, err = store.Get(ctx, "my-inference", "default")
	assert.Error(t, err)
}

func TestComputeDiff(t *testing.T) {
	plan := &aic.StoragePlan{
		Config: aic.StoragePlanConfig{
			PrefillWorkers: 4,
			DecodeWorkers:  1,
			PrefillTP:      1,
			DecodeTP:       4,
		},
	}

	tests := []struct {
		name         string
		deployed     *aic.DeployedConfig
		wantMatches  int
		wantDiffers  int
	}{
		{
			name: "all match",
			deployed: &aic.DeployedConfig{
				PrefillWorkers: 4,
				DecodeWorkers:  1,
				PrefillTP:      1,
				DecodeTP:       4,
			},
			wantMatches: 4,
			wantDiffers: 0,
		},
		{
			name: "all differ",
			deployed: &aic.DeployedConfig{
				PrefillWorkers: 2,
				DecodeWorkers:  2,
				PrefillTP:      2,
				DecodeTP:       2,
			},
			wantMatches: 0,
			wantDiffers: 4,
		},
		{
			name: "partial match",
			deployed: &aic.DeployedConfig{
				PrefillWorkers: 4,
				DecodeWorkers:  2,
				PrefillTP:      1,
				DecodeTP:       2,
			},
			wantMatches: 2,
			wantDiffers: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diffs := aic.ComputeDiff(plan, tt.deployed)
			assert.Len(t, diffs, 4)

			matches := 0
			differs := 0
			for _, d := range diffs {
				if d.Matches {
					matches++
				} else {
					differs++
				}
			}
			assert.Equal(t, tt.wantMatches, matches)
			assert.Equal(t, tt.wantDiffers, differs)
		})
	}
}

func TestFormatPlanOutput(t *testing.T) {
	plan := &aic.StoredPlan{
		Name:      "my-inference",
		Namespace: "default",
		Plan: aic.StoragePlan{
			Model:       "QWEN3_32B",
			System:      "h200_sxm",
			ServingMode: "disagg",
			Config: aic.StoragePlanConfig{
				PrefillWorkers: 4,
				DecodeWorkers:  1,
				PrefillTP:      1,
				DecodeTP:       4,
			},
			Expected: aic.StoragePlanExpected{
				ThroughputTokensPerSecPerGPU: 804.83,
				TTFTMs:                       486.53,
				TPOTMs:                       9.16,
			},
			SLA: aic.StoragePlanSLA{
				TTFTMs: 300,
				TPOTMs: 10,
			},
		},
	}

	var buf bytes.Buffer
	formatPlanOutput(&buf, plan)
	output := buf.String()

	// Verify key information is present
	assert.Contains(t, output, "Plan: my-inference")
	assert.Contains(t, output, "Model: QWEN3_32B")
	assert.Contains(t, output, "System: h200_sxm")
	assert.Contains(t, output, "Mode: disagg")
	assert.Contains(t, output, "Prefill Workers: 4 (tp=1)")
	assert.Contains(t, output, "Decode Workers: 1 (tp=4)")
	assert.Contains(t, output, "Throughput: 804.83 tok/s/gpu")
	assert.Contains(t, output, "TTFT: 486.53ms")
	assert.Contains(t, output, "TPOT: 9.16ms")
	assert.Contains(t, output, "SLA Targets:")
}

func TestFormatDiffOutput(t *testing.T) {
	diffs := []aic.DiffResult{
		{Setting: "Prefill Workers", Planned: "4", Deployed: "4", Matches: true},
		{Setting: "Decode Workers", Planned: "1", Deployed: "2", Matches: false},
		{Setting: "Prefill TP", Planned: "1", Deployed: "1", Matches: true},
		{Setting: "Decode TP", Planned: "4", Deployed: "2", Matches: false},
	}

	var buf bytes.Buffer
	formatDiffOutput(&buf, "my-inference", diffs)
	output := buf.String()

	// Verify table structure
	assert.Contains(t, output, "Plan vs Deployed: my-inference")
	assert.Contains(t, output, "Setting")
	assert.Contains(t, output, "Planned")
	assert.Contains(t, output, "Deployed")
	assert.Contains(t, output, "Status")
	assert.Contains(t, output, "Match")
	assert.Contains(t, output, "Differs")
	assert.Contains(t, output, "Note: Deployed config differs from plan")
}

func TestPlanStoreCmd_Execute(t *testing.T) {
	// Create a temporary plan file
	tmpDir := t.TempDir()
	planFile := filepath.Join(tmpDir, "plan.json")
	err := os.WriteFile(planFile, []byte(samplePlanJSON), 0644)
	require.NoError(t, err)

	clientset := fake.NewSimpleClientset()

	cmd := &PlanStoreCmd{
		Name:      "test-inference",
		File:      planFile,
		Namespace: "test-ns",
	}

	err = cmd.Execute(clientset)
	require.NoError(t, err)

	// Verify the plan was stored
	ctx := context.Background()
	cm, err := clientset.CoreV1().ConfigMaps("test-ns").Get(ctx, "test-inference-aic-plan", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "test-inference", cm.Labels[aic.PlanLabelKey])
}

func TestPlanShowCmd_Execute(t *testing.T) {
	// Create a ConfigMap with a plan
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-inference-aic-plan",
			Namespace: "default",
			Labels: map[string]string{
				aic.PlanLabelKey: "my-inference",
			},
			Annotations: map[string]string{
				aic.StoredAtAnnotation: "2026-01-15T10:30:00Z",
			},
		},
		Data: map[string]string{
			aic.PlanDataKey: samplePlanJSON,
		},
	}

	clientset := fake.NewSimpleClientset(cm)

	cmd := &PlanShowCmd{
		Name:      "my-inference",
		Namespace: "default",
	}

	var buf bytes.Buffer
	err := cmd.Execute(clientset, &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Plan: my-inference")
	assert.Contains(t, output, "Model: QWEN3_32B")
}

func TestPlanDiffCmd_Execute(t *testing.T) {
	// Create a ConfigMap with a plan
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-inference-aic-plan",
			Namespace: "default",
			Labels: map[string]string{
				aic.PlanLabelKey: "my-inference",
			},
		},
		Data: map[string]string{
			aic.PlanDataKey: samplePlanJSON,
		},
	}

	clientset := fake.NewSimpleClientset(cm)

	// Create a fake PodCliqueSet
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
							"name": "prefill-worker",
							"spec": map[string]interface{}{
								"replicas": int64(1),
							},
						},
						map[string]interface{}{
							"name": "decode-worker",
							"spec": map[string]interface{}{
								"replicas": int64(2),
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

	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, pcs)

	cmd := &PlanDiffCmd{
		Name:      "my-inference",
		Namespace: "default",
	}

	var buf bytes.Buffer
	err := cmd.Execute(clientset, dynamicClient, &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Plan vs Deployed: my-inference")
	assert.Contains(t, output, "Prefill Workers")
	assert.Contains(t, output, "Decode Workers")
}

func TestGetDeployedConfig(t *testing.T) {
	pcs := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodCliqueSet",
			"metadata": map[string]interface{}{
				"name":      "test-pcs",
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"template": map[string]interface{}{
					"cliques": []interface{}{
						map[string]interface{}{
							"name": "prefill",
							"spec": map[string]interface{}{
								"replicas": int64(2),
							},
						},
						map[string]interface{}{
							"name": "decode",
							"spec": map[string]interface{}{
								"replicas": int64(4),
							},
						},
					},
					"podCliqueScalingGroups": []interface{}{
						map[string]interface{}{
							"name":     "prefill-scaling",
							"replicas": int64(3),
						},
						map[string]interface{}{
							"name":     "decode-scaling",
							"replicas": int64(1),
						},
					},
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, pcs)

	ctx := context.Background()
	config, err := getDeployedConfig(ctx, dynamicClient, "test-pcs", "default")
	require.NoError(t, err)

	// From scaling groups
	assert.Equal(t, 3, config.PrefillWorkers)
	assert.Equal(t, 1, config.DecodeWorkers)

	// From cliques (TP values)
	assert.Equal(t, 2, config.PrefillTP)
	assert.Equal(t, 4, config.DecodeTP)
}

func TestGetDeployedConfig_NoScalingGroups(t *testing.T) {
	pcs := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodCliqueSet",
			"metadata": map[string]interface{}{
				"name":      "test-pcs",
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"template": map[string]interface{}{
					"cliques": []interface{}{
						map[string]interface{}{
							"name": "prefill-worker",
							"spec": map[string]interface{}{
								"replicas": int64(4),
							},
						},
						map[string]interface{}{
							"name": "decode-worker",
							"spec": map[string]interface{}{
								"replicas": int64(1),
							},
						},
					},
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, pcs)

	ctx := context.Background()
	config, err := getDeployedConfig(ctx, dynamicClient, "test-pcs", "default")
	require.NoError(t, err)

	// When no scaling groups, workers come from clique replicas
	assert.Equal(t, 4, config.PrefillWorkers)
	assert.Equal(t, 1, config.DecodeWorkers)
	assert.Equal(t, 4, config.PrefillTP)
	assert.Equal(t, 1, config.DecodeTP)
}

func TestPlanStoreCmd_FileNotFound(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	cmd := &PlanStoreCmd{
		Name:      "test",
		File:      "/nonexistent/path/plan.json",
		Namespace: "default",
	}

	err := cmd.Execute(clientset)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read plan file")
}

func TestPlanShowCmd_NotFound(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	cmd := &PlanShowCmd{
		Name:      "nonexistent",
		Namespace: "default",
	}

	var buf bytes.Buffer
	err := cmd.Execute(clientset, &buf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get plan")
}

func TestPlanDiffCmd_PlanNotFound(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	cmd := &PlanDiffCmd{
		Name:      "nonexistent",
		Namespace: "default",
	}

	var buf bytes.Buffer
	err := cmd.Execute(clientset, dynamicClient, &buf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get stored plan")
}

func TestPlanDiffCmd_PodCliqueSetNotFound(t *testing.T) {
	// Create a ConfigMap with a plan but no PodCliqueSet
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-inference-aic-plan",
			Namespace: "default",
			Labels: map[string]string{
				aic.PlanLabelKey: "my-inference",
			},
		},
		Data: map[string]string{
			aic.PlanDataKey: samplePlanJSON,
		},
	}

	clientset := fake.NewSimpleClientset(cm)
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	cmd := &PlanDiffCmd{
		Name:      "my-inference",
		Namespace: "default",
	}

	var buf bytes.Buffer
	err := cmd.Execute(clientset, dynamicClient, &buf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get deployed configuration")
}

func TestTableBorderRendering(t *testing.T) {
	var buf bytes.Buffer

	printTableBorder(&buf, 10, 10, 10, 10, "top")
	output := buf.String()

	// Verify border characters
	assert.True(t, strings.HasPrefix(output, "+"))
	assert.True(t, strings.HasSuffix(strings.TrimSpace(output), "+"))
	assert.Contains(t, output, "-")
}

func TestTableRowRendering(t *testing.T) {
	var buf bytes.Buffer

	printTableRow(&buf, 10, 10, 10, 10, "Col1", "Col2", "Col3", "Col4")
	output := buf.String()

	assert.Contains(t, output, "Col1")
	assert.Contains(t, output, "Col2")
	assert.Contains(t, output, "Col3")
	assert.Contains(t, output, "Col4")
	assert.Contains(t, output, "|")
}

func TestDiffResultStatusDisplay(t *testing.T) {
	// Test that the status display is correct
	diffs := []aic.DiffResult{
		{Setting: "Test", Planned: "1", Deployed: "1", Matches: true},
		{Setting: "Test2", Planned: "1", Deployed: "2", Matches: false},
	}

	var buf bytes.Buffer
	formatDiffOutput(&buf, "test", diffs)
	output := buf.String()

	// When there are differences, note should be shown
	assert.Contains(t, output, "Note: Deployed config differs from plan")
}

func TestDiffResultAllMatch(t *testing.T) {
	diffs := []aic.DiffResult{
		{Setting: "Test", Planned: "1", Deployed: "1", Matches: true},
		{Setting: "Test2", Planned: "2", Deployed: "2", Matches: true},
	}

	var buf bytes.Buffer
	formatDiffOutput(&buf, "test", diffs)
	output := buf.String()

	// When all match, no note should be shown
	assert.NotContains(t, output, "Note: Deployed config differs from plan")
}

// Integration test helper to verify the full workflow
func TestPlanWorkflowIntegration(t *testing.T) {
	ctx := context.Background()

	// Create clients
	clientset := fake.NewSimpleClientset()

	// Create a PodCliqueSet
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
							},
						},
						map[string]interface{}{
							"name": "decode",
							"spec": map[string]interface{}{
								"replicas": int64(4),
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
							"replicas": int64(1),
						},
					},
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, pcs)

	// Step 1: Store a plan
	store := aic.NewPlanStore(clientset)
	plan := &aic.StoragePlan{
		Model:       "QWEN3_32B",
		System:      "h200_sxm",
		ServingMode: "disagg",
		Config: aic.StoragePlanConfig{
			PrefillWorkers: 4,
			DecodeWorkers:  1,
			PrefillTP:      1,
			DecodeTP:       4,
		},
	}

	err := store.Store(ctx, "my-inference", "default", plan)
	require.NoError(t, err)

	// Step 2: Retrieve and verify the plan
	storedPlan, err := store.Get(ctx, "my-inference", "default")
	require.NoError(t, err)
	assert.Equal(t, "QWEN3_32B", storedPlan.Plan.Model)

	// Step 3: Get deployed config and compare
	config, err := getDeployedConfig(ctx, dynamicClient, "my-inference", "default")
	require.NoError(t, err)

	diffs := aic.ComputeDiff(&storedPlan.Plan, config)

	// Verify diff results
	matchCount := 0
	for _, d := range diffs {
		if d.Matches {
			matchCount++
		}
	}

	// Based on our setup:
	// - Prefill Workers: plan=4, deployed=4 (from scaling group) -> Match
	// - Decode Workers: plan=1, deployed=1 (from scaling group) -> Match
	// - Prefill TP: plan=1, deployed=1 (from clique) -> Match
	// - Decode TP: plan=4, deployed=4 (from clique) -> Match
	assert.Equal(t, 4, matchCount, "All settings should match")
}

// Register the GVR for the fake dynamic client
func init() {
	// This ensures the fake client knows about our CRD
	_ = schema.GroupVersionResource{
		Group:    "grove.io",
		Version:  "v1alpha1",
		Resource: "podcliquesets",
	}
}
