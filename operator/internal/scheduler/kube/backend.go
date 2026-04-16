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

package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha1 "k8s.io/api/scheduling/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// schedulerBackend implements the scheduler backend interface (Backend in scheduler package) for Kubernetes default scheduler.
type schedulerBackend struct {
	client        client.Client
	scheme        *runtime.Scheme
	name          string
	eventRecorder record.EventRecorder
	profile       configv1alpha1.SchedulerProfile
	config        configv1alpha1.KubeSchedulerConfig
}

var _ scheduler.Backend = (*schedulerBackend)(nil)

// New creates a new Kube backend instance. profile is the scheduler profile for default-scheduler;
// schedulerBackend uses profile.Name and may unmarshal profile.Config into KubeSchedulerConfig.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, profile configv1alpha1.SchedulerProfile) scheduler.Backend {
	var cfg configv1alpha1.KubeSchedulerConfig
	if profile.Config != nil {
		// Best-effort unmarshal; invalid config fields are silently ignored.
		_ = json.Unmarshal(profile.Config.Raw, &cfg)
	}
	return &schedulerBackend{
		client:        cl,
		scheme:        scheme,
		name:          string(configv1alpha1.SchedulerNameKube),
		eventRecorder: eventRecorder,
		profile:       profile,
		config:        cfg,
	}
}

// Name returns the pod-facing scheduler name (default-scheduler), for lookup and logging.
func (b *schedulerBackend) Name() string {
	return b.name
}

// Init initializes the Kube backend.
// For Kube backend, no special initialization is needed.
func (b *schedulerBackend) Init() error {
	return nil
}

// SyncPodGang creates or reconciles a scheduling.k8s.io/v1alpha1 Workload resource for the
// given PodGang when GangScheduling is enabled. Each PodGroup in the PodGang is mapped to a
// PodGroup in the Workload with GangSchedulingPolicy.MinCount set to PodGroup.MinReplicas.
// The Workload is owned by the PodGang and is garbage-collected on PodGang deletion.
//
// When GangScheduling is disabled this is a no-op.
func (b *schedulerBackend) SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	if !b.config.GangScheduling {
		return nil
	}
	logger := log.FromContext(ctx)

	desired, err := b.buildWorkload(podGang)
	if err != nil {
		return fmt.Errorf("failed to build Workload for PodGang %s/%s: %w", podGang.Namespace, podGang.Name, err)
	}

	existing := &schedulingv1alpha1.Workload{}
	err = b.client.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get Workload %s/%s: %w", desired.Namespace, desired.Name, err)
		}
		// Workload does not exist yet — create it.
		if err = b.client.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create Workload %s/%s: %w", desired.Namespace, desired.Name, err)
		}
		logger.Info("Created Workload for PodGang", "workload", client.ObjectKeyFromObject(desired))
		return nil
	}

	// Workload exists. Since Workload.Spec.PodGroups is immutable, we must delete and recreate
	// if the desired PodGroups differ from the existing ones (e.g. MinReplicas changed).
	if !reflect.DeepEqual(existing.Spec.PodGroups, desired.Spec.PodGroups) {
		if err = b.client.Delete(ctx, existing); err != nil {
			return fmt.Errorf("failed to delete stale Workload %s/%s for recreation: %w", existing.Namespace, existing.Name, err)
		}
		if err = b.client.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to recreate Workload %s/%s: %w", desired.Namespace, desired.Name, err)
		}
		logger.Info("Recreated Workload for PodGang (PodGroups changed)", "workload", client.ObjectKeyFromObject(desired))
	}
	return nil
}

// OnPodGangDelete is a no-op: the owner reference set on the Workload by SyncPodGang ensures
// the Workload is garbage-collected automatically when the PodGang is deleted.
func (b *schedulerBackend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

// PreparePod sets the scheduler name and, when GangScheduling is enabled, sets the WorkloadRef
// on the Pod so the kube-scheduler can apply workload-aware gang scheduling semantics.
// The WorkloadRef.Name is the PodGang name (from pod label grove.io/podgang) and
// WorkloadRef.PodGroup is the PodClique name (from pod label grove.io/podclique), which
// matches the PodGroup name inside the Workload created by SyncPodGang.
func (b *schedulerBackend) PreparePod(pod *corev1.Pod) {
	pod.Spec.SchedulerName = b.name
	if !b.config.GangScheduling {
		return
	}
	podGangName := pod.Labels[apicommon.LabelPodGang]
	podCliqueName := pod.Labels[apicommon.LabelPodClique]
	if podGangName == "" || podCliqueName == "" {
		return
	}
	pod.Spec.WorkloadRef = &corev1.WorkloadReference{
		Name:     podGangName,
		PodGroup: podCliqueName,
	}
}

// ValidatePodCliqueSet runs default-scheduler-specific validations on the PodCliqueSet.
func (b *schedulerBackend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}

// buildWorkload constructs the desired scheduling.k8s.io/v1alpha1 Workload for the given PodGang.
// The Workload name matches the PodGang name and lives in the same namespace.
func (b *schedulerBackend) buildWorkload(podGang *groveschedulerv1alpha1.PodGang) (*schedulingv1alpha1.Workload, error) {
	podGroups := lo.Map(podGang.Spec.PodGroups, func(pg groveschedulerv1alpha1.PodGroup, _ int) schedulingv1alpha1.PodGroup {
		return schedulingv1alpha1.PodGroup{
			Name: pg.Name,
			Policy: schedulingv1alpha1.PodGroupPolicy{
				Gang: &schedulingv1alpha1.GangSchedulingPolicy{
					MinCount: pg.MinReplicas,
				},
			},
		}
	})

	workload := &schedulingv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podGang.Name,
			Namespace: podGang.Namespace,
		},
		Spec: schedulingv1alpha1.WorkloadSpec{
			PodGroups: podGroups,
		},
	}

	if err := controllerutil.SetOwnerReference(podGang, workload, b.scheme); err != nil {
		return nil, fmt.Errorf("failed to set owner reference on Workload: %w", err)
	}
	return workload, nil
}
