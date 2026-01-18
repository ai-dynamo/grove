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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestBuildAIConfiguratorCommand tests the command building for aiconfigurator.
func TestBuildAIConfiguratorCommand(t *testing.T) {
	config := &TaskConfig{
		ModelName:        "QWEN3_32B",
		SystemName:       "h200_sxm",
		TotalGPUs:        32,
		BackendName:      "sglang",
		ISL:              4000,
		OSL:              1000,
		TTFT:             300,
		TPOT:             10,
		SaveDir:          "/tmp/output",
		DatabaseMode:     "SILICON",
		HuggingFaceID:    "Qwen/Qwen3-32B",
		DecodeSystemName: "h100_sxm",
		BackendVersion:   "0.5.0",
	}

	args := buildAIConfiguratorCommand(config)

	// Verify required arguments
	assert.Contains(t, args, "cli")
	assert.Contains(t, args, "default")
	assert.Contains(t, args, "--model")
	assert.Contains(t, args, "QWEN3_32B")
	assert.Contains(t, args, "--system")
	assert.Contains(t, args, "h200_sxm")
	assert.Contains(t, args, "--total_gpus")
	assert.Contains(t, args, "32")
	assert.Contains(t, args, "--backend")
	assert.Contains(t, args, "sglang")
	assert.Contains(t, args, "--save_dir")
	assert.Contains(t, args, "/tmp/output")
	assert.Contains(t, args, "--database_mode")
	assert.Contains(t, args, "SILICON")

	// Verify optional arguments
	assert.Contains(t, args, "--hf_id")
	assert.Contains(t, args, "Qwen/Qwen3-32B")
	assert.Contains(t, args, "--decode_system")
	assert.Contains(t, args, "h100_sxm")
	assert.Contains(t, args, "--backend_version")
	assert.Contains(t, args, "0.5.0")
}

func TestBuildAIConfiguratorCommand_MinimalArgs(t *testing.T) {
	config := &TaskConfig{
		ModelName:   "LLAMA3_70B",
		SystemName:  "a100_sxm",
		TotalGPUs:   8,
		BackendName: "vllm",
		ISL:         2000,
		OSL:         500,
		TTFT:        200,
		TPOT:        5,
		SaveDir:     "/tmp/test",
	}

	args := buildAIConfiguratorCommand(config)

	// Should have required args
	assert.Contains(t, args, "cli")
	assert.Contains(t, args, "default")
	assert.Contains(t, args, "--model")
	assert.Contains(t, args, "--system")
	assert.Contains(t, args, "--total_gpus")
	assert.Contains(t, args, "--backend")
	assert.Contains(t, args, "--save_dir")

	// Should NOT have optional args when not specified
	assert.NotContains(t, args, "--hf_id")
	assert.NotContains(t, args, "--decode_system")
	assert.NotContains(t, args, "--backend_version")
}

