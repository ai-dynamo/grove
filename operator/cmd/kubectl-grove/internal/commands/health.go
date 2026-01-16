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
	"strings"
	"time"

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Default termination delay if not specified
const DefaultTerminationDelay = 4 * time.Hour

// HealthCmd represents the health command
type HealthCmd struct {
	Clientset     kubernetes.Interface
	DynamicClient dynamic.Interface
	Namespace     string
	All           bool
	Watch         bool
	Name          string
}

// GangHealth represents the health status of a single PodGang
type GangHealth struct {
	Name            string
	Phase           string
	PlacementScore  float64
	IsHealthy       bool
	UnhealthyReason string
	UnhealthySince  *time.Time
	TerminationIn   *time.Duration
	Issues          []string
	CliqueStatuses  []CliqueHealth
}

// CliqueHealth represents the health status of a PodClique
type CliqueHealth struct {
	Name          string
	ReadyReplicas int32
	Replicas      int32
	MinAvailable  int32
	AboveMin      bool
}

// HealthDashboard contains all data needed to render the health dashboard
type HealthDashboard struct {
	PodCliqueSetName  string
	TerminationDelay  time.Duration
	Gangs             []GangHealth
	HealthyCount      int
	TotalCount        int
	AtRiskCount       int
}

// Run executes the health command
func (h *HealthCmd) Run(ctx context.Context) error {
	if h.All {
		return h.runAll(ctx)
	}
	if h.Name == "" {
		return fmt.Errorf("podcliqueset name is required (use --all to show all)")
	}
	return h.runSingle(ctx, h.Name)
}

// runAll shows health for all PodCliqueSets in the namespace
func (h *HealthCmd) runAll(ctx context.Context) error {
	// List all PodCliqueSets
	pcsList, err := h.DynamicClient.Resource(podCliqueSetGVR).Namespace(h.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list PodCliqueSets: %w", err)
	}

	if len(pcsList.Items) == 0 {
		fmt.Printf("No PodCliqueSets found in namespace %s\n", h.Namespace)
		return nil
	}

	for i, pcsUnstructured := range pcsList.Items {
		if i > 0 {
			fmt.Println() // Add spacing between dashboards
		}
		dashboard, err := h.buildDashboard(ctx, &pcsUnstructured)
		if err != nil {
			fmt.Printf("Error building dashboard for %s: %v\n", pcsUnstructured.GetName(), err)
			continue
		}
		h.renderDashboard(dashboard)
	}

	return nil
}

// runSingle shows health for a single PodCliqueSet
func (h *HealthCmd) runSingle(ctx context.Context, name string) error {
	if h.Watch {
		return h.watchHealth(ctx, name)
	}

	// Get the PodCliqueSet
	pcsUnstructured, err := h.DynamicClient.Resource(podCliqueSetGVR).Namespace(h.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get PodCliqueSet %s: %w", name, err)
	}

	dashboard, err := h.buildDashboard(ctx, pcsUnstructured)
	if err != nil {
		return fmt.Errorf("failed to build dashboard: %w", err)
	}

	h.renderDashboard(dashboard)
	return nil
}

// watchHealth implements the --watch mode
func (h *HealthCmd) watchHealth(ctx context.Context, name string) error {
	// Initial display
	if err := h.runSingle(context.Background(), name); err != nil {
		return err
	}

	// Set up watch on the PodCliqueSet
	watcher, err := h.DynamicClient.Resource(podCliqueSetGVR).Namespace(h.Namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", name),
	})
	if err != nil {
		return fmt.Errorf("failed to watch PodCliqueSet: %w", err)
	}
	defer watcher.Stop()

	// Watch for changes
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}
			if event.Type == watch.Modified || event.Type == watch.Added {
				// Clear screen and redisplay
				fmt.Print("\033[2J\033[H") // ANSI escape to clear screen
				pcsUnstructured, ok := event.Object.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				dashboard, err := h.buildDashboard(ctx, pcsUnstructured)
				if err != nil {
					fmt.Printf("Error: %v\n", err)
					continue
				}
				h.renderDashboard(dashboard)
			}
		}
	}
}

