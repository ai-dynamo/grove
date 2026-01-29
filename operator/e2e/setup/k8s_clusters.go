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

package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	k3dclient "github.com/k3d-io/k3d/v5/pkg/client"
	"github.com/k3d-io/k3d/v5/pkg/config"
	"github.com/k3d-io/k3d/v5/pkg/config/types"
	"github.com/k3d-io/k3d/v5/pkg/config/v1alpha5"
	k3dlogger "github.com/k3d-io/k3d/v5/pkg/logger"
	"github.com/k3d-io/k3d/v5/pkg/runtimes"
	k3d "github.com/k3d-io/k3d/v5/pkg/types"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

const (
	// defaultPollTimeout is the timeout for most polling conditions
	defaultPollTimeout = 5 * time.Minute
	// defaultPollInterval is the interval for most polling conditions
	defaultPollInterval = 5 * time.Second
)

// transientErrors contains error substrings that indicate a webhook is not ready yet
var transientErrors = []string{
	"no endpoints available",
	"connection refused",
	"i/o timeout",
	"Bad Gateway",
	"Service Unavailable",
}

// isTransient checks if an error string contains any transient error patterns
func isTransient(errStr string) bool {
	for _, s := range transientErrors {
		if strings.Contains(errStr, s) {
			return true
		}
	}
	return false
}

// NodeTaint represents a Kubernetes node taint
type NodeTaint struct {
	Key    string
	Value  string
	Effect string
}

// NodeLabel represents a Kubernetes node label with its target node filters
type NodeLabel struct {
	Key   string // Label key
	Value string // Label value
	// k3s refers to worker nodes as agent nodes
	NodeFilters []string // Node filters (e.g., "server:*", "agent:*", "server:0", "agent:1")
}

// ClusterConfig holds configuration for creating a k3d cluster
type ClusterConfig struct {
	Name              string      // Name of the k3d cluster
	ControlPlaneNodes int         // Number of Control Plane Nodes (k3s calls these server nodes)
	WorkerNodes       int         // Number of worker nodes (called agents in k3s terminology)
	Image             string      // Docker image to use for k3d cluster (e.g., "rancher/k3s:v1.28.8-k3s1")
	HostPort          string      // Port on host to expose Kubernetes API (e.g., "6550")
	LoadBalancerPort  string      // Load balancer port mapping in format "hostPort:containerPort" (e.g., "8080:80")
	NodeLabels        []NodeLabel // Kubernetes labels to apply with specific node filters
	WorkerNodeTaints  []NodeTaint // Taints to apply to worker nodes
	WorkerMemory      string      // Memory allocation for worker/agent nodes (e.g., "150m")
	EnableRegistry    bool        // Enable built-in Docker registry
	RegistryPort      string      // Port for the Docker registry (e.g., "5001")
}

// DefaultClusterConfig returns the default cluster configuration used by e2e tests.
// This includes all the node labels and taints required for Grove e2e testing.
// The setup-debug-cluster tool and SharedClusterManager both use this as their base config.
func DefaultClusterConfig() ClusterConfig {
	return ClusterConfig{
		Name:              "grove-e2e-cluster",
		ControlPlaneNodes: 1,
		WorkerNodes:       30,     // Maximum needed across all tests
		WorkerMemory:      "150m", // 150m memory per agent node to fit one workload pod
		Image:             "rancher/k3s:v1.33.5-k3s1",
		HostPort:          "6550",
		LoadBalancerPort:  "8080:80",
		EnableRegistry:    true,
		RegistryPort:      "5001",
		NodeLabels: []NodeLabel{
			{
				Key: "node_role.e2e.grove.nvidia.com",
				// k3s refers to worker nodes as agent nodes
				Value:       "agent",
				NodeFilters: []string{"agent:*"},
			},
			// Disable GPU deployment on all nodes (validator causes issues)
			{
				Key:   "nvidia.com/gpu.deploy.operands",
				Value: "false",
				// k3s refers to worker nodes as agent nodes
				NodeFilters: []string{"server:*", "agent:*"},
			},
		},
		WorkerNodeTaints: []NodeTaint{
			{
				Key: "node_role.e2e.grove.nvidia.com",
				// k3s refers to worker nodes as agent nodes
				Value:  "agent",
				Effect: "NoSchedule",
			},
		},
	}
}

// stripRegistryDomain extracts the image path WITHOUT the registry domain.
// When k3s uses a mirror, it strips the registry domain from the path.
// Examples:
//   - "registry.k8s.io/nfd/node-feature-discovery:v0.17.3" -> "nfd/node-feature-discovery:v0.17.3"
//   - "nvcr.io/nvidia/gpu-operator:v25.3.4" -> "nvidia/gpu-operator:v25.3.4"
//   - "ubuntu:latest" -> "ubuntu:latest" (no domain to strip)
func stripRegistryDomain(img string) string {
	parts := strings.SplitN(img, "/", 2)
	if len(parts) == 2 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		// First part looks like a domain (has . or :), so strip it
		return parts[1]
	}
	// No registry domain, use full path
	return img
}

// GetKubeconfig fetches and returns the kubeconfig for a k3d cluster.
func GetKubeconfig(ctx context.Context, clusterName string) (*clientcmdapi.Config, error) {
	cluster, err := k3dclient.ClusterGet(ctx, runtimes.Docker, &k3d.Cluster{Name: clusterName})
	if err != nil {
		return nil, fmt.Errorf("could not get cluster: %w", err)
	}

	kubeconfig, err := k3dclient.KubeconfigGet(ctx, runtimes.Docker, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig from k3d: %w", err)
	}

	return kubeconfig, nil
}

