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

// Package crdinstaller provides functionality to install and upgrade Grove CRDs
// via server-side apply. It is used by the operator's init container subcommand.
package crdinstaller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	fieldManager                 = "grove-crd-installer"
	legacyClusterTopologyCRDName = "clustertopologies.grove.io"
	legacyClusterTopologyGroup   = "grove.io"
	legacyClusterTopologyVersion = "v1alpha1"
	legacyClusterTopologyKind    = "ClusterTopology"
)

// InstallCRDs applies the given CRD YAML definitions via server-side apply and
// logs each applied name. It returns an error if any CRD fails to apply.
func InstallCRDs(ctx context.Context, cl client.Client, log logr.Logger, crds []string) error {
	if err := deleteEmptyLegacyClusterTopologyCRD(ctx, cl, log); err != nil {
		return err
	}

	for _, crdYAML := range crds {
		name, err := applyCRD(ctx, cl, []byte(crdYAML))
		if err != nil {
			return fmt.Errorf("failed to apply CRD %q: %w", name, err)
		}
		log.Info("CRD applied", "name", name)
	}
	return nil
}

// deleteEmptyLegacyClusterTopologyCRD removes the unused ClusterTopology CRD
// when it has no instances. It retains the CRD when resources exist.
func deleteEmptyLegacyClusterTopologyCRD(ctx context.Context, cl client.Client, log logr.Logger) error {
	legacyCRD := &apiextensionsv1.CustomResourceDefinition{}
	if err := cl.Get(ctx, client.ObjectKey{Name: legacyClusterTopologyCRDName}, legacyCRD); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get legacy ClusterTopology CRD: %w", err)
	}

	legacyTopologies := &unstructured.UnstructuredList{}
	legacyTopologies.SetGroupVersionKind(schema.GroupVersion{
		Group:   legacyClusterTopologyGroup,
		Version: legacyClusterTopologyVersion,
	}.WithKind(legacyClusterTopologyKind + "List"))
	if err := cl.List(ctx, legacyTopologies); err != nil {
		return fmt.Errorf("list legacy ClusterTopology resources: %w", err)
	}
	if len(legacyTopologies.Items) != 0 {
		log.Info("Legacy ClusterTopology CRD retained because resources still exist",
			"name", legacyClusterTopologyCRDName,
			"resourceCount", len(legacyTopologies.Items),
		)
		return nil
	}

	if err := cl.Delete(ctx, legacyCRD); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete empty legacy ClusterTopology CRD: %w", err)
	}
	log.Info("Deleted empty legacy ClusterTopology CRD", "name", legacyClusterTopologyCRDName)
	return nil
}

// applyCRD applies a single CRD yaml via server-side apply.
// It returns the CRD name and any error.
func applyCRD(ctx context.Context, cl client.Client, data []byte) (name string, err error) {
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
