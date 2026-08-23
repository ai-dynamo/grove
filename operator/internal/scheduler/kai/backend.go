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

package kai

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sort"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	operatorutils "github.com/ai-dynamo/grove/operator/internal/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaitopologyv1alpha1 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/kai/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// schedulerBackend implements the scheduler Backend interface (Backend in scheduler package) for KAI scheduler.
type schedulerBackend struct {
	client         client.Client
	scheme         *runtime.Scheme
	name           string
	eventRecorder  record.EventRecorder
	profile        configv1alpha1.SchedulerProfile
	aggregateLocks operatorutils.KeyedMutex[client.ObjectKey]
}

var _ scheduler.Backend = (*schedulerBackend)(nil)

const (
	labelKeyQueueName     = "kai.scheduler/queue"
	labelKeyNodePoolName  = "kai.scheduler/node-pool"
	annotationKeySkipPGR  = "kai.scheduler/skip-podgrouper"
	annotationValSkipPGR  = "true"
	annotationPodGroup    = "pod-group-name"
	labelSubGroup         = "kai.scheduler/subgroup-name"
	eventReasonSyncFailed = "KAIBackendSyncFailed"
)

// New creates a new KAI backend instance. profile is the scheduler profile for kai-scheduler;
// schedulerBackend uses profile.Name and may unmarshal profile.Config for kai-specific options.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, profile configv1alpha1.SchedulerProfile) scheduler.Backend {
	return &schedulerBackend{
		client:        cl,
		scheme:        scheme,
		name:          string(profile.Name),
		eventRecorder: eventRecorder,
		profile:       profile,
	}
}

// Name returns the pod-facing scheduler name (kai-scheduler), for lookup and logging.
func (b *schedulerBackend) Name() string {
	return b.name
}

// Init registers the KAI API types into b.scheme and must be called before
// that scheme is used to serialize or deserialize KAI objects.
func (b *schedulerBackend) Init(_ client.Client) error {
	if err := kaitopologyv1alpha1.AddToScheme(b.scheme); err != nil {
		return err
	}
	return kaischedulingv2alpha2.AddToScheme(b.scheme)
}

// SyncPodGang converts PodGang to KAI PodGroup and synchronizes it
func (b *schedulerBackend) SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	if podGang == nil {
		return fmt.Errorf("podGang is nil")
	}
	pcs, err := b.getOwningPodCliqueSet(ctx, podGang)
	if err != nil {
		// The aggregate PodGroup is owned by the PCS, so it is already being garbage-collected
		// when the owner is gone. Do not strand a deleting PodGang on our finalizer.
		if apierrors.IsNotFound(err) && !podGang.DeletionTimestamp.IsZero() {
			if finalizerErr := b.removePodGangFinalizer(ctx, podGang); finalizerErr != nil {
				b.recordWarning(podGang, finalizerErr)
				return finalizerErr
			}
			return nil
		}
		b.recordWarning(podGang, err)
		return err
	}
	if !pcs.DeletionTimestamp.IsZero() {
		if err = b.removePodGangFinalizer(ctx, podGang); err != nil {
			b.recordWarning(podGang, err)
			return err
		}
		return nil
	}
	if podGang.DeletionTimestamp.IsZero() {
		if err = b.ensurePodGangMetadata(ctx, podGang); err != nil {
			b.recordWarning(podGang, err)
			return err
		}
	}
	if err = b.syncAggregatePodGroup(ctx, pcs, podGang); err != nil {
		b.recordWarning(podGang, err)
		return err
	}
	if !podGang.DeletionTimestamp.IsZero() {
		if err = b.removePodGangFinalizer(ctx, podGang); err != nil {
			b.recordWarning(podGang, err)
			return err
		}
	}
	return nil
}

// PreparePod adds KAI scheduler-specific configuration to the Pod.
// It sets externally-created PodGroup membership because KAI's podgrouper is skipped.
func (b *schedulerBackend) PreparePod(pod *corev1.Pod) error {
	podGangName := pod.Labels[apicommon.LabelPodGang]
	if podGangName == "" {
		return fmt.Errorf("KAI scheduler requires pod label %q", apicommon.LabelPodGang)
	}
	subGroupName := pod.Labels[apicommon.LabelPodClique]
	if subGroupName == "" {
		return fmt.Errorf("KAI scheduler requires pod label %q", apicommon.LabelPodClique)
	}
	pcsName := pod.Labels[apicommon.LabelPartOfKey]
	if pcsName == "" {
		return fmt.Errorf("KAI scheduler requires pod label %q", apicommon.LabelPartOfKey)
	}
	pcsReplica, err := podCliqueSetReplicaFromObjectMeta(pod.ObjectMeta)
	if err != nil {
		return err
	}

	pod.Spec.SchedulerName = b.Name()
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Annotations[annotationKeySkipPGR] = annotationValSkipPGR
	pod.Annotations[annotationPodGroup] = aggregatePodGroupName(pcsName, pcsReplica)
	pod.Labels[labelSubGroup] = stableKAIName(subGroupName)
	return nil
}

// ValidatePodCliqueSet runs KAI-specific validations on the PodCliqueSet.
func (b *schedulerBackend) ValidatePodCliqueSet(_ context.Context, pcs *grovecorev1alpha1.PodCliqueSet) error {
	_, err := resolveQueueNameForPodCliqueSet(pcs)
	return err
}

