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
	"log"
	"sort"

	operatorv1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	schedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// ArboristClient provides Kubernetes client operations for the Arborist TUI.
type ArboristClient struct {
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface
	namespace     string
}

// NewArboristClient creates a new ArboristClient instance.
func NewArboristClient(clientset kubernetes.Interface, dynamicClient dynamic.Interface, namespace string) *ArboristClient {
	return &ArboristClient{
		clientset:     clientset,
		dynamicClient: dynamicClient,
		namespace:     namespace,
	}
}

// GetAllPodCliqueSets retrieves all PodCliqueSets in the configured namespace.
func (c *ArboristClient) GetAllPodCliqueSets(ctx context.Context) ([]operatorv1alpha1.PodCliqueSet, error) {
	list, err := c.dynamicClient.Resource(podCliqueSetGVR).Namespace(c.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list PodCliqueSets: %w", err)
	}

	var podCliqueSets []operatorv1alpha1.PodCliqueSet
	for _, item := range list.Items {
		var pcs operatorv1alpha1.PodCliqueSet
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &pcs); err != nil {
			log.Printf("Warning: failed to convert PodCliqueSet %s: %v", item.GetName(), err)
			continue
		}
		podCliqueSets = append(podCliqueSets, pcs)
	}

	// Sort by creation timestamp (newest first)
	sort.Slice(podCliqueSets, func(i, j int) bool {
		return podCliqueSets[i].CreationTimestamp.After(podCliqueSets[j].CreationTimestamp.Time)
	})

	return podCliqueSets, nil
}

// GetPodCliqueSetByName retrieves a specific PodCliqueSet by name.
func (c *ArboristClient) GetPodCliqueSetByName(ctx context.Context, name string) (*operatorv1alpha1.PodCliqueSet, error) {
	obj, err := c.dynamicClient.Resource(podCliqueSetGVR).Namespace(c.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get PodCliqueSet %s: %w", name, err)
	}

	var pcs operatorv1alpha1.PodCliqueSet
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pcs); err != nil {
		return nil, fmt.Errorf("failed to convert PodCliqueSet: %w", err)
	}

	return &pcs, nil
}

// GetPodCliquesForPodCliqueSet retrieves all PodCliques belonging to a PodCliqueSet.
func (c *ArboristClient) GetPodCliquesForPodCliqueSet(ctx context.Context, pcsName string) ([]operatorv1alpha1.PodClique, error) {
	list, err := c.dynamicClient.Resource(podCliqueGVR).Namespace(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/part-of=%s", pcsName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list PodCliques for PodCliqueSet %s: %w", pcsName, err)
	}

	var cliques []operatorv1alpha1.PodClique
	for _, item := range list.Items {
		var clique operatorv1alpha1.PodClique
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &clique); err != nil {
			log.Printf("Warning: failed to convert PodClique %s: %v", item.GetName(), err)
			continue
		}
		cliques = append(cliques, clique)
	}

	// Sort by name for consistent ordering
	sort.Slice(cliques, func(i, j int) bool {
		return cliques[i].Name < cliques[j].Name
	})

	return cliques, nil
}

// GetPodGangsForPodCliqueSet retrieves all PodGangs belonging to a PodCliqueSet.
func (c *ArboristClient) GetPodGangsForPodCliqueSet(ctx context.Context, pcsName string) ([]schedulerv1alpha1.PodGang, error) {
	list, err := c.dynamicClient.Resource(podGangGVR).Namespace(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/part-of=%s", pcsName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list PodGangs for PodCliqueSet %s: %w", pcsName, err)
	}

	var gangs []schedulerv1alpha1.PodGang
	for _, item := range list.Items {
		var gang schedulerv1alpha1.PodGang
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &gang); err != nil {
			log.Printf("Warning: failed to convert PodGang %s: %v", item.GetName(), err)
			continue
		}
		gangs = append(gangs, gang)
	}

	// Sort by name for consistent ordering
	sort.Slice(gangs, func(i, j int) bool {
		return gangs[i].Name < gangs[j].Name
	})

	return gangs, nil
}

// GetPodsForPodClique retrieves all Pods belonging to a PodClique.
func (c *ArboristClient) GetPodsForPodClique(ctx context.Context, cliqueName string) ([]corev1.Pod, error) {
	list, err := c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("grove.io/podclique=%s", cliqueName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list Pods for PodClique %s: %w", cliqueName, err)
	}

	pods := list.Items

	// Sort by creation timestamp (newest first)
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].CreationTimestamp.After(pods[j].CreationTimestamp.Time)
	})

	return pods, nil
}

// GetPodYAML retrieves the YAML representation of a Pod.
func (c *ArboristClient) GetPodYAML(ctx context.Context, podName string) (string, error) {
	pod, err := c.clientset.CoreV1().Pods(c.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get Pod %s: %w", podName, err)
	}

	// Clear managed fields and other metadata that clutters the output
	pod.ManagedFields = nil

	yamlBytes, err := yaml.Marshal(pod)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Pod to YAML: %w", err)
	}

	return string(yamlBytes), nil
}

// GetEventsForResource retrieves events for a specific resource.
func (c *ArboristClient) GetEventsForResource(ctx context.Context, kind, name string) ([]corev1.Event, error) {
	// List all events in the namespace
	eventList, err := c.clientset.CoreV1().Events(c.namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=%s", name, kind),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list events for %s/%s: %w", kind, name, err)
	}

	events := eventList.Items

	// Sort events by last timestamp (most recent first)
	sort.Slice(events, func(i, j int) bool {
		timeI := events[i].LastTimestamp.Time
		timeJ := events[j].LastTimestamp.Time
		// If LastTimestamp is zero, use EventTime
		if timeI.IsZero() {
			timeI = events[i].EventTime.Time
		}
		if timeJ.IsZero() {
			timeJ = events[j].EventTime.Time
		}
		return timeI.After(timeJ)
	})

	return events, nil
}

// GetNodes retrieves all nodes in the cluster.
func (c *ArboristClient) GetNodes(ctx context.Context) ([]corev1.Node, error) {
	list, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	nodes := list.Items

	// Sort by name for consistent ordering
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	return nodes, nil
}
