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

// Package commands provides CLI commands for kubectl-grove
package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// TUICmd represents the TUI command
type TUICmd struct {
	Namespace  string
	Kubeconfig string
	Context    string
}

// ViewType represents the different views in the TUI
type ViewType int

const (
	HierarchyView ViewType = iota
	TopologyView
	HealthView
	HelpView
)

// refreshInterval defines the refresh interval for the TUI
const refreshInterval = 2 * time.Second

// Styles for the TUI
var (
	// Colors
	titleColor      = lipgloss.Color("63")
	subtitleColor   = lipgloss.Color("241")
	healthyColor    = lipgloss.Color("82")
	warningColor    = lipgloss.Color("214")
	errorColor      = lipgloss.Color("196")
	selectedColor   = lipgloss.Color("170")
	dimColor        = lipgloss.Color("240")
	accentColor     = lipgloss.Color("86")

	// Styles
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(titleColor).
			MarginBottom(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)

	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(titleColor).
			Padding(0, 2)

	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(subtitleColor).
				Padding(0, 2)

	contentStyle = lipgloss.NewStyle().
			Padding(1, 2)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(dimColor).
			Padding(0, 1)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(1, 2)

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(selectedColor)

	healthyStyle = lipgloss.NewStyle().
			Foreground(healthyColor)

	warningStyle = lipgloss.NewStyle().
			Foreground(warningColor)

	errorStyle = lipgloss.NewStyle().
			Foreground(errorColor)

	dimStyle = lipgloss.NewStyle().
			Foreground(dimColor)
)

// KeyMap defines the key bindings for the TUI
type KeyMap struct {
	Up         key.Binding
	Down       key.Binding
	Tab        key.Binding
	ShiftTab   key.Binding
	Enter      key.Binding
	GoToTop    key.Binding
	GoToBottom key.Binding
	Search     key.Binding
	Quit       key.Binding
	Help       key.Binding
}

// DefaultKeyMap returns the default key bindings
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("j/k", "navigate"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("j/k", "navigate"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next view"),
		),
		ShiftTab: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "prev view"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "expand/collapse"),
		),
		GoToTop: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("g", "go to top"),
		),
		GoToBottom: key.NewBinding(
			key.WithKeys("G"),
			key.WithHelp("G", "go to bottom"),
		),
		Search: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "search"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
	}
}

// ShortHelp returns keybindings to be shown in the mini help view.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Tab, k.Enter, k.Quit, k.Help}
}

// FullHelp returns keybindings for the expanded help view.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.GoToTop, k.GoToBottom},
		{k.Tab, k.ShiftTab, k.Enter, k.Search},
		{k.Quit, k.Help},
	}
}

// TreeNode represents a node in the hierarchy tree
type TreeNode struct {
	Name        string
	Kind        string
	Status      string
	Score       *float64
	Expanded    bool
	Children    []*TreeNode
	Level       int
	NodeName    string // For pods - the node they're running on
	Ready       string // Ready status (e.g., "2/3")
}

// Model is the Bubble Tea model for the TUI
type Model struct {
	// Kubernetes clients
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface
	namespace     string

	// View state
	activeView  ViewType
	width       int
	height      int
	keys        KeyMap
	help        help.Model

	// Hierarchy view state
	hierarchyTree    []*TreeNode
	hierarchyCursor  int
	flattenedNodes   []*TreeNode // Flattened list of visible nodes

	// Topology view state
	topologyData *TopologyViewData

	// Health view state
	healthData *HealthViewData

	// Data refresh
	lastRefresh time.Time
	loading     bool
	err         error

	// Search state
	searchMode  bool
	searchQuery string
}

// TopologyViewData holds topology visualization data
type TopologyViewData struct {
	ClusterTopology *operatorv1alpha1.ClusterTopology
	RackNodes       map[string][]*NodePlacement
	TotalGPUs       int64
	AllocatedGPUs   int64
}

// NodePlacement represents a node's placement info
type NodePlacement struct {
	Name          string
	Rack          string
	GPUCapacity   int64
	GPUAllocated  int64
	Pods          []*PodInfo
}

// PodInfo holds pod information for display
type PodInfo struct {
	Name      string
	Status    string
	GPUCount  int64
	CliqueName string
}

