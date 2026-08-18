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
	"strconv"
	"strings"
	"time"

	nameutils "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
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
	Name       string
	PodCount   int32
	Constraint string
}

// ScaledPCSGConfig defines configuration for verifying a scaled PCSG replica.
type ScaledPCSGConfig struct {
	PCSGName      string
	PCSGReplica   int
	CliqueConfigs []PCSGCliqueConfig
	Constraint    string
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

// FindAnchorPodGroup finds the KAI PodGroup for the anchor PodGang of a PodCliqueSet replica.
// The anchor PodGang name embeds a runtime-minted epoch, so it cannot be reconstructed. Instead the
// PodGroup is matched on the labels cloned from its PodGang: the anchor role and the PodCliqueSet
// replica index.
func FindAnchorPodGroup(podGroups []kaischedulingv2alpha2.PodGroup, pcsReplicaIndex int) (*kaischedulingv2alpha2.PodGroup, error) {
	for i := range podGroups {
		labels := podGroups[i].Labels
		if labels[nameutils.LabelPodGangRole] == string(grovecorev1alpha1.PodGangEntryRoleAnchor) &&
			labels[nameutils.LabelPodCliqueSetReplicaIndex] == strconv.Itoa(pcsReplicaIndex) {
			return &podGroups[i], nil
		}
	}
	return nil, fmt.Errorf("no anchor PodGroup found for PodCliqueSet replica index %d", pcsReplicaIndex)
}

// FindScaledPodGroup finds the KAI PodGroup for a scaled (Tail or ScaleOut) PodGang of a
// PodCliqueScalingGroup replica. The scaled PodGang name is
// <pcs>-<replica>-<epoch>-<pcsgName>-<pcsgReplicaIndex>; only the epoch is runtime-minted. The
// PodGroup is matched on its cloned PodGang labels (a Tail or ScaleOut role and the PodCliqueSet
// replica index) plus a name suffix of -<pcsgName>-<pcsgReplicaIndex>.
func FindScaledPodGroup(podGroups []kaischedulingv2alpha2.PodGroup, pcsReplicaIndex int, pcsgName string, pcsgReplicaIndex int) (*kaischedulingv2alpha2.PodGroup, error) {
	nameSuffix := fmt.Sprintf("-%s-%d", pcsgName, pcsgReplicaIndex)
	for i := range podGroups {
		labels := podGroups[i].Labels
		role := labels[nameutils.LabelPodGangRole]
		if role != string(grovecorev1alpha1.PodGangEntryRoleTail) && role != string(grovecorev1alpha1.PodGangEntryRoleScaleOut) {
			continue
		}
		if labels[nameutils.LabelPodCliqueSetReplicaIndex] != strconv.Itoa(pcsReplicaIndex) {
			continue
		}
		if strings.HasSuffix(podGroups[i].Name, nameSuffix) {
			return &podGroups[i], nil
		}
	}
	return nil, fmt.Errorf("no scaled PodGroup found for PodCliqueSet replica index %d, PodCliqueScalingGroup %s replica index %d", pcsReplicaIndex, pcsgName, pcsgReplicaIndex)
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

		actualMinMember := ptr.Deref(actual.MinMember, 0)
		if actualMinMember != expected.MinMember {
			return fmt.Errorf("SubGroup %q MinMember: got %d, expected %d", expected.Name, actualMinMember, expected.MinMember)
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
			expected.Name, actual.Parent, actualMinMember, actualRequired, actualPreferred)
	}

	pv.logger.Infof("KAI PodGroup %s verified with %d SubGroups", podGroup.Name, len(expectedSubGroups))
	return nil
}

// GetPodGroupForAnchorPodGang retrieves the KAI PodGroup for a PodCliqueSet replica's anchor PodGang.
func (pv *PodGroupVerifier) GetPodGroupForAnchorPodGang(ctx context.Context, namespace, pcsName string, pcsReplica int, timeout, interval time.Duration) (*kaischedulingv2alpha2.PodGroup, error) {
	podGroups, err := pv.WaitForKAIPodGroups(ctx, namespace, pcsName, timeout, interval)
	if err != nil {
		return nil, fmt.Errorf("failed to get KAI PodGroups: %w", err)
	}

	anchorPodGroup, err := FindAnchorPodGroup(podGroups, pcsReplica)
	if err != nil {
		return nil, fmt.Errorf("failed to find anchor PodGroup for PodCliqueSet %s replica %d: %w", pcsName, pcsReplica, err)
	}

	return anchorPodGroup, nil
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

	scaledPodGroup, err := FindScaledPodGroup(podGroups, pcsReplica, pcsgConfig.PCSGName, pcsgConfig.PCSGReplica)
	if err != nil {
		return fmt.Errorf("failed to find scaled PodGroup: %w", err)
	}

	// The scaled PodGang carries the PCS-level constraint at the PodGroup top level. When the PCSG has
	// its own topology constraint the operator emits a PCSG-parent SubGroup carrying it, with the
	// PCSG's PodCliques parented under it; without one the PodCliques remain root-level SubGroups.
	hasPCSGConstraint := pcsgConfig.Constraint != ""
	var expectedSubGroups []ExpectedSubGroup
	if hasPCSGConstraint {
		expectedSubGroups = append(expectedSubGroups,
			CreateExpectedPCSGParentSubGroup(pcsName, pcsReplica, pcsgConfig.PCSGName, pcsgConfig.PCSGReplica, pcsgConfig.Constraint))
	}
	for _, cliqueConfig := range pcsgConfig.CliqueConfigs {
		if hasPCSGConstraint {
			expectedSubGroups = append(expectedSubGroups,
				CreateExpectedPCLQInPCSGSubGroup(pcsName, pcsReplica, pcsgConfig.PCSGName, pcsgConfig.PCSGReplica, cliqueConfig.Name, cliqueConfig.PodCount, cliqueConfig.Constraint))
		} else {
			expectedSubGroups = append(expectedSubGroups,
				CreateExpectedPCLQInPCSGSubGroupNoParent(pcsName, pcsReplica, pcsgConfig.PCSGName, pcsgConfig.PCSGReplica, cliqueConfig.Name, cliqueConfig.PodCount, cliqueConfig.Constraint))
		}
	}

	return pv.VerifyPodGroupTopology(scaledPodGroup, pcsConstraint, "", expectedSubGroups)
}
