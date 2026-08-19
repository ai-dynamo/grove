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
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	podGangFinalizer           = "kai.scheduler/aggregate-podgroup"
	aggregatePrefix            = "grove-"
	scaledPodGangsSubGroupName = "scaled-podgangs"
)

func (b *schedulerBackend) syncAggregatePodGroup(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, trigger *groveschedulerv1alpha1.PodGang) error {
	replica, err := podCliqueSetReplicaFromObjectMeta(trigger.ObjectMeta)
	if err != nil {
		return fmt.Errorf("get PodCliqueSet replica index from PodGang %s/%s: %w", trigger.Namespace, trigger.Name, err)
	}
	basePodGangName := apicommon.GenerateBasePodGangName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replica})
	aggregateName := aggregatePodGroupName(pcs.Name, replica)
	unlock := b.aggregateLocks.Lock(client.ObjectKey{Namespace: pcs.Namespace, Name: aggregateName})
	defer unlock()

	podGangs, err := b.listActivePodGangsForReplica(ctx, pcs, replica)
	if err != nil {
		return err
	}
	if len(podGangs) == 0 {
		if err = b.verifyNoActivePodsForReplica(ctx, pcs, replica); err != nil {
			return err
		}
		return b.deleteAggregatePodGroup(ctx, pcs, aggregateName)
	}
	for i := range podGangs {
		if err = b.ensurePodGangMetadata(ctx, &podGangs[i]); err != nil {
			return fmt.Errorf("ensure KAI metadata on PodGang %s/%s: %w", podGangs[i].Namespace, podGangs[i].Name, err)
		}
	}

	basePodGang, found := findPodGangByName(podGangs, basePodGangName)
	if !found {
		return fmt.Errorf("base PodGang %s/%s not found while reconciling aggregate KAI PodGroup", pcs.Namespace, basePodGangName)
	}
	topologyReference, err := b.resolveKAITopologyReference(ctx, podGangs)
	if err != nil {
		return err
	}
	desired, err := b.buildAggregatePodGroup(pcs, replica, basePodGang, podGangs, topologyReference)
	if err != nil {
		return err
	}
	if err = b.syncPodGroup(ctx, pcs, desired); err != nil {
		return err
	}

	legacyReferences, err := b.migratePods(ctx, pcs, replica, podGangs, desired)
	if err != nil {
		return err
	}
	if err = b.deleteLegacyPodGroups(ctx, pcs, replica, podGangs, aggregateName, legacyReferences); err != nil {
		return err
	}
	log.FromContext(ctx).Info("Synced aggregate KAI PodGroup", "podCliqueSet", client.ObjectKeyFromObject(pcs), "replica", replica, "podGroup", aggregateName)
	return nil
}

func (b *schedulerBackend) listActivePodGangsForReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, replica int) ([]groveschedulerv1alpha1.PodGang, error) {
	list := &groveschedulerv1alpha1.PodGangList{}
	labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)
	labels[apicommon.LabelPodCliqueSetReplicaIndex] = strconv.Itoa(replica)
	labels[apicommon.LabelSchedulerName] = b.Name()
	if err := b.client.List(ctx, list,
		client.InNamespace(pcs.Namespace),
		client.MatchingLabels(labels),
	); err != nil {
		return nil, fmt.Errorf("list PodGangs for PodCliqueSet %s/%s: %w", pcs.Namespace, pcs.Name, err)
	}

	result := make([]groveschedulerv1alpha1.PodGang, 0, len(list.Items))
	for i := range list.Items {
		podGang := &list.Items[i]
		if !metav1.IsControlledBy(podGang, pcs) || !podGang.DeletionTimestamp.IsZero() {
			continue
		}
		result = append(result, *podGang)
	}
	return result, nil
}