// buildDashboard constructs the health dashboard from cluster state
func (h *HealthCmd) buildDashboard(ctx context.Context, pcsUnstructured *unstructured.Unstructured) (*HealthDashboard, error) {
	// Convert to typed PodCliqueSet
	pcs := &operatorv1alpha1.PodCliqueSet{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pcsUnstructured.Object, pcs); err != nil {
		return nil, fmt.Errorf("failed to convert PodCliqueSet: %w", err)
	}

	// Get termination delay
	terminationDelay := DefaultTerminationDelay
	if pcs.Spec.Template.TerminationDelay != nil {
		terminationDelay = pcs.Spec.Template.TerminationDelay.Duration
	}

	dashboard := &HealthDashboard{
		PodCliqueSetName: pcs.Name,
		TerminationDelay: terminationDelay,
		Gangs:            []GangHealth{},
	}

	// Get all PodGangs owned by this PodCliqueSet
	podGangList, err := h.DynamicClient.Resource(podGangGVR).Namespace(h.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list PodGangs: %w", err)
	}

	// Get all PodCliques owned by this PodCliqueSet
	podCliqueList, err := h.DynamicClient.Resource(podCliqueGVR).Namespace(h.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list PodCliques: %w", err)
	}

	// Build a map of clique name -> PodClique for quick lookup
	cliqueMap := make(map[string]*operatorv1alpha1.PodClique)
	for _, clqUnstructured := range podCliqueList.Items {
		clq := &operatorv1alpha1.PodClique{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(clqUnstructured.Object, clq); err != nil {
			continue
		}
		// Check if this clique belongs to our PodCliqueSet
		if isOwnedBy(clq.GetOwnerReferences(), pcs.Name, "PodCliqueSet") {
			cliqueMap[clq.Name] = clq
		}
	}

	// Process each PodGang that belongs to this PodCliqueSet
	for _, pgUnstructured := range podGangList.Items {
		pg := &schedulerv1alpha1.PodGang{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pgUnstructured.Object, pg); err != nil {
			continue
		}

		// Check if this PodGang belongs to our PodCliqueSet (by name prefix)
		if !strings.HasPrefix(pg.Name, pcs.Name+"-") {
			continue
		}

		gangHealth := h.buildGangHealth(ctx, pg, cliqueMap, terminationDelay)
		dashboard.Gangs = append(dashboard.Gangs, gangHealth)

		dashboard.TotalCount++
		if gangHealth.IsHealthy {
			dashboard.HealthyCount++
		} else if gangHealth.TerminationIn != nil {
			dashboard.AtRiskCount++
		}
	}

	// If no PodGangs found, use status from PodCliqueSet
	if len(dashboard.Gangs) == 0 && len(pcs.Status.PodGangStatutes) > 0 {
		for _, pgStatus := range pcs.Status.PodGangStatutes {
			gangHealth := GangHealth{
				Name:      pgStatus.Name,
				Phase:     string(pgStatus.Phase),
				IsHealthy: pgStatus.Phase == operatorv1alpha1.PodGangRunning,
			}
			if !gangHealth.IsHealthy {
				gangHealth.UnhealthyReason = "Phase is not Running"
			}
			dashboard.Gangs = append(dashboard.Gangs, gangHealth)
			dashboard.TotalCount++
			if gangHealth.IsHealthy {
				dashboard.HealthyCount++
			}
		}
	}

	return dashboard, nil
}

// buildGangHealth builds health status for a single PodGang
func (h *HealthCmd) buildGangHealth(ctx context.Context, pg *schedulerv1alpha1.PodGang, cliqueMap map[string]*operatorv1alpha1.PodClique, terminationDelay time.Duration) GangHealth {
	gangHealth := GangHealth{
		Name:      pg.Name,
		Phase:     string(pg.Status.Phase),
		IsHealthy: true,
	}

	// Get placement score
	if pg.Status.PlacementScore != nil {
		gangHealth.PlacementScore = *pg.Status.PlacementScore
	}

	// Check for Unhealthy condition
	for _, cond := range pg.Status.Conditions {
		if cond.Type == string(schedulerv1alpha1.PodGangConditionTypeUnhealthy) && cond.Status == metav1.ConditionTrue {
			gangHealth.IsHealthy = false
			gangHealth.UnhealthyReason = cond.Message
			unhealthyTime := cond.LastTransitionTime.Time
			gangHealth.UnhealthySince = &unhealthyTime

			// Calculate time remaining until termination
			elapsed := time.Since(unhealthyTime)
			remaining := terminationDelay - elapsed
			if remaining > 0 {
				gangHealth.TerminationIn = &remaining
			} else {
				zero := time.Duration(0)
				gangHealth.TerminationIn = &zero
			}
			break
		}
	}

	// Check clique health
	for _, podGroup := range pg.Spec.PodGroups {
		// Find the corresponding PodClique
		cliqueName := fmt.Sprintf("%s-%s", pg.Name, podGroup.Name)
		for name, clq := range cliqueMap {
			if strings.HasSuffix(name, podGroup.Name) || name == cliqueName {
				cliqueHealth := CliqueHealth{
					Name:          podGroup.Name,
					ReadyReplicas: clq.Status.ReadyReplicas,
					Replicas:      clq.Spec.Replicas,
				}

				// Determine MinAvailable
				if clq.Spec.MinAvailable != nil {
					cliqueHealth.MinAvailable = *clq.Spec.MinAvailable
				} else {
					cliqueHealth.MinAvailable = clq.Spec.Replicas
				}

				cliqueHealth.AboveMin = clq.Status.ReadyReplicas >= cliqueHealth.MinAvailable
				gangHealth.CliqueStatuses = append(gangHealth.CliqueStatuses, cliqueHealth)

				if !cliqueHealth.AboveMin {
					gangHealth.IsHealthy = false
				}
				break
			}
		}
	}

	// Gather issues from pods if unhealthy
	if !gangHealth.IsHealthy {
		issues := h.gatherPodIssues(ctx, pg)
		gangHealth.Issues = issues
	}

	return gangHealth
}

// gatherPodIssues collects issues from pods in the PodGang
func (h *HealthCmd) gatherPodIssues(ctx context.Context, pg *schedulerv1alpha1.PodGang) []string {
	issues := []string{}

	for _, podGroup := range pg.Spec.PodGroups {
		for _, podRef := range podGroup.PodReferences {
			pod, err := h.Clientset.CoreV1().Pods(podRef.Namespace).Get(ctx, podRef.Name, metav1.GetOptions{})
			if err != nil {
				continue
			}

			// Check pod conditions for issues
			for _, cond := range pod.Status.Conditions {
				if cond.Status == corev1.ConditionFalse {
					switch cond.Type {
					case corev1.PodScheduled:
						if cond.Reason == corev1.PodReasonUnschedulable {
							issues = append(issues, fmt.Sprintf("%s: Pending (Unschedulable: %s)", podRef.Name, cond.Message))
						}
					case corev1.PodReady:
						if pod.Status.Phase == corev1.PodPending {
							issues = append(issues, fmt.Sprintf("%s: Pending", podRef.Name))
						} else if pod.Status.Phase == corev1.PodFailed {
							issues = append(issues, fmt.Sprintf("%s: Failed", podRef.Name))
						}
					}
				}
			}

			// Check container statuses
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.State.Waiting != nil {
					issues = append(issues, fmt.Sprintf("%s/%s: %s (%s)", podRef.Name, cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message))
				}
			}
		}
	}

	return issues
}

