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

package resourceclaim

import (
	"context"
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResolveTemplateSpec resolves a ResourceSharingSpec to its ResourceClaimTemplateSpec.
// Resolution order: internal PCS-level resourceClaimTemplates first, then external
// Kubernetes ResourceClaimTemplate objects. Internal templates shadow external ones.
func ResolveTemplateSpec(
	ctx context.Context,
	cl client.Reader,
	base *grovecorev1alpha1.ResourceSharingSpec,
	pcsTemplates []grovecorev1alpha1.ResourceClaimTemplateConfig,
	pcsNamespace string,
) (*resourcev1.ResourceClaimTemplateSpec, error) {
	if spec := resolveInternalRef(base, pcsTemplates); spec != nil {
		return spec, nil
	}
	return resolveExternalRef(ctx, cl, base, pcsNamespace)
}

// resolveInternalRef returns the matching template spec from pcsTemplates, or
// nil when the name does not match any internal template.
func resolveInternalRef(
	base *grovecorev1alpha1.ResourceSharingSpec,
	pcsTemplates []grovecorev1alpha1.ResourceClaimTemplateConfig,
) *resourcev1.ResourceClaimTemplateSpec {
	if base.Namespace != "" {
		return nil
	}
	for i := range pcsTemplates {
		if pcsTemplates[i].Name == base.Name {
			return &pcsTemplates[i].TemplateSpec
		}
	}
	return nil
}

func resolveExternalRef(
	ctx context.Context,
	cl client.Reader,
	base *grovecorev1alpha1.ResourceSharingSpec,
	pcsNamespace string,
) (*resourcev1.ResourceClaimTemplateSpec, error) {
	ns := base.Namespace
	if ns == "" {
		ns = pcsNamespace
	}
	rct := &resourcev1.ResourceClaimTemplate{}
	if err := cl.Get(ctx, types.NamespacedName{Name: base.Name, Namespace: ns}, rct); err != nil {
		return nil, fmt.Errorf("failed to get external ResourceClaimTemplate %s/%s: %w", ns, base.Name, err)
	}
	return &rct.Spec, nil
}
