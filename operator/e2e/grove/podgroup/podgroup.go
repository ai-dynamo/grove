//go:build e2e

// /*
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
// */

package podgroup

import (
	"context"
	"fmt"
	"time"

	nameutils "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
)

var kaiPodGroupGVR = schema.GroupVersionResource{
	Group:    "scheduling.run.ai",
	Version:  "v2alpha2",
	Resource: "podgroups",
}

// ExpectedSubGroup defines the expected structure of a KAI PodGroup SubGroup for verification.
type ExpectedSubGroup struct {
	Name                   string
	MinMember              int32
	Parent                 *string
	RequiredTopologyLevel  string
	PreferredTopologyLevel string
}

// PCSGCliqueConfig defines configuration for a single clique in a PCSG.
type PCSGCliqueConfig struct {
	Name       string
	PodCount   int32
	Constraint string
}

// ScaledPCSGConfig defines configuration for verifying a scaled PCSG replica.
type ScaledPCSGConfig struct {
	Name          string
	PCSGName      string
	PCSGReplica   int
	MinAvailable  int
	CliqueConfigs []PCSGCliqueConfig
	Constraint    string
}

// PodGroupVerifier provides KAI PodGroup verification using pre-created Kubernetes clients.
type PodGroupVerifier struct {
	clients *clients.Clients
	logger  *log.Logger
}

// NewPodGroupVerifier creates a PodGroupVerifier bound to the given clients.
func NewPodGroupVerifier(clients *clients.Clients, logger *log.Logger) *PodGroupVerifier {
	return &PodGroupVerifier{clients: clients, logger: logger}
}

// CreateExpectedStandalonePCLQSubGroup creates an ExpectedSubGroup for a standalone PodClique (not in PCSG).
func CreateExpectedStandalonePCLQSubGroup(pcsName string, pcsReplica int, cliqueName string, minMember int32, topologyLevel string) ExpectedSubGroup {
	name := nameutils.GeneratePodCliqueName(
		nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica},
		cliqueName,
	)
	return ExpectedSubGroup{
		Name:                  name,
		MinMember:             minMember,
		Parent:                nil,
		RequiredTopologyLevel: topologyLevel,
	}
}

// CreateExpectedPCSGParentSubGroup creates an ExpectedSubGroup for a PCSG parent (scaling group replica).
func CreateExpectedPCSGParentSubGroup(pcsName string, pcsReplica int, sgName string, sgReplica int, topologyLevel string) ExpectedSubGroup {
	pcsgFQN := nameutils.GeneratePodCliqueScalingGroupName(
		nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica},
		sgName,
	)
	name := fmt.Sprintf("%s-%d", pcsgFQN, sgReplica)
	return ExpectedSubGroup{
		Name:                  name,
		MinMember:             0,
		Parent:                nil,
		RequiredTopologyLevel: topologyLevel,
	}
}

// CreateExpectedPCLQInPCSGSubGroup creates an ExpectedSubGroup for a PodClique within a PCSG with parent.
func CreateExpectedPCLQInPCSGSubGroup(pcsName string, pcsReplica int, sgName string, sgReplica int, cliqueName string, minMember int32, topologyLevel string) ExpectedSubGroup {
	return createExpectedPCLQInPCSGSubGroup(pcsName, pcsReplica, sgName, sgReplica, cliqueName, minMember, topologyLevel, true)
}

// CreateExpectedPCLQInPCSGSubGroupNoParent creates an ExpectedSubGroup for a PodClique within a PCSG without parent.
func CreateExpectedPCLQInPCSGSubGroupNoParent(pcsName string, pcsReplica int, sgName string, sgReplica int, cliqueName string, minMember int32, topologyLevel string) ExpectedSubGroup {
	return createExpectedPCLQInPCSGSubGroup(pcsName, pcsReplica, sgName, sgReplica, cliqueName, minMember, topologyLevel, false)
}

func createExpectedPCLQInPCSGSubGroup(pcsName string, pcsReplica int, sgName string, sgReplica int, cliqueName string,
	minMember int32, topologyLevel string, hasParent bool) ExpectedSubGroup {
	pcsgFQN := nameutils.GeneratePodCliqueScalingGroupName(
		nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica},
		sgName,
	)
	name := nameutils.GeneratePodCliqueName(
		nameutils.ResourceNameReplica{Name: pcsgFQN, Replica: sgReplica},
		cliqueName,
	)
	var parentPtr *string
	if hasParent {
		parentPtr = ptr.To(fmt.Sprintf("%s-%d", pcsgFQN, sgReplica))
	}
	return ExpectedSubGroup{
		Name:                  name,
		MinMember:             minMember,
		Parent:                parentPtr,
		RequiredTopologyLevel: topologyLevel,
	}
}

