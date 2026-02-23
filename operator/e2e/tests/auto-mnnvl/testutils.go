//go:build e2e

// /*
// Copyright 2026 The Grove Authors.
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

// Package automnnvl contains end-to-end tests for the Auto-MNNVL feature.
// These tests verify the automatic creation and management of ComputeDomain
// resources for GPU workloads when the MNNVL feature is enabled.
package automnnvl

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclient "github.com/ai-dynamo/grove/operator/client/clientset/versioned"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	"gopkg.in/yaml.v3"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

const (
	// groveOperatorNamespace is the namespace where the Grove operator is deployed
	groveOperatorNamespace = "grove-system"

	// groveConfigMapPrefix is the prefix for the Grove operator ConfigMap name
	groveConfigMapPrefix = "grove-operator-cm-"

	// Constants for skipUnless
	featureEnabled  = true
	featureDisabled = false
	crdSupported    = true
	crdUnsupported  = false
)

// clusterMNNVLConfig describes the cluster's MNNVL-related state for test configuration.
// This is used to determine which test suites should run based on the current cluster setup.
type clusterMNNVLConfig struct {
	// featureEnabled indicates whether autoMNNVLEnabled is true in the operator config
	featureEnabled bool
	// crdSupported indicates whether the ComputeDomain CRD is installed in the cluster
	crdSupported bool
}

// String returns a human-readable representation of the config
func (c clusterMNNVLConfig) String() string {
	return fmt.Sprintf("clusterMNNVLConfig{FeatureEnabled: %v, CRDSupported: %v}",
		c.featureEnabled, c.crdSupported)
}

// skipUnless skips the test if the cluster configuration doesn't match the required values.
// Usage: config.skipUnless(t, crdSupported, featureEnabled)
// Use the constants: crdSupported/crdUnsupported, featureEnabled/featureDisabled
func (c *clusterMNNVLConfig) skipUnless(t *testing.T, requiredCRDSupported, requiredFeatureEnabled bool) {
	t.Helper()
	if c.featureEnabled != requiredFeatureEnabled || c.crdSupported != requiredCRDSupported {
		t.Skipf("Skipping: requires featureEnabled=%v, crdSupported=%v (got %s)",
			requiredFeatureEnabled, requiredCRDSupported, c)
	}
}

var (
	// cachedConfig holds the detected cluster configuration (cached for performance)
	cachedConfig *clusterMNNVLConfig
	// configOnce ensures config detection only happens once per test run
	configOnce sync.Once
	// configErr holds any error from config detection
	configErr error

	// logger for the tests
	logger *utils.Logger
)

func init() {
	logger = utils.NewTestLogger(utils.InfoLevel)
}

// requireClusterConfig returns the cached cluster MNNVL configuration, detecting it on first call.
// This function is safe to call from multiple tests - detection only happens once.
// If detection fails, the test is marked as fatal.
func requireClusterConfig(t *testing.T, ctx context.Context, clientset kubernetes.Interface, restConfig *rest.Config) *clusterMNNVLConfig {
	t.Helper()

	configOnce.Do(func() {
		cachedConfig, configErr = detectClusterConfig(ctx, clientset, restConfig)
		if configErr == nil {
			logger.Infof("Detected cluster MNNVL config: %s", cachedConfig)
		}
	})

	if configErr != nil {
		t.Fatalf("Failed to detect cluster MNNVL config: %v", configErr)
	}

	return cachedConfig
}

// detectClusterConfig detects the MNNVL configuration from the cluster.
// It checks both the operator ConfigMap and the presence of the ComputeDomain CRD.
func detectClusterConfig(ctx context.Context, clientset kubernetes.Interface, restConfig *rest.Config) (*clusterMNNVLConfig, error) {
	config := &clusterMNNVLConfig{}

	// Detect if MNNVL feature is enabled in operator config
	featureEnabled, err := detectAutoMNNVLEnabled(ctx, clientset)
	if err != nil {
		return nil, fmt.Errorf("failed to detect feature enabled: %w", err)
	}
	config.featureEnabled = featureEnabled

	// Detect if ComputeDomain CRD exists
	crdSupported, err := detectComputeDomainCRDExists(ctx, restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to detect CRD supported: %w", err)
	}
	config.crdSupported = crdSupported

	return config, nil
}

// detectAutoMNNVLEnabled checks if autoMNNVLEnabled is true in the operator ConfigMap
func detectAutoMNNVLEnabled(ctx context.Context, clientset kubernetes.Interface) (bool, error) {
	// List ConfigMaps in grove-system namespace to find the operator config
	configMaps, err := clientset.CoreV1().ConfigMaps(groveOperatorNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to list ConfigMaps in %s: %w", groveOperatorNamespace, err)
	}

	// Find the ConfigMap with the grove-operator-cm- prefix
	for _, cm := range configMaps.Items {
		if strings.HasPrefix(cm.Name, groveConfigMapPrefix) {
			// Parse the config YAML
			configData, ok := cm.Data["config.yaml"]
			if !ok {
				// Try alternative key name
				for key, value := range cm.Data {
					if strings.HasSuffix(key, ".yaml") || key == "config" {
						configData = value
						break
					}
				}
			}

			if configData == "" {
				// Check if there's any data at all
				for _, value := range cm.Data {
					configData = value
					break
				}
			}

			if configData != "" {
				return parseAutoMNNVLEnabled(configData)
			}
		}
	}

	// If no ConfigMap found, assume feature is disabled (default)
	logger.Warn("Grove operator ConfigMap not found, assuming autoMNNVLEnabled=false")
	return false, nil
}

