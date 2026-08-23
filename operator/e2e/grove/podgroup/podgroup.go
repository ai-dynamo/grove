//go:build e2e

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

package podgroup

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	nameutils "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const scaledPodGangsSubGroupName = "scaled-podgangs"

// ExpectedSubGroup defines the expected structure of a KAI PodGroup SubGroup for verification.
type ExpectedSubGroup struct {
	Name                   string
	MinMember              int32
	MinSubGroup            *int32
	Parent                 *string
	RequiredTopologyLevel  string
	PreferredTopologyLevel string
}

// PCSGCliqueConfig defines configuration for a single clique in a PCSG.
type PCSGCliqueConfig struct {
	Name                string
	PodCount            int32
	Constraint          string
	PreferredConstraint string
}

// ScaledPCSGConfig defines configuration for verifying a scaled PCSG replica.
type ScaledPCSGConfig struct {
	Name                string
	PCSGName            string
	PCSGReplica         int
	MinAvailable        int
	CliqueConfigs       []PCSGCliqueConfig
	Constraint          string
	PreferredConstraint string
}

// PodGroupVerifier provides KAI PodGroup verification using a controller-runtime client.
type PodGroupVerifier struct {
	cl     client.Client
	logger *log.Logger
}

// NewPodGroupVerifier creates a PodGroupVerifier bound to the given client.
func NewPodGroupVerifier(cl client.Client, logger *log.Logger) *PodGroupVerifier {
	return &PodGroupVerifier{cl: cl, logger: logger}
}

// CreateExpectedStandalonePCLQSubGroup creates an ExpectedSubGroup for a standalone PodClique under the base PodGang branch.
func CreateExpectedStandalonePCLQSubGroup(pcsName string, pcsReplica int, cliqueName string, minMember int32, topologyLevel string) ExpectedSubGroup {
	baseBranch := nameutils.GenerateBasePodGangName(nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica})
	name := nameutils.GeneratePodCliqueName(
		nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica},
		cliqueName,
	)
	return ExpectedSubGroup{
		Name:                  name,
		MinMember:             minMember,
		Parent:                &baseBranch,
		RequiredTopologyLevel: topologyLevel,
	}
}

// CreateExpectedPCSGParentSubGroup creates an ExpectedSubGroup for a PCSG topology group under the base PodGang branch.
func CreateExpectedPCSGParentSubGroup(pcsName string, pcsReplica int, sgName string, sgReplica int, topologyLevel string) ExpectedSubGroup {
	baseBranch := nameutils.GenerateBasePodGangName(nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica})
	pcsgFQN := nameutils.GeneratePodCliqueScalingGroupName(
		nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica},
		sgName,
	)
	name := fmt.Sprintf("%s-%d", pcsgFQN, sgReplica)
	return ExpectedSubGroup{
		Name:                  name,
		MinMember:             0,
		Parent:                &baseBranch,
		RequiredTopologyLevel: topologyLevel,
	}
}

// CreateExpectedPCLQInPCSGSubGroup creates an ExpectedSubGroup for a PodClique within a PCSG with parent.
func CreateExpectedPCLQInPCSGSubGroup(pcsName string, pcsReplica int, sgName string, sgReplica int, cliqueName string, minMember int32, topologyLevel string) ExpectedSubGroup {
	return createExpectedPCLQInPCSGSubGroup(pcsName, pcsReplica, sgName, sgReplica, cliqueName, minMember, topologyLevel, true)
}

