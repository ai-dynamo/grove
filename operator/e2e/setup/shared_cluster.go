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

package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/ai-dynamo/grove/operator/internal/utils/ioutil"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// cleanupTimeout is the maximum time to wait for all resources and pods to be deleted during cleanup.
	// This needs to be long enough to allow for cascade deletion propagation through
	// PodCliqueSet -> PodCliqueScalingGroup -> PodClique -> Pod
	cleanupTimeout = 2 * time.Minute

	// cleanupPollInterval is the interval between checks during cleanup polling
	cleanupPollInterval = 1 * time.Second

	// Default registry port for local development clusters (e.g., k3d with local registry)
	defaultRegistryPort = "5001"

	// Environment variables for cluster configuration.
	// The cluster must be created beforehand with Grove operator and Kai scheduler deployed.
	// For local development with k3d, use: ./operator/hack/infra-manager.py

	// EnvRegistryPort specifies the container registry port for test images (optional)
	EnvRegistryPort = "E2E_REGISTRY_PORT"
)

// resourceType represents a Kubernetes resource type for cleanup operations
type resourceType struct {
	group    string
	version  string
	resource string
	name     string
}

// groveManagedResourceTypes defines all resource types managed by Grove operator that need to be tracked for cleanup
var groveManagedResourceTypes = []resourceType{
	// Grove CRDs
	{"grove.io", "v1alpha1", "podcliquesets", "PodCliqueSets"},
	{"grove.io", "v1alpha1", "podcliquescalinggroups", "PodCliqueScalingGroups"},
	{"scheduler.grove.io", "v1alpha1", "podgangs", "PodGangs"},
	{"grove.io", "v1alpha1", "podcliques", "PodCliques"},
	// Kubernetes core resources
	{"", "v1", "services", "Services"},
	{"", "v1", "serviceaccounts", "ServiceAccounts"},
	{"", "v1", "secrets", "Secrets"},
	// RBAC resources
	{"rbac.authorization.k8s.io", "v1", "roles", "Roles"},
	{"rbac.authorization.k8s.io", "v1", "rolebindings", "RoleBindings"},
	// Autoscaling resources
	{"autoscaling", "v2", "horizontalpodautoscalers", "HorizontalPodAutoscalers"},
}

// SharedClusterManager manages a shared Kubernetes cluster for E2E tests.
// It connects to an existing cluster via kubeconfig and manages test lifecycle operations
// like workload cleanup and node cordoning.
type SharedClusterManager struct {
	clientset     *kubernetes.Clientset
	restConfig    *rest.Config
	dynamicClient dynamic.Interface
	cleanup       func()
	logger        *utils.Logger
	isSetup       bool
	workerNodes   []string
	kwokNodes     []string
	registryPort  string
	cleanupFailed bool   // Set to true if CleanupWorkloads fails, causing subsequent tests to fail
	cleanupError  string // The error message from the failed cleanup
}

var (
	sharedCluster *SharedClusterManager
	once          sync.Once
)

// SharedCluster returns the singleton shared cluster manager
func SharedCluster(logger *utils.Logger) *SharedClusterManager {
	once.Do(func() {
		sharedCluster = &SharedClusterManager{
			logger: logger,
		}
	})
	return sharedCluster
}

// Setup initializes the shared cluster by connecting to an existing Kubernetes cluster.
//
// The cluster must be created beforehand with:
//   - Grove operator deployed and ready
//   - Kai scheduler deployed and ready
//   - Required node labels and topology configuration
//
// For local development with k3d:
//
//	./operator/hack/infra-manager.py
//
// Optional environment variables:
//   - E2E_REGISTRY_PORT (default: 5001, for pushing test images to local registry)
//
// The cluster connection is established via standard kubeconfig resolution:
//   - KUBECONFIG environment variable, or
//   - ~/.kube/config, or
//   - In-cluster config
func (scm *SharedClusterManager) Setup(ctx context.Context, testImages []string) error {
	if scm.isSetup {
		return nil
	}

	return scm.connectToCluster(ctx, testImages)
}