// TestVersionCheck tests the version comparison logic.
func TestVersionCheck(t *testing.T) {
	tests := []struct {
		actual   string
		required string
		wantErr  bool
	}{
		{"0.5.0", "0.5.0", false},
		{"0.6.0", "0.5.0", false},
		{"1.0.0", "0.5.0", false},
		{"0.5.1", "0.5.0", false},
		{"0.4.0", "0.5.0", true},
		{"0.4.9", "0.5.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.actual+"_vs_"+tt.required, func(t *testing.T) {
			err := checkMinVersion(tt.actual, tt.required)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestParseGeneratorConfig tests parsing of generator_config.yaml files.
func TestParseGeneratorConfig(t *testing.T) {
	// Create a temporary directory with test config
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "disagg", "top1")
	require.NoError(t, os.MkdirAll(configDir, 0755))

	// Create a sample generator_config.yaml
	configContent := `
k8s:
  name_prefix: "qwen3-32b"
  k8s_namespace: "inference"
  k8s_image: "nvcr.io/nvidia/ai-dynamo/sglang:0.5.0"
  mode: "disagg"
  router_mode: "kv"
  is_kv: true
  enable_router: true
workers:
  prefill_workers: 4
  decode_workers: 8
  prefill_gpus_per_worker: 1
  decode_gpus_per_worker: 4
params:
  prefill:
    tensor_parallel_size: 1
    pipeline_parallel_size: 1
  decode:
    tensor_parallel_size: 4
    pipeline_parallel_size: 1
`
	configPath := filepath.Join(configDir, "generator_config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0644))

	// Parse the config
	config, err := parseGeneratorConfig(configPath)
	require.NoError(t, err)

	// Verify K8s config
	assert.Equal(t, "qwen3-32b", config.K8s.NamePrefix)
	assert.Equal(t, "inference", config.K8s.K8sNamespace)
	assert.Equal(t, "disagg", config.K8s.Mode)
	assert.True(t, config.K8s.IsKV)
	assert.True(t, config.K8s.EnableRouter)

	// Verify workers config
	assert.Equal(t, 4, config.Workers.PrefillWorkers)
	assert.Equal(t, 8, config.Workers.DecodeWorkers)
	assert.Equal(t, 1, config.Workers.PrefillGPUsPerWorker)
	assert.Equal(t, 4, config.Workers.DecodeGPUsPerWorker)

	// Verify params
	prefillParams := GetWorkerParams(config.Params.Prefill)
	assert.Equal(t, 1, prefillParams.TensorParallelSize)
	assert.Equal(t, 1, prefillParams.PipelineParallelSize)

	decodeParams := GetWorkerParams(config.Params.Decode)
	assert.Equal(t, 4, decodeParams.TensorParallelSize)
	assert.Equal(t, 1, decodeParams.PipelineParallelSize)
}

// TestLocateOutputDirectory tests finding the aiconfigurator output directory.
func TestLocateOutputDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a directory matching the expected pattern
	outputDirName := "QWEN3_32B_isl4000_osl1000_ttft300_tpot10_abc123"
	outputDir := filepath.Join(tmpDir, outputDirName)
	require.NoError(t, os.MkdirAll(filepath.Join(outputDir, "agg"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(outputDir, "disagg"), 0755))

	config := &TaskConfig{
		ModelName: "QWEN3_32B",
		ISL:       4000,
		OSL:       1000,
		TTFT:      300,
		TPOT:      10,
		SaveDir:   tmpDir,
	}

	foundDir, err := LocateOutputDirectory(config)
	require.NoError(t, err)
	assert.Equal(t, outputDir, foundDir)
}

func TestLocateOutputDirectory_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	config := &TaskConfig{
		ModelName: "NONEXISTENT",
		ISL:       1000,
		OSL:       500,
		TTFT:      100,
		TPOT:      5,
		SaveDir:   tmpDir,
	}

	_, err := LocateOutputDirectory(config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no output directory found")
}

// TestGetWorkerParams tests extracting worker params from a map.
func TestGetWorkerParams(t *testing.T) {
	params := map[string]interface{}{
		"tensor_parallel_size":    4,
		"pipeline_parallel_size":  2,
		"data_parallel_size":      1,
		"moe_tensor_parallel_size": 8,
		"moe_expert_parallel_size": 16,
	}

	wp := GetWorkerParams(params)
	assert.Equal(t, 4, wp.TensorParallelSize)
	assert.Equal(t, 2, wp.PipelineParallelSize)
	assert.Equal(t, 1, wp.DataParallelSize)
	assert.Equal(t, 8, wp.MoETensorParallelSize)
	assert.Equal(t, 16, wp.MoEExpertParallelSize)
}

func TestGetWorkerParams_Float64Values(t *testing.T) {
	// YAML parsing often produces float64 for numbers
	params := map[string]interface{}{
		"tensor_parallel_size":   float64(4),
		"pipeline_parallel_size": float64(2),
	}

	wp := GetWorkerParams(params)
	assert.Equal(t, 4, wp.TensorParallelSize)
	assert.Equal(t, 2, wp.PipelineParallelSize)
}

func TestGetWorkerParams_NilMap(t *testing.T) {
	wp := GetWorkerParams(nil)
	assert.Equal(t, 0, wp.TensorParallelSize)
	assert.Equal(t, 0, wp.PipelineParallelSize)
}

// TestRenderer_RenderPodCliqueSet tests the manifest renderer.
func TestRenderer_RenderPodCliqueSet(t *testing.T) {
	renderer := NewRenderer("test-namespace", "test-image:latest")

	// Create a disaggregated deployment plan
	disaggConfig := &GeneratorConfig{
		K8s: K8sConfig{
			Mode:       "disagg",
			NamePrefix: "qwen3-32b",
		},
		Workers: WorkersConfig{
			PrefillWorkers:       4,
			DecodeWorkers:        8,
			PrefillGPUsPerWorker: 1,
			DecodeGPUsPerWorker:  4,
		},
		Params: ParamsConfig{
			Prefill: map[string]interface{}{
				"tensor_parallel_size":   1,
				"pipeline_parallel_size": 1,
			},
			Decode: map[string]interface{}{
				"tensor_parallel_size":   4,
				"pipeline_parallel_size": 1,
			},
		},
	}

	plan := &DeploymentPlan{
		Mode:        DeploymentModeDisagg,
		Config:      disaggConfig,
		ModelName:   "QWEN3_32B",
		BackendName: "sglang",
	}

	manifest, err := renderer.RenderPodCliqueSet(plan)
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

	// Should have prefill and decode cliques
	assert.Equal(t, 2, len(cliques))

	// Verify clique names
	cliqueNames := make([]string, 0)
	for _, c := range cliques {
		clique, ok := c.(map[string]interface{})
		require.True(t, ok)
		cliqueNames = append(cliqueNames, clique["name"].(string))
	}
	assert.Contains(t, cliqueNames, "prefill")
	assert.Contains(t, cliqueNames, "decode")
}

func TestRenderer_RenderPodCliqueSet_Aggregated(t *testing.T) {
	renderer := NewRenderer("default", "test-image:latest")

	aggConfig := &GeneratorConfig{
		K8s: K8sConfig{
			Mode:       "agg",
			NamePrefix: "llama3-70b",
		},
		Workers: WorkersConfig{
			AggWorkers:       8,
			AggGPUsPerWorker: 1,
		},
		Params: ParamsConfig{
			Agg: map[string]interface{}{
				"tensor_parallel_size":   1,
				"pipeline_parallel_size": 1,
			},
		},
	}

	plan := &DeploymentPlan{
		Mode:        DeploymentModeAgg,
		Config:      aggConfig,
		ModelName:   "LLAMA3_70B",
		BackendName: "vllm",
	}

	manifest, err := renderer.RenderPodCliqueSet(plan)
	require.NoError(t, err)

	var pcs map[string]interface{}
	err = yaml.Unmarshal([]byte(manifest), &pcs)
	require.NoError(t, err)

	spec := pcs["spec"].(map[string]interface{})
	template := spec["template"].(map[string]interface{})
	cliques := template["cliques"].([]interface{})

	// Should have only worker clique
	assert.Equal(t, 1, len(cliques))
	workerClique := cliques[0].(map[string]interface{})
	assert.Equal(t, "worker", workerClique["name"])
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
			mode:     DeploymentModeDisagg,
			contains: []string{"qwen3-32b", "sglang", "disagg"},
		},
		{
			model:    "LLAMA3/70B",
			backend:  "vllm",
			mode:     DeploymentModeAgg,
			contains: []string{"llama3-70b", "vllm", "agg"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.model+"-"+tt.mode, func(t *testing.T) {
			name := renderer.generateResourceName(tt.model, tt.backend, tt.mode)

			for _, substr := range tt.contains {
				assert.Contains(t, name, substr)
			}

			// Verify name is valid Kubernetes name
			assert.LessOrEqual(t, len(name), 63)
		})
	}
}
