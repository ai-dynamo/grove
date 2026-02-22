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

package utils

import (
	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HasExpectedOwner checks if the owner references of a resource match the expected owner kind.
func HasExpectedOwner(expectedOwnerKind string, ownerRefs []metav1.OwnerReference) bool {
	if len(ownerRefs) == 0 || len(ownerRefs) > 1 {
		return false
	}
	return ownerRefs[0].Kind == expectedOwnerKind
}

// IsManagedByGrove checks if the pod is managed by Grove by inspecting its labels.
// All grove managed resources will have a standard set of default labels.
func IsManagedByGrove(labels map[string]string) bool {
	if val, ok := labels[apicommon.LabelManagedByKey]; ok {
		return apicommon.LabelManagedByValue == val
	}
	return false
}

// IsManagedPodClique checks if the PodClique is managed by Grove.
func IsManagedPodClique(obj client.Object, expectedOwnerKinds ...string) bool {
	podClique, ok := obj.(*grovecorev1alpha1.PodClique)
	if !ok {
		return false
	}
	hasExpectedOwner := lo.Reduce(expectedOwnerKinds, func(acc bool, expectedOwnerKind string, _ int) bool {
		return acc || HasExpectedOwner(expectedOwnerKind, podClique.GetOwnerReferences())
	}, false)
	return IsManagedByGrove(podClique.GetLabels()) && hasExpectedOwner
}

// IsManagedPodGang checks if the PodGang is managed by Grove.
func IsManagedPodGang(obj client.Object) bool {
	podGang, ok := obj.(*groveschedulerv1alpha1.PodGang)
	if !ok {
		return false
	}
	return IsManagedByGrove(podGang.Labels)
}
