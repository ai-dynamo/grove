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

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// TopologyCmd represents the topology command
type TopologyCmd struct {
	// Name is the name of the PodCliqueSet to show topology for
	Name string `arg:"" required:"" help:"Name of the PodCliqueSet to show topology for."`

	// Namespace is the namespace to look in
	Namespace string `short:"n" help:"Namespace of the PodCliqueSet." default:"default"`

	// Watch enables watch mode for continuous updates
	Watch bool `short:"w" help:"Watch for changes and update the display."`

	// Kubeconfig is the path to the kubeconfig file
	Kubeconfig string `help:"Path to kubeconfig file." env:"KUBECONFIG" placeholder:"FILE"`

	// Context is the Kubernetes context to use
	Context string `help:"Kubernetes context to use." placeholder:"NAME"`
}

// GPUResourceName is the resource name for NVIDIA GPUs
const GPUResourceName = "nvidia.com/gpu"

// TopologyInfo holds all the collected topology information
type TopologyInfo struct {
	// ClusterTopology is the cluster topology resource
	ClusterTopology *operatorv1alpha1.ClusterTopology
	// PodCliqueSet is the PodCliqueSet resource
	PodCliqueSet *operatorv1alpha1.PodCliqueSet
	// PodGangs is the list of PodGang resources for this PodCliqueSet
	PodGangs []schedulerv1alpha1.PodGang
	// Pods is the list of pods belonging to this PodCliqueSet
	Pods []corev1.Pod
	// Nodes is the map of node name to node
	Nodes map[string]*corev1.Node
	// NodeGPUUsage tracks GPU usage per node
	NodeGPUUsage map[string]GPUUsage
}

// GPUUsage tracks GPU allocation on a node
type GPUUsage struct {
	// Capacity is the total GPU capacity on the node
	Capacity int64
	// Allocated is the number of GPUs allocated
	Allocated int64
}

// PodPlacement holds pod placement information
type PodPlacement struct {
	// Pod is the pod
	Pod *corev1.Pod
	// Node is the node the pod is placed on
	Node *corev1.Node
	// GPUCount is the number of GPUs requested by this pod
	GPUCount int64
}

// TopologyNode represents a node in the topology tree
type TopologyNode struct {
	// Name is the name of this topology domain value
	Name string
	// Domain is the topology domain (e.g., rack, host)
	Domain operatorv1alpha1.TopologyDomain
	// Children are child topology nodes
	Children []*TopologyNode
	// Pods are pods placed at this topology level (only for host level)
	Pods []*PodPlacement
	// TotalGPUs is the total GPUs in this domain
	TotalGPUs int64
	// AllocatedGPUs is the allocated GPUs in this domain
	AllocatedGPUs int64
	// IsFragmented indicates if placement is suboptimal
	IsFragmented bool
}

// Warning represents a placement warning
type Warning struct {
	// Message is the warning message
	Message string
	// Recommendation is the recommended action
	Recommendation string
}

// Run executes the topology command
func (t *TopologyCmd) Run() error {
	ctx := context.Background()

	// Build Kubernetes clients
	clientset, dynamicClient, err := t.buildClients()
	if err != nil {
		return fmt.Errorf("failed to build Kubernetes clients: %w", err)
	}

	// Collect topology information
	info, err := t.collectTopologyInfo(ctx, clientset, dynamicClient)
	if err != nil {
		return fmt.Errorf("failed to collect topology information: %w", err)
	}

	// Build and render topology tree
	output := t.renderTopology(info)
	fmt.Print(output)

	return nil
}

// buildClients creates Kubernetes clients from kubeconfig
func (t *TopologyCmd) buildClients() (*kubernetes.Clientset, dynamic.Interface, error) {
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

// collectTopologyInfo gathers all necessary topology information from the cluster
func (t *TopologyCmd) collectTopologyInfo(ctx context.Context, clientset *kubernetes.Clientset, dynamicClient dynamic.Interface) (*TopologyInfo, error) {
	info := &TopologyInfo{
		Nodes:        make(map[string]*corev1.Node),
		NodeGPUUsage: make(map[string]GPUUsage),
	}

	// Fetch ClusterTopology
	clusterTopology, err := t.fetchClusterTopology(ctx, dynamicClient)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ClusterTopology: %w", err)
	}
	info.ClusterTopology = clusterTopology

	// Fetch PodCliqueSet
	podCliqueSet, err := t.fetchPodCliqueSet(ctx, dynamicClient)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch PodCliqueSet: %w", err)
	}
	info.PodCliqueSet = podCliqueSet

	// Fetch PodGangs for this PodCliqueSet
	podGangs, err := t.fetchPodGangs(ctx, dynamicClient)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch PodGangs: %w", err)
	}
	info.PodGangs = podGangs

	// Fetch Pods for this PodCliqueSet
	pods, err := t.fetchPods(ctx, clientset)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Pods: %w", err)
	}
	info.Pods = pods

	// Get unique node names from pods
	nodeNames := make(map[string]struct{})
	for _, pod := range pods {
		if pod.Spec.NodeName != "" {
			nodeNames[pod.Spec.NodeName] = struct{}{}
		}
	}

	// Fetch nodes
	for nodeName := range nodeNames {
		node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to fetch node %s: %w", nodeName, err)
		}
		info.Nodes[nodeName] = node
	}

	// Calculate GPU usage per node
	info.NodeGPUUsage = t.calculateNodeGPUUsage(info)

	return info, nil
}

