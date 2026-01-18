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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-dynamo/grove/cli-plugin/internal/aic"
)

// runGenerate executes the generate command logic.
func runGenerate(g *GenerateCmd) error {
	// Validate parameters
	if err := validateGenerateParams(g); err != nil {
		return err
	}

	// Create the save directory if it doesn't exist
	if err := os.MkdirAll(g.SaveDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Check if AIConfigurator is available
	aicExecutor := aic.NewExecutor()
	if !aicExecutor.IsAvailable() {
		fmt.Println("Note: AIConfigurator CLI not found in PATH, using mock configuration")
	}

	// Build AIConfigurator input parameters
	params := aic.Params{
		Model:     g.Model,
		System:    g.System,
		TotalGPUs: g.TotalGPUs,
		Backend:   g.Backend,
		ISL:       g.ISL,
		OSL:       g.OSL,
		TTFT:      g.TTFT,
		TPOT:      g.TPOT,
	}

	// Run AIConfigurator (or mock)
	result, err := aicExecutor.Run(params)
	if err != nil {
		return fmt.Errorf("AIConfigurator execution failed: %w", err)
	}

	fmt.Println("\u2713 AIConfigurator completed successfully")
	fmt.Println()

	// Generate manifests for each plan
	renderer := aic.NewRenderer(g.Namespace, g.Image)
	var generatedFiles []aic.GeneratedFile

	for i, plan := range result.Plans {
		// Generate filename
		filename := generateFilename(g.Model, g.Backend, plan.Mode)
		filePath := filepath.Join(g.SaveDir, filename)

		// Render the PodCliqueSet manifest
		manifest, err := renderer.RenderPodCliqueSet(plan, g.Model, g.Backend)
		if err != nil {
			return fmt.Errorf("failed to render manifest for plan %d: %w", i+1, err)
		}

		// Write the manifest file
		if err := os.WriteFile(filePath, []byte(manifest), 0644); err != nil {
			return fmt.Errorf("failed to write manifest file: %w", err)
		}

		generatedFiles = append(generatedFiles, aic.GeneratedFile{
			Path: filePath,
			Plan: plan,
		})
	}

	// Print summary
	printGenerateSummary(generatedFiles)

	return nil
}

// validateGenerateParams validates the generate command parameters.
func validateGenerateParams(g *GenerateCmd) error {
	if g.Model == "" {
		return fmt.Errorf("--model is required")
	}
	if g.System == "" {
		return fmt.Errorf("--system is required")
	}
	if g.TotalGPUs <= 0 {
		return fmt.Errorf("--total-gpus must be positive")
	}
	if g.Backend == "" {
		return fmt.Errorf("--backend is required")
	}
	if g.ISL <= 0 {
		return fmt.Errorf("--isl must be positive")
	}
	if g.OSL <= 0 {
		return fmt.Errorf("--osl must be positive")
	}
	if g.TTFT <= 0 {
		return fmt.Errorf("--ttft must be positive")
	}
	if g.TPOT <= 0 {
		return fmt.Errorf("--tpot must be positive")
	}
	if g.SaveDir == "" {
		return fmt.Errorf("--save-dir is required")
	}

	// Validate backend
	validBackends := []string{"sglang", "vllm", "trt-llm"}
	isValidBackend := false
	for _, vb := range validBackends {
		if strings.EqualFold(g.Backend, vb) {
			isValidBackend = true
			break
		}
	}
	if !isValidBackend {
		return fmt.Errorf("--backend must be one of: %s", strings.Join(validBackends, ", "))
	}

	return nil
}

// generateFilename generates a filename for the manifest based on model and mode.
func generateFilename(model, backend, mode string) string {
	// Normalize model name for filename
	modelName := strings.ToLower(strings.ReplaceAll(model, "_", "-"))
	backendName := strings.ToLower(backend)

	// Generate filename based on mode
	var suffix string
	switch strings.ToLower(mode) {
	case "disaggregated", "prefill-decode disaggregated mode":
		suffix = "disagg"
	case "aggregated", "aggregated mode":
		suffix = "agg"
	default:
		suffix = strings.ToLower(strings.ReplaceAll(mode, " ", "-"))
	}

	return fmt.Sprintf("%s-%s-%s.yaml", modelName, backendName, suffix)
}

// printGenerateSummary prints a summary of the generated files.
func printGenerateSummary(files []aic.GeneratedFile) {
	for i, file := range files {
		fmt.Printf("Plan %d: %s\n", i+1, file.Plan.ModeName)
		fmt.Printf("  File: %s\n", file.Path)
		fmt.Println("  Configuration:")

		if file.Plan.PrefillWorkers > 0 {
			fmt.Printf("    - Prefill Workers: %d (tp%d, %d GPU each)\n",
				file.Plan.PrefillWorkers, file.Plan.PrefillTP, file.Plan.PrefillTP)
		}
		if file.Plan.DecodeWorkers > 0 {
			fmt.Printf("    - Decode Workers: %d (tp%d, %d GPUs)\n",
				file.Plan.DecodeWorkers, file.Plan.DecodeTP, file.Plan.DecodeTP)
		}
		if file.Plan.AggregatedWorkers > 0 {
			fmt.Printf("    - Workers: %d (tp%d, %d GPUs each)\n",
				file.Plan.AggregatedWorkers, file.Plan.AggregatedTP, file.Plan.AggregatedTP)
		}
		fmt.Printf("    - Total GPU Usage: %d\n", file.Plan.TotalGPUUsage)

		fmt.Println("  Expected Performance:")
		fmt.Printf("    - Throughput: %.0f tok/s/gpu\n", file.Plan.Throughput)
		fmt.Printf("    - TTFT: %.0fms\n", file.Plan.TTFT)
		fmt.Printf("    - TPOT: %.2fms\n", file.Plan.TPOT)
		fmt.Println()
	}

	// Print deployment instructions
	if len(files) > 0 {
		fmt.Println("To deploy:")
		fmt.Printf("  kubectl apply -f %s\n", files[0].Path)
		fmt.Println()
		fmt.Println("To store plan for later comparison:")
		fmt.Printf("  kubectl grove plan store my-inference -f %s\n", files[0].Path)
	}
}
