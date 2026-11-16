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

package kai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// aggregatePrefix starts the name of every per-replica KAI PodGroup: grove-<pcs>-<replica>.
	aggregatePrefix = "grove-"
	// The subgroup kinds start every subgroup name: <kind>-r<replica>-<rest>.
	// KAI breaks a tie between subgroups with the same allocation ratio by name, and
	// "non-anchors" sorts the collection ahead of the "podgang" Anchor branches.
	subGroupKindNonAnchorCollection = "non-anchors"
	subGroupKindNonAnchorPodGang    = "non-anchor"
	subGroupKindPodGang             = "podgang"
	subGroupKindTopologyGroup       = "group"
	subGroupKindPodGroup            = "leaf"
)

// buildAggregatePlan aggregates the PodGangs of one PodCliqueSet replica into a single KAI PodGroup.
// Each Anchor PodGang becomes a required top-level branch, Tail and ScaleOut PodGangs become optional
// branches under one collection subgroup, and inside a branch the topology groups are mid-level
// subgroups and the PodGroups are leaves. The plan also records which leaf each Pod belongs to.
func (b *schedulerBackend) buildAggregatePlan(
	pcs *grovecorev1beta1.PodCliqueSet,
	replica int,
	materialized []componentutils.MaterializedPodGang,
	topologyReference string,
) (*aggregatePlan, error) {
	inputs, err := prepareAggregateInputs(pcs, replica, materialized)
	if err != nil {
		return nil, err
	}
	queueName, err := resolveQueueNameForPodCliqueSet(pcs)
	if err != nil {
		return nil, err
	}
	rootTopology, err := toKAITopologyConstraint(inputs.reference.Spec.TopologyConstraint, topologyReference)
	if err != nil {
		return nil, err
	}
	builder := newSubGroupBuilder(pcs.Name, replica, topologyReference)
	rootMinSubGroup, err := builder.appendAggregateHierarchy(inputs)
	if err != nil {
		return nil, err
	}
	result := newAggregatePodGroup(pcs, replica, kaischedulingv2alpha2.PodGroupSpec{
		MinSubGroup:       ptr.To(rootMinSubGroup),
		Queue:             queueName,
		PriorityClassName: inputs.reference.Spec.PriorityClassName,
		SubGroups:         builder.subGroups,
	})
	if rootTopology != nil {
		result.Spec.TopologyConstraint = *rootTopology
	}
	if err = controllerutil.SetControllerReference(pcs, result, b.scheme); err != nil {
		return nil, err
	}
	return &aggregatePlan{podGroup: result, podLeaves: builder.podLeaves}, nil
}

type aggregateInputs struct {
	reference  *groveschedulerv1alpha1.PodGang
	anchors    []componentutils.MaterializedPodGang
	nonAnchors []componentutils.MaterializedPodGang
}

type aggregatePlan struct {
	podGroup *kaischedulingv2alpha2.PodGroup
	// podLeaves maps PodGang name -> PodGroup name (PodClique FQN) -> KAI leaf subgroup name.
	podLeaves map[string]map[string]string
}

func prepareAggregateInputs(
	pcs *grovecorev1beta1.PodCliqueSet,
	replica int,
	materialized []componentutils.MaterializedPodGang,
) (*aggregateInputs, error) {
	if len(materialized) == 0 {
		return nil, fmt.Errorf("PodGangMap for PodCliqueSet %s/%s replica %d has no materialized PodGangs", pcs.Namespace, pcs.Name, replica)
	}

	for _, item := range materialized {
		if item.PodGang == nil {
			return nil, fmt.Errorf("PodGangMap for PodCliqueSet %s/%s replica %d has a materialization without a PodGang", pcs.Namespace, pcs.Name, replica)
		}
		if item.Entry == nil {
			return nil, fmt.Errorf("PodGang %s/%s of PodCliqueSet %s replica %d has no PodGangMap entry", item.PodGang.Namespace, item.PodGang.Name, pcs.Name, replica)
		}
	}
	podGangs := append([]componentutils.MaterializedPodGang(nil), materialized...)
	sort.Slice(podGangs, func(i, j int) bool { return podGangs[i].PodGang.Name < podGangs[j].PodGang.Name })
	reference := podGangs[0].PodGang
	inputs := &aggregateInputs{reference: reference}
	for _, item := range podGangs {
		if len(item.PodGang.Spec.PodGroups) == 0 {
			return nil, fmt.Errorf("PodGang %s/%s has no PodGroups", item.PodGang.Namespace, item.PodGang.Name)
		}
		if item.PodGang.Spec.PriorityClassName != reference.Spec.PriorityClassName {
			return nil, fmt.Errorf("PodGangs %s and %s have conflicting priority classes", reference.Name, item.PodGang.Name)
		}
		if packLevels(item.PodGang.Spec.TopologyConstraint) != packLevels(reference.Spec.TopologyConstraint) {
			return nil, fmt.Errorf("PodGangs %s and %s have conflicting root topology constraints", reference.Name, item.PodGang.Name)
		}
		switch item.Entry.Role {
		case grovecorev1beta1.PodGangEntryRoleAnchor:
			inputs.anchors = append(inputs.anchors, item)
		case grovecorev1beta1.PodGangEntryRoleTail, grovecorev1beta1.PodGangEntryRoleScaleOut:
			inputs.nonAnchors = append(inputs.nonAnchors, item)
		default:
			return nil, fmt.Errorf("PodGang %s/%s has unsupported role %q", item.PodGang.Namespace, item.PodGang.Name, item.Entry.Role)
		}
	}
	// LPX-scheduled PodGroups are removed before KAI sees a PodGang, and a PodGang left with none is
	// dropped, so the Anchor can be absent from the KAI aggregate of an LPX workload.
	if len(inputs.anchors) == 0 && reference.Labels[apicommon.LabelSchedulerName] != string(configv1beta1.SchedulerNameLPX) {
		return nil, fmt.Errorf("PodGangMap for PodCliqueSet %s/%s replica %d has no materialized Anchor PodGang", pcs.Namespace, pcs.Name, replica)
	}
	return inputs, nil
}

