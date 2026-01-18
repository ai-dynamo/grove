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

// Package commands provides CLI commands for kubectl-grove.
package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// ViewState represents the current navigation state in the hierarchy
type ViewState int

const (
	// ViewForest shows all PodCliqueSets
	ViewForest ViewState = iota
	// ViewPodCliqueSet shows PodGangs in a specific PodCliqueSet
	ViewPodCliqueSet
	// ViewPodGang shows PodCliques in a specific PodGang
	ViewPodGang
	// ViewPodClique shows Pods in a specific PodClique
	ViewPodClique
	// ViewPod shows Pod YAML details
	ViewPod
	// ViewTopology shows topology visualization
	ViewTopology
)

// Color constants for tcell in arborist TUI
const (
	arboristColorCyan    = tcell.ColorDarkCyan
	arboristColorMagenta = tcell.ColorDarkMagenta
)

// TUICmd represents the tview-based TUI command
type TUICmd struct {
	Clientset     kubernetes.Interface
	DynamicClient dynamic.Interface
	Namespace     string
}

// ArboristApp is the main TUI application struct
type ArboristApp struct {
	app           *tview.Application
	client        *ArboristClient
	pages         *tview.Pages
	resourceTable *tview.Table
	eventsTable   *tview.Table
	statusBar     *tview.TextView
	breadcrumb    *tview.TextView
	detailView    *tview.TextView
	topologyView  *tview.TextView
	currentView   ViewState
	namespace     string

	// Navigation state
	selectedPCS     string
	selectedPodGang string
	selectedClique  string
	selectedPod     string

	// Data caches
	podCliqueSets []operatorv1alpha1.PodCliqueSet
	podGangs      []schedulerv1alpha1.PodGang
	podCliques    []operatorv1alpha1.PodClique
	pods          []corev1.Pod
	events        []corev1.Event
	nodes         map[string]*corev1.Node

	// Refresh control
	stopRefresh chan struct{}
}

// Run executes the TUI command
func (t *TUICmd) Run() error {
	client := NewArboristClient(t.Clientset, t.DynamicClient, t.Namespace)
	app := newArboristApp(client, t.Namespace)
	return app.run()
}

// newArboristApp creates a new ArboristApp instance
func newArboristApp(client *ArboristClient, namespace string) *ArboristApp {
	return &ArboristApp{
		app:         tview.NewApplication(),
		client:      client,
		namespace:   namespace,
		currentView: ViewForest,
		nodes:       make(map[string]*corev1.Node),
		stopRefresh: make(chan struct{}),
	}
}

// run starts the TUI application
func (a *ArboristApp) run() error {
	// Initialize UI components
	a.setupUI()

	// Start data refresh goroutine
	go a.refreshLoop()

	// Initial data fetch
	a.fetchData()
	a.updateDisplay()

	// Run the application
	err := a.app.Run()

	// Stop refresh goroutine
	close(a.stopRefresh)

	return err
}

// setupUI initializes all UI components
func (a *ArboristApp) setupUI() {
	// Create header
	header := tview.NewTextView().
		SetTextAlign(tview.AlignCenter).
		SetDynamicColors(true).
		SetText("[::b]Arborist[::] | [::b]Grove Operator[::] | [::b]Hierarchical Resource Viewer[::]")
	header.SetBackgroundColor(tcell.ColorDarkBlue)
	header.SetTextColor(tcell.ColorWhite)

	// Create breadcrumb
	a.breadcrumb = tview.NewTextView().
		SetDynamicColors(true).
		SetText("[yellow]> Forest (All PodCliqueSets)[-]")

	// Create resources table (left pane)
	a.resourceTable = tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0)
	a.resourceTable.SetTitle(" Resources ").SetBorder(true)

	// Create events table (right pane)
	a.eventsTable = tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0)
	a.eventsTable.SetTitle(" Events ").SetBorder(true)

	// Create detail view (for Pod YAML)
	a.detailView = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true)
	a.detailView.SetTitle(" Details ").SetBorder(true)

	// Create topology view
	a.topologyView = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true)
	a.topologyView.SetTitle(" Topology ").SetBorder(true)

	// Create status bar
	a.statusBar = tview.NewTextView().
		SetDynamicColors(true).
		SetText("[green]Enter[-]: Drill down | [green]Esc[-]: Go back | [green]Tab[-]: Switch pane | [green]t[-]: Topology | [green]q[-]: Quit")

	// Create main content flex (resources and events side by side)
	contentFlex := tview.NewFlex().
		AddItem(a.resourceTable, 0, 7, true).
		AddItem(a.eventsTable, 0, 3, false)

	// Create pages for switching between views
	a.pages = tview.NewPages().
		AddPage("main", contentFlex, true, true).
		AddPage("detail", a.detailView, true, false).
		AddPage("topology", a.topologyView, true, false)

	// Create main layout
	mainFlex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(a.breadcrumb, 1, 0, false).
		AddItem(a.pages, 0, 1, true).
		AddItem(a.statusBar, 1, 0, false)

	// Set up input handling
	a.setupInputHandlers()

	a.app.SetRoot(mainFlex, true)
}

