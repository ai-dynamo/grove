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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerclient "github.com/docker/docker/client"
	"github.com/k3d-io/k3d/v5/pkg/client"
	"github.com/k3d-io/k3d/v5/pkg/config"
	"github.com/k3d-io/k3d/v5/pkg/config/types"
	"github.com/k3d-io/k3d/v5/pkg/config/v1alpha5"
	k3dlogger "github.com/k3d-io/k3d/v5/pkg/logger"
	"github.com/k3d-io/k3d/v5/pkg/runtimes"
	k3d "github.com/k3d-io/k3d/v5/pkg/types"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// defaultPollTimeout is the timeout for most polling conditions
	defaultPollTimeout = 5 * time.Minute
	// defaultPollInterval is the interval for most polling conditions
	defaultPollInterval = 5 * time.Second
)

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

// DefaultClusterConfig returns a sensible default cluster configuration
func DefaultClusterConfig() ClusterConfig {
	return ClusterConfig{
		Name:              "test-k3d-cluster",
		ControlPlaneNodes: 1,
		WorkerNodes:       2,
		Image:             "rancher/k3s:v1.28.8-k3s1",
		HostPort:          "6550",
		LoadBalancerPort:  "8080:80",
		WorkerMemory:      "150m",
		EnableRegistry:    false,
		RegistryPort:      "5001",
	}
}

// ensureClusterDoesNotExist removes any stale k3d cluster with the same name from previous runs.
func ensureClusterDoesNotExist(ctx context.Context, clusterName string, logger *utils.Logger) error {
	cluster := &k3d.Cluster{Name: clusterName}

	existingCluster, err := client.ClusterGet(ctx, runtimes.Docker, cluster)
	if err != nil {
		if errors.Is(err, client.ClusterGetNoNodesFoundError) {
			return nil
		}
		return fmt.Errorf("failed to inspect existing k3d cluster %s: %w", clusterName, err)
	}

	logger.Warnf("🧹 Removing stale k3d cluster '%s' before setup", clusterName)
	if err := client.ClusterDelete(ctx, runtimes.Docker, existingCluster, k3d.ClusterDeleteOpts{}); err != nil {
		return fmt.Errorf("failed to delete existing k3d cluster %s: %w", clusterName, err)
	}

	return nil
}

