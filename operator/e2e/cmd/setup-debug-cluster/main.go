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
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"

	"github.com/alecthomas/kong"
	"golang.org/x/term"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// CLI defines the command-line interface using Kong struct tags.
// All cluster configuration options default to the values from setup.DefaultE2EClusterConfig()
// to ensure consistency with e2e tests.
type CLI struct {
	// Cluster configuration overrides (defaults come from setup.DefaultE2EClusterConfig())
	Name              *string `name:"name" help:"Name of the K3D cluster"`
	ControlPlaneNodes *int    `name:"control-plane-nodes" help:"Number of control plane nodes"`
	WorkerNodes       *int    `name:"worker-nodes" help:"Number of worker nodes"`
	K3sImage          *string `name:"k3s-image" help:"K3s Docker image to use"`
	APIPort           *string `name:"api-port" help:"Port on host to expose Kubernetes API"`
	LBPort            *string `name:"lb-port" help:"Load balancer port mapping (host:container)"`
	WorkerMemory      *string `name:"worker-memory" help:"Memory allocation for worker nodes"`
	EnableRegistry    *bool   `name:"enable-registry" help:"Enable built-in Docker registry" negatable:""`
	RegistryPort      *string `name:"registry-port" help:"Port for the Docker registry"`

	// Deployment options
	SkaffoldPath string `name:"skaffold-path" help:"Path to skaffold.yaml (defaults to repo root)" type:"path"`

	// Test images
	TestImages []string `name:"test-images" help:"Test images to pre-load into registry" default:"nginx:alpine-slim"`

	// Logging
	Verbose bool `name:"verbose" short:"v" help:"Enable verbose logging"`
	Quiet   bool `name:"quiet" short:"q" help:"Suppress non-error output"`
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("setup-debug-cluster"),
		kong.Description("Create a K3D cluster with Grove operator for local development and debugging.\n\n"+
			"This command handles all setup steps including:\n"+
			"  - Creating the K3D cluster\n"+
			"  - Setting up a Docker registry\n"+
			"  - Pre-pulling and caching images\n"+
			"  - Deploying Grove operator via Skaffold\n"+
			"  - Installing Kai scheduler via Helm"),
		kong.UsageOnError(),
	)

	if err := run(&cli); err != nil {
		ctx.FatalIfErrorf(err)
	}
}

