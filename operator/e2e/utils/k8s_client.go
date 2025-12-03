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

package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// AppliedResource holds information about an applied Kubernetes resource
type AppliedResource struct {
	Name      string
	Namespace string
	GVK       schema.GroupVersionKind
	GVR       schema.GroupVersionResource
}

// ApplyYAMLFile applies a YAML file containing Kubernetes resources
// namespace parameter is optional - pass empty string to use namespace from YAML
func ApplyYAMLFile(ctx context.Context, yamlFilePath string, namespace string, restConfig *rest.Config, logger *Logger) ([]AppliedResource, error) {
	logger.Debugf("📄 Applying resources from %s...\n", yamlFilePath)

	// Read the YAML file
	yamlData, err := os.ReadFile(yamlFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read YAML file %s: %w", yamlFilePath, err)
	}

	return applyYAMLData(ctx, yamlData, namespace, restConfig, logger)
}

// WaitForPods waits for pods to be ready in the specified namespaces
// labelSelector is optional (pass empty string for all pods), timeout of 0 defaults to 5 minutes, interval of 0 defaults to 5 seconds
// expectedCount is the expected number of pods (pass 0 to skip count validation)
func WaitForPods(ctx context.Context, restConfig *rest.Config, namespaces []string, labelSelector string, expectedCount int, timeout time.Duration, interval time.Duration, logger *Logger) error {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create clientset: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// If no namespaces specified, use default
	if len(namespaces) == 0 {
		namespaces = []string{"default"}
	}

	logger.Debugf("⏳ Waiting for pods to be ready in namespaces: %v", namespaces)

	return wait.PollUntilContextTimeout(timeoutCtx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		allReady := true
		totalPods := 0
		readyPods := 0

		for _, namespace := range namespaces {
			pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: labelSelector,
			})
			if err != nil {
				logger.Errorf("Failed to list pods in namespace %s: %v", namespace, err)
				return false, nil
			}

			for _, pod := range pods.Items {
				totalPods++
				if isPodReady(&pod) {
					readyPods++
				} else {
					allReady = false
				}
			}
		}

		if totalPods == 0 {
			logger.Debug("⏳ No pods found yet, resources may still be creating pods...")
			return false, nil
		}

		// Verify expected count if specified
		if expectedCount > 0 && totalPods != expectedCount {
			logger.Debugf("⏳ Expected %d pods but found %d pods", expectedCount, totalPods)
			return false, nil
		}

		if !allReady {
			logger.Debugf("⏳ Waiting for %d more pods to become ready...", totalPods-readyPods)
		}

		return allReady, nil
	})
}

// applyYAMLData is the common function that applies YAML data to Kubernetes
func applyYAMLData(ctx context.Context, yamlData []byte, namespace string, restConfig *rest.Config, logger *Logger) ([]AppliedResource, error) {
	dynamicClient, restMapper, err := createKubernetesClients(restConfig)
	if err != nil {
		return nil, err
	}

	decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(string(yamlData)), 4096)
	var appliedResources []AppliedResource

	for {
		unstructuredObj, gvk, err := decodeNextYAMLObject(decoder)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if unstructuredObj == nil {
			continue // Skip empty objects
		}

		// Apply the resource
		appliedResource, err := applyResource(ctx, dynamicClient, restMapper, unstructuredObj, gvk, namespace)
		if err != nil {
			return nil, err
		}

		appliedResources = append(appliedResources, *appliedResource)
	}

	logger.Debugf("📋 Applied %d resources successfully", len(appliedResources))
	return appliedResources, nil
}

// createKubernetesClients creates the dynamic client and REST mapper
func createKubernetesClients(restConfig *rest.Config) (dynamic.Interface, meta.RESTMapper, error) {
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create discovery client: %w", err)
	}
	cachedDiscoveryClient := memory.NewMemCacheClient(discoveryClient)
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscoveryClient)

	return dynamicClient, restMapper, nil
}

// decodeNextYAMLObject decodes the next YAML object from the decoder
func decodeNextYAMLObject(decoder *yamlutil.YAMLOrJSONDecoder) (*unstructured.Unstructured, *schema.GroupVersionKind, error) {
	var rawObj runtime.RawExtension
	if err := decoder.Decode(&rawObj); err != nil {
		return nil, nil, err
	}

	if len(rawObj.Raw) == 0 {
		return nil, nil, nil // Empty object
	}

	// Decode the object as unstructured
	yamlDecoder := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	obj, gvk, err := yamlDecoder.Decode(rawObj.Raw, nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode object: %w", err)
	}

	unstructuredObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, nil, fmt.Errorf("expected unstructured object, got %T", obj)
	}

	return unstructuredObj, gvk, nil
}

