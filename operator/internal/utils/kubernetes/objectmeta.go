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

package kubernetes

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ObjectPtr is a constraint for generic helpers that receive a slice of API objects
// by value (e.g. List().Items) but need to call pointer-receiver methods such as
// GetName. It requires PT to be *T and for *T to implement client.Object.
type ObjectPtr[T any] interface {
	*T
	client.Object
}

// FilterMapOwnedResourceNames filters the candidate typed objects and returns the names of those that are controlled by the given owner object meta.
// The type parameter is constrained so that *T implements client.Object, allowing List results (which are slices of values) to be passed directly.
func FilterMapOwnedResourceNames[T any, PT ObjectPtr[T]](ownerObjMeta metav1.ObjectMeta, candidateResources []T) []string {
	names := make([]string, 0, len(candidateResources))
	for i := range candidateResources {
		obj := PT(&candidateResources[i]) // pointer into the slice, no copy
		if metav1.IsControlledBy(obj, &ownerObjMeta) {
			names = append(names, obj.GetName())
		}
	}
	return names
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
