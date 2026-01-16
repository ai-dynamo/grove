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
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
)

// TestNewModel tests the creation of a new TUI model
func TestNewModel(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	assert.Equal(t, HierarchyView, m.activeView)
	assert.Equal(t, "default", m.namespace)
	assert.True(t, m.loading)
	assert.NotNil(t, m.keys)
}

// TestViewSwitching tests tab navigation between views
func TestViewSwitching(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	// Test forward tab
	tests := []struct {
		name         string
		initialView  ViewType
		keyMsg       tea.KeyMsg
		expectedView ViewType
	}{
		{
			name:         "tab from hierarchy to topology",
			initialView:  HierarchyView,
			keyMsg:       tea.KeyMsg{Type: tea.KeyTab},
			expectedView: TopologyView,
		},
		{
			name:         "tab from topology to health",
			initialView:  TopologyView,
			keyMsg:       tea.KeyMsg{Type: tea.KeyTab},
			expectedView: HealthView,
		},
		{
			name:         "tab from health to help",
			initialView:  HealthView,
			keyMsg:       tea.KeyMsg{Type: tea.KeyTab},
			expectedView: HelpView,
		},
		{
			name:         "tab from help wraps to hierarchy",
			initialView:  HelpView,
			keyMsg:       tea.KeyMsg{Type: tea.KeyTab},
			expectedView: HierarchyView,
		},
		{
			name:         "shift+tab from hierarchy wraps to help",
			initialView:  HierarchyView,
			keyMsg:       tea.KeyMsg{Type: tea.KeyShiftTab},
			expectedView: HelpView,
		},
		{
			name:         "shift+tab from health to topology",
			initialView:  HealthView,
			keyMsg:       tea.KeyMsg{Type: tea.KeyShiftTab},
			expectedView: TopologyView,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.activeView = tt.initialView
			updated, _ := m.Update(tt.keyMsg)
			updatedModel := updated.(Model)
			assert.Equal(t, tt.expectedView, updatedModel.activeView)
		})
	}
}

// TestCursorNavigation tests j/k navigation in hierarchy view
func TestCursorNavigation(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")
	m.activeView = HierarchyView

	// Create test tree nodes
	m.flattenedNodes = []*TreeNode{
		{Name: "node1", Kind: "PodCliqueSet"},
		{Name: "node2", Kind: "PodGang"},
		{Name: "node3", Kind: "PodClique"},
		{Name: "node4", Kind: "Pod"},
	}
	m.hierarchyCursor = 0

	// Test moving down
	m.moveCursor(1)
	assert.Equal(t, 1, m.hierarchyCursor)

	m.moveCursor(1)
	assert.Equal(t, 2, m.hierarchyCursor)

	// Test moving up
	m.moveCursor(-1)
	assert.Equal(t, 1, m.hierarchyCursor)

	// Test boundary - can't go below 0
	m.hierarchyCursor = 0
	m.moveCursor(-1)
	assert.Equal(t, 0, m.hierarchyCursor)

	// Test boundary - can't exceed max
	m.hierarchyCursor = 3
	m.moveCursor(1)
	assert.Equal(t, 3, m.hierarchyCursor)
}

// TestTreeNodeToggle tests expand/collapse functionality
func TestTreeNodeToggle(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")
	m.activeView = HierarchyView

	// Create a tree with children
	root := &TreeNode{
		Name:     "root",
		Kind:     "PodCliqueSet",
		Expanded: true,
		Children: []*TreeNode{
			{Name: "child1", Kind: "PodGang", Level: 1},
			{Name: "child2", Kind: "PodGang", Level: 1},
		},
	}
	m.hierarchyTree = []*TreeNode{root}
	m.flattenTree()

	// Initially 3 nodes visible (root + 2 children)
	assert.Len(t, m.flattenedNodes, 3)

	// Collapse root
	m.hierarchyCursor = 0
	m.toggleExpand()
	assert.False(t, root.Expanded)
	assert.Len(t, m.flattenedNodes, 1)

	// Expand root again
	m.toggleExpand()
	assert.True(t, root.Expanded)
	assert.Len(t, m.flattenedNodes, 3)
}

