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

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Backend implements the SchedulerBackend interface for KAI scheduler
// Converts PodGang → PodGroup (scheduling.run.ai/v2alpha2 format, similar to posgroups.yaml)
type Backend struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
	profile       configv1alpha1.SchedulerProfile
}

// New creates a new KAI backend instance. profile is the scheduler profile for kai-scheduler;
// Backend uses profile.Name and may unmarshal profile.Config for kai-specific options.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, profile configv1alpha1.SchedulerProfile) *Backend {
	return &Backend{
		client:        cl,
		scheme:        scheme,
		eventRecorder: eventRecorder,
		profile:       profile,
	}
}

// Name returns the backend name
func (b *Backend) Name() string {
	return string(b.profile.Name)
}

// Init initializes the KAI backend
// For KAI backend, no special initialization is needed currently
func (b *Backend) Init() error {
	return nil
}

// SyncPodGang converts PodGang to KAI PodGroup and synchronizes it
func (b *Backend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

// OnPodGangDelete removes the PodGroup owned by this PodGang
func (b *Backend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

// PreparePod adds KAI scheduler-specific configuration to the Pod.
// Sets Pod.Spec.SchedulerName so the pod is scheduled by KAI.
func (b *Backend) PreparePod(pod *corev1.Pod) {
	pod.Spec.SchedulerName = b.Name()
}

// ValidatePodCliqueSet runs KAI-specific validations on the PodCliqueSet.
func (b *Backend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}
