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

package kubernetes

import (
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FilterMapOwnedResourceNames filters the candidate resources and returns the names of those that are owned by the given owner object meta.
func FilterMapOwnedResourceNames(ownerObjMeta metav1.ObjectMeta, candidateResources []metav1.PartialObjectMetadata) []string {
	return lo.FilterMap(candidateResources, func(objMeta metav1.PartialObjectMetadata, _ int) (string, bool) {
		if metav1.IsControlledBy(&objMeta, &ownerObjMeta) {
			return objMeta.Name, true
		}
		return "", false
	})
}

// FilterOwnedResourceNamesAndAnnotation returns the list of owned resource names plus a
// name→annotationValue map extracted from the given annotation key. Entries whose annotation
// is absent are omitted from the map (not short-circuited from the name list). Callers use the
// map as a cache for spec-hash style short-circuits without a per-object Get.
func FilterOwnedResourceNamesAndAnnotation(ownerObjMeta metav1.ObjectMeta, candidateResources []metav1.PartialObjectMetadata, annotationKey string) ([]string, map[string]string) {
	names := make([]string, 0, len(candidateResources))
	annotations := make(map[string]string, len(candidateResources))
	for _, objMeta := range candidateResources {
		if !metav1.IsControlledBy(&objMeta, &ownerObjMeta) {
			continue
		}
		names = append(names, objMeta.Name)
		if v := objMeta.Annotations[annotationKey]; v != "" {
			annotations[objMeta.Name] = v
		}
	}
	return names, annotations
}

// GetFirstOwnerName returns the name of the first owner reference of the resource object meta.
func GetFirstOwnerName(resourceObjMeta metav1.ObjectMeta) string {
	if len(resourceObjMeta.OwnerReferences) == 0 {
		return ""
	}
	return resourceObjMeta.OwnerReferences[0].Name
}

// FindOwnerRefByKind returns the first OwnerReference matching the given kind, or nil if none match.
func FindOwnerRefByKind(ownerRefs []metav1.OwnerReference, kind string) *metav1.OwnerReference {
	for i := range ownerRefs {
		if ownerRefs[i].Kind == kind {
			return &ownerRefs[i]
		}
	}
	return nil
}

// GetObjectKeyFromObjectMeta creates a client.ObjectKey from the given ObjectMeta.
func GetObjectKeyFromObjectMeta(objMeta metav1.ObjectMeta) client.ObjectKey {
	return client.ObjectKey{
		Namespace: objMeta.Namespace,
		Name:      objMeta.Name,
	}
}

// IsResourceTerminating checks if a deletion timestamp is set. If it is set it returns true else false.
func IsResourceTerminating(objMeta metav1.ObjectMeta) bool {
	return objMeta.GetDeletionTimestamp() != nil
}
