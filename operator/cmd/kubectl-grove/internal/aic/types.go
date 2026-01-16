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

// Package aic provides AIConfigurator integration for kubectl-grove.
package aic

// Params represents the input parameters for AIConfigurator.
type Params struct {
	// Model is the model name (e.g., QWEN3_32B)
	Model string

	// System is the hardware system type (e.g., h200_sxm)
	System string

	// TotalGPUs is the total number of GPUs available
	TotalGPUs int

	// Backend is the inference backend (e.g., sglang, vllm)
	Backend string

	// ISL is the input sequence length
	ISL int

	// OSL is the output sequence length
	OSL int

	// TTFT is the target time-to-first-token in milliseconds
	TTFT int

	// TPOT is the target time-per-output-token in milliseconds
	TPOT int
}

// GeneratorResult represents the output from AIConfigurator.
type GeneratorResult struct {
	// Plans contains the list of deployment plans
	Plans []GeneratorPlan
}

// GeneratorPlan represents a single deployment plan from AIConfigurator.
type GeneratorPlan struct {
	// Mode is the deployment mode identifier (e.g., "disaggregated", "aggregated")
	Mode string

	// ModeName is the human-readable name of the mode
	ModeName string

	// PrefillWorkers is the number of prefill workers (for disaggregated mode)
	PrefillWorkers int

	// PrefillTP is the tensor parallelism for prefill workers
	PrefillTP int

	// PrefillPP is the pipeline parallelism for prefill workers
	PrefillPP int

	// DecodeWorkers is the number of decode workers (for disaggregated mode)
	DecodeWorkers int

	// DecodeTP is the tensor parallelism for decode workers
	DecodeTP int

	// DecodePP is the pipeline parallelism for decode workers
	DecodePP int

	// AggregatedWorkers is the number of workers (for aggregated mode)
	AggregatedWorkers int

	// AggregatedTP is the tensor parallelism for aggregated workers
	AggregatedTP int

	// AggregatedPP is the pipeline parallelism for aggregated workers
	AggregatedPP int

	// TotalGPUUsage is the total number of GPUs used by this plan
	TotalGPUUsage int

	// Throughput is the expected throughput in tokens/second/gpu
	Throughput float64

	// TTFT is the expected time-to-first-token in milliseconds
	TTFT float64

	// TPOT is the expected time-per-output-token in milliseconds
	TPOT float64
}

// GeneratedFile represents a generated manifest file.
type GeneratedFile struct {
	// Path is the absolute path to the generated file
	Path string

	// Plan is the plan that was used to generate this file
	Plan GeneratorPlan
}
