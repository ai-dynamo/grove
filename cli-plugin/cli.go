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

	"github.com/ai-dynamo/grove/cli-plugin/internal/commands"
	"github.com/ai-dynamo/grove/cli-plugin/internal/diagnostics"
	"github.com/alecthomas/kong"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Version is the kubectl-grove version, set at build time
var Version = "dev"

// CLI defines the kubectl-grove command-line interface with subcommands
type CLI struct {
	// Global flags
	Kubeconfig string `help:"Path to kubeconfig file." env:"KUBECONFIG" placeholder:"FILE"`
	Context    string `help:"Kubernetes context to use." placeholder:"NAME"`

	// Version flag to print version and exit
	Version VersionFlag `short:"v" help:"Print version and exit."`

	// Subcommands
	Diagnostics DiagnosticsCmd          `cmd:"" help:"Collect diagnostics from Grove resources."`
	Generate    GenerateCmd             `cmd:"" help:"Generate Grove manifests using AIConfigurator."`
	Status      StatusCmd               `cmd:"" help:"Show status of Grove PodCliqueSets."`
	Topology    TopologyCmd             `cmd:"" help:"Visualize pod placement topology."`
	Health      HealthCmd               `cmd:"" help:"Show gang health dashboard."`
	Plan        PlanCmd                 `cmd:"" help:"Manage AIConfigurator deployment plans."`
	TUI         TUICmd                  `cmd:"" help:"Interactive terminal UI for Grove resources."`
	Metrics     MetricsCmd              `cmd:"" help:"Show live inference metrics from pods."`
	Compare     CompareCmd              `cmd:"" help:"Compare stored plan with actual deployment."`
	Completion  completionCmd `cmd:"" help:"Generate shell completion scripts."`
}

// StatusCmd wraps the commands.StatusCmd for CLI integration.
type StatusCmd struct {
	Name      string `arg:"" optional:"" help:"Name of the PodCliqueSet to show status for."`
	All       bool   `short:"a" help:"Show status for all PodCliqueSets in the namespace."`
	Namespace string `short:"n" help:"Namespace to query." default:"default"`
}

// Run executes the status command.
func (s *StatusCmd) Run(globals *CLI) error {
	_, dynamicClient, err := buildClients(globals.Kubeconfig, globals.Context)
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}
	cmd := &commands.StatusCmd{
		Name:      s.Name,
		All:       s.All,
		Namespace: resolveNamespace(globals.Kubeconfig, globals.Context, s.Namespace),
	}
	return cmd.Execute(dynamicClient)
}

// TopologyCmd wraps the commands.TopologyCmd for CLI integration.
type TopologyCmd struct {
	Name      string `arg:"" help:"Name of the PodCliqueSet to show topology for."`
	Namespace string `short:"n" help:"Namespace to query." default:"default"`
	Watch     bool   `short:"w" help:"Watch for changes and update display."`
}

// Run executes the topology command.
func (t *TopologyCmd) Run(globals *CLI) error {
	cmd := &commands.TopologyCmd{
		Name:       t.Name,
		Namespace:  resolveNamespace(globals.Kubeconfig, globals.Context, t.Namespace),
		Watch:      t.Watch,
		Kubeconfig: globals.Kubeconfig,
		Context:    globals.Context,
	}
	return cmd.Run()
}

// HealthCmd wraps the commands.HealthCmd for CLI integration.
type HealthCmd struct {
	Name      string `arg:"" optional:"" help:"Name of the PodCliqueSet to show health for."`
	All       bool   `short:"a" help:"Show health for all PodCliqueSets in the namespace."`
	Namespace string `short:"n" help:"Namespace to query." default:"default"`
	Watch     bool   `short:"w" help:"Watch for changes and update display."`
}

// Run executes the health command.
func (h *HealthCmd) Run(globals *CLI) error {
	clientset, dynamicClient, err := buildClients(globals.Kubeconfig, globals.Context)
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}
	cmd := &commands.HealthCmd{
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		Namespace:     resolveNamespace(globals.Kubeconfig, globals.Context, h.Namespace),
		Name:          h.Name,
		All:           h.All,
		Watch:         h.Watch,
	}
	return cmd.Run(context.Background())
}

