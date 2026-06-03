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

package setup

import (
	"strings"

	"github.com/ai-dynamo/grove/operator/e2e/grove/gvk"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// kindToResource converts a Kind to its plural resource name.
// Follows the Kubernetes convention: lowercase + "s" (e.g. "PodCliqueSet" → "podcliquesets").
func kindToResource(kind string) string {
	return strings.ToLower(kind) + "s"
}

// resourceType represents a Kubernetes resource type for cleanup operations.
type resourceType struct {
	schema.GroupVersionResource
	kind string // singular Kind (e.g. "PodCliqueSet")
	name string // display name for logging (e.g. "PodCliqueSets")
}

// newResourceType creates a resourceType from a GroupVersionKind.
// Resource (plural) is derived from Kind, display name is Kind + "s".
func newResourceType(g schema.GroupVersionKind) resourceType {
	return resourceType{
		GroupVersionResource: schema.GroupVersionResource{
			Group:    g.Group,
			Version:  g.Version,
			Resource: kindToResource(g.Kind),
		},
		kind: g.Kind,
		name: g.Kind + "s",
	}
}

// Kubernetes core resource GVKs used in cleanup operations.
var (
	serviceGVK              = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}
	serviceAccountGVK       = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ServiceAccount"}
	secretGVK               = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"}
	roleGVK                 = schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"}
	roleBindingGVK          = schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"}
	horizontalPodAutoscaler = schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"}
)

// pcsResourceType is the PodCliqueSet resource — the top-level user-created resource
// that doesn't carry the managed-by label and must be checked without label selector.
var pcsResourceType = newResourceType(gvk.PodCliqueSet)

// groveManagedResourceTypes defines all resource types managed by Grove operator that need to be tracked for cleanup.
var groveManagedResourceTypes = []resourceType{
	// Grove CRDs
	pcsResourceType,
	newResourceType(gvk.PodCliqueScalingGroup),
	newResourceType(gvk.PodGang),
	newResourceType(gvk.PodClique),
	// Kubernetes core resources
	newResourceType(serviceGVK),
	newResourceType(serviceAccountGVK),
	newResourceType(secretGVK),
	// RBAC resources
	newResourceType(roleGVK),
	newResourceType(roleBindingGVK),
	// Autoscaling resources
	newResourceType(horizontalPodAutoscaler),
	// NVIDIA ComputeDomain (created by MNNVL feature, has finalizers that must be processed)
	newResourceType(gvk.ComputeDomain),
}