// setupInputHandlers configures keyboard input handling
func (a *ArboristApp) setupInputHandlers() {
	a.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEscape:
			a.navigateBack()
			return nil
		case tcell.KeyEnter:
			a.drillDown()
			return nil
		case tcell.KeyTab:
			a.switchPane()
			return nil
		case tcell.KeyRune:
			switch event.Rune() {
			case 'q':
				a.app.Stop()
				return nil
			case 't':
				a.showTopology()
				return nil
			}
		}
		return event
	})
}

// refreshLoop periodically refreshes data
func (a *ArboristApp) refreshLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.fetchData()
			a.app.QueueUpdateDraw(func() {
				a.updateDisplay()
			})
		case <-a.stopRefresh:
			return
		}
	}
}

// fetchData retrieves data from Kubernetes
func (a *ArboristApp) fetchData() {
	ctx := context.Background()

	// Fetch PodCliqueSets
	pcsList, err := a.client.dynamicClient.Resource(podCliqueSetGVR).Namespace(a.namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		a.podCliqueSets = nil
		for _, item := range pcsList.Items {
			var pcs operatorv1alpha1.PodCliqueSet
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &pcs); err == nil {
				a.podCliqueSets = append(a.podCliqueSets, pcs)
			}
		}
	}

	// Fetch PodGangs
	gangList, err := a.client.dynamicClient.Resource(podGangGVR).Namespace(a.namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		a.podGangs = nil
		for _, item := range gangList.Items {
			var gang schedulerv1alpha1.PodGang
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &gang); err == nil {
				a.podGangs = append(a.podGangs, gang)
			}
		}
	}

	// Fetch PodCliques
	cliqueList, err := a.client.dynamicClient.Resource(podCliqueGVR).Namespace(a.namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		a.podCliques = nil
		for _, item := range cliqueList.Items {
			var clique operatorv1alpha1.PodClique
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &clique); err == nil {
				a.podCliques = append(a.podCliques, clique)
			}
		}
	}

	// Fetch Pods
	podList, err := a.client.clientset.CoreV1().Pods(a.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/part-of",
	})
	if err == nil {
		a.pods = podList.Items
	}

	// Fetch Events
	eventList, err := a.client.clientset.CoreV1().Events(a.namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		// Sort events by timestamp (most recent first)
		events := eventList.Items
		sort.Slice(events, func(i, j int) bool {
			return events[i].LastTimestamp.After(events[j].LastTimestamp.Time)
		})
		// Keep only the most recent 50 events
		if len(events) > 50 {
			events = events[:50]
		}
		a.events = events
	}

	// Fetch Nodes
	nodeList, err := a.client.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err == nil {
		for i := range nodeList.Items {
			node := &nodeList.Items[i]
			a.nodes[node.Name] = node
		}
	}
}

// updateDisplay refreshes the UI based on current view state
func (a *ArboristApp) updateDisplay() {
	switch a.currentView {
	case ViewForest:
		a.displayForest()
	case ViewPodCliqueSet:
		a.displayPodCliqueSet()
	case ViewPodGang:
		a.displayPodGang()
	case ViewPodClique:
		a.displayPodClique()
	case ViewPod:
		a.displayPod()
	case ViewTopology:
		a.displayTopology()
	}
	a.updateEvents()
	a.updateBreadcrumb()
}