// ensureClusterDoesNotExist removes any stale k3d cluster with the same name from previous runs.
func ensureClusterDoesNotExist(ctx context.Context, clusterName string, logger *utils.Logger) error {
	cluster := &k3d.Cluster{Name: clusterName}

	existingCluster, err := k3dclient.ClusterGet(ctx, runtimes.Docker, cluster)
	if err != nil {
		if errors.Is(err, k3dclient.ClusterGetNoNodesFoundError) {
			return nil
		}
		return fmt.Errorf("failed to inspect existing k3d cluster %s: %w", clusterName, err)
	}

	logger.Warnf("🧹 Removing stale k3d cluster '%s' before setup", clusterName)
	if err := k3dclient.ClusterDelete(ctx, runtimes.Docker, existingCluster, k3d.ClusterDeleteOpts{}); err != nil {
		return fmt.Errorf("failed to delete existing k3d cluster %s: %w", clusterName, err)
	}

	return nil
}

// ensureRegistryDoesNotExist removes any stale k3d registry container from previous runs.
// It looks for containers named "registry" (the name used in our k3d config).
// If another container is using the registry port, it returns an error with details.
func ensureRegistryDoesNotExist(ctx context.Context, registryPort string, logger *utils.Logger) error {
	dockerClient, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer dockerClient.Close()

	// Look for the registry container by name - k3d creates it as just "registry"
	// based on the Name field in SimpleConfigRegistryCreateConfig
	filterArgs := filters.NewArgs()
	filterArgs.Add("name", "registry")

	containers, err := dockerClient.ContainerList(ctx, container.ListOptions{All: true, Filters: filterArgs})
	if err != nil {
		return fmt.Errorf("failed to list Docker containers: %w", err)
	}

	for _, c := range containers {
		displayName := "registry"
		if len(c.Names) > 0 {
			displayName = strings.TrimPrefix(c.Names[0], "/")
		}

		logger.Warnf("🧹 Removing stale k3d registry container %s (%s) before cluster setup", displayName, c.ID[:12])

		if err := dockerClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			return fmt.Errorf("failed to remove existing registry container %s: %w", displayName, err)
		}
	}

	// Check if any other container is using the registry port
	allContainers, err := dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list all Docker containers: %w", err)
	}

	for _, c := range allContainers {
		for _, port := range c.Ports {
			if fmt.Sprintf("%d", port.PublicPort) == registryPort {
				displayName := c.ID[:12]
				if len(c.Names) > 0 {
					displayName = strings.TrimPrefix(c.Names[0], "/")
				}

				return fmt.Errorf("port %s is already in use by container %s (%s). Please stop or remove this container, or use a different registry port with --registry-port", registryPort, displayName, c.ID[:12])
			}
		}
	}

	return nil
}

// SetupCompleteK3DCluster creates a complete k3d cluster with Grove, Kai Scheduler, and NVIDIA GPU Operator
func SetupCompleteK3DCluster(ctx context.Context, cfg ClusterConfig, skaffoldYAMLPath string, logger *utils.Logger) (*rest.Config, func(), error) {
	restConfig, cleanup, err := SetupK3DCluster(ctx, cfg, logger)
	if err != nil {
		return nil, cleanup, err
	}

	// Create clientset for node monitoring
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, cleanup, fmt.Errorf("could not create clientset: %w", err)
	}

	// Start node monitoring to handle not ready nodes (see StartNodeMonitoring for more details)
	nodeMonitoringCleanup := StartNodeMonitoring(ctx, clientset, logger)

	// Create enhanced cleanup function that includes node monitoring
	enhancedCleanup := func() {
		// Stop node monitoring first
		nodeMonitoringCleanup()
		// Then run the original cleanup
		cleanup()
	}

	// Load dependencies for image pre-pulling and helm charts
	deps, err := e2e.GetDependencies()
	if err != nil {
		return nil, enhancedCleanup, fmt.Errorf("failed to load dependencies: %w", err)
	}
	imagesToPrepull := deps.GetImagesToPrePull()

	// Pre-pull and push images to local registry if enabled
	if cfg.EnableRegistry {
		if err := prepullImages(ctx, imagesToPrepull, cfg.RegistryPort, logger); err != nil {
			logger.Warnf("⚠️  Failed to pre-load images (cluster will still work, but slower startup): %v", err)
			// Don't fail the cluster creation if image pre-loading fails - cluster will still work
		} else {
			// Verify images are accessible from the registry
			if err := verifyRegistryImages(imagesToPrepull, cfg.RegistryPort, logger); err != nil {
				logger.Warnf("⚠️  Registry images verification failed: %v", err)
			}
		}
	}

	tolerations := []map[string]interface{}{
		{
			"key":      "node-role.kubernetes.io/control-plane",
			"operator": "Exists",
			"effect":   "NoSchedule",
		},
		{
			"key":      "node_role.e2e.grove.nvidia.com",
			"operator": "Equal",
			// k3s refers to worker nodes as agent nodes
			"value":  "agent",
			"effect": "NoSchedule",
		},
	}

	// Use Kai Scheduler configuration from dependencies
	kaiConfig := &HelmInstallConfig{
		ReleaseName:     deps.HelmCharts.KaiScheduler.ReleaseName,
		ChartRef:        deps.HelmCharts.KaiScheduler.ChartRef,
		ChartVersion:    deps.HelmCharts.KaiScheduler.Version,
		Namespace:       deps.HelmCharts.KaiScheduler.Namespace,
		RestConfig:      restConfig,
		CreateNamespace: true,
		Wait:            false,
		Values: map[string]interface{}{
			"global": map[string]interface{}{
				"tolerations": tolerations,
			},
		},
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	logger.Info("🚀 Installing Grove, Kai Scheduler...")
	if err := InstallCoreComponents(ctx, restConfig, kaiConfig, skaffoldYAMLPath, cfg.RegistryPort, logger); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("component installation failed: %w", err)
	}

	// Wait for Grove pods to be ready (0 = skip count validation)
	if err := utils.WaitForPodsInNamespace(ctx, OperatorNamespace, restConfig, 0, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		cleanup()

		return nil, nil, fmt.Errorf("grove pods not ready: %w", err)
	}

	// Wait for Kai Scheduler pods to be ready (0 = skip count validation)
	if err := utils.WaitForPodsInNamespace(ctx, kaiConfig.Namespace, kaiConfig.RestConfig, 0, defaultPollTimeout, defaultPollInterval, kaiConfig.Logger); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("kai scheduler pods not ready: %w", err)
	}

	// Wait for the Kai CRDs to be available (before creating queues)
	if err := WaitForKaiCRDs(ctx, kaiConfig); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to wait for Kai CRDs: %w", err)
	}

	// need to create the default Kai queues
	if err := CreateDefaultKaiQueues(ctx, kaiConfig); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to create default Kai queue: %w", err)
	}

	// Nvidia Operator seems to take the longest to be ready, so we wait for it last
	// to get the most done while waiting. (0 = skip count validation)
	//if err := utils.WaitForPodsInNamespace(ctx, gpuOperatorConfig.Namespace, gpuOperatorConfig.RestConfig, 0, defaultPollTimeout, defaultPollInterval, gpuOperatorConfig.Logger); err != nil {
	//	cleanup()
	//	return nil, nil, fmt.Errorf("NVIDIA GPU Operator not ready: %w", err)
	//}

	// Wait for Grove webhook to be ready by actually testing it
	// This ensures the webhook is ready to handle requests before tests start
	if err := waitForWebhookReady(ctx, restConfig, logger); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("grove webhook not ready: %w", err)
	}

	// Find any deployed images that are not in the prepull list
	namespacesToCheck := []string{kaiConfig.Namespace /*gpuOperatorConfig.Namespace*/}
	missingImages := findMissingImages(ctx, clientset, namespacesToCheck, imagesToPrepull, logger)

	// Warn about missing images to help keep the prepull list up to date
	if len(missingImages) > 0 {
		logger.Warnf("⚠️  Found %d images not in prepull list. Consider adding these to e2e/dependencies.yaml:", len(missingImages))
		for _, img := range missingImages {
			logger.Warnf("    - %s", img)
		}
	} else {
		logger.Debug("✅ All deployed images are in prepull list")
	}

	return restConfig, enhancedCleanup, nil
}

