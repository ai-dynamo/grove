//go:build e2e

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
	"path/filepath"
	"runtime"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/resources"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WaitForKaiCRDs waits for the Queue CRD from scheduling.run.ai/v2 to be available
func WaitForKaiCRDs(ctx context.Context, config *HelmInstallConfig) error {
	config.Logger.Debug("⏳ Waiting for Queue CRD (scheduling.run.ai/v2) to be available...")

	// Create API extensions client to check CRDs
	apiExtClient, err := apiextensionsclientset.NewForConfig(config.RestConfig)
	if err != nil {
		return fmt.Errorf("failed to create API extensions client: %w", err)
	}

	crdName := "queues.scheduling.run.ai"
	timeout := 1 * time.Minute
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Check if the Queue CRD exists
		crd, err := apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crdName, metav1.GetOptions{})
		if err != nil {
			config.Logger.Debugf("❌ Queue CRD not found yet: %v", err)
			config.Logger.Info("⏳ Queue CRD not available, waiting 2 seconds...")
			time.Sleep(2 * time.Second)
			continue
		}

		// Check if the CRD is established and has the v2 version
		if isKaiCRDEstablished(crd) && hasKaiCRDVersion(crd, "v2") {
			config.Logger.Debug("✅ Queue CRD (scheduling.run.ai/v2) is available and established!")
			return nil
		}

		config.Logger.Info("⏳ Queue CRD exists but not fully established, waiting 2 seconds...")
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for Queue CRD to be available after %v", timeout)
}

// isKaiCRDEstablished checks if a CRD is established
func isKaiCRDEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, condition := range crd.Status.Conditions {
		if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
			return true
		}
	}
	return false
}

// hasKaiCRDVersion checks if a CRD has a specific version available
func hasKaiCRDVersion(crd *apiextensionsv1.CustomResourceDefinition, version string) bool {
	for _, ver := range crd.Spec.Versions {
		if ver.Name == version && ver.Served {
			return true
		}
	}
	return false
}

// CreateDefaultKaiQueues creates queues using the k8s client YAML apply functionality
func CreateDefaultKaiQueues(ctx context.Context, config *HelmInstallConfig) error {
	config.Logger.Debug("📄 Creating queues using k8s client...")

	// Get the path to the queues.yaml file relative to this source file
	_, currentFile, _, _ := runtime.Caller(0)
	queuesPath := filepath.Join(filepath.Dir(currentFile), "../yaml/queues.yaml")

	// Create K8s client and apply
	k8sClient, err := k8sclient.New(config.RestConfig)
	if err != nil {
		return fmt.Errorf("failed to create K8s client: %w", err)
	}

	appliedResources, err := resources.NewResourceManager(k8sClient, config.Logger).ApplyYAMLFile(ctx, queuesPath, "")
	if err != nil {
		return fmt.Errorf("failed to apply queues YAML: %w", err)
	}

	config.Logger.Debugf("✅ Successfully applied %d queue resources", len(appliedResources))
	return nil
}
