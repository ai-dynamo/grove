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

package aic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestExecutor_Mock tests the AIConfigurator executor mock functionality.
func TestExecutor_Mock(t *testing.T) {
	executor := NewExecutor()

	// When aiconfigurator is not installed, it should use mock
	params := Params{
		Model:     "QWEN3_32B",
		System:    "h200_sxm",
		TotalGPUs: 32,
		Backend:   "sglang",
		ISL:       4000,
		OSL:       1000,
		TTFT:      300,
		TPOT:      10,
	}

	result, err := executor.Run(params)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.Plans)

	// Verify mock plans have reasonable values
	for _, plan := range result.Plans {
		assert.NotEmpty(t, plan.Mode)
		assert.NotEmpty(t, plan.ModeName)
		assert.Greater(t, plan.TotalGPUUsage, 0)
		assert.Greater(t, plan.Throughput, 0.0)
		assert.Greater(t, plan.TTFT, 0.0)
		assert.Greater(t, plan.TPOT, 0.0)
	}
}

func TestExecutor_MockDisaggregated(t *testing.T) {
	executor := NewExecutor()

	params := Params{
		Model:     "QWEN3_32B",
		System:    "h200_sxm",
		TotalGPUs: 8,
		Backend:   "sglang",
		ISL:       4000,
		OSL:       1000,
		TTFT:      300,
		TPOT:      10,
	}

	result, err := executor.Run(params)
	require.NoError(t, err)
	require.NotNil(t, result)

	// With 8 GPUs, we should get both disaggregated and aggregated plans
	require.GreaterOrEqual(t, len(result.Plans), 2)

	// Verify we have both modes
	hasDisagg := false
	hasAgg := false
	for _, plan := range result.Plans {
		if plan.Mode == "disaggregated" {
			hasDisagg = true
			assert.Greater(t, plan.PrefillWorkers, 0)
			assert.Greater(t, plan.DecodeWorkers, 0)
		}
		if plan.Mode == "aggregated" {
			hasAgg = true
			assert.Greater(t, plan.AggregatedWorkers, 0)
		}
	}
	assert.True(t, hasDisagg, "should have disaggregated plan")
	assert.True(t, hasAgg, "should have aggregated plan")
}

func TestExecutor_MockSmallGPU(t *testing.T) {
	executor := NewExecutor()

	// With less than 4 GPUs, we should only get aggregated mode
	params := Params{
		Model:     "LLAMA3_8B",
		System:    "a100_sxm",
		TotalGPUs: 2,
		Backend:   "vllm",
		ISL:       2000,
		OSL:       500,
		TTFT:      200,
		TPOT:      5,
	}

	result, err := executor.Run(params)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.GreaterOrEqual(t, len(result.Plans), 1)

	// Should only have aggregated mode with small GPU count
	for _, plan := range result.Plans {
		if plan.Mode == "disaggregated" {
			t.Error("should not have disaggregated plan with only 2 GPUs")
		}
	}
}

// TestParser_JSON tests the AIConfigurator output parser with JSON input.
func TestParser_JSON(t *testing.T) {
	parser := NewParser()

	jsonInput := `{
		"plans": [
			{
				"mode": "disaggregated",
				"mode_name": "Prefill-Decode Disaggregated Mode",
				"prefill_workers": 4,
				"prefill_tp": 1,
				"prefill_pp": 1,
				"decode_workers": 1,
				"decode_tp": 4,
				"decode_pp": 1,
				"total_gpu_usage": 8,
				"throughput": 804.5,
				"ttft": 486.0,
				"tpot": 9.16
			}
		]
	}`

	result, err := parser.Parse(jsonInput)
	require.NoError(t, err)
	require.Len(t, result.Plans, 1)

	plan := result.Plans[0]
	assert.Equal(t, "disaggregated", plan.Mode)
	assert.Equal(t, "Prefill-Decode Disaggregated Mode", plan.ModeName)
	assert.Equal(t, 4, plan.PrefillWorkers)
	assert.Equal(t, 1, plan.PrefillTP)
	assert.Equal(t, 1, plan.DecodeWorkers)
	assert.Equal(t, 4, plan.DecodeTP)
	assert.Equal(t, 8, plan.TotalGPUUsage)
	assert.Equal(t, 804.5, plan.Throughput)
	assert.Equal(t, 486.0, plan.TTFT)
	assert.Equal(t, 9.16, plan.TPOT)
}