// renderDashboard renders the health dashboard to stdout
func (h *HealthCmd) renderDashboard(dashboard *HealthDashboard) {
	// Box-drawing characters
	const (
		topLeft     = "\u250C"     // Top left corner
		topRight    = "\u2510"     // Top right corner
		bottomLeft  = "\u2514"     // Bottom left corner
		bottomRight = "\u2518"     // Bottom right corner
		horizontal  = "\u2500"     // Horizontal line
		vertical    = "\u2502"     // Vertical line
		teeLeft     = "\u251C"     // Left tee
		teeRight    = "\u2524"     // Right tee
	)

	boxWidth := 68

	// Header
	fmt.Println()
	fmt.Println("Gang Health Dashboard")
	fmt.Println()
	fmt.Printf("PodCliqueSet: %s\n", dashboard.PodCliqueSetName)
	fmt.Println()

	// Render each gang
	for _, gang := range dashboard.Gangs {
		// Gang header
		title := fmt.Sprintf(" PodGang: %s ", gang.Name)
		padding := boxWidth - len(title) - 2
		fmt.Printf("%s%s%s%s%s\n", topLeft, horizontal, title, strings.Repeat(horizontal, padding), topRight)

		// Phase and PlacementScore line
		phaseStr := fmt.Sprintf("Phase: %s", gang.Phase)
		scoreStr := ""
		if gang.PlacementScore > 0 {
			scoreStr = fmt.Sprintf("    PlacementScore: %.2f", gang.PlacementScore)
		}
		contentLine := fmt.Sprintf("%s%s", phaseStr, scoreStr)
		fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, contentLine, vertical)

		// Health status line
		var statusLine string
		if gang.IsHealthy {
			statusLine = "Status: [OK] Healthy"
		} else if gang.TerminationIn != nil {
			statusLine = fmt.Sprintf("Status: [!] UNHEALTHY (Termination in %s)", formatDuration(*gang.TerminationIn))
		} else {
			statusLine = "Status: [!] UNHEALTHY"
			if gang.UnhealthyReason != "" {
				statusLine = fmt.Sprintf("Status: [!] UNHEALTHY (%s)", gang.UnhealthyReason)
			}
		}
		fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, statusLine, vertical)

		// Empty line
		fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, "", vertical)

		// Clique Health section
		if len(gang.CliqueStatuses) > 0 {
			fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, "Clique Health:", vertical)
			for _, clq := range gang.CliqueStatuses {
				var thresholdStatus string
				if clq.AboveMin {
					thresholdStatus = "[OK] Above threshold"
				} else {
					thresholdStatus = "[!] Below threshold"
				}
				clqLine := fmt.Sprintf("  %s   %d/%d ready  (min: %d)  %s", clq.Name, clq.ReadyReplicas, clq.Replicas, clq.MinAvailable, thresholdStatus)
				fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, clqLine, vertical)
			}
		}

		// Issues section
		if len(gang.Issues) > 0 {
			fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, "", vertical)
			fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, "Issues:", vertical)
			for _, issue := range gang.Issues {
				issueLine := fmt.Sprintf("  - %s", issue)
				// Truncate if too long
				if len(issueLine) > boxWidth-3 {
					issueLine = issueLine[:boxWidth-6] + "..."
				}
				fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, issueLine, vertical)
			}
		}

		// Termination countdown section
		if gang.TerminationIn != nil && !gang.IsHealthy {
			fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, "", vertical)
			delayLine := fmt.Sprintf("TerminationDelay: %s (configured)", formatDuration(dashboard.TerminationDelay))
			fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, delayLine, vertical)

			// Progress bar
			progressBar := renderProgressBar(*gang.TerminationIn, dashboard.TerminationDelay, 20)
			percent := 100.0
			if dashboard.TerminationDelay > 0 {
				percent = float64(*gang.TerminationIn) / float64(dashboard.TerminationDelay) * 100
			}
			timeLine := fmt.Sprintf("Time Remaining: %s %s %.0f%%", formatDuration(*gang.TerminationIn), progressBar, percent)
			fmt.Printf("%s %-*s %s\n", vertical, boxWidth-2, timeLine, vertical)
		}

		// Gang footer
		fmt.Printf("%s%s%s\n", bottomLeft, strings.Repeat(horizontal, boxWidth), bottomRight)
		fmt.Println()
	}

	// Summary
	fmt.Printf("Summary: %d/%d gangs healthy", dashboard.HealthyCount, dashboard.TotalCount)
	if dashboard.AtRiskCount > 0 {
		fmt.Printf(" | %d gang at risk of termination", dashboard.AtRiskCount)
	}
	fmt.Println()
}

// renderProgressBar creates an ASCII progress bar
func renderProgressBar(remaining, total time.Duration, width int) string {
	if total == 0 {
		return strings.Repeat(" ", width)
	}

	ratio := float64(remaining) / float64(total)
	if ratio > 1 {
		ratio = 1
	}
	if ratio < 0 {
		ratio = 0
	}

	filled := int(ratio * float64(width))
	empty := width - filled

	return strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", empty)
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	if d < 0 {
		return "0s"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// isOwnedBy checks if the owner references contain a specific owner
func isOwnedBy(ownerRefs []metav1.OwnerReference, name, kind string) bool {
	for _, ref := range ownerRefs {
		if ref.Name == name && ref.Kind == kind {
			return true
		}
	}
	return false
}
