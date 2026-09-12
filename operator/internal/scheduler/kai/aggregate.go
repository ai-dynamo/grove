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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	aggregatePrefix               = "grove-"
	nonAnchorPodGangsSubGroupName = "0-non-anchor-podgangs"
)

func (b *schedulerBackend) reconcileAggregatePodGroup(
	ctx context.Context,
	pcs *grovecorev1alpha1.PodCliqueSet,
	replica int,
	materialized []componentutils.MaterializedPodGang,
) (*kaischedulingv2alpha2.PodGroup, error) {
	for _, item := range materialized {
		if !item.PodGang.DeletionTimestamp.IsZero() {
			continue
		}
		if err := b.ensurePodGangMetadata(ctx, item.PodGang); err != nil {
			return nil, fmt.Errorf("ensure KAI metadata on PodGang %s/%s: %w", item.PodGang.Namespace, item.PodGang.Name, err)
		}
	}

	topologyReference, err := b.resolveKAITopologyReference(ctx, materialized)
	if err != nil {
		return nil, err
	}
	desired, err := b.buildAggregatePodGroup(pcs, replica, materialized, topologyReference)
	if err != nil {
		return nil, err
	}
	if err = b.syncAggregatePodGroup(ctx, pcs, desired); err != nil {
		return nil, err
	}
	return desired, nil
}

func (b *schedulerBackend) buildAggregatePodGroup(
	pcs *grovecorev1alpha1.PodCliqueSet,
	replica int,
	materialized []componentutils.MaterializedPodGang,
	topologyReference string,
) (*kaischedulingv2alpha2.PodGroup, error) {
	if len(materialized) == 0 {
		return nil, fmt.Errorf("PodGangMap for PodCliqueSet %s/%s replica %d materializes no PodGangs", pcs.Namespace, pcs.Name, replica)
	}

	for _, item := range materialized {
		if item.PodGang == nil || item.Entry == nil {
			return nil, fmt.Errorf("materialized PodGang must include PodGang and PodGangMap entry")
		}
	}
	podGangs := append([]componentutils.MaterializedPodGang(nil), materialized...)
	sort.Slice(podGangs, func(i, j int) bool { return podGangs[i].PodGang.Name < podGangs[j].PodGang.Name })
	reference := podGangs[0].PodGang
	for _, item := range podGangs {
		if item.PodGang.Spec.PriorityClassName != reference.Spec.PriorityClassName {
			return nil, fmt.Errorf("PodGangs %s and %s have conflicting priority classes", reference.Name, item.PodGang.Name)
		}
		if !reflect.DeepEqual(item.PodGang.Spec.TopologyConstraint, reference.Spec.TopologyConstraint) {
			return nil, fmt.Errorf("PodGangs %s and %s have conflicting root topology constraints", reference.Name, item.PodGang.Name)
		}
	}

	queueName, err := resolveQueueNameForPodCliqueSet(pcs)
	if err != nil {
		return nil, err
	}
	rootTopology, err := toKAITopologyConstraint(reference.Spec.TopologyConstraint, topologyReference)
	if err != nil {
		return nil, err
	}

	anchors := make([]componentutils.MaterializedPodGang, 0, len(podGangs))
	nonAnchors := make([]componentutils.MaterializedPodGang, 0, len(podGangs))
	for _, item := range podGangs {
		switch item.Entry.Role {
		case grovecorev1alpha1.PodGangEntryRoleAnchor:
			anchors = append(anchors, item)
		case grovecorev1alpha1.PodGangEntryRoleTail, grovecorev1alpha1.PodGangEntryRoleScaleOut:
			nonAnchors = append(nonAnchors, item)
		default:
			return nil, fmt.Errorf("PodGang %s/%s has unsupported role %q", item.PodGang.Namespace, item.PodGang.Name, item.Entry.Role)
		}
	}
	if len(anchors) == 0 {
		return nil, fmt.Errorf("PodGangMap for PodCliqueSet %s/%s replica %d has no materialized Anchor PodGang", pcs.Namespace, pcs.Name, replica)
	}

	builder := newSubGroupBuilder(topologyReference)
	for _, item := range anchors {
		if err = builder.appendPodGangBranch(item.PodGang, anchorBranchName(item.PodGang.Name), nil); err != nil {
			return nil, err
		}
	}
	rootMinSubGroup := int32(len(anchors))
	if len(nonAnchors) > 0 {
		rootMinSubGroup++
		if err = builder.add(kaischedulingv2alpha2.SubGroup{
			Name:        nonAnchorPodGangsSubGroupName,
			MinSubGroup: ptr.To(int32(len(nonAnchors))),
		}); err != nil {
			return nil, err
		}
		for _, item := range nonAnchors {
			collection := nonAnchorPodGangsSubGroupName
			utilityName := utilityParentName(item.PodGang.Name)
			if err = builder.add(kaischedulingv2alpha2.SubGroup{
				Name:        utilityName,
				Parent:      &collection,
				MinSubGroup: ptr.To[int32](0),
			}); err != nil {
				return nil, err
			}
			if err = builder.appendPodGangBranch(item.PodGang, podGangBranchName(item.PodGang.Name), &utilityName); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(builder.subGroups, func(i, j int) bool { return builder.subGroups[i].Name < builder.subGroups[j].Name })
	labels := maps.Clone(pcs.Labels)
	if labels == nil {
		labels = make(map[string]string)
	}
	maps.Copy(labels, apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))
	labels[apicommon.LabelComponentKey] = apicommon.LabelComponentNameAggregatePodGroup
	labels[apicommon.LabelPodCliqueSetReplicaIndex] = strconv.Itoa(replica)

	result := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:        aggregatePodGroupName(pcs.Name, replica),
			Namespace:   pcs.Namespace,
			Labels:      labels,
			Annotations: maps.Clone(pcs.Annotations),
		},
		Spec: kaischedulingv2alpha2.PodGroupSpec{
			MinSubGroup:       ptr.To(rootMinSubGroup),
			Queue:             queueName,
			PriorityClassName: reference.Spec.PriorityClassName,
			SubGroups:         builder.subGroups,
		},
	}
	if rootTopology != nil {
		result.Spec.TopologyConstraint = *rootTopology
	}
	if err = controllerutil.SetControllerReference(pcs, result, b.scheme); err != nil {
		return nil, err
	}
	return result, nil
}

