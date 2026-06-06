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

// GenerateBasePodGangName generates a legacy base PodGang name (shape: `<pcs-name>-<pcs-replica-index>`)
// for a PodCliqueSet replica. All existing PodCliqueSet resources that already have base PodGang with
// this naming scheme will be preserved till they are updated via coherent update. For all new PodCliqueSet resources
// this naming scheme will no longer be used.
// Use GeneratePodGangName instead for all newly created PodGangs.
func GenerateBasePodGangName(pcsNameReplica ResourceNameReplica) string {
	return fmt.Sprintf("%s-%d", pcsNameReplica.Name, pcsNameReplica.Replica)
}

// CreatePodGangNameFromPCSGFQN generates a legacy scaled PodGang name (shape: `<pcsg-fqn>-<index>`)
// for each replica of PodCliqueScalingGroup above the minAvailable. All existing PodCliqueSet resources that
// already have scaled PodGangs with this naming scheme will be preserved till they are updated via coherent update.
// For all new PodCliqueSet resources this naming scheme to create scaled PodGangs will no longer be used.
// Use GeneratePodGangName instead for all newly created PodGangs.
func CreatePodGangNameFromPCSGFQN(pcsgFQN string, scaledPodGangIndex int) string {
	return fmt.Sprintf("%s-%d", pcsgFQN, scaledPodGangIndex)
}

// ExtractScalingGroupNameFromPCSGFQN extracts the scaling group name from a PodCliqueScalingGroup FQN.
// For example, "simple1-0-sga" with pcsNameReplica="simple1-0" returns "sga".
func ExtractScalingGroupNameFromPCSGFQN(pcsgFQN string, pcsNameReplica ResourceNameReplica) string {
	prefix := fmt.Sprintf("%s-%d-", pcsNameReplica.Name, pcsNameReplica.Replica)
	return pcsgFQN[len(prefix):]
}

// GeneratePodGangName generates a PodGang name following the unified naming convention
// for all PodGangs created for the PodCliqueSet.
// Format: <pcs-name>-<pcs-replica-index>-<unique-suffix>
//
// `uniqueSuffix` is opaque to this function. The caller is responsible for ensuring that
// every name produced for the same (pcsName, replicaIndex) is unique — both across
// reconciles and within a single reconcile call when generating multiple names.
//
// This is the unified PodGang naming convention and applies to all update strategies
// including Coherent and RollingRecreate, as well as initial deployment. Over time,
// the legacy BasePodGang and ScaledPodGang naming conventions
// (GenerateBasePodGangName, CreatePodGangNameFromPCSGFQN) will be retired in favor
// of this scheme.
func GeneratePodGangName(pcsName string, replicaIndex int32, uniqueSuffix string) string {
	return fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, uniqueSuffix)
}

// GeneratePodGangMapName generates a PodGangMap resource name for a PodCliqueSet replica.
// One PodGangMap exists per PodCliqueSet replica, named <pcs-name>-<pcs-replica-index>.
func GeneratePodGangMapName(pcsNameReplica ResourceNameReplica) string {
	return fmt.Sprintf("%s-%d", pcsNameReplica.Name, pcsNameReplica.Replica)
}
