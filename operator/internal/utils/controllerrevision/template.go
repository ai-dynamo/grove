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

package controllerrevision

import (
	"context"
	"fmt"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/lru"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// decodedTemplateCacheSize bounds how many decoded templates are kept in memory.
const decodedTemplateCacheSize = 256

// decodedTemplates memoizes decoded templates by ControllerRevision UID. The data of a ControllerRevision is
// immutable, so an entry never goes stale.
var decodedTemplates = lru.New(decodedTemplateCacheSize)

// GetTemplate returns the template stored in the named ControllerRevision of the PodCliqueSet.
// It returns a NotFound error when the revision does not exist or is not controlled by the PodCliqueSet.
// The returned template is shared, so callers must not mutate it.
func GetTemplate(ctx context.Context, reader client.Reader, pcs metav1.Object, name string) (*grovecorev1beta1.PodCliqueSetTemplateSpec, error) {
	revision := &appsv1.ControllerRevision{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: pcs.GetNamespace(), Name: name}, revision); err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(revision, pcs) {
		return nil, apierrors.NewNotFound(appsv1.Resource("controllerrevisions"), name)
	}
	// Objects created without a UID, as in unit tests, are decoded every time.
	if revision.UID != "" {
		if cached, ok := decodedTemplates.Get(revision.UID); ok {
			return cached.(*grovecorev1beta1.PodCliqueSetTemplateSpec), nil
		}
	}
	template, err := DecodeTemplate(revision.Data.Raw)
	if err != nil {
		return nil, fmt.Errorf("could not decode ControllerRevision %s: %w", client.ObjectKeyFromObject(revision), err)
	}
	if revision.UID != "" {
		decodedTemplates.Add(revision.UID, template)
	}
	return template, nil
}

// CliqueTemplatesEqual reports whether the named clique has the same pod template in both templates.
// It compares the inputs of the pod template hash: the clique's labels, annotations and pod spec, and the
// template's priority class name. A clique missing from either template counts as different.
func CliqueTemplatesEqual(a, b *grovecorev1beta1.PodCliqueSetTemplateSpec, cliqueName string) bool {
	if a == nil || b == nil || a.PriorityClassName != b.PriorityClassName {
		return false
	}
	cliqueA, cliqueB := findClique(a, cliqueName), findClique(b, cliqueName)
	if cliqueA == nil || cliqueB == nil {
		return false
	}
	return apiequality.Semantic.DeepEqual(cliqueA.Labels, cliqueB.Labels) &&
		apiequality.Semantic.DeepEqual(cliqueA.Annotations, cliqueB.Annotations) &&
		apiequality.Semantic.DeepEqual(cliqueA.Spec.PodSpec, cliqueB.Spec.PodSpec)
}

// PodTemplatesEqual reports whether both templates have the same cliques with the same pod templates.
func PodTemplatesEqual(a, b *grovecorev1beta1.PodCliqueSetTemplateSpec) bool {
	if a == nil || b == nil || len(a.Cliques) != len(b.Cliques) {
		return false
	}
	for _, clique := range b.Cliques {
		if clique == nil || !CliqueTemplatesEqual(a, b, clique.Name) {
			return false
		}
	}
	return true
}

func findClique(template *grovecorev1beta1.PodCliqueSetTemplateSpec, cliqueName string) *grovecorev1beta1.PodCliqueTemplateSpec {
	for _, clique := range template.Cliques {
		if clique != nil && clique.Name == cliqueName {
			return clique
		}
	}
	return nil
}
