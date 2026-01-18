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

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestValidateGenerateParams(t *testing.T) {
	tests := []struct {
		name    string
		cmd     GenerateCmd
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid parameters",
			cmd: GenerateCmd{
				Model:     "QWEN3_32B",
				System:    "h200_sxm",
				TotalGPUs: 32,
				Backend:   "sglang",
				ISL:       4000,
				OSL:       1000,
				TTFT:      300,
				TPOT:      10,
				SaveDir:   "/tmp/output",
			},
			wantErr: false,
		},
		{
			name: "missing model",
			cmd: GenerateCmd{
				System:    "h200_sxm",
				TotalGPUs: 32,
				Backend:   "sglang",
				ISL:       4000,
				OSL:       1000,
				TTFT:      300,
				TPOT:      10,
				SaveDir:   "/tmp/output",
			},
			wantErr: true,
			errMsg:  "--model is required",
		},
		{
			name: "missing system",
			cmd: GenerateCmd{
				Model:     "QWEN3_32B",
				TotalGPUs: 32,
				Backend:   "sglang",
				ISL:       4000,
				OSL:       1000,
				TTFT:      300,
				TPOT:      10,
				SaveDir:   "/tmp/output",
			},
			wantErr: true,
			errMsg:  "--system is required",
		},
		{
			name: "invalid total GPUs",
			cmd: GenerateCmd{
				Model:     "QWEN3_32B",
				System:    "h200_sxm",
				TotalGPUs: 0,
				Backend:   "sglang",
				ISL:       4000,
				OSL:       1000,
				TTFT:      300,
				TPOT:      10,
				SaveDir:   "/tmp/output",
			},
			wantErr: true,
			errMsg:  "--total-gpus must be positive",
		},
		{
			name: "invalid backend",
			cmd: GenerateCmd{
				Model:     "QWEN3_32B",
				System:    "h200_sxm",
				TotalGPUs: 32,
				Backend:   "invalid",
				ISL:       4000,
				OSL:       1000,
				TTFT:      300,
				TPOT:      10,
				SaveDir:   "/tmp/output",
			},
			wantErr: true,
			errMsg:  "--backend must be one of",
		},
		{
			name: "invalid ISL",
			cmd: GenerateCmd{
				Model:     "QWEN3_32B",
				System:    "h200_sxm",
				TotalGPUs: 32,
				Backend:   "sglang",
				ISL:       0,
				OSL:       1000,
				TTFT:      300,
				TPOT:      10,
				SaveDir:   "/tmp/output",
			},
			wantErr: true,
			errMsg:  "--isl must be positive",
		},
		{
			name: "valid backend vllm",
			cmd: GenerateCmd{
				Model:     "QWEN3_32B",
				System:    "h200_sxm",
				TotalGPUs: 32,
				Backend:   "vllm",
				ISL:       4000,
				OSL:       1000,
				TTFT:      300,
				TPOT:      10,
				SaveDir:   "/tmp/output",
			},
			wantErr: false,
		},
		{
			name: "valid backend trt-llm",
			cmd: GenerateCmd{
				Model:     "QWEN3_32B",
				System:    "h200_sxm",
				TotalGPUs: 32,
				Backend:   "trt-llm",
				ISL:       4000,
				OSL:       1000,
				TTFT:      300,
				TPOT:      10,
				SaveDir:   "/tmp/output",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGenerateParams(&tt.cmd)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGenerateFilename(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		backend  string
		mode     string
		expected string
	}{
		{
			name:     "disaggregated mode",
			model:    "QWEN3_32B",
			backend:  "sglang",
			mode:     "disaggregated",
			expected: "qwen3-32b-sglang-disagg.yaml",
		},
		{
			name:     "aggregated mode",
			model:    "QWEN3_32B",
			backend:  "sglang",
			mode:     "aggregated",
			expected: "qwen3-32b-sglang-agg.yaml",
		},
		{
			name:     "prefill-decode mode name",
			model:    "QWEN3_32B",
			backend:  "sglang",
			mode:     "Prefill-Decode Disaggregated Mode",
			expected: "qwen3-32b-sglang-disagg.yaml",
		},
		{
			name:     "aggregated mode name",
			model:    "QWEN3_32B",
			backend:  "sglang",
			mode:     "Aggregated Mode",
			expected: "qwen3-32b-sglang-agg.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateFilename(tt.model, tt.backend, tt.mode)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRunGenerate(t *testing.T) {
	// Create a temporary directory for output
	tmpDir, err := os.MkdirTemp("", "grove-generate-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cmd := &GenerateCmd{
		Model:     "QWEN3_32B",
		System:    "h200_sxm",
		TotalGPUs: 32,
		Backend:   "sglang",
		ISL:       4000,
		OSL:       1000,
		TTFT:      300,
		TPOT:      10,
		SaveDir:   tmpDir,
		Namespace: "test-namespace",
		Image:     "test-image:latest",
	}

	err = runGenerate(cmd)
	require.NoError(t, err)

	// Verify files were created
	files, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(files), 1, "should create at least one file")

	// Verify at least one YAML file was created and is valid
	foundYAML := false
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".yaml" {
			foundYAML = true

			// Read and verify the YAML is valid
			content, err := os.ReadFile(filepath.Join(tmpDir, f.Name()))
			require.NoError(t, err)

			// Parse as generic YAML to verify structure
			var manifest map[string]interface{}
			err = yaml.Unmarshal(content, &manifest)
			require.NoError(t, err)

			// Verify basic PodCliqueSet structure
			assert.Equal(t, "grove.io/v1alpha1", manifest["apiVersion"])
			assert.Equal(t, "PodCliqueSet", manifest["kind"])

			// Verify metadata
			metadata, ok := manifest["metadata"].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, "test-namespace", metadata["namespace"])

			// Verify spec
			spec, ok := manifest["spec"].(map[string]interface{})
			require.True(t, ok)
			assert.Contains(t, spec, "replicas")
			assert.Contains(t, spec, "template")
		}
	}
	assert.True(t, foundYAML, "should create at least one YAML file")
}

func TestRunGenerateWithSmallGPUCount(t *testing.T) {
	// Test with small GPU count that only allows aggregated mode
	tmpDir, err := os.MkdirTemp("", "grove-generate-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cmd := &GenerateCmd{
		Model:     "LLAMA3_8B",
		System:    "a100_sxm",
		TotalGPUs: 2,
		Backend:   "vllm",
		ISL:       2000,
		OSL:       500,
		TTFT:      200,
		TPOT:      5,
		SaveDir:   tmpDir,
		Namespace: "default",
		Image:     "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.5.1",
	}

	err = runGenerate(cmd)
	require.NoError(t, err)

	// Verify files were created
	files, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(files), 1)
}