// connectToCluster connects to an existing Kubernetes cluster using standard kubeconfig resolution.
// It expects the cluster to already have Grove operator, Kai scheduler, etc. installed.
func (scm *SharedClusterManager) connectToCluster(ctx context.Context, testImages []string) error {
	// Get registry port from env or use default
	registryPort := os.Getenv(EnvRegistryPort)
	if registryPort == "" {
		registryPort = defaultRegistryPort
	}
	scm.registryPort = registryPort

	scm.logger.Info("🔗 Connecting to existing Kubernetes cluster...")

	// Get REST config using standard kubeconfig resolution
	restConfig, err := getRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get cluster config: %w", err)
	}
	scm.restConfig = restConfig

	// Create clientset from restConfig
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create clientset: %w", err)
	}
	scm.clientset = clientset

	// Create dynamic client from restConfig
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}
	scm.dynamicClient = dynamicClient

	// Setup test images in registry (if registry port is configured)
	if scm.registryPort != "" && len(testImages) > 0 {
		if err := SetupRegistryTestImages(scm.registryPort, testImages); err != nil {
			return fmt.Errorf("failed to setup registry test images: %w", err)
		}
	}

	// Get list of worker nodes for cordoning management
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	scm.workerNodes = make([]string, 0)
	scm.kwokNodes = make([]string, 0)
	for _, node := range nodes.Items {
		if _, isServer := node.Labels["node-role.kubernetes.io/control-plane"]; isServer {
			continue
		}
		if node.Annotations["kwok.x-k8s.io/node"] == "fake" {
			scm.kwokNodes = append(scm.kwokNodes, node.Name)
			continue
		}
		scm.workerNodes = append(scm.workerNodes, node.Name)
	}

	// Start node monitoring to handle unhealthy k3d nodes during test execution.
	// This is critical for k3d clusters where nodes can become NotReady due to resource pressure.
	// The monitoring automatically detects and replaces unhealthy nodes to prevent test flakiness.
	nodeMonitoringCleanup := StartNodeMonitoring(ctx, clientset, scm.logger)

	// Cleanup function stops node monitoring - we don't delete the cluster when tests finish
	scm.cleanup = func() {
		nodeMonitoringCleanup()
		scm.logger.Info("ℹ️  Test run complete - cluster preserved for inspection or reuse")
	}

	scm.logger.Infof("✅ Connected to cluster with %d worker nodes (%d KWOK simulated)",
		len(scm.workerNodes), len(scm.kwokNodes))
	scm.isSetup = true
	return nil
}

// PrepareForTest prepares the cluster for a specific test by cordoning the appropriate nodes.
// It ensures exactly `requiredWorkerNodes` nodes are schedulable by cordoning excess nodes.
// Returns an error if a previous cleanup operation failed, preventing potentially corrupted test state.
func (scm *SharedClusterManager) PrepareForTest(ctx context.Context, requiredWorkerNodes int) error {
	if scm.cleanupFailed {
		return fmt.Errorf("cannot prepare cluster: a previous test cleanup failed - cluster may have orphaned resources. Original error: %s", scm.cleanupError)
	}

	if !scm.isSetup {
		return fmt.Errorf("shared cluster not setup")
	}

	totalWorkerNodes := len(scm.workerNodes)
	if requiredWorkerNodes > totalWorkerNodes {
		return fmt.Errorf("required worker nodes (%d) is greater than the number of worker nodes in the cluster (%d)", requiredWorkerNodes, totalWorkerNodes)
	}

	if requiredWorkerNodes < totalWorkerNodes {
		// Cordon nodes that are not needed for this test
		nodesToCordon := scm.workerNodes[requiredWorkerNodes:]
		scm.logger.Debugf("🔧 Preparing cluster: keeping %d nodes schedulable, cordoning %d nodes", requiredWorkerNodes, len(nodesToCordon))

		for i, nodeName := range nodesToCordon {
			scm.logger.Debugf("  Cordoning node %d/%d: %s", i+1, len(nodesToCordon), nodeName)
			if err := utils.SetNodeSchedulable(ctx, scm.clientset, nodeName, false); err != nil {
				scm.logger.Errorf("Failed to cordon node %s (attempt to cordon node %d/%d): %v", nodeName, i+1, len(nodesToCordon), err)
				return fmt.Errorf("failed to cordon node %s: %w", nodeName, err)
			}
		}
		scm.logger.Debugf("✅ Successfully cordoned %d nodes", len(nodesToCordon))
	} else {
		scm.logger.Debugf("🔧 Preparing cluster: all %d worker nodes will be schedulable", requiredWorkerNodes)
	}

	return nil
}

