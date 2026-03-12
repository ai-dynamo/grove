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
	operatorNamespace       = "grove-system"
	operatorDeploymentName  = "grove-operator"
	operatorConfigVolume    = "operator-config"
	operatorConfigDataKey   = "config.yaml"
)

// ReadGroveConfig reads and parses the operator's live configuration.
// Orchestrates: findOperatorConfigMapName → readConfigMapData → ParseGroveConfig.
func ReadGroveConfig(ctx context.Context, crClient client.Client) (*configv1alpha1.OperatorConfiguration, error) {
	cmName, err := findOperatorConfigMapName(ctx, crClient)
	if err != nil {
		return nil, err
	}
	data, err := readConfigMapData(ctx, crClient, cmName)
	if err != nil {
		return nil, err
	}
	return configv1alpha1.DecodeOperatorConfig([]byte(data))
}

// findOperatorConfigMapName retrieves the ConfigMap name from the operator
// Deployment's volume reference for the operator-config volume.
func findOperatorConfigMapName(ctx context.Context, crClient client.Client) (string, error) {
	dep := &appsv1.Deployment{}
	if err := crClient.Get(ctx, types.NamespacedName{
		Namespace: operatorNamespace,
		Name:      operatorDeploymentName,
	}, dep); err != nil {
		return "", fmt.Errorf("getting deployment %q: %w", operatorDeploymentName, err)
	}
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == operatorConfigVolume && vol.ConfigMap != nil {
			return vol.ConfigMap.Name, nil
		}
	}
	return "", fmt.Errorf("volume %q not found in deployment %q",
		operatorConfigVolume, operatorDeploymentName)
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