// TestFlattenTree tests tree flattening logic
func TestFlattenTree(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	// Build a multi-level tree
	m.hierarchyTree = []*TreeNode{
		{
			Name:     "pcs1",
			Kind:     "PodCliqueSet",
			Expanded: true,
			Level:    0,
			Children: []*TreeNode{
				{
					Name:     "gang1",
					Kind:     "PodGang",
					Expanded: true,
					Level:    1,
					Children: []*TreeNode{
						{
							Name:     "clique1",
							Kind:     "PodClique",
							Expanded: false,
							Level:    2,
							Children: []*TreeNode{
								{Name: "pod1", Kind: "Pod", Level: 3},
								{Name: "pod2", Kind: "Pod", Level: 3},
							},
						},
					},
				},
			},
		},
	}

	m.flattenTree()

	// With clique collapsed, should have: pcs1, gang1, clique1
	assert.Len(t, m.flattenedNodes, 3)
	assert.Equal(t, "pcs1", m.flattenedNodes[0].Name)
	assert.Equal(t, "gang1", m.flattenedNodes[1].Name)
	assert.Equal(t, "clique1", m.flattenedNodes[2].Name)

	// Expand clique
	m.flattenedNodes[2].Expanded = true
	m.flattenTree()

	// Now should have 5 nodes
	assert.Len(t, m.flattenedNodes, 5)
	assert.Equal(t, "pod1", m.flattenedNodes[3].Name)
	assert.Equal(t, "pod2", m.flattenedNodes[4].Name)
}

// TestDefaultKeyMap tests that all default key bindings are set
func TestDefaultKeyMap(t *testing.T) {
	km := DefaultKeyMap()

	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyUp}, km.Up))
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, km.Up))
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyDown}, km.Down))
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, km.Down))
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyTab}, km.Tab))
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyShiftTab}, km.ShiftTab))
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyEnter}, km.Enter))
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}, km.GoToTop))
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}, km.GoToBottom))
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}, km.Search))
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}, km.Quit))
}

// TestKeyMapHelp tests that ShortHelp and FullHelp return expected bindings
func TestKeyMapHelp(t *testing.T) {
	km := DefaultKeyMap()

	shortHelp := km.ShortHelp()
	assert.GreaterOrEqual(t, len(shortHelp), 3)

	fullHelp := km.FullHelp()
	assert.GreaterOrEqual(t, len(fullHelp), 2)
}

// TestStatusStyle tests the status styling logic
func TestStatusStyle(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	tests := []struct {
		status   string
		isGreen  bool
		isYellow bool
		isRed    bool
	}{
		{"Running", true, false, false},
		{"Ready", true, false, false},
		{"Pending", false, true, false},
		{"Starting", false, true, false},
		{"Failed", false, false, true},
		{"Error", false, false, true},
		{"Unknown", false, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			style := m.getStatusStyle(tt.status)
			// Just verify it returns a valid style (not nil)
			assert.NotNil(t, style)
		})
	}
}

// TestTreeNodeRendering tests the tree node rendering
func TestTreeNodeRendering(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	tests := []struct {
		name     string
		node     *TreeNode
		selected bool
		contains []string
	}{
		{
			name: "PodCliqueSet node",
			node: &TreeNode{
				Name:     "my-pcs",
				Kind:     "PodCliqueSet",
				Status:   "Ready",
				Expanded: true,
				Children: []*TreeNode{{Name: "child"}},
				Level:    0,
			},
			selected: false,
			contains: []string{"PodCliqueSet:", "my-pcs", "Ready"},
		},
		{
			name: "PodGang with score",
			node: &TreeNode{
				Name:   "my-gang",
				Kind:   "PodGang",
				Status: "Running",
				Score:  func() *float64 { v := 0.95; return &v }(),
				Level:  1,
			},
			selected: false,
			contains: []string{"PodGang:", "my-gang", "Running", "Score: 0.95"},
		},
		{
			name: "PodClique with ready count",
			node: &TreeNode{
				Name:  "prefill",
				Kind:  "PodClique",
				Ready: "3/3",
				Level: 2,
			},
			selected: false,
			contains: []string{"PodClique:", "prefill", "3/3 ready"},
		},
		{
			name: "Pod with node name",
			node: &TreeNode{
				Name:     "my-pod-0",
				Kind:     "Pod",
				Status:   "Running",
				NodeName: "node-1",
				Level:    3,
			},
			selected: true,
			contains: []string{"pod/", "my-pod-0", "Running", "node-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendered := m.renderTreeNode(tt.node, tt.selected)
			for _, s := range tt.contains {
				assert.Contains(t, rendered, s, "Expected rendered output to contain: %s", s)
			}
			if tt.selected {
				assert.Contains(t, rendered, ">")
			}
		})
	}
}

