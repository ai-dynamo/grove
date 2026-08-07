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

package common

import (
	"fmt"
	"strconv"
	"strings"

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
// Deprecated: retained for migration after shortening the suffix to "-ic-sat".
// Expected in v0.1.0-alpha.12; remove three releases later.
// Track removal in https://github.com/ai-dynamo/grove/issues/658.
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

// GenerateBasePodGangName generates a legacy base PodGang name (shape: `<pcs-name>-<pcs-replica-index>`)
// for a PodCliqueSet replica. It is a recognizer for PodGangs created before the hash-based naming
// scheme. Migration converts these to the new scheme. New PodGangs use GenerateAnchorPodGangName.
func GenerateBasePodGangName(pcsNameReplica ResourceNameReplica) string {
	return fmt.Sprintf("%s-%d", pcsNameReplica.Name, pcsNameReplica.Replica)
}

// CreatePodGangNameFromPCSGFQN generates a legacy scaled PodGang name (shape: `<pcsg-fqn>-<index>`)
// for each replica of PodCliqueScalingGroup above the minAvailable. It is a recognizer for PodGangs
// created before the hash-based naming scheme. Migration converts these to the new scheme. New
// PodGangs use GenerateNonAnchorPodGangName.
func CreatePodGangNameFromPCSGFQN(pcsgFQN string, scaledPodGangIndex int) string {
	return fmt.Sprintf("%s-%d", pcsgFQN, scaledPodGangIndex)
}

// GenerateAnchorPodGangName generates the name of an anchor PodGang.
// Format: <pcs-name>-<pcs-replica-index>-<pcs-generation-hash>-<anchor-index>.
// anchorIndex is 0 for the single anchor of a generation. A coherent update that creates additional
// anchors of the same generation hash uses the next index. The PodGangMap writer authors the anchor
// name once. Every other reconciler reads it and never recomputes it.
func GenerateAnchorPodGangName(pcsNameReplica ResourceNameReplica, pcsGenerationHash string, anchorIndex int32) string {
	return fmt.Sprintf("%s-%d-%s-%d", pcsNameReplica.Name, pcsNameReplica.Replica, pcsGenerationHash, anchorIndex)
}

// GenerateNonAnchorPodGangName generates the name of a non-anchor PodGang.
// Format: <pcs-name>-<pcs-replica-index>-<pcs-generation-hash>-<pcsg-name>-<pcsg-replica-index>.
// One non-anchor PodGang exists per PodCliqueScalingGroup replica index. The name is deterministic
// and carries no epoch. The pcsgName segment keeps replica indices of different PodCliqueScalingGroups
// from colliding. Each PodCliqueScalingGroup numbers its replicas from 0.
func GenerateNonAnchorPodGangName(pcsNameReplica ResourceNameReplica, pcsGenerationHash, pcsgName string, pcsgReplicaIndex int32) string {
	return fmt.Sprintf("%s-%d-%s-%s-%d", pcsNameReplica.Name, pcsNameReplica.Replica, pcsGenerationHash, pcsgName, pcsgReplicaIndex)
}

// GeneratePodGangMapName generates a PodGangMap resource name for a PodCliqueSet replica.
// One PodGangMap exists per PodCliqueSet replica, named <pcs-name>-<pcs-replica-index>.
func GeneratePodGangMapName(pcsNameReplica ResourceNameReplica) string {
	return fmt.Sprintf("%s-%d", pcsNameReplica.Name, pcsNameReplica.Replica)
}

// ExtractPCSGNameAndIndexFromPodGangName parses a non-anchor PodGang name back into its
// PodCliqueScalingGroup name and replica index. It is the inverse of GenerateNonAnchorPodGangName.
// It builds the known <pcs-name>-<pcs-replica-index>-<pcs-generation-hash>- prefix and strips it
// first. The remainder is split on the last dash into the PodCliqueScalingGroup name and the integer
// index. Stripping the known prefix first keeps the split correct even when the PodCliqueScalingGroup
// name contains dashes. It returns an error when podGangName does not carry the prefix or the index
// is not an integer.
func ExtractPCSGNameAndIndexFromPodGangName(podGangName string, pcsNameReplica ResourceNameReplica, pcsGenerationHash string) (pcsgName string, index int32, err error) {
	prefix := fmt.Sprintf("%s-%d-%s-", pcsNameReplica.Name, pcsNameReplica.Replica, pcsGenerationHash)
	if !strings.HasPrefix(podGangName, prefix) {
		return "", 0, fmt.Errorf("PodGang name %q does not carry the expected prefix %q", podGangName, prefix)
	}
	remainder := podGangName[len(prefix):]
	lastDash := strings.LastIndex(remainder, "-")
	if lastDash <= 0 || lastDash == len(remainder)-1 {
		return "", 0, fmt.Errorf("PodGang name %q has no <pcsg-name>-<index> remainder after prefix %q", podGangName, prefix)
	}
	parsedIndex, err := strconv.Atoi(remainder[lastDash+1:])
	if err != nil {
		return "", 0, fmt.Errorf("trailing index of PodGang name %q is not an integer: %w", podGangName, err)
	}
	return remainder[:lastDash], int32(parsedIndex), nil
}

// ExtractScalingGroupNameFromPCSGFQN extracts the scaling group name from a PodCliqueScalingGroup FQN.
// For example, "simple1-0-sga" with pcsNameReplica="simple1-0" returns "sga".
func ExtractScalingGroupNameFromPCSGFQN(pcsgFQN string, pcsNameReplica ResourceNameReplica) (string, error) {
	prefix := fmt.Sprintf("%s-%d-", pcsNameReplica.Name, pcsNameReplica.Replica)
	if !strings.HasPrefix(pcsgFQN, prefix) {
		return "", fmt.Errorf("FQN %q does not have expected prefix %q", pcsgFQN, prefix)
	}
	return pcsgFQN[len(prefix):], nil
}

// ExtractPodCliqueNameFromStandalonePCLQFQN extracts the unqualified PodClique template name from a
// standalone PodClique FQN. For example, "simple1-0-frontend" with pcsNameReplica="simple1-0"
// returns "frontend". The caller must pass a standalone PodClique FQN.
func ExtractPodCliqueNameFromStandalonePCLQFQN(pclqFQN string, pcsNameReplica ResourceNameReplica) string {
	prefix := fmt.Sprintf("%s-%d-", pcsNameReplica.Name, pcsNameReplica.Replica)
	return pclqFQN[len(prefix):]
}