// parseAutoMNNVLEnabled parses the config YAML to find network.autoMNNVLEnabled
func parseAutoMNNVLEnabled(configData string) (bool, error) {
	var config struct {
		Network struct {
			AutoMNNVLEnabled bool `yaml:"autoMNNVLEnabled"`
		} `yaml:"network"`
	}

	if err := yaml.Unmarshal([]byte(configData), &config); err != nil {
		return false, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	return config.Network.AutoMNNVLEnabled, nil
}

// detectComputeDomainCRDExists checks if the ComputeDomain CRD exists in the cluster
func detectComputeDomainCRDExists(ctx context.Context, restConfig *rest.Config) (bool, error) {
	apiExtClient, err := apiextensionsclientset.NewForConfig(restConfig)
	if err != nil {
		return false, fmt.Errorf("failed to create API extensions client: %w", err)
	}

	_, err = apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Get(
		ctx, mnnvl.ComputeDomainCRDName, metav1.GetOptions{})
	if err != nil {
		// CRD not found - this is expected in some test scenarios
		return false, nil
	}

	return true, nil
}

// testContext holds common test parameters for MNNVL e2e tests
type testContext struct {
	t             *testing.T
	ctx           context.Context
	clientset     kubernetes.Interface
	groveClient   groveclient.Interface
	dynamicClient dynamic.Interface
	restConfig    *rest.Config
	namespace     string
	clusterConfig *clusterMNNVLConfig
}

// prepareTestCluster is a helper that prepares the cluster for a test.
// It returns the clients and a cleanup function.
func prepareTestCluster(ctx context.Context, t *testing.T, requiredWorkerNodes int) (kubernetes.Interface, *rest.Config, dynamic.Interface, groveclient.Interface, func()) {
	t.Helper()

	// Get the cluster instance
	sharedCluster := setup.SharedCluster(logger)

	// Prepare cluster with required worker nodes
	if err := sharedCluster.PrepareForTest(ctx, requiredWorkerNodes); err != nil {
		t.Fatalf("Failed to prepare cluster: %v", err)
	}

	// Get clients from cluster
	clientset, restConfig, dynamicClient := sharedCluster.GetClients()

	// Create Grove typed client
	groveClient, err := groveclient.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create Grove client: %v", err)
	}

	// Create cleanup function
	cleanup := func() {
		if err := sharedCluster.CleanupWorkloads(ctx); err != nil {
			t.Fatalf("Failed to cleanup workloads: %v", err)
		}
	}

	return clientset, restConfig, dynamicClient, groveClient, cleanup
}

// createTestContext creates a testContext with all necessary clients and configuration
func createTestContext(
	t *testing.T,
	ctx context.Context,
	clientset kubernetes.Interface,
	restConfig *rest.Config,
	dynamicClient dynamic.Interface,
	groveClient groveclient.Interface,
	clusterConfig *clusterMNNVLConfig,
) testContext {
	return testContext{
		t:             t,
		ctx:           ctx,
		clientset:     clientset,
		groveClient:   groveClient,
		dynamicClient: dynamicClient,
		restConfig:    restConfig,
		namespace:     "default",
		clusterConfig: clusterConfig,
	}
}

const (
	// defaultPollTimeout is the timeout for polling conditions
	defaultPollTimeout = 2 * time.Minute
	// defaultPollInterval is the interval between poll attempts
	defaultPollInterval = 2 * time.Second
)

// GVR for ComputeDomain (no typed client available)
var computeDomainGVR = schema.GroupVersionResource{
	Group:    mnnvl.ComputeDomainGroup,
	Version:  mnnvl.ComputeDomainVersion,
	Resource: mnnvl.ComputeDomainResource,
}

// buildGPUPCS builds a PCS with GPU requirements
func buildGPUPCS(name string, replicas int) *grovecorev1alpha1.PodCliqueSet {
	gpuClique := testutils.NewPodCliqueTemplateSpecBuilder("gpu-worker").
		WithRoleName("gpu-worker").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewGPUContainer("gpu-container", "nginx:alpine-slim", 1)).
		Build()

	return testutils.NewPodCliqueSetBuilder(name, "default", types.UID("")).
		WithReplicas(int32(replicas)).
		WithTerminationDelay(time.Second).
		WithPodCliqueTemplateSpec(gpuClique).
		Build()
}

