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
	"fmt"
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	workloadbuilder "k8s.io/component-helpers/scheduling/schedulingv1/workloadbuilder"
	"k8s.io/utils/ptr"
)

func TestBuildWorkloadForPodGang_FlatHierarchy(t *testing.T) {
	podGang := testutils.NewPodGangBuilder("pcs-0", "test-ns").
		WithPodGroup("pcs-0-prefill", 2).
		WithPodGroup("pcs-0-decode", 1).
		WithLabels(map[string]string{"app": "inference", apicommon.LabelPodGang: "stale"}).
		Build()
	podGang.UID = "podgang-uid"

	workload, err := buildWorkloadForPodGang(podGang)
	require.NoError(t, err)
	assert.Equal(t, podGang.Name, workload.Name)
	assert.Equal(t, podGang.Namespace, workload.Namespace)
	assert.Equal(t, []metav1.OwnerReference{{
		APIVersion:         groveschedulerv1alpha1.SchemeGroupVersion.String(),
		Kind:               "PodGang",
		Name:               "pcs-0",
		UID:                "podgang-uid",
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}}, workload.OwnerReferences)
	assert.Equal(t, &schedulingv1beta1.TypedLocalObjectReference{
		APIGroup: groveschedulerv1alpha1.SchemeGroupVersion.Group,
		Kind:     "PodGang",
		Name:     "pcs-0",
	}, workload.Spec.ControllerRef)
	assert.Equal(t, map[string]string{"app": "inference", apicommon.LabelPodGang: "pcs-0"}, workload.Labels)
	assert.Empty(t, workload.Spec.PodGroupTemplates)
	require.Len(t, workload.Spec.CompositePodGroupTemplates, 1)

	root := workload.Spec.CompositePodGroupTemplates[0]
	assert.Equal(t, rootTemplateName, root.Name)
	assert.Nil(t, root.SchedulingPolicy.Basic)
	require.NotNil(t, root.SchedulingPolicy.Gang)
	assert.EqualValues(t, 2, root.SchedulingPolicy.Gang.MinGroupCount)
	assert.Nil(t, root.SchedulingConstraints)
	assert.Empty(t, root.CompositePodGroupTemplates)
	require.Len(t, root.PodGroupTemplates, 2)
	for i, leaf := range root.PodGroupTemplates {
		assert.Equal(t, leafTemplateName(podGang.Spec.PodGroups[i].Name), leaf.Name)
		assert.Nil(t, leaf.SchedulingPolicy.Basic)
		require.NotNil(t, leaf.SchedulingPolicy.Gang)
		assert.Equal(t, podGang.Spec.PodGroups[i].MinReplicas, leaf.SchedulingPolicy.Gang.MinCount)
		assert.Nil(t, leaf.SchedulingConstraints)
	}

	workload.Labels["app"] = "changed"
	assert.Equal(t, "inference", podGang.Labels["app"])
	assert.Equal(t, "stale", podGang.Labels[apicommon.LabelPodGang])
}

