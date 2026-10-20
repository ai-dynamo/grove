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

package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// schedulerBackend implements the scheduler backend interface (Backend in scheduler package) for Kubernetes default scheduler.
// Without gang scheduling it does minimal work - just sets the scheduler name on pods.
// With gang scheduling enabled it translates each Grove PodGang into an upstream
// Workload / CompositePodGroup / PodGroup hierarchy (GREP-531).
type schedulerBackend struct {
	client        client.Client
	scheme        *runtime.Scheme
	name          string
	eventRecorder record.EventRecorder
	profile       configv1alpha1.SchedulerProfile
	// gangSchedulingEnabled toggles the Workload-Aware Scheduling translation.
	// It requires the upstream scheduling.k8s.io Workload APIs to be served.
	gangSchedulingEnabled bool
}

var _ scheduler.Backend = (*schedulerBackend)(nil)

const workloadAPICheckTimeout = 30 * time.Second

// New creates a new Kube backend instance. profile is the scheduler profile for default-scheduler;
// schedulerBackend uses profile.Name and unmarshals profile.Config into KubeSchedulerConfig.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, profile configv1alpha1.SchedulerProfile) scheduler.Backend {
	return &schedulerBackend{
		client:        cl,
		scheme:        scheme,
		name:          string(profile.Name),
		eventRecorder: eventRecorder,
		profile:       profile,
	}
}

// Name returns the pod-facing scheduler name (default-scheduler), for lookup and logging.
func (b *schedulerBackend) Name() string {
	return b.name
}

// Init initializes the Kube backend. When gang scheduling is enabled in the
// profile config, it registers the upstream scheduling API types into the
// scheme and verifies that the cluster serves the Workload-Aware Scheduling
// APIs; missing capabilities fail startup without fallback. Init must finish
// before the manager starts using the shared scheme.
func (b *schedulerBackend) Init(directClient client.Client) error {
	kubeSchedulerConfig, err := b.parseKubeSchedulerConfig()
	if err != nil {
		return err
	}
	if !kubeSchedulerConfig.GangScheduling {
		return nil
	}

	if err := schedulingv1beta1.AddToScheme(b.scheme); err != nil {
		return fmt.Errorf("failed to register scheduling/v1beta1 scheme: %w", err)
	}
	if err := schedulingv1alpha3.AddToScheme(b.scheme); err != nil {
		return fmt.Errorf("failed to register scheduling/v1alpha3 scheme: %w", err)
	}

	if err := verifyWorkloadAPIsServed(directClient); err != nil {
		return fmt.Errorf("default-scheduler gang scheduling requires the Kubernetes Workload-Aware Scheduling APIs (Kubernetes >= 1.37 with the GenericWorkload feature gate enabled on kube-apiserver, kube-scheduler, and kube-controller-manager, plus the CompositePodGroup and TopologyAwareWorkloadScheduling feature gates enabled on kube-apiserver and kube-scheduler); enable these prerequisites or disable config.gangScheduling on the default-scheduler profile: %w", err)
	}
	b.gangSchedulingEnabled = true
	return nil
}

// parseKubeSchedulerConfig unmarshals the profile config into KubeSchedulerConfig.
func (b *schedulerBackend) parseKubeSchedulerConfig() (*configv1alpha1.KubeSchedulerConfig, error) {
	kubeSchedulerConfig := &configv1alpha1.KubeSchedulerConfig{}
	if b.profile.Config == nil || len(b.profile.Config.Raw) == 0 {
		return kubeSchedulerConfig, nil
	}
	if err := json.Unmarshal(b.profile.Config.Raw, kubeSchedulerConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal default-scheduler profile config: %w", err)
	}
	return kubeSchedulerConfig, nil
}

// verifyWorkloadAPIsServed checks that the Workload-Aware Scheduling API
// resources are accessible through the direct client before the manager starts.
// Serving CompositePodGroup also requires GenericWorkload and
// TopologyAwareWorkloadScheduling on kube-apiserver. Scheduler and
// controller-manager feature gates must be configured by the administrator.
func verifyWorkloadAPIsServed(directClient client.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), workloadAPICheckTimeout)
	defer cancel()
	limit := client.Limit(1)
	if err := directClient.List(ctx, &schedulingv1beta1.WorkloadList{}, limit); err != nil {
		return fmt.Errorf("cannot access %s Workload API: %w", schedulingv1beta1.SchemeGroupVersion.String(), err)
	}
	if err := directClient.List(ctx, &schedulingv1beta1.PodGroupList{}, limit); err != nil {
		return fmt.Errorf("cannot access %s PodGroup API: %w", schedulingv1beta1.SchemeGroupVersion.String(), err)
	}
	if err := directClient.List(ctx, &schedulingv1alpha3.CompositePodGroupList{}, limit); err != nil {
		return fmt.Errorf("cannot access %s CompositePodGroup API: %w", schedulingv1alpha3.SchemeGroupVersion.String(), err)
	}
	return nil
}

