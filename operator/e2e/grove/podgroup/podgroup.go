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
	Name                string
	PodCount            int32
	Constraint          string
	PreferredConstraint string
}

// ScaledPCSGConfig defines configuration for verifying a scaled PCSG replica.
type ScaledPCSGConfig struct {
	PCSGName            string
	PCSGReplica         int
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

// CreateExpectedStandalonePCLQSubGroup creates an ExpectedSubGroup for a standalone PodClique (not in PCSG).
func CreateExpectedStandalonePCLQSubGroup(pcsName string, pcsReplica int, cliqueName string, minMember int32, topologyLevel string) ExpectedSubGroup {
	name := nameutils.GeneratePodCliqueName(
		nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica},
		cliqueName,
	)
	return ExpectedSubGroup{
		Name:                  name,
		MinMember:             minMember,
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

func (pv *PodGroupVerifier) getKAIPodGroupsForPCS(ctx context.Context, namespace, pcsName string) ([]kaischedulingv2alpha2.PodGroup, error) {
	var podGroupList kaischedulingv2alpha2.PodGroupList
	if err := pv.cl.List(ctx, &podGroupList,
		client.InNamespace(namespace),
		client.MatchingLabels{nameutils.LabelPartOfKey: pcsName},
	); err != nil {
		return nil, fmt.Errorf("failed to list KAI PodGroups with label app.kubernetes.io/part-of=%s in namespace %s: %w", pcsName, namespace, err)
	}

	return podGroupList.Items, nil
}

// FilterAggregatePodGroupForPCSReplica selects the aggregate PodGroup by its PCS controller and replica label.
func FilterAggregatePodGroupForPCSReplica(podGroups []kaischedulingv2alpha2.PodGroup, pcsName string, pcsReplica int) (*kaischedulingv2alpha2.PodGroup, error) {
	replica := strconv.Itoa(pcsReplica)
	matches := make([]int, 0, 1)
	for i := range podGroups {
		if podGroups[i].Labels[nameutils.LabelPodCliqueSetReplicaIndex] != replica ||
			podGroups[i].Labels[nameutils.LabelComponentKey] != nameutils.LabelComponentNameAggregatePodGroup {
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

// GetAggregatePodGroupForPCSReplica retrieves the PCS-owned aggregate KAI PodGroup for one PCS replica.
func (pv *PodGroupVerifier) GetAggregatePodGroupForPCSReplica(ctx context.Context, namespace, workloadName string, pcsReplica int, timeout, interval time.Duration) (*kaischedulingv2alpha2.PodGroup, error) {
	w := waiter.New[*kaischedulingv2alpha2.PodGroup]().
		WithTimeout(timeout).
		WithInterval(interval).
		WithRetryOnError().
		WithLogger(pv.logger)
	aggregatePodGroup, err := w.WaitFor(ctx, func(ctx context.Context) (*kaischedulingv2alpha2.PodGroup, error) {
		podGroups, err := pv.getKAIPodGroupsForPCS(ctx, namespace, workloadName)
		if err != nil {
			return nil, err
		}
		return FilterAggregatePodGroupForPCSReplica(podGroups, workloadName, pcsReplica)
	}, waiter.AlwaysTrue[*kaischedulingv2alpha2.PodGroup])
	if err != nil {
		return nil, fmt.Errorf("timed out waiting for aggregate KAI PodGroup for PCS %s/%s replica %d: %w", namespace, workloadName, pcsReplica, err)
	}
	return aggregatePodGroup, nil
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
	expectedRootMinSubGroup := int32(1)
	if len(scaledPCSGs) > 0 {
		expectedRootMinSubGroup = 2
	}
	if podGroup.Spec.MinSubGroup == nil || *podGroup.Spec.MinSubGroup != expectedRootMinSubGroup {
		return fmt.Errorf("aggregate PodGroup %s MinSubGroup: got %v, expected %d", podGroup.Name, podGroup.Spec.MinSubGroup, expectedRootMinSubGroup)
	}
	if err := pv.VerifyTopologyConstraint(podGroup, requiredLevel, preferredLevel); err != nil {
		return fmt.Errorf("top-level constraint verification failed: %w", err)
	}

	expectedShapes, err := expectedAggregateSubGroupShapes(pcsName, pcsReplica, baseSubGroups, scaledPCSGs)
	if err != nil {
		return fmt.Errorf("build expected aggregate hierarchy: %w", err)
	}
	actualShapes, err := subGroupShapes(podGroup.Spec.SubGroups)
	if err != nil {
		return fmt.Errorf("read aggregate hierarchy: %w", err)
	}
	expectedCanonical := canonicalShapes(expectedShapes)
	actualCanonical := canonicalShapes(actualShapes)
	if fmt.Sprint(actualCanonical) != fmt.Sprint(expectedCanonical) {
		return fmt.Errorf("aggregate PodGroup %s subgroup hierarchy:\nactual:   %v\nexpected: %v", podGroup.Name, actualCanonical, expectedCanonical)
	}
	pv.logger.Infof("KAI aggregate PodGroup %s verified with %d SubGroups", podGroup.Name, len(podGroup.Spec.SubGroups))
	return nil
}

type subGroupShape struct {
	minMember              *int32
	minSubGroup            *int32
	requiredTopologyLevel  string
	preferredTopologyLevel string
	children               []subGroupShape
}

func expectedAggregateSubGroupShapes(
	pcsName string,
	pcsReplica int,
	baseSubGroups []ExpectedSubGroup,
	scaledPCSGs []ScaledPCSGConfig,
) ([]subGroupShape, error) {
	baseBranch := nameutils.GenerateBasePodGangName(nameutils.ResourceNameReplica{Name: pcsName, Replica: pcsReplica})
	expectedSubGroups := make([]ExpectedSubGroup, 0, len(baseSubGroups)+1)
	expectedSubGroups = append(expectedSubGroups, ExpectedSubGroup{Name: baseBranch})
	for _, subGroup := range baseSubGroups {
		if subGroup.Parent == nil {
			subGroup.Parent = ptr.To(baseBranch)
		}
		expectedSubGroups = append(expectedSubGroups, subGroup)
	}
	baseShapes, err := expectedSubGroupShapes(expectedSubGroups)
	if err != nil {
		return nil, err
	}
	if len(baseShapes) != 1 {
		return nil, fmt.Errorf("expected one Anchor branch, got %d", len(baseShapes))
	}

	expected := baseShapes
	if len(scaledPCSGs) == 0 {
		return expected, nil
	}
	nonAnchorChildren := make([]subGroupShape, 0, len(scaledPCSGs))
	for _, config := range scaledPCSGs {
		if len(config.CliqueConfigs) == 0 {
			return nil, fmt.Errorf("scaled PCSG %q replica %d has no PodCliques", config.PCSGName, config.PCSGReplica)
		}
		leaves := make([]subGroupShape, 0, len(config.CliqueConfigs))
		for _, clique := range config.CliqueConfigs {
			leaves = append(leaves, subGroupShape{
				minMember:              ptr.To(clique.PodCount),
				requiredTopologyLevel:  clique.Constraint,
				preferredTopologyLevel: clique.PreferredConstraint,
			})
		}
		branchChildren := leaves
		if config.Constraint != "" || config.PreferredConstraint != "" {
			branchChildren = []subGroupShape{{
				minSubGroup:            ptr.To(int32(len(leaves))),
				requiredTopologyLevel:  config.Constraint,
				preferredTopologyLevel: config.PreferredConstraint,
				children:               leaves,
			}}
		}
		branch := subGroupShape{minSubGroup: ptr.To(int32(len(branchChildren))), children: branchChildren}
		nonAnchorChildren = append(nonAnchorChildren, subGroupShape{
			minSubGroup: ptr.To[int32](0),
			children:    []subGroupShape{branch},
		})
	}
	expected = append(expected, subGroupShape{
		minSubGroup: ptr.To(int32(len(nonAnchorChildren))),
		children:    nonAnchorChildren,
	})
	return expected, nil
}

func expectedSubGroupShapes(expected []ExpectedSubGroup) ([]subGroupShape, error) {
	subGroups := make([]kaischedulingv2alpha2.SubGroup, 0, len(expected))
	for _, item := range expected {
		var topologyConstraint *kaischedulingv2alpha2.TopologyConstraint
		if item.RequiredTopologyLevel != "" || item.PreferredTopologyLevel != "" {
			topologyConstraint = &kaischedulingv2alpha2.TopologyConstraint{
				RequiredTopologyLevel:  item.RequiredTopologyLevel,
				PreferredTopologyLevel: item.PreferredTopologyLevel,
			}
		}
		var minMember *int32
		if item.MinMember != 0 {
			minMember = ptr.To(item.MinMember)
		}
		subGroups = append(subGroups, kaischedulingv2alpha2.SubGroup{
			Name:               item.Name,
			Parent:             item.Parent,
			MinMember:          minMember,
			TopologyConstraint: topologyConstraint,
		})
	}

	childCounts := make(map[string]int32)
	for _, subGroup := range subGroups {
		if subGroup.Parent != nil {
			childCounts[*subGroup.Parent]++
		}
	}
	for i := range subGroups {
		if count := childCounts[subGroups[i].Name]; count > 0 {
			subGroups[i].MinSubGroup = ptr.To(count)
		}
	}
	return subGroupShapes(subGroups)
}

func subGroupShapes(subGroups []kaischedulingv2alpha2.SubGroup) ([]subGroupShape, error) {
	byName := make(map[string]kaischedulingv2alpha2.SubGroup, len(subGroups))
	children := make(map[string][]string, len(subGroups))
	rootNames := make([]string, 0)
	for _, subGroup := range subGroups {
		if _, found := byName[subGroup.Name]; found {
			return nil, fmt.Errorf("duplicate SubGroup %q", subGroup.Name)
		}
		byName[subGroup.Name] = subGroup
	}
	for _, subGroup := range subGroups {
		if subGroup.Parent == nil {
			rootNames = append(rootNames, subGroup.Name)
			continue
		}
		if _, found := byName[*subGroup.Parent]; !found {
			return nil, fmt.Errorf("SubGroup %q references unknown parent %q", subGroup.Name, *subGroup.Parent)
		}
		children[*subGroup.Parent] = append(children[*subGroup.Parent], subGroup.Name)
	}

	state := make(map[string]uint8, len(subGroups))
	var buildShape func(string) (subGroupShape, error)
	buildShape = func(name string) (subGroupShape, error) {
		if state[name] == 1 {
			return subGroupShape{}, fmt.Errorf("SubGroup hierarchy contains a cycle at %q", name)
		}
		state[name] = 1
		subGroup := byName[name]
		shape := subGroupShape{minMember: subGroup.MinMember, minSubGroup: subGroup.MinSubGroup}
		if subGroup.TopologyConstraint != nil {
			shape.requiredTopologyLevel = subGroup.TopologyConstraint.RequiredTopologyLevel
			shape.preferredTopologyLevel = subGroup.TopologyConstraint.PreferredTopologyLevel
		}
		for _, childName := range children[name] {
			child, err := buildShape(childName)
			if err != nil {
				return subGroupShape{}, err
			}
			shape.children = append(shape.children, child)
		}
		state[name] = 2
		return shape, nil
	}

	shapes := make([]subGroupShape, 0, len(rootNames))
	for _, rootName := range rootNames {
		shape, err := buildShape(rootName)
		if err != nil {
			return nil, err
		}
		shapes = append(shapes, shape)
	}
	if len(state) != len(subGroups) {
		return nil, fmt.Errorf("SubGroup hierarchy contains nodes that are not reachable from a root")
	}
	return shapes, nil
}

func canonicalShapes(shapes []subGroupShape) []string {
	result := make([]string, 0, len(shapes))
	for _, shape := range shapes {
		children := canonicalShapes(shape.children)
		result = append(result, fmt.Sprintf("{member:%s subgroups:%s required:%q preferred:%q children:%v}",
			optionalInt32(shape.minMember), optionalInt32(shape.minSubGroup), shape.requiredTopologyLevel, shape.preferredTopologyLevel, children))
	}
	sort.Strings(result)
	return result
}

func optionalInt32(value *int32) string {
	if value == nil {
		return "nil"
	}
	return strconv.FormatInt(int64(*value), 10)
}
