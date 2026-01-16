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

// Package commands provides CLI command implementations for kubectl-grove.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/cmd/kubectl-grove/internal/aic"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// CompareCmd represents the compare command for plan vs actual comparison.
type CompareCmd struct {
	Name      string `arg:"" help:"Name of the PodCliqueSet to compare."`
	Namespace string `short:"n" help:"Namespace to query." default:"default"`
	JSON      bool   `help:"Output in JSON format."`
	Verbose   bool   `short:"v" help:"Show verbose output including per-pod details."`
}

// DiffStatus represents the status of a comparison.
type DiffStatus string

const (
	// DiffStatusMatch indicates values match.
	DiffStatusMatch DiffStatus = "match"
	// DiffStatusMismatch indicates values do not match.
	DiffStatusMismatch DiffStatus = "mismatch"
	// DiffStatusWarning indicates a warning condition.
	DiffStatusWarning DiffStatus = "warning"
)

// Plan represents a stored AIConfigurator plan.
type Plan struct {
	Model        string      `json:"model"`
	Backend      string      `json:"backend"`
	Roles        []RolePlan  `json:"roles"`
	TopologyPack string      `json:"topologyPack"`
	SLATargets   *SLATargets `json:"slaTargets,omitempty"`
}

// RolePlan represents a planned role configuration.
type RolePlan struct {
	Name               string `json:"name"`
	Replicas           int    `json:"replicas"`
	GPUs               int    `json:"gpus"`
	TensorParallelSize int    `json:"tensorParallelSize"`
}

// SLATargets represents SLA performance targets.
type SLATargets struct {
	TTFTMs           float64 `json:"ttftMs,omitempty"`
	TPOTMs           float64 `json:"tpotMs,omitempty"`
	ThroughputPerGPU float64 `json:"throughputPerGPU,omitempty"`
}

// ActualState represents the actual deployed state.
type ActualState struct {
	PodCliqueSet *operatorv1alpha1.PodCliqueSet
	PodCliques   []*operatorv1alpha1.PodClique
	PodGangs     []*schedulerv1alpha1.PodGang
	Pods         []*corev1.Pod
}

// ComparisonResult holds the complete comparison results.
type ComparisonResult struct {
	Name            string             `json:"name"`
	Namespace       string             `json:"namespace"`
	PlanTimestamp   string             `json:"planTimestamp,omitempty"`
	Configuration   ConfigComparison   `json:"configuration"`
	Topology        TopologyComparison `json:"topology"`
	Diagnosis       []string           `json:"diagnosis"`
	Recommendations []string           `json:"recommendations"`
}

// ConfigComparison holds configuration comparison results.
type ConfigComparison struct {
	Matches bool         `json:"matches"`
	Diffs   []ConfigDiff `json:"diffs"`
}

// ConfigDiff represents a single configuration difference.
type ConfigDiff struct {
	Setting string     `json:"setting"`
	Planned string     `json:"planned"`
	Actual  string     `json:"actual"`
	Status  DiffStatus `json:"status"`
}

// TopologyComparison holds topology comparison results.
type TopologyComparison struct {
	PlannedConstraint string             `json:"plannedConstraint"`
	ActualConstraint  string             `json:"actualConstraint,omitempty"`
	PlacementScore    float64            `json:"placementScore"`
	RacksUsed         int                `json:"racksUsed"`
	ExpectedRacks     int                `json:"expectedRacks"`
	Status            DiffStatus         `json:"status"`
	PodPlacements     []PodPlacementInfo `json:"podPlacements,omitempty"`
}

// PodPlacementInfo holds information about a pod's placement.
type PodPlacementInfo struct {
	PodName  string `json:"podName"`
	NodeName string `json:"nodeName"`
	Rack     string `json:"rack,omitempty"`
	Status   string `json:"status"`
}

// Execute runs the compare command.
func (c *CompareCmd) Execute(clientset kubernetes.Interface, dynamicClient dynamic.Interface) error {
	return c.ExecuteWithWriter(clientset, dynamicClient, os.Stdout)
}