// fetchClusterTopology fetches the ClusterTopology resource
func (t *TopologyCmd) fetchClusterTopology(ctx context.Context, dynamicClient dynamic.Interface) (*operatorv1alpha1.ClusterTopology, error) {
	gvr := operatorv1alpha1.SchemeGroupVersion.WithResource("clustertopologies")

	unstructured, err := dynamicClient.Resource(gvr).Get(ctx, operatorv1alpha1.DefaultClusterTopologyName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	ct := &operatorv1alpha1.ClusterTopology{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured.Object, ct); err != nil {
		return nil, fmt.Errorf("failed to convert ClusterTopology: %w", err)
	}

	return ct, nil
}

// fetchPodCliqueSet fetches the PodCliqueSet resource
func (t *TopologyCmd) fetchPodCliqueSet(ctx context.Context, dynamicClient dynamic.Interface) (*operatorv1alpha1.PodCliqueSet, error) {
	gvr := operatorv1alpha1.SchemeGroupVersion.WithResource("podcliquesets")

	unstructured, err := dynamicClient.Resource(gvr).Namespace(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	pcs := &operatorv1alpha1.PodCliqueSet{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured.Object, pcs); err != nil {
		return nil, fmt.Errorf("failed to convert PodCliqueSet: %w", err)
	}

	return pcs, nil
}

// fetchPodGangs fetches PodGang resources for this PodCliqueSet
func (t *TopologyCmd) fetchPodGangs(ctx context.Context, dynamicClient dynamic.Interface) ([]schedulerv1alpha1.PodGang, error) {
	gvr := schedulerv1alpha1.SchemeGroupVersion.WithResource("podgangs")

	list, err := dynamicClient.Resource(gvr).Namespace(t.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("grove.io/podcliqueset-name=%s", t.Name),
	})
	if err != nil {
		return nil, err
	}

	var podGangs []schedulerv1alpha1.PodGang
	for _, item := range list.Items {
		pg := schedulerv1alpha1.PodGang{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &pg); err != nil {
			return nil, fmt.Errorf("failed to convert PodGang: %w", err)
		}
		podGangs = append(podGangs, pg)
	}

	return podGangs, nil
}

// fetchPods fetches pods for this PodCliqueSet
func (t *TopologyCmd) fetchPods(ctx context.Context, clientset *kubernetes.Clientset) ([]corev1.Pod, error) {
	list, err := clientset.CoreV1().Pods(t.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("grove.io/podcliqueset-name=%s", t.Name),
	})
	if err != nil {
		return nil, err
	}

	return list.Items, nil
}

// calculateNodeGPUUsage calculates GPU usage per node
func (t *TopologyCmd) calculateNodeGPUUsage(info *TopologyInfo) map[string]GPUUsage {
	usage := make(map[string]GPUUsage)

	// Initialize with node capacities
	for nodeName, node := range info.Nodes {
		capacity := int64(0)
		if gpuCap, ok := node.Status.Capacity[corev1.ResourceName(GPUResourceName)]; ok {
			capacity = gpuCap.Value()
		}
		usage[nodeName] = GPUUsage{
			Capacity:  capacity,
			Allocated: 0,
		}
	}

	// Sum up GPU requests from pods
	for _, pod := range info.Pods {
		if pod.Spec.NodeName == "" {
			continue
		}

		gpuCount := getPodGPUCount(&pod)
		if gpuCount > 0 {
			if u, ok := usage[pod.Spec.NodeName]; ok {
				u.Allocated += gpuCount
				usage[pod.Spec.NodeName] = u
			}
		}
	}

	return usage
}