type packConstraintLevels struct {
	required  string
	preferred string
}

func packLevels(topologyConstraint *groveschedulerv1alpha1.TopologyConstraint) packConstraintLevels {
	if topologyConstraint == nil || topologyConstraint.PackConstraint == nil {
		return packConstraintLevels{}
	}
	return packConstraintLevels{
		required:  ptr.Deref(topologyConstraint.PackConstraint.Required, ""),
		preferred: ptr.Deref(topologyConstraint.PackConstraint.Preferred, ""),
	}
}

func newAggregatePodGroup(pcs *grovecorev1beta1.PodCliqueSet, replica int, spec kaischedulingv2alpha2.PodGroupSpec) *kaischedulingv2alpha2.PodGroup {
	labels := withoutGroveKeys(pcs.Labels)
	if labels == nil {
		labels = make(map[string]string)
	}
	maps.Copy(labels, apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))
	labels[apicommon.LabelComponentKey] = apicommon.LabelComponentNamePodCliqueSetReplicaPodGroup
	labels[apicommon.LabelPodCliqueSetReplicaIndex] = strconv.Itoa(replica)

	return &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:        aggregatePodGroupName(pcs.Name, replica),
			Namespace:   pcs.Namespace,
			Labels:      labels,
			Annotations: withoutGroveKeys(pcs.Annotations),
		},
		Spec: spec,
	}
}

func withoutGroveKeys(entries map[string]string) map[string]string {
	var result map[string]string
	for key, value := range entries {
		if strings.HasPrefix(key, apicommonconstants.GroveDomainPrefix) {
			continue
		}
		if result == nil {
			result = make(map[string]string, len(entries))
		}
		result[key] = value
	}
	return result
}

type subGroupBuilder struct {
	topologyReference string
	replica           int
	// groveNamePrefix is "<pcs>-<replica>-", which starts the name of every Grove resource of the
	// replica and is left out of subgroup names.
	groveNamePrefix string
	// names is the set of subgroup names emitted so far, used to reject duplicates.
	names     map[string]struct{}
	subGroups []kaischedulingv2alpha2.SubGroup
	// podLeaves maps PodGang name -> PodGroup name (PodClique FQN) -> KAI leaf subgroup name.
	podLeaves map[string]map[string]string
}

func newSubGroupBuilder(pcsName string, replica int, topologyReference string) *subGroupBuilder {
	return &subGroupBuilder{
		topologyReference: topologyReference,
		replica:           replica,
		groveNamePrefix:   fmt.Sprintf("%s-%d-", pcsName, replica),
		names:             map[string]struct{}{},
		podLeaves:         map[string]map[string]string{},
	}
}

func (b *subGroupBuilder) appendAggregateHierarchy(inputs *aggregateInputs) (int32, error) {
	for _, anchor := range inputs.anchors {
		if err := b.appendPodGangBranch(anchor, nil); err != nil {
			return 0, err
		}
	}
	rootMinSubGroup := int32(len(inputs.anchors))
	if len(inputs.nonAnchors) > 0 {
		rootMinSubGroup++
		if err := b.appendNonAnchorBranches(inputs.nonAnchors); err != nil {
			return 0, err
		}
	}
	sort.Slice(b.subGroups, func(i, j int) bool { return b.subGroups[i].Name < b.subGroups[j].Name })
	return rootMinSubGroup, nil
}