// GetKAIPodGroupsForPCS retrieves all KAI PodGroups for a given PodCliqueSet by label selector.
func (pv *PodGroupVerifier) GetKAIPodGroupsForPCS(ctx context.Context, namespace, pcsName string) ([]kaischedulingv2alpha2.PodGroup, error) {
	labelSelector := fmt.Sprintf("app.kubernetes.io/part-of=%s", pcsName)
	unstructuredList, err := pv.clients.DynamicClient.Resource(kaiPodGroupGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list KAI PodGroups with label %s in namespace %s: %w", labelSelector, namespace, err)
	}

	podGroups := make([]kaischedulingv2alpha2.PodGroup, 0, len(unstructuredList.Items))
	for _, item := range unstructuredList.Items {
		var podGroup kaischedulingv2alpha2.PodGroup
		if err := k8s.ConvertUnstructuredToTyped(item.Object, &podGroup); err != nil {
			return nil, fmt.Errorf("failed to convert KAI PodGroup to typed: %w", err)
		}
		podGroups = append(podGroups, podGroup)
	}

	if len(podGroups) == 0 {
		return nil, fmt.Errorf("no KAI PodGroups found for PCS %s in namespace %s", pcsName, namespace)
	}

	return podGroups, nil
}

// WaitForKAIPodGroups waits for KAI PodGroups for the given PCS to exist and returns them.
func (pv *PodGroupVerifier) WaitForKAIPodGroups(ctx context.Context, namespace, pcsName string, timeout, interval time.Duration) ([]kaischedulingv2alpha2.PodGroup, error) {
	var podGroups []kaischedulingv2alpha2.PodGroup
	err := k8s.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		pgs, err := pv.GetKAIPodGroupsForPCS(ctx, namespace, pcsName)
		if err != nil {
			pv.logger.Debugf("Waiting for KAI PodGroups for PCS %s/%s: %v", namespace, pcsName, err)
			return false, nil
		}
		podGroups = pgs
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("timed out waiting for KAI PodGroups for PCS %s/%s: %w", namespace, pcsName, err)
	}
	return podGroups, nil
}

// FilterPodGroupByOwner filters a list of PodGroups to find the one owned by the specified PodGang.
func FilterPodGroupByOwner(podGroups []kaischedulingv2alpha2.PodGroup, podGangName string) (*kaischedulingv2alpha2.PodGroup, error) {
	for i := range podGroups {
		for _, ref := range podGroups[i].OwnerReferences {
			if ref.Kind == "PodGang" && ref.Name == podGangName {
				return &podGroups[i], nil
			}
		}
	}
	return nil, fmt.Errorf("no PodGroup found owned by PodGang %s", podGangName)
}

// VerifyTopologyConstraint verifies the top-level TopologyConstraint of a KAI PodGroup.
func (pv *PodGroupVerifier) VerifyTopologyConstraint(podGroup *kaischedulingv2alpha2.PodGroup, expectedRequired, expectedPreferred string) error {
	actualRequired := podGroup.Spec.TopologyConstraint.RequiredTopologyLevel
	actualPreferred := podGroup.Spec.TopologyConstraint.PreferredTopologyLevel

	if actualRequired != expectedRequired {
		return fmt.Errorf("KAI PodGroup %s top-level RequiredTopologyLevel: got %q, expected %q",
			podGroup.Name, actualRequired, expectedRequired)
	}

	if actualPreferred != expectedPreferred {
		return fmt.Errorf("KAI PodGroup %s top-level PreferredTopologyLevel: got %q, expected %q",
			podGroup.Name, actualPreferred, expectedPreferred)
	}

	pv.logger.Infof("KAI PodGroup %s top-level TopologyConstraint verified: required=%q, preferred=%q",
		podGroup.Name, actualRequired, actualPreferred)
	return nil
}

// VerifySubGroups verifies the SubGroups of a KAI PodGroup.
func (pv *PodGroupVerifier) VerifySubGroups(podGroup *kaischedulingv2alpha2.PodGroup, expectedSubGroups []ExpectedSubGroup) error {
	if len(podGroup.Spec.SubGroups) != len(expectedSubGroups) {
		return fmt.Errorf("KAI PodGroup %s has %d SubGroups, expected %d",
			podGroup.Name, len(podGroup.Spec.SubGroups), len(expectedSubGroups))
	}

	actualSubGroups := make(map[string]kaischedulingv2alpha2.SubGroup)
	for _, sg := range podGroup.Spec.SubGroups {
		actualSubGroups[sg.Name] = sg
	}

	for _, expected := range expectedSubGroups {
		actual, ok := actualSubGroups[expected.Name]
		if !ok {
			return fmt.Errorf("KAI PodGroup %s missing expected SubGroup %q", podGroup.Name, expected.Name)
		}

		if expected.Parent == nil && actual.Parent != nil {
			return fmt.Errorf("SubGroup %q Parent: got %q, expected nil", expected.Name, *actual.Parent)
		}
		if expected.Parent != nil && actual.Parent == nil {
			return fmt.Errorf("SubGroup %q Parent: got nil, expected %q", expected.Name, *expected.Parent)
		}
		if expected.Parent != nil && actual.Parent != nil && *expected.Parent != *actual.Parent {
			return fmt.Errorf("SubGroup %q Parent: got %q, expected %q", expected.Name, *actual.Parent, *expected.Parent)
		}

		if actual.MinMember != expected.MinMember {
			return fmt.Errorf("SubGroup %q MinMember: got %d, expected %d", expected.Name, actual.MinMember, expected.MinMember)
		}

		actualRequired := ""
		actualPreferred := ""
		if actual.TopologyConstraint != nil {
			actualRequired = actual.TopologyConstraint.RequiredTopologyLevel
			actualPreferred = actual.TopologyConstraint.PreferredTopologyLevel
		}

		if actualRequired != expected.RequiredTopologyLevel {
			return fmt.Errorf("SubGroup %q RequiredTopologyLevel: got %q, expected %q",
				expected.Name, actualRequired, expected.RequiredTopologyLevel)
		}
		if actualPreferred != expected.PreferredTopologyLevel {
			return fmt.Errorf("SubGroup %q PreferredTopologyLevel: got %q, expected %q",
				expected.Name, actualPreferred, expected.PreferredTopologyLevel)
		}

		pv.logger.Debugf("SubGroup %q verified: parent=%v, minMember=%d, required=%q, preferred=%q",
			expected.Name, actual.Parent, actual.MinMember, actualRequired, actualPreferred)
	}

	pv.logger.Infof("KAI PodGroup %s verified with %d SubGroups", podGroup.Name, len(expectedSubGroups))
	return nil
}