// ExecuteWithWriter runs the compare command with a custom writer.
func (c *CompareCmd) ExecuteWithWriter(clientset kubernetes.Interface, dynamicClient dynamic.Interface, out io.Writer) error {
	ctx := context.Background()

	// Load the stored plan
	plan, storedPlan, err := c.loadPlan(ctx, clientset)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Load the actual state
	actualState, err := c.loadActualState(ctx, dynamicClient, clientset)
	if err != nil {
		return fmt.Errorf("failed to load actual state: %w", err)
	}

	// Perform comparison
	result := c.compare(plan, storedPlan, actualState)

	// Output results
	if c.JSON {
		return c.outputJSON(out, result)
	}
	return c.outputTable(out, result)
}

// loadPlan loads the plan from a ConfigMap.
func (c *CompareCmd) loadPlan(ctx context.Context, clientset kubernetes.Interface) (*Plan, *aic.StoredPlan, error) {
	store := aic.NewPlanStore(clientset)
	storedPlan, err := store.Get(ctx, c.Name, c.Namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("no plan found for '%s' in namespace '%s': %w", c.Name, c.Namespace, err)
	}

	// Convert StoredPlan to our Plan structure
	plan := &Plan{
		Model:        storedPlan.Plan.Model,
		Backend:      storedPlan.Plan.ServingMode,
		TopologyPack: "rack", // Default, may be overridden
		Roles:        []RolePlan{},
	}

	// Add prefill role
	if storedPlan.Plan.Config.PrefillWorkers > 0 {
		plan.Roles = append(plan.Roles, RolePlan{
			Name:               "prefill",
			Replicas:           storedPlan.Plan.Config.PrefillWorkers,
			GPUs:               storedPlan.Plan.Config.PrefillTP,
			TensorParallelSize: storedPlan.Plan.Config.PrefillTP,
		})
	}

	// Add decode role
	if storedPlan.Plan.Config.DecodeWorkers > 0 {
		plan.Roles = append(plan.Roles, RolePlan{
			Name:               "decode",
			Replicas:           storedPlan.Plan.Config.DecodeWorkers,
			GPUs:               storedPlan.Plan.Config.DecodeTP,
			TensorParallelSize: storedPlan.Plan.Config.DecodeTP,
		})
	}

	// Add SLA targets if available
	if storedPlan.Plan.SLA.TTFTMs > 0 || storedPlan.Plan.SLA.TPOTMs > 0 {
		plan.SLATargets = &SLATargets{
			TTFTMs: storedPlan.Plan.SLA.TTFTMs,
			TPOTMs: storedPlan.Plan.SLA.TPOTMs,
		}
		if storedPlan.Plan.Expected.ThroughputTokensPerSecPerGPU > 0 {
			plan.SLATargets.ThroughputPerGPU = storedPlan.Plan.Expected.ThroughputTokensPerSecPerGPU
		}
	}

	return plan, storedPlan, nil
}

// loadActualState loads the actual deployed state from the cluster.
func (c *CompareCmd) loadActualState(ctx context.Context, dynamicClient dynamic.Interface, clientset kubernetes.Interface) (*ActualState, error) {
	state := &ActualState{}

	// Get PodCliqueSet
	pcsUnstructured, err := dynamicClient.Resource(podCliqueSetGVR).Namespace(c.Namespace).Get(ctx, c.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get PodCliqueSet %s: %w", c.Name, err)
	}

	pcs := &operatorv1alpha1.PodCliqueSet{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pcsUnstructured.Object, pcs); err != nil {
		return nil, fmt.Errorf("failed to convert PodCliqueSet: %w", err)
	}
	state.PodCliqueSet = pcs

	// Get PodCliques
	pcList, err := dynamicClient.Resource(podCliqueGVR).Namespace(c.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("grove.io/podcliqueset=%s", c.Name),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list PodCliques: %w", err)
	}

	for _, item := range pcList.Items {
		pc := &operatorv1alpha1.PodClique{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, pc); err != nil {
			continue
		}
		state.PodCliques = append(state.PodCliques, pc)
	}

	// Get PodGangs
	pgList, err := dynamicClient.Resource(podGangGVR).Namespace(c.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("grove.io/podcliqueset=%s", c.Name),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list PodGangs: %w", err)
	}

	for _, item := range pgList.Items {
		pg := &schedulerv1alpha1.PodGang{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, pg); err != nil {
			continue
		}
		state.PodGangs = append(state.PodGangs, pg)
	}

	// Get Pods
	podList, err := clientset.CoreV1().Pods(c.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("grove.io/podcliqueset-name=%s", c.Name),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list Pods: %w", err)
	}

	for i := range podList.Items {
		state.Pods = append(state.Pods, &podList.Items[i])
	}

	return state, nil
}

