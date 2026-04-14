// /*
// Copyright 2025 The Grove Authors.
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

package kai

import (
	"context"
	"fmt"
	"reflect"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	kaischedulingv2alpha2 "github.com/NVIDIA/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// schedulerBackend implements the scheduler Backend interface (Backend in scheduler package) for KAI scheduler.
type schedulerBackend struct {
	client        client.Client
	scheme        *runtime.Scheme
	name          string
	eventRecorder record.EventRecorder
	profile       configv1alpha1.SchedulerProfile
}

var _ scheduler.Backend = (*schedulerBackend)(nil)

const (
	labelKeyQueueName        = "kai.scheduler/queue"
	labelKeyNodePoolName     = "kai.scheduler/node-pool"
	annotationKeyIgnoreGrove = "grove.io/ignore"
	annotationValIgnoreGrove = "true"
)

// New creates a new KAI backend instance. profile is the scheduler profile for kai-scheduler;
// schedulerBackend uses profile.Name and may unmarshal profile.Config for kai-specific options.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, profile configv1alpha1.SchedulerProfile) scheduler.Backend {
	return &schedulerBackend{
		client:        cl,
		scheme:        scheme,
		name:          string(configv1alpha1.SchedulerNameKai),
		eventRecorder: eventRecorder,
		profile:       profile,
	}
}

// Name returns the pod-facing scheduler name (kai-scheduler), for lookup and logging.
func (b *schedulerBackend) Name() string {
	return b.name
}

// Init initializes the KAI backend
func (b *schedulerBackend) Init() error {
	return nil
}

// SyncPodGang converts PodGang to KAI PodGroup and synchronizes it
func (b *schedulerBackend) SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	if podGang == nil {
		return fmt.Errorf("podGang is nil")
	}
	if err := b.ensurePodGangIgnoredByGrovePlugin(ctx, podGang); err != nil {
		return err
	}

	newPodGroup, err := b.buildPodGroupForPodGang(podGang)
	if err != nil {
		return err
	}

	oldPodGroup := &kaischedulingv2alpha2.PodGroup{}
	key := client.ObjectKeyFromObject(newPodGroup)
	if err = b.client.Get(ctx, key, oldPodGroup); err != nil {
		if apierrors.IsNotFound(err) {
			return b.client.Create(ctx, newPodGroup)
		}
		return err
	}

	newPodGroup = b.inheritRuntimeManagedFields(oldPodGroup, newPodGroup)
	if podGroupsEqual(oldPodGroup, newPodGroup) {
		return nil
	}
	updatePodGroup(oldPodGroup, newPodGroup)
	return b.client.Update(ctx, oldPodGroup)
}

// OnPodGangDelete removes the PodGroup owned by this PodGang
func (b *schedulerBackend) OnPodGangDelete(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	if podGang == nil {
		return nil
	}
	return client.IgnoreNotFound(b.client.Delete(ctx, &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podGang.Name,
			Namespace: podGang.Namespace,
		},
	}))
}

// PreparePod adds KAI scheduler-specific configuration to the Pod.
// Sets Pod.Spec.SchedulerName so the pod is scheduled by KAI.
func (b *schedulerBackend) PreparePod(pod *corev1.Pod) {
	pod.Spec.SchedulerName = b.Name()
}

// ValidatePodCliqueSet runs KAI-specific validations on the PodCliqueSet.
func (b *schedulerBackend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}

// ensurePodGangIgnoredByGrovePlugin marks PodGang so legacy Grove podgrouper ignores it.
func (b *schedulerBackend) ensurePodGangIgnoredByGrovePlugin(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	if podGang.Annotations != nil && podGang.Annotations[annotationKeyIgnoreGrove] == annotationValIgnoreGrove {
		return nil
	}
	patchBase := podGang.DeepCopy()
	if podGang.Annotations == nil {
		podGang.Annotations = map[string]string{}
	}
	podGang.Annotations[annotationKeyIgnoreGrove] = annotationValIgnoreGrove
	return b.client.Patch(ctx, podGang, client.MergeFrom(patchBase))
}

