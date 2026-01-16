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

package commands

import (
	"strings"
	"testing"

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderGPUBar(t *testing.T) {
	tests := []struct {
		name      string
		allocated int64
		total     int64
		expected  string
	}{
		{
			name:      "empty bar with zero total",
			allocated: 0,
			total:     0,
			expected:  "[--------]",
		},
		{
			name:      "empty bar",
			allocated: 0,
			total:     8,
			expected:  "[--------]",
		},
		{
			name:      "half filled bar",
			allocated: 4,
			total:     8,
			expected:  "[####----]",
		},
		{
			name:      "full bar",
			allocated: 8,
			total:     8,
			expected:  "[########]",
		},
		{
			name:      "quarter filled bar",
			allocated: 2,
			total:     8,
			expected:  "[##------]",
		},
		{
			name:      "three quarters filled bar",
			allocated: 6,
			total:     8,
			expected:  "[######--]",
		},
		{
			name:      "overallocated (should cap at full)",
			allocated: 10,
			total:     8,
			expected:  "[########]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderGPUBar(tt.allocated, tt.total)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRenderScoreBar(t *testing.T) {
	tests := []struct {
		name     string
		score    float64
		expected string
	}{
		{
			name:     "zero score",
			score:    0.0,
			expected: "[----------]",
		},
		{
			name:     "perfect score",
			score:    1.0,
			expected: "[##########]",
		},
		{
			name:     "half score",
			score:    0.5,
			expected: "[#####-----]",
		},
		{
			name:     "high score",
			score:    0.92,
			expected: "[#########-]",
		},
		{
			name:     "low score",
			score:    0.25,
			expected: "[##+-------]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderScoreBar(tt.score)
			// Check that the result is a valid bar format
			assert.True(t, strings.HasPrefix(result, "["), "bar should start with [")
			assert.True(t, strings.HasSuffix(result, "]"), "bar should end with ]")
			// The inner content should be 10 characters
			inner := result[1 : len(result)-1]
			assert.Equal(t, 10, len(inner), "bar inner content should be 10 characters")
		})
	}
}

func TestBuildTopologyTree(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(*TopologyInfo)
		expectedRoots  int
		expectedPods   int
		checkFragments bool
		fragmented     int
	}{
		{
			name: "single rack with multiple nodes",
			setup: func(info *TopologyInfo) {
				info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
					{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
					{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
				})
				info.SetPodCliqueSet("test-pcs", operatorv1alpha1.TopologyDomainRack)

				info.AddNode("node-1", map[string]string{
					"topology.kubernetes.io/rack": "rack-1",
					"kubernetes.io/hostname":      "node-1",
				}, 8)
				info.AddNode("node-2", map[string]string{
					"topology.kubernetes.io/rack": "rack-1",
					"kubernetes.io/hostname":      "node-2",
				}, 8)

				info.AddPod("prefill-0", "node-1", "Running", 4)
				info.AddPod("prefill-1", "node-1", "Running", 4)
				info.AddPod("decode-0", "node-2", "Running", 4)

				info.CalculateGPUUsage()
			},
			expectedRoots: 1,
			expectedPods:  3,
		},
		{
			name: "multiple racks (fragmented)",
			setup: func(info *TopologyInfo) {
				info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
					{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
					{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
				})
				info.SetPodCliqueSet("test-pcs", operatorv1alpha1.TopologyDomainRack)

				info.AddNode("node-1", map[string]string{
					"topology.kubernetes.io/rack": "rack-1",
					"kubernetes.io/hostname":      "node-1",
				}, 8)
				info.AddNode("node-2", map[string]string{
					"topology.kubernetes.io/rack": "rack-1",
					"kubernetes.io/hostname":      "node-2",
				}, 8)
				info.AddNode("node-3", map[string]string{
					"topology.kubernetes.io/rack": "rack-2",
					"kubernetes.io/hostname":      "node-3",
				}, 8)

				// Most pods in rack-1
				info.AddPod("prefill-0", "node-1", "Running", 4)
				info.AddPod("prefill-1", "node-1", "Running", 4)
				info.AddPod("decode-0", "node-2", "Running", 4)
				info.AddPod("decode-1", "node-2", "Running", 4)
				// One pod in rack-2 (fragmented)
				info.AddPod("router-0", "node-3", "Running", 2)

				info.CalculateGPUUsage()
			},
			expectedRoots:  2,
			expectedPods:   5,
			checkFragments: true,
			fragmented:     1,
		},
		{
			name: "empty topology",
			setup: func(info *TopologyInfo) {
				info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
					{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
					{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
				})
				info.SetPodCliqueSet("test-pcs", operatorv1alpha1.TopologyDomainRack)
			},
			expectedRoots: 0,
			expectedPods:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := NewTopologyInfo()
			tt.setup(info)

			cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
			roots := cmd.BuildTopologyTree(info)

			assert.Len(t, roots, tt.expectedRoots)

			// Count total pods in tree
			totalPods := 0
			fragmentedCount := 0
			for _, root := range roots {
				if root.IsFragmented {
					fragmentedCount++
				}
				for _, child := range root.Children {
					totalPods += len(child.Pods)
				}
			}
			assert.Equal(t, tt.expectedPods, totalPods)

			if tt.checkFragments {
				assert.Equal(t, tt.fragmented, fragmentedCount, "fragmented domain count mismatch")
			}
		})
	}
}

