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

package scheduler

import (
	"context"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Backend defines the interface that different scheduler backends must implement.
// It is defined in this package (consumer side) so that kube and kai subpackages
// need not import scheduler, avoiding circular dependencies (see "accept interfaces,
// return structs" and consumer-defined interfaces in Go / Kubernetes).
//
// Architecture: Backend validates PodCliqueSet at admission, converts PodGang to scheduler-specific
// CR (PodGroup/Workload/etc), and prepares Pods with scheduler-specific configurations.
type Backend interface {
	// Name is a unique name of the scheduler backend.
	Name() string

	// Init provides a hook to initialize/setup one-time scheduler resources,
	// called at the startup of grove operator.
	Init() error

	// SyncPodGang synchronizes (creates/updates) scheduler-specific resources for a PodGang
	// reacting to a creation or update of a PodGang resource.
	SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error

	// OnPodGangDelete cleans up scheduler-specific resources for the given PodGang.
	OnPodGangDelete(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error

	// PreparePod adds scheduler-backend-specific configuration to the given Pod object
	// prior to its creation (schedulerName, annotations, etc.).
	PreparePod(pod *corev1.Pod)

	// ValidatePodCliqueSet runs scheduler-specific validations on the PodCliqueSet (e.g. TAS required but not supported).
	ValidatePodCliqueSet(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) error
}

// TopologyAwareSchedBackend is an optional interface that Backend
// implementations may satisfy if they manage a scheduler-specific topology CRD.
// The ClusterTopology controller type-asserts each registered backend to this
// interface at startup and calls these methods during reconciliation.
type TopologyAwareSchedBackend interface {
	// TopologyGVR returns the GroupVersionResource of the topology CRD
	// managed by this backend (e.g. KAI's "topologies.kai.scheduler").
	// The CT controller uses this to register dynamic watches at startup.
	TopologyGVR() schema.GroupVersionResource

	// TopologyResourceName returns the name of the backend-specific topology resource
	// that corresponds to the given ClusterTopology. Called for auto-managed backends
	// to populate SchedulerTopologyStatus.TopologyReference.
	TopologyResourceName(ct *grovecorev1alpha1.ClusterTopology) string

	// SyncTopology creates or updates the scheduler-specific topology resource
	// for the given ClusterTopology. Called for backends not listed in
	// the ClusterTopology's schedulerTopologyReferences (auto-managed path).
	// k8sClient may be a non-cached client for use before the manager cache
	// is started. If nil, the backend falls back to its own client.
	SyncTopology(ctx context.Context, k8sClient client.Client, ct *grovecorev1alpha1.ClusterTopology) error

	// OnTopologyDelete removes the scheduler-specific topology resource for
	// the given ClusterTopology. Called on CT deletion (auto-managed path only).
	// k8sClient may be nil; if so, the backend falls back to its own client.
	OnTopologyDelete(ctx context.Context, k8sClient client.Client, ct *grovecorev1alpha1.ClusterTopology) error

	// CheckTopologyDrift compares the scheduler-specific topology resource named by
	// ref.TopologyReference against the ClusterTopology's levels.
	// Returns (inSync bool, message string, observedGeneration int64, error).
	// Called for backends listed in schedulerTopologyReferences (externally-managed path).
	CheckTopologyDrift(
		ctx context.Context,
		ct *grovecorev1alpha1.ClusterTopology,
		ref grovecorev1alpha1.SchedulerTopologyReference,
	) (bool, string, int64, error)
}