// applyResource applies a single Kubernetes resource
func applyResource(ctx context.Context, dynamicClient dynamic.Interface, restMapper meta.RESTMapper, obj *unstructured.Unstructured, gvk *schema.GroupVersionKind, namespace string) (*AppliedResource, error) {
	// Get resource mapping
	gvr, mapping, err := getResourceMapping(restMapper, gvk)
	if err != nil {
		return nil, err
	}

	// Handle namespace based on resource scope
	handleResourceNamespace(obj, mapping, namespace)

	// Apply the resource (create or update)
	result, err := createOrUpdateResource(ctx, dynamicClient, gvr, mapping, obj)
	if err != nil {
		return nil, fmt.Errorf("failed to apply %s %s: %w", gvk.Kind, obj.GetName(), err)
	}

	return &AppliedResource{
		Name:      result.GetName(),
		Namespace: result.GetNamespace(),
		GVK:       *gvk,
		GVR:       gvr,
	}, nil
}

// getResourceMapping gets the GVR and mapping for a resource
func getResourceMapping(restMapper meta.RESTMapper, gvk *schema.GroupVersionKind) (schema.GroupVersionResource, *meta.RESTMapping, error) {
	gvr, err := getGVRFromGVK(restMapper, *gvk)
	if err != nil {
		return schema.GroupVersionResource{}, nil, fmt.Errorf("failed to get GVR for %s: %w", gvk.String(), err)
	}

	mapping, err := restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, nil, fmt.Errorf("failed to get REST mapping for %s: %w", gvk.String(), err)
	}

	return gvr, mapping, nil
}

// handleResourceNamespace sets the appropriate namespace based on resource scope
func handleResourceNamespace(obj *unstructured.Unstructured, mapping *meta.RESTMapping, namespace string) {
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		// Namespaced resource
		if namespace != "" {
			obj.SetNamespace(namespace)
		}
		if obj.GetNamespace() == "" {
			obj.SetNamespace("default")
		}
	} else {
		// Cluster-scoped resource - clear any namespace
		obj.SetNamespace("")
	}
}

// createOrUpdateResource creates or updates a resource
func createOrUpdateResource(ctx context.Context, dynamicClient dynamic.Interface, gvr schema.GroupVersionResource, mapping *meta.RESTMapping, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	// Try to create first
	result, err := createResource(ctx, dynamicClient, gvr, mapping, obj)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			// Resource exists, try to update
			return updateResource(ctx, dynamicClient, gvr, mapping, obj)
		}
		return nil, err
	}
	return result, nil
}

// createResource creates a new resource
func createResource(ctx context.Context, dynamicClient dynamic.Interface, gvr schema.GroupVersionResource, mapping *meta.RESTMapping, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return dynamicClient.Resource(gvr).Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{})
	}
	return dynamicClient.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
}

// updateResource updates an existing resource
func updateResource(ctx context.Context, dynamicClient dynamic.Interface, gvr schema.GroupVersionResource, mapping *meta.RESTMapping, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return dynamicClient.Resource(gvr).Namespace(obj.GetNamespace()).Update(ctx, obj, metav1.UpdateOptions{})
	}
	return dynamicClient.Resource(gvr).Update(ctx, obj, metav1.UpdateOptions{})
}

// WaitForPodsInNamespace waits for all pods in a namespace to be ready
// expectedCount is the expected number of pods (pass 0 to skip count validation)
func WaitForPodsInNamespace(ctx context.Context, namespace string, restConfig *rest.Config, expectedCount int, timeout time.Duration, interval time.Duration, logger *Logger) error {
	return WaitForPods(ctx, restConfig, []string{namespace}, "", expectedCount, timeout, interval, logger)
}

// getGVRFromGVK converts a GroupVersionKind to GroupVersionResource using REST mapper
func getGVRFromGVK(restMapper meta.RESTMapper, gvk schema.GroupVersionKind) (schema.GroupVersionResource, error) {
	mapping, err := restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	return mapping.Resource, nil
}

// isPodReady checks if a pod is ready
func isPodReady(pod *v1.Pod) bool {
	// First check that the pod is in Running phase
	if pod.Status.Phase != v1.PodRunning {
		return false
	}
	// Then check that the Ready condition is true
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// SetNodeSchedulable a Kubernetes node to be unschedulable or schedulable
func SetNodeSchedulable(ctx context.Context, clientset kubernetes.Interface, nodeName string, schedulable bool) error {
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// NOTE: schedulable is the opposite of unschedulable in the node spec
	// so we invert it to make it more intuitive in the function parameters
	if node.Spec.Unschedulable == !schedulable {
		// Already in desired state
		return nil
	}

	// NOTE: schedulable is the opposite of unschedulable in the node spec
	// so we invert it to make it more intuitive in the function parameters
	node.Spec.Unschedulable = !schedulable
	_, err = clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update node %s: %w", nodeName, err)
	}
	return nil
}

// ListPods lists pods in a namespace with an optional label selector
func ListPods(ctx context.Context, clientset kubernetes.Interface, namespace string, labelSelector string) (*v1.PodList, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods in namespace %s: %w", namespace, err)
	}
	return pods, nil
}