// buildPodGroupForPodGang translates a Grove PodGang into a KAI PodGroup object.
func (b *schedulerBackend) buildPodGroupForPodGang(podGang *groveschedulerv1alpha1.PodGang) (*kaischedulingv2alpha2.PodGroup, error) {
	topologyName := getTopologyName(podGang)
	topologyConstraint, err := toKAITopologyConstraint(podGang.Spec.TopologyConstraint, topologyName)
	if err != nil {
		return nil, err
	}

	parentBySubGroupName := map[string]string{}
	subGroups := make([]kaischedulingv2alpha2.SubGroup, 0, len(podGang.Spec.TopologyConstraintGroupConfigs)+len(podGang.Spec.PodGroups))

	for _, groupConfig := range podGang.Spec.TopologyConstraintGroupConfigs {
		groupTopologyConstraint, groupErr := toKAITopologyConstraint(groupConfig.TopologyConstraint, topologyName)
		if groupErr != nil {
			return nil, groupErr
		}
		subGroups = append(subGroups, kaischedulingv2alpha2.SubGroup{
			Name:               groupConfig.Name,
			MinMember:          0,
			TopologyConstraint: groupTopologyConstraint,
		})
		for _, podGroupName := range groupConfig.PodGroupNames {
			parentBySubGroupName[podGroupName] = groupConfig.Name
		}
	}

	var minMember int32
	for _, podGroup := range podGang.Spec.PodGroups {
		subGroupTopologyConstraint, groupErr := toKAITopologyConstraint(podGroup.TopologyConstraint, topologyName)
		if groupErr != nil {
			return nil, groupErr
		}
		subGroup := kaischedulingv2alpha2.SubGroup{
			Name:               podGroup.Name,
			MinMember:          podGroup.MinReplicas,
			TopologyConstraint: subGroupTopologyConstraint,
		}
		if parentName, found := parentBySubGroupName[podGroup.Name]; found {
			subGroup.Parent = ptr.To(parentName)
		}
		subGroups = append(subGroups, subGroup)
		minMember += podGroup.MinReplicas
	}

	result := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podGang.Name,
			Namespace:   podGang.Namespace,
			Labels:      cloneStringMap(podGang.Labels),
			Annotations: cloneStringMap(podGang.Annotations),
		},
		Spec: kaischedulingv2alpha2.PodGroupSpec{
			MinMember:         minMember,
			Queue:             resolveQueueName(podGang),
			PriorityClassName: podGang.Spec.PriorityClassName,
			SubGroups:         subGroups,
		},
	}
	if topologyConstraint != nil {
		result.Spec.TopologyConstraint = *topologyConstraint
	}
	if err := controllerutil.SetControllerReference(podGang, result, b.scheme); err != nil {
		return nil, err
	}
	return result, nil
}

// getTopologyName resolves topology name from PodGang annotations with fallback keys.
func getTopologyName(podGang *groveschedulerv1alpha1.PodGang) string {
	if podGang.Annotations == nil {
		return ""
	}
	if topologyName := podGang.Annotations[apicommonconstants.AnnotationTopologyName]; topologyName != "" {
		return topologyName
	}
	// Backward compatibility with KAI annotation key.
	return podGang.Annotations["kai.scheduler/topology"]
}

// toKAITopologyConstraint converts Grove topology constraint to KAI topology constraint.
func toKAITopologyConstraint(topologyConstraint *groveschedulerv1alpha1.TopologyConstraint, topologyName string) (*kaischedulingv2alpha2.TopologyConstraint, error) {
	if topologyConstraint == nil || topologyConstraint.PackConstraint == nil {
		return nil, nil
	}
	if topologyName == "" {
		return nil, fmt.Errorf("topology name cannot be empty when topology constraints are defined")
	}
	result := &kaischedulingv2alpha2.TopologyConstraint{
		Topology: topologyName,
	}
	if topologyConstraint.PackConstraint.Preferred != nil {
		result.PreferredTopologyLevel = *topologyConstraint.PackConstraint.Preferred
	}
	if topologyConstraint.PackConstraint.Required != nil {
		result.RequiredTopologyLevel = *topologyConstraint.PackConstraint.Required
	}
	return result, nil
}

