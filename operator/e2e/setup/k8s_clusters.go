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

// Package setup provides utilities for connecting to Kubernetes clusters for E2E testing.
//
// The cluster must be created beforehand with Grove operator, Kai scheduler, and required
// test infrastructure already deployed. For local development with k3d, you can use:
//
//	./operator/hack/create-e2e-cluster.py
//
// This package only handles connecting to existing clusters - it does not create clusters.
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// defaultPollTimeout is the default timeout for polling operations
	defaultPollTimeout = 5 * time.Minute
	// defaultPollInterval is the interval for most polling conditions
	defaultPollInterval = 5 * time.Second
)

// GetRestConfig returns a REST config for connecting to a Kubernetes cluster.
// It tries the following methods in order:
//  1. KUBECONFIG environment variable (if set)
//  2. Default kubeconfig at ~/.kube/config
//
// For local development with k3d, run './operator/hack/create-e2e-cluster.py' first
// to create a cluster and configure kubectl.
func GetRestConfig() (*rest.Config, error) {
	// Try KUBECONFIG environment variable first
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		// Fall back to default location
		homeDir, err := os.UserHomeDir()
		if err == nil {
			kubeconfigPath = filepath.Join(homeDir, ".kube", "config")
		}
	}

	// Try to load from kubeconfig file
	if kubeconfigPath != "" {
		if _, err := os.Stat(kubeconfigPath); err == nil {
			config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
			if err == nil {
				return config, nil
			}
		}
	}

	return nil, fmt.Errorf("failed to get kubernetes config: no KUBECONFIG found and ~/.kube/config not accessible." +
		"For local development, run './operator/hack/create-e2e-cluster.py' first")
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
		if !utils.IsNodeReady(&node) {
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

// replaceNotReadyNode handles the process of replacing a not ready node
func replaceNotReadyNode(ctx context.Context, node *v1.Node, clientset *kubernetes.Clientset, logger *utils.Logger) error {
	nodeName := node.Name
	originalNodeLabels := node.Labels

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

	// Step 3: Wait for the node to become ready
	logger.Debugf("⏳ Waiting for node to become ready: %s", nodeName)
	readyNode, err := utils.WaitAndGetReadyNode(ctx, clientset, nodeName, defaultPollTimeout, logger)
	if err != nil {
		return fmt.Errorf("node %s did not become ready: %w", nodeName, err)
	}

	// Step 4: Reapply original labels to the replaced node
	logger.Debugf("🏷️  Reapplying original labels to replaced node: %s", nodeName)
	if err := reapplyNodeLabels(ctx, clientset, readyNode, originalNodeLabels, logger); err != nil {
		return fmt.Errorf("failed to reapply labels to node %s: %w", nodeName, err)
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

// reapplyNodeLabels reapplies the original labels to a replaced node
func reapplyNodeLabels(ctx context.Context, clientset *kubernetes.Clientset, node *v1.Node, labels map[string]string, logger *utils.Logger) error {
	nodeTopologyLabelsPatch := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
		},
	}

	patchBytes, err := json.Marshal(nodeTopologyLabelsPatch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch data for node %s: %w", node.Name, err)
	}

	_, err = clientset.CoreV1().Nodes().Patch(ctx, node.Name, k8stypes.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch node %s with labels: %w", node.Name, err)
	}

	logger.Debugf("✅ Reapplied original labels to node %s", node.Name)
	return nil
}