// compare performs the comparison between plan and actual state.
func (c *CompareCmd) compare(plan *Plan, storedPlan *aic.StoredPlan, actual *ActualState) *ComparisonResult {
	result := &ComparisonResult{
		Name:      c.Name,
		Namespace: c.Namespace,
	}

	if storedPlan != nil {
		result.PlanTimestamp = storedPlan.StoredAt.Format("2006-01-02T15:04:05Z")
	}

	// Compare configuration
	result.Configuration = c.compareConfiguration(plan, actual)

	// Compare topology
	result.Topology = c.compareTopology(plan, actual)

	// Generate diagnosis and recommendations
	result.Diagnosis, result.Recommendations = c.generateDiagnosisAndRecommendations(result, actual)

	return result
}

// compareConfiguration compares the configuration between plan and actual.
func (c *CompareCmd) compareConfiguration(plan *Plan, actual *ActualState) ConfigComparison {
	comparison := ConfigComparison{
		Matches: true,
		Diffs:   []ConfigDiff{},
	}

	// Build a map of actual configuration by role
	actualConfig := c.extractActualConfig(actual)
	titleCaser := cases.Title(language.English)

	// Compare each planned role
	for _, role := range plan.Roles {
		roleLower := strings.ToLower(role.Name)

		// Compare replicas
		actualReplicas := actualConfig[roleLower+"_replicas"]
		replicasDiff := ConfigDiff{
			Setting: fmt.Sprintf("%s Replicas", titleCaser.String(role.Name)),
			Planned: fmt.Sprintf("%d", role.Replicas),
			Actual:  fmt.Sprintf("%d", actualReplicas),
			Status:  DiffStatusMatch,
		}
		if role.Replicas != actualReplicas {
			replicasDiff.Status = DiffStatusMismatch
			comparison.Matches = false
		}
		comparison.Diffs = append(comparison.Diffs, replicasDiff)

		// Compare GPUs
		actualGPUs := actualConfig[roleLower+"_gpus"]
		gpusDiff := ConfigDiff{
			Setting: fmt.Sprintf("%s GPUs", titleCaser.String(role.Name)),
			Planned: fmt.Sprintf("%d", role.GPUs),
			Actual:  fmt.Sprintf("%d", actualGPUs),
			Status:  DiffStatusMatch,
		}
		if role.GPUs != actualGPUs {
			gpusDiff.Status = DiffStatusMismatch
			comparison.Matches = false
		}
		comparison.Diffs = append(comparison.Diffs, gpusDiff)

		// Compare TP Size
		actualTP := actualConfig[roleLower+"_tp"]
		tpDiff := ConfigDiff{
			Setting: fmt.Sprintf("TP Size (%s)", role.Name),
			Planned: fmt.Sprintf("%d", role.TensorParallelSize),
			Actual:  fmt.Sprintf("%d", actualTP),
			Status:  DiffStatusMatch,
		}
		if role.TensorParallelSize != actualTP {
			tpDiff.Status = DiffStatusMismatch
			comparison.Matches = false
		}
		comparison.Diffs = append(comparison.Diffs, tpDiff)
	}

	return comparison
}

