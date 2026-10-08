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

package kube

import (
	"crypto/sha256"
	"fmt"
	"maps"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	workloadbuilder "k8s.io/component-helpers/scheduling/schedulingv1/workloadbuilder"
	"k8s.io/utils/ptr"
)

const (
	rootTemplateName      = "root"
	templateNameHashBytes = 10
)

// buildWorkloadForPodGang translates a PodGang into a Workload template tree.
// It does not mutate the input or access the API. Runtime group reconciliation,
// Pod membership, and recovery of released minimums belong to backend integration.
func buildWorkloadForPodGang(podGang *groveschedulerv1alpha1.PodGang) (*schedulingv1beta1.Workload, error) {
	if podGang == nil {
		return nil, field.Required(field.NewPath("podGang"), "must not be nil")
	}
	root, err := workloadItemTreeForPodGang(podGang)
	if err != nil {
		return nil, err
	}
	if err := validateWorkloadLimits(root, field.NewPath("spec", "compositePodGroupTemplates").Index(0), 1); err != nil {
		return nil, err
	}

	builder := workloadbuilder.NewBuilder(root, workloadbuilder.BuildOptions{
		Name:      podGang.Name,
		Namespace: podGang.Namespace,
		Owner:     metav1.NewControllerRef(podGang, groveschedulerv1alpha1.SchemeGroupVersion.WithKind("PodGang")),
		AllowedPolicies: []workloadbuilder.SchedulingPolicyOption{
			workloadbuilder.GangPolicy,
		},
	})
	workload, err := builder.BuildWorkload()
	if err != nil {
		return nil, fmt.Errorf("failed to compile Workload for PodGang %s/%s: %w", podGang.Namespace, podGang.Name, err)
	}
	workload.Labels = maps.Clone(podGang.Labels)
	if workload.Labels == nil {
		workload.Labels = make(map[string]string)
	}
	workload.Labels[apicommon.LabelPodGang] = podGang.Name
	return workload, nil
}

// workloadItemTreeForPodGang keeps ungrouped leaves under the root and places
// each topology group's leaves beneath one composite to share a topology domain.
func workloadItemTreeForPodGang(podGang *groveschedulerv1alpha1.PodGang) (*workloadbuilder.WorkloadItem, error) {
	podGroupsPath := field.NewPath("spec", "podgroups")
	if len(podGang.Spec.PodGroups) == 0 {
		return nil, field.Required(podGroupsPath, "at least one PodGroup is required")
	}

	leafItems := make(map[string]*workloadbuilder.WorkloadItem, len(podGang.Spec.PodGroups))
	for i, podGroup := range podGang.Spec.PodGroups {
		groupPath := podGroupsPath.Index(i)
		if podGroup.Name == "" {
			return nil, field.Required(groupPath.Child("name"), "must not be empty")
		}
		if _, exists := leafItems[podGroup.Name]; exists {
			return nil, field.Duplicate(groupPath.Child("name"), podGroup.Name)
		}
		if podGroup.MinReplicas < 1 {
			return nil, field.Invalid(groupPath.Child("minReplicas"), podGroup.MinReplicas, "WAS gang scheduling requires minCount >= 1")
		}
		leafItems[podGroup.Name] = &workloadbuilder.WorkloadItem{
			Name: leafTemplateName(podGroup.Name),
			DefaultConfig: &workloadbuilder.SchedulingConfig{
				Policy: &workloadbuilder.SchedulingPolicy{
					Gang: &workloadbuilder.GangSchedulingPolicy{MinCount: ptr.To(podGroup.MinReplicas)},
				},
				Constraints:       toUpstreamTopologyConstraints(podGroup.TopologyConstraint),
				PriorityClassName: podGang.Spec.PriorityClassName,
			},
		}
	}

	rootChildren := make([]*workloadbuilder.WorkloadItem, 0, len(podGang.Spec.TopologyConstraintGroupConfigs)+len(podGang.Spec.PodGroups))
	grouped := sets.New[string]()
	topologyGroups := sets.New[string]()
	for i, groupConfig := range podGang.Spec.TopologyConstraintGroupConfigs {
		// Empty topology groups own no leaves and must not become leaf templates.
		if len(groupConfig.PodGroupNames) == 0 {
			continue
		}
		groupPath := field.NewPath("spec", "topologyConstraintGroupConfigs").Index(i)
		if groupConfig.Name == "" {
			return nil, field.Required(groupPath.Child("name"), "must not be empty")
		}
		if topologyGroups.Has(groupConfig.Name) {
			return nil, field.Duplicate(groupPath.Child("name"), groupConfig.Name)
		}
		topologyGroups.Insert(groupConfig.Name)
		children := make([]*workloadbuilder.WorkloadItem, 0, len(groupConfig.PodGroupNames))
		for j, podGroupName := range groupConfig.PodGroupNames {
			memberPath := groupPath.Child("podGroupNames").Index(j)
			leaf, found := leafItems[podGroupName]
			if !found {
				return nil, field.NotFound(memberPath, podGroupName)
			}
			if grouped.Has(podGroupName) {
				return nil, field.Duplicate(memberPath, podGroupName)
			}
			grouped.Insert(podGroupName)
			children = append(children, leaf)
		}
		rootChildren = append(rootChildren, &workloadbuilder.WorkloadItem{
			Name: topologyGroupTemplateName(groupConfig.Name),
			DefaultConfig: &workloadbuilder.SchedulingConfig{
				Policy: &workloadbuilder.SchedulingPolicy{
					Gang: &workloadbuilder.GangSchedulingPolicy{MinCount: ptr.To(int32(len(children)))},
				},
				Constraints:       toUpstreamTopologyConstraints(groupConfig.TopologyConstraint),
				PriorityClassName: podGang.Spec.PriorityClassName,
			},
			Children: children,
		})
	}
	for _, podGroup := range podGang.Spec.PodGroups {
		if !grouped.Has(podGroup.Name) {
			rootChildren = append(rootChildren, leafItems[podGroup.Name])
		}
	}
	return &workloadbuilder.WorkloadItem{
		Name: rootTemplateName,
		DefaultConfig: &workloadbuilder.SchedulingConfig{
			Policy: &workloadbuilder.SchedulingPolicy{
				Gang: &workloadbuilder.GangSchedulingPolicy{MinCount: ptr.To(int32(len(rootChildren)))},
			},
			Constraints:       toUpstreamTopologyConstraints(podGang.Spec.TopologyConstraint),
			PriorityClassName: podGang.Spec.PriorityClassName,
		},
		Children: rootChildren,
	}, nil
}