// displayForest shows all PodCliqueSets
func (a *ArboristApp) displayForest() {
	a.resourceTable.Clear()
	a.setTableHeaders([]string{"TYPE", "NAME", "STATUS", "READY"})

	row := 1
	for _, pcs := range a.podCliqueSets {
		status := a.getPCSStatus(&pcs)
		ready := fmt.Sprintf("%d/%d", pcs.Status.AvailableReplicas, pcs.Spec.Replicas)

		a.resourceTable.SetCell(row, 0, a.createCell("PodCliqueSet", tcell.ColorWhite))
		a.resourceTable.SetCell(row, 1, a.createCell(pcs.Name, arboristColorCyan))
		a.resourceTable.SetCell(row, 2, a.createStatusCell(status))
		a.resourceTable.SetCell(row, 3, a.createCell(ready, tcell.ColorWhite))
		row++
	}

	if len(a.podCliqueSets) == 0 {
		a.resourceTable.SetCell(1, 0, a.createCell("No PodCliqueSets found", tcell.ColorGray))
	}
}

// displayPodCliqueSet shows PodGangs in the selected PodCliqueSet
func (a *ArboristApp) displayPodCliqueSet() {
	a.resourceTable.Clear()
	a.setTableHeaders([]string{"TYPE", "NAME", "STATUS", "READY"})

	row := 1
	for _, gang := range a.podGangs {
		// Check if this gang belongs to the selected PCS
		if !strings.HasPrefix(gang.Name, a.selectedPCS+"-") {
			continue
		}

		status := string(gang.Status.Phase)
		if status == "" {
			status = "Unknown"
		}

		ready := "N/A"
		if gang.Status.PlacementScore != nil {
			ready = fmt.Sprintf("Score: %.2f", *gang.Status.PlacementScore)
		}

		a.resourceTable.SetCell(row, 0, a.createCell("PodGang", tcell.ColorWhite))
		a.resourceTable.SetCell(row, 1, a.createCell(gang.Name, arboristColorMagenta))
		a.resourceTable.SetCell(row, 2, a.createStatusCell(status))
		a.resourceTable.SetCell(row, 3, a.createCell(ready, tcell.ColorWhite))
		row++
	}

	if row == 1 {
		a.resourceTable.SetCell(1, 0, a.createCell("No PodGangs found", tcell.ColorGray))
	}
}

// displayPodGang shows PodCliques in the selected PodGang
func (a *ArboristApp) displayPodGang() {
	a.resourceTable.Clear()
	a.setTableHeaders([]string{"TYPE", "NAME", "STATUS", "READY"})

	row := 1
	for _, clique := range a.podCliques {
		// Check if this clique belongs to the selected PodGang
		if !strings.HasPrefix(clique.Name, a.selectedPodGang) {
			continue
		}

		status := "Ready"
		if clique.Status.ReadyReplicas < clique.Spec.Replicas {
			status = "NotReady"
		}

		ready := fmt.Sprintf("%d/%d", clique.Status.ReadyReplicas, clique.Spec.Replicas)

		a.resourceTable.SetCell(row, 0, a.createCell("PodClique", tcell.ColorWhite))
		a.resourceTable.SetCell(row, 1, a.createCell(clique.Spec.RoleName, tcell.ColorYellow))
		a.resourceTable.SetCell(row, 2, a.createStatusCell(status))
		a.resourceTable.SetCell(row, 3, a.createCell(ready, tcell.ColorWhite))

		// Store full name for navigation
		a.resourceTable.GetCell(row, 1).SetReference(clique.Name)
		row++
	}

	if row == 1 {
		a.resourceTable.SetCell(1, 0, a.createCell("No PodCliques found", tcell.ColorGray))
	}
}