// extractActualConfig extracts configuration from the actual state.
func (c *CompareCmd) extractActualConfig(actual *ActualState) map[string]int {
	config := make(map[string]int)

	if actual.PodCliqueSet == nil {
		return config
	}

	// Extract from PodCliqueScalingGroups
	for _, sg := range actual.PodCliqueSet.Spec.Template.PodCliqueScalingGroupConfigs {
		nameLower := strings.ToLower(sg.Name)
		replicas := int32(1)
		if sg.Replicas != nil {
			replicas = *sg.Replicas
		}

		if strings.Contains(nameLower, "prefill") {
			config["prefill_replicas"] = int(replicas)
		} else if strings.Contains(nameLower, "decode") {
			config["decode_replicas"] = int(replicas)
		}
	}

	// Extract from cliques
	for _, clique := range actual.PodCliqueSet.Spec.Template.Cliques {
		if clique == nil {
			continue
		}
		nameLower := strings.ToLower(clique.Name)

		// Get replicas (used as TP)
		replicas := int(clique.Spec.Replicas)

		// Extract GPU count from pod spec
		gpuCount := 0
		for _, container := range clique.Spec.PodSpec.Containers {
			if gpuReq, ok := container.Resources.Requests[corev1.ResourceName(GPUResourceName)]; ok {
				gpuCount += int(gpuReq.Value())
			} else if gpuLimit, ok := container.Resources.Limits[corev1.ResourceName(GPUResourceName)]; ok {
				gpuCount += int(gpuLimit.Value())
			}
		}

		if strings.Contains(nameLower, "prefill") {
			config["prefill_tp"] = replicas
			config["prefill_gpus"] = replicas // TP typically equals GPUs for prefill
			if config["prefill_replicas"] == 0 {
				config["prefill_replicas"] = replicas
			}
		} else if strings.Contains(nameLower, "decode") {
			config["decode_tp"] = replicas
			config["decode_gpus"] = replicas // TP typically equals GPUs for decode
			if config["decode_replicas"] == 0 {
				config["decode_replicas"] = replicas
			}
		}
	}

	return config
}

// compareTopology compares the topology between plan and actual.
func (c *CompareCmd) compareTopology(plan *Plan, actual *ActualState) TopologyComparison {
	comparison := TopologyComparison{
		PlannedConstraint: plan.TopologyPack,
		Status:            DiffStatusMatch,
		ExpectedRacks:     1,
	}

	// Get actual topology constraint
	if actual.PodCliqueSet != nil && actual.PodCliqueSet.Spec.Template.TopologyConstraint != nil {
		comparison.ActualConstraint = string(actual.PodCliqueSet.Spec.Template.TopologyConstraint.PackDomain)
	}

	// Calculate placement score
	var totalScore float64
	var scoreCount int
	for _, pg := range actual.PodGangs {
		if pg.Status.PlacementScore != nil {
			totalScore += *pg.Status.PlacementScore
			scoreCount++
		}
	}
	if scoreCount > 0 {
		comparison.PlacementScore = totalScore / float64(scoreCount)
	}

	// Count unique racks
	racks := make(map[string]struct{})
	for _, pod := range actual.Pods {
		if pod.Spec.NodeName != "" {
			// Try to extract rack from node labels (would need node info)
			// For now, use a heuristic based on node name
			rack := extractRackFromNodeName(pod.Spec.NodeName)
			if rack != "" {
				racks[rack] = struct{}{}
			}
		}
	}
	comparison.RacksUsed = len(racks)
	if comparison.RacksUsed == 0 {
		comparison.RacksUsed = 1 // Default to 1 if we can't determine
	}

	// Determine status
	if comparison.PlacementScore > 0 && comparison.PlacementScore < 0.9 {
		comparison.Status = DiffStatusWarning
	}
	if comparison.RacksUsed > comparison.ExpectedRacks {
		comparison.Status = DiffStatusWarning
	}

	// Add pod placements if verbose
	if c.Verbose {
		for _, pod := range actual.Pods {
			placement := PodPlacementInfo{
				PodName:  pod.Name,
				NodeName: pod.Spec.NodeName,
				Rack:     extractRackFromNodeName(pod.Spec.NodeName),
				Status:   string(pod.Status.Phase),
			}
			comparison.PodPlacements = append(comparison.PodPlacements, placement)
		}
	}

	return comparison
}

