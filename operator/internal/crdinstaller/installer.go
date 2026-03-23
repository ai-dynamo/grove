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

// Package crdinstaller provides functionality to install and upgrade Grove CRDs
// via server-side apply. It is used by the operator's init container subcommand.
package crdinstaller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	operatorcrds "github.com/ai-dynamo/grove/operator/api/core/v1alpha1/crds"
	schedulercrds "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1/crds"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const fieldManager = "grove-crd-installer"

// allCRDs returns all 5 Grove CRD yaml definitions as a slice of strings.
func allCRDs() []string {
	return []string{
		operatorcrds.PodCliqueCRD(),
		operatorcrds.PodCliqueSetCRD(),
		operatorcrds.PodCliqueScalingGroupCRD(),
		operatorcrds.ClusterTopologyCRD(),
		schedulercrds.PodGangCRD(),
	}
}

// InstallCRDs applies all 5 Grove CRDs via server-side apply and logs each name.
// It returns an error if any CRD fails to apply.
func InstallCRDs(ctx context.Context, cl client.Client, log logr.Logger) error {
	for _, crdYAML := range allCRDs() {
		name, err := ApplyCRD(ctx, cl, []byte(crdYAML))
		if err != nil {
			return fmt.Errorf("failed to apply CRD %q: %w", name, err)
		}
		log.Info("CRD applied", "name", name)
	}
	return nil
}

// ApplyCRD applies a single CRD yaml via server-side apply.
// It returns the CRD name and any error.
func ApplyCRD(ctx context.Context, cl client.Client, data []byte) (name string, err error) {
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(data, &obj.Object); err != nil {
		return "", fmt.Errorf("failed to unmarshal CRD yaml: %w", err)
	}

	name = obj.GetName()

	applyConfig := client.ApplyConfigurationFromUnstructured(obj)
	if err := cl.Apply(ctx, applyConfig, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
		return name, fmt.Errorf("server-side apply failed: %w", err)
	}

	return name, nil
}
