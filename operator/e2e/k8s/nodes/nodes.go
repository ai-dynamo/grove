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

	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultNodePollInterval = 2 * time.Second

// NodeManager provides node operations using pre-created Kubernetes clients.
type NodeManager struct {
	k8s    *k8sclient.Client
	logger *log.Logger
}

// NewNodeManager creates a NodeManager bound to the given K8s client.
func NewNodeManager(k8s *k8sclient.Client, logger *log.Logger) *NodeManager {
	return &NodeManager{k8s: k8s, logger: logger}
}

// SetSchedulable sets a node to be schedulable or unschedulable (cordon/uncordon).
// Uses retry logic to handle optimistic concurrency conflicts.
func (nm *NodeManager) SetSchedulable(ctx context.Context, nodeName string, schedulable bool) error {
	action := "uncordon"
	if !schedulable {
		action = "cordon"
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var node v1.Node
		if err := nm.k8s.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
			return fmt.Errorf("failed to get node %s for %s: %w", nodeName, action, err)
		}

		if node.Spec.Unschedulable == !schedulable {
			return nil // Already in desired state
		}

		node.Spec.Unschedulable = !schedulable
		if err := nm.k8s.Update(ctx, &node); err != nil {
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
	var nodeList v1.NodeList
	if err := nm.k8s.List(ctx, &nodeList); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	var workerNodes []string
	for _, node := range nodeList.Items {
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

// WaitForReady waits for a specific node to become ready and returns it.
// NotFound errors are treated as "not yet created" and polling continues.
func (nm *NodeManager) WaitForReady(ctx context.Context, nodeName string, timeout time.Duration) (*v1.Node, error) {
	w := waiter.New[*v1.Node]().
		WithTimeout(timeout).
		WithInterval(defaultNodePollInterval).
		WithLogger(nm.logger)
	return w.WaitFor(ctx, waiter.FetchByName(nodeName, k8sclient.Getter[*v1.Node](nm.k8s, "")),
		func(node *v1.Node) bool { return node != nil && IsReady(node) })
}

// SetNodeSchedulable sets a node to be schedulable or unschedulable (cordon/uncordon).
// Uses retry logic for optimistic concurrency conflicts.
func SetNodeSchedulable(ctx context.Context, cl client.Client, nodeName string, schedulable bool) error {
	action := "uncordon"
	if !schedulable {
		action = "cordon"
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var node v1.Node
		if err := cl.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
			return fmt.Errorf("failed to get node %s for %s: %w", nodeName, action, err)
		}

		if node.Spec.Unschedulable == !schedulable {
			return nil
		}

		node.Spec.Unschedulable = !schedulable
		if err := cl.Update(ctx, &node); err != nil {
			return fmt.Errorf("failed to %s node %s: %w", action, nodeName, err)
		}
		return nil
	})
}