// extractRackFromNodeName attempts to extract a rack identifier from a node name.
func extractRackFromNodeName(nodeName string) string {
	// Common patterns: node-rack1-001, rack1-node-001, etc.
	parts := strings.Split(nodeName, "-")
	for _, part := range parts {
		if strings.HasPrefix(strings.ToLower(part), "rack") {
			return part
		}
	}
	// If no rack found, use a heuristic based on node name
	if len(nodeName) > 0 {
		// Group by first part of node name
		if len(parts) > 0 {
			return parts[0]
		}
	}
	return ""
}

// generateDiagnosisAndRecommendations generates diagnosis and recommendations.
func (c *CompareCmd) generateDiagnosisAndRecommendations(result *ComparisonResult, actual *ActualState) ([]string, []string) {
	var diagnosis []string
	var recommendations []string

	// Check placement score
	if result.Topology.PlacementScore > 0 && result.Topology.PlacementScore < 0.9 {
		diagnosis = append(diagnosis, fmt.Sprintf("PlacementScore %.2f indicates suboptimal placement", result.Topology.PlacementScore))
		recommendations = append(recommendations, "Consider rescheduling to improve network locality")
	}

	// Check rack spread
	if result.Topology.RacksUsed > result.Topology.ExpectedRacks {
		diagnosis = append(diagnosis, fmt.Sprintf("Pods are split across %d racks (expected: %d)", result.Topology.RacksUsed, result.Topology.ExpectedRacks))
		recommendations = append(recommendations, "Consider rescheduling to consolidate pods on single rack")
	}

	// Check configuration mismatches
	if !result.Configuration.Matches {
		mismatchCount := 0
		for _, diff := range result.Configuration.Diffs {
			if diff.Status == DiffStatusMismatch {
				mismatchCount++
			}
		}
		diagnosis = append(diagnosis, fmt.Sprintf("%d configuration settings differ from plan", mismatchCount))
		recommendations = append(recommendations, "Review deployed configuration against the stored plan")
	}

	// Check for unhealthy pods
	unhealthyPods := 0
	for _, pod := range actual.Pods {
		if pod.Status.Phase != corev1.PodRunning {
			unhealthyPods++
		}
	}
	if unhealthyPods > 0 {
		diagnosis = append(diagnosis, fmt.Sprintf("%d pods are not in Running state", unhealthyPods))
		recommendations = append(recommendations, "Check cluster capacity and pod health")
	}

	// Check for resource pressure
	if result.Topology.PlacementScore < 0.7 && result.Topology.RacksUsed > 1 {
		diagnosis = append(diagnosis, "Fragmentation may indicate resource pressure in the cluster")
		recommendations = append(recommendations, "Check cluster capacity - fragmentation may indicate resource pressure")
	}

	return diagnosis, recommendations
}

// outputJSON outputs the result in JSON format.
func (c *CompareCmd) outputJSON(out io.Writer, result *ComparisonResult) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

// outputTable outputs the result in a formatted table.
func (c *CompareCmd) outputTable(out io.Writer, result *ComparisonResult) error {
	// Header
	fmt.Fprintf(out, "Plan vs Actual: %s (namespace: %s)\n", result.Name, result.Namespace)
	if result.PlanTimestamp != "" {
		fmt.Fprintf(out, "Plan stored: %s\n", result.PlanTimestamp)
	}
	fmt.Fprintln(out)

	// Configuration table
	fmt.Fprintln(out, "Configuration:")
	c.printConfigTable(out, result.Configuration.Diffs)
	fmt.Fprintln(out)

	// Topology table
	fmt.Fprintln(out, "Topology:")
	c.printTopologyTable(out, result.Topology)
	fmt.Fprintln(out)

	// Verbose pod placements
	if c.Verbose && len(result.Topology.PodPlacements) > 0 {
		fmt.Fprintln(out, "Pod Placements:")
		c.printPodPlacementsTable(out, result.Topology.PodPlacements)
		fmt.Fprintln(out)
	}

	// Diagnosis
	if len(result.Diagnosis) > 0 {
		fmt.Fprintln(out, "Diagnosis:")
		for _, d := range result.Diagnosis {
			fmt.Fprintf(out, "  ! %s\n", d)
		}
		fmt.Fprintln(out)
	}

	// Recommendations
	if len(result.Recommendations) > 0 {
		fmt.Fprintln(out, "Recommendations:")
		for i, r := range result.Recommendations {
			fmt.Fprintf(out, "  %d. %s\n", i+1, r)
		}
	}

	return nil
}

