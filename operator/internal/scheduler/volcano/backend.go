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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

const volcanoPodGroupCRDName = "podgroups.scheduling.volcano.sh"

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

// Init registers the Volcano API types into b.scheme and must be called before
// that scheme is used to serialize or deserialize Volcano objects.
func (b *schedulerBackend) Init(directClient client.Client) error {
	if err := volcanov1beta1.AddToScheme(b.scheme); err != nil {
		return fmt.Errorf("failed to register volcano scheme: %w", err)
	}
	if err := apiextensionsv1.AddToScheme(b.scheme); err != nil {
		return fmt.Errorf("failed to register apiextensions scheme: %w", err)
	}

	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: volcanoPodGroupCRDName},
	}
	if err := directClient.Get(context.Background(), client.ObjectKey{Name: volcanoPodGroupCRDName}, crd); err != nil {
		err = fmt.Errorf("volcano scheduler backend requires Volcano 1.14 or newer with PodGroup.spec.subGroupPolicy: failed to get CRD %q: %w", volcanoPodGroupCRDName, err)
		recordCapabilityEvent(b.eventRecorder, crd, err)
		return err
	}
	if !podGroupCRDHasSubGroupPolicy(crd) {
		err := fmt.Errorf("volcano scheduler backend requires Volcano 1.14 or newer with PodGroup.spec.subGroupPolicy on scheduling.volcano.sh/v1beta1 PodGroup")
		recordCapabilityEvent(b.eventRecorder, crd, err)
		return err
	}
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

		if shouldSyncSchedulingConstraints(podGang, podGroup) {
			podGroup.Spec.MinMember = minMemberForPodGang(podGang)
			podGroup.Spec.SubGroupPolicy = subGroupPoliciesForPodGang(podGang)
		}
		podGroup.Spec.Queue = effectiveQueueFromAnnotations(podGang.Annotations)
		podGroup.Spec.PriorityClassName = podGang.Spec.PriorityClassName
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to sync Volcano PodGroup for PodGang %s/%s: %w", podGang.Namespace, podGang.Name, err)
	}

	return nil
}

func recordCapabilityEvent(eventRecorder record.EventRecorder, obj runtime.Object, err error) {
	if obj == nil {
		return
	}
	eventRecorder.Eventf(obj, corev1.EventTypeWarning, "VolcanoCapabilityUnavailable", "%v", err)
}

func (b *schedulerBackend) PreparePod(pod *corev1.Pod, _ scheduler.PreparePodContext) error {
	pod.Spec.SchedulerName = b.Name()
	podGangName := pod.Labels[apicommon.LabelPodGang]
	if podGangName == "" {
		return fmt.Errorf("volcano scheduler requires pod label %q", apicommon.LabelPodGang)
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[volcanov1beta1.VolcanoGroupNameAnnotationKey] = podGangName
	pod.Annotations[volcanov1beta1.KubeGroupNameAnnotationKey] = podGangName
	return nil
}

func (b *schedulerBackend) ValidatePodCliqueSet(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) error {
	if err := validateTopologyConstraints(pcs); err != nil {
		return err
	}
	return validateQueueAnnotations(ctx, b.client, pcs)
}

func validateTopologyConstraints(pcs *grovecorev1alpha1.PodCliqueSet) error {
	if pcs.Spec.Template.TopologyConstraint != nil {
		return fmt.Errorf("volcano scheduler backend does not support topologyConstraint on PodCliqueSet")
	}
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique.TopologyConstraint != nil {
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

func shouldSyncSchedulingConstraints(podGang *groveschedulerv1alpha1.PodGang, podGroup *volcanov1beta1.PodGroup) bool {
	for _, group := range podGang.Spec.PodGroups {
		if group.MinReplicas > 0 {
			return true
		}
	}
	// Coherent updates release Grove scheduling constraints by setting all
	// MinReplicas to 0. Keep existing Volcano gang constraints in that phase.
	return podGroup.Spec.MinMember == 0 && len(podGroup.Spec.SubGroupPolicy) == 0
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

func subGroupPoliciesForPodGang(podGang *groveschedulerv1alpha1.PodGang) []volcanov1beta1.SubGroupPolicySpec {
	subGroups := make([]volcanov1beta1.SubGroupPolicySpec, 0, len(podGang.Spec.PodGroups))
	for _, group := range podGang.Spec.PodGroups {
		size := group.MinReplicas
		if size == 0 {
			size = 1
		}
		minSubGroups := int32(1)
		subGroups = append(subGroups, volcanov1beta1.SubGroupPolicySpec{
			Name: group.Name,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					apicommon.LabelPodClique: group.Name,
				},
			},
			MatchLabelKeys: []string{apicommon.LabelPodClique},
			SubGroupSize:   &size,
			// Each Grove PodGroup in a PodGang maps to one Volcano subgroup
			// policy, so at least one matching subgroup must satisfy subGroupSize.
			MinSubGroups: &minSubGroups,
		})
	}
	return subGroups
}

func podGroupCRDHasSubGroupPolicy(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, version := range crd.Spec.Versions {
		if version.Name != volcanov1beta1.SchemeGroupVersion.Version || !version.Served || version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
			continue
		}
		specSchema, ok := version.Schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			return false
		}
		_, ok = specSchema.Properties["subGroupPolicy"]
		return ok
	}
	return false
}