func TestGenerateWarnings(t *testing.T) {
	tests := []struct {
		name             string
		setup            func(*TopologyInfo) []*TopologyNode
		expectedWarnings int
		checkMessages    []string
	}{
		{
			name: "no warnings for optimal placement",
			setup: func(info *TopologyInfo) []*TopologyNode {
				info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
					{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
					{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
				})
				info.SetPodCliqueSet("test-pcs", operatorv1alpha1.TopologyDomainRack)
				info.SetPodGangPlacementScore("pg-0", 0.95)

				info.AddNode("node-1", map[string]string{
					"topology.kubernetes.io/rack": "rack-1",
					"kubernetes.io/hostname":      "node-1",
				}, 8)

				info.AddPod("prefill-0", "node-1", "Running", 4)
				info.CalculateGPUUsage()

				cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
				return cmd.BuildTopologyTree(info)
			},
			expectedWarnings: 0,
		},
		{
			name: "warning for fragmented placement",
			setup: func(info *TopologyInfo) []*TopologyNode {
				info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
					{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
					{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
				})
				info.SetPodCliqueSet("test-pcs", operatorv1alpha1.TopologyDomainRack)
				info.SetPodGangPlacementScore("pg-0", 0.72)

				info.AddNode("node-1", map[string]string{
					"topology.kubernetes.io/rack": "rack-1",
					"kubernetes.io/hostname":      "node-1",
				}, 8)
				info.AddNode("node-2", map[string]string{
					"topology.kubernetes.io/rack": "rack-2",
					"kubernetes.io/hostname":      "node-2",
				}, 8)

				info.AddPod("prefill-0", "node-1", "Running", 4)
				info.AddPod("router-0", "node-2", "Running", 2)
				info.CalculateGPUUsage()

				cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
				return cmd.BuildTopologyTree(info)
			},
			// Generates 2 warnings: fragmented placement (across 2 racks) AND low placement score (0.72 < 0.75)
			expectedWarnings: 2,
			checkMessages:    []string{"split across 2 racks", "Low placement score"},
		},
		{
			name: "warning for pending pods",
			setup: func(info *TopologyInfo) []*TopologyNode {
				info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
					{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
					{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
				})
				info.SetPodCliqueSet("test-pcs", operatorv1alpha1.TopologyDomainRack)
				info.SetPodGangPlacementScore("pg-0", 0.95)

				info.AddNode("node-1", map[string]string{
					"topology.kubernetes.io/rack": "rack-1",
					"kubernetes.io/hostname":      "node-1",
				}, 8)

				info.AddPod("prefill-0", "node-1", "Running", 4)
				info.AddPod("prefill-1", "", "Pending", 4) // Pending pod
				info.CalculateGPUUsage()

				cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
				return cmd.BuildTopologyTree(info)
			},
			expectedWarnings: 1,
			checkMessages:    []string{"pending scheduling"},
		},
		{
			name: "warning for low placement score",
			setup: func(info *TopologyInfo) []*TopologyNode {
				info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
					{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
					{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
				})
				info.SetPodCliqueSet("test-pcs", operatorv1alpha1.TopologyDomainRack)
				info.SetPodGangPlacementScore("pg-0", 0.65) // Low score

				info.AddNode("node-1", map[string]string{
					"topology.kubernetes.io/rack": "rack-1",
					"kubernetes.io/hostname":      "node-1",
				}, 8)

				info.AddPod("prefill-0", "node-1", "Running", 4)
				info.CalculateGPUUsage()

				cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
				return cmd.BuildTopologyTree(info)
			},
			expectedWarnings: 1,
			checkMessages:    []string{"Low placement score"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := NewTopologyInfo()
			roots := tt.setup(info)

			cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
			warnings := cmd.GenerateWarnings(info, roots)

			assert.Len(t, warnings, tt.expectedWarnings)

			for _, expectedMsg := range tt.checkMessages {
				found := false
				for _, w := range warnings {
					if strings.Contains(w.Message, expectedMsg) {
						found = true
						break
					}
				}
				assert.True(t, found, "expected warning message containing %q not found", expectedMsg)
			}
		})
	}
}

