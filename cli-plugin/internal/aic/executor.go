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
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Executor handles AIConfigurator CLI execution.
type Executor struct{}

// NewExecutor creates a new AIConfigurator executor.
func NewExecutor() *Executor {
	return &Executor{}
}

// IsAvailable checks if AIConfigurator CLI is installed and available.
func (e *Executor) IsAvailable() bool {
	return CheckAIConfiguratorAvailable() == nil
}

// Execute runs the aiconfigurator CLI with the given configuration.
// Output is streamed to stdout/stderr for real-time feedback.
func (e *Executor) Execute(config *TaskConfig) error {
	args := buildAIConfiguratorCommand(config)

	cmd := exec.Command(AIConfiguratorBinary, args...)

	// Stream output to stdout/stderr for real-time feedback
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Running AIConfigurator optimization...\n")
	fmt.Printf("Command: %s %s\n\n", AIConfiguratorBinary, strings.Join(args, " "))

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("aiconfigurator execution failed: %w\nPlease check the error output above", err)
	}

	fmt.Println("\n✓ AIConfigurator optimization completed successfully")
	return nil
}

// buildAIConfiguratorCommand constructs the aiconfigurator CLI command from TaskConfig.
func buildAIConfiguratorCommand(config *TaskConfig) []string {
	args := []string{"cli", "default"}

	// Required parameters
	args = append(args, "--model", config.ModelName)
	args = append(args, "--system", config.SystemName)
	args = append(args, "--total_gpus", strconv.Itoa(config.TotalGPUs))
	args = append(args, "--backend", config.BackendName)
	args = append(args, "--isl", strconv.Itoa(config.ISL))
	args = append(args, "--osl", strconv.Itoa(config.OSL))
	args = append(args, "--ttft", strconv.FormatFloat(config.TTFT, 'f', -1, 64))
	args = append(args, "--tpot", strconv.FormatFloat(config.TPOT, 'f', -1, 64))
	args = append(args, "--save_dir", config.SaveDir)

	// Database mode (default to SILICON if not specified)
	if config.DatabaseMode != "" {
		args = append(args, "--database_mode", config.DatabaseMode)
	} else {
		args = append(args, "--database_mode", DatabaseModeSilicon)
	}

	// Optional parameters
	if config.HuggingFaceID != "" {
		args = append(args, "--hf_id", config.HuggingFaceID)
	}

	if config.DecodeSystemName != "" && config.DecodeSystemName != config.SystemName {
		args = append(args, "--decode_system", config.DecodeSystemName)
	}

	if config.BackendVersion != "" && config.BackendVersion != "latest" {
		args = append(args, "--backend_version", config.BackendVersion)
	}

	if config.Prefix > 0 {
		args = append(args, "--prefix", strconv.Itoa(config.Prefix))
	}

	if config.RequestLatency > 0 {
		args = append(args, "--request_latency", strconv.FormatFloat(config.RequestLatency, 'f', -1, 64))
	}

	if config.Debug {
		args = append(args, "--debug")
	}

	return args
}
