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

package utils

import (
	"context"
	"fmt"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	operatorNamespace        = "grove-system"
	operatorDeploymentName   = "grove-operator"
	operatorConfigVolume     = "operator-config"
	operatorConfigDataKey    = "config.yaml"
	operatorManagerContainer = "manager"
)

// GroveMetadata holds operator deployment metadata collected before a test run.
type GroveMetadata struct {
	Config *configv1alpha1.OperatorConfiguration
	Image  string
}

// ReadGroveMetadata fetches the operator Deployment once and returns both the
// operator config and the manager container image.
func ReadGroveMetadata(ctx context.Context, crClient client.Client) (*GroveMetadata, error) {
	dep, err := getOperatorDeployment(ctx, crClient)
	if err != nil {
		return nil, err
	}
	image, err := findContainerImage(dep, operatorManagerContainer)
	if err != nil {
		return nil, err
	}
	cmName, err := findConfigMapName(dep)
	if err != nil {
		return nil, err
	}
	data, err := readConfigMapData(ctx, crClient, cmName)
	if err != nil {
		return nil, err
	}
	cfg, err := configv1alpha1.DecodeOperatorConfig([]byte(data))
	if err != nil {
		return nil, err
	}
	return &GroveMetadata{Config: cfg, Image: image}, nil
}

// getOperatorDeployment fetches the grove operator Deployment.
func getOperatorDeployment(ctx context.Context, crClient client.Client) (*appsv1.Deployment, error) {
	dep := &appsv1.Deployment{}
	if err := crClient.Get(ctx, types.NamespacedName{
		Namespace: operatorNamespace,
		Name:      operatorDeploymentName,
	}, dep); err != nil {
		return nil, fmt.Errorf("getting deployment %q: %w", operatorDeploymentName, err)
	}
	return dep, nil
}

// findConfigMapName extracts the operator ConfigMap name from the deployment's volume reference.
// The ConfigMap name is not hardcoded because Helm appends a hash suffix (e.g. grove-operator-cm-2ed7ef98),
// while the volume name is stable and set by the chart.
func findConfigMapName(dep *appsv1.Deployment) (string, error) {
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == operatorConfigVolume && vol.ConfigMap != nil {
			return vol.ConfigMap.Name, nil
		}
	}
	return "", fmt.Errorf("volume %q not found in deployment %q",
		operatorConfigVolume, operatorDeploymentName)
}

// findContainerImage returns the image for the named container.
// Falls back to the first container if the named one is not found, to handle
// deployments where the main container has a non-standard name.
func findContainerImage(dep *appsv1.Deployment, name string) (string, error) {
	containers := dep.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		return "", fmt.Errorf("deployment %q has no containers", operatorDeploymentName)
	}
	for _, c := range containers {
		if c.Name == name {
			return c.Image, nil
		}
	}
	// Named container not found; fall back to first container.
	return containers[0].Image, nil
}

// readConfigMapData fetches the operator ConfigMap and returns the config.yaml value.
func readConfigMapData(ctx context.Context, crClient client.Client, cmName string) (string, error) {
	cm := &corev1.ConfigMap{}
	if err := crClient.Get(ctx, types.NamespacedName{
		Namespace: operatorNamespace,
		Name:      cmName,
	}, cm); err != nil {
		return "", fmt.Errorf("getting configmap %q: %w", cmName, err)
	}
	data, ok := cm.Data[operatorConfigDataKey]
	if !ok {
		return "", fmt.Errorf("key %q not found in configmap %q",
			operatorConfigDataKey, cmName)
	}
	return data, nil
}