type subGroupBuilder struct {
	topologyReference string
	names             map[string]struct{}
	subGroups         []kaischedulingv2alpha2.SubGroup
}

func newSubGroupBuilder(topologyReference string) *subGroupBuilder {
	return &subGroupBuilder{topologyReference: topologyReference, names: map[string]struct{}{}}
}

func (b *subGroupBuilder) add(subGroup kaischedulingv2alpha2.SubGroup) error {
	if _, found := b.names[subGroup.Name]; found {
		return fmt.Errorf("duplicate KAI subgroup name %q", subGroup.Name)
	}
	b.names[subGroup.Name] = struct{}{}
	b.subGroups = append(b.subGroups, subGroup)
	return nil
}

func (b *subGroupBuilder) appendPodGangBranch(podGang *groveschedulerv1alpha1.PodGang, branchName string, parent *string) error {
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
		groupName := topologyGroupName(podGang.Name, group.Name)
		for _, podGroupName := range group.PodGroupNames {
			if _, found := podGroupNames[podGroupName]; !found {
				return fmt.Errorf("topology group %q in PodGang %s/%s references unknown PodGroup %q", group.Name, podGang.Namespace, podGang.Name, podGroupName)
			}
			if previous, found := parentByPodGroup[podGroupName]; found {
				return fmt.Errorf("PodGroup %q in PodGang %s/%s belongs to topology groups %q and %q", podGroupName, podGang.Namespace, podGang.Name, previous, group.Name)
			}
			parentByPodGroup[podGroupName] = groupName
		}
	}
	directChildren := len(groups)
	for _, podGroup := range podGroups {
		if _, grouped := parentByPodGroup[podGroup.Name]; !grouped {
			directChildren++
		}
	}
	if err := b.add(kaischedulingv2alpha2.SubGroup{
		Name:        branchName,
		Parent:      parent,
		MinSubGroup: ptr.To(int32(directChildren)),
	}); err != nil {
		return err
	}

	for _, group := range groups {
		groupTopology, err := toKAITopologyConstraint(group.TopologyConstraint, b.topologyReference)
		if err != nil {
			return err
		}
		branch := branchName
		if err = b.add(kaischedulingv2alpha2.SubGroup{
			Name:               topologyGroupName(podGang.Name, group.Name),
			Parent:             &branch,
			MinSubGroup:        ptr.To(int32(len(group.PodGroupNames))),
			TopologyConstraint: groupTopology,
		}); err != nil {
			return err
		}
	}
	for _, podGroup := range podGroups {
		leafTopology, err := toKAITopologyConstraint(podGroup.TopologyConstraint, b.topologyReference)
		if err != nil {
			return err
		}
		leafParent := branchName
		if groupParent, found := parentByPodGroup[podGroup.Name]; found {
			leafParent = groupParent
		}
		if err = b.add(kaischedulingv2alpha2.SubGroup{
			Name:               podGroupLeafName(podGang.Name, podGroup.Name),
			Parent:             &leafParent,
			MinMember:          ptr.To(podGroup.MinReplicas),
			TopologyConstraint: leafTopology,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (b *schedulerBackend) resolveKAITopologyReference(ctx context.Context, materialized []componentutils.MaterializedPodGang) (string, error) {
	clusterTopologyName := ""
	for _, item := range materialized {
		podGang := item.PodGang
		if !podGangHasTopologyConstraints(podGang) {
			continue
		}
		name := getTopologyName(podGang)
		if name == "" {
			return "", fmt.Errorf("PodGang %s/%s has topology constraints without %q annotation", podGang.Namespace, podGang.Name, apicommonconstants.AnnotationTopologyName)
		}
		if clusterTopologyName != "" && clusterTopologyName != name {
			return "", fmt.Errorf("PodGangs for one PodCliqueSet replica reference multiple ClusterTopologyBindings: %q and %q", clusterTopologyName, name)
		}
		clusterTopologyName = name
	}
	if clusterTopologyName == "" {
		return "", nil
	}

	ct := &grovecorev1alpha1.ClusterTopologyBinding{}
	if err := b.client.Get(ctx, client.ObjectKey{Name: clusterTopologyName}, ct); err != nil {
		return "", fmt.Errorf("get ClusterTopologyBinding %q: %w", clusterTopologyName, err)
	}
	for _, binding := range ct.Spec.SchedulerTopologyBindings {
		if binding.SchedulerName == b.Name() {
			return binding.TopologyReference, nil
		}
	}
	return b.TopologyResourceName(ct), nil
}

func podGangHasTopologyConstraints(podGang *groveschedulerv1alpha1.PodGang) bool {
	if podGang.Spec.TopologyConstraint != nil {
		return true
	}
	for _, group := range podGang.Spec.TopologyConstraintGroupConfigs {
		if len(group.PodGroupNames) > 0 && group.TopologyConstraint != nil {
			return true
		}
	}
	for _, podGroup := range podGang.Spec.PodGroups {
		if podGroup.TopologyConstraint != nil {
			return true
		}
	}
	return false
}

func (b *schedulerBackend) syncAggregatePodGroup(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, desired *kaischedulingv2alpha2.PodGroup) error {
	existing := &kaischedulingv2alpha2.PodGroup{}
	if err := b.client.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
		if apierrors.IsNotFound(err) {
			return b.client.Create(ctx, desired)
		}
		return err
	}
	if !metav1.IsControlledBy(existing, pcs) {
		return fmt.Errorf("KAI PodGroup %s/%s already exists and is not controlled by PodCliqueSet %s", existing.Namespace, existing.Name, pcs.Name)
	}
	desired = b.inheritRuntimeManagedFields(existing, desired)
	if podGroupsEqual(existing, desired) {
		return nil
	}
	updatePodGroup(existing, desired)
	return b.client.Update(ctx, existing)
}

func (b *schedulerBackend) migratePods(
	ctx context.Context,
	pcs *grovecorev1alpha1.PodCliqueSet,
	replica int,
	materialized []componentutils.MaterializedPodGang,
	desired *kaischedulingv2alpha2.PodGroup,
) error {
	podGangNames := make(map[string]struct{}, len(materialized))
	for _, item := range materialized {
		podGangNames[item.PodGang.Name] = struct{}{}
	}
	leaves := make(map[string]struct{})
	for _, subGroup := range desired.Spec.SubGroups {
		if subGroup.MinMember != nil {
			leaves[subGroup.Name] = struct{}{}
		}
	}

	pods, err := b.listActivePodsForReplica(ctx, pcs, replica)
	if err != nil {
		return err
	}
	for i := range pods {
		pod := &pods[i]
		podGangName := pod.Labels[apicommon.LabelPodGang]
		if _, found := podGangNames[podGangName]; !found {
			return fmt.Errorf("pod %s/%s references PodGang %q outside aggregate %q", pod.Namespace, pod.Name, podGangName, desired.Name)
		}
		leaf := podGroupLeafName(podGangName, pod.Labels[apicommon.LabelPodClique])
		if _, found := leaves[leaf]; !found {
			return fmt.Errorf("pod %s/%s maps to missing KAI subgroup %q", pod.Namespace, pod.Name, leaf)
		}
		if pod.Annotations[annotationPodGroup] == desired.Name && pod.Labels[labelSubGroup] == leaf && pod.Annotations[annotationKeySkipPGR] == annotationValSkipPGR {
			continue
		}
		before := pod.DeepCopy()
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		if pod.Labels == nil {
			pod.Labels = map[string]string{}
		}
		pod.Annotations[annotationKeySkipPGR] = annotationValSkipPGR
		pod.Annotations[annotationPodGroup] = desired.Name
		pod.Labels[labelSubGroup] = leaf
		if err = b.client.Patch(ctx, pod, client.MergeFrom(before)); err != nil {
			return fmt.Errorf("migrate Pod %s/%s to KAI PodGroup %q: %w", pod.Namespace, pod.Name, desired.Name, err)
		}
	}
	return nil
}

func (b *schedulerBackend) listActivePodsForReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, replica int) ([]corev1.Pod, error) {
	list := &corev1.PodList{}
	if err := b.client.List(ctx, list,
		client.InNamespace(pcs.Namespace),
		client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)),
	); err != nil {
		return nil, fmt.Errorf("list Pods for PodCliqueSet %s/%s: %w", pcs.Namespace, pcs.Name, err)
	}
	result := make([]corev1.Pod, 0, len(list.Items))
	for i := range list.Items {
		pod := &list.Items[i]
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		podReplica, err := podCliqueSetReplicaFromObjectMeta(pod.ObjectMeta)
		if err != nil {
			return nil, fmt.Errorf("pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		if podReplica == replica {
			result = append(result, *pod.DeepCopy())
		}
	}
	return result, nil
}