// CreateExpectedPCLQInPCSGBaseSubGroup creates an ExpectedSubGroup for a PodClique whose PCSG has no topology group.
// Such cliques are direct children of the base PodGang branch in the aggregate PodGroup.
func CreateExpectedPCLQInPCSGBaseSubGroup(pcsName string, pcsReplica int, sgName string, sgReplica int, cliqueName string, minMember int32, topologyLevel string) ExpectedSubGroup {
	subGroup := createExpectedPCLQInPCSGSubGroup(pcsName, pcsReplica, sgName, sgReplica, cliqueName, minMember, topologyLevel, false)
	baseBranch := nameutils.GenerateBasePodGangName(nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica})
	subGroup.Parent = &baseBranch
	return subGroup
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
	var podGroupList kaischedulingv2alpha2.PodGroupList
	if err := pv.cl.List(ctx, &podGroupList,
		client.InNamespace(namespace),
		client.MatchingLabels{nameutils.LabelPartOfKey: pcsName},
	); err != nil {
		return nil, fmt.Errorf("failed to list KAI PodGroups with label app.kubernetes.io/part-of=%s in namespace %s: %w", pcsName, namespace, err)
	}

	if len(podGroupList.Items) == 0 {
		return nil, fmt.Errorf("no KAI PodGroups found for PCS %s in namespace %s", pcsName, namespace)
	}

	return podGroupList.Items, nil
}

// WaitForKAIPodGroups waits for KAI PodGroups for the given PCS to exist and returns them.
func (pv *PodGroupVerifier) WaitForKAIPodGroups(ctx context.Context, namespace, pcsName string, timeout, interval time.Duration) ([]kaischedulingv2alpha2.PodGroup, error) {
	w := waiter.New[[]kaischedulingv2alpha2.PodGroup]().
		WithTimeout(timeout).
		WithInterval(interval).
		WithRetryOnError().
		WithLogger(pv.logger)
	podGroups, err := w.WaitFor(ctx,
		waiter.ToFetchFunc2(pv.GetKAIPodGroupsForPCS, namespace, pcsName),
		waiter.AlwaysTrue[[]kaischedulingv2alpha2.PodGroup],
	)
	if err != nil {
		return nil, fmt.Errorf("timed out waiting for KAI PodGroups for PCS %s/%s: %w", namespace, pcsName, err)
	}
	return podGroups, nil
}

// FilterAggregatePodGroupForPCSReplica selects the aggregate PodGroup by its PCS controller and replica label.
func FilterAggregatePodGroupForPCSReplica(podGroups []kaischedulingv2alpha2.PodGroup, pcsName string, pcsReplica int) (*kaischedulingv2alpha2.PodGroup, error) {
	replica := strconv.Itoa(pcsReplica)
	matches := make([]int, 0, 1)
	for i := range podGroups {
		if podGroups[i].Labels[nameutils.LabelPodCliqueSetReplicaIndex] != replica {
			continue
		}
		for _, ref := range podGroups[i].OwnerReferences {
			if ref.Kind == "PodCliqueSet" && ref.Name == pcsName && ptr.Deref(ref.Controller, false) {
				matches = append(matches, i)
				break
			}
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no aggregate PodGroup found controlled by PodCliqueSet %s with %s=%s",
			pcsName, nameutils.LabelPodCliqueSetReplicaIndex, replica)
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, i := range matches {
			names = append(names, podGroups[i].Name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("found multiple aggregate PodGroups controlled by PodCliqueSet %s with %s=%s: %v",
			pcsName, nameutils.LabelPodCliqueSetReplicaIndex, replica, names)
	}
	return &podGroups[matches[0]], nil
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

		if expected.MinMember == 0 && actual.MinMember != nil {
			return fmt.Errorf("SubGroup %q MinMember: got %d, expected nil", expected.Name, *actual.MinMember)
		}
		if expected.MinMember != 0 && (actual.MinMember == nil || *actual.MinMember != expected.MinMember) {
			return fmt.Errorf("SubGroup %q MinMember: got %v, expected %d", expected.Name, actual.MinMember, expected.MinMember)
		}
		if expected.MinSubGroup == nil && actual.MinSubGroup != nil {
			return fmt.Errorf("SubGroup %q MinSubGroup: got %d, expected nil", expected.Name, *actual.MinSubGroup)
		}
		if expected.MinSubGroup != nil && (actual.MinSubGroup == nil || *actual.MinSubGroup != *expected.MinSubGroup) {
			return fmt.Errorf("SubGroup %q MinSubGroup: got %v, expected %d",
				expected.Name, actual.MinSubGroup, *expected.MinSubGroup)
		}
		actualMinMember := ptr.Deref(actual.MinMember, 0)

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
			expected.Name, actual.Parent, actualMinMember, actualRequired, actualPreferred)
	}

	pv.logger.Infof("KAI PodGroup %s verified with %d SubGroups", podGroup.Name, len(expectedSubGroups))
	return nil
}