// getPodGPUCount returns the total GPU count requested by a pod
func getPodGPUCount(pod *corev1.Pod) int64 {
	var total int64
	for _, container := range pod.Spec.Containers {
		if gpuReq, ok := container.Resources.Requests[corev1.ResourceName(GPUResourceName)]; ok {
			total += gpuReq.Value()
		}
		if gpuLimit, ok := container.Resources.Limits[corev1.ResourceName(GPUResourceName)]; ok {
			// Use limit if request is not set
			if _, hasReq := container.Resources.Requests[corev1.ResourceName(GPUResourceName)]; !hasReq {
				total += gpuLimit.Value()
			}
		}
	}
	return total
}

// renderTopology renders the topology visualization
func (t *TopologyCmd) renderTopology(info *TopologyInfo) string {
	var sb strings.Builder

	// Render header box
	sb.WriteString(t.renderHeader(info))
	sb.WriteString("\n")

	// Render PodCliqueSet info with placement score
	sb.WriteString(t.renderPodCliqueSetInfo(info))
	sb.WriteString("\n")

	// Build topology tree
	tree := t.buildTopologyTree(info)

	// Render topology tree
	sb.WriteString(t.renderTopologyTree(tree, info))

	// Render warnings
	warnings := t.generateWarnings(info, tree)
	if len(warnings) > 0 {
		sb.WriteString("\n")
		for _, w := range warnings {
			sb.WriteString(fmt.Sprintf("!! Warning: %s\n", w.Message))
			if w.Recommendation != "" {
				sb.WriteString(fmt.Sprintf("   Recommendation: %s\n", w.Recommendation))
			}
		}
	}

	return sb.String()
}

// renderHeader renders the header box showing ClusterTopology info
func (t *TopologyCmd) renderHeader(info *TopologyInfo) string {
	var sb strings.Builder

	topologyName := "grove-topology"
	if info.ClusterTopology != nil {
		topologyName = info.ClusterTopology.Name
	}

	// Build hierarchy string from topology levels
	hierarchyParts := make([]string, 0)
	if info.ClusterTopology != nil {
		for _, level := range info.ClusterTopology.Spec.Levels {
			hierarchyParts = append(hierarchyParts, string(level.Domain))
		}
	}
	hierarchy := strings.Join(hierarchyParts, " -> ")

	title := fmt.Sprintf(" ClusterTopology: %s ", topologyName)
	contentLine := fmt.Sprintf("| Hierarchy: %-60s |", hierarchy)

	// Calculate box width
	width := len(contentLine)
	if len(title)+4 > width {
		width = len(title) + 4
	}

	// Top border
	sb.WriteString("+")
	sb.WriteString(strings.Repeat("-", len(title)))
	sb.WriteString(strings.Repeat("-", width-len(title)-2))
	sb.WriteString("+\n")

	// Title
	sb.WriteString("|")
	sb.WriteString(title)
	sb.WriteString(strings.Repeat(" ", width-len(title)-2))
	sb.WriteString("|\n")

	// Separator
	sb.WriteString("|")
	sb.WriteString(strings.Repeat("-", width-2))
	sb.WriteString("|\n")

	// Hierarchy content
	if hierarchy != "" {
		hierarchyLine := fmt.Sprintf("| Hierarchy: %s", hierarchy)
		sb.WriteString(hierarchyLine)
		sb.WriteString(strings.Repeat(" ", width-len(hierarchyLine)-1))
		sb.WriteString("|\n")
	}

	// Bottom border
	sb.WriteString("+")
	sb.WriteString(strings.Repeat("-", width-2))
	sb.WriteString("+\n")

	return sb.String()
}

// renderPodCliqueSetInfo renders PodCliqueSet info with placement score
func (t *TopologyCmd) renderPodCliqueSetInfo(info *TopologyInfo) string {
	var sb strings.Builder

	pcsName := t.Name
	if info.PodCliqueSet != nil {
		pcsName = info.PodCliqueSet.Name
	}

	// Get placement score from PodGangs
	placementScore := t.getAveragePlacementScore(info)

	// Get topology constraint
	packDomain := ""
	if info.PodCliqueSet != nil && info.PodCliqueSet.Spec.Template.TopologyConstraint != nil {
		packDomain = string(info.PodCliqueSet.Spec.Template.TopologyConstraint.PackDomain)
	}

	sb.WriteString(fmt.Sprintf("PodCliqueSet: %s", pcsName))
	if placementScore >= 0 {
		sb.WriteString(fmt.Sprintf("    PlacementScore: %.2f %s", placementScore, renderScoreBar(placementScore)))
	}
	sb.WriteString("\n")

	if packDomain != "" {
		sb.WriteString(fmt.Sprintf("\nTopologyConstraint: packDomain=%s\n", packDomain))
	}

	return sb.String()
}

