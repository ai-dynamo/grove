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

import (
	"fmt"

	"github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

// ResourceNameReplica is a type that holds a resource name and its replica index.
type ResourceNameReplica struct {
	// Name is the name of the resource.
	Name string
	// Replica is the index of the replica within the resource.
	Replica int
}

// GenerateHeadlessServiceName generates a headless service name based on the PodCliqueSet name and replica index.
func GenerateHeadlessServiceName(pcsNameReplica ResourceNameReplica) string {
	return fmt.Sprintf("%s-%d", pcsNameReplica.Name, pcsNameReplica.Replica)
}

// GenerateHeadlessServiceAddress generates a headless service address based on the PodCliqueSet name, replica index, and namespace.
// The address is in the format: <headless-service-name>.<namespace>.svc.cluster.local
func GenerateHeadlessServiceAddress(pcsNameReplica ResourceNameReplica, namespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", GenerateHeadlessServiceName(pcsNameReplica), namespace)
}

// GeneratePodRoleName generates a Pod role name.
// This role will be associated to an init container within each Pod for a PodCliqueSet.
// The init container is created by the operator and is responsible for ensuring start-up order amongst PodCliques.
func GeneratePodRoleName(pcsName string) string {
	return fmt.Sprintf("%s:pcs:%s", v1alpha1.SchemeGroupVersion.Group, pcsName)
}

// GeneratePodRoleBindingName generates a role binding name. The role binding will bind the
// role to the service account that are created for the init container responsible for ensuring start-up order amongst PodCliques.
func GeneratePodRoleBindingName(pcsName string) string {
	return fmt.Sprintf("%s:pcs:%s", v1alpha1.SchemeGroupVersion.Group, pcsName)
}

// GeneratePodServiceAccountName generates a Pod service account used by all the init containers
// within the PodCliqueSet (one per pod) that are responsible for ensuring start-up order amongst PodCliques.
func GeneratePodServiceAccountName(pcsName string) string {
	return pcsName
}

// GenerateInitContainerSATokenSecretName generates a Secret name containing a service account token that will be mounted onto the init container
// responsible for ensuring start-up order amongst PodCliques.
func GenerateInitContainerSATokenSecretName(pcsName string) string {
	return fmt.Sprintf("%s-ic-sat", pcsName)
}

// GenerateLegacyInitContainerSATokenSecretName generates the legacy init-container service account token
// Secret name used before the suffix was shortened. Retained as a migration source and delete target.
//
// Deprecated: scheduled for removal three releases after the shortening (see https://github.com/ai-dynamo/grove/issues/658).
func GenerateLegacyInitContainerSATokenSecretName(pcsName string) string {
	return fmt.Sprintf("%s-initc-sa-token-secret", pcsName)
}

// GeneratePodCliqueName generates a PodClique name based on the PodCliqueSet name, replica index, and PodCliqueTemplate name.
func GeneratePodCliqueName(ownerNameReplica ResourceNameReplica, pclqTemplateName string) string {
	return fmt.Sprintf("%s-%d-%s", ownerNameReplica.Name, ownerNameReplica.Replica, pclqTemplateName)
}

// GeneratePodCliqueScalingGroupName generates a PodCliqueScalingGroup name based on the PodCliqueSet name, replica index and PodCliqueScalingGroup name.
// PodCliqueScalingGroup name is only guaranteed to be unique within the PodCliqueSet, so it is prefixed with the PodCliqueSet name and its replica index.
func GeneratePodCliqueScalingGroupName(pcsNameReplica ResourceNameReplica, pclqScalingGroupName string) string {
	return fmt.Sprintf("%s-%d-%s", pcsNameReplica.Name, pcsNameReplica.Replica, pclqScalingGroupName)
}

// GenerateBasePodGangName generates a base PodGang name for a PodCliqueSet replica.
// This is used for PodGangs that are not part of scaled scaling group replicas.
func GenerateBasePodGangName(pcsNameReplica ResourceNameReplica) string {
	return fmt.Sprintf("%s-%d", pcsNameReplica.Name, pcsNameReplica.Replica)
}

// CreatePodGangNameFromPCSGFQN generates the PodGang name for a replica of a PodCliqueScalingGroup
// when the PCSG name is already fully qualified.
func CreatePodGangNameFromPCSGFQN(pcsgFQN string, scaledPodGangIndex int) string {
	return fmt.Sprintf("%s-%d", pcsgFQN, scaledPodGangIndex)
}

// GeneratePodGangNameForPodCliqueOwnedByPodCliqueSet generates the PodGang name for a PodClique
// that is directly owned by a PodCliqueSet.
func GeneratePodGangNameForPodCliqueOwnedByPodCliqueSet(pcs *v1alpha1.PodCliqueSet, pcsReplicaIndex int) string {
	return GenerateBasePodGangName(ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex})
}

// GeneratePodGangNameForPodCliqueOwnedByPCSG generates the PodGang name for a PodClique
// that is owned by a PodCliqueScalingGroup, using the PCSG object directly (no config lookup needed).
func GeneratePodGangNameForPodCliqueOwnedByPCSG(pcs *v1alpha1.PodCliqueSet, pcsReplicaIndex int, pcsg *v1alpha1.PodCliqueScalingGroup, pcsgReplicaIndex int) string {
	// MinAvailable should always be non-nil due to kubebuilder default and defaulting webhook
	minAvailable := *pcsg.Spec.MinAvailable

	// Apply the same logic as PodGang creation:
	// Replicas 0..(minAvailable-1) → PCS replica PodGang (base PodGang)
	// Replicas minAvailable+ → Scaled PodGangs (0-based indexing)
	if pcsgReplicaIndex < int(minAvailable) {
		return GenerateBasePodGangName(ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex})
	} else {
		// Convert scaling group replica index to 0-based scaled PodGang index
		scaledPodGangIndex := pcsgReplicaIndex - int(minAvailable)
		// Use the PCSG name directly (it's already the FQN)
		return CreatePodGangNameFromPCSGFQN(pcsg.Name, scaledPodGangIndex)
	}
}

// ExtractScalingGroupNameFromPCSGFQN extracts the scaling group name from a PodCliqueScalingGroup FQN.
// For example, "simple1-0-sga" with pcsNameReplica="simple1-0" returns "sga".
func ExtractScalingGroupNameFromPCSGFQN(pcsgFQN string, pcsNameReplica ResourceNameReplica) string {
	prefix := fmt.Sprintf("%s-%d-", pcsNameReplica.Name, pcsNameReplica.Replica)
	return pcsgFQN[len(prefix):]
}
