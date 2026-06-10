// /*
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
// */

package lpx

import (
	"context"
	"errors"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	errTopologyConstraintsUnsupported = errors.New(
		"lpx-scheduler does not support Grove topology constraints; " +
			"placement topology must be expressed through the LPX workload contract",
	)

	_ scheduler.Backend = (*schedulerBackend)(nil)
)

type schedulerBackend struct {
	name string
}

// New creates an LPX scheduler backend.
func New(profile configv1alpha1.SchedulerProfile) scheduler.Backend {
	return &schedulerBackend{name: string(profile.Name)}
}

func (b *schedulerBackend) Name() string {
	return b.name
}

func (b *schedulerBackend) Init(_ client.Client) error {
	return nil
}

func (b *schedulerBackend) SyncPodGang(
	_ context.Context,
	_ *groveschedulerv1alpha1.PodGang,
) error {
	return nil
}

func (b *schedulerBackend) PreparePod(pod *corev1.Pod) error {
	pod.Spec.SchedulerName = b.name
	return nil
}

func (b *schedulerBackend) ValidatePodCliqueSet(
	_ context.Context,
	pcs *grovecorev1alpha1.PodCliqueSet,
) error {
	if pcs.Spec.Template.TopologyConstraint != nil {
		return errTopologyConstraintsUnsupported
	}
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique != nil && clique.TopologyConstraint != nil {
			return errTopologyConstraintsUnsupported
		}
	}
	for _, scalingGroup := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if scalingGroup.TopologyConstraint != nil {
			return errTopologyConstraintsUnsupported
		}
	}
	return nil
}