// PlanCmd is the parent command for plan subcommands.
type PlanCmd struct {
	Store PlanStoreCmd `cmd:"" help:"Store an AIConfigurator plan."`
	Show  PlanShowCmd  `cmd:"" help:"Show a stored plan."`
	Diff  PlanDiffCmd  `cmd:"" help:"Compare stored plan with deployed configuration."`
}

// PlanStoreCmd stores a plan.
type PlanStoreCmd struct {
	Name      string `arg:"" help:"Name for the stored plan (typically the PodCliqueSet name)."`
	File      string `short:"f" required:"" help:"Path to the plan file (JSON or YAML)."`
	Namespace string `short:"n" help:"Namespace for the plan." default:"default"`
}

// Run executes the plan store command.
func (p *PlanStoreCmd) Run(globals *CLI) error {
	clientset, _, err := buildClients(globals.Kubeconfig, globals.Context)
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}
	cmd := &commands.PlanStoreCmd{
		Name:      p.Name,
		File:      p.File,
		Namespace: resolveNamespace(globals.Kubeconfig, globals.Context, p.Namespace),
	}
	return cmd.Execute(clientset)
}

// PlanShowCmd shows a stored plan.
type PlanShowCmd struct {
	Name      string `arg:"" help:"Name of the stored plan."`
	Namespace string `short:"n" help:"Namespace for the plan." default:"default"`
}

// Run executes the plan show command.
func (p *PlanShowCmd) Run(globals *CLI) error {
	clientset, _, err := buildClients(globals.Kubeconfig, globals.Context)
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}
	cmd := &commands.PlanShowCmd{
		Name:      p.Name,
		Namespace: resolveNamespace(globals.Kubeconfig, globals.Context, p.Namespace),
	}
	return cmd.Execute(clientset, nil)
}

// PlanDiffCmd compares a stored plan with deployed configuration.
type PlanDiffCmd struct {
	Name      string `arg:"" help:"Name of the PodCliqueSet to compare."`
	Namespace string `short:"n" help:"Namespace to query." default:"default"`
}

// Run executes the plan diff command.
func (p *PlanDiffCmd) Run(globals *CLI) error {
	clientset, dynamicClient, err := buildClients(globals.Kubeconfig, globals.Context)
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}
	cmd := &commands.PlanDiffCmd{
		Name:      p.Name,
		Namespace: resolveNamespace(globals.Kubeconfig, globals.Context, p.Namespace),
	}
	return cmd.Execute(clientset, dynamicClient, nil)
}

// TUICmd wraps the commands.TUICmd for CLI integration.
type TUICmd struct {
	Namespace string `short:"n" help:"Namespace to query." default:"default"`
}

// Run executes the TUI command.
func (t *TUICmd) Run(globals *CLI) error {
	clientset, dynamicClient, err := buildClients(globals.Kubeconfig, globals.Context)
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}
	cmd := &commands.TUICmd{
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		Namespace:     resolveNamespace(globals.Kubeconfig, globals.Context, t.Namespace),
	}
	return cmd.Run()
}

// MetricsCmd wraps the commands.MetricsCmd for CLI integration.
type MetricsCmd struct {
	Name      string `arg:"" help:"Name of the PodCliqueSet to show metrics for."`
	Namespace string `short:"n" help:"Namespace to query." default:"default"`
	Watch     bool   `short:"w" help:"Watch for changes and update display."`
	JSON      bool   `help:"Output in JSON format."`
	Role      string `help:"Filter by role (e.g., prefill, decode)."`
}

// Run executes the metrics command.
func (m *MetricsCmd) Run(globals *CLI) error {
	clientset, _, err := buildClients(globals.Kubeconfig, globals.Context)
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}
	restConfig, err := buildRestConfig(globals.Kubeconfig, globals.Context)
	if err != nil {
		return fmt.Errorf("failed to build REST config: %w", err)
	}
	cmd := &commands.MetricsCmd{
		Clientset:  clientset,
		RestConfig: restConfig,
		Namespace:  resolveNamespace(globals.Kubeconfig, globals.Context, m.Namespace),
		Name:       m.Name,
		Watch:      m.Watch,
		JSON:       m.JSON,
		Role:       m.Role,
	}
	return cmd.Run(context.Background())
}

