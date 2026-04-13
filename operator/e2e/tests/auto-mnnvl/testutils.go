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
	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	"gopkg.in/yaml.v3"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
func requireClusterConfig(t *testing.T, ctx context.Context, clients *clients.Clients) *clusterMNNVLConfig {
	t.Helper()

	configOnce.Do(func() {
		cachedConfig, configErr = detectClusterConfig(ctx, clients)
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
func detectClusterConfig(ctx context.Context, clients *clients.Clients) (*clusterMNNVLConfig, error) {
	config := &clusterMNNVLConfig{}

	// Detect if MNNVL feature is enabled in operator config
	featureEnabled, err := detectAutoMNNVLEnabled(ctx, clients.Clientset)
	if err != nil {
		return nil, fmt.Errorf("failed to detect feature enabled: %w", err)
	}
	config.featureEnabled = featureEnabled

	// Detect if ComputeDomain CRD exists
	crdSupported, err := detectComputeDomainCRDExists(ctx, clients.RestConfig)
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
		WithContainer(testutils.NewGPUContainer("gpu-container", "busybox:latest", 1, "sleep", "infinity")).
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
		WithContainer(testutils.NewContainer("cpu-container", "busybox:latest", "sleep", "infinity")).
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
		WithContainer(testutils.NewGPUContainer("gpu", "busybox:latest", 1, "sleep", "infinity")).
		WithContainer(testutils.NewContainer("cpu", "busybox:latest", "sleep", "infinity")).
		Build()

	standaloneCPUOnly := testutils.NewPodCliqueTemplateSpecBuilder("cpu1").
		WithRoleName("cpu1").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewContainer("cpu", "busybox:latest", "sleep", "infinity")).
		Build()

	// PCSG sg1 cliques
	pcsg1GPUMixed := testutils.NewPodCliqueTemplateSpecBuilder("gpu2").
		WithRoleName("gpu2").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewGPUContainer("gpu", "busybox:latest", 1, "sleep", "infinity")).
		WithContainer(testutils.NewContainer("cpu", "busybox:latest", "sleep", "infinity")).
		Build()

	pcsg1CPUOnly := testutils.NewPodCliqueTemplateSpecBuilder("cpu2").
		WithRoleName("cpu2").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewContainer("cpu", "busybox:latest", "sleep", "infinity")).
		Build()

	// PCSG sg2 clique
	pcsg2CPUOnly := testutils.NewPodCliqueTemplateSpecBuilder("cpu3").
		WithRoleName("cpu3").
		WithReplicas(1).
		WithMinAvailable(1).
		WithContainer(testutils.NewContainer("cpu", "busybox:latest", "sleep", "infinity")).
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
func deletePCS(tc *testctx.TestContext, name string) {
	_ = utils.DeletePodCliqueSet(tc.Ctx, tc.Clients.DynamicClient, tc.Namespace, name)
}

// scalePCS scales a PCS to the specified number of replicas
func scalePCS(tc *testctx.TestContext, name string, replicas int) error {
	return utils.ScalePodCliqueSetWithClient(tc.Ctx, tc.Clients.DynamicClient, tc.Namespace, name, replicas)
}

// waitForComputeDomainCount waits for the specified number of ComputeDomains for a PCS
func waitForComputeDomainCount(tc *testctx.TestContext, pcsName string, expectedCount int) error {
	return utils.PollForCondition(tc.Ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		list, err := tc.Clients.DynamicClient.Resource(computeDomainGVR).Namespace(tc.Namespace).List(tc.Ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app.kubernetes.io/part-of=%s", pcsName),
		})
		if err != nil {
			return false, nil
		}
		return len(list.Items) == expectedCount, nil
	})
}

// waitForPCSG waits for a PCSG to exist and returns it
func waitForPCSG(tc *testctx.TestContext, pcsgName string) (*grovecorev1alpha1.PodCliqueScalingGroup, error) {
	return utils.WaitForPodCliqueScalingGroup(tc.Ctx, tc.Clients.GroveClient, tc.Namespace, pcsgName, defaultPollTimeout, defaultPollInterval)
}

// waitForPCLQ waits for a PCLQ to exist and returns it
func waitForPCLQ(tc *testctx.TestContext, pclqName string) (*grovecorev1alpha1.PodClique, error) {
	return utils.WaitForPodClique(tc.Ctx, tc.Clients.GroveClient, tc.Namespace, pclqName, defaultPollTimeout, defaultPollInterval)
}