// displayPodClique shows Pods in the selected PodClique
func (a *ArboristApp) displayPodClique() {
	a.resourceTable.Clear()
	a.setTableHeaders([]string{"TYPE", "NAME", "STATUS", "READY"})

	row := 1
	for _, pod := range a.pods {
		// Check if this pod belongs to the selected PodClique
		cliqueLabel := pod.Labels["grove.io/podclique"]
		if cliqueLabel != a.selectedClique {
			continue
		}

		status := string(pod.Status.Phase)
		ready := pod.Spec.NodeName
		if ready == "" {
			ready = "Unscheduled"
		}

		a.resourceTable.SetCell(row, 0, a.createCell("Pod", tcell.ColorWhite))
		a.resourceTable.SetCell(row, 1, a.createCell(pod.Name, tcell.ColorGreen))
		a.resourceTable.SetCell(row, 2, a.createStatusCell(status))
		a.resourceTable.SetCell(row, 3, a.createCell(ready, tcell.ColorWhite))
		row++
	}

	if row == 1 {
		a.resourceTable.SetCell(1, 0, a.createCell("No Pods found", tcell.ColorGray))
	}
}

// displayPod shows Pod YAML details
func (a *ArboristApp) displayPod() {
	ctx := context.Background()

	pod, err := a.client.clientset.CoreV1().Pods(a.namespace).Get(ctx, a.selectedPod, metav1.GetOptions{})
	if err != nil {
		a.detailView.SetText(fmt.Sprintf("[red]Error fetching pod: %v[-]", err))
		return
	}

	// Convert to YAML
	yamlBytes, err := yaml.Marshal(pod)
	if err != nil {
		a.detailView.SetText(fmt.Sprintf("[red]Error marshaling pod: %v[-]", err))
		return
	}

	a.detailView.SetText(string(yamlBytes))
	a.pages.SwitchToPage("detail")
}

// displayTopology shows the topology visualization
func (a *ArboristApp) displayTopology() {
	var sb strings.Builder

	sb.WriteString("[::b]Topology View - Rack/Node Layout[::-]\n\n")

	// Group pods by node
	podsByNode := make(map[string][]corev1.Pod)
	for _, pod := range a.pods {
		if pod.Spec.NodeName != "" {
			podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], pod)
		}
	}

	// Group nodes by rack
	nodesByRack := make(map[string][]*corev1.Node)
	for _, node := range a.nodes {
		rack := node.Labels["topology.kubernetes.io/rack"]
		if rack == "" {
			rack = "unknown"
		}
		nodesByRack[rack] = append(nodesByRack[rack], node)
	}

	// Sort racks
	racks := make([]string, 0, len(nodesByRack))
	for rack := range nodesByRack {
		racks = append(racks, rack)
	}
	sort.Strings(racks)

	for _, rack := range racks {
		nodes := nodesByRack[rack]
		sb.WriteString(fmt.Sprintf("[yellow]Rack: %s[-]\n", rack))

		for _, node := range nodes {
			// Get GPU capacity and usage
			gpuCapacity := int64(0)
			if gpuCap, ok := node.Status.Capacity[corev1.ResourceName(GPUResourceName)]; ok {
				gpuCapacity = gpuCap.Value()
			}

			gpuAllocated := int64(0)
			pods := podsByNode[node.Name]
			for i := range pods {
				gpuAllocated += getPodGPUCount(&pods[i])
			}

			// Render GPU bar
			gpuBar := a.renderGPUBar(gpuAllocated, gpuCapacity)

			sb.WriteString(fmt.Sprintf("  [darkcyan]%s[-]: %s (%d/%d GPUs)\n", node.Name, gpuBar, gpuAllocated, gpuCapacity))

			// Show pods on this node
			for i := range pods {
				pod := &pods[i]
				status := string(pod.Status.Phase)
				statusColor := a.getStatusColor(status)

				role := pod.Labels["grove.io/role"]
				rolePrefix := ""
				if role == "prefill" {
					rolePrefix = "[P] "
				} else if role == "decode" {
					rolePrefix = "[D] "
				}

				gpuCount := getPodGPUCount(pod)
				gpuStr := ""
				if gpuCount > 0 {
					gpuStr = fmt.Sprintf(" gpu:%d", gpuCount)
				}

				sb.WriteString(fmt.Sprintf("    - %s%s [%s]%s%s[-]\n", rolePrefix, pod.Name, statusColor, status, gpuStr))
			}
		}
		sb.WriteString("\n")
	}

	a.topologyView.SetText(sb.String())
}