// CompareCmd wraps the commands.CompareCmd for CLI integration.
type CompareCmd struct {
	Name      string `arg:"" help:"Name of the PodCliqueSet to compare."`
	Namespace string `short:"n" help:"Namespace to query." default:"default"`
	JSON      bool   `help:"Output in JSON format."`
	Verbose   bool   `help:"Show verbose output including per-pod placement."`
}

// Run executes the compare command.
func (c *CompareCmd) Run(globals *CLI) error {
	clientset, dynamicClient, err := buildClients(globals.Kubeconfig, globals.Context)
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}
	cmd := &commands.CompareCmd{
		Name:      c.Name,
		Namespace: resolveNamespace(globals.Kubeconfig, globals.Context, c.Namespace),
		JSON:      c.JSON,
		Verbose:   c.Verbose,
	}
	return cmd.Execute(clientset, dynamicClient)
}

// DiagnosticsCmd represents the diagnostics subcommand.
type DiagnosticsCmd struct {
	// Namespace is the namespace to collect diagnostics from
	Namespace string `short:"n" help:"Namespace to collect diagnostics from." default:"default"`

	// OutputDir is the output directory for diagnostics
	OutputDir string `short:"o" help:"Output directory for diagnostics. Defaults to ./grove-diagnostics-{timestamp}." placeholder:"DIR"`

	// OperatorNamespace is the namespace where the Grove operator is deployed
	OperatorNamespace string `help:"Namespace where Grove operator is deployed." default:"grove-system"`
}