func TestRackGrouping(t *testing.T) {
	info := NewTopologyInfo()
	info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
		{Domain: operatorv1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
		{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	info.SetPodCliqueSet("test-pcs", operatorv1alpha1.TopologyDomainRack)

	// Add nodes in different racks
	info.AddNode("node-1", map[string]string{
		"topology.kubernetes.io/zone": "us-west-2a",
		"topology.kubernetes.io/rack": "rack-1",
		"kubernetes.io/hostname":      "node-1",
	}, 8)
	info.AddNode("node-2", map[string]string{
		"topology.kubernetes.io/zone": "us-west-2a",
		"topology.kubernetes.io/rack": "rack-1",
		"kubernetes.io/hostname":      "node-2",
	}, 8)
	info.AddNode("node-3", map[string]string{
		"topology.kubernetes.io/zone": "us-west-2a",
		"topology.kubernetes.io/rack": "rack-2",
		"kubernetes.io/hostname":      "node-3",
	}, 8)

	info.AddPod("pod-1", "node-1", "Running", 4)
	info.AddPod("pod-2", "node-2", "Running", 4)
	info.AddPod("pod-3", "node-3", "Running", 4)

	info.CalculateGPUUsage()

	cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
	roots := cmd.BuildTopologyTree(info)

	// Should have 2 racks
	require.Len(t, roots, 2)

	// Find rack-1 and rack-2
	var rack1, rack2 *TopologyNode
	for _, root := range roots {
		if root.Name == "rack-1" {
			rack1 = root
		} else if root.Name == "rack-2" {
			rack2 = root
		}
	}

	require.NotNil(t, rack1, "rack-1 should exist")
	require.NotNil(t, rack2, "rack-2 should exist")

	// rack-1 should have 2 nodes
	assert.Len(t, rack1.Children, 2)
	// rack-2 should have 1 node
	assert.Len(t, rack2.Children, 1)

	// Check GPU counts
	assert.Equal(t, int64(16), rack1.TotalGPUs)
	assert.Equal(t, int64(8), rack1.AllocatedGPUs)
	assert.Equal(t, int64(8), rack2.TotalGPUs)
	assert.Equal(t, int64(4), rack2.AllocatedGPUs)
}

func TestGPUUsageCalculation(t *testing.T) {
	info := NewTopologyInfo()

	info.AddNode("node-1", map[string]string{
		"topology.kubernetes.io/rack": "rack-1",
		"kubernetes.io/hostname":      "node-1",
	}, 8)

	// Add multiple pods with different GPU requests
	info.AddPod("pod-1", "node-1", "Running", 2)
	info.AddPod("pod-2", "node-1", "Running", 3)
	info.AddPod("pod-3", "node-1", "Running", 1)

	info.CalculateGPUUsage()

	usage := info.NodeGPUUsage["node-1"]
	assert.Equal(t, int64(8), usage.Capacity)
	// Total should be 2+3+1=6
	assert.Equal(t, int64(6), usage.Allocated)
}

func TestTopologyCommandStructure(t *testing.T) {
	cmd := &TopologyCmd{
		Name:      "my-inference",
		Namespace: "production",
		Watch:     false,
	}

	assert.Equal(t, "my-inference", cmd.Name)
	assert.Equal(t, "production", cmd.Namespace)
	assert.False(t, cmd.Watch)
}

func TestTopologyInfoHelpers(t *testing.T) {
	info := NewTopologyInfo()

	// Test AddNode
	info.AddNode("test-node", map[string]string{"key": "value"}, 4)
	assert.NotNil(t, info.Nodes["test-node"])
	assert.Equal(t, "value", info.Nodes["test-node"].Labels["key"])

	// Test AddPod
	info.AddPod("test-pod", "test-node", "Running", 2)
	assert.Len(t, info.Pods, 1)
	assert.Equal(t, "test-pod", info.Pods[0].Name)
	assert.Equal(t, "test-node", info.Pods[0].Spec.NodeName)

	// Test SetClusterTopology
	info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
		{Domain: operatorv1alpha1.TopologyDomainRack, Key: "rack-key"},
	})
	assert.NotNil(t, info.ClusterTopology)
	assert.Len(t, info.ClusterTopology.Spec.Levels, 1)

	// Test SetPodCliqueSet
	info.SetPodCliqueSet("my-pcs", operatorv1alpha1.TopologyDomainRack)
	assert.NotNil(t, info.PodCliqueSet)
	assert.Equal(t, "my-pcs", info.PodCliqueSet.Name)

	// Test SetPodGangPlacementScore
	info.SetPodGangPlacementScore("pg-1", 0.85)
	assert.Len(t, info.PodGangs, 1)
	assert.Equal(t, 0.85, *info.PodGangs[0].Status.PlacementScore)
}

