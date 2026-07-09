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

package pod

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newKueuePOCResource(cl client.Client) _resource {
	return _resource{
		client: cl,
		scheme: cl.Scheme(),
		schedRegistry: &testutils.FakeSchedulerRegistry{
			Backends: map[string]scheduler.Backend{
				string(configv1alpha1.SchedulerNameKueue): testutils.NewFakeSchedulerBackend(string(configv1alpha1.SchedulerNameKueue)),
			},
			DefaultBackend: string(configv1alpha1.SchedulerNameKueue),
		},
	}
}

func TestBuildResource_StampsPrebuiltWorkloadMetadataForSimplePCS(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	r := newKueuePOCResource(cl)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{"kueue.x-k8s.io/queue-name": "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "prefill", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3}},
				},
			},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0-worker",
			Namespace: "default",
			Labels: map[string]string{
				apicommon.LabelPartOfKey:                "demo",
				apicommon.LabelPodCliqueSetReplicaIndex: "0",
				apicommon.LabelPodGang:                  "demo-0",
				"kueue.x-k8s.io/queue-name":             "grove-poc",
			},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas: 3,
			PodSpec:  corev1.PodSpec{SchedulerName: string(configv1alpha1.SchedulerNameKueue)},
		},
	}
	pod := &corev1.Pod{}

	require.NoError(t, r.buildResource(pcs, pclq, "demo-0", pod, 0))

	assert.Equal(t, "5", pod.Annotations[kueuePodGroupTotalCountAnnotation])
	assert.Equal(t, "demo-0", pod.Labels[kueuePodGroupNameLabel])
	assert.Equal(t, "demo-0", pod.Labels[kueuePrebuiltWorkloadNameLabel])
	assert.Equal(t, "grove-poc", pod.Labels["kueue.x-k8s.io/queue-name"])
	assert.Equal(t, "demo-0-worker", pod.Annotations[kueueRoleHashAnnotation])
}

func TestBuildResource_StampsPrebuiltWorkloadMetadataForPCSG(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	r := newKueuePOCResource(cl)
	pcsgReplicas := int32(2)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{"kueue.x-k8s.io/queue-name": "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "decode", CliqueNames: []string{"leader", "worker"}, Replicas: &pcsgReplicas},
				},
			},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0-decode-0-worker",
			Namespace: "default",
			Labels: map[string]string{
				apicommon.LabelPartOfKey:                         "demo",
				apicommon.LabelPodCliqueSetReplicaIndex:          "0",
				apicommon.LabelPodGang:                           "demo-0",
				apicommon.LabelPodCliqueScalingGroup:             "demo-0-decode",
				apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
				"kueue.x-k8s.io/queue-name":                      "grove-poc",
			},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas: 2,
			PodSpec:  corev1.PodSpec{SchedulerName: string(configv1alpha1.SchedulerNameKueue)},
		},
	}
	pod := &corev1.Pod{}

	require.NoError(t, r.buildResource(pcs, pclq, "demo-0", pod, 0))

	// (leader 1 + worker 2) * pcsg replicas 2 = 6 pods in the PodGang.
	assert.Equal(t, "6", pod.Annotations[kueuePodGroupTotalCountAnnotation])
	assert.Equal(t, "demo-0", pod.Labels[kueuePodGroupNameLabel])
	// Grove pre-builds the Workload for PCSG too; Pods reference it and the role-hash is the PodClique FQN.
	assert.Equal(t, "demo-0", pod.Labels[kueuePrebuiltWorkloadNameLabel])
	assert.Equal(t, "grove-poc", pod.Labels["kueue.x-k8s.io/queue-name"])
	assert.Equal(t, "demo-0-decode-0-worker", pod.Annotations[kueueRoleHashAnnotation])
}