// HealthViewData holds health dashboard data
type HealthViewData struct {
	PodCliqueSets []PodCliqueSetHealth
}

// PodCliqueSetHealth holds health info for a PodCliqueSet
type PodCliqueSetHealth struct {
	Name         string
	Namespace    string
	Gangs        []GangHealthInfo
	TotalGangs   int
	HealthyGangs int
}

// GangHealthInfo holds health info for a PodGang
type GangHealthInfo struct {
	Name            string
	Phase           string
	PlacementScore  *float64
	IsHealthy       bool
	UnhealthyReason string
	CliqueStatuses  []CliqueHealthInfo
}

// CliqueHealthInfo holds health info for a PodClique
type CliqueHealthInfo struct {
	Name          string
	ReadyReplicas int32
	Replicas      int32
	MinAvailable  int32
	IsHealthy     bool
}

// Messages for Bubble Tea
type tickMsg time.Time
type dataMsg struct {
	hierarchyTree []*TreeNode
	topologyData  *TopologyViewData
	healthData    *HealthViewData
	err           error
}

// Run executes the TUI command
func (t *TUICmd) Run() error {
	// Build Kubernetes clients
	clientset, dynamicClient, err := t.buildClients()
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}

	// Initialize the model
	m := NewModel(clientset, dynamicClient, t.Namespace)

	// Create and run the Bubble Tea program
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// buildClients creates Kubernetes clients from kubeconfig
func (t *TUICmd) buildClients() (*kubernetes.Clientset, dynamic.Interface, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if t.Kubeconfig != "" {
		loadingRules.ExplicitPath = t.Kubeconfig
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	if t.Context != "" {
		configOverrides.CurrentContext = t.Context
	}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return clientset, dynamicClient, nil
}

// NewModel creates a new TUI model
func NewModel(clientset kubernetes.Interface, dynamicClient dynamic.Interface, namespace string) Model {
	return Model{
		clientset:     clientset,
		dynamicClient: dynamicClient,
		namespace:     namespace,
		activeView:    HierarchyView,
		keys:          DefaultKeyMap(),
		help:          help.New(),
		loading:       true,
	}
}

// Init implements tea.Model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchData,
		m.tick(),
	)
}

// Update implements tea.Model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Handle search mode
		if m.searchMode {
			switch msg.String() {
			case "esc":
				m.searchMode = false
				m.searchQuery = ""
			case "enter":
				m.searchMode = false
			case "backspace":
				if len(m.searchQuery) > 0 {
					m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
				}
			default:
				if len(msg.String()) == 1 {
					m.searchQuery += msg.String()
				}
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Tab):
			m.activeView = (m.activeView + 1) % 4
		case key.Matches(msg, m.keys.ShiftTab):
			m.activeView = (m.activeView + 3) % 4
		case key.Matches(msg, m.keys.Up):
			m.moveCursor(-1)
		case key.Matches(msg, m.keys.Down):
			m.moveCursor(1)
		case key.Matches(msg, m.keys.Enter):
			m.toggleExpand()
		case key.Matches(msg, m.keys.GoToTop):
			m.hierarchyCursor = 0
		case key.Matches(msg, m.keys.GoToBottom):
			if len(m.flattenedNodes) > 0 {
				m.hierarchyCursor = len(m.flattenedNodes) - 1
			}
		case key.Matches(msg, m.keys.Search):
			m.searchMode = true
			m.searchQuery = ""
		case key.Matches(msg, m.keys.Help):
			m.activeView = HelpView
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.help.Width = msg.Width

	case tickMsg:
		return m, tea.Batch(m.fetchData, m.tick())

	case dataMsg:
		m.loading = false
		m.lastRefresh = time.Now()
		m.err = msg.err
		if msg.err == nil {
			m.hierarchyTree = msg.hierarchyTree
			m.topologyData = msg.topologyData
			m.healthData = msg.healthData
			m.flattenTree()
		}
	}

	return m, nil
}

// tick returns a command that triggers a refresh
func (m Model) tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// fetchData fetches data from the Kubernetes API
func (m Model) fetchData() tea.Msg {
	ctx := context.Background()

	hierarchyTree, err := m.buildHierarchyTree(ctx)
	if err != nil {
		return dataMsg{err: err}
	}

	topologyData, err := m.buildTopologyData(ctx)
	if err != nil {
		// Don't fail completely if topology data fails
		topologyData = nil
	}

	healthData, err := m.buildHealthData(ctx)
	if err != nil {
		// Don't fail completely if health data fails
		healthData = nil
	}

	return dataMsg{
		hierarchyTree: hierarchyTree,
		topologyData:  topologyData,
		healthData:    healthData,
	}
}