// getAveragePlacementScore returns the average placement score across all PodGangs
func (t *TopologyCmd) getAveragePlacementScore(info *TopologyInfo) float64 {
	if len(info.PodGangs) == 0 {
		return -1
	}

	var total float64
	var count int
	for _, pg := range info.PodGangs {
		if pg.Status.PlacementScore != nil {
			total += *pg.Status.PlacementScore
			count++
		}
	}

	if count == 0 {
		return -1
	}

	return total / float64(count)
}

// renderScoreBar renders a visual bar for the placement score
func renderScoreBar(score float64) string {
	const barLength = 10
	filled := int(score * barLength)
	if filled > barLength {
		filled = barLength
	}
	if filled < 0 {
		filled = 0
	}

	bar := strings.Repeat("#", filled)

	// Add partial fill for fractional part
	remaining := int((score*barLength - float64(filled)) * 10)
	if remaining >= 5 && filled < barLength {
		bar += "+"
		filled++
	}

	bar += strings.Repeat("-", barLength-filled)

	return "[" + bar + "]"
}

// buildTopologyTree builds a tree structure from topology information
func (t *TopologyCmd) buildTopologyTree(info *TopologyInfo) []*TopologyNode {
	if info.ClusterTopology == nil || len(info.ClusterTopology.Spec.Levels) == 0 {
		return nil
	}

	// Find the pack domain to group by
	packDomain := operatorv1alpha1.TopologyDomainRack // default
	if info.PodCliqueSet != nil && info.PodCliqueSet.Spec.Template.TopologyConstraint != nil {
		packDomain = info.PodCliqueSet.Spec.Template.TopologyConstraint.PackDomain
	}

	// Find the topology key for the pack domain
	packDomainKey := ""
	hostKey := ""
	for _, level := range info.ClusterTopology.Spec.Levels {
		if level.Domain == packDomain {
			packDomainKey = level.Key
		}
		if level.Domain == operatorv1alpha1.TopologyDomainHost {
			hostKey = level.Key
		}
	}

	if packDomainKey == "" {
		// Default to rack if not found
		packDomainKey = "topology.kubernetes.io/rack"
	}
	if hostKey == "" {
		hostKey = "kubernetes.io/hostname"
	}

	// Group nodes by pack domain
	domainNodes := make(map[string][]*corev1.Node)
	for _, node := range info.Nodes {
		domainValue := node.Labels[packDomainKey]
		if domainValue == "" {
			domainValue = "unknown"
		}
		domainNodes[domainValue] = append(domainNodes[domainValue], node)
	}

	// Build pod placements
	podPlacements := make(map[string][]*PodPlacement)
	for i := range info.Pods {
		pod := &info.Pods[i]
		if pod.Spec.NodeName == "" {
			continue
		}
		node := info.Nodes[pod.Spec.NodeName]
		placement := &PodPlacement{
			Pod:      pod,
			Node:     node,
			GPUCount: getPodGPUCount(pod),
		}
		podPlacements[pod.Spec.NodeName] = append(podPlacements[pod.Spec.NodeName], placement)
	}

	// Build tree
	var roots []*TopologyNode
	domainNames := make([]string, 0, len(domainNodes))
	for name := range domainNodes {
		domainNames = append(domainNames, name)
	}
	sort.Strings(domainNames)

	for _, domainName := range domainNames {
		nodes := domainNodes[domainName]
		domainNode := &TopologyNode{
			Name:     domainName,
			Domain:   packDomain,
			Children: make([]*TopologyNode, 0),
		}

		// Sort nodes by name
		sort.Slice(nodes, func(i, j int) bool {
			return nodes[i].Name < nodes[j].Name
		})

		for _, node := range nodes {
			hostNode := &TopologyNode{
				Name:   node.Name,
				Domain: operatorv1alpha1.TopologyDomainHost,
				Pods:   podPlacements[node.Name],
			}

			// Calculate GPU usage for this host
			if usage, ok := info.NodeGPUUsage[node.Name]; ok {
				hostNode.TotalGPUs = usage.Capacity
				hostNode.AllocatedGPUs = usage.Allocated
			}

			domainNode.Children = append(domainNode.Children, hostNode)
			domainNode.TotalGPUs += hostNode.TotalGPUs
			domainNode.AllocatedGPUs += hostNode.AllocatedGPUs
		}

		roots = append(roots, domainNode)
	}

	// Mark fragmentation
	t.markFragmentation(roots, info)

	return roots
}