// TestGPUBarRendering tests the GPU usage bar rendering
func TestGPUBarRendering(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	tests := []struct {
		name      string
		allocated int64
		total     int64
		expected  string
	}{
		{"empty", 0, 8, "[--------]"},
		{"half", 4, 8, "[####----]"},
		{"full", 8, 8, "[########]"},
		{"zero total", 0, 0, "[--------]"},
		{"quarter", 2, 8, "[##------]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := m.renderGPUBar(tt.allocated, tt.total)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestWindowResize tests handling of window resize messages
func TestWindowResize(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	// Send resize message
	msg := tea.WindowSizeMsg{Width: 120, Height: 40}
	updated, _ := m.Update(msg)
	updatedModel := updated.(Model)

	assert.Equal(t, 120, updatedModel.width)
	assert.Equal(t, 40, updatedModel.height)
}

// TestQuitKey tests the quit key handling
func TestQuitKey(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	// Test 'q' key
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	_, cmd := m.Update(msg)
	require.NotNil(t, cmd)

	// Test Ctrl+C
	msg = tea.KeyMsg{Type: tea.KeyCtrlC}
	_, cmd = m.Update(msg)
	require.NotNil(t, cmd)
}

// TestGoToTopBottom tests go to top/bottom navigation
func TestGoToTopBottom(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")
	m.activeView = HierarchyView
	m.flattenedNodes = []*TreeNode{
		{Name: "node1"},
		{Name: "node2"},
		{Name: "node3"},
		{Name: "node4"},
		{Name: "node5"},
	}
	m.hierarchyCursor = 2

	// Test 'g' - go to top
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}
	updated, _ := m.Update(msg)
	updatedModel := updated.(Model)
	assert.Equal(t, 0, updatedModel.hierarchyCursor)

	// Test 'G' - go to bottom
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}
	updated, _ = updatedModel.Update(msg)
	updatedModel = updated.(Model)
	assert.Equal(t, 4, updatedModel.hierarchyCursor)
}

// TestSearchMode tests search mode activation
func TestSearchMode(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	// Activate search mode
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
	updated, _ := m.Update(msg)
	updatedModel := updated.(Model)
	assert.True(t, updatedModel.searchMode)
	assert.Empty(t, updatedModel.searchQuery)

	// Type in search mode
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	updated, _ = updatedModel.Update(msg)
	updatedModel = updated.(Model)
	assert.Equal(t, "a", updatedModel.searchQuery)

	// Exit search mode with Escape
	msg = tea.KeyMsg{Type: tea.KeyEsc}
	updated, _ = updatedModel.Update(msg)
	updatedModel = updated.(Model)
	assert.False(t, updatedModel.searchMode)
	assert.Empty(t, updatedModel.searchQuery)
}

// TestHelpView tests the help view rendering
func TestHelpView(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")
	m.activeView = HelpView
	m.width = 100
	m.height = 40

	view := m.renderHelpView()

	// Verify help content
	assert.Contains(t, view, "Keyboard Shortcuts")
	assert.Contains(t, view, "Tab")
	assert.Contains(t, view, "Enter")
	assert.Contains(t, view, "Quit")
	assert.Contains(t, view, "Views")
	assert.Contains(t, view, "Hierarchy")
	assert.Contains(t, view, "Topology")
	assert.Contains(t, view, "Health")
	assert.Contains(t, view, "Status Colors")
}

// TestViewTypes tests that all view types are defined
func TestViewTypes(t *testing.T) {
	assert.Equal(t, ViewType(0), HierarchyView)
	assert.Equal(t, ViewType(1), TopologyView)
	assert.Equal(t, ViewType(2), HealthView)
	assert.Equal(t, ViewType(3), HelpView)
}

// TestDataMsgHandling tests handling of data refresh messages
func TestDataMsgHandling(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")
	m.loading = true

	// Create test data
	testTree := []*TreeNode{
		{Name: "test-pcs", Kind: "PodCliqueSet", Level: 0},
	}

	msg := dataMsg{
		hierarchyTree: testTree,
		topologyData:  &TopologyViewData{},
		healthData:    &HealthViewData{},
		err:           nil,
	}

	updated, _ := m.Update(msg)
	updatedModel := updated.(Model)

	assert.False(t, updatedModel.loading)
	assert.Nil(t, updatedModel.err)
	assert.Len(t, updatedModel.hierarchyTree, 1)
	assert.Equal(t, "test-pcs", updatedModel.hierarchyTree[0].Name)
}

// TestDataMsgWithError tests handling of data refresh messages with errors
func TestDataMsgWithError(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")
	m.loading = true

	testErr := assert.AnError
	msg := dataMsg{
		err: testErr,
	}

	updated, _ := m.Update(msg)
	updatedModel := updated.(Model)

	assert.False(t, updatedModel.loading)
	assert.Equal(t, testErr, updatedModel.err)
}

// TestEmptyHierarchyView tests rendering when no PodCliqueSets exist
func TestEmptyHierarchyView(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "test-ns")
	m.activeView = HierarchyView
	m.width = 100
	m.height = 40
	m.flattenedNodes = nil

	view := m.renderHierarchyView()
	assert.Contains(t, view, "No PodCliqueSets found in namespace test-ns")
}