// resolveQueueName returns queue from labels first, then falls back to annotations.
func resolveQueueName(podGang *groveschedulerv1alpha1.PodGang) string {
	if podGang.Labels != nil && podGang.Labels[labelKeyQueueName] != "" {
		return podGang.Labels[labelKeyQueueName]
	}
	if podGang.Annotations != nil {
		return podGang.Annotations[labelKeyQueueName]
	}
	return ""
}

// inheritRuntimeManagedFields preserves fields that are managed by KAI runtime components.
func (b *schedulerBackend) inheritRuntimeManagedFields(oldPodGroup, newPodGroup *kaischedulingv2alpha2.PodGroup) *kaischedulingv2alpha2.PodGroup {
	newPodGroupCopy := newPodGroup.DeepCopy()
	// These fields are managed by KAI components after initial creation.
	newPodGroupCopy.Spec.MarkUnschedulable = oldPodGroup.Spec.MarkUnschedulable
	newPodGroupCopy.Spec.SchedulingBackoff = oldPodGroup.Spec.SchedulingBackoff
	newPodGroupCopy.Spec.Queue = oldPodGroup.Spec.Queue

	if newPodGroupCopy.Labels == nil {
		newPodGroupCopy.Labels = map[string]string{}
	}
	if nodePoolName := oldPodGroup.Labels[labelKeyNodePoolName]; nodePoolName != "" {
		newPodGroupCopy.Labels[labelKeyNodePoolName] = nodePoolName
	}
	if queueName := oldPodGroup.Labels[labelKeyQueueName]; queueName != "" {
		newPodGroupCopy.Labels[labelKeyQueueName] = queueName
	}
	return newPodGroupCopy
}

// podGroupsEqual compares spec plus source-owned metadata fields for update decisions.
func podGroupsEqual(oldPodGroup, newPodGroup *kaischedulingv2alpha2.PodGroup) bool {
	return reflect.DeepEqual(oldPodGroup.Spec, newPodGroup.Spec) &&
		reflect.DeepEqual(oldPodGroup.OwnerReferences, newPodGroup.OwnerReferences) &&
		mapsEqualBySourceKeys(newPodGroup.Labels, oldPodGroup.Labels) &&
		mapsEqualBySourceKeys(newPodGroup.Annotations, oldPodGroup.Annotations)
}

// mapsEqualBySourceKeys checks whether target contains all key-values from source.
func mapsEqualBySourceKeys(source, target map[string]string) bool {
	if source != nil && target == nil {
		return false
	}
	for key, sourceValue := range source {
		if targetValue, exists := target[key]; !exists || targetValue != sourceValue {
			return false
		}
	}
	return true
}

// updatePodGroup copies desired fields from newPodGroup into existing object.
func updatePodGroup(oldPodGroup, newPodGroup *kaischedulingv2alpha2.PodGroup) {
	oldPodGroup.Annotations = copyStringMap(newPodGroup.Annotations, oldPodGroup.Annotations)
	oldPodGroup.Labels = copyStringMap(newPodGroup.Labels, oldPodGroup.Labels)
	oldPodGroup.Spec = newPodGroup.Spec
	oldPodGroup.OwnerReferences = newPodGroup.OwnerReferences
}

// copyStringMap copies all key-values from source into target map.
func copyStringMap(source, target map[string]string) map[string]string {
	if source != nil && target == nil {
		target = map[string]string{}
	}
	for k, v := range source {
		target[k] = v
	}
	return target
}

// cloneStringMap returns a shallow copy of the input string map.
func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for k, v := range input {
		cloned[k] = v
	}
	return cloned
}
