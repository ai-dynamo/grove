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
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/common"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PodSchedulerName is the value set on Pod.Spec.SchedulerName for the Kubernetes default scheduler.
const PodSchedulerName = "default-scheduler"

// Backend implements the common.SchedBackend interface for Kubernetes default scheduler
// This backend does minimal work - just sets the scheduler name on pods
type Backend struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
	profile       configv1alpha1.SchedulerProfile
}

// New creates a new Kube backend instance. profile is the scheduler profile for default-scheduler;
// Backend uses profile.Name and may unmarshal profile.Config into KubeSchedulerConfig.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, profile configv1alpha1.SchedulerProfile) common.SchedBackend {
	return &Backend{
		client:        cl,
		scheme:        scheme,
		eventRecorder: eventRecorder,
		profile:       profile,
	}
}

// Name returns the backend profile name (kube-scheduler), for lookup and logging.
func (b *Backend) Name() string {
	return string(b.profile.Name)
}

// Init initializes the Kube backend
// For Kube backend, no special initialization is needed
func (b *Backend) Init() error {
	return nil
}

// SyncPodGang synchronizes PodGang resources
// For default kube scheduler, no additional resources are needed
func (b *Backend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	// No-op: default kube scheduler doesn't need any custom resources
	return nil
}

// OnPodGangDelete handles PodGang deletion
// For default kube scheduler, no cleanup is needed
func (b *Backend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	// No-op: default kube scheduler doesn't have any resources to clean up
	return nil
}

// PreparePod adds Kubernetes default scheduler-specific configuration to the Pod.
// Pod.Spec.SchedulerName is set to "default-scheduler" (the value expected by kube-apiserver / kube-scheduler).
func (b *Backend) PreparePod(pod *corev1.Pod) {
	pod.Spec.SchedulerName = PodSchedulerName
}

// ValidatePodCliqueSet runs default-scheduler-specific validations on the PodCliqueSet.
func (b *Backend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}