// SetupK3DCluster creates a k3d cluster and returns a REST config
func SetupK3DCluster(ctx context.Context, cfg ClusterConfig, logger *utils.Logger) (*rest.Config, func(), error) {
	// k3d is very verbose, we don't want the INFO level logs unless the logger is set to DEBUG
	// k3d uses logrus internally, so we need to convert our level to logrus level
	if logger.GetLevel() == utils.DebugLevel {
		k3dlogger.Log().SetLevel(logrus.DebugLevel)
	} else {
		k3dlogger.Log().SetLevel(logrus.ErrorLevel)
	}

	// Create simple cluster configuration
	clusterConfig := v1alpha5.SimpleConfig{
		ObjectMeta: types.ObjectMeta{
			Name: cfg.Name,
		},
		Servers: cfg.ControlPlaneNodes,
		// k3s calls these agents, but we call them worker nodes
		Agents: cfg.WorkerNodes,
		Image:  cfg.Image,
		ExposeAPI: v1alpha5.SimpleExposureOpts{
			Host:     "0.0.0.0",
			HostPort: cfg.HostPort,
		},
		Ports: []v1alpha5.PortWithNodeFilters{
			{
				Port:        cfg.LoadBalancerPort,
				NodeFilters: []string{"loadbalancer"},
			},
		},
		Options: v1alpha5.SimpleConfigOptions{
			Runtime: v1alpha5.SimpleConfigOptionsRuntime{
				// worker node memory
				AgentsMemory: cfg.WorkerMemory,
			},
		},
	}

	// Apply Kubernetes node labels with their specified node filters
	for _, nodeLabel := range cfg.NodeLabels {
		clusterConfig.Options.K3sOptions.NodeLabels = append(
			clusterConfig.Options.K3sOptions.NodeLabels,
			v1alpha5.LabelWithNodeFilters{
				Label:       fmt.Sprintf("%s=%s", nodeLabel.Key, nodeLabel.Value),
				NodeFilters: nodeLabel.NodeFilters,
			},
		)
	}

	// Apply worker node taints if specified
	for _, taint := range cfg.WorkerNodeTaints {
		clusterConfig.Options.K3sOptions.ExtraArgs = append(
			clusterConfig.Options.K3sOptions.ExtraArgs,
			v1alpha5.K3sArgWithNodeFilters{
				Arg: fmt.Sprintf("--node-taint=%s=%s:%s", taint.Key, taint.Value, taint.Effect),
				// k3s refers to worker nodes as agent nodes
				NodeFilters: []string{"agent:*"},
			},
		)
	}

	// Configure registry if enabled
	if cfg.EnableRegistry {
		// Create registries.yaml config for k3s to mirror registries to local registry
		// This allows the cluster to pull images from the local registry automatically
		// Note: The registry listens on port 5000 INSIDE the Docker network,
		// even though it's exposed as cfg.RegistryPort to the host
		registriesYAML := `mirrors:
  registry.k8s.io:
    endpoint:
      - http://registry:5000
  nvcr.io:
    endpoint:
      - http://registry:5000
configs:
  registry:5000:
    tls:
      insecure_skip_verify: true
`

		clusterConfig.Registries = v1alpha5.SimpleConfigRegistries{
			Create: &v1alpha5.SimpleConfigRegistryCreateConfig{
				Name:     "registry",
				Host:     "0.0.0.0",
				HostPort: cfg.RegistryPort,
				// Use GHCR-hosted Distribution registry to avoid Docker Hub rate limits
				Image: "ghcr.io/distribution/distribution:3.0.0",
			},
			Config: registriesYAML,
		}
	}

	// Transform configuration into full cluster config that is ready to be used
	k3dConfig, err := config.TransformSimpleToClusterConfig(ctx, runtimes.Docker, clusterConfig, "")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to transform config: %w", err)
	}

	if err := ensureClusterDoesNotExist(ctx, k3dConfig.Name, logger); err != nil {
		return nil, nil, err
	}

	if cfg.EnableRegistry {
		if err := ensureRegistryDoesNotExist(ctx, cfg.RegistryPort, logger); err != nil {
			return nil, nil, err
		}
	}

	// this is the cleanup function, we always return it now so the caller can decide to use it or not
	// Note: we create a fresh context here rather than using the passed-in ctx, because cleanup
	// may be called after the parent context is cancelled (e.g., after Ctrl+C signal)
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		logger.Debug("🗑️ Deleting cluster...")
		if err := k3dclient.ClusterDelete(cleanupCtx, runtimes.Docker, &k3dConfig.Cluster, k3d.ClusterDeleteOpts{}); err != nil {
			logger.Errorf("Failed to delete cluster: %v", err)
		} else {
			logger.Info("✅ Cluster deleted successfully")
		}
	}

	// Create cluster with retry logic and timeout
	// k3d cluster creation can fail intermittently when starting many nodes (30+) due to
	// the "thundering herd" effect - all agents trying to register with the server simultaneously.
	// Additionally, ClusterRun can hang indefinitely if a node gets stuck during startup.
	// We use a timeout to detect hangs and retry, which usually succeeds.
	const maxClusterCreateRetries = 3
	const clusterCreateRetryDelay = 5 * time.Second
	const clusterCreateTimeout = 5 * time.Minute

	logger.Debugf("🚀 Creating cluster '%s' with %d server(s) and %d worker node(s)...",
		k3dConfig.Name, cfg.ControlPlaneNodes, cfg.WorkerNodes)

	var createErr error
	for attempt := 1; attempt <= maxClusterCreateRetries; attempt++ {
		if attempt > 1 {
			logger.Warnf("🔄 Retrying cluster creation (attempt %d/%d)...", attempt, maxClusterCreateRetries)

			// Clean up failed cluster before retry - use original context for cleanup
			_ = k3dclient.ClusterDelete(ctx, runtimes.Docker, &k3dConfig.Cluster, k3d.ClusterDeleteOpts{})

			// Clean up registry if enabled (it may have been partially created)
			if cfg.EnableRegistry {
				_ = ensureRegistryDoesNotExist(ctx, cfg.Name, logger)
			}

			time.Sleep(clusterCreateRetryDelay)
		}

		// Create a timeout context for this attempt
		// ClusterRun can hang indefinitely if node startup gets stuck
		attemptCtx, cancel := context.WithTimeout(ctx, clusterCreateTimeout)
		createErr = k3dclient.ClusterRun(attemptCtx, runtimes.Docker, k3dConfig)
		cancel() // Always cancel to release resources

		if createErr == nil {
			if attempt > 1 {
				logger.Infof("✅ Cluster creation succeeded on attempt %d/%d", attempt, maxClusterCreateRetries)
			}
			break
		}

		// Check if it was a timeout
		if attemptCtx.Err() == context.DeadlineExceeded {
			logger.Errorf("❌ Cluster creation timed out after %v (attempt %d/%d)", clusterCreateTimeout, attempt, maxClusterCreateRetries)
		} else {
			logger.Errorf("❌ Cluster creation failed (attempt %d/%d): %v", attempt, maxClusterCreateRetries, createErr)
		}
	}

	if createErr != nil {
		return nil, cleanup, fmt.Errorf("failed to create cluster after %d attempts: %w", maxClusterCreateRetries, createErr)
	}

	// Get kubeconfig
	logger.Debug("📄 Fetching kubeconfig...")
	cluster, err := k3dclient.ClusterGet(ctx, runtimes.Docker, &k3dConfig.Cluster)
	if err != nil {
		return nil, cleanup, fmt.Errorf("could not get cluster: %w", err)
	}

	kubeconfig, err := k3dclient.KubeconfigGet(ctx, runtimes.Docker, cluster)
	if err != nil {
		return nil, cleanup, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	kubeconfigBytes, err := clientcmd.Write(*kubeconfig)
	if err != nil {
		return nil, cleanup, fmt.Errorf("failed to serialize kubeconfig: %w", err)
	}

	// Create REST config
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, cleanup, fmt.Errorf("could not create rest config: %w", err)
	}

	return restConfig, cleanup, nil
}