// markFragmentation marks domains with suboptimal pod placement
func (t *TopologyCmd) markFragmentation(roots []*TopologyNode, info *TopologyInfo) {
	if len(roots) <= 1 {
		return
	}

	// Find domain with most allocated GPUs
	maxAllocated := int64(0)
	for _, root := range roots {
		if root.AllocatedGPUs > maxAllocated {
			maxAllocated = root.AllocatedGPUs
		}
	}

	// Mark domains with few allocations as fragmented
	for _, root := range roots {
		if root.AllocatedGPUs > 0 && root.AllocatedGPUs < maxAllocated/2 {
			root.IsFragmented = true
		}
	}
}

// renderTopologyTree renders the topology tree
func (t *TopologyCmd) renderTopologyTree(roots []*TopologyNode, info *TopologyInfo) string {
	var sb strings.Builder

	for i, root := range roots {
		isLast := i == len(roots)-1

		// Render domain header
		status := "[optimal]"
		if root.IsFragmented {
			status = "[fragmented] !!"
		}
		sb.WriteString(fmt.Sprintf("\n%s %s  %d GPUs allocated\n",
			root.Name, status, root.AllocatedGPUs))

		// Render children (hosts)
		for j, child := range root.Children {
			isLastChild := j == len(root.Children)-1
			prefix := "|-- "
			if isLastChild {
				prefix = "\\-- "
			}

			// GPU bar
			gpuBar := renderGPUBar(child.AllocatedGPUs, child.TotalGPUs)

			sb.WriteString(fmt.Sprintf("%s%s: %s (%d/%d GPUs)\n",
				prefix, child.Name, gpuBar, child.AllocatedGPUs, child.TotalGPUs))

			// Render pods
			childPrefix := "|   "
			if isLastChild {
				childPrefix = "    "
			}

			for k, placement := range child.Pods {
				isLastPod := k == len(child.Pods)-1
				podPrefix := "|-- "
				if isLastPod {
					podPrefix = "\\-- "
				}

				podName := placement.Pod.Name
				podStatus := string(placement.Pod.Status.Phase)

				// Get GPU info
				gpuInfo := ""
				if placement.GPUCount > 0 {
					gpuInfo = fmt.Sprintf("  gpu:%d", placement.GPUCount)
				}

				sb.WriteString(fmt.Sprintf("%s%s%s  %s%s\n",
					childPrefix, podPrefix, podName, podStatus, gpuInfo))
			}
		}

		if !isLast {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// renderGPUBar renders a GPU usage bar
func renderGPUBar(allocated, total int64) string {
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

// generateWarnings generates warnings for suboptimal placements
func (t *TopologyCmd) generateWarnings(info *TopologyInfo, roots []*TopologyNode) []Warning {
	var warnings []Warning

	// Check for fragmentation across topology domains
	fragmentedDomains := 0
	optimalDomain := ""
	maxGPUs := int64(0)

	for _, root := range roots {
		if root.AllocatedGPUs > 0 {
			fragmentedDomains++
			if root.AllocatedGPUs > maxGPUs {
				maxGPUs = root.AllocatedGPUs
				optimalDomain = root.Name
			}
		}
	}

	if fragmentedDomains > 1 {
		// Calculate actual placement score if available
		placementScore := t.getAveragePlacementScore(info)
		scoreStr := ""
		if placementScore >= 0 {
			scoreStr = fmt.Sprintf(" PlacementScore: %.2f", placementScore)
		}

		packDomain := "rack"
		if info.PodCliqueSet != nil && info.PodCliqueSet.Spec.Template.TopologyConstraint != nil {
			packDomain = string(info.PodCliqueSet.Spec.Template.TopologyConstraint.PackDomain)
		}

		warnings = append(warnings, Warning{
			Message:        fmt.Sprintf("Pods split across %d %ss.%s", fragmentedDomains, packDomain, scoreStr),
			Recommendation: fmt.Sprintf("Consolidate to %s for optimal NVLink connectivity", optimalDomain),
		})
	}

	// Check for pods pending scheduling
	pendingPods := 0
	for _, pod := range info.Pods {
		if pod.Status.Phase == corev1.PodPending {
			pendingPods++
		}
	}

	if pendingPods > 0 {
		warnings = append(warnings, Warning{
			Message:        fmt.Sprintf("%d pods pending scheduling", pendingPods),
			Recommendation: "Check cluster capacity and scheduling constraints",
		})
	}

	// Check for low placement score
	placementScore := t.getAveragePlacementScore(info)
	if placementScore >= 0 && placementScore < 0.8 {
		warnings = append(warnings, Warning{
			Message:        fmt.Sprintf("Low placement score (%.2f) indicates suboptimal pod placement", placementScore),
			Recommendation: "Consider rescheduling pods to improve network locality",
		})
	}

	return warnings
}

// RenderGPUBar is exported for testing
func RenderGPUBar(allocated, total int64) string {
	return renderGPUBar(allocated, total)
}

// RenderScoreBar is exported for testing
func RenderScoreBar(score float64) string {
	return renderScoreBar(score)
}

// BuildTopologyTree is exported for testing
func (t *TopologyCmd) BuildTopologyTree(info *TopologyInfo) []*TopologyNode {
	return t.buildTopologyTree(info)
}

// GenerateWarnings is exported for testing
func (t *TopologyCmd) GenerateWarnings(info *TopologyInfo, roots []*TopologyNode) []Warning {
	return t.generateWarnings(info, roots)
}

// NewTopologyInfo creates a new TopologyInfo for testing
func NewTopologyInfo() *TopologyInfo {
	return &TopologyInfo{
		Nodes:        make(map[string]*corev1.Node),
		NodeGPUUsage: make(map[string]GPUUsage),
	}
}

// AddNode adds a node for testing
func (info *TopologyInfo) AddNode(name string, labels map[string]string, gpuCapacity int64) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{},
		},
	}
	if gpuCapacity > 0 {
		node.Status.Capacity[corev1.ResourceName(GPUResourceName)] = *resource.NewQuantity(gpuCapacity, resource.DecimalSI)
	}
	info.Nodes[name] = node
}

// AddPod adds a pod for testing
func (info *TopologyInfo) AddPod(name, nodeName, status string, gpuCount int64) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Name: "main",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPhase(status),
		},
	}
	if gpuCount > 0 {
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceName(GPUResourceName)] = *resource.NewQuantity(gpuCount, resource.DecimalSI)
	}
	info.Pods = append(info.Pods, pod)
}

