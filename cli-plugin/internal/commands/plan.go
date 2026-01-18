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

// Package commands provides CLI command implementations for kubectl-grove.
package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ai-dynamo/grove/cli-plugin/internal/aic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// PlanCmd represents the plan command group
type PlanCmd struct {
	Store PlanStoreCmd `cmd:"" help:"Store an AIConfigurator plan."`
	Show  PlanShowCmd  `cmd:"" help:"Show a stored AIConfigurator plan."`
	Diff  PlanDiffCmd  `cmd:"" help:"Compare a stored plan with deployed configuration."`
}

// PlanStoreCmd stores an AIConfigurator plan
type PlanStoreCmd struct {
	Name      string `arg:"" help:"Name of the plan (typically the PodCliqueSet name)."`
	File      string `short:"f" required:"" help:"Path to the plan file (JSON or YAML)."`
	Namespace string `short:"n" help:"Namespace to store the plan in." default:"default"`
}

// PlanShowCmd shows a stored AIConfigurator plan
type PlanShowCmd struct {
	Name      string `arg:"" help:"Name of the PodCliqueSet to show the plan for."`
	Namespace string `short:"n" help:"Namespace of the plan." default:"default"`
}

// PlanDiffCmd compares a stored plan with deployed configuration
type PlanDiffCmd struct {
	Name      string `arg:"" help:"Name of the PodCliqueSet to compare."`
	Namespace string `short:"n" help:"Namespace of the PodCliqueSet." default:"default"`
}

// Execute executes the plan store command
func (c *PlanStoreCmd) Execute(clientset kubernetes.Interface) error {
	ctx := context.Background()

	// Read the plan file
	content, err := os.ReadFile(c.File)
	if err != nil {
		return fmt.Errorf("failed to read plan file: %w", err)
	}

	// Parse the plan
	plan, err := aic.ParsePlanFromFile(content)
	if err != nil {
		return fmt.Errorf("failed to parse plan file: %w", err)
	}

	// Store the plan
	store := aic.NewPlanStore(clientset)
	if err := store.Store(ctx, c.Name, c.Namespace, plan); err != nil {
		return fmt.Errorf("failed to store plan: %w", err)
	}

	fmt.Printf("Plan '%s' stored successfully in namespace '%s'\n", c.Name, c.Namespace)
	return nil
}

// Execute executes the plan show command
func (c *PlanShowCmd) Execute(clientset kubernetes.Interface, out io.Writer) error {
	ctx := context.Background()

	store := aic.NewPlanStore(clientset)
	storedPlan, err := store.Get(ctx, c.Name, c.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get plan: %w", err)
	}

	// Format and print the plan
	formatPlanOutput(out, storedPlan)
	return nil
}

// Execute executes the plan diff command
func (c *PlanDiffCmd) Execute(clientset kubernetes.Interface, dynamicClient dynamic.Interface, out io.Writer) error {
	ctx := context.Background()

	// Get the stored plan
	store := aic.NewPlanStore(clientset)
	storedPlan, err := store.Get(ctx, c.Name, c.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get stored plan: %w", err)
	}

	// Get the deployed PodCliqueSet configuration
	deployed, err := getDeployedConfig(ctx, dynamicClient, c.Name, c.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get deployed configuration: %w", err)
	}

	// Compute and display the diff
	diffs := aic.ComputeDiff(&storedPlan.Plan, deployed)
	formatDiffOutput(out, c.Name, diffs)

	return nil
}