// validateWorkloadLimits applies the upstream width limit independently to each
// template list. The depth check also protects future extensions of the mapping.
// fldPath points into the generated Workload; depth counts the root as level one.
func validateWorkloadLimits(item *workloadbuilder.WorkloadItem, fldPath *field.Path, depth int) error {
	if depth > schedulingv1beta1.WorkloadMaxTreeDepth {
		return field.Invalid(fldPath, item.Name, fmt.Sprintf("WAS template depth must not exceed %d", schedulingv1beta1.WorkloadMaxTreeDepth))
	}
	leafCount, compositeCount := 0, 0
	for _, child := range item.Children {
		if len(child.Children) == 0 {
			leafCount++
		} else {
			compositeCount++
		}
	}
	if leafCount > schedulingv1beta1.WorkloadMaxPodGroupTemplates {
		return field.TooMany(fldPath.Child("podGroupTemplates"), leafCount, schedulingv1beta1.WorkloadMaxPodGroupTemplates)
	}
	if compositeCount > schedulingv1beta1.WorkloadMaxPodGroupTemplates {
		return field.TooMany(fldPath.Child("compositePodGroupTemplates"), compositeCount, schedulingv1beta1.WorkloadMaxPodGroupTemplates)
	}
	leafIndex, compositeIndex := 0, 0
	for _, child := range item.Children {
		childPath := fldPath.Child("podGroupTemplates").Index(leafIndex)
		if len(child.Children) > 0 {
			childPath = fldPath.Child("compositePodGroupTemplates").Index(compositeIndex)
			compositeIndex++
		} else {
			leafIndex++
		}
		if err := validateWorkloadLimits(child, childPath, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// Preferred constraints are omitted from the mapping. Backend integration is
// responsible for warning about them while preserving these required constraints.
func toUpstreamTopologyConstraints(topologyConstraint *groveschedulerv1alpha1.TopologyConstraint) *workloadbuilder.SchedulingConstraints {
	if topologyConstraint == nil || topologyConstraint.PackConstraint == nil || topologyConstraint.PackConstraint.Required == nil {
		return nil
	}
	return &workloadbuilder.SchedulingConstraints{
		Topology: []schedulingv1beta1.TopologyConstraint{{Key: *topologyConstraint.PackConstraint.Required}},
	}
}

// Grove names are DNS subdomains, but Workload template names must be DNS labels.
// Hash the full name so dotted or long names remain distinct after translation.
func workloadTemplateName(prefix, groveName string) string {
	sum := sha256.Sum256([]byte(groveName))
	return fmt.Sprintf("%s-%x", prefix, sum[:templateNameHashBytes])
}

func leafTemplateName(podGroupName string) string {
	return workloadTemplateName("leaf", podGroupName)
}

func topologyGroupTemplateName(groupName string) string {
	return workloadTemplateName("group", groupName)
}
