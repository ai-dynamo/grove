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
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaitopologyv1alpha1 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/kai/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// schedulerBackend implements the scheduler Backend interface (Backend in scheduler package) for KAI scheduler.
// TODO: Converts PodGang → PodGroup
type schedulerBackend struct {
	client        client.Client
	scheme        *runtime.Scheme
	name          string
	eventRecorder record.EventRecorder
	profile       configv1alpha1.SchedulerProfile
}

var _ scheduler.Backend = (*schedulerBackend)(nil)

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
	return kaitopologyv1alpha1.AddToScheme(b.scheme)
}

// SyncPodGang converts PodGang to KAI PodGroup and synchronizes it
func (b *schedulerBackend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

// PreparePod adds KAI scheduler-specific configuration to the Pod.
// Sets Pod.Spec.SchedulerName so the pod is scheduled by KAI.
func (b *schedulerBackend) PreparePod(pod *corev1.Pod) error {
	pod.Spec.SchedulerName = b.Name()
	return nil
}

// ValidatePodCliqueSet runs KAI-specific validations on the PodCliqueSet.
func (b *schedulerBackend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}