// CleanupWorkloads removes all test workloads from the cluster
func (scm *SharedClusterManager) CleanupWorkloads(ctx context.Context) error {
	if !scm.isSetup {
		return nil
	}

	scm.logger.Info("🧹 Cleaning up workloads from shared cluster...")

	// Step 1: Delete PodCliqueSets first (should cascade delete other resources)
	if err := scm.deleteAllResources(ctx, "grove.io", "v1alpha1", "podcliquesets"); err != nil {
		scm.logger.Warnf("failed to delete PodCliqueSets: %v", err)
	}

	// Step 2: Poll for all resources and pods to be cleaned up
	scm.logger.Infof("⏳ Waiting up to %v for cascade deletion to complete...", cleanupTimeout)
	if err := scm.waitForAllGroveManagedResourcesAndPodsDeleted(ctx, cleanupTimeout, cleanupPollInterval); err != nil {
		// List remaining resources and pods for debugging
		scm.listRemainingGroveManagedResources(ctx)
		scm.listRemainingPods(ctx, "default")
		return fmt.Errorf("failed to delete all resources and pods: %w", err)
	}

	// Step 3: Reset node cordoning state
	if err := scm.resetNodeStates(ctx); err != nil {
		scm.logger.Warnf("failed to reset node states: %v", err)
	}

	return nil
}

// deleteAllResources deletes all resources of a specific type across all namespaces
func (scm *SharedClusterManager) deleteAllResources(ctx context.Context, group, version, resource string) error {
	gvr := schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}

	// List all resources across all namespaces
	resourceList, err := scm.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list %s: %w", resource, err)
	}

	if len(resourceList.Items) == 0 {
		scm.logger.Debugf("No %s found to delete", resource)
		return nil
	}

	scm.logger.Infof("🗑️ Deleting %d %s...", len(resourceList.Items), resource)

	// Delete each resource
	for _, item := range resourceList.Items {
		namespace := item.GetNamespace()
		name := item.GetName()

		scm.logger.Debugf("Deleting %s %s/%s", resource, namespace, name)
		err := scm.dynamicClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil {
			scm.logger.Warnf("failed to delete %s %s/%s: %v", resource, namespace, name, err)
		} else {
			scm.logger.Debugf("✓ Delete request sent for %s %s/%s", resource, namespace, name)
		}
	}

	return nil
}

// isSystemPod checks if a pod is a system pod that should be ignored during cleanup
func isSystemPod(pod *v1.Pod) bool {
	// Skip pods managed by DaemonSets or system namespaces
	if pod.Namespace == "kube-system" || pod.Namespace == OperatorNamespace {
		return true
	}

	// Skip pods with system owner references
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" {
			return true
		}
	}

	return false
}

// listRemainingPods lists remaining pods for debugging
func (scm *SharedClusterManager) listRemainingPods(ctx context.Context, namespace string) {
	pods, err := scm.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		scm.logger.Errorf("Failed to list remaining pods: %v", err)
		return
	}

	nonSystemPods := []string{}
	for _, pod := range pods.Items {
		if !isSystemPod(&pod) {
			nonSystemPods = append(nonSystemPods, fmt.Sprintf("%s (Phase: %s)", pod.Name, pod.Status.Phase))
		}
	}

	if len(nonSystemPods) > 0 {
		scm.logger.Errorf("Remaining non-system pods: %v", nonSystemPods)
	}
}