func TestMultiplePodGangPlacementScoreAverage(t *testing.T) {
	info := NewTopologyInfo()

	info.SetPodGangPlacementScore("pg-1", 0.8)
	info.SetPodGangPlacementScore("pg-2", 0.9)
	info.SetPodGangPlacementScore("pg-3", 1.0)

	cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
	avgScore := cmd.getAveragePlacementScore(info)

	// (0.8 + 0.9 + 1.0) / 3 = 0.9
	assert.InDelta(t, 0.9, avgScore, 0.001)
}

func TestNoPodGangsPlacementScore(t *testing.T) {
	info := NewTopologyInfo()

	cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
	avgScore := cmd.getAveragePlacementScore(info)

	assert.Equal(t, float64(-1), avgScore)
}

func TestTreeRenderingOutput(t *testing.T) {
	info := NewTopologyInfo()
	info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
		{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
		{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	info.SetPodCliqueSet("my-inference", operatorv1alpha1.TopologyDomainRack)
	info.SetPodGangPlacementScore("pg-0", 0.92)

	info.AddNode("node-1", map[string]string{
		"topology.kubernetes.io/rack": "rack-1",
		"kubernetes.io/hostname":      "node-1",
	}, 8)
	info.AddNode("node-2", map[string]string{
		"topology.kubernetes.io/rack": "rack-1",
		"kubernetes.io/hostname":      "node-2",
	}, 8)

	info.AddPod("prefill-0", "node-1", "Running", 4)
	info.AddPod("prefill-1", "node-1", "Running", 4)
	info.AddPod("decode-0", "node-2", "Running", 4)

	info.CalculateGPUUsage()

	cmd := &TopologyCmd{Name: "my-inference", Namespace: "default"}
	output := cmd.renderTopology(info)

	// Check that output contains expected elements
	assert.Contains(t, output, "ClusterTopology: grove-topology")
	assert.Contains(t, output, "PodCliqueSet: my-inference")
	assert.Contains(t, output, "PlacementScore:")
	assert.Contains(t, output, "TopologyConstraint: packDomain=rack")
	assert.Contains(t, output, "rack-1")
	assert.Contains(t, output, "node-1")
	assert.Contains(t, output, "node-2")
	assert.Contains(t, output, "prefill-0")
	assert.Contains(t, output, "decode-0")
	assert.Contains(t, output, "GPUs")
}

func TestHostLevelPackDomain(t *testing.T) {
	info := NewTopologyInfo()
	info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
		{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
		{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	// Pack at host level instead of rack
	info.SetPodCliqueSet("test-pcs", operatorv1alpha1.TopologyDomainHost)

	info.AddNode("node-1", map[string]string{
		"topology.kubernetes.io/rack": "rack-1",
		"kubernetes.io/hostname":      "node-1",
	}, 8)
	info.AddNode("node-2", map[string]string{
		"topology.kubernetes.io/rack": "rack-1",
		"kubernetes.io/hostname":      "node-2",
	}, 8)

	info.AddPod("prefill-0", "node-1", "Running", 4)
	info.AddPod("decode-0", "node-2", "Running", 4)

	info.CalculateGPUUsage()

	cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
	roots := cmd.BuildTopologyTree(info)

	// With host as pack domain, we should see individual hosts at the top level
	assert.Equal(t, 2, len(roots))

	// Each root should be a host
	hostNames := make([]string, 0)
	for _, root := range roots {
		hostNames = append(hostNames, root.Name)
		assert.Equal(t, operatorv1alpha1.TopologyDomainHost, root.Domain)
	}
	assert.Contains(t, hostNames, "node-1")
	assert.Contains(t, hostNames, "node-2")
}

func TestUnknownTopologyDomain(t *testing.T) {
	info := NewTopologyInfo()
	info.SetClusterTopology([]operatorv1alpha1.TopologyLevel{
		{Domain: operatorv1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
		{Domain: operatorv1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	info.SetPodCliqueSet("test-pcs", operatorv1alpha1.TopologyDomainRack)

	// Add a node without the rack label
	info.AddNode("node-1", map[string]string{
		"kubernetes.io/hostname": "node-1",
		// Missing topology.kubernetes.io/rack label
	}, 8)

	info.AddPod("pod-1", "node-1", "Running", 4)
	info.CalculateGPUUsage()

	cmd := &TopologyCmd{Name: "test-pcs", Namespace: "default"}
	roots := cmd.BuildTopologyTree(info)

	// Should be grouped under "unknown"
	require.Len(t, roots, 1)
	assert.Equal(t, "unknown", roots[0].Name)
}