func (b *subGroupBuilder) appendNonAnchorBranches(nonAnchors []componentutils.MaterializedPodGang) error {
	collection := nonAnchorCollectionName(b.replica)
	// The collection requires every wrapper, so KAI's subgroup ordering ranks it with the Anchor
	// branches rather than behind them as it would an optional subgroup.
	if err := b.add(kaischedulingv2alpha2.SubGroup{
		Name:        collection,
		MinSubGroup: ptr.To(int32(len(nonAnchors))),
	}); err != nil {
		return err
	}
	for _, nonAnchor := range nonAnchors {
		wrapperName := nonAnchorPodGangSubGroupName(b.replica, b.shortName(nonAnchor.PodGang.Name))
		// KAI requires no child for an explicit minSubGroup of 0 and every child for an unset one
		// (SubGroupSet.GetMinMembersToSatisfy), which makes each non-Anchor PodGang optional.
		if err := b.add(kaischedulingv2alpha2.SubGroup{
			Name:        wrapperName,
			Parent:      &collection,
			MinSubGroup: ptr.To[int32](0),
		}); err != nil {
			return err
		}
		if err := b.appendPodGangBranch(nonAnchor, &wrapperName); err != nil {
			return err
		}
	}
	return nil
}

func (b *subGroupBuilder) add(subGroup kaischedulingv2alpha2.SubGroup) error {
	if _, found := b.names[subGroup.Name]; found {
		return fmt.Errorf("duplicate KAI subgroup name %q", subGroup.Name)
	}
	b.names[subGroup.Name] = struct{}{}
	b.subGroups = append(b.subGroups, subGroup)
	return nil
}

type podGangHierarchy struct {
	podGroups        []groveschedulerv1alpha1.PodGroup
	groups           []groveschedulerv1alpha1.TopologyConstraintGroupConfig
	parentByPodGroup map[string]string
	directChildren   int32
}

