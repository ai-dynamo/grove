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

//go:build e2e

package utils

import (
	"context"
	"fmt"
	"testing"
	"time"

	kaischedulingv2alpha2 "github.com/NVIDIA/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	nameutils "github.com/ai-dynamo/grove/operator/api/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/utils/ptr"
)

// ExpectedSubGroup defines the expected structure of a KAI PodGroup SubGroup for verification
type ExpectedSubGroup struct {
	Name                   string
	MinMember              int32
	Parent                 *string
	RequiredTopologyLevel  string
	PreferredTopologyLevel string
}

// PCSGCliqueConfig defines configuration for a single clique in a PCSG
type PCSGCliqueConfig struct {
	Name       string
	PodCount   int32
	Constraint string
}

// ScaledPCSGConfig defines configuration for verifying a scaled PCSG replica
type ScaledPCSGConfig struct {
	Name          string
	PCSGName      string
	PCSGReplica   int
	MinAvailable  int
	CliqueConfigs []PCSGCliqueConfig
	Constraint    string
}

// CreateExpectedStandalonePCLQSubGroup creates an ExpectedSubGroup for a standalone PodClique (not in PCSG)
// Name format: <pcs-name>-<pcs-replica>-<clique-name>
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

// CreateExpectedPCSGParentSubGroup creates an ExpectedSubGroup for a PCSG parent (scaling group replica)
// Name format: <pcs-name>-<pcs-replica>-<sg-name>-<sg-replica>
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