// WaitForPodCount waits for a specific number of pods to be created with the given label selector
// Returns the pod list once the expected count is reached
func WaitForPodCount(ctx context.Context, clientset kubernetes.Interface, namespace string, labelSelector string, expectedCount int, timeout time.Duration, interval time.Duration) (*v1.PodList, error) {
	var pods *v1.PodList
	err := PollForCondition(ctx, timeout, interval, func() (bool, error) {
		var err error
		pods, err = ListPods(ctx, clientset, namespace, labelSelector)
		if err != nil {
			return false, err
		}
		return len(pods.Items) == expectedCount, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to wait for %d pods with selector %q in namespace %s: %w", expectedCount, labelSelector, namespace, err)
	}
	return pods, nil
}

// PodPhaseCount holds counts of pods by phase
type PodPhaseCount struct {
	Running int
	Pending int
	Failed  int
	Unknown int
	Total   int
}

// CountPodsByPhase counts pods by their phase and returns a PodPhaseCount
func CountPodsByPhase(pods *v1.PodList) PodPhaseCount {
	count := PodPhaseCount{
		Total: len(pods.Items),
	}
	for _, pod := range pods.Items {
		switch pod.Status.Phase {
		case v1.PodRunning:
			count.Running++
		case v1.PodPending:
			count.Pending++
		case v1.PodFailed:
			count.Failed++
		case v1.PodUnknown:
			count.Unknown++
		}
	}
	return count
}

// CountReadyPods counts the number of ready pods (Running + Ready condition)
func CountReadyPods(pods *v1.PodList) int {
	readyCount := 0
	for _, pod := range pods.Items {
		if isPodReady(&pod) {
			readyCount++
		}
	}
	return readyCount
}

// VerifyPodPhases verifies that the pod counts match the expected values
// Pass -1 for any count you want to skip verification
func VerifyPodPhases(pods *v1.PodList, expectedRunning, expectedPending int) error {
	count := CountPodsByPhase(pods)

	if expectedRunning >= 0 && count.Running != expectedRunning {
		return fmt.Errorf("expected %d running pods but found %d", expectedRunning, count.Running)
	}

	if expectedPending >= 0 && count.Pending != expectedPending {
		return fmt.Errorf("expected %d pending pods but found %d", expectedPending, count.Pending)
	}

	return nil
}

// WaitForPodCountAndPhases waits for pods to reach specific total count and phase counts
// Pass -1 for any count you want to skip verification
func WaitForPodCountAndPhases(ctx context.Context, clientset kubernetes.Interface, namespace, labelSelector string, expectedTotal, expectedRunning, expectedPending int, timeout, interval time.Duration) error {
	return PollForCondition(ctx, timeout, interval, func() (bool, error) {
		pods, err := ListPods(ctx, clientset, namespace, labelSelector)
		if err != nil {
			return false, err
		}

		count := CountPodsByPhase(pods)

		totalMatch := expectedTotal < 0 || count.Total == expectedTotal
		runningMatch := expectedRunning < 0 || count.Running == expectedRunning
		pendingMatch := expectedPending < 0 || count.Pending == expectedPending

		return totalMatch && runningMatch && pendingMatch, nil
	})
}

// ScalePodCliqueScalingGroup scales a PodCliqueScalingGroup to the specified replica count
// It waits for the PCSG to exist before scaling
func ScalePodCliqueScalingGroup(ctx context.Context, restConfig *rest.Config, namespace, name string, replicas int, timeout, interval time.Duration) error {
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return ScalePodCliqueScalingGroupWithClient(ctx, dynamicClient, namespace, name, replicas, timeout, interval)
}

// ScalePodCliqueScalingGroupWithClient scales a PodCliqueScalingGroup using an existing dynamic client
// It waits for the PCSG to exist before scaling
func ScalePodCliqueScalingGroupWithClient(ctx context.Context, dynamicClient dynamic.Interface, namespace, name string, replicas int, timeout, interval time.Duration) error {
	pcsgGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquescalinggroups"}

	// Wait for PCSG to exist
	err := PollForCondition(ctx, timeout, interval, func() (bool, error) {
		_, err := dynamicClient.Resource(pcsgGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to find PodCliqueScalingGroup %s: %w", name, err)
	}

	// Scale the PCSG
	return scaleCRD(ctx, dynamicClient, pcsgGVR, namespace, name, replicas)
}

// ScalePodCliqueSet scales a PodCliqueSet to the specified replica count
func ScalePodCliqueSet(ctx context.Context, restConfig *rest.Config, namespace, name string, replicas int) error {
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return ScalePodCliqueSetWithClient(ctx, dynamicClient, namespace, name, replicas)
}

// ScalePodCliqueSetWithClient scales a PodCliqueSet using an existing dynamic client
func ScalePodCliqueSetWithClient(ctx context.Context, dynamicClient dynamic.Interface, namespace, name string, replicas int) error {
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	return scaleCRD(ctx, dynamicClient, pcsGVR, namespace, name, replicas)
}

// scaleCRD is a helper function that patches the replicas field of a custom resource
func scaleCRD(ctx context.Context, dynamicClient dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string, replicas int) error {
	scalePatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": replicas,
		},
	}
	patchBytes, err := json.Marshal(scalePatch)
	if err != nil {
		return fmt.Errorf("failed to marshal scale patch: %w", err)
	}

	if _, err := dynamicClient.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("failed to scale %s %s: %w", gvr.Resource, name, err)
	}

	return nil
}