// printConfigTable prints the configuration comparison table.
func (c *CompareCmd) printConfigTable(out io.Writer, diffs []ConfigDiff) {
	// Calculate column widths
	settingWidth := len("Setting")
	plannedWidth := len("Planned")
	actualWidth := len("Actual")
	statusWidth := len("Status")

	for _, d := range diffs {
		if len(d.Setting) > settingWidth {
			settingWidth = len(d.Setting)
		}
		if len(d.Planned) > plannedWidth {
			plannedWidth = len(d.Planned)
		}
		if len(d.Actual) > actualWidth {
			actualWidth = len(d.Actual)
		}
	}

	// Add padding
	settingWidth += 2
	plannedWidth += 2
	actualWidth += 2
	statusWidth += 6 // For status symbols

	// Print table
	printCompareTableBorder(out, settingWidth, plannedWidth, actualWidth, statusWidth, "top")
	printCompareTableRow(out, settingWidth, plannedWidth, actualWidth, statusWidth, "Setting", "Planned", "Actual", "Status")
	printCompareTableBorder(out, settingWidth, plannedWidth, actualWidth, statusWidth, "middle")

	for _, d := range diffs {
		status := formatDiffStatus(d.Status)
		printCompareTableRow(out, settingWidth, plannedWidth, actualWidth, statusWidth, d.Setting, d.Planned, d.Actual, status)
	}

	printCompareTableBorder(out, settingWidth, plannedWidth, actualWidth, statusWidth, "bottom")
}

// printTopologyTable prints the topology comparison table.
func (c *CompareCmd) printTopologyTable(out io.Writer, topology TopologyComparison) {
	diffs := []ConfigDiff{
		{
			Setting: "Pack Constraint",
			Planned: topology.PlannedConstraint,
			Actual:  topology.ActualConstraint,
			Status:  boolToDiffStatus(topology.PlannedConstraint == topology.ActualConstraint),
		},
		{
			Setting: "PlacementScore",
			Planned: "1.0",
			Actual:  fmt.Sprintf("%.2f", topology.PlacementScore),
			Status:  scoreToStatus(topology.PlacementScore),
		},
		{
			Setting: "Racks Used",
			Planned: fmt.Sprintf("%d", topology.ExpectedRacks),
			Actual:  fmt.Sprintf("%d", topology.RacksUsed),
			Status:  boolToDiffStatus(topology.RacksUsed <= topology.ExpectedRacks),
		},
	}

	// Calculate column widths
	settingWidth := len("Metric")
	plannedWidth := len("Planned")
	actualWidth := len("Actual")
	statusWidth := len("Status")

	for _, d := range diffs {
		if len(d.Setting) > settingWidth {
			settingWidth = len(d.Setting)
		}
		if len(d.Planned) > plannedWidth {
			plannedWidth = len(d.Planned)
		}
		if len(d.Actual) > actualWidth {
			actualWidth = len(d.Actual)
		}
	}

	// Add padding
	settingWidth += 2
	plannedWidth += 2
	actualWidth += 2
	statusWidth += 6

	// Print table
	printCompareTableBorder(out, settingWidth, plannedWidth, actualWidth, statusWidth, "top")
	printCompareTableRow(out, settingWidth, plannedWidth, actualWidth, statusWidth, "Metric", "Planned", "Actual", "Status")
	printCompareTableBorder(out, settingWidth, plannedWidth, actualWidth, statusWidth, "middle")

	for _, d := range diffs {
		status := formatDiffStatus(d.Status)
		printCompareTableRow(out, settingWidth, plannedWidth, actualWidth, statusWidth, d.Setting, d.Planned, d.Actual, status)
	}

	printCompareTableBorder(out, settingWidth, plannedWidth, actualWidth, statusWidth, "bottom")
}

