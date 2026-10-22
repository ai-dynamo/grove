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

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InitSpec configures the initial (and every re-) deployment of a resource's replicas.
// It is carried by PodCliqueScalingGroups and PodCliques only.
type InitSpec struct {
	// MinReplicas is the guaranteed minimum number of replicas gang-scheduled together when the
	// resource is first deployed and whenever it, or one of its replicas, is recreated.
	// Defaults to termination.resource.availability.minReplicas when availability is set, and to 1 otherwise.
	// It must not be greater than the template's replicas, nor less than availability.minReplicas.
	// Changing it changes the gang shape: every PodCliqueSet replica is recreated, one at a time.
	// +optional
	MinReplicas *int32 `json:"minReplicas,omitempty"`
}

// TerminationSpec defines the terminal conditions of a resource.
type TerminationSpec struct {
	// Resource defines resource-scope terminal conditions.
	// +optional
	Resource *ResourceTerminationSpec `json:"resource,omitempty"`
}

// ResourceTerminationSpec defines the resource-scope terminal conditions of a resource.
type ResourceTerminationSpec struct {
	// Availability defines the minimum requirement for a long-running resource to be available.
	// Nil means MinAvailableBreached is permanently False, so the resource never triggers gang termination.
	// It cannot be added or removed after creation.
	// +optional
	Availability *AvailabilityPolicy `json:"availability,omitempty"`
}

// AvailabilityPolicy defines the availability floor of a resource and how long a breach of it is tolerated.
type AvailabilityPolicy struct {
	// MinReplicas is the minimum number of available replicas. Defaults to 1.
	// It must not be greater than init.minReplicas. It can be changed, and the new floor applies to every
	// replica in place: raising it above the available replicas makes MinAvailableBreached True at once.
	// +optional
	MinReplicas *int32 `json:"minReplicas,omitempty"`
	// BreachTolerance is how long MinAvailableBreached may stay True before the PodCliqueSet replica that the
	// resource belongs to (or, for a PodCliqueScalingGroup member PodClique, its PodCliqueScalingGroup replica)
	// is gang-terminated. Defaults to 4h.
	// +optional
	BreachTolerance *metav1.Duration `json:"breachTolerance,omitempty"`
}