// CreateExpectedPCLQInPCSGSubGroup creates an ExpectedSubGroup for a PodClique within a PCSG with parent
// Name format: <pcs-name>-<pcs-replica>-<sg-name>-<sg-replica>-<clique-name>
func CreateExpectedPCLQInPCSGSubGroup(pcsName string, pcsReplica int, sgName string, sgReplica int, cliqueName string, minMember int32, topologyLevel string) ExpectedSubGroup {
	return createExpectedPCLQInPCSGSubGroup(pcsName, pcsReplica, sgName, sgReplica, cliqueName, minMember, topologyLevel, true)
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

// CreateExpectedPCLQInPCSGSubGroupNoParent creates an ExpectedSubGroup for a PodClique within a PCSG without parent
// Used when PCSG has no topology constraint (no parent SubGroup created)
// Name format: <pcs-name>-<pcs-replica>-<sg-name>-<sg-replica>-<clique-name>
func CreateExpectedPCLQInPCSGSubGroupNoParent(pcsName string, pcsReplica int, sgName string, sgReplica int, cliqueName string, minMember int32, topologyLevel string) ExpectedSubGroup {
	return createExpectedPCLQInPCSGSubGroup(pcsName, pcsReplica, sgName, sgReplica, cliqueName, minMember, topologyLevel, false)
}

// GetKAIPodGroupsForPCS retrieves all KAI PodGroups for a given PodCliqueSet by label selector
// KAI scheduler creates PodGroups with label: app.kubernetes.io/part-of=<pcs-name>
// Returns a list of PodGroups that tests can filter by owner reference if needed
func GetKAIPodGroupsForPCS(ctx context.Context, dynamicClient dynamic.Interface, namespace, pcsName string) ([]kaischedulingv2alpha2.PodGroup, error) {
	// List PodGroups using label selector
	labelSelector := fmt.Sprintf("app.kubernetes.io/part-of=%s", pcsName)
	unstructuredList, err := dynamicClient.Resource(kaiPodGroupGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list KAI PodGroups with label %s in namespace %s: %w", labelSelector, namespace, err)
	}

	// Convert all items to typed PodGroups
	podGroups := make([]kaischedulingv2alpha2.PodGroup, 0, len(unstructuredList.Items))
	for _, item := range unstructuredList.Items {
		var podGroup kaischedulingv2alpha2.PodGroup
		if err := ConvertUnstructuredToTyped(item.Object, &podGroup); err != nil {
			return nil, fmt.Errorf("failed to convert KAI PodGroup to typed: %w", err)
		}
		podGroups = append(podGroups, podGroup)
	}

	if len(podGroups) == 0 {
		return nil, fmt.Errorf("no KAI PodGroups found for PCS %s in namespace %s", pcsName, namespace)
	}

	return podGroups, nil
}

// WaitForKAIPodGroups waits for KAI PodGroups for the given PCS to exist and returns them
func WaitForKAIPodGroups(ctx context.Context, dynamicClient dynamic.Interface, namespace, pcsName string, timeout, interval time.Duration, logger *Logger) ([]kaischedulingv2alpha2.PodGroup, error) {
	var podGroups []kaischedulingv2alpha2.PodGroup
	err := PollForCondition(ctx, timeout, interval, func() (bool, error) {
		pgs, err := GetKAIPodGroupsForPCS(ctx, dynamicClient, namespace, pcsName)
		if err != nil {
			logger.Debugf("Waiting for KAI PodGroups for PCS %s/%s: %v", namespace, pcsName, err)
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

// FilterPodGroupByOwner filters a list of PodGroups to find the one owned by the specified PodGang
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

// VerifyKAIPodGroupTopologyConstraint verifies the top-level TopologyConstraint of a KAI PodGroup
func VerifyKAIPodGroupTopologyConstraint(podGroup *kaischedulingv2alpha2.PodGroup, expectedRequired, expectedPreferred string, logger *Logger) error {
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

	logger.Infof("KAI PodGroup %s top-level TopologyConstraint verified: required=%q, preferred=%q",
		podGroup.Name, actualRequired, actualPreferred)
	return nil
}

// VerifyKAIPodGroupSubGroups verifies the SubGroups of a KAI PodGroup
func VerifyKAIPodGroupSubGroups(podGroup *kaischedulingv2alpha2.PodGroup, expectedSubGroups []ExpectedSubGroup, logger *Logger) error {
	if len(podGroup.Spec.SubGroups) != len(expectedSubGroups) {
		return fmt.Errorf("KAI PodGroup %s has %d SubGroups, expected %d",
			podGroup.Name, len(podGroup.Spec.SubGroups), len(expectedSubGroups))
	}

	// Build a map of actual SubGroups by name for easier lookup
	actualSubGroups := make(map[string]kaischedulingv2alpha2.SubGroup)
	for _, sg := range podGroup.Spec.SubGroups {
		actualSubGroups[sg.Name] = sg
	}

	for _, expected := range expectedSubGroups {
		actual, ok := actualSubGroups[expected.Name]
		if !ok {
			return fmt.Errorf("KAI PodGroup %s missing expected SubGroup %q", podGroup.Name, expected.Name)
		}

		// Verify Parent
		if expected.Parent == nil && actual.Parent != nil {
			return fmt.Errorf("SubGroup %q Parent: got %q, expected nil", expected.Name, *actual.Parent)
		}
		if expected.Parent != nil && actual.Parent == nil {
			return fmt.Errorf("SubGroup %q Parent: got nil, expected %q", expected.Name, *expected.Parent)
		}
		if expected.Parent != nil && actual.Parent != nil && *expected.Parent != *actual.Parent {
			return fmt.Errorf("SubGroup %q Parent: got %q, expected %q", expected.Name, *actual.Parent, *expected.Parent)
		}

		// Verify MinMember
		if actual.MinMember != expected.MinMember {
			return fmt.Errorf("SubGroup %q MinMember: got %d, expected %d", expected.Name, actual.MinMember, expected.MinMember)
		}

		// Verify TopologyConstraint
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

		logger.Debugf("SubGroup %q verified: parent=%v, minMember=%d, required=%q, preferred=%q",
			expected.Name, actual.Parent, actual.MinMember, actualRequired, actualPreferred)
	}

	logger.Infof("KAI PodGroup %s verified with %d SubGroups", podGroup.Name, len(expectedSubGroups))
	return nil
}

// GetPodGroupForBasePodGangReplica retrieves the KAI PodGroup of the corresponding PodGang
// which is the base PodGang of specific PodGangSet replica.
// For a PodGangSet workload "my-workload", replica 0's base PodGang is "my-workload-0".
func GetPodGroupForBasePodGangReplica(
	ctx context.Context,
	dynamicClient dynamic.Interface,
	namespace string,
	workloadName string,
	pgsReplica int,
	timeout time.Duration,
	interval time.Duration,
	logger *Logger,
) (*kaischedulingv2alpha2.PodGroup, error) {
	podGroups, err := WaitForKAIPodGroups(ctx, dynamicClient, namespace, workloadName, timeout, interval, logger)
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
func VerifyPodGroupTopology(
	podGroup *kaischedulingv2alpha2.PodGroup,
	requiredLevel, preferredLevel string,
	expectedSubGroups []ExpectedSubGroup,
	logger *Logger,
) error {
	if err := VerifyKAIPodGroupTopologyConstraint(podGroup, requiredLevel, preferredLevel, logger); err != nil {
		return fmt.Errorf("top-level constraint verification failed: %w", err)
	}

	if err := VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		return fmt.Errorf("SubGroups verification failed: %w", err)
	}

	return nil
}

// VerifyScaledPCSGReplicaTopology verifies KAI PodGroup for ONE scaled PCSG replica.
// Scaled PodGroup top-level constraint: uses pcsConstraint ONLY if PCSG has NO constraint.
func VerifyScaledPCSGReplicaTopology(
	ctx context.Context,
	t *testing.T,
	dynamicClient dynamic.Interface,
	namespace string,
	pcsName string,
	pcsReplica int,
	pcsgConfig ScaledPCSGConfig,
	pcsConstraint string,
	logger *Logger,
) {
	podGroups, err := GetKAIPodGroupsForPCS(ctx, dynamicClient, namespace, pcsName)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	pcsgFQN := nameutils.GeneratePodCliqueScalingGroupName(
		nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica},
		pcsgConfig.PCSGName,
	)

	scaledPodGangName := nameutils.CreatePodGangNameFromPCSGFQN(pcsgFQN, pcsgConfig.PCSGReplica-pcsgConfig.MinAvailable)

	scaledPodGroup, err := FilterPodGroupByOwner(podGroups, scaledPodGangName)
	if err != nil {
		t.Fatalf("Failed to find scaled PodGroup for %s: %v", scaledPodGangName, err)
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

	if err := VerifyPodGroupTopology(scaledPodGroup, scaledTopConstraint, "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify scaled PodGroup %s (%s replica %d) topology: %v",
			scaledPodGangName, pcsgConfig.Name, pcsgConfig.PCSGReplica, err)
	}
}
