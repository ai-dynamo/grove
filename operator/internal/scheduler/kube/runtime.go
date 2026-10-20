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
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	workloadbuilder "k8s.io/component-helpers/scheduling/schedulingv1/workloadbuilder"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A clique can span several anchor epochs. Runtime membership must identify
// both the gang and the clique; template names are only local to a Workload.
func runtimeLeafName(gangName, cliqueName string) string {
	return workloadTemplateName("pg", gangName+"\x00"+cliqueName)
}

func runtimeCompositeName(gangName, templateName string) string {
	if templateName == rootTemplateName {
		return gangName
	}
	return workloadTemplateName("cg", gangName+"\x00"+templateName)
}

type hierarchyObject struct {
	desired  client.Object
	existing client.Object
}

func (b *schedulerBackend) syncWorkloadHierarchy(ctx context.Context, gang *groveschedulerv1alpha1.PodGang) error {
	objects, err := b.readHierarchy(ctx, gang)
	if err != nil {
		return err
	}
	// Validate the entire hierarchy before writing anything. In particular, a
	// missing Workload must not hide incompatible surviving runtime groups.
	for _, object := range objects {
		if object.existing == nil {
			if err := b.client.Create(ctx, object.desired); err != nil {
				return fmt.Errorf("create WAS %T %s: %w", object.desired, client.ObjectKeyFromObject(object.desired), err)
			}
			continue
		}
		before := object.existing.DeepCopyObject().(client.Object)
		copyMutableSchedulingFields(object.existing, object.desired)
		labels := maps.Clone(object.existing.GetLabels())
		if labels == nil {
			labels = make(map[string]string)
		}
		maps.Copy(labels, object.desired.GetLabels())
		object.existing.SetLabels(labels)
		if equality.Semantic.DeepEqual(before, object.existing) {
			continue
		}
		if err := b.client.Patch(ctx, object.existing, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return fmt.Errorf("patch WAS %T %s: %w", object.existing, client.ObjectKeyFromObject(object.existing), err)
		}
	}
	return nil
}

func (b *schedulerBackend) readHierarchy(ctx context.Context, gang *groveschedulerv1alpha1.PodGang) ([]hierarchyObject, error) {
	effective, err := b.withPersistedMinimums(ctx, gang)
	if err != nil {
		return nil, err
	}
	workload, err := buildWorkloadForPodGang(effective)
	if err != nil {
		return nil, err
	}
	desired, err := materializeHierarchy(gang, workload)
	if err != nil {
		return nil, err
	}
	result := make([]hierarchyObject, 0, len(desired))
	for _, object := range desired {
		existing := object.DeepCopyObject().(client.Object)
		if err := b.client.Get(ctx, client.ObjectKeyFromObject(object), existing); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("get WAS %T %s: %w", object, client.ObjectKeyFromObject(object), err)
			}
			result = append(result, hierarchyObject{desired: object})
			continue
		}
		if !metav1.IsControlledBy(existing, gang) {
			return nil, fmt.Errorf("WAS %T %s is not controlled by PodGang %s", existing, client.ObjectKeyFromObject(existing), client.ObjectKeyFromObject(gang))
		}
		if !existing.GetDeletionTimestamp().IsZero() {
			return nil, fmt.Errorf("WAS %T %s is terminating; waiting for its protection controller", existing, client.ObjectKeyFromObject(existing))
		}
		if !hierarchyStructureEqual(existing, object) {
			// Long-running Pods can hold the PodGroup protection finalizer
			// indefinitely. Never delete their hierarchy or claim a same-name
			// replacement completed; the caller must roll out a new gang epoch.
			return nil, fmt.Errorf("WAS %T %s has incompatible immutable scheduling fields; roll out a new PodGang epoch before replacing the hierarchy", existing, client.ObjectKeyFromObject(existing))
		}
		result = append(result, hierarchyObject{desired: object, existing: existing})
	}
	return result, nil
}

// A released minimum is not a new zero-minimum gang. Recover only from objects
// owned by this gang, and keep the pure mapper independent of persisted state.
func (b *schedulerBackend) withPersistedMinimums(ctx context.Context, gang *groveschedulerv1alpha1.PodGang) (*groveschedulerv1alpha1.PodGang, error) {
	if gang == nil {
		return nil, fmt.Errorf("PodGang must not be nil")
	}
	effective := gang.DeepCopy()
	if !slices.ContainsFunc(effective.Spec.PodGroups, func(group groveschedulerv1alpha1.PodGroup) bool { return group.MinReplicas == 0 }) {
		return effective, nil
	}
	workload := &schedulingv1beta1.Workload{}
	err := b.client.Get(ctx, client.ObjectKeyFromObject(gang), workload)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	var templates map[string]*schedulingv1beta1.PodGroupTemplate
	if err == nil && metav1.IsControlledBy(workload, gang) {
		templates = indexLeafTemplates(workload)
	}
	for i := range effective.Spec.PodGroups {
		group := &effective.Spec.PodGroups[i]
		if group.MinReplicas != 0 {
			continue
		}
		if leaf := templates[leafTemplateName(group.Name)]; leaf != nil && leaf.SchedulingPolicy.Gang != nil {
			group.MinReplicas = leaf.SchedulingPolicy.Gang.MinCount
		}
		if group.MinReplicas > 0 {
			continue
		}
		leaf := &schedulingv1beta1.PodGroup{}
		key := client.ObjectKey{Namespace: gang.Namespace, Name: runtimeLeafName(gang.Name, group.Name)}
		if err := b.client.Get(ctx, key, leaf); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, err
			}
			continue
		}
		if metav1.IsControlledBy(leaf, gang) && leaf.Spec.SchedulingPolicy.Gang != nil {
			group.MinReplicas = leaf.Spec.SchedulingPolicy.Gang.MinCount
		}
	}
	return effective, nil
}

