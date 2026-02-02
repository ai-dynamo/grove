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

// Package setup provides utilities for connecting to Kubernetes clusters for E2E testing.
//
// The cluster must be created beforehand with Grove operator, Kai scheduler, and required
// test infrastructure already deployed. For local development with k3d, you can use:
//
//	./operator/hack/create-e2e-cluster.sh
//
// This package only handles connecting to existing clusters - it does not create clusters.
package setup

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// GetRestConfig returns a REST config for connecting to a Kubernetes cluster.
// It tries the following methods in order:
//  1. KUBECONFIG environment variable (if set)
//  2. Default kubeconfig at ~/.kube/config
//  3. In-cluster config (when running inside a pod)
//
// For local development with k3d, run './operator/hack/create-e2e-cluster.sh' first
// to create a cluster and configure kubectl.
func GetRestConfig() (*rest.Config, error) {
	// Try KUBECONFIG environment variable first
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		// Fall back to default location
		homeDir, err := os.UserHomeDir()
		if err == nil {
			kubeconfigPath = filepath.Join(homeDir, ".kube", "config")
		}
	}

	// Try to load from kubeconfig file
	if kubeconfigPath != "" {
		if _, err := os.Stat(kubeconfigPath); err == nil {
			config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
			if err == nil {
				return config, nil
			}
		}
	}

	// Fall back to in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubernetes config: no KUBECONFIG found, ~/.kube/config not accessible, and not running in-cluster. "+
			"For local development, run './operator/hack/create-e2e-cluster.sh' first: %w", err)
	}

	return config, nil
}
