// /*
// Copyright 2026 The Grove Authors.
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

package volcano

import (
	"context"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

type schedulerBackend struct {
	client        client.Client
	scheme        *runtime.Scheme
	name          string
	eventRecorder record.EventRecorder
	profile       configv1alpha1.SchedulerProfile
}

var _ scheduler.Backend = (*schedulerBackend)(nil)

// New creates a Volcano scheduler backend from the given scheduler profile.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, profile configv1alpha1.SchedulerProfile) scheduler.Backend {
	return &schedulerBackend{
		client:        cl,
		scheme:        scheme,
		name:          string(configv1alpha1.SchedulerNameVolcano),
		eventRecorder: eventRecorder,
		profile:       profile,
	}
}

func (b *schedulerBackend) Name() string {
	return b.name
}

func (b *schedulerBackend) Init() error {
	return nil
}

func (b *schedulerBackend) SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	podGroup := &volcanov1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podGang.Name,
			Namespace: podGang.Namespace,
		},
	}

	_, err := controllerutil.CreateOrPatch(ctx, b.client, podGroup, func() error {
		if podGroup.Labels == nil {
			podGroup.Labels = map[string]string{}
		}
		for key, value := range podGang.Labels {
			podGroup.Labels[key] = value
		}

		if err := controllerutil.SetControllerReference(podGang, podGroup, b.scheme); err != nil {
			return err
		}

		podGroup.Spec.MinMember = minMemberForPodGang(podGang)
		podGroup.Spec.Queue = EffectiveQueueFromAnnotations(podGang.Annotations)
		podGroup.Spec.PriorityClassName = podGang.Spec.PriorityClassName
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to sync Volcano PodGroup for PodGang %s/%s: %w", podGang.Namespace, podGang.Name, err)
	}

	return nil
}

func (b *schedulerBackend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	return nil
}

func (b *schedulerBackend) PreparePod(pod *corev1.Pod) {
	pod.Spec.SchedulerName = b.Name()
	podGangName := pod.Labels[apicommon.LabelPodGang]
	if podGangName == "" {
		return
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[volcanov1beta1.VolcanoGroupNameAnnotationKey] = podGangName
	pod.Annotations[volcanov1beta1.KubeGroupNameAnnotationKey] = podGangName
}

func (b *schedulerBackend) ValidatePodCliqueSet(_ context.Context, pcs *grovecorev1alpha1.PodCliqueSet) error {
	if pcs.Spec.Template.TopologyConstraint != nil {
		return fmt.Errorf("volcano scheduler backend does not support topologyConstraint on PodCliqueSet")
	}
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique != nil && clique.TopologyConstraint != nil {
			return fmt.Errorf("volcano scheduler backend does not support topologyConstraint on PodClique %q", clique.Name)
		}
	}
	for _, pcsg := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsg.TopologyConstraint != nil {
			return fmt.Errorf("volcano scheduler backend does not support topologyConstraint on PodCliqueScalingGroup %q", pcsg.Name)
		}
	}
	return nil
}

func minMemberForPodGang(podGang *groveschedulerv1alpha1.PodGang) int32 {
	var total int32
	for _, group := range podGang.Spec.PodGroups {
		total += group.MinReplicas
	}
	if total == 0 {
		return 1
	}
	return total
}