func TestBuildWorkloadForPodGang_NestedHierarchy(t *testing.T) {
	podGang := testutils.NewPodGangBuilder("pcs-0", "test-ns").
		WithPodGroup("prefill", 2).WithPodGroup("decode", 1).WithPodGroup("router", 3).Build()
	podGang.Spec.PriorityClassName = "inference"
	podGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To("topology.kubernetes.io/zone")},
	}
	podGang.Spec.PodGroups[0].TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To("kubernetes.io/hostname")},
	}
	podGang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{{
		Name:          "workers",
		PodGroupNames: []string{"prefill", "decode"},
		TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
			PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To("topology.kubernetes.io/rack")},
		},
	}}
	original := podGang.DeepCopy()

	workload, err := buildWorkloadForPodGang(podGang)
	require.NoError(t, err)
	assert.Equal(t, original, podGang, "mapping must not mutate the scheduling intent")
	require.Len(t, workload.Spec.CompositePodGroupTemplates, 1)
	root := workload.Spec.CompositePodGroupTemplates[0]
	require.NotNil(t, root.SchedulingPolicy.Gang)
	assert.EqualValues(t, 2, root.SchedulingPolicy.Gang.MinGroupCount, "gang all direct children, not all leaves")
	assert.Equal(t, "inference", root.PriorityClassName)
	assert.Equal(t, &schedulingv1beta1.CompositePodGroupSchedulingConstraints{
		Topology: []schedulingv1beta1.TopologyConstraint{{Key: "topology.kubernetes.io/zone"}},
	}, root.SchedulingConstraints)
	require.Len(t, root.CompositePodGroupTemplates, 1)
	child := root.CompositePodGroupTemplates[0]
	assert.Equal(t, topologyGroupTemplateName("workers"), child.Name)
	assert.Nil(t, child.SchedulingPolicy.Basic)
	require.NotNil(t, child.SchedulingPolicy.Gang)
	assert.EqualValues(t, 2, child.SchedulingPolicy.Gang.MinGroupCount)
	assert.Equal(t, "inference", child.PriorityClassName)
	assert.Equal(t, &schedulingv1beta1.CompositePodGroupSchedulingConstraints{
		Topology: []schedulingv1beta1.TopologyConstraint{{Key: "topology.kubernetes.io/rack"}},
	}, child.SchedulingConstraints)
	require.Len(t, child.PodGroupTemplates, 2)
	for i, leaf := range child.PodGroupTemplates {
		assert.Equal(t, leafTemplateName(podGang.Spec.PodGroups[i].Name), leaf.Name)
		assert.Equal(t, "inference", leaf.PriorityClassName)
		require.NotNil(t, leaf.SchedulingPolicy.Gang)
		assert.Equal(t, podGang.Spec.PodGroups[i].MinReplicas, leaf.SchedulingPolicy.Gang.MinCount)
	}
	assert.Equal(t, &schedulingv1beta1.PodGroupSchedulingConstraints{
		Topology: []schedulingv1beta1.TopologyConstraint{{Key: "kubernetes.io/hostname"}},
	}, child.PodGroupTemplates[0].SchedulingConstraints)
	assert.Nil(t, child.PodGroupTemplates[1].SchedulingConstraints)
	require.Len(t, root.PodGroupTemplates, 1)
	ungrouped := root.PodGroupTemplates[0]
	assert.Equal(t, leafTemplateName("router"), ungrouped.Name)
	assert.Equal(t, "inference", ungrouped.PriorityClassName)
	require.NotNil(t, ungrouped.SchedulingPolicy.Gang)
	assert.EqualValues(t, 3, ungrouped.SchedulingPolicy.Gang.MinCount)

	root.SchedulingConstraints.Topology[0].Key = "changed"
	child.PodGroupTemplates[0].SchedulingConstraints.Topology[0].Key = "changed"
	assert.Equal(t, original, podGang, "compiled constraints must not alias the input")
}

func TestBuildWorkloadForPodGang_AllLeavesGrouped(t *testing.T) {
	podGang := testutils.NewPodGangBuilder("pcs-0", "test-ns").
		WithPodGroup("prefill", 2).WithPodGroup("decode", 1).Build()
	podGang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
		{Name: "workers", PodGroupNames: []string{"decode", "prefill"}},
		{Name: "empty"},
	}

	workload, err := buildWorkloadForPodGang(podGang)
	require.NoError(t, err)
	require.Len(t, workload.Spec.CompositePodGroupTemplates, 1)
	root := workload.Spec.CompositePodGroupTemplates[0]
	assert.Empty(t, root.PodGroupTemplates)
	require.NotNil(t, root.SchedulingPolicy.Gang)
	assert.EqualValues(t, 1, root.SchedulingPolicy.Gang.MinGroupCount)
	require.Len(t, root.CompositePodGroupTemplates, 1)
	child := root.CompositePodGroupTemplates[0]
	require.Len(t, child.PodGroupTemplates, 2)
	assert.Equal(t, leafTemplateName("decode"), child.PodGroupTemplates[0].Name)
	assert.Equal(t, leafTemplateName("prefill"), child.PodGroupTemplates[1].Name)
}

