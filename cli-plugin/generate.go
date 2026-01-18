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
	if err := aic.CheckAIConfiguratorAvailable(); err != nil {
		return err
	}

	version := aic.GetAIConfiguratorVersion()
	if version != "" {
		fmt.Printf("Using AIConfigurator version: %s\n", version)
	}

	// Build AIConfigurator task config
	taskConfig := &aic.TaskConfig{
		ModelName:        g.Model,
		HuggingFaceID:    g.HuggingFaceID,
		SystemName:       g.System,
		DecodeSystemName: g.DecodeSystem,
		TotalGPUs:        g.TotalGPUs,
		BackendName:      g.Backend,
		BackendVersion:   g.BackendVersion,
		ISL:              g.ISL,
		OSL:              g.OSL,
		Prefix:           g.Prefix,
		TTFT:             float64(g.TTFT),
		TPOT:             float64(g.TPOT),
		RequestLatency:   float64(g.RequestLatency),
		DatabaseMode:     g.DatabaseMode,
		SaveDir:          g.SaveDir,
		Debug:            g.Debug,
	}

	// Execute AIConfigurator
	executor := aic.NewExecutor()
	if err := executor.Execute(taskConfig); err != nil {
		return fmt.Errorf("AIConfigurator execution failed: %w", err)
	}

	// Locate the output directory
	outputDir, err := aic.LocateOutputDirectory(taskConfig)
	if err != nil {
		return fmt.Errorf("failed to locate AIConfigurator output: %w", err)
	}

	fmt.Printf("\nFound AIConfigurator output: %s\n", outputDir)

	// Parse the generator configs
	aggConfig, disaggConfig, err := aic.ParseGeneratorConfigs(outputDir)
	if err != nil {
		return fmt.Errorf("failed to parse AIConfigurator output: %w", err)
	}

	// Generate PodCliqueSet manifests
	renderer := aic.NewRenderer(g.Namespace, g.Image)
	var generatedFiles []aic.GeneratedFile

	// Generate disaggregated mode manifest
	disaggPlan := &aic.DeploymentPlan{
		Mode:          aic.DeploymentModeDisagg,
		Config:        disaggConfig,
		OutputPath:    filepath.Join(g.SaveDir, generateFilename(g.Model, g.Backend, "disagg")),
		ModelName:     g.Model,
		BackendName:   g.Backend,
		HuggingFaceID: g.HuggingFaceID,
		Namespace:     g.Namespace,
		Image:         g.Image,
	}

	if err := renderer.RenderDeploymentYAML(disaggPlan); err != nil {
		return fmt.Errorf("failed to render disaggregated manifest: %w", err)
	}
	generatedFiles = append(generatedFiles, aic.GeneratedFile{
		Path: disaggPlan.OutputPath,
		Plan: disaggPlan,
	})

	// Generate aggregated mode manifest
	aggPlan := &aic.DeploymentPlan{
		Mode:          aic.DeploymentModeAgg,
		Config:        aggConfig,
		OutputPath:    filepath.Join(g.SaveDir, generateFilename(g.Model, g.Backend, "agg")),
		ModelName:     g.Model,
		BackendName:   g.Backend,
		HuggingFaceID: g.HuggingFaceID,
		Namespace:     g.Namespace,
		Image:         g.Image,
	}

	if err := renderer.RenderDeploymentYAML(aggPlan); err != nil {
		return fmt.Errorf("failed to render aggregated manifest: %w", err)
	}
	generatedFiles = append(generatedFiles, aic.GeneratedFile{
		Path: aggPlan.OutputPath,
		Plan: aggPlan,
	})

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
	validBackends := []string{aic.BackendSGLang, aic.BackendVLLM, aic.BackendTRTLLM}
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

	// Validate database mode if provided
	if g.DatabaseMode != "" {
		validModes := []string{aic.DatabaseModeSilicon, aic.DatabaseModeHybrid, aic.DatabaseModeEmpirical, aic.DatabaseModeSOL}
		isValidMode := false
		for _, vm := range validModes {
			if strings.EqualFold(g.DatabaseMode, vm) {
				isValidMode = true
				break
			}
		}
		if !isValidMode {
			return fmt.Errorf("--database-mode must be one of: %s", strings.Join(validModes, ", "))
		}
	}

	return nil
}

// generateFilename generates a filename for the manifest based on model and mode.
func generateFilename(model, backend, mode string) string {
	// Normalize model name for filename
	modelName := strings.ToLower(strings.ReplaceAll(model, "_", "-"))
	modelName = strings.ReplaceAll(modelName, "/", "-")
	backendName := strings.ToLower(backend)

	return fmt.Sprintf("%s-%s-%s-pcs.yaml", modelName, backendName, mode)
}

// printGenerateSummary prints a summary of the generated files.
func printGenerateSummary(files []aic.GeneratedFile) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("Generated PodCliqueSet Manifests")
	fmt.Println(strings.Repeat("=", 60))

	for _, file := range files {
		plan := file.Plan
		config := plan.Config

		fmt.Printf("\n%s Mode:\n", strings.ToUpper(plan.Mode))
		fmt.Printf("  File: %s\n", file.Path)
		fmt.Println("  Configuration:")

		if plan.Mode == aic.DeploymentModeDisagg {
			if config.Workers.PrefillWorkers > 0 {
				prefillParams := aic.GetWorkerParams(config.Params.Prefill)
				fmt.Printf("    - Prefill Workers: %d (tp%d, %d GPUs each)\n",
					config.Workers.PrefillWorkers,
					prefillParams.TensorParallelSize,
					config.Workers.PrefillGPUsPerWorker)
			}
			if config.Workers.DecodeWorkers > 0 {
				decodeParams := aic.GetWorkerParams(config.Params.Decode)
				fmt.Printf("    - Decode Workers: %d (tp%d, %d GPUs each)\n",
					config.Workers.DecodeWorkers,
					decodeParams.TensorParallelSize,
					config.Workers.DecodeGPUsPerWorker)
			}
		} else {
			if config.Workers.AggWorkers > 0 {
				aggParams := aic.GetWorkerParams(config.Params.Agg)
				fmt.Printf("    - Workers: %d (tp%d, %d GPUs each)\n",
					config.Workers.AggWorkers,
					aggParams.TensorParallelSize,
					config.Workers.AggGPUsPerWorker)
			}
		}
	}

	// Print deployment instructions
	if len(files) > 0 {
		fmt.Println("\n" + strings.Repeat("-", 60))
		fmt.Println("To deploy:")
		for _, file := range files {
			fmt.Printf("  kubectl apply -f %s\n", file.Path)
		}
		fmt.Println()
		fmt.Println("To store plan for later comparison:")
		fmt.Printf("  kubectl grove plan store my-inference -f %s\n", files[0].Path)
	}
}
