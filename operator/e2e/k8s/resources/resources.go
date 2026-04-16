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

package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

// AppliedResource holds information about an applied Kubernetes resource.
type AppliedResource struct {
	Name      string
	Namespace string
	GVK       schema.GroupVersionKind
	GVR       schema.GroupVersionResource
}

// ResourceManager provides Kubernetes resource operations using pre-created clients.
type ResourceManager struct {
	clients *clients.Clients
	logger  *log.Logger
}

// NewResourceManager creates a ResourceManager bound to the given clients.
func NewResourceManager(c *clients.Clients, logger *log.Logger) *ResourceManager {
	return &ResourceManager{clients: c, logger: logger}
}

// ApplyYAMLFile applies a YAML file containing Kubernetes resources.
// namespace parameter is optional - pass empty string to use namespace from YAML.
func (rm *ResourceManager) ApplyYAMLFile(ctx context.Context, yamlFilePath, namespace string) ([]AppliedResource, error) {
	rm.logger.Debugf("Applying resources from %s...", yamlFilePath)

	yamlData, err := os.ReadFile(yamlFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read YAML file %s: %w", yamlFilePath, err)
	}

	return rm.ApplyYAMLData(ctx, yamlData, namespace)
}

// ApplyYAMLData applies YAML data to Kubernetes.
func (rm *ResourceManager) ApplyYAMLData(ctx context.Context, yamlData []byte, namespace string) ([]AppliedResource, error) {
	decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(string(yamlData)), 4096)
	var appliedResources []AppliedResource

	for {
		unstructuredObj, gvk, err := decodeNextYAMLObject(decoder)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if unstructuredObj == nil {
			continue
		}

		appliedResource, err := rm.applyResource(ctx, unstructuredObj, gvk, namespace)
		if err != nil {
			return nil, err
		}

		appliedResources = append(appliedResources, *appliedResource)
	}

	rm.logger.Debugf("Applied %d resources successfully", len(appliedResources))
	return appliedResources, nil
}

// ScaleCRD patches the replicas field of a custom resource.
func (rm *ResourceManager) ScaleCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, replicas int) error {
	scalePatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": replicas,
		},
	}
	patchBytes, err := json.Marshal(scalePatch)
	if err != nil {
		return fmt.Errorf("failed to marshal scale patch: %w", err)
	}

	if _, err := rm.clients.DynamicClient.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("failed to scale %s %s: %w", gvr.Resource, name, err)
	}

	return nil
}

func (rm *ResourceManager) applyResource(ctx context.Context, obj *unstructured.Unstructured, gvk *schema.GroupVersionKind, namespace string) (*AppliedResource, error) {
	gvr, mapping, err := rm.getResourceMapping(gvk)
	if err != nil {
		return nil, err
	}

	handleResourceNamespace(obj, mapping, namespace)

	result, err := rm.createOrUpdateResource(ctx, gvr, mapping, obj)
	if err != nil {
		return nil, fmt.Errorf("failed to apply %s %s: %w", gvk.Kind, obj.GetName(), err)
	}

	return &AppliedResource{
		Name:      result.GetName(),
		Namespace: result.GetNamespace(),
		GVK:       *gvk,
		GVR:       gvr,
	}, nil
}

func (rm *ResourceManager) getResourceMapping(gvk *schema.GroupVersionKind) (schema.GroupVersionResource, *meta.RESTMapping, error) {
	mapping, err := rm.clients.RestMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, nil, fmt.Errorf("failed to get REST mapping for %s: %w", gvk.String(), err)
	}

	return mapping.Resource, mapping, nil
}

func handleResourceNamespace(obj *unstructured.Unstructured, mapping *meta.RESTMapping, namespace string) {
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		if namespace != "" {
			obj.SetNamespace(namespace)
		}
		if obj.GetNamespace() == "" {
			obj.SetNamespace("default")
		}
	} else {
		obj.SetNamespace("")
	}
}

func (rm *ResourceManager) createOrUpdateResource(ctx context.Context, gvr schema.GroupVersionResource, mapping *meta.RESTMapping, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	result, err := rm.createResource(ctx, gvr, mapping, obj)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			return rm.updateResource(ctx, gvr, mapping, obj)
		}
		return nil, err
	}
	return result, nil
}

func (rm *ResourceManager) createResource(ctx context.Context, gvr schema.GroupVersionResource, mapping *meta.RESTMapping, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return rm.clients.DynamicClient.Resource(gvr).Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{})
	}
	return rm.clients.DynamicClient.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
}

func (rm *ResourceManager) updateResource(ctx context.Context, gvr schema.GroupVersionResource, mapping *meta.RESTMapping, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	var existing *unstructured.Unstructured
	var err error
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		existing, err = rm.clients.DynamicClient.Resource(gvr).Namespace(obj.GetNamespace()).Get(ctx, obj.GetName(), metav1.GetOptions{})
	} else {
		existing, err = rm.clients.DynamicClient.Resource(gvr).Get(ctx, obj.GetName(), metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("get existing resource for update: %w", err)
	}
	obj.SetResourceVersion(existing.GetResourceVersion())

	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return rm.clients.DynamicClient.Resource(gvr).Namespace(obj.GetNamespace()).Update(ctx, obj, metav1.UpdateOptions{})
	}
	return rm.clients.DynamicClient.Resource(gvr).Update(ctx, obj, metav1.UpdateOptions{})
}

func decodeNextYAMLObject(decoder *yamlutil.YAMLOrJSONDecoder) (*unstructured.Unstructured, *schema.GroupVersionKind, error) {
	var rawObj runtime.RawExtension
	if err := decoder.Decode(&rawObj); err != nil {
		return nil, nil, err
	}

	if len(rawObj.Raw) == 0 {
		return nil, nil, nil
	}

	yamlDecoder := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	obj, gvk, err := yamlDecoder.Decode(rawObj.Raw, nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode object: %w", err)
	}

	unstructuredObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, nil, fmt.Errorf("expected unstructured object, got %T", obj)
	}

	return unstructuredObj, gvk, nil
}