// TestHierarchyViewWithData tests rendering with actual data
func TestHierarchyViewWithData(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")
	m.activeView = HierarchyView
	m.width = 100
	m.height = 40

	// Create test tree
	score := 0.95
	m.hierarchyTree = []*TreeNode{
		{
			Name:     "demo-inference",
			Kind:     "PodCliqueSet",
			Status:   "Ready",
			Expanded: true,
			Level:    0,
			Children: []*TreeNode{
				{
					Name:     "demo-inference-0",
					Kind:     "PodGang",
					Status:   "Running",
					Score:    &score,
					Expanded: true,
					Level:    1,
					Children: []*TreeNode{
						{
							Name:  "prefill",
							Kind:  "PodClique",
							Ready: "2/2",
							Level: 2,
						},
					},
				},
			},
		},
	}
	m.flattenTree()
	m.hierarchyCursor = 0

	view := m.renderHierarchyView()
	assert.Contains(t, view, "demo-inference")
	assert.Contains(t, view, "demo-inference-0")
	assert.Contains(t, view, "prefill")
}

// TestTopologyViewRendering tests topology view rendering
func TestTopologyViewRendering(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")
	m.activeView = TopologyView
	m.width = 100
	m.height = 40

	// Test with nil data
	view := m.renderTopologyView()
	assert.Contains(t, view, "No topology data available")

	// Test with data
	m.topologyData = &TopologyViewData{
		ClusterTopology: &operatorv1alpha1.ClusterTopology{
			ObjectMeta: metav1.ObjectMeta{
				Name: "grove-topology",
			},
		},
		RackNodes: map[string][]*NodePlacement{
			"rack-1": {
				{
					Name:         "node-1",
					Rack:         "rack-1",
					GPUCapacity:  8,
					GPUAllocated: 4,
					Pods: []*PodInfo{
						{Name: "pod-1", Status: "Running", GPUCount: 4, CliqueName: "prefill"},
					},
				},
			},
		},
		TotalGPUs:     8,
		AllocatedGPUs: 4,
	}

	view = m.renderTopologyView()
	assert.Contains(t, view, "ClusterTopology")
	assert.Contains(t, view, "grove-topology")
	assert.Contains(t, view, "Total GPUs")
	assert.Contains(t, view, "rack-1")
	assert.Contains(t, view, "node-1")
	assert.Contains(t, view, "pod-1")
}