func TestBuildWorkloadForPodGang_InvalidMapping(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*groveschedulerv1alpha1.PodGang)
		errorType field.ErrorType
		errorPath string
	}{
		{
			name: "empty gang", errorType: field.ErrorTypeRequired, errorPath: "spec.podgroups",
			mutate: func(pg *groveschedulerv1alpha1.PodGang) { pg.Spec.PodGroups = nil },
		},
		{
			name: "empty leaf name", errorType: field.ErrorTypeRequired, errorPath: "spec.podgroups[0].name",
			mutate: func(pg *groveschedulerv1alpha1.PodGang) { pg.Spec.PodGroups[0].Name = "" },
		},
		{
			name: "duplicate leaf name", errorType: field.ErrorTypeDuplicate, errorPath: "spec.podgroups[1].name",
			mutate: func(pg *groveschedulerv1alpha1.PodGang) { pg.Spec.PodGroups[1].Name = pg.Spec.PodGroups[0].Name },
		},
		{
			name: "zero minimum", errorType: field.ErrorTypeInvalid, errorPath: "spec.podgroups[0].minReplicas",
			mutate: func(pg *groveschedulerv1alpha1.PodGang) { pg.Spec.PodGroups[0].MinReplicas = 0 },
		},
		{
			name: "negative minimum", errorType: field.ErrorTypeInvalid, errorPath: "spec.podgroups[0].minReplicas",
			mutate: func(pg *groveschedulerv1alpha1.PodGang) { pg.Spec.PodGroups[0].MinReplicas = -1 },
		},
		{
			name: "unknown topology member", errorType: field.ErrorTypeNotFound, errorPath: "spec.topologyConstraintGroupConfigs[0].podGroupNames[0]",
			mutate: func(pg *groveschedulerv1alpha1.PodGang) {
				pg.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
					{Name: "workers", PodGroupNames: []string{"missing"}},
				}
			},
		},
		{
			name: "duplicate member within group", errorType: field.ErrorTypeDuplicate, errorPath: "spec.topologyConstraintGroupConfigs[0].podGroupNames[1]",
			mutate: func(pg *groveschedulerv1alpha1.PodGang) {
				pg.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
					{Name: "workers", PodGroupNames: []string{"prefill", "prefill"}},
				}
			},
		},
		{
			name: "overlapping groups", errorType: field.ErrorTypeDuplicate, errorPath: "spec.topologyConstraintGroupConfigs[1].podGroupNames[0]",
			mutate: func(pg *groveschedulerv1alpha1.PodGang) {
				pg.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
					{Name: "workers-a", PodGroupNames: []string{"prefill"}},
					{Name: "workers-b", PodGroupNames: []string{"prefill"}},
				}
			},
		},
		{
			name: "duplicate topology name", errorType: field.ErrorTypeDuplicate, errorPath: "spec.topologyConstraintGroupConfigs[1].name",
			mutate: func(pg *groveschedulerv1alpha1.PodGang) {
				pg.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
					{Name: "workers", PodGroupNames: []string{"prefill"}},
					{Name: "workers", PodGroupNames: []string{"decode"}},
				}
			},
		},
		{
			name: "empty topology name", errorType: field.ErrorTypeRequired, errorPath: "spec.topologyConstraintGroupConfigs[0].name",
			mutate: func(pg *groveschedulerv1alpha1.PodGang) {
				pg.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
					{PodGroupNames: []string{"prefill"}},
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			podGang := testutils.NewPodGangBuilder("pcs-0", "test-ns").
				WithPodGroup("prefill", 2).WithPodGroup("decode", 1).Build()
			tt.mutate(podGang)
			workload, err := buildWorkloadForPodGang(podGang)
			require.Nil(t, workload)
			var fieldErr *field.Error
			require.ErrorAs(t, err, &fieldErr)
			assert.Equal(t, tt.errorType, fieldErr.Type)
			assert.Equal(t, tt.errorPath, fieldErr.Field)
		})
	}
}

func TestBuildWorkloadForPodGang_Nil(t *testing.T) {
	workload, err := buildWorkloadForPodGang(nil)
	assert.Nil(t, workload)
	var fieldErr *field.Error
	require.ErrorAs(t, err, &fieldErr)
	assert.Equal(t, field.ErrorTypeRequired, fieldErr.Type)
	assert.Equal(t, "podGang", fieldErr.Field)
}

