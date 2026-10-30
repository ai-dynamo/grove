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

// Package crdinstaller installs Grove CRDs and configures their conversion webhooks.
package crdinstaller

import (
	"context"
	"encoding/base64"
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

// ConversionWebhookConfig contains values injected by the CRD-installer init container. The PodCliqueSet CRD in
// the OCI artifact is not Helm-templated, so its configurable webhook Service name, namespace, and port must be
// injected at runtime. Manual certificate provisioning also injects CABundle; automatic mode leaves it to
// cert-controller.
type ConversionWebhookConfig struct {
	// ServiceName is the webhook Service name.
	ServiceName string
	// ServiceNamespace is the webhook Service namespace.
	ServiceNamespace string
	// ServicePort is the webhook Service port.
	ServicePort int
	// CABundle verifies the webhook's serving certificate. An empty value is left to an external injector.
	CABundle []byte
}

// InstallCRDs applies the given CRD YAML definitions via server-side apply and
// logs each applied name. Service-addressed conversion webhooks are configured with conversion.
// It returns an error if any CRD fails to apply.
func InstallCRDs(ctx context.Context, cl client.Client, log logr.Logger, crds []string, conversion ConversionWebhookConfig) error {
	if err := deleteEmptyLegacyClusterTopologyCRD(ctx, cl, log); err != nil {
		return err
	}

	for _, crdYAML := range crds {
		name, err := applyCRD(ctx, cl, []byte(crdYAML), conversion)
		if err != nil {
			return fmt.Errorf("failed to apply CRD %q: %w", name, err)
		}
		log.Info("CRD applied", "name", name)
	}
	return nil
}

// ConfigureConversionWebhook applies only the conversion configuration from crdYAML. It does not update any other
// CRD fields.
func ConfigureConversionWebhook(ctx context.Context, cl client.Client, log logr.Logger, crdYAML string, conversion ConversionWebhookConfig) error {
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(crdYAML), &obj.Object); err != nil {
		return fmt.Errorf("failed to unmarshal CRD yaml: %w", err)
	}
	if err := setConversionWebhook(obj, conversion); err != nil {
		return err
	}

	conversionSpec, found, err := unstructured.NestedMap(obj.Object, "spec", "conversion")
	if err != nil {
		return fmt.Errorf("failed to read conversion webhook configuration: %w", err)
	}
	if !found {
		return fmt.Errorf("CRD %q has no conversion webhook configuration", obj.GetName())
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	if err := cl.Get(ctx, client.ObjectKey{Name: obj.GetName()}, existing); err != nil {
		return fmt.Errorf("failed to get CRD %q: %w", obj.GetName(), err)
	}

	// In automatic mode, cert-controller owns caBundle. Preserve its current value in this merge patch.
	if len(conversion.CABundle) == 0 {
		if caBundle, found, err := unstructured.NestedFieldCopy(existing.Object, "spec", "conversion", "webhook", "clientConfig", "caBundle"); err != nil {
			return fmt.Errorf("failed to read the existing conversion webhook CA bundle: %w", err)
		} else if found {
			if err := unstructured.SetNestedField(conversionSpec, caBundle, "webhook", "clientConfig", "caBundle"); err != nil {
				return fmt.Errorf("failed to preserve the conversion webhook CA bundle: %w", err)
			}
		}
	}

	base := existing.DeepCopy()
	if err := unstructured.SetNestedMap(existing.Object, conversionSpec, "spec", "conversion"); err != nil {
		return fmt.Errorf("failed to set conversion webhook configuration: %w", err)
	}
	if err := cl.Patch(ctx, existing, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("failed to patch conversion webhook configuration on CRD %q: %w", obj.GetName(), err)
	}
	log.Info("CRD conversion webhook configured", "name", obj.GetName())
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
func applyCRD(ctx context.Context, cl client.Client, data []byte, conversion ConversionWebhookConfig) (name string, err error) {
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(data, &obj.Object); err != nil {
		return "", fmt.Errorf("failed to unmarshal CRD yaml: %w", err)
	}

	name = obj.GetName()

	if err := setConversionWebhook(obj, conversion); err != nil {
		return name, err
	}

	applyConfig := client.ApplyConfigurationFromUnstructured(obj)
	if err := cl.Apply(ctx, applyConfig, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
		return name, fmt.Errorf("server-side apply failed: %w", err)
	}

	return name, nil
}

// setConversionWebhook configures a Service-addressed conversion webhook. An empty CA bundle is omitted
// so that an external injector can own it.
func setConversionWebhook(obj *unstructured.Unstructured, conversion ConversionWebhookConfig) error {
	servicePath := []string{"spec", "conversion", "webhook", "clientConfig", "service"}
	_, found, err := unstructured.NestedMap(obj.Object, servicePath...)
	if err != nil {
		return fmt.Errorf("failed to read conversion webhook service: %w", err)
	}
	if !found {
		return nil
	}
	if conversion.ServiceName == "" || conversion.ServiceNamespace == "" {
		return fmt.Errorf("conversion webhook service name and namespace are required")
	}
	if conversion.ServicePort < 1 || conversion.ServicePort > 65535 {
		return fmt.Errorf("conversion webhook service port must be between 1 and 65535")
	}
	fields := []struct {
		value any
		path  []string
	}{
		{conversion.ServiceName, append(servicePath, "name")},
		{conversion.ServiceNamespace, append(servicePath, "namespace")},
		{int64(conversion.ServicePort), append(servicePath, "port")},
	}
	for _, field := range fields {
		if err := unstructured.SetNestedField(obj.Object, field.value, field.path...); err != nil {
			return err
		}
	}

	caBundlePath := []string{"spec", "conversion", "webhook", "clientConfig", "caBundle"}
	if len(conversion.CABundle) == 0 {
		unstructured.RemoveNestedField(obj.Object, caBundlePath...)
		return nil
	}
	if err := unstructured.SetNestedField(obj.Object, base64.StdEncoding.EncodeToString(conversion.CABundle), caBundlePath...); err != nil {
		return err
	}
	return nil
}