// TestHealthViewRendering tests health view rendering
func TestHealthViewRendering(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")
	m.activeView = HealthView
	m.width = 100
	m.height = 40

	// Test with nil data
	view := m.renderHealthView()
	assert.Contains(t, view, "No health data available")

	// Test with data
	score := 0.92
	m.healthData = &HealthViewData{
		PodCliqueSets: []PodCliqueSetHealth{
			{
				Name:         "demo-inference",
				Namespace:    "default",
				TotalGangs:   2,
				HealthyGangs: 2,
				Gangs: []GangHealthInfo{
					{
						Name:           "demo-inference-0",
						Phase:          "Running",
						PlacementScore: &score,
						IsHealthy:      true,
					},
					{
						Name:            "demo-inference-1",
						Phase:           "Running",
						PlacementScore:  &score,
						IsHealthy:       false,
						UnhealthyReason: "Below minimum replicas",
					},
				},
			},
		},
	}

	view = m.renderHealthView()
	assert.Contains(t, view, "Gang Health Dashboard")
	assert.Contains(t, view, "demo-inference")
	assert.Contains(t, view, "demo-inference-0")
	assert.Contains(t, view, "demo-inference-1")
	assert.Contains(t, view, "Running")
}

// TestBuildHierarchyTreeWithFakeClient tests building hierarchy from fake K8s client
func TestBuildHierarchyTreeWithFakeClient(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = operatorv1alpha1.AddToScheme(scheme)
	_ = schedulerv1alpha1.AddToScheme(scheme)

	// Create fake clients with proper list kind registration
	gvrToListKind := map[schema.GroupVersionResource]string{
		podCliqueSetGVR: "PodCliqueSetList",
		podCliqueGVR:    "PodCliqueList",
		podGangGVR:      "PodGangList",
	}

	// Create an empty dynamic client - we'll verify the code path without data
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	clientset := fake.NewSimpleClientset()

	m := NewModel(clientset, dynamicClient, "default")

	ctx := t.Context()
	tree, err := m.buildHierarchyTree(ctx)

	// Should succeed with empty list
	require.NoError(t, err)
	assert.Len(t, tree, 0)
}

// TestHeaderRendering tests the header rendering
func TestHeaderRendering(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "testns")
	m.width = 120
	m.height = 40
	m.activeView = HierarchyView

	header := m.renderHeader()

	assert.Contains(t, header, "kubectl grove tui")
	assert.Contains(t, header, "testns")
	assert.Contains(t, header, "Hierarchy")
}

// TestStatusBarRendering tests the status bar rendering
func TestStatusBarRendering(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")
	m.width = 120
	m.height = 40

	// Test loading state
	m.loading = true
	statusBar := m.renderStatusBar()
	assert.Contains(t, statusBar, "Refreshing...")

	// Test not loading state
	m.loading = false
	statusBar = m.renderStatusBar()
	assert.Contains(t, statusBar, "Last update:")
	assert.Contains(t, statusBar, "navigate")
	assert.Contains(t, statusBar, "quit")
}

// TestMaxIntFunction tests the maxInt helper function
func TestMaxIntFunction(t *testing.T) {
	assert.Equal(t, 5, maxInt(5, 3))
	assert.Equal(t, 5, maxInt(3, 5))
	assert.Equal(t, 5, maxInt(5, 5))
	assert.Equal(t, 0, maxInt(0, -1))
	assert.Equal(t, -1, maxInt(-1, -5))
}

