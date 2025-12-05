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

package common

// Common label keys to be placed on all resources managed by grove operator.
const (
	// LabelAppNameKey is a key of a label which sets the name of the resource.
	LabelAppNameKey = "app.kubernetes.io/name"
	// LabelManagedByKey is a key of a label which sets the operator which manages this resource.
	LabelManagedByKey = "app.kubernetes.io/managed-by"
	// LabelPartOfKey is a key of a label which sets the type of the resource.
	LabelPartOfKey = "app.kubernetes.io/part-of"
	// LabelManagedByValue is the value for LabelManagedByKey
	LabelManagedByValue = "grove-operator"
	// LabelComponentKey is a key for a label that sets the component type on resources provisioned for a PodCliqueSet.
	LabelComponentKey = "app.kubernetes.io/component"
	// LabelPodClique is a key for a label that sets the PodClique name.
	LabelPodClique = "grove.io/podclique"
	// LabelPodGang is a key for a label that sets the PodGang name.
	LabelPodGang = "grove.io/podgang"
	// LabelBasePodGang is a key for a label that sets the base PodGang name for scaled PodGangs.
	// This label is present on scaled PodGangs (beyond MinAvailable) and points to their base PodGang.
	LabelBasePodGang = "grove.io/base-podgang"
	// LabelPodCliqueSetReplicaIndex is a key for a label that sets the replica index of a PodCliqueSet.
	LabelPodCliqueSetReplicaIndex = "grove.io/podcliqueset-replica-index"
	// LabelPodCliqueScalingGroup is a key for a label that sets the PodCliqueScalingGroup name.
	LabelPodCliqueScalingGroup = "grove.io/podcliquescalinggroup"
	// LabelPodCliqueScalingGroupReplicaIndex is a key for a label that sets the replica index of a PodCliqueScalingGroup within PodCliqueSet.
	LabelPodCliqueScalingGroupReplicaIndex = "grove.io/podcliquescalinggroup-replica-index"
	// LabelPodTemplateHash is a key for a label that sets the hash of the PodSpec. This label will be set on a PodClique and will be shared by all pods in the PodClique.
	LabelPodTemplateHash = "grove.io/pod-template-hash"
	// LabelSchedulerBackend is a key for a label that specifies which scheduler backend should handle the PodGang.
	// Values: "workload" (for default-scheduler), "kai" (for KAI scheduler), etc.
	LabelSchedulerBackend = "grove.io/scheduler-backend"
)

// Labels for setting component names for all managed resources whose lifecycle
// is managed by grove operator and are provisioned as part of a PodCliqueSet
// These component names will be set against LabelComponentKey label key on
// respective components.
const (
	// LabelComponentNamePodCliqueSetReplicaHeadlessService is the label key representing the component name for a
	// Headless service for a PodCliqueSet replica.
	LabelComponentNamePodCliqueSetReplicaHeadlessService = "pcs-headless-service"
	// LabelComponentNamePodRole is the label key representing the component name for a role that is associated to all
	// Pods that are created for a PodCliqueSet.
	LabelComponentNamePodRole = "pod-role"
	// LabelComponentNamePodRoleBinding is the label key representing the component name for a RoleBinding to a Role
	// that is associated to all Pods that are created for a PodCliqueSet.
	LabelComponentNamePodRoleBinding = "pod-role-binding"
	// LabelComponentNamePodServiceAccount is the label key representing the component name  for a ServiceAccount that
	// is used by all Pods that are created for a PodCliqueSet.
	LabelComponentNamePodServiceAccount = "pod-service-account"
	// LabelComponentNameServiceAccountTokenSecret is the label key representing the component name for a Secret for
	// generating service account token that is used by an init container responsible for enforcing start-up ordering in
	// each Pod for a PodCliqueSet.
	LabelComponentNameServiceAccountTokenSecret = "pod-sa-token-secret"
	// LabelComponentNamePodCliqueScalingGroup is the label key representing the component name for a
	// PodCliqueScalingGroup resource.
	LabelComponentNamePodCliqueScalingGroup = "pcs-podcliquescalinggroup"
	// LabelComponentNameHorizontalPodAutoscaler is the label key representing the component name for
	// a HorizontalPodAutoscaler that is created for every PodClique and/or PodCliqueScalingGroup that has
	// ScaleConfig defined.
	LabelComponentNameHorizontalPodAutoscaler = "pcs-hpa"
	// LabelComponentNamePodGang is the label key representing the component name for a PodGang resource.
	LabelComponentNamePodGang = "podgang"
	// LabelComponentNamePodCliqueSetPodClique is the label key representing the component name for a PodClique
	// whose owner is PodCliqueSet. These PodCliques do not belong to any PodCliqueScalingGroup.
	LabelComponentNamePodCliqueSetPodClique = "pcs-podclique"
	// LabelComponentNamePodCliqueScalingGroupPodClique is the label key representing the component name
	// for a PodClique whose owner is a PodCliqueScalingGroup.
	LabelComponentNamePodCliqueScalingGroupPodClique = "pcsg-podclique"
)

// GetDefaultLabelsForPodCliqueSetManagedResources gets the default labels for resources managed by PodCliqueSet.
func GetDefaultLabelsForPodCliqueSetManagedResources(pcsName string) map[string]string {
	return map[string]string{
		LabelManagedByKey: LabelManagedByValue,
		LabelPartOfKey:    pcsName,
	}
}