func (b *schedulerBackend) ensurePodGangMetadata(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	if podGang.Annotations != nil && podGang.Annotations[annotationKeySkipPGR] == annotationValSkipPGR && controllerutil.ContainsFinalizer(podGang, podGangFinalizer) {
		return nil
	}
	before := podGang.DeepCopy()
	if podGang.Annotations == nil {
		podGang.Annotations = map[string]string{}
	}
	podGang.Annotations[annotationKeySkipPGR] = annotationValSkipPGR
	controllerutil.AddFinalizer(podGang, podGangFinalizer)
	return b.client.Patch(ctx, podGang, client.MergeFrom(before))
}

func (b *schedulerBackend) removePodGangFinalizer(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	if !controllerutil.ContainsFinalizer(podGang, podGangFinalizer) {
		return nil
	}
	before := podGang.DeepCopy()
	controllerutil.RemoveFinalizer(podGang, podGangFinalizer)
	return b.client.Patch(ctx, podGang, client.MergeFrom(before))
}

func (b *schedulerBackend) recordWarning(obj runtime.Object, err error) {
	if b.eventRecorder != nil && obj != nil && err != nil {
		b.eventRecorder.Eventf(obj, corev1.EventTypeWarning, eventReasonSyncFailed, "%v", err)
	}
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

func (b *schedulerBackend) getOwningPodCliqueSet(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) (*grovecorev1alpha1.PodCliqueSet, error) {
	owner := metav1.GetControllerOf(podGang)
	if owner == nil {
		return nil, fmt.Errorf("podgang %s/%s has no controlling PodCliqueSet", podGang.Namespace, podGang.Name)
	}
	if owner.APIVersion != grovecorev1alpha1.SchemeGroupVersion.String() || owner.Kind != "PodCliqueSet" {
		return nil, fmt.Errorf("podgang %s/%s is controlled by %s %q, expected PodCliqueSet", podGang.Namespace, podGang.Name, owner.APIVersion, owner.Kind)
	}

	pcs := &grovecorev1alpha1.PodCliqueSet{}
	if err := b.client.Get(ctx, client.ObjectKey{Namespace: podGang.Namespace, Name: owner.Name}, pcs); err != nil {
		return nil, fmt.Errorf("get controlling PodCliqueSet %s/%s: %w", podGang.Namespace, owner.Name, err)
	}
	return pcs, nil
}

func resolveQueueNameForPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) (string, error) {
	if queueName := resolveQueueNameFromMetadata(pcs.Labels, pcs.Annotations); queueName != "" {
		for _, clique := range pcs.Spec.Template.Cliques {
			if clique == nil {
				continue
			}
			if templateQueueName := resolveQueueNameFromMetadata(clique.Labels, clique.Annotations); templateQueueName != "" && templateQueueName != queueName {
				return "", fmt.Errorf("KAI queue on PodCliqueSet %s/%s is %q but PodClique template %q resolves to %q", pcs.Namespace, pcs.Name, queueName, clique.Name, templateQueueName)
			}
		}
		return queueName, nil
	}

	queueNames := map[string]struct{}{}
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique == nil {
			continue
		}
		if queueName := resolveQueueNameFromMetadata(clique.Labels, clique.Annotations); queueName != "" {
			queueNames[queueName] = struct{}{}
		}
	}

	switch len(queueNames) {
	case 0:
		return "", fmt.Errorf("no KAI queue is configured on PodCliqueSet %s/%s or its PodClique templates", pcs.Namespace, pcs.Name)
	case 1:
		for queueName := range queueNames {
			return queueName, nil
		}
	}

	queueNamesList := make([]string, 0, len(queueNames))
	for queueName := range queueNames {
		queueNamesList = append(queueNamesList, queueName)
	}
	sort.Strings(queueNamesList)
	return "", fmt.Errorf("conflicting KAI queues on PodCliqueSet %s/%s PodClique templates: %v", pcs.Namespace, pcs.Name, queueNamesList)
}

// resolveQueueNameFromMetadata returns queue from labels first, then falls back to annotations.
func resolveQueueNameFromMetadata(labels, annotations map[string]string) string {
	if labels != nil && labels[labelKeyQueueName] != "" {
		return labels[labelKeyQueueName]
	}
	if annotations != nil {
		return annotations[labelKeyQueueName]
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
		operatorutils.MapContainsAll(oldPodGroup.Labels, newPodGroup.Labels) &&
		operatorutils.MapContainsAll(oldPodGroup.Annotations, newPodGroup.Annotations)
}

// updatePodGroup copies desired fields from newPodGroup into existing object.
func updatePodGroup(oldPodGroup, newPodGroup *kaischedulingv2alpha2.PodGroup) {
	if newPodGroup.Annotations != nil {
		if oldPodGroup.Annotations == nil {
			oldPodGroup.Annotations = map[string]string{}
		}
		maps.Copy(oldPodGroup.Annotations, newPodGroup.Annotations)
	}
	if newPodGroup.Labels != nil {
		if oldPodGroup.Labels == nil {
			oldPodGroup.Labels = map[string]string{}
		}
		maps.Copy(oldPodGroup.Labels, newPodGroup.Labels)
	}
	oldPodGroup.Spec = newPodGroup.Spec
	oldPodGroup.OwnerReferences = newPodGroup.OwnerReferences
}