// SetClusterTopology sets the cluster topology for testing
func (info *TopologyInfo) SetClusterTopology(levels []operatorv1alpha1.TopologyLevel) {
	info.ClusterTopology = &operatorv1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: "grove-topology",
		},
		Spec: operatorv1alpha1.ClusterTopologySpec{
			Levels: levels,
		},
	}
}

// SetPodCliqueSet sets the PodCliqueSet for testing
func (info *TopologyInfo) SetPodCliqueSet(name string, packDomain operatorv1alpha1.TopologyDomain) {
	info.PodCliqueSet = &operatorv1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: operatorv1alpha1.PodCliqueSetSpec{
			Template: operatorv1alpha1.PodCliqueSetTemplateSpec{
				TopologyConstraint: &operatorv1alpha1.TopologyConstraint{
					PackDomain: packDomain,
				},
			},
		},
	}
}

// SetPodGangPlacementScore sets a PodGang with placement score for testing
func (info *TopologyInfo) SetPodGangPlacementScore(name string, score float64) {
	pg := schedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: schedulerv1alpha1.PodGangStatus{
			PlacementScore: &score,
		},
	}
	info.PodGangs = append(info.PodGangs, pg)
}

// CalculateGPUUsage calculates GPU usage for testing
func (info *TopologyInfo) CalculateGPUUsage() {
	for nodeName, node := range info.Nodes {
		capacity := int64(0)
		if gpuCap, ok := node.Status.Capacity[corev1.ResourceName(GPUResourceName)]; ok {
			capacity = gpuCap.Value()
		}
		info.NodeGPUUsage[nodeName] = GPUUsage{
			Capacity:  capacity,
			Allocated: 0,
		}
	}

	for _, pod := range info.Pods {
		if pod.Spec.NodeName == "" {
			continue
		}
		gpuCount := getPodGPUCount(&pod)
		if gpuCount > 0 {
			if u, ok := info.NodeGPUUsage[pod.Spec.NodeName]; ok {
				info.NodeGPUUsage[pod.Spec.NodeName] = GPUUsage{
					Capacity:  u.Capacity,
					Allocated: u.Allocated + gpuCount,
				}
			}
		}
	}
}