// buildHierarchyTree builds the hierarchy tree from Kubernetes resources
func (m Model) buildHierarchyTree(ctx context.Context) ([]*TreeNode, error) {
	var roots []*TreeNode

	// Fetch PodCliqueSets
	pcsList, err := m.dynamicClient.Resource(podCliqueSetGVR).Namespace(m.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list PodCliqueSets: %w", err)
	}

	for _, pcsUnstructured := range pcsList.Items {
		pcs := &operatorv1alpha1.PodCliqueSet{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pcsUnstructured.Object, pcs); err != nil {
			continue
		}

		pcsNode := &TreeNode{
			Name:     pcs.Name,
			Kind:     "PodCliqueSet",
			Status:   m.getPodCliqueSetStatus(pcs),
			Expanded: true,
			Level:    0,
		}

		// Fetch PodGangs for this PodCliqueSet
		gangList, err := m.dynamicClient.Resource(podGangGVR).Namespace(m.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("grove.io/podcliqueset=%s", pcs.Name),
		})
		if err == nil {
			for _, gangUnstructured := range gangList.Items {
				gang := &schedulerv1alpha1.PodGang{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(gangUnstructured.Object, gang); err != nil {
					continue
				}

				gangNode := &TreeNode{
					Name:     gang.Name,
					Kind:     "PodGang",
					Status:   string(gang.Status.Phase),
					Score:    gang.Status.PlacementScore,
					Expanded: true,
					Level:    1,
				}

				// Fetch PodCliques for this PodGang
				cliqueList, err := m.dynamicClient.Resource(podCliqueGVR).Namespace(m.namespace).List(ctx, metav1.ListOptions{
					LabelSelector: fmt.Sprintf("grove.io/podcliqueset=%s", pcs.Name),
				})
				if err == nil {
					for _, cliqueUnstructured := range cliqueList.Items {
						clique := &operatorv1alpha1.PodClique{}
						if err := runtime.DefaultUnstructuredConverter.FromUnstructured(cliqueUnstructured.Object, clique); err != nil {
							continue
						}

						// Check if this clique belongs to this gang
						if !strings.HasPrefix(clique.Name, gang.Name) {
							continue
						}

						cliqueNode := &TreeNode{
							Name:     clique.Spec.RoleName,
							Kind:     "PodClique",
							Ready:    fmt.Sprintf("%d/%d", clique.Status.ReadyReplicas, clique.Spec.Replicas),
							Expanded: false,
							Level:    2,
						}

						// Fetch Pods for this PodClique
						podList, err := m.clientset.CoreV1().Pods(m.namespace).List(ctx, metav1.ListOptions{
							LabelSelector: fmt.Sprintf("grove.io/role=%s,grove.io/podcliqueset=%s", clique.Spec.RoleName, pcs.Name),
						})
						if err == nil {
							for _, pod := range podList.Items {
								podNode := &TreeNode{
									Name:     pod.Name,
									Kind:     "Pod",
									Status:   string(pod.Status.Phase),
									NodeName: pod.Spec.NodeName,
									Level:    3,
								}
								cliqueNode.Children = append(cliqueNode.Children, podNode)
							}
						}

						gangNode.Children = append(gangNode.Children, cliqueNode)
					}
				}

				pcsNode.Children = append(pcsNode.Children, gangNode)
			}
		}

		roots = append(roots, pcsNode)
	}

	return roots, nil
}