func materializeHierarchy(gang *groveschedulerv1alpha1.PodGang, workload *schedulingv1beta1.Workload) ([]client.Object, error) {
	cliqueNames := make(map[string]string, len(gang.Spec.PodGroups))
	for _, group := range gang.Spec.PodGroups {
		cliqueNames[leafTemplateName(group.Name)] = group.Name
	}
	owner := metav1.NewControllerRef(gang, groveschedulerv1alpha1.SchemeGroupVersion.WithKind("PodGang"))
	builder := workloadbuilder.NewBuilderFromExistingWorkload(workload, workloadbuilder.BuildOptions{Owner: owner})
	objects := []client.Object{workload}
	var addComposite func(schedulingv1beta1.CompositePodGroupTemplate, *string) error
	addComposite = func(template schedulingv1beta1.CompositePodGroupTemplate, parent *string) error {
		name := runtimeCompositeName(gang.Name, template.Name)
		group, err := builder.NewCompositePodGroup(name, template.Name)
		if err != nil {
			return err
		}
		group.Spec.ParentCompositePodGroupName = parent
		group.Labels = maps.Clone(workload.Labels)
		objects = append(objects, group)
		for _, child := range template.CompositePodGroupTemplates {
			if err := addComposite(child, ptr.To(name)); err != nil {
				return err
			}
		}
		for _, leaf := range template.PodGroupTemplates {
			cliqueName, found := cliqueNames[leaf.Name]
			if !found {
				return fmt.Errorf("no Grove PodGroup for template %q", leaf.Name)
			}
			// NewPodGroup only indexes top-level templates. Grove's leaves
			// are nested, so copy the selected template explicitly.
			labels := maps.Clone(workload.Labels)
			labels[apicommon.LabelPodClique] = cliqueName
			objects = append(objects, &schedulingv1beta1.PodGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: runtimeLeafName(gang.Name, cliqueName), Namespace: gang.Namespace,
					OwnerReferences: []metav1.OwnerReference{*owner}, Labels: labels,
				},
				Spec: schedulingv1beta1.PodGroupSpec{
					ParentCompositePodGroupName: ptr.To(name),
					WorkloadRef: &schedulingv1beta1.WorkloadReference{
						WorkloadName: workload.Name, TemplateName: leaf.Name,
					},
					SchedulingPolicy: *leaf.SchedulingPolicy.DeepCopy(), SchedulingConstraints: leaf.SchedulingConstraints.DeepCopy(),
					DisruptionMode: leaf.DisruptionMode.DeepCopy(), PriorityClassName: leaf.PriorityClassName,
				},
			})
		}
		return nil
	}
	for _, root := range workload.Spec.CompositePodGroupTemplates {
		if err := addComposite(root, nil); err != nil {
			return nil, err
		}
	}
	return objects, nil
}

// Indexes are local to one operation; their pointers must not outlive sorting
// of the template slices.
func indexLeafTemplates(workload *schedulingv1beta1.Workload) map[string]*schedulingv1beta1.PodGroupTemplate {
	index := make(map[string]*schedulingv1beta1.PodGroupTemplate)
	var collect func([]schedulingv1beta1.CompositePodGroupTemplate, []schedulingv1beta1.PodGroupTemplate)
	collect = func(composites []schedulingv1beta1.CompositePodGroupTemplate, leaves []schedulingv1beta1.PodGroupTemplate) {
		for i := range leaves {
			index[leaves[i].Name] = &leaves[i]
		}
		for i := range composites {
			collect(composites[i].CompositePodGroupTemplates, composites[i].PodGroupTemplates)
		}
	}
	collect(workload.Spec.CompositePodGroupTemplates, workload.Spec.PodGroupTemplates)
	return index
}