func TestParser_Text(t *testing.T) {
	parser := NewParser()

	textInput := `
Plan 1: Prefill-Decode Disaggregated Mode
  Prefill Workers: 4 (tp1, 1 GPU each)
  Decode Workers: 1 (tp4, 4 GPUs)
  Total GPU Usage: 8
  Throughput: 804 tok/s/gpu
  TTFT: 486ms
  TPOT: 9.16ms

Plan 2: Aggregated Mode
  Workers: 8 (tp1, 1 GPU each)
  Total GPU Usage: 8
  Throughput: 500 tok/s/gpu
  TTFT: 300ms
  TPOT: 12.0ms
`

	result, err := parser.Parse(textInput)
	require.NoError(t, err)
	require.Len(t, result.Plans, 2)

	// Verify first plan (disaggregated)
	plan1 := result.Plans[0]
	assert.Equal(t, "disaggregated", plan1.Mode)
	assert.Equal(t, 4, plan1.PrefillWorkers)
	assert.Equal(t, 1, plan1.PrefillTP)
	assert.Equal(t, 1, plan1.DecodeWorkers)
	assert.Equal(t, 4, plan1.DecodeTP)
	assert.Equal(t, 8, plan1.TotalGPUUsage)
	assert.Equal(t, 804.0, plan1.Throughput)
	assert.Equal(t, 486.0, plan1.TTFT)
	assert.Equal(t, 9.16, plan1.TPOT)

	// Verify second plan (aggregated)
	plan2 := result.Plans[1]
	assert.Equal(t, "aggregated", plan2.Mode)
	assert.Equal(t, 8, plan2.AggregatedWorkers)
	assert.Equal(t, 1, plan2.AggregatedTP)
}

// TestRenderer_RenderPodCliqueSet tests the manifest renderer.
func TestRenderer_RenderPodCliqueSet(t *testing.T) {
	renderer := NewRenderer("test-namespace", "test-image:latest")

	tests := []struct {
		name    string
		plan    GeneratorPlan
		model   string
		backend string
	}{
		{
			name: "disaggregated mode",
			plan: GeneratorPlan{
				Mode:           "disaggregated",
				ModeName:       "Prefill-Decode Disaggregated Mode",
				PrefillWorkers: 4,
				PrefillTP:      1,
				DecodeWorkers:  1,
				DecodeTP:       4,
				TotalGPUUsage:  8,
			},
			model:   "QWEN3_32B",
			backend: "sglang",
		},
		{
			name: "aggregated mode",
			plan: GeneratorPlan{
				Mode:              "aggregated",
				ModeName:          "Aggregated Mode",
				AggregatedWorkers: 8,
				AggregatedTP:      1,
				TotalGPUUsage:     8,
			},
			model:   "LLAMA3_70B",
			backend: "vllm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest, err := renderer.RenderPodCliqueSet(tt.plan, tt.model, tt.backend)
			require.NoError(t, err)
			require.NotEmpty(t, manifest)

			// Parse the generated YAML
			var pcs map[string]interface{}
			err = yaml.Unmarshal([]byte(manifest), &pcs)
			require.NoError(t, err)

			// Verify basic structure
			assert.Equal(t, "grove.io/v1alpha1", pcs["apiVersion"])
			assert.Equal(t, "PodCliqueSet", pcs["kind"])

			metadata, ok := pcs["metadata"].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, "test-namespace", metadata["namespace"])

			spec, ok := pcs["spec"].(map[string]interface{})
			require.True(t, ok)

			template, ok := spec["template"].(map[string]interface{})
			require.True(t, ok)

			cliques, ok := template["cliques"].([]interface{})
			require.True(t, ok)

			if tt.plan.Mode == "disaggregated" {
				// Should have prefill and decode cliques
				assert.GreaterOrEqual(t, len(cliques), 2)
			} else {
				// Should have worker clique
				assert.GreaterOrEqual(t, len(cliques), 1)
			}

			// Verify clique structure
			for _, c := range cliques {
				clique, ok := c.(map[string]interface{})
				require.True(t, ok)
				assert.Contains(t, clique, "name")
				assert.Contains(t, clique, "spec")
			}
		})
	}
}

// TestRenderer_ResourceNameGeneration tests resource name generation.
func TestRenderer_ResourceNameGeneration(t *testing.T) {
	renderer := NewRenderer("default", "test:latest")

	tests := []struct {
		model    string
		backend  string
		mode     string
		contains []string
	}{
		{
			model:    "QWEN3_32B",
			backend:  "sglang",
			mode:     "disaggregated",
			contains: []string{"qwen3-32b", "sglang", "disagg"},
		},
		{
			model:    "LLAMA3/70B",
			backend:  "vllm",
			mode:     "aggregated",
			contains: []string{"llama3-70b", "vllm", "agg"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.model+"-"+tt.mode, func(t *testing.T) {
			plan := GeneratorPlan{Mode: tt.mode, AggregatedWorkers: 1, AggregatedTP: 1}
			manifest, err := renderer.RenderPodCliqueSet(plan, tt.model, tt.backend)
			require.NoError(t, err)

			var pcs map[string]interface{}
			err = yaml.Unmarshal([]byte(manifest), &pcs)
			require.NoError(t, err)

			metadata := pcs["metadata"].(map[string]interface{})
			name := metadata["name"].(string)

			for _, substr := range tt.contains {
				assert.Contains(t, name, substr)
			}

			// Verify name is valid Kubernetes name
			assert.LessOrEqual(t, len(name), 63)
		})
	}
}