// InstallCoreComponents installs the core components (Grove via Skaffold and Kai Scheduler via Helm)
func InstallCoreComponents(ctx context.Context, restConfig *rest.Config, kaiConfig *HelmInstallConfig, skaffoldYAMLPath string, registryPort string, logger *utils.Logger) error {
	// use wait group to wait for all installations to complete
	var wg sync.WaitGroup

	// error channel to collect errors from goroutines, buffered to 3 errors
	errChan := make(chan error, 3)

	// There's occasionally weird races regarding CRDS, for test stability we retry a few times
	const maxRetries = 3
	const retryDelay = 5 * time.Second

	// Install Kai Scheduler
	if kaiConfig != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Debug("🚀 Starting Kai Scheduler installation...")

			installFunc := func() error {
				_, err := InstallHelmChart(kaiConfig)
				return err
			}

			err := retryInstallation(installFunc, "Kai Scheduler", maxRetries, retryDelay, logger)
			if err != nil {
				errChan <- err
			} else {
				logger.Debug("✅ Kai Scheduler installation completed successfully")
			}
		}()
	}

	// Install Grove via Skaffold
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Debug("🚀 Starting Grove installation via Skaffold...")

		installFunc := func() error {
			absoluteSkaffoldYAMLPath, err := filepath.Abs(skaffoldYAMLPath)
			if err != nil {
				return fmt.Errorf("failed to get absolute path for skaffold.yaml: %w", err)
			}

			skaffoldConfig := &SkaffoldInstallConfig{
				SkaffoldYAMLPath: absoluteSkaffoldYAMLPath,
				RestConfig:       restConfig,
				Profiles:         []string{"topology-test"},
				PushRepo:         fmt.Sprintf("localhost:%s", registryPort),
				PullRepo:         fmt.Sprintf("registry:%s", registryPort),
				Namespace:        OperatorNamespace,
				Env: map[string]string{
					"VERSION":  "E2E_TESTS",
					"LD_FLAGS": buildLDFlagsForE2E(),
				},
				Logger: logger,
			}
			cleanup, err := InstallWithSkaffold(ctx, skaffoldConfig)
			if cleanup != nil {
				defer cleanup()
			}
			return err
		}

		err := retryInstallation(installFunc, "Grove", maxRetries, retryDelay, logger)
		if err != nil {
			errChan <- err
		} else {
			logger.Debug("✅ Grove installation completed successfully")
		}
	}()

	// Wait for all installations to complete
	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		return err // Return the first error encountered
	}

	// Apply hierarchical topology labels to worker nodes
	if err := applyTopologyLabels(ctx, restConfig, logger); err != nil {
		return fmt.Errorf("failed to apply topology labels: %w", err)
	}

	logger.Debug("✅ All component installations completed successfully")
	return nil
}

