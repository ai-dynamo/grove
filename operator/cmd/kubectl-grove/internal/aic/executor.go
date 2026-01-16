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
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
)

// Executor handles AIConfigurator CLI execution.
type Executor struct {
	// binaryPath is the path to the AIConfigurator binary
	binaryPath string
}

// NewExecutor creates a new AIConfigurator executor.
func NewExecutor() *Executor {
	return &Executor{
		binaryPath: "aiconfigurator",
	}
}

// IsAvailable checks if AIConfigurator CLI is installed and available.
// Note: This only checks if the binary exists, not if it's the right version.
func (e *Executor) IsAvailable() bool {
	// For now, always return false to use mock mode
	// TODO: Implement proper version checking once AIConfigurator interface is finalized
	return false
}

// Run executes AIConfigurator with the given parameters.
// If AIConfigurator is not available, it returns mock results.
func (e *Executor) Run(params Params) (*GeneratorResult, error) {
	if !e.IsAvailable() {
		return e.runMock(params)
	}
	return e.runReal(params)
}

// runReal executes the actual AIConfigurator CLI.
func (e *Executor) runReal(params Params) (*GeneratorResult, error) {
	args := e.buildArgs(params)

	cmd := exec.Command(e.binaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("AIConfigurator failed: %w\nstderr: %s", err, stderr.String())
	}

	// Parse the output
	parser := NewParser()
	result, err := parser.Parse(stdout.String())
	if err != nil {
		return nil, fmt.Errorf("failed to parse AIConfigurator output: %w", err)
	}

	return result, nil
}

// runMock generates mock results when AIConfigurator is not available.
func (e *Executor) runMock(params Params) (*GeneratorResult, error) {
	// Generate mock plans based on input parameters
	plans := e.generateMockPlans(params)
	return &GeneratorResult{Plans: plans}, nil
}

// buildArgs builds the command-line arguments for AIConfigurator.
func (e *Executor) buildArgs(params Params) []string {
	return []string{
		"--model", params.Model,
		"--system", params.System,
		"--total-gpus", strconv.Itoa(params.TotalGPUs),
		"--backend", params.Backend,
		"--isl", strconv.Itoa(params.ISL),
		"--osl", strconv.Itoa(params.OSL),
		"--ttft", strconv.Itoa(params.TTFT),
		"--tpot", strconv.Itoa(params.TPOT),
	}
}

// generateMockPlans generates mock deployment plans for testing.
func (e *Executor) generateMockPlans(params Params) []GeneratorPlan {
	plans := []GeneratorPlan{}

	// Calculate mock configurations based on total GPUs
	// Disaggregated mode: split GPUs between prefill and decode workers
	if params.TotalGPUs >= 4 {
		prefillGPUs := params.TotalGPUs / 2
		decodeGPUs := params.TotalGPUs - prefillGPUs

		// Determine tensor parallelism based on model
		prefillTP := 1
		decodeTP := calculateTP(decodeGPUs)

		disaggPlan := GeneratorPlan{
			Mode:           "disaggregated",
			ModeName:       "Prefill-Decode Disaggregated Mode",
			PrefillWorkers: prefillGPUs / prefillTP,
			PrefillTP:      prefillTP,
			PrefillPP:      1,
			DecodeWorkers:  decodeGPUs / decodeTP,
			DecodeTP:       decodeTP,
			DecodePP:       1,
			TotalGPUUsage:  params.TotalGPUs,
			Throughput:     calculateMockThroughput(params, true),
			TTFT:           float64(params.TTFT) * 1.5, // Mock: 50% higher than target
			TPOT:           float64(params.TPOT) * 0.9, // Mock: 10% better than target
		}
		plans = append(plans, disaggPlan)
	}

	// Aggregated mode: all GPUs run the same workload
	aggTP := calculateTP(params.TotalGPUs)
	numWorkers := params.TotalGPUs / aggTP

	aggPlan := GeneratorPlan{
		Mode:              "aggregated",
		ModeName:          "Aggregated Mode",
		AggregatedWorkers: numWorkers,
		AggregatedTP:      aggTP,
		AggregatedPP:      1,
		TotalGPUUsage:     numWorkers * aggTP,
		Throughput:        calculateMockThroughput(params, false),
		TTFT:              float64(params.TTFT) * 1.2, // Mock: 20% higher than target
		TPOT:              float64(params.TPOT) * 1.1, // Mock: 10% higher than target
	}
	plans = append(plans, aggPlan)

	return plans
}

// calculateTP calculates a reasonable tensor parallelism value.
func calculateTP(gpus int) int {
	// Use power of 2 TP values that divide the GPU count
	for _, tp := range []int{8, 4, 2, 1} {
		if gpus >= tp && gpus%tp == 0 {
			return tp
		}
	}
	return 1
}

// calculateMockThroughput calculates mock throughput based on parameters.
func calculateMockThroughput(params Params, isDisaggregated bool) float64 {
	// Base throughput varies by backend and mode
	baseThroughput := 500.0

	if isDisaggregated {
		baseThroughput = 800.0 // Disaggregated mode is typically more efficient
	}

	// Adjust based on ISL/OSL ratio
	if params.ISL > 0 && params.OSL > 0 {
		ratio := float64(params.ISL) / float64(params.OSL)
		if ratio > 4 {
			baseThroughput *= 1.2 // Higher ISL/OSL ratio benefits disaggregation
		}
	}

	return baseThroughput
}