// ensureRegistryDoesNotExist removes any stale k3d registry container from previous runs.
func ensureRegistryDoesNotExist(ctx context.Context, clusterName string, logger *utils.Logger) error {
	registryContainerName := fmt.Sprintf("k3d-%s-registry", clusterName)

	dockerClient, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer dockerClient.Close()

	filterArgs := filters.NewArgs()
	filterArgs.Add("name", registryContainerName)

	containers, err := dockerClient.ContainerList(ctx, container.ListOptions{All: true, Filters: filterArgs})
	if err != nil {
		return fmt.Errorf("failed to list Docker containers: %w", err)
	}

	if len(containers) == 0 {
		return nil
	}

	for _, c := range containers {
		displayName := registryContainerName
		if len(c.Names) > 0 {
			displayName = strings.TrimPrefix(c.Names[0], "/")
		}

		logger.Warnf("🧹 Removing stale k3d registry container %s (%s) before cluster setup", displayName, c.ID[:12])

		if err := dockerClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			return fmt.Errorf("failed to remove existing registry container %s: %w", displayName, err)
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

	kaiConfig := &HelmInstallConfig{
		ReleaseName:     "kai-scheduler",
		ChartRef:        "oci://ghcr.io/nvidia/kai-scheduler/kai-scheduler",
		ChartVersion:    "v0.9.3",
		Namespace:       "kai-scheduler",
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

	nvidiaConfig := &HelmInstallConfig{
		ReleaseName:     "nvidia-gpu-operator",
		ChartRef:        "gpu-operator",
		ChartVersion:    "v25.3.4",
		Namespace:       "gpu-operator",
		RestConfig:      restConfig,
		CreateNamespace: true,
		Wait:            false,
		GenerateName:    false,
		RepoURL:         "https://helm.ngc.nvidia.com/nvidia",
		Values: map[string]interface{}{
			"tolerations":        tolerations,
			"driver":             map[string]interface{}{"enabled": false},
			"toolkit":            map[string]interface{}{"enabled": false},
			"devicePlugin":       map[string]interface{}{"enabled": false},
			"dcgmExporter":       map[string]interface{}{"enabled": false},
			"gfd":                map[string]interface{}{"enabled": false},
			"migManager":         map[string]interface{}{"enabled": false},
			"nodeStatusExporter": map[string]interface{}{"enabled": false},
		},
		HelmLoggerFunc: logger.Debugf,
		Logger:         logger,
	}

	logger.Info("🚀 Installing Grove, Kai Scheduler, and NVIDIA GPU Operator...")
	if err := InstallCoreComponents(ctx, restConfig, kaiConfig, nvidiaConfig, skaffoldYAMLPath, cfg.RegistryPort, logger); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("component installation failed: %w", err)
	}

	// Wait for Grove pods to be ready (0 = skip count validation)
	if err := utils.WaitForPodsInNamespace(ctx, "grove-system", restConfig, 0, defaultPollTimeout, defaultPollInterval, logger); err != nil {
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
	if err := utils.WaitForPodsInNamespace(ctx, nvidiaConfig.Namespace, nvidiaConfig.RestConfig, 0, defaultPollTimeout, defaultPollInterval, nvidiaConfig.Logger); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("NVIDIA GPU Operator not ready: %w", err)
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
		clusterConfig.Registries = v1alpha5.SimpleConfigRegistries{
			Create: &v1alpha5.SimpleConfigRegistryCreateConfig{
				Name:     "registry",
				Host:     "0.0.0.0",
				HostPort: cfg.RegistryPort,
			},
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
		if err := ensureRegistryDoesNotExist(ctx, cfg.Name, logger); err != nil {
			return nil, nil, err
		}
	}

	// this is the cleanup function, we always return it now so the caller can decide to use it or not
	cleanup := func() {
		logger.Debug("🗑️ Deleting cluster...")
		if err := client.ClusterDelete(ctx, runtimes.Docker, &k3dConfig.Cluster, k3d.ClusterDeleteOpts{}); err != nil {
			logger.Errorf("Failed to delete cluster: %v", err)
		} else {
			logger.Info("✅ Cluster deleted successfully")
		}
	}

	// Create cluster
	logger.Debugf("🚀 Creating cluster '%s' with %d server(s) and %d worker node(s)...",
		k3dConfig.Name, cfg.ControlPlaneNodes, cfg.WorkerNodes)

	if err := client.ClusterRun(ctx, runtimes.Docker, k3dConfig); err != nil {
		return nil, cleanup, fmt.Errorf("failed to create cluster: %w", err)
	}

	// Get kubeconfig
	logger.Debug("📄 Fetching kubeconfig...")
	cluster, err := client.ClusterGet(ctx, runtimes.Docker, &k3dConfig.Cluster)
	if err != nil {
		return nil, cleanup, fmt.Errorf("could not get cluster: %w", err)
	}

	kubeconfig, err := client.KubeconfigGet(ctx, runtimes.Docker, cluster)
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

// InstallCoreComponents installs the core components (Grove via Skaffold, Kai Scheduler and NVIDIA GPU Operator via Helm)
func InstallCoreComponents(ctx context.Context, restConfig *rest.Config, kaiConfig *HelmInstallConfig, nvidiaConfig *HelmInstallConfig, skaffoldYAMLPath string, registryPort string, logger *utils.Logger) error {
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
				Profiles:         []string{"debug"},
				PushRepo:         fmt.Sprintf("localhost:%s", registryPort),
				PullRepo:         fmt.Sprintf("registry:%s", registryPort),
				Namespace:        "grove-system",
				Env: map[string]string{
					"VERSION":  "E2E_TESTS", //version required but it doesn't matter which for e2e tests
					"LD_FLAGS": "",          // Empty for e2e tests
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

	// Install NVIDIA GPU Operator
	if nvidiaConfig != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Debug("🚀 Starting NVIDIA GPU Operator installation...")

			installFunc := func() error {
				_, err := InstallHelmChart(nvidiaConfig)
				return err
			}

			err := retryInstallation(installFunc, "NVIDIA GPU Operator", maxRetries, retryDelay, logger)
			if err != nil {
				errChan <- err
			} else {
				logger.Debug("✅ NVIDIA GPU Operator installation completed successfully")
			}
		}()
	}

	// Wait for all installations to complete
	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		return err // Return the first error encountered
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