// resetNodeStates uncordons all worker nodes to reset cluster state for the next test
func (scm *SharedClusterManager) resetNodeStates(ctx context.Context) error {
	scm.logger.Debugf("🔄 Resetting node states: uncordoning %d worker nodes", len(scm.workerNodes))

	for i, nodeName := range scm.workerNodes {
		if err := utils.SetNodeSchedulable(ctx, scm.clientset, nodeName, true); err != nil {
			scm.logger.Errorf("Failed to uncordon node %s (node %d/%d): %v", nodeName, i+1, len(scm.workerNodes), err)
			return fmt.Errorf("failed to uncordon node %s: %w", nodeName, err)
		}
	}

	scm.logger.Debugf("✅ Successfully reset all %d worker nodes to schedulable", len(scm.workerNodes))
	return nil
}

// waitForAllGroveManagedResourcesAndPodsDeleted waits for all Grove resources and pods to be deleted
func (scm *SharedClusterManager) waitForAllGroveManagedResourcesAndPodsDeleted(ctx context.Context, timeout time.Duration, interval time.Duration) error {
	startTime := time.Now()
	lastLogTime := startTime
	logInterval := 30 * time.Second // Log progress every 30 seconds

	return utils.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		allResourcesDeleted := true
		totalResources := 0
		var resourceDetails []string

		// Create label selector for Grove-managed resources
		labelSelector := fmt.Sprintf("%s=%s", common.LabelManagedByKey, common.LabelManagedByValue)

		// Check Grove resources
		for _, rt := range groveManagedResourceTypes {
			gvr := schema.GroupVersionResource{
				Group:    rt.group,
				Version:  rt.version,
				Resource: rt.resource,
			}

			// PodCliqueSets are user-created top-level resources and don't have the managed-by label,
			// so we need to check for them without the label selector
			listOptions := metav1.ListOptions{
				LabelSelector: labelSelector,
			}
			if rt.resource == "podcliquesets" {
				listOptions = metav1.ListOptions{}
			}

			resourceList, err := scm.dynamicClient.Resource(gvr).List(ctx, listOptions)
			if err != nil {
				// If we can't list the resource type, assume it doesn't exist or is being deleted
				continue
			}

			if len(resourceList.Items) > 0 {
				allResourcesDeleted = false
				totalResources += len(resourceList.Items)
				for _, item := range resourceList.Items {
					deletionTS := item.GetDeletionTimestamp()
					if deletionTS != nil {
						resourceDetails = append(resourceDetails, fmt.Sprintf("%s/%s (deleting since %v)", item.GetNamespace(), item.GetName(), time.Since(deletionTS.Time).Round(time.Second)))
					} else {
						resourceDetails = append(resourceDetails, fmt.Sprintf("%s/%s (NOT marked for deletion!)", item.GetNamespace(), item.GetName()))
					}
				}
			}
		}

		// Check pods
		allPodsDeleted := true
		nonSystemPods := 0
		pods, err := scm.clientset.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
		if err == nil {
			for _, pod := range pods.Items {
				if !isSystemPod(&pod) {
					allPodsDeleted = false
					nonSystemPods++
				}
			}
		}

		if allResourcesDeleted && allPodsDeleted {
			scm.logger.Infof("✅ All resources deleted after %v", time.Since(startTime).Round(time.Second))
			return true, nil
		}

		// Log progress at intervals for visibility
		now := time.Now()
		if now.Sub(lastLogTime) >= logInterval {
			lastLogTime = now
			elapsed := now.Sub(startTime).Round(time.Second)
			scm.logger.Infof("⏳ [%v elapsed] Waiting for %d resources and %d pods. Details: %v", elapsed, totalResources, nonSystemPods, resourceDetails)
		}

		return false, nil
	})
}

