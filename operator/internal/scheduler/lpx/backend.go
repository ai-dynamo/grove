// Copyright 2026 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lpx

import (
	"context"
	"errors"
	"fmt"
	"slices"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type schedulerBackend struct {
	client          client.Client
	name            string
	fallbackBackend scheduler.Backend
}

var (
	errTopologyConstraintsUnsupported = errors.New(
		"lpx-scheduler does not support Grove topology constraints; " +
			"placement topology must be expressed through the LPX workload contract",
	)

	_ scheduler.Backend = (*schedulerBackend)(nil)

	resourcesLPX = []corev1.ResourceName{"lpu.nvidia.com/lpu", "nvidia.com/lpu"}
)

// New creates an LPX scheduler backend.
func New(cl client.Client, profile configv1alpha1.SchedulerProfile, fallbackBackend scheduler.Backend) scheduler.Backend {
	return &schedulerBackend{
		client:          cl,
		name:            string(profile.Name),
		fallbackBackend: fallbackBackend,
	}
}

// Name returns the pod-facing scheduler name (lpx-scheduler), for lookup and logging.
func (b *schedulerBackend) Name() string {
	return b.name
}

// Init defers to the fallback backend.
func (b *schedulerBackend) Init(directClient client.Client) error {
	if b.fallbackBackend != nil {
		return b.fallbackBackend.Init(directClient)
	}

	return nil
}

// SyncPodGang creates no new resources of LPX pod groups.
// Any non-LPX pod groups are passed to the fallback backend SyncPodGang.
func (b *schedulerBackend) SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	if podGang == nil {
		return fmt.Errorf("podGang is nil")
	}

	if b.fallbackBackend == nil {
		return nil
	}

	newPodGang, err := b.fallbackPodGang(ctx, podGang)
	if err != nil {
		return err
	}

	return b.fallbackBackend.SyncPodGang(ctx, newPodGang)
}

// PreparePod adds LPX-scheduler-specific or fallback scheduler backend configuration to the Pod,
// depending on if the pod requests LPX resources.
func (b *schedulerBackend) PreparePod(pod *corev1.Pod) error {
	if usesLPX(pod.Spec) {
		pod.Spec.SchedulerName = b.name
		return nil
	}

	if b.fallbackBackend != nil {
		return b.fallbackBackend.PreparePod(pod)
	}

	return nil
}

// ValidatePodCliqueSet runs LPX-specific validations on the PodCliqueSet as well as any fallback backend validations, if necessary.
func (b *schedulerBackend) ValidatePodCliqueSet(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) error {
	if pcs.Spec.Template.TopologyConstraint != nil {
		return errTopologyConstraintsUnsupported
	}

	for _, clique := range pcs.Spec.Template.Cliques {
		if clique.TopologyConstraint != nil {
			return errTopologyConstraintsUnsupported
		}
	}

	for _, scalingGroup := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if scalingGroup.TopologyConstraint != nil {
			return errTopologyConstraintsUnsupported
		}
	}

	if b.fallbackBackend != nil {
		if slices.ContainsFunc(pcs.Spec.Template.Cliques, func(clique *grovecorev1alpha1.PodCliqueTemplateSpec) bool { return !usesLPX(clique.Spec.PodSpec) }) {
			return b.fallbackBackend.ValidatePodCliqueSet(ctx, pcs)
		}
	}

	return nil
}

// fallbackPodGang constructs a new PodGang object without any LPX pod groups.
// It is then passed to the SyncPodGang function of the fallback backend.
func (b *schedulerBackend) fallbackPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) (*groveschedulerv1alpha1.PodGang, error) {
	fallback := podGang.DeepCopy()
	fallback.Spec.PodGroups = make([]groveschedulerv1alpha1.PodGroup, 0, len(podGang.Spec.PodGroups))

	for _, group := range podGang.Spec.PodGroups {
		var pclq grovecorev1alpha1.PodClique

		if err := b.client.Get(ctx, client.ObjectKey{Namespace: podGang.Namespace, Name: group.Name}, &pclq); err != nil {
			return nil, fmt.Errorf("get PodClique %s for PodGang %s/%s: %w", group.Name, podGang.Namespace, podGang.Name, err)
		}

		if !usesLPX(pclq.Spec.PodSpec) {
			fallback.Spec.PodGroups = append(fallback.Spec.PodGroups, group)
		}
	}

	return fallback, nil
}

// usesLPX determines whether a given pod spec is requesting LPX support by
// checking the resource requests and limits for lpu.nvidia.com/lpu or nvidia.com/lpu.
func usesLPX(podSpec corev1.PodSpec) bool {
	return slices.ContainsFunc(podSpec.Containers, func(container corev1.Container) bool {
		return slices.ContainsFunc(resourcesLPX, func(r corev1.ResourceName) bool {
			_, requests := container.Resources.Requests[r]
			_, limits := container.Resources.Limits[r]
			return requests || limits
		})
	})
}