// GenerateCmd represents the generate subcommand.
type GenerateCmd struct {
	// Model is the model name (e.g., QWEN3_32B, LLAMA3_70B)
	Model string `help:"Model name (e.g., QWEN3_32B, LLAMA3_70B)." required:""`

	// HuggingFaceID is the optional HuggingFace model ID
	HuggingFaceID string `help:"HuggingFace model ID (optional, e.g., Qwen/Qwen3-32B)." name:"hf-id"`

	// System is the hardware system type (e.g., h200_sxm, a100_sxm)
	System string `help:"Hardware system type (e.g., h200_sxm, h100_sxm, a100_sxm)." required:""`

	// DecodeSystem is the optional system for decode workers in disaggregated mode
	DecodeSystem string `help:"Hardware system for decode workers (disagg mode only)." name:"decode-system"`

	// TotalGPUs is the total number of GPUs available
	TotalGPUs int `help:"Total number of GPUs available." required:"" name:"total-gpus"`

	// Backend is the inference backend (e.g., sglang, vllm, trtllm)
	Backend string `help:"Inference backend (sglang, vllm, trtllm)." required:""`

	// BackendVersion is the optional backend version
	BackendVersion string `help:"Specific backend version (optional)." name:"backend-version"`

	// ISL is the input sequence length
	ISL int `help:"Input sequence length." required:"" name:"isl" default:"4000"`

	// OSL is the output sequence length
	OSL int `help:"Output sequence length." required:"" name:"osl" default:"1000"`

	// Prefix is the prefix cache length
	Prefix int `help:"Prefix cache length." name:"prefix" default:"0"`

	// TTFT is the target time-to-first-token in milliseconds
	TTFT int `help:"Target time-to-first-token in milliseconds." required:"" name:"ttft" default:"2000"`

	// TPOT is the target time-per-output-token in milliseconds
	TPOT int `help:"Target time-per-output-token in milliseconds." required:"" name:"tpot" default:"30"`

	// RequestLatency is the optional end-to-end request latency target
	RequestLatency int `help:"End-to-end request latency target in milliseconds (optional)." name:"request-latency"`

	// DatabaseMode is the AIConfigurator database mode
	DatabaseMode string `help:"AIConfigurator database mode (SILICON, HYBRID, EMPIRICAL, SOL)." name:"database-mode" default:"SILICON"`

	// SaveDir is the output directory for generated manifests
	SaveDir string `help:"Output directory for generated manifests and AIConfigurator output." required:"" name:"save-dir" placeholder:"DIR"`

	// Namespace is the Kubernetes namespace for the generated manifests
	Namespace string `help:"Kubernetes namespace for generated manifests." default:"default" short:"n"`

	// Image is the container image for worker pods
	Image string `help:"Container image for worker pods (uses AIConfigurator default if not specified)." placeholder:"IMAGE"`

	// Debug enables debug mode for AIConfigurator
	Debug bool `help:"Enable debug mode for AIConfigurator." name:"debug"`
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
func (d *DiagnosticsCmd) Run(globals *CLI) error {
	fmt.Println("kubectl-grove - Grove Cluster Diagnostics")
	fmt.Println("==========================================")
	fmt.Println()

	// Build Kubernetes clients
	clientset, dynamicClient, err := buildClients(globals.Kubeconfig, globals.Context)
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}

	// Determine output directory
	outputDir := d.OutputDir
	if outputDir == "" {
		timestamp := time.Now().Format("2006-01-02-150405")
		outputDir = fmt.Sprintf("grove-diagnostics-%s", timestamp)
	}

	namespace := resolveNamespace(globals.Kubeconfig, globals.Context, d.Namespace)
	fmt.Printf("Collecting diagnostics from namespace: %s\n", namespace)
	fmt.Printf("Operator namespace: %s\n", d.OperatorNamespace)
	fmt.Printf("Output directory: %s\n", outputDir)
	fmt.Println()

	// Create diagnostic context
	ctx := context.Background()
	dc := diagnostics.NewDiagnosticContext(ctx, clientset, dynamicClient, namespace)
	dc.OperatorNamespace = d.OperatorNamespace

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

// Run executes the generate command
func (g *GenerateCmd) Run(_ *CLI) error {
	// Import the aic package logic
	return runGenerate(g)
}

// buildClients creates Kubernetes clients from kubeconfig
func buildClients(kubeconfig, kubeContext string) (*kubernetes.Clientset, dynamic.Interface, error) {
	config, err := buildRestConfig(kubeconfig, kubeContext)
	if err != nil {
		return nil, nil, err
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

// buildRestConfig creates a REST config from kubeconfig
func buildRestConfig(kubeconfig, kubeContext string) (*rest.Config, error) {
	// Build config from kubeconfig
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		configOverrides.CurrentContext = kubeContext
	}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	return config, nil
}

// completionCmd generates shell completion scripts.
type completionCmd struct {
	Shell string `arg:"" enum:"bash,zsh,fish" help:"Shell type (bash, zsh, fish)."`
}

// Run generates completion scripts for the specified shell.
func (c *completionCmd) Run() error {
	switch c.Shell {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	default:
		return fmt.Errorf("unsupported shell: %s", c.Shell)
	}
	return nil
}

const bashCompletion = `# kubectl-grove bash completion
# Add to ~/.bashrc: eval "$(kubectl-grove completion bash)"

_kubectl_grove_completions() {
    local cur prev commands
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    commands="status topology health metrics tui generate plan compare diagnostics completion"

    case "${prev}" in
        kubectl-grove|grove)
            COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
            return 0
            ;;
        -n|--namespace)
            # Complete with kubectl namespaces
            COMPREPLY=( $(compgen -W "$(kubectl get namespaces -o name 2>/dev/null | cut -d/ -f2)" -- "${cur}") )
            return 0
            ;;
        plan)
            COMPREPLY=( $(compgen -W "store show diff" -- "${cur}") )
            return 0
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )
            return 0
            ;;
    esac

    # Complete flags
    if [[ "${cur}" == -* ]]; then
        COMPREPLY=( $(compgen -W "-n --namespace --kubeconfig --context -w --watch -a --all -v --version -h --help" -- "${cur}") )
        return 0
    fi
}

complete -F _kubectl_grove_completions kubectl-grove
complete -F _kubectl_grove_completions kubectl grove
`

const zshCompletion = `#compdef kubectl-grove

# kubectl-grove zsh completion
# Add to ~/.zshrc: eval "$(kubectl-grove completion zsh)"

_kubectl_grove() {
    local -a commands
    commands=(
        'status:Show status of Grove PodCliqueSets'
        'topology:Visualize pod placement topology'
        'health:Show gang health dashboard'
        'metrics:Show live inference metrics from pods'
        'tui:Interactive terminal UI for Grove resources'
        'generate:Generate Grove manifests using AIConfigurator'
        'plan:Manage AIConfigurator deployment plans'
        'compare:Compare stored plan with actual deployment'
        'diagnostics:Collect diagnostics from Grove resources'
        'completion:Generate shell completion scripts'
    )

    _arguments -C \
        '(-n --namespace)'{-n,--namespace}'[Namespace to query]:namespace:_kubectl_grove_namespaces' \
        '--kubeconfig[Path to kubeconfig file]:file:_files' \
        '--context[Kubernetes context to use]:context:' \
        '(-v --version)'{-v,--version}'[Print version and exit]' \
        '(-h --help)'{-h,--help}'[Show help]' \
        '1:command:->command' \
        '*::arg:->args'

    case "$state" in
        command)
            _describe -t commands 'kubectl-grove commands' commands
            ;;
        args)
            case "$words[1]" in
                plan)
                    _values 'plan subcommand' store show diff
                    ;;
                completion)
                    _values 'shell' bash zsh fish
                    ;;
            esac
            ;;
    esac
}

_kubectl_grove_namespaces() {
    local -a namespaces
    namespaces=(${(f)"$(kubectl get namespaces -o name 2>/dev/null | cut -d/ -f2)"})
    _describe -t namespaces 'namespaces' namespaces
}

compdef _kubectl_grove kubectl-grove
compdef _kubectl_grove kubectl grove
`

const fishCompletion = `# kubectl-grove fish completion
# Add to ~/.config/fish/completions/kubectl-grove.fish

# Disable file completion by default
complete -c kubectl-grove -f

# Commands
complete -c kubectl-grove -n "__fish_use_subcommand" -a status -d "Show status of Grove PodCliqueSets"
complete -c kubectl-grove -n "__fish_use_subcommand" -a topology -d "Visualize pod placement topology"
complete -c kubectl-grove -n "__fish_use_subcommand" -a health -d "Show gang health dashboard"
complete -c kubectl-grove -n "__fish_use_subcommand" -a metrics -d "Show live inference metrics from pods"
complete -c kubectl-grove -n "__fish_use_subcommand" -a tui -d "Interactive terminal UI for Grove resources"
complete -c kubectl-grove -n "__fish_use_subcommand" -a generate -d "Generate Grove manifests using AIConfigurator"
complete -c kubectl-grove -n "__fish_use_subcommand" -a plan -d "Manage AIConfigurator deployment plans"
complete -c kubectl-grove -n "__fish_use_subcommand" -a compare -d "Compare stored plan with actual deployment"
complete -c kubectl-grove -n "__fish_use_subcommand" -a diagnostics -d "Collect diagnostics from Grove resources"
complete -c kubectl-grove -n "__fish_use_subcommand" -a completion -d "Generate shell completion scripts"

# Plan subcommands
complete -c kubectl-grove -n "__fish_seen_subcommand_from plan" -a store -d "Store an AIConfigurator plan"
complete -c kubectl-grove -n "__fish_seen_subcommand_from plan" -a show -d "Show a stored plan"
complete -c kubectl-grove -n "__fish_seen_subcommand_from plan" -a diff -d "Compare stored plan with deployed configuration"

# Completion subcommand
complete -c kubectl-grove -n "__fish_seen_subcommand_from completion" -a "bash zsh fish"

# Global flags
complete -c kubectl-grove -s n -l namespace -d "Namespace to query" -xa "(kubectl get namespaces -o name 2>/dev/null | cut -d/ -f2)"
complete -c kubectl-grove -l kubeconfig -d "Path to kubeconfig file" -r
complete -c kubectl-grove -l context -d "Kubernetes context to use"
complete -c kubectl-grove -s v -l version -d "Print version and exit"
complete -c kubectl-grove -s h -l help -d "Show help"

# Watch flag for applicable commands
complete -c kubectl-grove -n "__fish_seen_subcommand_from topology health metrics" -s w -l watch -d "Watch for changes"
complete -c kubectl-grove -n "__fish_seen_subcommand_from status health" -s a -l all -d "Show all PodCliqueSets"
`

// resolveNamespace returns the namespace to use, respecting kubeconfig context.
// If flagValue is explicitly set (not empty and not "default"), use it.
// Otherwise, try to get the namespace from kubeconfig current context.
// Falls back to "default" if no namespace is configured anywhere.
func resolveNamespace(kubeconfig, kubeContext, flagValue string) string {
	// If user explicitly set a non-default namespace, use it
	if flagValue != "" && flagValue != "default" {
		return flagValue
	}

	// Try to get namespace from kubeconfig
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		configOverrides.CurrentContext = kubeContext
	}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	namespace, _, err := kubeConfig.Namespace()
	if err != nil || namespace == "" {
		return "default"
	}

	return namespace
}