// listRemainingGroveManagedResources lists remaining Grove resources for debugging
func (scm *SharedClusterManager) listRemainingGroveManagedResources(ctx context.Context) {
	// Create label selector for Grove-managed resources
	labelSelector := fmt.Sprintf("%s=%s", common.LabelManagedByKey, common.LabelManagedByValue)

	for _, rt := range groveManagedResourceTypes {
		gvr := schema.GroupVersionResource{
			Group:    rt.group,
			Version:  rt.version,
			Resource: rt.resource,
		}

		// PodCliqueSets are user-created top-level resources and don't have the managed-by label
		listOptions := metav1.ListOptions{
			LabelSelector: labelSelector,
		}
		if rt.resource == "podcliquesets" {
			listOptions = metav1.ListOptions{}
		}

		resourceList, err := scm.dynamicClient.Resource(gvr).List(ctx, listOptions)
		if err != nil {
			scm.logger.Errorf("Failed to list %s: %v", rt.name, err)
			continue
		}

		if len(resourceList.Items) > 0 {
			resourceNames := make([]string, 0, len(resourceList.Items))
			for _, item := range resourceList.Items {
				resourceNames = append(resourceNames, fmt.Sprintf("%s/%s", item.GetNamespace(), item.GetName()))
			}
			scm.logger.Errorf("Remaining %s: %v", rt.name, resourceNames)
		}
	}
}

// GetClients returns the kubernetes clients for tests to use
func (scm *SharedClusterManager) GetClients() (*kubernetes.Clientset, *rest.Config, dynamic.Interface) {
	return scm.clientset, scm.restConfig, scm.dynamicClient
}

// GetRegistryPort returns the registry port for test image setup
func (scm *SharedClusterManager) GetRegistryPort() string {
	return scm.registryPort
}

// GetWorkerNodes returns the list of worker node names
func (scm *SharedClusterManager) GetWorkerNodes() []string {
	return scm.workerNodes
}

// IsSetup returns whether the shared cluster is setup
func (scm *SharedClusterManager) IsSetup() bool {
	return scm.isSetup
}

// MarkCleanupFailed marks that a cleanup operation has failed.
// This causes all subsequent tests to fail immediately when they try to prepare the cluster.
func (scm *SharedClusterManager) MarkCleanupFailed(err error) {
	scm.cleanupFailed = true
	scm.cleanupError = err.Error()
}

// HasCleanupFailed returns true if a previous cleanup operation failed.
func (scm *SharedClusterManager) HasCleanupFailed() bool {
	return scm.cleanupFailed
}

// GetCleanupError returns the error message from the failed cleanup, or empty string if no failure.
func (scm *SharedClusterManager) GetCleanupError() string {
	return scm.cleanupError
}

// Teardown cleans up the shared cluster
func (scm *SharedClusterManager) Teardown() {
	if scm.cleanup != nil {
		scm.cleanup()
		scm.isSetup = false
	}
}

// SetupRegistryTestImages pulls images and pushes them to a local container registry.
// This is used for test images that need to be available inside the cluster.
// The registry is expected to be accessible at localhost:<registryPort>.
func SetupRegistryTestImages(registryPort string, images []string) error {
	if len(images) == 0 {
		return nil
	}

	ctx := context.Background()

	// Initialize Docker client
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer ioutil.CloseQuietly(cli)

	// Process each image
	for _, imageName := range images {
		registryImage := fmt.Sprintf("localhost:%s/%s", registryPort, imageName)

		// Step 1: Pull the image
		pullReader, err := cli.ImagePull(ctx, imageName, image.PullOptions{})
		if err != nil {
			return fmt.Errorf("failed to pull %s: %w", imageName, err)
		}

		// Consume the pull output to avoid blocking
		_, err = io.Copy(io.Discard, pullReader)
		ioutil.CloseQuietly(pullReader)
		if err != nil {
			return fmt.Errorf("failed to read pull output for %s: %w", imageName, err)
		}

		// Step 2: Tag the image for the local registry
		err = cli.ImageTag(ctx, imageName, registryImage)
		if err != nil {
			return fmt.Errorf("failed to tag image %s as %s: %w", imageName, registryImage, err)
		}

		// Step 3: Push the image to the local registry
		pushReader, err := cli.ImagePush(ctx, registryImage, image.PushOptions{})
		if err != nil {
			return fmt.Errorf("failed to push %s: %w", registryImage, err)
		}

		// Consume the push output to avoid blocking
		_, err = io.Copy(io.Discard, pushReader)
		ioutil.CloseQuietly(pushReader)
		if err != nil {
			return fmt.Errorf("failed to read push output for %s: %w", registryImage, err)
		}
	}

	return nil
}
