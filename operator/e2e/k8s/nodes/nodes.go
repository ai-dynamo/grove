//go:build e2e

// /*
// Copyright 2026 The Grove Authors.
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

package nodes

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

const defaultNodePollInterval = 2 * time.Second

// NodeManager provides node operations using pre-created Kubernetes clients.
type NodeManager struct {
	clients *clients.Clients
	logger  *utils.Logger
}

// NewNodeManager creates a NodeManager bound to the given clients.
func NewNodeManager(c *clients.Clients, logger *utils.Logger) *NodeManager {
	return &NodeManager{clients: c, logger: logger}
}

// SetSchedulable sets a node to be schedulable or unschedulable (cordon/uncordon).
// Uses retry logic to handle optimistic concurrency conflicts.
func (nm *NodeManager) SetSchedulable(ctx context.Context, nodeName string, schedulable bool) error {
	action := "uncordon"
	if !schedulable {
		action = "cordon"
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := nm.clients.Clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get node %s for %s: %w", nodeName, action, err)
		}

		if node.Spec.Unschedulable == !schedulable {
			return nil // Already in desired state
		}

		node.Spec.Unschedulable = !schedulable
		_, err = nm.clients.Clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to %s node %s: %w", action, nodeName, err)
		}
		return nil
	})
}

// Cordon marks a node as unschedulable.
func (nm *NodeManager) Cordon(ctx context.Context, nodeName string) error {
	return nm.SetSchedulable(ctx, nodeName, false)
}

// Uncordon marks a node as schedulable.
func (nm *NodeManager) Uncordon(ctx context.Context, nodeName string) error {
	return nm.SetSchedulable(ctx, nodeName, true)
}

// CordonAll cordons multiple nodes. Returns the first error encountered.
func (nm *NodeManager) CordonAll(ctx context.Context, nodeNames []string) error {
	for _, name := range nodeNames {
		if err := nm.Cordon(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// UncordonAll uncordons multiple nodes. Returns the first error encountered.
func (nm *NodeManager) UncordonAll(ctx context.Context, nodeNames []string) error {
	for _, name := range nodeNames {
		if err := nm.Uncordon(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// GetWorkerNodes retrieves the names of all worker nodes (excludes control plane).
func (nm *NodeManager) GetWorkerNodes(ctx context.Context) ([]string, error) {
	nodes, err := nm.clients.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	var workerNodes []string
	for _, node := range nodes.Items {
		if _, isControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]; !isControlPlane {
			workerNodes = append(workerNodes, node.Name)
		}
	}
	return workerNodes, nil
}

// IsReady checks if a node has Ready=True condition.
func IsReady(node *v1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady {
			return condition.Status == v1.ConditionTrue
		}
	}
	return false
}

// WaitAndGetReady waits for a specific node to become ready and returns it.
func (nm *NodeManager) WaitAndGetReady(ctx context.Context, nodeName string, timeout time.Duration) (*v1.Node, error) {
	var node *v1.Node
	err := k8s.PollForCondition(ctx, timeout, defaultNodePollInterval, func() (bool, error) {
		var err error
		node, err = nm.clients.Clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				nm.logger.Debugf("Node %s not found yet, waiting...", nodeName)
				return false, nil
			}
			return false, err
		}
		if IsReady(node) {
			nm.logger.Debugf("Node %s is ready", nodeName)
			return true, nil
		}
		nm.logger.Debugf("Node %s found but not ready yet, waiting...", nodeName)
		return false, nil
	})
	return node, err
}