// buildTopologyData builds topology visualization data
func (m Model) buildTopologyData(ctx context.Context) (*TopologyViewData, error) {
	data := &TopologyViewData{
		RackNodes: make(map[string][]*NodePlacement),
	}

	// Fetch ClusterTopology
	ctUnstructured, err := m.dynamicClient.Resource(operatorv1alpha1.SchemeGroupVersion.WithResource("clustertopologies")).
		Get(ctx, operatorv1alpha1.DefaultClusterTopologyName, metav1.GetOptions{})
	if err == nil {
		ct := &operatorv1alpha1.ClusterTopology{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(ctUnstructured.Object, ct); err == nil {
			data.ClusterTopology = ct
		}
	}

	// Fetch nodes
	nodeList, err := m.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return data, nil
	}

	// Fetch pods in namespace
	podList, err := m.clientset.CoreV1().Pods(m.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "grove.io/podcliqueset",
	})
	if err != nil {
		return data, nil
	}

	// Build pod map by node
	podsByNode := make(map[string][]corev1.Pod)
	for _, pod := range podList.Items {
		if pod.Spec.NodeName != "" {
			podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], pod)
		}
	}

	// Build node placement info
	for _, node := range nodeList.Items {
		rack := node.Labels["topology.kubernetes.io/rack"]
		if rack == "" {
			rack = "unknown"
		}

		var gpuCapacity int64
		if gpuCap, ok := node.Status.Capacity[corev1.ResourceName(GPUResourceName)]; ok {
			gpuCapacity = gpuCap.Value()
		}

		np := &NodePlacement{
			Name:        node.Name,
			Rack:        rack,
			GPUCapacity: gpuCapacity,
		}

		// Add pods and calculate GPU usage
		for _, pod := range podsByNode[node.Name] {
			gpuCount := getPodGPUCount(&pod)
			np.GPUAllocated += gpuCount

			cliqueName := pod.Labels["grove.io/role"]
			np.Pods = append(np.Pods, &PodInfo{
				Name:       pod.Name,
				Status:     string(pod.Status.Phase),
				GPUCount:   gpuCount,
				CliqueName: cliqueName,
			})
		}

		data.RackNodes[rack] = append(data.RackNodes[rack], np)
		data.TotalGPUs += gpuCapacity
		data.AllocatedGPUs += np.GPUAllocated
	}

	return data, nil
}

// buildHealthData builds health dashboard data
func (m Model) buildHealthData(ctx context.Context) (*HealthViewData, error) {
	data := &HealthViewData{}

	// Fetch PodCliqueSets
	pcsList, err := m.dynamicClient.Resource(podCliqueSetGVR).Namespace(m.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, pcsUnstructured := range pcsList.Items {
		pcs := &operatorv1alpha1.PodCliqueSet{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pcsUnstructured.Object, pcs); err != nil {
			continue
		}

		pcsHealth := PodCliqueSetHealth{
			Name:      pcs.Name,
			Namespace: pcs.Namespace,
		}

		// Fetch PodGangs
		gangList, err := m.dynamicClient.Resource(podGangGVR).Namespace(m.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("grove.io/podcliqueset=%s", pcs.Name),
		})
		if err == nil {
			for _, gangUnstructured := range gangList.Items {
				gang := &schedulerv1alpha1.PodGang{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(gangUnstructured.Object, gang); err != nil {
					continue
				}

				gangHealth := GangHealthInfo{
					Name:           gang.Name,
					Phase:          string(gang.Status.Phase),
					PlacementScore: gang.Status.PlacementScore,
					IsHealthy:      true,
				}

				// Check for Unhealthy condition
				for _, cond := range gang.Status.Conditions {
					if cond.Type == string(schedulerv1alpha1.PodGangConditionTypeUnhealthy) && cond.Status == metav1.ConditionTrue {
						gangHealth.IsHealthy = false
						gangHealth.UnhealthyReason = cond.Message
						break
					}
				}

				pcsHealth.Gangs = append(pcsHealth.Gangs, gangHealth)
				pcsHealth.TotalGangs++
				if gangHealth.IsHealthy {
					pcsHealth.HealthyGangs++
				}
			}
		}

		data.PodCliqueSets = append(data.PodCliqueSets, pcsHealth)
	}

	return data, nil
}

// getPodCliqueSetStatus returns a status string for a PodCliqueSet
func (m Model) getPodCliqueSetStatus(pcs *operatorv1alpha1.PodCliqueSet) string {
	if pcs.Status.AvailableReplicas == pcs.Spec.Replicas {
		return "Ready"
	}
	return fmt.Sprintf("%d/%d Available", pcs.Status.AvailableReplicas, pcs.Spec.Replicas)
}

// flattenTree flattens the hierarchy tree for navigation
func (m *Model) flattenTree() {
	m.flattenedNodes = nil
	for _, root := range m.hierarchyTree {
		m.flattenNode(root)
	}
}

// flattenNode recursively adds nodes to the flattened list
func (m *Model) flattenNode(node *TreeNode) {
	m.flattenedNodes = append(m.flattenedNodes, node)
	if node.Expanded {
		for _, child := range node.Children {
			m.flattenNode(child)
		}
	}
}

// moveCursor moves the cursor up or down
func (m *Model) moveCursor(delta int) {
	if m.activeView != HierarchyView {
		return
	}
	m.hierarchyCursor += delta
	if m.hierarchyCursor < 0 {
		m.hierarchyCursor = 0
	}
	if m.hierarchyCursor >= len(m.flattenedNodes) {
		m.hierarchyCursor = len(m.flattenedNodes) - 1
	}
}

// toggleExpand toggles the expansion state of the current node
func (m *Model) toggleExpand() {
	if m.activeView != HierarchyView {
		return
	}
	if m.hierarchyCursor >= 0 && m.hierarchyCursor < len(m.flattenedNodes) {
		node := m.flattenedNodes[m.hierarchyCursor]
		if len(node.Children) > 0 {
			node.Expanded = !node.Expanded
			m.flattenTree()
		}
	}
}

// View implements tea.Model
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	var content string

	// Build header
	header := m.renderHeader()

	// Build content based on active view
	switch m.activeView {
	case HierarchyView:
		content = m.renderHierarchyView()
	case TopologyView:
		content = m.renderTopologyView()
	case HealthView:
		content = m.renderHealthView()
	case HelpView:
		content = m.renderHelpView()
	}

	// Build status bar
	statusBar := m.renderStatusBar()

	// Combine all sections
	return lipgloss.JoinVertical(lipgloss.Left, header, content, statusBar)
}