// formatPlanOutput formats a stored plan for display
func formatPlanOutput(out io.Writer, plan *aic.StoredPlan) {
	fmt.Fprintf(out, "Plan: %s\n", plan.Name)
	fmt.Fprintf(out, "Stored: %s\n", plan.StoredAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintln(out)

	fmt.Fprintf(out, "Model: %s\n", plan.Plan.Model)
	fmt.Fprintf(out, "System: %s\n", plan.Plan.System)
	fmt.Fprintf(out, "Mode: %s\n", plan.Plan.ServingMode)
	fmt.Fprintln(out)

	fmt.Fprintln(out, "Configuration:")
	fmt.Fprintf(out, "  Prefill Workers: %d (tp=%d)\n", plan.Plan.Config.PrefillWorkers, plan.Plan.Config.PrefillTP)
	fmt.Fprintf(out, "  Decode Workers: %d (tp=%d)\n", plan.Plan.Config.DecodeWorkers, plan.Plan.Config.DecodeTP)
	fmt.Fprintln(out)

	if plan.Plan.Expected.ThroughputTokensPerSecPerGPU > 0 ||
		plan.Plan.Expected.TTFTMs > 0 ||
		plan.Plan.Expected.TPOTMs > 0 {
		fmt.Fprintln(out, "Expected Performance:")
		if plan.Plan.Expected.ThroughputTokensPerSecPerGPU > 0 {
			fmt.Fprintf(out, "  Throughput: %.2f tok/s/gpu\n", plan.Plan.Expected.ThroughputTokensPerSecPerGPU)
		}
		if plan.Plan.Expected.TTFTMs > 0 {
			fmt.Fprintf(out, "  TTFT: %.2fms\n", plan.Plan.Expected.TTFTMs)
		}
		if plan.Plan.Expected.TPOTMs > 0 {
			fmt.Fprintf(out, "  TPOT: %.2fms\n", plan.Plan.Expected.TPOTMs)
		}
		fmt.Fprintln(out)
	}

	if plan.Plan.SLA.TTFTMs > 0 || plan.Plan.SLA.TPOTMs > 0 {
		fmt.Fprintln(out, "SLA Targets:")
		if plan.Plan.SLA.TTFTMs > 0 {
			fmt.Fprintf(out, "  TTFT: <=%0.fms\n", plan.Plan.SLA.TTFTMs)
		}
		if plan.Plan.SLA.TPOTMs > 0 {
			fmt.Fprintf(out, "  TPOT: <=%0.fms\n", plan.Plan.SLA.TPOTMs)
		}
	}
}

// formatDiffOutput formats the diff results as a table
func formatDiffOutput(out io.Writer, name string, diffs []aic.DiffResult) {
	fmt.Fprintf(out, "Plan vs Deployed: %s\n", name)
	fmt.Fprintln(out)

	// Calculate column widths
	settingWidth := len("Setting")
	plannedWidth := len("Planned")
	deployedWidth := len("Deployed")
	statusWidth := len("Status")

	for _, d := range diffs {
		if len(d.Setting) > settingWidth {
			settingWidth = len(d.Setting)
		}
		if len(d.Planned) > plannedWidth {
			plannedWidth = len(d.Planned)
		}
		if len(d.Deployed) > deployedWidth {
			deployedWidth = len(d.Deployed)
		}
	}

	// Add padding
	settingWidth += 2
	plannedWidth += 2
	deployedWidth += 2
	statusWidth += 2

	// Print table header
	printTableBorder(out, settingWidth, plannedWidth, deployedWidth, statusWidth, "top")
	printTableRow(out, settingWidth, plannedWidth, deployedWidth, statusWidth, "Setting", "Planned", "Deployed", "Status")
	printTableBorder(out, settingWidth, plannedWidth, deployedWidth, statusWidth, "middle")

	// Print rows
	hasDifferences := false
	for _, d := range diffs {
		status := "Match"
		if !d.Matches {
			status = "Differs"
			hasDifferences = true
		}
		printTableRow(out, settingWidth, plannedWidth, deployedWidth, statusWidth, d.Setting, d.Planned, d.Deployed, status)
	}

	printTableBorder(out, settingWidth, plannedWidth, deployedWidth, statusWidth, "bottom")

	// Print note if there are differences
	if hasDifferences {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Note: Deployed config differs from plan. Review changes.")
	}
}

// printTableBorder prints a table border
func printTableBorder(out io.Writer, w1, w2, w3, w4 int, position string) {
	var left, mid, right, fill string

	switch position {
	case "top":
		left, mid, right, fill = "+", "+", "+", "-"
	case "middle":
		left, mid, right, fill = "+", "+", "+", "-"
	case "bottom":
		left, mid, right, fill = "+", "+", "+", "-"
	}

	fmt.Fprintf(out, "%s%s%s%s%s%s%s%s%s\n",
		left, strings.Repeat(fill, w1+1),
		mid, strings.Repeat(fill, w2+1),
		mid, strings.Repeat(fill, w3+1),
		mid, strings.Repeat(fill, w4+1),
		right)
}

// printTableRow prints a table row
func printTableRow(out io.Writer, w1, w2, w3, w4 int, c1, c2, c3, c4 string) {
	fmt.Fprintf(out, "| %-*s | %-*s | %-*s | %-*s |\n", w1, c1, w2, c2, w3, c3, w4, c4)
}

// getDeployedConfig extracts configuration from a deployed PodCliqueSet
func getDeployedConfig(ctx context.Context, dynamicClient dynamic.Interface, name, namespace string) (*aic.DeployedConfig, error) {
	// Define the GVR for PodCliqueSet
	pcsGVR := schema.GroupVersionResource{
		Group:    "grove.io",
		Version:  "v1alpha1",
		Resource: "podcliquesets",
	}

	// Get the PodCliqueSet
	pcs, err := dynamicClient.Resource(pcsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get PodCliqueSet %s: %w", name, err)
	}

	config := &aic.DeployedConfig{}

	// Extract configuration from the PodCliqueSet spec
	spec, ok := pcs.Object["spec"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid PodCliqueSet spec")
	}

	template, ok := spec["template"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid PodCliqueSet template")
	}

	// Extract from scaling groups if present
	if scalingGroups, ok := template["podCliqueScalingGroups"].([]interface{}); ok {
		for _, sg := range scalingGroups {
			sgMap, ok := sg.(map[string]interface{})
			if !ok {
				continue
			}

			name, _ := sgMap["name"].(string)
			replicas := int(1)
			if r, ok := sgMap["replicas"].(int64); ok {
				replicas = int(r)
			} else if r, ok := sgMap["replicas"].(float64); ok {
				replicas = int(r)
			}

			// Identify prefill vs decode workers by name convention
			nameLower := strings.ToLower(name)
			if strings.Contains(nameLower, "prefill") {
				config.PrefillWorkers = replicas
			} else if strings.Contains(nameLower, "decode") {
				config.DecodeWorkers = replicas
			}
		}
	}

	// Extract from cliques for TP values
	if cliques, ok := template["cliques"].([]interface{}); ok {
		for _, clique := range cliques {
			cliqueMap, ok := clique.(map[string]interface{})
			if !ok {
				continue
			}

			name, _ := cliqueMap["name"].(string)
			nameLower := strings.ToLower(name)

			// Get replicas as a proxy for TP
			cliqueSpec, ok := cliqueMap["spec"].(map[string]interface{})
			if !ok {
				continue
			}

			replicas := int(1)
			if r, ok := cliqueSpec["replicas"].(int64); ok {
				replicas = int(r)
			} else if r, ok := cliqueSpec["replicas"].(float64); ok {
				replicas = int(r)
			}

			// Identify prefill vs decode by name convention
			if strings.Contains(nameLower, "prefill") {
				config.PrefillTP = replicas
				// If no scaling group provided workers, use clique replicas
				if config.PrefillWorkers == 0 {
					config.PrefillWorkers = replicas
				}
			} else if strings.Contains(nameLower, "decode") {
				config.DecodeTP = replicas
				// If no scaling group provided workers, use clique replicas
				if config.DecodeWorkers == 0 {
					config.DecodeWorkers = replicas
				}
			}
		}
	}

	return config, nil
}