// retryInstallation retries an installation function up to maxRetries times with a delay between attempts
func retryInstallation(installFunc func() error, componentName string, maxRetries int, retryDelay time.Duration, logger *utils.Logger) error {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			logger.Debugf("🔄 Retrying %s installation (attempt %d/%d)...", componentName, attempt, maxRetries)
			time.Sleep(retryDelay)
		}

		err := installFunc()
		if err == nil {
			if attempt > 1 {
				logger.Debugf("✅ %s installation succeeded on attempt %d/%d", componentName, attempt, maxRetries)
			}
			return nil
		}

		lastErr = err
		logger.Errorf("❌ %s installation failed on attempt %d/%d: %v", componentName, attempt, maxRetries, err)
	}

	return fmt.Errorf("%s installation failed after %d attempts: %w", componentName, maxRetries, lastErr)
}

// StartNodeMonitoring starts a goroutine that monitors k3d cluster nodes for not ready status
// and automatically replaces them by deleting the node and restarting the corresponding Docker container.
// Returns a cleanup function that should be deferred to stop the monitoring.
//
// Background: There's an intermitten issue where nodes go not ready which causes the tests to fail
// occasional. This is an issue with either k3d or docker on mac. This is
// a simple solution that is working flawlessly.
//
// The monitoring process:
// 1. Checks for nodes that are not in Ready status every 5 seconds
// 2. Skips cordoned nodes (intentionally unschedulable for maintenance)
// 3. Deletes the not ready node from Kubernetes
// 4. Finds and restarts the corresponding Docker container (node names match container names exactly)
// 5. The restarted container will rejoin the cluster as a new node
func StartNodeMonitoring(ctx context.Context, clientset *kubernetes.Clientset, logger *utils.Logger) func() {
	logger.Debug("🔍 Starting node monitoring for not ready nodes...")

	// Create a context that can be cancelled to stop the monitoring
	monitorCtx, cancel := context.WithCancel(ctx)

	// Start the monitoring goroutine
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-monitorCtx.Done():
				logger.Debug("🛑 Stopping node monitoring...")
				return
			case <-ticker.C:
				if err := checkAndReplaceNotReadyNodes(monitorCtx, clientset, logger); err != nil {
					logger.Errorf("Error during node monitoring: %v", err)
				}
			}
		}
	}()

	// Return cleanup function
	return func() {
		cancel()
	}
}

// checkAndReplaceNotReadyNodes checks for nodes that are not ready and replaces them
func checkAndReplaceNotReadyNodes(ctx context.Context, clientset *kubernetes.Clientset, logger *utils.Logger) error {
	// List all nodes
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, node := range nodes.Items {
		if !isNodeReady(&node) {
			// Skip cordoned nodes because even if they're also not ready, we don't want to replace
			// them with an uncordoned node as it'll break tests. When/if the node becomes uncordoned,
			// the node monitoring will automatically replace it then as it's needed.
			if node.Spec.Unschedulable {
				logger.Debugf("⏭️ Skipping cordoned node: %s (intentionally unschedulable)", node.Name)
				continue
			}

			logger.Warnf("🚨 Found not ready node: %s", node.Name)

			if err := replaceNotReadyNode(ctx, &node, clientset, logger); err != nil {
				logger.Errorf("Failed to replace not ready node %s: %v", node.Name, err)
				continue
			}

			logger.Warnf("✅ Successfully replaced not ready node: %s", node.Name)
		}
	}

	return nil
}

// isNodeReady checks if a node is in Ready state
func isNodeReady(node *v1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady {
			return condition.Status == v1.ConditionTrue
		}
	}
	return false // If no Ready condition is found, consider the node not ready
}

