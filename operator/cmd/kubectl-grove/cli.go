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
	"context"
	"fmt"
	"time"

	"github.com/ai-dynamo/grove/operator/internal/diagnostics"
	"github.com/alecthomas/kong"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Version is the kubectl-grove version, set at build time
var Version = "dev"

// CLI defines the kubectl-grove command-line interface
type CLI struct {
	// Namespace is the namespace to collect diagnostics from
	Namespace string `short:"n" help:"Namespace to collect diagnostics from." default:"default"`

	// OutputDir is the output directory for diagnostics
	OutputDir string `short:"o" help:"Output directory for diagnostics. Defaults to ./grove-diagnostics-{timestamp}." placeholder:"DIR"`

	// Kubeconfig is the path to the kubeconfig file
	Kubeconfig string `help:"Path to kubeconfig file." env:"KUBECONFIG" placeholder:"FILE"`

	// Context is the Kubernetes context to use
	Context string `help:"Kubernetes context to use." placeholder:"NAME"`

	// OperatorNamespace is the namespace where the Grove operator is deployed
	OperatorNamespace string `help:"Namespace where Grove operator is deployed." default:"grove-system"`

	// Version flag to print version and exit
	Version VersionFlag `short:"v" help:"Print version and exit."`
}

// VersionFlag is a custom flag type that prints the version and exits
type VersionFlag bool

// BeforeApply implements kong.BeforeApply to handle --version flag
//
//nolint:unparam // Kong requires this signature even though we always return nil
func (v VersionFlag) BeforeApply(app *kong.Kong) error {
	fmt.Printf("kubectl-grove version %s\n", Version)
	app.Exit(0)
	return nil
}

// Run executes the diagnostics collection
func (c *CLI) Run() error {
	fmt.Println("kubectl-grove - Grove Cluster Diagnostics")
	fmt.Println("==========================================")
	fmt.Println()

	// Build Kubernetes clients
	clientset, dynamicClient, err := c.buildClients()
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}

	// Determine output directory
	outputDir := c.OutputDir
	if outputDir == "" {
		timestamp := time.Now().Format("2006-01-02-150405")
		outputDir = fmt.Sprintf("grove-diagnostics-%s", timestamp)
	}

	fmt.Printf("Collecting diagnostics from namespace: %s\n", c.Namespace)
	fmt.Printf("Operator namespace: %s\n", c.OperatorNamespace)
	fmt.Printf("Output directory: %s\n", outputDir)
	fmt.Println()

	// Create diagnostic context
	ctx := context.Background()
	dc := diagnostics.NewDiagnosticContext(ctx, clientset, dynamicClient, c.Namespace)
	dc.OperatorNamespace = c.OperatorNamespace

	// Create file output
	output, err := diagnostics.NewFileOutput(outputDir, true)
	if err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Collect diagnostics
	if err := diagnostics.CollectAllDiagnostics(dc, output); err != nil {
		return fmt.Errorf("failed to collect diagnostics: %w", err)
	}

	return nil
}

// buildClients creates Kubernetes clients from kubeconfig
func (c *CLI) buildClients() (*kubernetes.Clientset, dynamic.Interface, error) {
	// Build config from kubeconfig
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if c.Kubeconfig != "" {
		loadingRules.ExplicitPath = c.Kubeconfig
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	if c.Context != "" {
		configOverrides.CurrentContext = c.Context
	}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	// Create dynamic client
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return clientset, dynamicClient, nil
}