func TestBuildWorkloadForPodGang_TemplateListLimits(t *testing.T) {
	tests := []struct {
		name             string
		directLeaves     int
		composites       int
		leavesPerGroup   int
		exceededListPath string
	}{
		{name: "both lists at limit", directLeaves: 8, composites: 8, leavesPerGroup: 1},
		{name: "nested leaves at limit", composites: 1, leavesPerGroup: 8},
		{
			name: "direct leaf list too wide", directLeaves: 9, composites: 1, leavesPerGroup: 1,
			exceededListPath: "spec.compositePodGroupTemplates[0].podGroupTemplates",
		},
		{
			name: "composite list too wide", directLeaves: 1, composites: 9, leavesPerGroup: 1,
			exceededListPath: "spec.compositePodGroupTemplates[0].compositePodGroupTemplates",
		},
		{
			name: "nested leaf list too wide", composites: 1, leavesPerGroup: 9,
			exceededListPath: "spec.compositePodGroupTemplates[0].compositePodGroupTemplates[0].podGroupTemplates",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := testutils.NewPodGangBuilder("pcs-0", "test-ns")
			for i := 0; i < tt.directLeaves; i++ {
				builder.WithPodGroup(fmt.Sprintf("direct-%d", i), 1)
			}
			podGang := builder.Build()
			for i := 0; i < tt.composites; i++ {
				group := groveschedulerv1alpha1.TopologyConstraintGroupConfig{Name: fmt.Sprintf("group-%d", i)}
				for j := 0; j < tt.leavesPerGroup; j++ {
					name := fmt.Sprintf("group-%d-leaf-%d", i, j)
					builder.WithPodGroup(name, 1)
					group.PodGroupNames = append(group.PodGroupNames, name)
				}
				podGang.Spec.TopologyConstraintGroupConfigs = append(podGang.Spec.TopologyConstraintGroupConfigs, group)
			}
			workload, err := buildWorkloadForPodGang(podGang)
			if tt.exceededListPath != "" {
				require.Nil(t, workload)
				var fieldErr *field.Error
				require.ErrorAs(t, err, &fieldErr)
				assert.Equal(t, field.ErrorTypeTooMany, fieldErr.Type)
				assert.Equal(t, tt.exceededListPath, fieldErr.Field)
				return
			}
			require.NoError(t, err)
			require.Len(t, workload.Spec.CompositePodGroupTemplates, 1)
			root := workload.Spec.CompositePodGroupTemplates[0]
			assert.Len(t, root.PodGroupTemplates, tt.directLeaves)
			assert.Len(t, root.CompositePodGroupTemplates, tt.composites)
			require.NotNil(t, root.SchedulingPolicy.Gang)
			assert.EqualValues(t, tt.directLeaves+tt.composites, root.SchedulingPolicy.Gang.MinGroupCount)
			for _, child := range root.CompositePodGroupTemplates {
				assert.Len(t, child.PodGroupTemplates, tt.leavesPerGroup)
			}
		})
	}
}

func TestValidateWorkloadLimits_Depth(t *testing.T) {
	for _, depth := range []int{schedulingv1beta1.WorkloadMaxTreeDepth, schedulingv1beta1.WorkloadMaxTreeDepth + 1} {
		t.Run(fmt.Sprintf("depth-%d", depth), func(t *testing.T) {
			root := &workloadbuilder.WorkloadItem{Name: "leaf"}
			for i := 1; i < depth; i++ {
				root = &workloadbuilder.WorkloadItem{Name: fmt.Sprintf("group-%d", i), Children: []*workloadbuilder.WorkloadItem{root}}
			}
			err := validateWorkloadLimits(root, field.NewPath("spec", "compositePodGroupTemplates").Index(0), 1)
			if depth == schedulingv1beta1.WorkloadMaxTreeDepth {
				require.NoError(t, err)
				return
			}
			var fieldErr *field.Error
			require.ErrorAs(t, err, &fieldErr)
			assert.Equal(t, field.ErrorTypeInvalid, fieldErr.Type)
			assert.Equal(t, "leaf", fieldErr.BadValue)
		})
	}
}

func TestToUpstreamTopologyConstraints(t *testing.T) {
	tests := []struct {
		name       string
		constraint *groveschedulerv1alpha1.TopologyConstraint
		want       *workloadbuilder.SchedulingConstraints
	}{
		{name: "no topology"},
		{name: "no pack constraint", constraint: &groveschedulerv1alpha1.TopologyConstraint{}},
		{
			name: "preferred only",
			constraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Preferred: ptr.To("kubernetes.io/hostname")},
			},
		},
		{
			name: "required",
			constraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To("topology.kubernetes.io/rack")},
			},
			want: &workloadbuilder.SchedulingConstraints{Topology: []schedulingv1beta1.TopologyConstraint{{Key: "topology.kubernetes.io/rack"}}},
		},
		{
			name: "required survives preferred",
			constraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{
					Required: ptr.To("topology.kubernetes.io/rack"), Preferred: ptr.To("kubernetes.io/hostname"),
				},
			},
			want: &workloadbuilder.SchedulingConstraints{Topology: []schedulingv1beta1.TopologyConstraint{{Key: "topology.kubernetes.io/rack"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toUpstreamTopologyConstraints(tt.constraint))
		})
	}
}

func TestWorkloadTemplateNames(t *testing.T) {
	names := []string{"worker", "worker.example", "worker-example", strings.Repeat("a", 63) + ".worker"}
	seen := make(map[string]bool)
	for _, name := range names {
		for _, templateName := range []string{leafTemplateName(name), topologyGroupTemplateName(name)} {
			assert.Empty(t, validation.IsDNS1123Label(templateName))
			assert.False(t, seen[templateName], "distinct Grove names and template kinds must remain distinct")
			seen[templateName] = true
		}
		assert.Equal(t, leafTemplateName(name), leafTemplateName(name))
		assert.Equal(t, topologyGroupTemplateName(name), topologyGroupTemplateName(name))
	}
}