func analyzePodGangHierarchy(podGang *groveschedulerv1alpha1.PodGang) (*podGangHierarchy, error) {
	podGroups := append([]groveschedulerv1alpha1.PodGroup(nil), podGang.Spec.PodGroups...)
	sort.Slice(podGroups, func(i, j int) bool { return podGroups[i].Name < podGroups[j].Name })
	groups := make([]groveschedulerv1alpha1.TopologyConstraintGroupConfig, 0, len(podGang.Spec.TopologyConstraintGroupConfigs))
	for _, group := range podGang.Spec.TopologyConstraintGroupConfigs {
		if len(group.PodGroupNames) > 0 {
			groups = append(groups, group)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	podGroupNames := make(map[string]struct{}, len(podGroups))
	for _, podGroup := range podGroups {
		podGroupNames[podGroup.Name] = struct{}{}
	}
	parentByPodGroup := make(map[string]string)
	for _, group := range groups {
		for _, podGroupName := range group.PodGroupNames {
			if _, found := podGroupNames[podGroupName]; !found {
				return nil, fmt.Errorf("topology group %q in PodGang %s/%s references unknown PodGroup %q", group.Name, podGang.Namespace, podGang.Name, podGroupName)
			}
			if previous, found := parentByPodGroup[podGroupName]; found {
				return nil, fmt.Errorf("PodGroup %q in PodGang %s/%s belongs to topology groups %q and %q", podGroupName, podGang.Namespace, podGang.Name, previous, group.Name)
			}
			parentByPodGroup[podGroupName] = group.Name
		}
	}
	directChildren := len(groups)
	for _, podGroup := range podGroups {
		if _, grouped := parentByPodGroup[podGroup.Name]; !grouped {
			directChildren++
		}
	}
	return &podGangHierarchy{
		podGroups:        podGroups,
		groups:           groups,
		parentByPodGroup: parentByPodGroup,
		directChildren:   int32(directChildren),
	}, nil
}

func (b *subGroupBuilder) appendPodGangBranch(item componentutils.MaterializedPodGang, parent *string) error {
	podGang := item.PodGang
	hierarchy, err := analyzePodGangHierarchy(podGang)
	if err != nil {
		return err
	}
	branchName := podGangBranchName(b.replica, b.shortName(podGang.Name))
	if err := b.add(kaischedulingv2alpha2.SubGroup{
		Name:        branchName,
		Parent:      parent,
		MinSubGroup: ptr.To(hierarchy.directChildren),
	}); err != nil {
		return err
	}
	if err := b.appendTopologyGroups(item.Entry.Epoch, branchName, hierarchy.groups); err != nil {
		return err
	}
	return b.appendPodGroupLeaves(podGang.Name, item.Entry.Epoch, branchName, hierarchy)
}

func (b *subGroupBuilder) appendTopologyGroups(epoch, branchName string, groups []groveschedulerv1alpha1.TopologyConstraintGroupConfig) error {
	for _, group := range groups {
		groupTopology, err := toKAITopologyConstraint(group.TopologyConstraint, b.topologyReference)
		if err != nil {
			return err
		}
		branch := branchName
		if err = b.add(kaischedulingv2alpha2.SubGroup{
			Name:               topologyGroupName(b.replica, b.shortName(group.Name), epoch),
			Parent:             &branch,
			MinSubGroup:        ptr.To(int32(len(group.PodGroupNames))),
			TopologyConstraint: groupTopology,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (b *subGroupBuilder) appendPodGroupLeaves(podGangName, epoch, branchName string, hierarchy *podGangHierarchy) error {
	leaves := make(map[string]string, len(hierarchy.podGroups))
	for _, podGroup := range hierarchy.podGroups {
		leafTopology, err := toKAITopologyConstraint(podGroup.TopologyConstraint, b.topologyReference)
		if err != nil {
			return err
		}
		leafParent := branchName
		if groupParent, found := hierarchy.parentByPodGroup[podGroup.Name]; found {
			leafParent = topologyGroupName(b.replica, b.shortName(groupParent), epoch)
		}
		leafName := podGroupLeafName(b.replica, b.shortName(podGroup.Name), epoch)
		if err = b.add(kaischedulingv2alpha2.SubGroup{
			Name:               leafName,
			Parent:             &leafParent,
			MinMember:          ptr.To(podGroup.MinReplicas),
			TopologyConstraint: leafTopology,
		}); err != nil {
			return err
		}
		leaves[podGroup.Name] = leafName
	}
	b.podLeaves[podGangName] = leaves
	return nil
}

func aggregatePodGroupName(pcsName string, replica int) string {
	return stableKAIName(fmt.Sprintf("%s%s-%d", aggregatePrefix, pcsName, replica))
}

func nonAnchorCollectionName(replica int) string {
	return subGroupName(subGroupKindNonAnchorCollection, replica)
}

func nonAnchorPodGangSubGroupName(replica int, podGang string) string {
	return subGroupName(subGroupKindNonAnchorPodGang, replica, podGang)
}

func podGangBranchName(replica int, podGang string) string {
	return subGroupName(subGroupKindPodGang, replica, podGang)
}

func topologyGroupName(replica int, group, epoch string) string {
	return subGroupName(subGroupKindTopologyGroup, replica, group, epoch)
}

func podGroupLeafName(replica int, podGroup, epoch string) string {
	return subGroupName(subGroupKindPodGroup, replica, podGroup, epoch)
}

func subGroupName(kind string, replica int, parts ...string) string {
	return stableKAIName(strings.Join(append([]string{kind, fmt.Sprintf("r%d", replica)}, parts...), "-"))
}

func (b *subGroupBuilder) shortName(name string) string {
	return strings.TrimPrefix(name, b.groveNamePrefix)
}

func stableKAIName(value string) string {
	lower := strings.ToLower(value)
	var sanitized strings.Builder
	lastDash := false
	for _, char := range lower {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-'
		if !valid {
			char = '-'
		}
		if char == '-' {
			if lastDash {
				continue
			}
			lastDash = true
		} else {
			lastDash = false
		}
		sanitized.WriteRune(char)
	}
	name := strings.Trim(sanitized.String(), "-")
	if name == value && len(name) <= 63 {
		return name
	}
	hash := sha256.Sum256([]byte(value))
	suffix := hex.EncodeToString(hash[:])[:10]
	if name == "" {
		return "kai-" + suffix
	}
	if len(name) > 52 {
		name = strings.TrimRight(name[:52], "-")
	}
	return name + "-" + suffix
}

func (p *aggregatePlan) leafForPod(pod *corev1.Pod) (string, error) {
	podGangName := pod.Labels[apicommon.LabelPodGang]
	leaves, found := p.podLeaves[podGangName]
	if !found {
		return "", fmt.Errorf("pod %s/%s references PodGang %q outside aggregate %q", pod.Namespace, pod.Name, podGangName, p.podGroup.Name)
	}
	podGroupName := pod.Labels[apicommon.LabelPodClique]
	leaf, found := leaves[podGroupName]
	if !found {
		return "", fmt.Errorf("pod %s/%s maps to missing KAI subgroup for PodGroup %q", pod.Namespace, pod.Name, podGroupName)
	}
	return leaf, nil
}