// buildCPUOnlyPCS builds a PCS with CPU-only containers (no GPU)
func buildCPUOnlyPCS(name string, replicas int) *grovecorev1alpha1.PodCliqueSet {
	cpuClique := testutils.NewPodCliqueTemplateSpecBuilder("cpu-worker").
		WithRoleName("cpu-worker").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewContainer("cpu-container", "nginx:alpine-slim")).
		Build()

	return testutils.NewPodCliqueSetBuilder(name, "default", types.UID("")).
		WithReplicas(int32(replicas)).
		WithTerminationDelay(time.Second).
		WithPodCliqueTemplateSpec(cpuClique).
		Build()
}

// buildComprehensivePCS builds a PCS with multiple cliques and scaling groups for comprehensive testing.
// Names are kept short to satisfy the 45-character resource name limit.
// Structure:
//   - Standalone cliques (not in any PCSG):
//     1. "gpu1": 2 containers (1 GPU, 1 non-GPU)
//     2. "cpu1": 1 container (no GPU)
//   - PCSG "sg1" contains:
//     3. "gpu2": 2 containers (1 GPU, 1 non-GPU)
//     4. "cpu2": 1 container (no GPU)
//   - PCSG "sg2" contains:
//     5. "cpu3": 1 container (no GPU)
func buildComprehensivePCS(name string, replicas int) *grovecorev1alpha1.PodCliqueSet {
	// Standalone cliques
	standaloneGPUMixed := testutils.NewPodCliqueTemplateSpecBuilder("gpu1").
		WithRoleName("gpu1").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewGPUContainer("gpu", "nginx:alpine-slim", 1)).
		WithContainer(testutils.NewContainer("cpu", "nginx:alpine-slim")).
		Build()

	standaloneCPUOnly := testutils.NewPodCliqueTemplateSpecBuilder("cpu1").
		WithRoleName("cpu1").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewContainer("cpu", "nginx:alpine-slim")).
		Build()

	// PCSG sg1 cliques
	pcsg1GPUMixed := testutils.NewPodCliqueTemplateSpecBuilder("gpu2").
		WithRoleName("gpu2").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewGPUContainer("gpu", "nginx:alpine-slim", 1)).
		WithContainer(testutils.NewContainer("cpu", "nginx:alpine-slim")).
		Build()

	pcsg1CPUOnly := testutils.NewPodCliqueTemplateSpecBuilder("cpu2").
		WithRoleName("cpu2").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewContainer("cpu", "nginx:alpine-slim")).
		Build()

	// PCSG sg2 clique
	pcsg2CPUOnly := testutils.NewPodCliqueTemplateSpecBuilder("cpu3").
		WithRoleName("cpu3").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewContainer("cpu", "nginx:alpine-slim")).
		Build()

	return testutils.NewPodCliqueSetBuilder(name, "default", types.UID("")).
		WithReplicas(int32(replicas)).
		WithTerminationDelay(time.Second).
		WithPodCliqueTemplateSpec(standaloneGPUMixed).
		WithPodCliqueTemplateSpec(standaloneCPUOnly).
		WithPodCliqueTemplateSpec(pcsg1GPUMixed).
		WithPodCliqueTemplateSpec(pcsg1CPUOnly).
		WithPodCliqueTemplateSpec(pcsg2CPUOnly).
		WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{
			Name:         "sg1",
			CliqueNames:  []string{"gpu2", "cpu2"},
			MinAvailable: ptr.To(int32(1)),
		}).
		WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{
			Name:         "sg2",
			CliqueNames:  []string{"cpu3"},
			MinAvailable: ptr.To(int32(1)),
		}).
		Build()
}

// deletePCS deletes a PCS by name
func deletePCS(tc testContext, name string) {
	_ = utils.DeletePodCliqueSet(tc.ctx, tc.dynamicClient, tc.namespace, name)
}

// scalePCS scales a PCS to the specified number of replicas
func scalePCS(tc testContext, name string, replicas int) error {
	return utils.ScalePodCliqueSetWithClient(tc.ctx, tc.dynamicClient, tc.namespace, name, replicas)
}

// waitForComputeDomainCount waits for the specified number of ComputeDomains for a PCS
func waitForComputeDomainCount(tc testContext, pcsName string, expectedCount int) error {
	return utils.PollForCondition(tc.ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		list, err := tc.dynamicClient.Resource(computeDomainGVR).Namespace(tc.namespace).List(tc.ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app.kubernetes.io/part-of=%s", pcsName),
		})
		if err != nil {
			return false, nil
		}
		return len(list.Items) == expectedCount, nil
	})
}

// waitForPCSG waits for a PCSG to exist and returns it
func waitForPCSG(tc testContext, pcsgName string) (*grovecorev1alpha1.PodCliqueScalingGroup, error) {
	return utils.WaitForPodCliqueScalingGroup(tc.ctx, tc.groveClient, tc.namespace, pcsgName, defaultPollTimeout, defaultPollInterval)
}

// waitForPCLQ waits for a PCLQ to exist and returns it
func waitForPCLQ(tc testContext, pclqName string) (*grovecorev1alpha1.PodClique, error) {
	return utils.WaitForPodClique(tc.ctx, tc.groveClient, tc.namespace, pclqName, defaultPollTimeout, defaultPollInterval)
}