// renderGPUBar renders a GPU usage bar with colors
func (a *ArboristApp) renderGPUBar(allocated, total int64) string {
	if total == 0 {
		return "[gray][--------][-]"
	}

	const barLength = 8
	filled := int(float64(allocated) / float64(total) * barLength)
	if filled > barLength {
		filled = barLength
	}

	utilization := float64(allocated) / float64(total)

	var color string
	switch {
	case utilization >= 0.9:
		color = "red"
	case utilization >= 0.7:
		color = "yellow"
	case utilization >= 0.3:
		color = "green"
	default:
		color = "darkcyan"
	}

	filledPart := strings.Repeat("#", filled)
	emptyPart := strings.Repeat("-", barLength-filled)

	return fmt.Sprintf("[%s][%s%s][-]", color, filledPart, emptyPart)
}

// updateEvents refreshes the events table
func (a *ArboristApp) updateEvents() {
	a.eventsTable.Clear()

	// Set headers
	headers := []string{"TYPE", "REASON", "AGE", "MESSAGE"}
	for i, h := range headers {
		cell := tview.NewTableCell(h).
			SetTextColor(tcell.ColorYellow).
			SetSelectable(false).
			SetExpansion(1)
		a.eventsTable.SetCell(0, i, cell)
	}

	row := 1
	for _, event := range a.events {
		// Filter events based on current view
		if !a.isRelevantEvent(&event) {
			continue
		}

		age := formatAge(time.Since(event.LastTimestamp.Time))

		typeColor := tcell.ColorWhite
		if event.Type == "Warning" {
			typeColor = tcell.ColorYellow
		} else if event.Type == "Normal" {
			typeColor = tcell.ColorGreen
		}

		message := event.Message
		if len(message) > 40 {
			message = message[:37] + "..."
		}

		a.eventsTable.SetCell(row, 0, a.createCell(event.Type, typeColor))
		a.eventsTable.SetCell(row, 1, a.createCell(event.Reason, tcell.ColorWhite))
		a.eventsTable.SetCell(row, 2, a.createCell(age, tcell.ColorGray))
		a.eventsTable.SetCell(row, 3, a.createCell(message, tcell.ColorWhite))
		row++

		if row > 20 {
			break
		}
	}
}

// isRelevantEvent checks if an event is relevant to the current view
func (a *ArboristApp) isRelevantEvent(event *corev1.Event) bool {
	switch a.currentView {
	case ViewForest:
		return event.InvolvedObject.Kind == "PodCliqueSet"
	case ViewPodCliqueSet:
		return (event.InvolvedObject.Kind == "PodGang" &&
			strings.HasPrefix(event.InvolvedObject.Name, a.selectedPCS)) ||
			event.InvolvedObject.Kind == "PodCliqueSet"
	case ViewPodGang:
		return strings.HasPrefix(event.InvolvedObject.Name, a.selectedPodGang)
	case ViewPodClique:
		return strings.HasPrefix(event.InvolvedObject.Name, a.selectedClique)
	case ViewPod:
		return event.InvolvedObject.Name == a.selectedPod
	default:
		return true
	}
}

// updateBreadcrumb updates the breadcrumb navigation display
func (a *ArboristApp) updateBreadcrumb() {
	var parts []string
	parts = append(parts, "[yellow]Forest[-]")

	if a.selectedPCS != "" {
		parts = append(parts, fmt.Sprintf("[darkcyan]%s[-]", a.selectedPCS))
	}
	if a.selectedPodGang != "" {
		parts = append(parts, fmt.Sprintf("[darkmagenta]%s[-]", a.selectedPodGang))
	}
	if a.selectedClique != "" {
		parts = append(parts, fmt.Sprintf("[yellow]%s[-]", a.selectedClique))
	}
	if a.selectedPod != "" {
		parts = append(parts, fmt.Sprintf("[green]%s[-]", a.selectedPod))
	}

	a.breadcrumb.SetText("> " + strings.Join(parts, " > "))
}

