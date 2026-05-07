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

package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/setup"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NodeLabelChange describes label mutations for a single node.
type NodeLabelChange struct {
	NodeName     string
	AddLabels    map[string]string // Labels to add (key → value)
	RemoveLabels []string          // Label keys to remove
}

type originalLabelState struct {
	Exists bool
	Value  string
}

type touchedLabelSnapshot map[string]map[string]originalLabelState

// MutateNodeLabels applies label changes to nodes. Returns a cleanup function that
// restores each touched label to its original state.
// The cleanup function is safe to call multiple times. It reports rollback failures via t.Errorf.
func (tv *TopologyVerifier) MutateNodeLabels(ctx context.Context, t testing.TB, changes []NodeLabelChange) (cleanup func(), err error) {
	// Snapshot the original state of each touched label key before any mutation,
	// so rollback can restore exactly what this helper changed.
	originalTouchedLabels := make(touchedLabelSnapshot) // nodeName -> labelKey -> original state

	// Track which nodes were successfully mutated for partial rollback on error.
	var mutatedNodes []string
	mutatedNodeSet := make(map[string]struct{})

	// Apply all mutations and capture original state.
	for _, change := range changes {
		// GET the node to capture current labels.
		var node v1.Node
		if err := tv.cl.Get(ctx, types.NamespacedName{Name: change.NodeName}, &node); err != nil {
			// Rollback any already-mutated nodes.
			cleanupErr := rollbackNodeLabels(ctx, tv, mutatedNodes, originalTouchedLabels)
			if cleanupErr != nil {
				return nil, fmt.Errorf("failed to get node %s: %w; additionally failed to rollback: %v", change.NodeName, err, cleanupErr)
			}
			return nil, fmt.Errorf("failed to get node %s: %w", change.NodeName, err)
		}

		captureTouchedLabelSnapshot(originalTouchedLabels, change, node.Labels)

		// Build the strategic merge patch.
		labels := make(map[string]interface{})

		// Add new labels.
		for key, value := range change.AddLabels {
			labels[key] = value
		}

		// Remove labels by setting them to null.
		for _, key := range change.RemoveLabels {
			labels[key] = nil
		}

		patchData := map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": labels,
			},
		}

		patchBytes, err := json.Marshal(patchData)
		if err != nil {
			cleanupErr := rollbackNodeLabels(ctx, tv, mutatedNodes, originalTouchedLabels)
			if cleanupErr != nil {
				return nil, fmt.Errorf("failed to marshal patch for node %s: %w; additionally failed to rollback: %v", change.NodeName, err, cleanupErr)
			}
			return nil, fmt.Errorf("failed to marshal patch for node %s: %w", change.NodeName, err)
		}

		// Apply the patch.
		if err := tv.cl.Patch(ctx, &node, client.RawPatch(types.StrategicMergePatchType, patchBytes)); err != nil {
			cleanupErr := rollbackNodeLabels(ctx, tv, mutatedNodes, originalTouchedLabels)
			if cleanupErr != nil {
				return nil, fmt.Errorf("failed to patch node %s: %w; additionally failed to rollback: %v", change.NodeName, err, cleanupErr)
			}
			return nil, fmt.Errorf("failed to patch node %s: %w", change.NodeName, err)
		}

		if _, seen := mutatedNodeSet[change.NodeName]; !seen {
			mutatedNodes = append(mutatedNodes, change.NodeName)
			mutatedNodeSet[change.NodeName] = struct{}{}
		}

		tv.logger.Debugf("Mutated labels on node %s: added %d labels, removed %d labels", change.NodeName, len(change.AddLabels), len(change.RemoveLabels))
	}

	// Return a cleanup function that reverses all changes.
	var cleanedUp bool
	cleanup = func() {
		if cleanedUp {
			return
		}
		cleanedUp = true
		cleanupErr := rollbackNodeLabels(ctx, tv, mutatedNodes, originalTouchedLabels)
		if cleanupErr != nil {
			t.Errorf("Failed to cleanup node labels: %v", cleanupErr)
		}
	}

	return cleanup, nil
}

func captureTouchedLabelSnapshot(snapshot touchedLabelSnapshot, change NodeLabelChange, labels map[string]string) {
	if _, ok := snapshot[change.NodeName]; !ok {
		snapshot[change.NodeName] = make(map[string]originalLabelState)
	}

	capture := func(labelKey string) {
		if _, tracked := snapshot[change.NodeName][labelKey]; tracked {
			return
		}
		if val, exists := labels[labelKey]; exists {
			snapshot[change.NodeName][labelKey] = originalLabelState{Exists: true, Value: val}
		} else {
			snapshot[change.NodeName][labelKey] = originalLabelState{}
		}
	}

	for labelKey := range change.AddLabels {
		capture(labelKey)
	}
	for _, labelKey := range change.RemoveLabels {
		capture(labelKey)
	}
}

// rollbackNodeLabels reverses label mutations on the specified nodes:
// - Restores original values for labels that existed before mutation.
// - Removes labels that did not exist before mutation.
func rollbackNodeLabels(ctx context.Context, tv *TopologyVerifier, mutatedNodes []string, originalTouchedLabels touchedLabelSnapshot) error {
	for _, nodeName := range mutatedNodes {
		labels := make(map[string]interface{})

		for labelKey, originalState := range originalTouchedLabels[nodeName] {
			if originalState.Exists {
				labels[labelKey] = originalState.Value
			} else {
				labels[labelKey] = nil
			}
		}

		if len(labels) == 0 {
			continue
		}

		patchData := map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": labels,
			},
		}

		patchBytes, err := json.Marshal(patchData)
		if err != nil {
			return fmt.Errorf("failed to marshal cleanup patch for node %s: %w", nodeName, err)
		}

		node := &v1.Node{}
		node.Name = nodeName
		if err := tv.cl.Patch(ctx, node, client.RawPatch(types.StrategicMergePatchType, patchBytes)); err != nil {
			return fmt.Errorf("failed to cleanup node %s labels: %w", nodeName, err)
		}

		tv.logger.Debugf("Cleaned up labels on node %s", nodeName)
	}
	return nil
}

// GetWorkerNodeNames returns sorted names of all worker nodes matching the e2e worker label selector.
func (tv *TopologyVerifier) GetWorkerNodeNames(ctx context.Context) ([]string, error) {
	var nodeList v1.NodeList
	if err := tv.cl.List(ctx, &nodeList, &client.ListOptions{
		Raw: &metav1.ListOptions{LabelSelector: setup.GetWorkerNodeLabelSelector()},
	}); err != nil {
		return nil, fmt.Errorf("failed to list worker nodes: %w", err)
	}
	names := make([]string, len(nodeList.Items))
	for i, node := range nodeList.Items {
		names[i] = node.Name
	}
	sort.Strings(names)
	return names, nil
}