// replaceNotReadyNode handles the process of replacing a not ready node
func replaceNotReadyNode(ctx context.Context, node *v1.Node, clientset *kubernetes.Clientset, logger *utils.Logger) error {
	nodeName := node.Name

	// Save the original node labels to check if topology labels need to be reapplied
	originalLabels := node.Labels

	// Step 1: Delete the node from Kubernetes
	logger.Debugf("🗑️ Deleting node from Kubernetes: %s", nodeName)
	if err := clientset.CoreV1().Nodes().Delete(ctx, nodeName, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("failed to delete node %s: %w", nodeName, err)
	}

	// Step 2: Find and restart the corresponding Docker container to bring it back
	logger.Debugf("🔄 Restarting Docker container for node: %s", nodeName)
	if err := restartNodeContainer(ctx, nodeName, logger); err != nil {
		return fmt.Errorf("failed to restart container for node %s: %w", nodeName, err)
	}

	// Step 3: Wait for the node to rejoin and become ready
	logger.Debugf("⏳ Waiting for node to rejoin cluster: %s", nodeName)
	if err := waitForSingleNodeReady(ctx, clientset, nodeName, defaultPollTimeout, logger); err != nil {
		return fmt.Errorf("node %s did not rejoin cluster: %w", nodeName, err)
	}

	// Step 4: Reapply topology labels if they existed on the original node
	// Check if this node had topology labels before replacement
	hasTopologyLabels := false
	for key := range originalLabels {
		if key == TopologyLabelZone || key == TopologyLabelBlock || key == TopologyLabelRack {
			hasTopologyLabels = true
			break
		}
	}

	if hasTopologyLabels {
		logger.Debugf("🏷️  Reapplying topology labels to replaced node: %s", nodeName)
		if err := reapplyTopologyLabelsToNode(ctx, clientset, nodeName, logger); err != nil {
			// Log warning but don't fail - node is functional, just missing topology labels
			logger.Warnf("⚠️  Failed to reapply topology labels to node %s: %v", nodeName, err)
		}
	}

	return nil
}

// restartNodeContainer finds and restarts the Docker container corresponding to a k3d node
func restartNodeContainer(ctx context.Context, nodeName string, logger *utils.Logger) error {
	// Create Docker client
	dockerClient, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer dockerClient.Close()

	// List all containers to find the one corresponding to this node
	containers, err := dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list Docker containers: %w", err)
	}

	// Find the container for this specific node. The Node names match container names exactly
	// (e.g., k3d-gang-scheduling-pcs-pcsg-scaling-test-cluster-agent-24).
	var targetContainer *container.Summary
	for _, c := range containers {
		for _, name := range c.Names {
			// Remove leading slash from container name
			containerName := strings.TrimPrefix(name, "/")

			// Direct name match - node names are the same as container names
			if containerName == nodeName {
				targetContainer = &c
				logger.Debugf("  🎯 Found matching container: %s (ID: %s)", containerName, c.ID[:12])
				break
			}
		}
		if targetContainer != nil {
			break
		}
	}

	if targetContainer == nil {
		return fmt.Errorf("could not find Docker container for node %s", nodeName)
	}

	// Restart the container
	logger.Debugf("  🔄 Restarting container: %s", targetContainer.ID[:12])
	if err := dockerClient.ContainerRestart(ctx, targetContainer.ID, container.StopOptions{}); err != nil {
		return fmt.Errorf("failed to restart container %s: %w", targetContainer.ID[:12], err)
	}

	logger.Debugf("  ✅ Container restarted successfully: %s", targetContainer.ID[:12])
	return nil
}

// prepullImages pre-pulls container images and pushes them to the local k3d registry
// This significantly speeds up cluster startup by avoiding image pull delays during pod creation.
// Note: Using the registry is much faster than k3d's image import which creates tar archives.
func prepullImages(ctx context.Context, images []string, registryPort string, logger *utils.Logger) error {
	logger.Info("📦 Pre-pulling and pushing container images to local registry...")

	// Create Docker client
	dockerClient, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer dockerClient.Close()

	// Process all images in parallel
	var wg sync.WaitGroup
	errChan := make(chan error, len(images))

	for _, imageName := range images {
		wg.Add(1)
		go func(img string) {
			defer wg.Done()

			logger.Debugf("  🔄 Pulling image: %s", img)

			// Pull the image using Docker API
			pullReader, err := dockerClient.ImagePull(ctx, img, image.PullOptions{})
			if err != nil {
				errChan <- fmt.Errorf("failed to pull %s: %w", img, err)
				return
			}

			// We need to read the output to ensure the pull completes
			if logger.GetLevel() == utils.DebugLevel {
				_, err = io.Copy(logger.WriterLevel(utils.DebugLevel), pullReader)
			} else {
				_, err = io.Copy(io.Discard, pullReader)
			}
			pullReader.Close()
			if err != nil {
				errChan <- fmt.Errorf("failed to read pull output for %s: %w", img, err)
				return
			}

			logger.Debugf("  ✅ Successfully pulled: %s", img)

			// Extract the image path WITHOUT the registry domain for storage in local registry
			// When k3s uses a mirror, it strips the registry domain from the path
			// e.g., "registry.k8s.io/nfd/node-feature-discovery:v0.17.3" -> "nfd/node-feature-discovery:v0.17.3"
			// So when k3s pulls from mirror, it looks for "http://registry:5001/nfd/node-feature-discovery:v0.17.3"
			imagePath := stripRegistryDomain(img)

			registryImage := fmt.Sprintf("localhost:%s/%s", registryPort, imagePath)

			logger.Debugf("  🏷️  Tagging image for local registry: %s (from %s)", registryImage, img)

			// Tag the image for the local registry
			if err := dockerClient.ImageTag(ctx, img, registryImage); err != nil {
				errChan <- fmt.Errorf("failed to tag image %s as %s: %w", img, registryImage, err)
				return
			}

			logger.Debugf("  📤 Pushing to local registry: %s", registryImage)

			// Push the image to the local registry
			pushReader, err := dockerClient.ImagePush(ctx, registryImage, image.PushOptions{})
			if err != nil {
				errChan <- fmt.Errorf("failed to push %s: %w", registryImage, err)
				return
			}

			// Consume the push output
			if logger.GetLevel() == utils.DebugLevel {
				_, err = io.Copy(logger.WriterLevel(utils.DebugLevel), pushReader)
			} else {
				_, err = io.Copy(io.Discard, pushReader)
			}
			pushReader.Close()
			if err != nil {
				errChan <- fmt.Errorf("failed to read push output for %s: %w", registryImage, err)
				return
			}

			logger.Debugf("  ✅ Successfully pushed to registry: %s -> %s", img, imagePath)
		}(imageName)
	}

	// Wait for all operations to complete
	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		return err
	}

	logger.Info("✅ Container images pre-loaded into local registry")
	return nil
}

