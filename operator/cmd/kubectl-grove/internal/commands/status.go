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

// Package commands provides the CLI subcommands for kubectl-grove.
package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// StatusCmd represents the status command.
type StatusCmd struct {
	Name      string `arg:"" optional:"" help:"Name of the PodCliqueSet to show status for."`
	All       bool   `short:"a" help:"Show status for all PodCliqueSets in the namespace."`
	Namespace string `short:"n" help:"Namespace to query." default:"default"`
}

// GVRs for Grove CRDs
var (
	podCliqueSetGVR = schema.GroupVersionResource{
		Group:    "grove.io",
		Version:  "v1alpha1",
		Resource: "podcliquesets",
	}
	podCliqueGVR = schema.GroupVersionResource{
		Group:    "grove.io",
		Version:  "v1alpha1",
		Resource: "podcliques",
	}
	podGangGVR = schema.GroupVersionResource{
		Group:    "scheduler.grove.io",
		Version:  "v1alpha1",
		Resource: "podgangs",
	}
)

// CliqueSummary holds formatted information about a PodClique.
type CliqueSummary struct {
	Name          string
	ReadyReplicas int32
	Replicas      int32
	MinAvailable  int32
}

// GangSummary holds formatted information about a PodGang.
type GangSummary struct {
	Name           string
	Phase          string
	PlacementScore *float64
}

// StatusOutput holds all the data for status display.
type StatusOutput struct {
	Namespace  string
	Name       string
	Age        time.Duration
	Cliques    []CliqueSummary
	Gangs      []GangSummary
	TotalReady int32
	TotalPods  int32
}

// Execute runs the status command with the provided dynamic client.
func (s *StatusCmd) Execute(dynamicClient dynamic.Interface) error {
	ctx := context.Background()

	if !s.All && s.Name == "" {
		return fmt.Errorf("either provide a PodCliqueSet name or use --all flag")
	}

	if s.All {
		return s.showAllStatus(ctx, dynamicClient)
	}

	return s.showStatus(ctx, dynamicClient, s.Name)
}

// showAllStatus shows status for all PodCliqueSets in the namespace.
func (s *StatusCmd) showAllStatus(ctx context.Context, dynamicClient dynamic.Interface) error {
	list, err := dynamicClient.Resource(podCliqueSetGVR).Namespace(s.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list PodCliqueSets: %w", err)
	}

	if len(list.Items) == 0 {
		fmt.Printf("No PodCliqueSets found in namespace %s\n", s.Namespace)
		return nil
	}

	for i, item := range list.Items {
		if i > 0 {
			fmt.Println("\n" + strings.Repeat("-", 60) + "\n")
		}
		if err := s.showStatusForUnstructured(ctx, dynamicClient, &item); err != nil {
			fmt.Printf("Error getting status for %s: %v\n", item.GetName(), err)
		}
	}

	return nil
}

// showStatus shows status for a single PodCliqueSet.
func (s *StatusCmd) showStatus(ctx context.Context, dynamicClient dynamic.Interface, name string) error {
	obj, err := dynamicClient.Resource(podCliqueSetGVR).Namespace(s.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get PodCliqueSet %s: %w", name, err)
	}

	return s.showStatusForUnstructured(ctx, dynamicClient, obj)
}

// showStatusForUnstructured displays status for a PodCliqueSet from unstructured data.
func (s *StatusCmd) showStatusForUnstructured(ctx context.Context, dynamicClient dynamic.Interface, obj *unstructured.Unstructured) error {
	// Convert to typed PodCliqueSet
	var pcs corev1alpha1.PodCliqueSet
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pcs)
	if err != nil {
		return fmt.Errorf("failed to convert PodCliqueSet: %w", err)
	}

	// Fetch related PodCliques
	cliques, err := s.fetchPodCliques(ctx, dynamicClient, pcs.Name)
	if err != nil {
		return fmt.Errorf("failed to fetch PodCliques: %w", err)
	}

	// Fetch related PodGangs
	gangs, err := s.fetchPodGangs(ctx, dynamicClient, pcs.Name)
	if err != nil {
		return fmt.Errorf("failed to fetch PodGangs: %w", err)
	}

	// Build and display output
	output := s.buildStatusOutput(&pcs, cliques, gangs)
	s.printStatus(output)

	return nil
}

// fetchPodCliques fetches PodCliques owned by a PodCliqueSet.
func (s *StatusCmd) fetchPodCliques(ctx context.Context, dynamicClient dynamic.Interface, pcsName string) ([]corev1alpha1.PodClique, error) {
	list, err := dynamicClient.Resource(podCliqueGVR).Namespace(s.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("grove.io/podcliqueset=%s", pcsName),
	})
	if err != nil {
		return nil, err
	}

	var cliques []corev1alpha1.PodClique
	for _, item := range list.Items {
		var clique corev1alpha1.PodClique
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &clique); err != nil {
			continue
		}
		cliques = append(cliques, clique)
	}

	return cliques, nil
}