// GetAggregatePodGroupForPCSReplica retrieves the PCS-owned aggregate KAI PodGroup for one PCS replica.
func (pv *PodGroupVerifier) GetAggregatePodGroupForPCSReplica(ctx context.Context, namespace, workloadName string, pcsReplica int, timeout, interval time.Duration) (*kaischedulingv2alpha2.PodGroup, error) {
	podGroups, err := pv.WaitForKAIPodGroups(ctx, namespace, workloadName, timeout, interval)
	if err != nil {
		return nil, fmt.Errorf("failed to get KAI PodGroups: %w", err)
	}

	aggregatePodGroup, err := FilterAggregatePodGroupForPCSReplica(podGroups, workloadName, pcsReplica)
	if err != nil {
		return nil, err
	}
	return aggregatePodGroup, nil
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

// VerifyAggregatePodGroupTopology verifies aggregate identity, root thresholds, topology, and the exact subgroup tree.
func (pv *PodGroupVerifier) VerifyAggregatePodGroupTopology(
	podGroup *kaischedulingv2alpha2.PodGroup,
	pcsName string,
	pcsReplica int,
	requiredLevel string,
	preferredLevel string,
	baseSubGroups []ExpectedSubGroup,
	scaledPCSGs []ScaledPCSGConfig,
) error {
	if _, err := FilterAggregatePodGroupForPCSReplica([]kaischedulingv2alpha2.PodGroup{*podGroup}, pcsName, pcsReplica); err != nil {
		return fmt.Errorf("aggregate identity verification failed: %w", err)
	}
	if podGroup.Spec.MinMember != nil {
		return fmt.Errorf("aggregate PodGroup %s MinMember: got %d, expected nil", podGroup.Name, *podGroup.Spec.MinMember)
	}
	expectedSubGroups, err := createExpectedAggregateSubGroups(
		pcsName, pcsReplica, requiredLevel, preferredLevel, baseSubGroups, scaledPCSGs,
	)
	if err != nil {
		return fmt.Errorf("failed to build expected aggregate hierarchy: %w", err)
	}
	expectedRootMinSubGroup := int32(1)
	if len(scaledPCSGs) > 0 {
		expectedRootMinSubGroup = 2
	}
	if podGroup.Spec.MinSubGroup == nil || *podGroup.Spec.MinSubGroup != expectedRootMinSubGroup {
		return fmt.Errorf("aggregate PodGroup %s MinSubGroup: got %v, expected %d", podGroup.Name, podGroup.Spec.MinSubGroup, expectedRootMinSubGroup)
	}
	return pv.VerifyPodGroupTopology(podGroup, requiredLevel, preferredLevel, expectedSubGroups)
}

func createExpectedAggregateSubGroups(
	pcsName string,
	pcsReplica int,
	rootRequired string,
	rootPreferred string,
	baseSubGroups []ExpectedSubGroup,
	scaledPCSGs []ScaledPCSGConfig,
) ([]ExpectedSubGroup, error) {
	baseBranch := nameutils.GenerateBasePodGangName(nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica})
	baseSubGroups = append([]ExpectedSubGroup(nil), baseSubGroups...)
	directBaseChildren := int32(0)
	childCounts := map[string]int32{}
	usedNames := map[string]struct{}{baseBranch: {}}
	for i := range baseSubGroups {
		if _, found := usedNames[baseSubGroups[i].Name]; found {
			return nil, fmt.Errorf("duplicate expected SubGroup %q", baseSubGroups[i].Name)
		}
		usedNames[baseSubGroups[i].Name] = struct{}{}
		if baseSubGroups[i].Parent != nil {
			childCounts[*baseSubGroups[i].Parent]++
			if *baseSubGroups[i].Parent == baseBranch {
				directBaseChildren++
			}
		}
	}
	for i := range baseSubGroups {
		if count := childCounts[baseSubGroups[i].Name]; count > 0 {
			baseSubGroups[i].MinSubGroup = ptr.To(count)
		}
	}

	expected := []ExpectedSubGroup{{Name: baseBranch, MinSubGroup: ptr.To(directBaseChildren)}}
	expected = append(expected, baseSubGroups...)
	if _, found := usedNames[scaledPodGangsSubGroupName]; found {
		return nil, fmt.Errorf("duplicate expected SubGroup %q", scaledPodGangsSubGroupName)
	}
	usedNames[scaledPodGangsSubGroupName] = struct{}{}
	if len(scaledPCSGs) > 0 {
		expected = append(expected, ExpectedSubGroup{Name: scaledPodGangsSubGroupName, MinSubGroup: ptr.To[int32](0)})
	}

	sort.Slice(scaledPCSGs, func(i, j int) bool {
		if scaledPCSGs[i].PCSGName == scaledPCSGs[j].PCSGName {
			return scaledPCSGs[i].PCSGReplica < scaledPCSGs[j].PCSGReplica
		}
		return scaledPCSGs[i].PCSGName < scaledPCSGs[j].PCSGName
	})
	for _, config := range scaledPCSGs {
		if config.PCSGReplica < config.MinAvailable {
			return nil, fmt.Errorf("scaled PCSG %q replica %d is below minAvailable %d", config.PCSGName, config.PCSGReplica, config.MinAvailable)
		}
		pcsgFQN := nameutils.GeneratePodCliqueScalingGroupName(
			nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica}, config.PCSGName,
		)
		podGangName := nameutils.CreatePodGangNameFromPCSGFQN(pcsgFQN, config.PCSGReplica-config.MinAvailable)
		branchName := expectedStructuralName(podGangName, "gang", usedNames)
		branchRequired := config.Constraint
		branchPreferred := config.PreferredConstraint
		if branchRequired == rootRequired && branchPreferred == rootPreferred {
			branchRequired = ""
			branchPreferred = ""
		}
		expected = append(expected, ExpectedSubGroup{
			Name:                   branchName,
			Parent:                 ptr.To(scaledPodGangsSubGroupName),
			MinSubGroup:            ptr.To(int32(len(config.CliqueConfigs))),
			RequiredTopologyLevel:  branchRequired,
			PreferredTopologyLevel: branchPreferred,
		})
		for _, clique := range config.CliqueConfigs {
			leafName := nameutils.GeneratePodCliqueName(
				nameutils.ResourceNameReplica{Name: pcsgFQN, Replica: config.PCSGReplica}, clique.Name,
			)
			if _, found := usedNames[leafName]; found {
				return nil, fmt.Errorf("duplicate expected SubGroup %q", leafName)
			}
			usedNames[leafName] = struct{}{}
			expected = append(expected, ExpectedSubGroup{
				Name:                   leafName,
				Parent:                 ptr.To(branchName),
				MinMember:              clique.PodCount,
				RequiredTopologyLevel:  clique.Constraint,
				PreferredTopologyLevel: clique.PreferredConstraint,
			})
		}
	}
	return expected, nil
}

// expectedStructuralName mirrors the producer's collision role suffix while names remain in the
// short, already-valid form generated by Grove's public name helpers, as all topology E2Es do.
func expectedStructuralName(name, role string, usedNames map[string]struct{}) string {
	if _, found := usedNames[name]; !found {
		usedNames[name] = struct{}{}
		return name
	}
	name += "-" + role
	usedNames[name] = struct{}{}
	return name
}
