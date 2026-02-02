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

// Package setup provides utilities for connecting to k3d clusters for E2E testing.
//
// Cluster creation is handled by the create-e2e-cluster.sh script, which must be run
// before E2E tests. This package only handles connecting to existing clusters.
package setup

import (
	"context"
	"fmt"

	k3dclient "github.com/k3d-io/k3d/v5/pkg/client"
	"github.com/k3d-io/k3d/v5/pkg/runtimes"
	k3d "github.com/k3d-io/k3d/v5/pkg/types"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// GetKubeconfig fetches and returns the kubeconfig for a k3d cluster.
// The cluster must already exist (created via create-e2e-cluster.sh).
func GetKubeconfig(ctx context.Context, clusterName string) (*clientcmdapi.Config, error) {
	cluster, err := k3dclient.ClusterGet(ctx, runtimes.Docker, &k3d.Cluster{Name: clusterName})
	if err != nil {
		return nil, fmt.Errorf("could not get cluster '%s'. For local development run './operator/hack/create-e2e-cluster.sh' first: %w", clusterName, err)
	}

	kubeconfig, err := k3dclient.KubeconfigGet(ctx, runtimes.Docker, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig from k3d: %w", err)
	}

	return kubeconfig, nil
}