// renderHeader renders the header with tabs
func (m Model) renderHeader() string {
	var tabs []string

	viewNames := []string{"Hierarchy", "Topology", "Health", "Help"}
	for i, name := range viewNames {
		if ViewType(i) == m.activeView {
			tabs = append(tabs, activeTabStyle.Render("["+name+"]"))
		} else {
			tabs = append(tabs, inactiveTabStyle.Render(name))
		}
	}

	tabBar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)

	title := titleStyle.Render("kubectl grove tui")
	nsInfo := dimStyle.Render(fmt.Sprintf("ns: %s", m.namespace))

	headerLine := lipgloss.JoinHorizontal(
		lipgloss.Top,
		title,
		strings.Repeat(" ", maxInt(0, m.width-lipgloss.Width(title)-lipgloss.Width(nsInfo)-lipgloss.Width(tabBar)-4)),
		tabBar,
		"  ",
		nsInfo,
	)

	return boxStyle.Width(m.width - 4).Render(headerLine)
}

// renderStatusBar renders the status bar
func (m Model) renderStatusBar() string {
	var refreshInfo string
	if m.loading {
		refreshInfo = "Refreshing..."
	} else {
		ago := time.Since(m.lastRefresh).Round(time.Second)
		refreshInfo = fmt.Sprintf("Last update: %s ago", ago)
	}

	helpText := "j/k: navigate  Tab: switch view  Enter: expand  q: quit  ?: help"

	statusLine := lipgloss.JoinHorizontal(
		lipgloss.Top,
		statusBarStyle.Render(helpText),
		strings.Repeat(" ", maxInt(0, m.width-lipgloss.Width(helpText)-lipgloss.Width(refreshInfo)-4)),
		statusBarStyle.Render(refreshInfo),
	)

	return statusLine
}

// renderHierarchyView renders the hierarchy tree view
func (m Model) renderHierarchyView() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf("Error: %v", m.err))
	}

	if len(m.flattenedNodes) == 0 {
		return dimStyle.Render("No PodCliqueSets found in namespace " + m.namespace)
	}

	var lines []string
	for i, node := range m.flattenedNodes {
		line := m.renderTreeNode(node, i == m.hierarchyCursor)
		lines = append(lines, line)
	}

	content := strings.Join(lines, "\n")
	return contentStyle.Width(m.width - 4).Render(content)
}