// drillDown navigates deeper into the hierarchy
func (a *ArboristApp) drillDown() {
	row, _ := a.resourceTable.GetSelection()
	if row < 1 {
		return
	}

	cell := a.resourceTable.GetCell(row, 1)
	if cell == nil {
		return
	}

	name := cell.Text

	switch a.currentView {
	case ViewForest:
		a.selectedPCS = name
		a.currentView = ViewPodCliqueSet
	case ViewPodCliqueSet:
		a.selectedPodGang = name
		a.currentView = ViewPodGang
	case ViewPodGang:
		// Use reference if available (full clique name)
		if ref := cell.GetReference(); ref != nil {
			if fullName, ok := ref.(string); ok {
				a.selectedClique = fullName
			}
		} else {
			a.selectedClique = name
		}
		a.currentView = ViewPodClique
	case ViewPodClique:
		a.selectedPod = name
		a.currentView = ViewPod
	}

	a.updateDisplay()
}

// navigateBack goes up one level in the hierarchy
func (a *ArboristApp) navigateBack() {
	// First check if we're on a special page
	if page, _ := a.pages.GetFrontPage(); page == "detail" || page == "topology" {
		a.pages.SwitchToPage("main")
		if a.currentView == ViewPod {
			a.currentView = ViewPodClique
		} else if a.currentView == ViewTopology {
			a.currentView = ViewForest
		}
		a.updateDisplay()
		return
	}

	switch a.currentView {
	case ViewPodCliqueSet:
		a.selectedPCS = ""
		a.currentView = ViewForest
	case ViewPodGang:
		a.selectedPodGang = ""
		a.currentView = ViewPodCliqueSet
	case ViewPodClique:
		a.selectedClique = ""
		a.currentView = ViewPodGang
	case ViewPod:
		a.selectedPod = ""
		a.currentView = ViewPodClique
		a.pages.SwitchToPage("main")
	case ViewTopology:
		a.currentView = ViewForest
		a.pages.SwitchToPage("main")
	}

	a.updateDisplay()
}

// switchPane switches focus between resources and events tables
func (a *ArboristApp) switchPane() {
	if a.app.GetFocus() == a.resourceTable {
		a.app.SetFocus(a.eventsTable)
	} else {
		a.app.SetFocus(a.resourceTable)
	}
}

// showTopology switches to the topology view
func (a *ArboristApp) showTopology() {
	a.currentView = ViewTopology
	a.displayTopology()
	a.pages.SwitchToPage("topology")
	a.updateBreadcrumb()
}

// setTableHeaders sets the header row for the resource table
func (a *ArboristApp) setTableHeaders(headers []string) {
	for i, h := range headers {
		cell := tview.NewTableCell(h).
			SetTextColor(tcell.ColorYellow).
			SetSelectable(false).
			SetExpansion(1)
		a.resourceTable.SetCell(0, i, cell)
	}
}

// createCell creates a table cell with the given text and color
func (a *ArboristApp) createCell(text string, color tcell.Color) *tview.TableCell {
	return tview.NewTableCell(text).
		SetTextColor(color).
		SetExpansion(1)
}

// createStatusCell creates a table cell with color based on status
func (a *ArboristApp) createStatusCell(status string) *tview.TableCell {
	color := a.getStatusTcellColor(status)
	return tview.NewTableCell(status).
		SetTextColor(color).
		SetExpansion(1)
}

// getStatusTcellColor returns the tcell color for a status string
func (a *ArboristApp) getStatusTcellColor(status string) tcell.Color {
	switch status {
	case "Running", "Ready", "Healthy":
		return tcell.ColorGreen
	case "Pending", "Starting", "NotReady":
		return tcell.ColorYellow
	case "Failed", "Error", "Unknown":
		return tcell.ColorRed
	default:
		return tcell.ColorWhite
	}
}

// getStatusColor returns the tview color name for a status string
func (a *ArboristApp) getStatusColor(status string) string {
	switch status {
	case "Running", "Ready", "Healthy":
		return "green"
	case "Pending", "Starting", "NotReady":
		return "yellow"
	case "Failed", "Error", "Unknown":
		return "red"
	default:
		return "white"
	}
}

// getPCSStatus returns a status string for a PodCliqueSet
func (a *ArboristApp) getPCSStatus(pcs *operatorv1alpha1.PodCliqueSet) string {
	if pcs.Status.AvailableReplicas == pcs.Spec.Replicas {
		return "Ready"
	}
	if pcs.Status.AvailableReplicas > 0 {
		return "Partial"
	}
	return "NotReady"
}

// formatAge formats a duration as a human-readable age string
func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