// SyncPodGang synchronizes scheduler resources for a PodGang. Without gang
// scheduling it is a no-op. With gang scheduling it translates the PodGang
// into an upstream Workload / CompositePodGroup / PodGroup hierarchy. The
// scheduler owns runtime group status; Grove's PodCliqueSet reconciler derives
// PodGang conditions from member Pods, as with the KAI and Volcano backends.
func (b *schedulerBackend) SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	if !b.gangSchedulingEnabled {
		return nil
	}
	if podGang == nil {
		return fmt.Errorf("podGang is nil")
	}
	b.warnPreferredTopologyConstraints(podGang)
	if err := b.syncWorkloadHierarchy(ctx, podGang); err != nil {
		b.recordWarning(podGang, "KubeBackendSyncFailed", err)
		return err
	}
	return nil
}

// PreparePod prepares the Pod by setting the relevant schedulerName field with the chosen scheduler backend.
// With gang scheduling enabled membership includes the gang epoch and clique.
func (b *schedulerBackend) PreparePod(pod *corev1.Pod) error {
	if !b.gangSchedulingEnabled {
		pod.Spec.SchedulerName = b.name
		return nil
	}
	podCliqueName := pod.Labels[apicommon.LabelPodClique]
	if podCliqueName == "" {
		return fmt.Errorf("default-scheduler gang scheduling requires pod label %q", apicommon.LabelPodClique)
	}
	podGangName := pod.Labels[apicommon.LabelPodGang]
	if podGangName == "" {
		return fmt.Errorf("default-scheduler gang scheduling requires pod label %q", apicommon.LabelPodGang)
	}
	leafName := runtimeLeafName(podGangName, podCliqueName)
	pod.Spec.SchedulerName = b.name
	pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{
		PodGroupName: &leafName,
	}
	return nil
}

// ValidatePodCliqueSet runs default-scheduler-specific validations on the PodCliqueSet.
func (b *schedulerBackend) ValidatePodCliqueSet(_ context.Context, pcs *grovecorev1alpha1.PodCliqueSet, opts ...scheduler.ValidateOption) error {
	oldPCS := scheduler.NewValidateOptions(opts...).OldPCS
	if !b.gangSchedulingEnabled || oldPCS == nil {
		return nil
	}
	// Native group priorities are immutable, including on epochs retained by an update.
	return apivalidation.ValidateImmutableField(pcs.Spec.Template.PriorityClassName, oldPCS.Spec.Template.PriorityClassName,
		field.NewPath("spec", "template", "priorityClassName")).ToAggregate()
}

func (b *schedulerBackend) warnPreferredTopologyConstraints(podGang *groveschedulerv1alpha1.PodGang) {
	constraints := []*groveschedulerv1alpha1.TopologyConstraint{podGang.Spec.TopologyConstraint}
	for _, group := range podGang.Spec.TopologyConstraintGroupConfigs {
		constraints = append(constraints, group.TopologyConstraint)
	}
	for _, group := range podGang.Spec.PodGroups {
		constraints = append(constraints, group.TopologyConstraint)
	}
	for _, constraint := range constraints {
		if constraint != nil && constraint.PackConstraint != nil && constraint.PackConstraint.Preferred != nil {
			b.recordWarning(podGang, "KubeBackendPreferredTopologyIgnored",
				fmt.Errorf("default-scheduler gang scheduling ignores preferred topology constraints; required constraints remain enforced"))
			return
		}
	}
}

// recordWarning emits a warning event on the object when a recorder is configured.
func (b *schedulerBackend) recordWarning(obj runtime.Object, reason string, err error) {
	if b.eventRecorder != nil && obj != nil && err != nil {
		b.eventRecorder.Eventf(obj, corev1.EventTypeWarning, reason, "%v", err)
	}
}