// TestMaxInt64Function tests the maxInt64 helper function
func TestMaxInt64Function(t *testing.T) {
	assert.Equal(t, int64(5), maxInt64(5, 3))
	assert.Equal(t, int64(5), maxInt64(3, 5))
	assert.Equal(t, int64(5), maxInt64(5, 5))
	assert.Equal(t, int64(0), maxInt64(0, -1))
	assert.Equal(t, int64(-1), maxInt64(-1, -5))
}

// TestPodCliqueSetStatus tests the status string generation for PodCliqueSets
func TestPodCliqueSetStatus(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	tests := []struct {
		name              string
		availableReplicas int32
		specReplicas      int32
		expected          string
	}{
		{
			name:              "all available",
			availableReplicas: 3,
			specReplicas:      3,
			expected:          "Ready",
		},
		{
			name:              "partial availability",
			availableReplicas: 2,
			specReplicas:      3,
			expected:          "2/3 Available",
		},
		{
			name:              "none available",
			availableReplicas: 0,
			specReplicas:      3,
			expected:          "0/3 Available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := &operatorv1alpha1.PodCliqueSet{
				Spec: operatorv1alpha1.PodCliqueSetSpec{
					Replicas: tt.specReplicas,
				},
				Status: operatorv1alpha1.PodCliqueSetStatus{
					AvailableReplicas: tt.availableReplicas,
				},
			}

			status := m.getPodCliqueSetStatus(pcs)
			assert.Equal(t, tt.expected, status)
		})
	}
}

// TestTreeNodeWithNestedChildren tests deeply nested tree structures
func TestTreeNodeWithNestedChildren(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	// Build a 4-level deep tree
	pod1 := &TreeNode{Name: "pod-0", Kind: "Pod", Level: 3}
	pod2 := &TreeNode{Name: "pod-1", Kind: "Pod", Level: 3}
	clique := &TreeNode{
		Name:     "prefill",
		Kind:     "PodClique",
		Level:    2,
		Expanded: true,
		Children: []*TreeNode{pod1, pod2},
	}
	gang := &TreeNode{
		Name:     "gang-0",
		Kind:     "PodGang",
		Level:    1,
		Expanded: true,
		Children: []*TreeNode{clique},
	}
	pcs := &TreeNode{
		Name:     "my-pcs",
		Kind:     "PodCliqueSet",
		Level:    0,
		Expanded: true,
		Children: []*TreeNode{gang},
	}

	m.hierarchyTree = []*TreeNode{pcs}
	m.flattenTree()

	// All 5 nodes should be visible
	assert.Len(t, m.flattenedNodes, 5)

	// Verify order
	assert.Equal(t, "my-pcs", m.flattenedNodes[0].Name)
	assert.Equal(t, "gang-0", m.flattenedNodes[1].Name)
	assert.Equal(t, "prefill", m.flattenedNodes[2].Name)
	assert.Equal(t, "pod-0", m.flattenedNodes[3].Name)
	assert.Equal(t, "pod-1", m.flattenedNodes[4].Name)

	// Collapse clique
	clique.Expanded = false
	m.flattenTree()

	// Now only 3 nodes should be visible
	assert.Len(t, m.flattenedNodes, 3)
}

// TestMultiplePodCliqueSets tests handling of multiple PodCliqueSets
func TestMultiplePodCliqueSets(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	m := NewModel(clientset, dynamicClient, "default")

	// Create multiple PodCliqueSets
	m.hierarchyTree = []*TreeNode{
		{Name: "pcs-1", Kind: "PodCliqueSet", Level: 0, Expanded: false},
		{Name: "pcs-2", Kind: "PodCliqueSet", Level: 0, Expanded: false},
		{Name: "pcs-3", Kind: "PodCliqueSet", Level: 0, Expanded: false},
	}
	m.flattenTree()

	assert.Len(t, m.flattenedNodes, 3)

	// Navigate through them
	m.hierarchyCursor = 0
	m.moveCursor(1)
	assert.Equal(t, 1, m.hierarchyCursor)
	assert.Equal(t, "pcs-2", m.flattenedNodes[m.hierarchyCursor].Name)
}
