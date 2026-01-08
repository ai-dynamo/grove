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

	"github.com/ai-dynamo/grove/operator/api/common"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// BackendName is the name of the KAI backend
	BackendName = "kai"
	// SchedulingGateName is the name of the scheduling gate used by KAI
	SchedulingGateName = "grove.io/podgang-pending-creation"
	// BackendLabelValue is the label value to identify KAI backend
	BackendLabelValue = "kai"
)

// PodGroup API constants (run.ai format)
const (
	PodGroupAPIGroup   = "scheduling.run.ai"
	PodGroupAPIVersion = "v2alpha2"
	PodGroupKind       = "PodGroup"
)

// Backend implements the SchedulerBackend interface for KAI scheduler
// Converts PodGang → PodGroup (scheduling.run.ai/v2alpha2 format, similar to posgroups.yaml)
type Backend struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
	schedulerName string // The scheduler name from configuration
}

// New creates a new KAI backend instance
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, schedulerName string) *Backend {
	return &Backend{
		client:        cl,
		scheme:        scheme,
		eventRecorder: eventRecorder,
		schedulerName: schedulerName,
	}
}

// Name returns the backend name
func (b *Backend) Name() string {
	return BackendName
}

// Init initializes the KAI backend
// For KAI backend, no special initialization is needed currently
func (b *Backend) Init() error {
	return nil
}

// SyncPodGang converts PodGang to KAI PodGroup and synchronizes it
// TODO: Currently disabled - will be implemented in phase 2
func (b *Backend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	// Phase 1: Skip PodGroup creation/update
	// Phase 2: Will convert PodGang to PodGroup and synchronize
	return nil
}

// OnPodGangDelete removes the PodGroup owned by this PodGang
// TODO: Currently disabled - will be implemented in phase 2
func (b *Backend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	// Phase 1: Skip PodGroup deletion
	// Phase 2: Will delete PodGroup when PodGang is deleted
	return nil
}

// PreparePod adds KAI scheduler-specific configuration to the Pod
// This includes: schedulerName, and annotations
func (b *Backend) PreparePod(pod *corev1.Pod) {
	// Set scheduler name from configuration
	pod.Spec.SchedulerName = b.schedulerName

	// Add annotations for observability
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	// Get PodGang and PodGroup names from labels
	if podGangName, ok := pod.Labels[common.LabelPodGang]; ok {
		pod.Annotations["kai.scheduler/podgang"] = podGangName
	}
	if podCliqueName, ok := pod.Labels[common.LabelPodClique]; ok {
		pod.Annotations["kai.scheduler/podgroup"] = podCliqueName
	}
}