// renderTreeNode renders a single tree node
func (m Model) renderTreeNode(node *TreeNode, selected bool) string {
	indent := strings.Repeat("  ", node.Level)

	var prefix string
	if len(node.Children) > 0 {
		if node.Expanded {
			prefix = "[-] "
		} else {
			prefix = "[+] "
		}
	} else {
		prefix = "    "
	}

	// Build the line
	var parts []string
	parts = append(parts, indent+prefix)

	// Kind icon
	switch node.Kind {
	case "PodCliqueSet":
		parts = append(parts, "PodCliqueSet: ")
	case "PodGang":
		parts = append(parts, "PodGang: ")
	case "PodClique":
		parts = append(parts, "PodClique: ")
	case "Pod":
		parts = append(parts, "pod/")
	}

	parts = append(parts, node.Name)

	// Status
	if node.Status != "" {
		statusStyle := m.getStatusStyle(node.Status)
		parts = append(parts, " ["+statusStyle.Render(node.Status)+"]")
	}

	// Ready count
	if node.Ready != "" {
		parts = append(parts, " ("+node.Ready+" ready)")
	}

	// Score
	if node.Score != nil {
		parts = append(parts, fmt.Sprintf(" Score: %.2f", *node.Score))
	}

	// Node name for pods
	if node.NodeName != "" {
		parts = append(parts, dimStyle.Render(" "+node.NodeName))
	}

	line := strings.Join(parts, "")

	if selected {
		return selectedStyle.Render("> " + line)
	}
	return "  " + line
}

// getStatusStyle returns the appropriate style for a status
func (m Model) getStatusStyle(status string) lipgloss.Style {
	switch status {
	case "Running", "Ready":
		return healthyStyle
	case "Pending", "Starting":
		return warningStyle
	case "Failed", "Error", "Unknown":
		return errorStyle
	default:
		return dimStyle
	}
}