// TODO: When WAS requires Kubernetes >= 1.38, update composite
// minGroupCount in place, including the corresponding Workload templates.
// Workload template membership changes remain unsupported.
func copyMutableSchedulingFields(existing, desired client.Object) {
	switch current := existing.(type) {
	case *schedulingv1beta1.Workload:
		target := indexLeafTemplates(desired.(*schedulingv1beta1.Workload))
		for name, leaf := range indexLeafTemplates(current) {
			if wanted := target[name]; wanted != nil && wanted.SchedulingPolicy.Gang != nil && leaf.SchedulingPolicy.Gang != nil {
				leaf.SchedulingPolicy.Gang.MinCount = wanted.SchedulingPolicy.Gang.MinCount
			}
		}
	case *schedulingv1beta1.PodGroup:
		target := desired.(*schedulingv1beta1.PodGroup)
		if current.Spec.SchedulingPolicy.Gang != nil && target.Spec.SchedulingPolicy.Gang != nil {
			current.Spec.SchedulingPolicy.Gang.MinCount = target.Spec.SchedulingPolicy.Gang.MinCount
		}
	}
}

func hierarchyStructureEqual(existing, desired client.Object) bool {
	actual := existing.DeepCopyObject().(client.Object)
	copyMutableSchedulingFields(actual, desired)
	switch current := actual.(type) {
	case *schedulingv1beta1.Workload:
		target := desired.(*schedulingv1beta1.Workload).DeepCopy()
		normalizeTemplates(current, target)
		normalizeTemplates(target, target)
		return equality.Semantic.DeepEqual(current.Spec, target.Spec)
	case *schedulingv1beta1.PodGroup:
		target := desired.(*schedulingv1beta1.PodGroup).DeepCopy()
		current.Spec.Priority, current.Spec.PreemptionPolicy = nil, nil
		if target.Spec.PriorityClassName == "" {
			current.Spec.PriorityClassName = ""
		}
		if current.Spec.DisruptionMode == nil {
			current.Spec.DisruptionMode = &schedulingv1beta1.DisruptionMode{Single: &schedulingv1beta1.SingleDisruptionMode{}}
		}
		if target.Spec.DisruptionMode == nil {
			target.Spec.DisruptionMode = &schedulingv1beta1.DisruptionMode{Single: &schedulingv1beta1.SingleDisruptionMode{}}
		}
		return equality.Semantic.DeepEqual(current.Spec, target.Spec)
	case *schedulingv1alpha3.CompositePodGroup:
		target := desired.(*schedulingv1alpha3.CompositePodGroup).DeepCopy()
		current.Spec.Priority, current.Spec.PreemptionPolicy = nil, nil
		if target.Spec.PriorityClassName == "" {
			current.Spec.PriorityClassName = ""
		}
		if target.Spec.DisruptionMode == nil {
			target.Spec.DisruptionMode = &schedulingv1alpha3.CompositeDisruptionMode{Single: &schedulingv1alpha3.SingleCompositeDisruptionMode{}}
		}
		if current.Spec.DisruptionMode == nil {
			current.Spec.DisruptionMode = &schedulingv1alpha3.CompositeDisruptionMode{Single: &schedulingv1alpha3.SingleCompositeDisruptionMode{}}
		}
		return equality.Semantic.DeepEqual(current.Spec, target.Spec)
	default:
		return false
	}
}

// API admission resolves priority and preemption defaults. Compare only intent,
// and canonicalize map-lists so an input reorder never requests replacement.
func normalizeTemplates(workload, target *schedulingv1beta1.Workload) {
	targetLeaves := indexLeafTemplates(target)
	for name, leaf := range indexLeafTemplates(workload) {
		leaf.Priority, leaf.PreemptionPolicy = nil, nil
		if leaf.DisruptionMode == nil {
			leaf.DisruptionMode = &schedulingv1beta1.DisruptionMode{Single: &schedulingv1beta1.SingleDisruptionMode{}}
		}
		if wanted := targetLeaves[name]; wanted != nil && wanted.PriorityClassName == "" {
			leaf.PriorityClassName = ""
		}
	}
	// Complete indexed lookups before reordering either template slice.
	implicitPriority := len(target.Spec.CompositePodGroupTemplates) > 0 && target.Spec.CompositePodGroupTemplates[0].PriorityClassName == ""
	var normalizeTree func([]schedulingv1beta1.CompositePodGroupTemplate, []schedulingv1beta1.PodGroupTemplate)
	normalizeTree = func(composites []schedulingv1beta1.CompositePodGroupTemplate, leaves []schedulingv1beta1.PodGroupTemplate) {
		slices.SortFunc(composites, func(a, b schedulingv1beta1.CompositePodGroupTemplate) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(leaves, func(a, b schedulingv1beta1.PodGroupTemplate) int { return strings.Compare(a.Name, b.Name) })
		for i := range composites {
			composites[i].Priority, composites[i].PreemptionPolicy = nil, nil
			if composites[i].DisruptionMode == nil {
				composites[i].DisruptionMode = &schedulingv1beta1.CompositeDisruptionMode{Single: &schedulingv1beta1.SingleCompositeDisruptionMode{}}
			}
			if implicitPriority {
				composites[i].PriorityClassName = ""
			}
			normalizeTree(composites[i].CompositePodGroupTemplates, composites[i].PodGroupTemplates)
		}
	}
	normalizeTree(workload.Spec.CompositePodGroupTemplates, workload.Spec.PodGroupTemplates)
}