// GetPodGroupForBasePodGangReplica retrieves the KAI PodGroup for a PodGangSet replica's base PodGang.
func (pv *PodGroupVerifier) GetPodGroupForBasePodGangReplica(ctx context.Context, namespace, workloadName string, pgsReplica int, timeout, interval time.Duration) (*kaischedulingv2alpha2.PodGroup, error) {
	podGroups, err := pv.WaitForKAIPodGroups(ctx, namespace, workloadName, timeout, interval)
	if err != nil {
		return nil, fmt.Errorf("failed to get KAI PodGroups: %w", err)
	}

	basePodGangName := nameutils.GenerateBasePodGangName(nameutils.ResourceNameReplica{Name: workloadName, Replica: pgsReplica})
	basePodGroup, err := FilterPodGroupByOwner(podGroups, basePodGangName)
	if err != nil {
		return nil, fmt.Errorf("failed to find PodGroup for PodGang %s: %w", basePodGangName, err)
	}

	return basePodGroup, nil
}

// VerifyPodGroupTopology verifies both top-level topology constraint and SubGroups structure.
func (pv *PodGroupVerifier) VerifyPodGroupTopology(podGroup *kaischedulingv2alpha2.PodGroup, requiredLevel, preferredLevel string, expectedSubGroups []ExpectedSubGroup) error {
	if err := pv.VerifyTopologyConstraint(podGroup, requiredLevel, preferredLevel); err != nil {
		return fmt.Errorf("top-level constraint verification failed: %w", err)
	}

	if err := pv.VerifySubGroups(podGroup, expectedSubGroups); err != nil {
		return fmt.Errorf("SubGroups verification failed: %w", err)
	}

	return nil
}

// VerifyScaledPCSGReplicaTopology verifies KAI PodGroup for one scaled PCSG replica.
func (pv *PodGroupVerifier) VerifyScaledPCSGReplicaTopology(ctx context.Context, namespace, pcsName string, pcsReplica int, pcsgConfig ScaledPCSGConfig, pcsConstraint string) error {
	podGroups, err := pv.GetKAIPodGroupsForPCS(ctx, namespace, pcsName)
	if err != nil {
		return fmt.Errorf("failed to get KAI PodGroups: %w", err)
	}

	pcsgFQN := nameutils.GeneratePodCliqueScalingGroupName(
		nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica},
		pcsgConfig.PCSGName,
	)

	scaledPodGangName := nameutils.CreatePodGangNameFromPCSGFQN(pcsgFQN, pcsgConfig.PCSGReplica-pcsgConfig.MinAvailable)

	scaledPodGroup, err := FilterPodGroupByOwner(podGroups, scaledPodGangName)
	if err != nil {
		return fmt.Errorf("failed to find scaled PodGroup for %s: %w", scaledPodGangName, err)
	}

	var expectedSubGroups []ExpectedSubGroup
	for _, cliqueConfig := range pcsgConfig.CliqueConfigs {
		expectedSubGroups = append(expectedSubGroups,
			CreateExpectedPCLQInPCSGSubGroupNoParent(pcsName, pcsReplica, pcsgConfig.PCSGName, pcsgConfig.PCSGReplica, cliqueConfig.Name, cliqueConfig.PodCount, cliqueConfig.Constraint))
	}

	scaledTopConstraint := pcsConstraint
	if pcsgConfig.Constraint != "" {
		scaledTopConstraint = pcsgConfig.Constraint
	}

	return pv.VerifyPodGroupTopology(scaledPodGroup, scaledTopConstraint, "", expectedSubGroups)
}