// renderTopologyView renders the topology visualization
func (m Model) renderTopologyView() string {
	if m.topologyData == nil {
		return dimStyle.Render("No topology data available")
	}

	var lines []string

	// Header
	if m.topologyData.ClusterTopology != nil {
		lines = append(lines, titleStyle.Render("ClusterTopology: "+m.topologyData.ClusterTopology.Name))
	}

	// GPU summary
	gpuSummary := fmt.Sprintf("Total GPUs: %d | Allocated: %d (%.1f%%)",
		m.topologyData.TotalGPUs,
		m.topologyData.AllocatedGPUs,
		float64(m.topologyData.AllocatedGPUs)/float64(maxInt64(1, m.topologyData.TotalGPUs))*100,
	)
	lines = append(lines, gpuSummary)
	lines = append(lines, "")

	// Rack layout
	racks := make([]string, 0, len(m.topologyData.RackNodes))
	for rack := range m.topologyData.RackNodes {
		racks = append(racks, rack)
	}
	sort.Strings(racks)

	for _, rack := range racks {
		nodes := m.topologyData.RackNodes[rack]
		lines = append(lines, headerStyle.Render(fmt.Sprintf(" Rack: %s ", rack)))

		for _, node := range nodes {
			gpuBar := m.renderGPUBar(node.GPUAllocated, node.GPUCapacity)
			nodeLine := fmt.Sprintf("  %s: %s (%d/%d GPUs)",
				node.Name, gpuBar, node.GPUAllocated, node.GPUCapacity)
			lines = append(lines, nodeLine)

			// Show pods
			for _, pod := range node.Pods {
				statusStyle := m.getStatusStyle(pod.Status)
				podLine := fmt.Sprintf("    - %s [%s] gpu:%d (%s)",
					pod.Name, statusStyle.Render(pod.Status), pod.GPUCount, pod.CliqueName)
				lines = append(lines, podLine)
			}
		}
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")
	return contentStyle.Width(m.width - 4).Render(content)
}

// renderGPUBar renders a GPU usage bar
func (m Model) renderGPUBar(allocated, total int64) string {
	if total == 0 {
		return "[--------]"
	}

	const barLength = 8
	filled := int(float64(allocated) / float64(total) * barLength)
	if filled > barLength {
		filled = barLength
	}

	bar := strings.Repeat("#", filled) + strings.Repeat("-", barLength-filled)
	return "[" + bar + "]"
}

// renderHealthView renders the health dashboard
func (m Model) renderHealthView() string {
	if m.healthData == nil {
		return dimStyle.Render("No health data available")
	}

	var lines []string

	lines = append(lines, titleStyle.Render("Gang Health Dashboard"))
	lines = append(lines, "")

	for _, pcs := range m.healthData.PodCliqueSets {
		// PodCliqueSet header
		healthRatio := fmt.Sprintf("%d/%d healthy", pcs.HealthyGangs, pcs.TotalGangs)
		if pcs.HealthyGangs == pcs.TotalGangs {
			healthRatio = healthyStyle.Render(healthRatio)
		} else {
			healthRatio = warningStyle.Render(healthRatio)
		}
		lines = append(lines, fmt.Sprintf("PodCliqueSet: %s (%s)", pcs.Name, healthRatio))
		lines = append(lines, "")

		for _, gang := range pcs.Gangs {
			// Gang status
			var statusIcon string
			if gang.IsHealthy {
				statusIcon = healthyStyle.Render("[OK]")
			} else {
				statusIcon = errorStyle.Render("[!]")
			}

			gangLine := fmt.Sprintf("  %s %s  Phase: %s", statusIcon, gang.Name, gang.Phase)
			if gang.PlacementScore != nil {
				gangLine += fmt.Sprintf("  Score: %.2f", *gang.PlacementScore)
			}
			lines = append(lines, gangLine)

			if !gang.IsHealthy && gang.UnhealthyReason != "" {
				lines = append(lines, errorStyle.Render(fmt.Sprintf("      Reason: %s", gang.UnhealthyReason)))
			}

			// Clique statuses
			for _, clique := range gang.CliqueStatuses {
				var cliqueStatus string
				if clique.IsHealthy {
					cliqueStatus = healthyStyle.Render("healthy")
				} else {
					cliqueStatus = errorStyle.Render("unhealthy")
				}
				lines = append(lines, fmt.Sprintf("      %s: %d/%d ready (min: %d) [%s]",
					clique.Name, clique.ReadyReplicas, clique.Replicas, clique.MinAvailable, cliqueStatus))
			}
		}
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")
	return contentStyle.Width(m.width - 4).Render(content)
}

// renderHelpView renders the help/keyboard shortcuts view
func (m Model) renderHelpView() string {
	var lines []string

	lines = append(lines, titleStyle.Render("Keyboard Shortcuts"))
	lines = append(lines, "")

	shortcuts := []struct {
		key  string
		desc string
	}{
		{"Tab / Shift+Tab", "Switch between views"},
		{"j / Down", "Move cursor down"},
		{"k / Up", "Move cursor up"},
		{"Enter", "Expand/collapse tree node"},
		{"g", "Go to top"},
		{"G", "Go to bottom"},
		{"/", "Search/filter (in hierarchy view)"},
		{"?", "Show this help"},
		{"q / Ctrl+C", "Quit"},
	}

	accentStyle := lipgloss.NewStyle().Foreground(accentColor)
	for _, s := range shortcuts {
		lines = append(lines, fmt.Sprintf("  %s  %s",
			accentStyle.Render(fmt.Sprintf("%-18s", s.key)),
			s.desc))
	}

	lines = append(lines, "")
	lines = append(lines, titleStyle.Render("Views"))
	lines = append(lines, "")

	views := []struct {
		name string
		desc string
	}{
		{"Hierarchy", "Tree view of PodCliqueSet -> PodGang -> PodClique -> Pod"},
		{"Topology", "Rack/node layout showing GPU allocation and PlacementScore"},
		{"Health", "Gang health dashboard with status indicators"},
		{"Help", "This help screen"},
	}

	for _, v := range views {
		lines = append(lines, fmt.Sprintf("  %s",
			accentStyle.Render(v.name)))
		lines = append(lines, fmt.Sprintf("    %s", dimStyle.Render(v.desc)))
	}

	lines = append(lines, "")
	lines = append(lines, titleStyle.Render("Status Colors"))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("  %s  Healthy/Running", healthyStyle.Render("Green")))
	lines = append(lines, fmt.Sprintf("  %s  Warning/Pending", warningStyle.Render("Yellow")))
	lines = append(lines, fmt.Sprintf("  %s  Error/Failed", errorStyle.Render("Red")))

	content := strings.Join(lines, "\n")
	return contentStyle.Width(m.width - 4).Render(content)
}

// maxInt returns the maximum of two int values
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// maxInt64 returns the maximum of two int64 values
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
