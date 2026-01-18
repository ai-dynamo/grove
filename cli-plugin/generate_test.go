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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			name: "valid backend trtllm",
			cmd: GenerateCmd{
				Model:     "QWEN3_32B",
				System:    "h200_sxm",
				TotalGPUs: 32,
				Backend:   "trtllm",
				ISL:       4000,
				OSL:       1000,
				TTFT:      300,
				TPOT:      10,
				SaveDir:   "/tmp/output",
			},
			wantErr: false,
		},
		{
			name: "valid database mode",
			cmd: GenerateCmd{
				Model:        "QWEN3_32B",
				System:       "h200_sxm",
				TotalGPUs:    32,
				Backend:      "sglang",
				ISL:          4000,
				OSL:          1000,
				TTFT:         300,
				TPOT:         10,
				SaveDir:      "/tmp/output",
				DatabaseMode: "HYBRID",
			},
			wantErr: false,
		},
		{
			name: "invalid database mode",
			cmd: GenerateCmd{
				Model:        "QWEN3_32B",
				System:       "h200_sxm",
				TotalGPUs:    32,
				Backend:      "sglang",
				ISL:          4000,
				OSL:          1000,
				TTFT:         300,
				TPOT:         10,
				SaveDir:      "/tmp/output",
				DatabaseMode: "INVALID",
			},
			wantErr: true,
			errMsg:  "--database-mode must be one of",
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
			name:     "disagg mode",
			model:    "QWEN3_32B",
			backend:  "sglang",
			mode:     "disagg",
			expected: "qwen3-32b-sglang-disagg-pcs.yaml",
		},
		{
			name:     "agg mode",
			model:    "QWEN3_32B",
			backend:  "sglang",
			mode:     "agg",
			expected: "qwen3-32b-sglang-agg-pcs.yaml",
		},
		{
			name:     "model with slash",
			model:    "Qwen/Qwen3-32B",
			backend:  "vllm",
			mode:     "disagg",
			expected: "qwen-qwen3-32b-vllm-disagg-pcs.yaml",
		},
		{
			name:     "model with underscore",
			model:    "LLAMA3_70B",
			backend:  "trtllm",
			mode:     "agg",
			expected: "llama3-70b-trtllm-agg-pcs.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateFilename(tt.model, tt.backend, tt.mode)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Note: Integration tests that require aiconfigurator to be installed are skipped
// when the CLI is not available. These tests exercise the full generate flow.
// To run them, ensure aiconfigurator >= 0.5.0 is installed:
//   pip install aiconfigurator
