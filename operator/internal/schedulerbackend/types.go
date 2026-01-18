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

package schedulerbackend

import (
	"context"
	"fmt"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// SchedulerBackend defines the interface that different scheduler backends must implement.
//
// Architecture: Backend converts PodGang to scheduler-specific CR (PodGroup/Workload/etc)
// and prepares Pods with scheduler-specific configurations.
type SchedulerBackend interface {
	// Name is a unique name of the scheduler backend.
	Name() string

	// Init provides a hook to initialize/setup one-time scheduler resources,
	// called at the startup of grove operator.
	Init() error

	// SyncPodGang synchronizes (creates/updates) scheduler specific resources for a PodGang
	// reacting to a creation or update of a PodGang resource.
	SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error

	// OnPodGangDelete cleans up scheduler specific resources for the given PodGang.
	OnPodGangDelete(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error

	// PreparePod adds scheduler backend specific configuration to the given Pod object
	// prior to its creation. This includes setting schedulerName, scheduling gates,
	// annotations, etc.
	PreparePod(pod *corev1.Pod)
}

// PreparePod adds scheduler backend specific configuration to the given Pod object
// prior to its creation. This includes setting schedulerName, scheduling gates,
// annotations, etc.
func PreparePod(pod *corev1.Pod) error {
	// Get backend instance
	backend := Get()
	if backend == nil {
		return fmt.Errorf("backend not initialized")
	}

	// Let the backend prepare the pod
	backend.PreparePod(pod)
	return nil
}