func (b *schedulerBackend) buildAggregatePodGroup(
	pcs *grovecorev1alpha1.PodCliqueSet,
	replica int,
	basePodGang *groveschedulerv1alpha1.PodGang,
	podGangs []groveschedulerv1alpha1.PodGang,
	topologyReference string,
) (*kaischedulingv2alpha2.PodGroup, error) {
	queueName, err := resolveQueueNameForPodCliqueSet(pcs)
	if err != nil {
		return nil, err
	}
	rootTopology, err := toKAITopologyConstraint(basePodGang.Spec.TopologyConstraint, topologyReference)
	if err != nil {
		return nil, err
	}

	builder := newSubGroupBuilder(topologyReference)
	baseBranch := stableKAIName(basePodGang.Name)
	if err = builder.appendPodGangBranch(basePodGang, baseBranch, nil, nil); err != nil {
		return nil, err
	}
	if _, found := builder.names[scaledPodGangsSubGroupName]; found {
		return nil, fmt.Errorf("base PodGang %s/%s conflicts with reserved KAI subgroup name %q", basePodGang.Namespace, basePodGang.Name, scaledPodGangsSubGroupName)
	}

	scaledPodGangs := make([]groveschedulerv1alpha1.PodGang, 0, len(podGangs))
	for i := range podGangs {
		podGang := &podGangs[i]
		if podGang.Name == basePodGang.Name {
			continue
		}
		if podGang.Spec.PriorityClassName != basePodGang.Spec.PriorityClassName {
			return nil, fmt.Errorf("PodGangs %s and %s have conflicting priority classes", basePodGang.Name, podGang.Name)
		}
		if podGang.Labels[apicommon.LabelPodCliqueScalingGroup] == "" {
			return nil, fmt.Errorf("scaled PodGang %s/%s is missing required label %q", podGang.Namespace, podGang.Name, apicommon.LabelPodCliqueScalingGroup)
		}
		scaledPodGangs = append(scaledPodGangs, *podGang)
	}

	sort.Slice(scaledPodGangs, func(i, j int) bool { return scaledPodGangs[i].Name < scaledPodGangs[j].Name })
	rootMinSubGroup := int32(1)
	if len(scaledPodGangs) > 0 {
		if err = builder.add(kaischedulingv2alpha2.SubGroup{
			Name:        scaledPodGangsSubGroupName,
			MinSubGroup: ptr.To[int32](0),
		}); err != nil {
			return nil, err
		}
		rootMinSubGroup = 2
	}
	for i := range scaledPodGangs {
		podGang := &scaledPodGangs[i]
		if stableKAIName(podGang.Name) == scaledPodGangsSubGroupName {
			return nil, fmt.Errorf("scaled PodGang %s/%s conflicts with reserved KAI subgroup name %q", podGang.Namespace, podGang.Name, scaledPodGangsSubGroupName)
		}
		branchTopology, topologyErr := toKAITopologyConstraint(podGang.Spec.TopologyConstraint, topologyReference)
		if topologyErr != nil {
			return nil, topologyErr
		}
		if reflect.DeepEqual(rootTopology, branchTopology) {
			branchTopology = nil
		}
		parent := scaledPodGangsSubGroupName
		if err = builder.appendPodGangBranch(podGang, builder.structuralName(podGang.Name, "gang"), &parent, branchTopology); err != nil {
			return nil, err
		}
	}

	result := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:        aggregatePodGroupName(pcs.Name, replica),
			Namespace:   basePodGang.Namespace,
			Labels:      maps.Clone(basePodGang.Labels),
			Annotations: maps.Clone(basePodGang.Annotations),
		},
		Spec: kaischedulingv2alpha2.PodGroupSpec{
			MinSubGroup:       ptr.To(rootMinSubGroup),
			Queue:             queueName,
			PriorityClassName: basePodGang.Spec.PriorityClassName,
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

func (b *subGroupBuilder) structuralName(value, role string) string {
	name := stableKAIName(value)
	if _, found := b.names[name]; !found {
		return name
	}
	return stableKAIName(value + "-" + role)
}

func (b *subGroupBuilder) appendPodGangBranch(podGang *groveschedulerv1alpha1.PodGang, branchName string, parent *string, branchTopology *kaischedulingv2alpha2.TopologyConstraint) error {
	podGroups := append([]groveschedulerv1alpha1.PodGroup(nil), podGang.Spec.PodGroups...)
	sort.Slice(podGroups, func(i, j int) bool { return podGroups[i].Name < podGroups[j].Name })
	groups := make([]groveschedulerv1alpha1.TopologyConstraintGroupConfig, 0, len(podGang.Spec.TopologyConstraintGroupConfigs))
	for _, group := range podGang.Spec.TopologyConstraintGroupConfigs {
		if len(group.PodGroupNames) > 0 {
			groups = append(groups, group)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	podGroupNames := map[string]struct{}{}
	for _, podGroup := range podGroups {
		podGroupNames[podGroup.Name] = struct{}{}
	}
	parentByPodGroup := map[string]string{}
	for _, group := range groups {
		groupName := stableKAIName(group.Name)
		for _, podGroupName := range group.PodGroupNames {
			if _, found := podGroupNames[podGroupName]; !found {
				return fmt.Errorf("topology group %q references unknown PodGroup %q", group.Name, podGroupName)
			}
			if previous, found := parentByPodGroup[podGroupName]; found {
				return fmt.Errorf("PodGroup %q belongs to topology groups %q and %q", podGroupName, previous, group.Name)
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
		Name:               branchName,
		Parent:             parent,
		MinSubGroup:        ptr.To(int32(directChildren)),
		TopologyConstraint: branchTopology,
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
			Name:               stableKAIName(group.Name),
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
			Name:               stableKAIName(podGroup.Name),
			Parent:             &leafParent,
			MinMember:          ptr.To(podGroup.MinReplicas),
			TopologyConstraint: leafTopology,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (b *schedulerBackend) resolveKAITopologyReference(ctx context.Context, podGangs []groveschedulerv1alpha1.PodGang) (string, error) {
	clusterTopologyName := ""
	for i := range podGangs {
		podGang := &podGangs[i]
		if !podGangHasTopologyConstraints(podGang) {
			continue
		}
		name := getTopologyName(podGang)
		if name == "" {
			return "", fmt.Errorf("PodGang %s/%s has topology constraints without %q annotation", podGang.Namespace, podGang.Name, apicommonconstants.AnnotationTopologyName)
		}
		if clusterTopologyName != "" && clusterTopologyName != name {
			return "", fmt.Errorf("PodGangs for one PCS replica reference multiple ClusterTopologyBindings: %q and %q", clusterTopologyName, name)
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

func (b *schedulerBackend) syncPodGroup(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, desired *kaischedulingv2alpha2.PodGroup) error {
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
	podGangs []groveschedulerv1alpha1.PodGang,
	desired *kaischedulingv2alpha2.PodGroup,
) (map[string]bool, error) {
	podGangNames := map[string]struct{}{}
	for i := range podGangs {
		podGangNames[podGangs[i].Name] = struct{}{}
	}
	leaves := map[string]struct{}{}
	for _, subGroup := range desired.Spec.SubGroups {
		if subGroup.MinMember != nil {
			leaves[subGroup.Name] = struct{}{}
		}
	}

	pods, err := b.listActivePodsForReplica(ctx, pcs, replica)
	if err != nil {
		return nil, err
	}
	legacyReferences := map[string]bool{}
	for i := range pods {
		pod := &pods[i]
		podGangName := pod.Labels[apicommon.LabelPodGang]
		if _, found := podGangNames[podGangName]; !found {
			return nil, fmt.Errorf("pod %s/%s references PodGang %q outside aggregate %q", pod.Namespace, pod.Name, podGangName, desired.Name)
		}
		leaf := stableKAIName(pod.Labels[apicommon.LabelPodClique])
		if _, found := leaves[leaf]; !found {
			return nil, fmt.Errorf("pod %s/%s maps to missing KAI subgroup %q", pod.Namespace, pod.Name, leaf)
		}
		if oldPodGroup := pod.Annotations[annotationPodGroup]; oldPodGroup != "" && oldPodGroup != desired.Name {
			legacyReferences[oldPodGroup] = true
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
			return nil, fmt.Errorf("migrate Pod %s/%s to KAI PodGroup %q: %w", pod.Namespace, pod.Name, desired.Name, err)
		}
	}
	return legacyReferences, nil
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

func (b *schedulerBackend) verifyNoActivePodsForReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, replica int) error {
	pods, err := b.listActivePodsForReplica(ctx, pcs, replica)
	if err != nil {
		return err
	}
	if len(pods) > 0 {
		return fmt.Errorf("waiting for %d Pods in PodCliqueSet %s/%s replica %d to terminate", len(pods), pcs.Namespace, pcs.Name, replica)
	}
	return nil
}

func (b *schedulerBackend) deleteLegacyPodGroups(
	ctx context.Context,
	pcs *grovecorev1alpha1.PodCliqueSet,
	replica int,
	podGangs []groveschedulerv1alpha1.PodGang,
	aggregateName string,
	referenced map[string]bool,
) error {
	activePods, err := b.listActivePodsForReplica(ctx, pcs, replica)
	if err != nil {
		return err
	}
	activeReferences := make(map[string]struct{}, len(activePods))
	for i := range activePods {
		if name := activePods[i].Annotations[annotationPodGroup]; name != "" {
			activeReferences[name] = struct{}{}
		}
	}

	podGroups := &kaischedulingv2alpha2.PodGroupList{}
	if err = b.client.List(ctx, podGroups, client.InNamespace(pcs.Namespace)); err != nil {
		return fmt.Errorf("list KAI PodGroups while cleaning legacy groups for PodCliqueSet %s/%s replica %d: %w",
			pcs.Namespace, pcs.Name, replica, err)
	}
	legacyByName := make(map[string]*kaischedulingv2alpha2.PodGroup)
	for i := range podGroups.Items {
		podGroup := &podGroups.Items[i]
		if podGroup.Name == aggregateName {
			continue
		}
		if _, owned := owningPodGang(podGroup, podGangs); owned {
			legacyByName[podGroup.Name] = podGroup
		}
	}

	// PR #725 used the PodGang name directly. Keep looking up those deterministic
	// names as well, including objects that predate or lost their controller reference.
	for i := range podGangs {
		podGang := &podGangs[i]
		if podGang.Name == aggregateName {
			continue
		}
		legacy := &kaischedulingv2alpha2.PodGroup{}
		key := client.ObjectKey{Namespace: podGang.Namespace, Name: podGang.Name}
		if err = b.client.Get(ctx, key, legacy); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		legacyByName[legacy.Name] = legacy
	}

	legacyNames := make([]string, 0, len(legacyByName))
	for name := range legacyByName {
		legacyNames = append(legacyNames, name)
	}
	sort.Strings(legacyNames)
	for _, name := range legacyNames {
		legacy := legacyByName[name]
		key := client.ObjectKeyFromObject(legacy)
		podGang, owned := owningPodGang(legacy, podGangs)
		if !owned && !referenced[legacy.Name] {
			return fmt.Errorf("refusing to delete KAI PodGroup %s: it is neither owned by a sibling PodGang nor referenced by its Grove Pods", key)
		}
		if _, active := activeReferences[legacy.Name]; active {
			return fmt.Errorf("refusing to delete legacy KAI PodGroup %s while an active Grove Pod still references it", key)
		}
		if err = b.client.Delete(ctx, legacy); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete legacy KAI PodGroup %s: %w", key, err)
		}
		if owned {
			log.FromContext(ctx).V(1).Info("Deleted legacy KAI PodGroup", "podGroup", key, "podGang", client.ObjectKeyFromObject(podGang))
		}
	}
	return nil
}

func owningPodGang(
	podGroup *kaischedulingv2alpha2.PodGroup,
	podGangs []groveschedulerv1alpha1.PodGang,
) (*groveschedulerv1alpha1.PodGang, bool) {
	for i := range podGangs {
		for _, owner := range podGroup.OwnerReferences {
			if owner.Kind == "PodGang" &&
				owner.Name == podGangs[i].Name &&
				owner.UID == podGangs[i].UID {
				return &podGangs[i], true
			}
		}
	}
	return nil, false
}

func (b *schedulerBackend) deleteAggregatePodGroup(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, name string) error {
	podGroup := &kaischedulingv2alpha2.PodGroup{}
	key := client.ObjectKey{Namespace: pcs.Namespace, Name: name}
	if err := b.client.Get(ctx, key, podGroup); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(podGroup, pcs) {
		return fmt.Errorf("refusing to delete KAI PodGroup %s not controlled by PodCliqueSet %s", key, pcs.Name)
	}
	return client.IgnoreNotFound(b.client.Delete(ctx, podGroup))
}

func findPodGangByName(podGangs []groveschedulerv1alpha1.PodGang, name string) (*groveschedulerv1alpha1.PodGang, bool) {
	for i := range podGangs {
		if podGangs[i].Name == name {
			return &podGangs[i], true
		}
	}
	return nil, false
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

func aggregatePodGroupName(pcsName string, replica int) string {
	return stableKAIName(fmt.Sprintf("%s%s-%d", aggregatePrefix, pcsName, replica))
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
