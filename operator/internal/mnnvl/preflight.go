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

package mnnvl

import (
	"fmt"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
)

var logger = ctrl.Log.WithName("mnnvl")

// Preflight checks if MNNVL prerequisites are met when the feature is enabled.
// If MNNVL is enabled, it verifies that the ComputeDomain CRD is installed in the cluster.
func Preflight(operatorCfg *configv1alpha1.OperatorConfiguration) error {
	if !operatorCfg.Network.AutoMNNVLEnabled {
		return nil
	}

	logger.Info("MNNVL support is enabled, validating prerequisites")

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get cluster config for MNNVL validation: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create discovery client for MNNVL validation: %w", err)
	}

	// Check if the ComputeDomain CRD exists
	_, apiResourceList, err := discoveryClient.ServerGroupsAndResources()
	if err != nil {
		// Handle partial discovery errors gracefully
		if !discovery.IsGroupDiscoveryFailedError(err) {
			return fmt.Errorf("failed to discover API resources: %w", err)
		}
	}

	if !IsComputeDomainCRDPresent(apiResourceList) {
		return fmt.Errorf("MNNVL is enabled but ComputeDomain CRD (%s) is not installed. "+
			"Please install the NVIDIA DRA driver or disable MNNVL in the operator configuration", ComputeDomainCRDName)
	}

	logger.Info("ComputeDomain CRD found, MNNVL prerequisites validated")
	return nil
}

// IsComputeDomainCRDPresent checks if the ComputeDomain CRD is present in the API resource list.
func IsComputeDomainCRDPresent(apiResourceList []*metav1.APIResourceList) bool {
	computeDomainGroupVersion := ComputeDomainGroup + "/" + ComputeDomainVersion
	for _, resourceList := range apiResourceList {
		for _, resource := range resourceList.APIResources {
			if resource.Name == ComputeDomainResource && resourceList.GroupVersion == computeDomainGroupVersion {
				return true
			}
		}
	}
	return false
}