// verifyRegistryImages verifies that images are accessible in the registry
func verifyRegistryImages(images []string, registryPort string, logger *utils.Logger) error {
	logger.Debug("🔍 Verifying images are in registry...")

	for _, img := range images {
		// Extract the image path WITHOUT the registry domain (same logic as prepullImages)
		imagePath := stripRegistryDomain(img)

		// Try to pull from local registry to verify it exists
		registryImage := fmt.Sprintf("localhost:%s/%s", registryPort, imagePath)
		logger.Debugf("  Checking: %s (original: %s)", registryImage, img)

		// Create Docker client
		dockerClient, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		if err != nil {
			return fmt.Errorf("failed to create Docker client: %w", err)
		}

		ctx := context.Background()
		reader, err := dockerClient.ImagePull(ctx, registryImage, image.PullOptions{})
		if err != nil {
			dockerClient.Close()
			return fmt.Errorf("failed to verify %s in registry: %w", imagePath, err)
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			reader.Close()
			dockerClient.Close()
			return fmt.Errorf("failed to drain image pull response: %w", err)
		}
		reader.Close()
		dockerClient.Close()

		logger.Debugf("  ✅ Verified: %s", imagePath)
	}

	return nil
}

// findMissingImages finds all images used in the specified namespaces that are not in the
// imagesToPrepull list. Returns a slice of missing image names.
func findMissingImages(ctx context.Context, clientset *kubernetes.Clientset, namespaces []string, imagesToPrepull []string, logger *utils.Logger) []string {
	logger.Info("🔍 Checking deployed images against prepull list...")

	// Get all images from all specified namespaces
	deployedImages := make(map[string]bool)

	// Check each namespace
	for _, namespace := range namespaces {
		images, err := collectImagesFromNamespace(ctx, clientset, namespace, logger)
		if err != nil {
			logger.Warnf("Failed to collect images from %s namespace: %v", namespace, err)
			return nil
		}
		// Add images to the set
		for _, img := range images {
			deployedImages[img] = true
		}
	}

	// Create a set of prepulled images for quick lookup
	prepulledImages := make(map[string]bool)
	for _, img := range imagesToPrepull {
		prepulledImages[img] = true
	}

	// Check if any deployed images are missing from prepull list
	var missingImages []string
	for image := range deployedImages {
		if !prepulledImages[image] {
			missingImages = append(missingImages, image)
			logger.Debugf("  ❌ Image not in prepull list: %s", image)
		} else {
			logger.Debugf("  ✅ Image found in prepull list: %s", image)
		}
	}

	return missingImages
}

// collectImagesFromNamespace collects all container and init container images from pods in a namespace
// and returns them as a slice of unique image names.
func collectImagesFromNamespace(ctx context.Context, clientset *kubernetes.Clientset, namespace string, logger *utils.Logger) ([]string, error) {
	logger.Debugf("  Collecting images from namespace: %s", namespace)

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods in namespace %s: %w", namespace, err)
	}

	// Use a map to track unique images
	imageSet := make(map[string]bool)

	for _, pod := range pods.Items {
		// Collect images from init containers
		for _, container := range pod.Spec.InitContainers {
			if container.Image != "" {
				imageSet[container.Image] = true
				logger.Debugf("    Found init container image: %s (pod: %s)", container.Image, pod.Name)
			}
		}

		// Collect images from regular containers
		for _, container := range pod.Spec.Containers {
			if container.Image != "" {
				imageSet[container.Image] = true
				logger.Debugf("    Found container image: %s (pod: %s)", container.Image, pod.Name)
			}
		}
	}

	// Convert map to slice
	images := make([]string, 0, len(imageSet))
	for img := range imageSet {
		images = append(images, img)
	}

	logger.Debugf("  Found %d unique images in namespace %s", len(images), namespace)
	return images, nil
}

// buildLDFlagsForE2E generates ldflags for e2e tests to properly set version information.
// This overrides the git placeholders in internal/version/version.go that would otherwise
// contain invalid values like "$Format:%H$" which causes image reference parsing errors.
func buildLDFlagsForE2E() string {
	// Get current git commit hash (or use a placeholder for e2e tests)
	gitCommit := "e2e-test-commit"

	// Set build date to current time
	buildDate := time.Now().Format("2006-01-02T15:04:05Z07:00")

	// Set tree state to clean for e2e tests
	treeState := "clean"

	// Build the ldflags string
	// Note: The module path changed from github.com/NVIDIA to github.com/ai-dynamo
	ldflags := fmt.Sprintf("-X github.com/ai-dynamo/grove/operator/internal/version.gitCommit=%s -X github.com/ai-dynamo/grove/operator/internal/version.gitTreeState=%s -X github.com/ai-dynamo/grove/operator/internal/version.buildDate=%s -X github.com/ai-dynamo/grove/operator/internal/version.gitVersion=E2E_TESTS",
		gitCommit, treeState, buildDate)

	return ldflags
}