func (b *schedulerBackend) deleteScaledInAggregatePodGroup(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, replica int) error {
	pods, err := b.listActivePodsForReplica(ctx, pcs, replica)
	if err != nil {
		return err
	}
	if len(pods) > 0 {
		return fmt.Errorf("waiting for %d Pods in PodCliqueSet %s/%s replica %d to terminate", len(pods), pcs.Namespace, pcs.Name, replica)
	}

	podGroup := &kaischedulingv2alpha2.PodGroup{}
	key := aggregatePodGroupKey(pcs, replica)
	if err = b.client.Get(ctx, key, podGroup); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(podGroup, pcs) {
		return fmt.Errorf("refusing to delete KAI PodGroup %s not controlled by PodCliqueSet %s", key, pcs.Name)
	}
	return client.IgnoreNotFound(b.client.Delete(ctx, podGroup))
}

func aggregatePodGroupKey(pcs *grovecorev1alpha1.PodCliqueSet, replica int) client.ObjectKey {
	return client.ObjectKey{Namespace: pcs.Namespace, Name: aggregatePodGroupName(pcs.Name, replica)}
}

func aggregatePodGroupName(pcsName string, replica int) string {
	return stableKAIName(fmt.Sprintf("%s%s-%d", aggregatePrefix, pcsName, replica))
}

func anchorBranchName(podGangName string) string {
	return stableKAIName("1-" + podGangName)
}

func podGangBranchName(podGangName string) string {
	return stableKAIName(podGangName)
}

func utilityParentName(podGangName string) string {
	return structuralKAIName("utility", podGangName)
}

func topologyGroupName(podGangName, groupName string) string {
	return structuralKAIName(podGangName, "group", groupName)
}

func podGroupLeafName(podGangName, podGroupName string) string {
	return structuralKAIName(podGangName, "leaf", podGroupName)
}

func structuralKAIName(parts ...string) string {
	return stableKAIName(strings.Join(parts, "\x00"))
}

func podCliqueSetReplicaFromObjectMeta(objMeta metav1.ObjectMeta) (int, error) {
	replica, err := k8sutils.GetPodCliqueSetReplicaIndex(objMeta)
	if err != nil {
		return 0, err
	}
	if replica < 0 {
		return 0, fmt.Errorf("invalid %s value %q", apicommon.LabelPodCliqueSetReplicaIndex, objMeta.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
	}
	return replica, nil
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