// printPodPlacementsTable prints the pod placements table.
func (c *CompareCmd) printPodPlacementsTable(out io.Writer, placements []PodPlacementInfo) {
	// Sort by rack then name
	sort.Slice(placements, func(i, j int) bool {
		if placements[i].Rack != placements[j].Rack {
			return placements[i].Rack < placements[j].Rack
		}
		return placements[i].PodName < placements[j].PodName
	})

	// Calculate column widths
	podWidth := len("Pod")
	nodeWidth := len("Node")
	rackWidth := len("Rack")
	statusWidth := len("Status")

	for _, p := range placements {
		if len(p.PodName) > podWidth {
			podWidth = len(p.PodName)
		}
		if len(p.NodeName) > nodeWidth {
			nodeWidth = len(p.NodeName)
		}
		if len(p.Rack) > rackWidth {
			rackWidth = len(p.Rack)
		}
		if len(p.Status) > statusWidth {
			statusWidth = len(p.Status)
		}
	}

	// Add padding
	podWidth += 2
	nodeWidth += 2
	rackWidth += 2
	statusWidth += 2

	// Print table
	fmt.Fprintf(out, "| %-*s | %-*s | %-*s | %-*s |\n", podWidth, "Pod", nodeWidth, "Node", rackWidth, "Rack", statusWidth, "Status")
	fmt.Fprintf(out, "|%s|%s|%s|%s|\n",
		strings.Repeat("-", podWidth+2),
		strings.Repeat("-", nodeWidth+2),
		strings.Repeat("-", rackWidth+2),
		strings.Repeat("-", statusWidth+2))

	for _, p := range placements {
		fmt.Fprintf(out, "| %-*s | %-*s | %-*s | %-*s |\n", podWidth, p.PodName, nodeWidth, p.NodeName, rackWidth, p.Rack, statusWidth, p.Status)
	}
}

// printCompareTableBorder prints a table border.
func printCompareTableBorder(out io.Writer, w1, w2, w3, w4 int, position string) {
	var left, mid, right, fill string
	switch position {
	case "top":
		left, mid, right, fill = "+", "+", "+", "-"
	case "middle":
		left, mid, right, fill = "+", "+", "+", "-"
	case "bottom":
		left, mid, right, fill = "+", "+", "+", "-"
	}

	fmt.Fprintf(out, "%s%s%s%s%s%s%s%s%s\n",
		left, strings.Repeat(fill, w1+1),
		mid, strings.Repeat(fill, w2+1),
		mid, strings.Repeat(fill, w3+1),
		mid, strings.Repeat(fill, w4+1),
		right)
}

// printCompareTableRow prints a table row.
func printCompareTableRow(out io.Writer, w1, w2, w3, w4 int, c1, c2, c3, c4 string) {
	fmt.Fprintf(out, "| %-*s | %-*s | %-*s | %-*s |\n", w1, c1, w2, c2, w3, c3, w4, c4)
}

// formatDiffStatus formats a diff status for display.
func formatDiffStatus(status DiffStatus) string {
	switch status {
	case DiffStatusMatch:
		return "Match"
	case DiffStatusMismatch:
		return "Differs"
	case DiffStatusWarning:
		return "Warning"
	default:
		return string(status)
	}
}

// boolToDiffStatus converts a boolean to a diff status.
func boolToDiffStatus(matches bool) DiffStatus {
	if matches {
		return DiffStatusMatch
	}
	return DiffStatusMismatch
}

// scoreToStatus converts a placement score to a diff status.
func scoreToStatus(score float64) DiffStatus {
	if score >= 0.9 {
		return DiffStatusMatch
	}
	if score >= 0.7 {
		return DiffStatusWarning
	}
	return DiffStatusMismatch
}