// waitForWebhookReady waits for the webhook server to be ready.
// This ensures the webhook server is fully registered with the Kubernetes API server
// and can handle admission requests before tests start.
// We do this by making a dry-run request to create a PodCliqueSet using workload1.yaml -
// if the webhook is ready, the request will be processed (may fail validation, but that's fine).
// If the webhook is not ready, we'll get "no endpoints available" or similar errors.
func waitForWebhookReady(ctx context.Context, restConfig *rest.Config, logger *utils.Logger) error {
	logger.Info("⏳ Waiting for Grove webhook to be ready...")

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	// Define the GVR for PodCliqueSet
	pcsGVR := schema.GroupVersionResource{
		Group:    "grove.io",
		Version:  "v1alpha1",
		Resource: "podcliquesets",
	}

	// Get the path to workload1.yaml relative to this source file
	_, currentFile, _, _ := runtime.Caller(0)
	workload1YAMLPath := filepath.Join(filepath.Dir(currentFile), "../yaml/workload1.yaml")

	// Read workload1.yaml to get a real PodCliqueSet for testing the webhook
	workloadYAML, err := os.ReadFile(workload1YAMLPath)
	if err != nil {
		return fmt.Errorf("failed to read workload1.yaml: %w", err)
	}

	testPCS := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(workloadYAML, &testPCS.Object); err != nil {
		return fmt.Errorf("failed to parse workload1.yaml: %w", err)
	}

	// Override the name and namespace for the webhook test
	testPCS.SetName("webhook-ready-test")
	testPCS.SetNamespace("default")

	return utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		// Try to create the PodCliqueSet with dry-run mode
		// This will invoke the webhook without actually creating the resource
		_, err := dynamicClient.Resource(pcsGVR).Namespace("default").Create(
			ctx,
			testPCS,
			metav1.CreateOptions{
				DryRun: []string{metav1.DryRunAll},
			},
		)

		if err != nil {
			errStr := err.Error()
			// These errors indicate the webhook is not ready yet
			if isTransient(errStr) {
				logger.Debugf("  Webhook not ready yet: %v", err)
				return false, nil
			}
			// Any other error (including validation errors) means the webhook responded
			// which is what we want to confirm
			logger.Infof("✅ Grove webhook is ready (responded with: %v)", err)
			return true, nil
		}

		// Success means the webhook processed the request
		logger.Info("✅ Grove webhook is ready")
		return true, nil
	})
}

// waitForSingleNodeReady waits for a specific node to become ready after container restart
func waitForSingleNodeReady(ctx context.Context, clientset *kubernetes.Clientset, nodeName string, timeout time.Duration, logger *utils.Logger) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("timeout waiting for node %s to become ready", nodeName)
		case <-ticker.C:
			node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
			if err != nil {
				// Node might not have rejoined yet (k3d agent still starting up)
				logger.Debugf("  Node %s not found yet (k3d agent still connecting), waiting...", nodeName)
				continue
			}

			if isNodeReady(node) {
				logger.Debugf("✅ Node %s has rejoined and is ready", nodeName)
				return nil
			}

			logger.Debugf("  Node %s found but not ready yet, waiting...", nodeName)
		}
	}
}

// reapplyTopologyLabelsToNode reapplies topology labels to a single node after replacement
func reapplyTopologyLabelsToNode(ctx context.Context, clientset *kubernetes.Clientset, nodeName string, logger *utils.Logger) error {
	// Get all worker nodes to determine the index of this node
	workerLabelSelector := GetWorkerNodeLabelSelector()
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: workerLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list worker nodes: %w", err)
	}

	// Sort nodes by name to maintain consistent indexing (same as applyTopologyLabels)
	sortedNodes := make([]v1.Node, len(nodes.Items))
	copy(sortedNodes, nodes.Items)
	sort.Slice(sortedNodes, func(i, j int) bool { return sortedNodes[i].Name < sortedNodes[j].Name })

	// Find the index of the target node
	nodeIndex := -1
	for idx, node := range sortedNodes {
		if node.Name == nodeName {
			nodeIndex = idx
			break
		}
	}

	if nodeIndex == -1 {
		return fmt.Errorf("node %s not found in worker nodes list", nodeName)
	}

	// Apply topology labels based on the node's index (same logic as applyTopologyLabels)
	topologyLabels := fmt.Sprintf(`{"metadata":{"labels":{"%s":"%s","%s":"%s","%s":"%s"}}}`,
		TopologyLabelZone, GetZoneForNodeIndex(nodeIndex),
		TopologyLabelBlock, GetBlockForNodeIndex(nodeIndex),
		TopologyLabelRack, GetRackForNodeIndex(nodeIndex))

	_, err = clientset.CoreV1().Nodes().Patch(
		ctx,
		nodeName,
		k8stypes.StrategicMergePatchType,
		[]byte(topologyLabels),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to patch node %s with topology labels: %w", nodeName, err)
	}

	logger.Debugf("✅ Reapplied topology labels to node %s (index %d): zone=%s, block=%s, rack=%s",
		nodeName, nodeIndex,
		GetZoneForNodeIndex(nodeIndex),
		GetBlockForNodeIndex(nodeIndex),
		GetRackForNodeIndex(nodeIndex))

	return nil
}