// run executes the main logic for setting up the debug cluster.
func run(cli *CLI) error {
	// Set up logging
	logger := utils.NewTestLogger(getLogLevel(cli))

	// Start with the default cluster configuration
	// This includes all node labels and taints required for Grove e2e testing
	cfg := setup.DefaultClusterConfig()

	// Apply CLI overrides if provided
	if cli.Name != nil {
		cfg.Name = *cli.Name
	}
	if cli.ControlPlaneNodes != nil {
		cfg.ControlPlaneNodes = *cli.ControlPlaneNodes
	}
	if cli.WorkerNodes != nil {
		cfg.WorkerNodes = *cli.WorkerNodes
	}
	if cli.K3sImage != nil {
		cfg.Image = *cli.K3sImage
	}
	if cli.APIPort != nil {
		cfg.HostPort = *cli.APIPort
	}
	if cli.LBPort != nil {
		cfg.LoadBalancerPort = *cli.LBPort
	}
	if cli.WorkerMemory != nil {
		cfg.WorkerMemory = *cli.WorkerMemory
	}
	if cli.EnableRegistry != nil {
		cfg.EnableRegistry = *cli.EnableRegistry
	}
	if cli.RegistryPort != nil {
		cfg.RegistryPort = *cli.RegistryPort
	}

	// Determine skaffold path
	skaffoldPath := cli.SkaffoldPath
	if skaffoldPath == "" {
		// Find skaffold.yaml relative to git repo root
		skaffoldPath = findSkaffoldYAML()
		if skaffoldPath == "" {
			return fmt.Errorf("could not find skaffold.yaml. Are you running from within the grove repo? You can also specify the path with --skaffold-path")
		}
		logger.Debugf("Using skaffold.yaml from: %s", skaffoldPath)
	} else {
		// Validate specified skaffold path exists
		if _, err := os.Stat(skaffoldPath); err != nil {
			return fmt.Errorf("skaffold file not found at %s: %w", skaffoldPath, err)
		}
	}

	// Create context that cancels on SIGINT/SIGTERM
	runCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Print configuration
	if !cli.Quiet {
		printConfiguration(&cfg, logger)
	}

	// Set up the cluster
	logger.Info("🚀 Setting up K3D cluster with Grove operator...")

	_, cleanupFunc, err := setup.SetupCompleteK3DCluster(runCtx, cfg, skaffoldPath, logger)
	if err != nil {
		logger.Errorf("Failed to setup K3D cluster: %v", err)
		if cleanupFunc != nil {
			logger.Info("Running cleanup...")
			cleanupFunc()
		}
		return fmt.Errorf("failed to setup K3D cluster: %w", err)
	}

	// Wrap cleanup to use DeleteCluster which creates a fresh context
	// This ensures cleanup works even if the main context is cancelled
	cleanup := func() {
		if err := setup.DeleteCluster(cfg.Name, logger); err != nil {
			logger.Errorf("Failed to delete cluster: %v", err)
		}
	}

	// Setup test images in registry (matching what e2e tests do)
	logger.Infof("📦 Pre-loading %d test image(s) to registry...", len(cli.TestImages))
	if err := setup.SetupRegistryTestImages(cfg.RegistryPort, cli.TestImages); err != nil {
		logger.Warnf("⚠️  Failed to pre-load test images (you can push them manually): %v", err)
		// Don't fail - user can push images manually if needed
	} else {
		logger.Info("✅ Test images successfully pre-loaded to registry")
	}

	// Write kubeconfig to KUBECONFIG env var or default location
	kubeconfigPath, err := writeKubeconfig(runCtx, cfg.Name, logger)
	if err != nil {
		logger.Errorf("Failed to write kubeconfig: %v", err)
		logger.Info("Running cleanup...")
		cleanup()
		return fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	// Success message
	logger.Info("✅ K3D cluster successfully created!")
	logger.Infof("Cluster name: %s", cfg.Name)
	logger.Infof("API server: https://localhost:%s", cfg.HostPort)
	if cfg.EnableRegistry {
		logger.Infof("Docker registry: localhost:%s", cfg.RegistryPort)
	}
	logger.Infof("Kubeconfig written to: %s", kubeconfigPath)

	// Print kubectl config instructions
	fmt.Println("\nTo use this cluster:")
	if kubeconfigPath != clientcmd.RecommendedHomeFile {
		fmt.Printf("  export KUBECONFIG=%s\n", kubeconfigPath)
	}
	fmt.Printf("  kubectl cluster-info\n\n")

	// Print teardown instructions
	fmt.Println("To tear down the cluster:")
	fmt.Printf("  k3d cluster delete %s\n\n", cfg.Name)

	// If running interactively, wait for signal
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Println("Press Ctrl+C to tear down the cluster...")
		<-runCtx.Done()

		logger.Info("Tearing down cluster...")
		cleanup()
		logger.Info("✅ Cluster teardown complete")
	} else {
		logger.Info("Cluster is ready. Run 'k3d cluster delete " + cfg.Name + "' to tear it down.")
	}

	return nil
}

// getLogLevel returns the log level based on CLI flags.
func getLogLevel(cli *CLI) utils.LogLevel {
	if cli.Quiet {
		return utils.ErrorLevel
	}
	if cli.Verbose {
		return utils.DebugLevel
	}
	return utils.InfoLevel
}