// fetchPodGangs fetches PodGangs owned by a PodCliqueSet.
func (s *StatusCmd) fetchPodGangs(ctx context.Context, dynamicClient dynamic.Interface, pcsName string) ([]schedulerv1alpha1.PodGang, error) {
	list, err := dynamicClient.Resource(podGangGVR).Namespace(s.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("grove.io/podcliqueset=%s", pcsName),
	})
	if err != nil {
		return nil, err
	}

	var gangs []schedulerv1alpha1.PodGang
	for _, item := range list.Items {
		var gang schedulerv1alpha1.PodGang
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &gang); err != nil {
			continue
		}
		gangs = append(gangs, gang)
	}

	return gangs, nil
}

// buildStatusOutput builds the StatusOutput from the fetched resources.
func (s *StatusCmd) buildStatusOutput(pcs *corev1alpha1.PodCliqueSet, cliques []corev1alpha1.PodClique, gangs []schedulerv1alpha1.PodGang) *StatusOutput {
	output := &StatusOutput{
		Namespace: pcs.Namespace,
		Name:      pcs.Name,
		Age:       time.Since(pcs.CreationTimestamp.Time),
	}

	// Build clique summaries
	for _, clique := range cliques {
		minAvailable := clique.Spec.Replicas
		if clique.Spec.MinAvailable != nil {
			minAvailable = *clique.Spec.MinAvailable
		}

		output.Cliques = append(output.Cliques, CliqueSummary{
			Name:          clique.Spec.RoleName,
			ReadyReplicas: clique.Status.ReadyReplicas,
			Replicas:      clique.Spec.Replicas,
			MinAvailable:  minAvailable,
		})

		output.TotalReady += clique.Status.ReadyReplicas
		output.TotalPods += clique.Spec.Replicas
	}

	// Build gang summaries
	for _, gang := range gangs {
		output.Gangs = append(output.Gangs, GangSummary{
			Name:           gang.Name,
			Phase:          string(gang.Status.Phase),
			PlacementScore: gang.Status.PlacementScore,
		})
	}

	return output
}

// printStatus prints the formatted status output.
func (s *StatusCmd) printStatus(output *StatusOutput) {
	// Resource Overview
	fmt.Println("Resource Overview")
	fmt.Printf("  Namespace: %s\n", output.Namespace)
	fmt.Printf("  Name:      %s\n", output.Name)
	fmt.Printf("  Age:       %s\n", FormatDuration(output.Age))
	fmt.Println()

	// Clique Statuses
	if len(output.Cliques) > 0 {
		fmt.Println("Clique Statuses")
		for _, clique := range output.Cliques {
			percentage := float64(0)
			if clique.Replicas > 0 {
				percentage = float64(clique.ReadyReplicas) / float64(clique.Replicas) * 100
			}
			bar := RenderProgressBar(percentage, 16)
			fmt.Printf("%-12s %d/%d     (min: %d)    [%s] %.0f%%\n",
				clique.Name,
				clique.ReadyReplicas,
				clique.Replicas,
				clique.MinAvailable,
				bar,
				percentage,
			)
		}
		fmt.Println()
	}

	// PodGang Status
	if len(output.Gangs) > 0 {
		fmt.Println("PodGang Status")
		for _, gang := range output.Gangs {
			scoreStr := "N/A"
			scoreBar := ""
			if gang.PlacementScore != nil {
				scoreStr = fmt.Sprintf("%.2f", *gang.PlacementScore)
				scoreBar = RenderPlacementScoreBar(*gang.PlacementScore, 10)
			}
			fmt.Printf("%-20s %-10s PlacementScore: %s %s\n",
				gang.Name,
				gang.Phase,
				scoreStr,
				scoreBar,
			)
		}
		fmt.Println()
	}

	// Summary
	runningGangs := 0
	for _, gang := range output.Gangs {
		if gang.Phase == string(schedulerv1alpha1.PodGangPhaseRunning) {
			runningGangs++
		}
	}

	fmt.Printf("Summary: %d cliques | %d/%d Ready | %d/%d Gangs Running\n",
		len(output.Cliques),
		output.TotalReady,
		output.TotalPods,
		runningGangs,
		len(output.Gangs),
	)
}

// FormatDuration formats a duration in a human-readable way.
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		if minutes > 0 {
			return fmt.Sprintf("%dh%dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if hours > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	return fmt.Sprintf("%dd", days)
}

// RenderProgressBar renders a progress bar for a given percentage.
func RenderProgressBar(percentage float64, width int) string {
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 100 {
		percentage = 100
	}

	filled := int(percentage / 100 * float64(width))
	if filled > width {
		filled = width
	}

	// Use block characters for the progress bar
	bar := strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", width-filled)
	return bar
}

// RenderPlacementScoreBar renders a visual bar for placement score (0.0 to 1.0).
func RenderPlacementScoreBar(score float64, width int) string {
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}

	filled := int(score * float64(width))
	partial := score*float64(width) - float64(filled)

	var bar string
	bar = strings.Repeat("\u2588", filled)

	// Add partial block if there's remaining space
	if filled < width {
		if partial > 0.5 {
			bar += "\u2593" // medium shade
		} else if partial > 0 {
			bar += "\u2592" // light shade
		}
		remaining := width - len([]rune(bar))
		if remaining > 0 {
			bar += strings.Repeat("\u2591", remaining)
		}
	}

	return bar
}
