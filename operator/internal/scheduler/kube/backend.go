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

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// schedulerBackend implements the scheduler backend interface (Backend in scheduler package) for Kubernetes default scheduler.
// This backend does minimal work - just sets the scheduler name on pods
type schedulerBackend struct {
	client        client.Client
	scheme        *runtime.Scheme
	name          string
	eventRecorder record.EventRecorder
	profile       configv1alpha1.SchedulerProfile
}

var _ scheduler.Backend = (*schedulerBackend)(nil)

// New creates a new Kube backend instance. profile is the scheduler profile for default-scheduler;
// schedulerBackend uses profile.Name and may unmarshal profile.Config into KubeSchedulerConfig.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, profile configv1alpha1.SchedulerProfile) scheduler.Backend {
	return &schedulerBackend{
		client:        cl,
		scheme:        scheme,
		name:          string(configv1alpha1.SchedulerNameKube),
		eventRecorder: eventRecorder,
		profile:       profile,
	}
}

// Name returns the pod-facing scheduler name (default-scheduler), for lookup and logging.
func (b *schedulerBackend) Name() string {
	return b.name
}

// Init initializes the Kube backend
// For Kube backend, no special initialization is needed
func (b *schedulerBackend) Init() error {
	return nil
}

// SyncPodGang synchronizes PodGang resources
// For default kube scheduler, no additional resources are needed
func (b *schedulerBackend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	// No-op: default kube scheduler doesn't need any custom resources
	return nil
}

// OnPodGangDelete handles PodGang deletion
// For default kube scheduler, no cleanup is needed
func (b *schedulerBackend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	// No-op: default kube scheduler doesn't have any resources to clean up
	return nil
}

// PreparePod prepares the Pod by setting the relevant schedulerName field with the chosen scheduler backend.
func (b *schedulerBackend) PreparePod(pod *corev1.Pod) {
	pod.Spec.SchedulerName = b.name
}

// ValidatePodCliqueSet runs default-scheduler-specific validations on the PodCliqueSet.
func (b *schedulerBackend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}