// printConfiguration logs the cluster configuration.
func printConfiguration(cfg *setup.ClusterConfig, logger *utils.Logger) {
	logger.Info("Cluster Configuration:")
	logger.Infof("  Name: %s", cfg.Name)
	logger.Infof("  Control Plane Nodes: %d", cfg.ControlPlaneNodes)
	logger.Infof("  Worker Nodes: %d", cfg.WorkerNodes)
	logger.Infof("  K3s Image: %s", cfg.Image)
	logger.Infof("  API Port: %s", cfg.HostPort)
	logger.Infof("  Load Balancer Port: %s", cfg.LoadBalancerPort)
	logger.Infof("  Worker Memory: %s", cfg.WorkerMemory)
	if cfg.EnableRegistry {
		logger.Infof("  Registry: Enabled (port %s)", cfg.RegistryPort)
	} else {
		logger.Info("  Registry: Disabled")
	}
	logger.Info("")
}

// findSkaffoldYAML finds skaffold.yaml by locating the git repo root.
func findSkaffoldYAML() string {
	// Use git to find the repo root - works from anywhere in the repo
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	repoRoot := strings.TrimSpace(string(output))
	skaffoldPath := filepath.Join(repoRoot, "operator", "skaffold.yaml")

	if _, err := os.Stat(skaffoldPath); err == nil {
		return skaffoldPath
	}

	return ""
}

// getKubeconfigPath returns KUBECONFIG env var or ~/.kube/config.
func getKubeconfigPath() string {
	if kubeconfigEnv := os.Getenv("KUBECONFIG"); kubeconfigEnv != "" {
		return kubeconfigEnv
	}
	return clientcmd.RecommendedHomeFile
}

// writeKubeconfig writes the cluster kubeconfig, merging with existing config if present.
func writeKubeconfig(ctx context.Context, clusterName string, logger *utils.Logger) (string, error) {
	logger.Debug("📄 Fetching kubeconfig from k3d cluster...")

	// Get kubeconfig from k3d
	kubeconfig, err := setup.GetKubeconfig(ctx, clusterName)
	if err != nil {
		return "", err
	}

	// Determine target path
	targetPath := getKubeconfigPath()
	if targetPath == "" {
		return "", fmt.Errorf("could not determine kubeconfig path")
	}

	// Ensure directory exists
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create kubeconfig directory: %w", err)
	}

	// Check if kubeconfig file already exists
	var existingConfig *clientcmdapi.Config
	if _, err := os.Stat(targetPath); err == nil {
		// File exists, load it
		logger.Debugf("Loading existing kubeconfig from %s", targetPath)
		existingConfig, err = clientcmd.LoadFromFile(targetPath)
		if err != nil {
			logger.Warnf("Failed to load existing kubeconfig, will overwrite: %v", err)
			existingConfig = nil
		}
	}

	// Merge or use new config
	var finalConfig *clientcmdapi.Config
	if existingConfig != nil {
		// Merge the new cluster config into existing
		logger.Debug("Merging new cluster config with existing kubeconfig")
		finalConfig = mergeKubeconfigs(existingConfig, kubeconfig, clusterName)
	} else {
		finalConfig = kubeconfig
	}

	// Write the kubeconfig
	if err := clientcmd.WriteToFile(*finalConfig, targetPath); err != nil {
		return "", fmt.Errorf("failed to write kubeconfig to %s: %w", targetPath, err)
	}

	logger.Debugf("✓ Kubeconfig written to %s", targetPath)
	return targetPath, nil
}

// mergeKubeconfigs merges a new kubeconfig into an existing one.
func mergeKubeconfigs(existing, new *clientcmdapi.Config, _ string) *clientcmdapi.Config {
	merged := existing.DeepCopy()

	// Add/update cluster
	for name, cluster := range new.Clusters {
		merged.Clusters[name] = cluster
	}

	// Add/update auth info
	for name, authInfo := range new.AuthInfos {
		merged.AuthInfos[name] = authInfo
	}

	// Add/update context
	for name, context := range new.Contexts {
		merged.Contexts[name] = context
		// Set the new cluster as current context
		merged.CurrentContext = name
	}

	return merged
}

