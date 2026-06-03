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

package config

import (
	"context"
	"fmt"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	operatorNamespace        = setup.OperatorNamespace
	operatorDeploymentName   = setup.OperatorDeploymentName
	operatorConfigVolume     = "operator-config"
	operatorConfigDataKey    = "config.yaml"
	operatorManagerContainer = "manager"
)

// GroveMetadata holds operator deployment metadata collected before a test run.
type GroveMetadata struct {
	Config *configv1alpha1.OperatorConfiguration
	Image  string
}

// OperatorConfig provides access to Grove operator configuration using a controller-runtime client.
type OperatorConfig struct {
	cl client.Client
}

// NewOperatorConfig creates an OperatorConfig bound to the given client.
func NewOperatorConfig(cl client.Client) *OperatorConfig {
	return &OperatorConfig{cl: cl}
}

// ReadGroveMetadata fetches the operator Deployment and returns both the config and image.
func (oc *OperatorConfig) ReadGroveMetadata(ctx context.Context) (*GroveMetadata, error) {
	dep := &appsv1.Deployment{}
	if err := oc.cl.Get(ctx, types.NamespacedName{
		Namespace: operatorNamespace,
		Name:      operatorDeploymentName,
	}, dep); err != nil {
		return nil, fmt.Errorf("getting deployment %q: %w", operatorDeploymentName, err)
	}

	image, err := findContainerImage(dep, operatorManagerContainer)
	if err != nil {
		return nil, err
	}
	cmName, err := findConfigMapName(dep)
	if err != nil {
		return nil, err
	}

	cm := &corev1.ConfigMap{}
	if err := oc.cl.Get(ctx, types.NamespacedName{
		Namespace: operatorNamespace,
		Name:      cmName,
	}, cm); err != nil {
		return nil, fmt.Errorf("getting configmap %q: %w", cmName, err)
	}
	data, ok := cm.Data[operatorConfigDataKey]
	if !ok {
		return nil, fmt.Errorf("key %q not found in configmap %q", operatorConfigDataKey, cmName)
	}

	cfg, err := configv1alpha1.DecodeOperatorConfig([]byte(data))
	if err != nil {
		return nil, err
	}
	return &GroveMetadata{Config: cfg, Image: image}, nil
}

func findConfigMapName(dep *appsv1.Deployment) (string, error) {
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == operatorConfigVolume && vol.ConfigMap != nil {
			return vol.ConfigMap.Name, nil
		}
	}
	return "", fmt.Errorf("volume %q not found in deployment %q", operatorConfigVolume, operatorDeploymentName)
}

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
	return containers[0].Image, nil
}
