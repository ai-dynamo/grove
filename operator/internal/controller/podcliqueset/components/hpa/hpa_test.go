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

package hpa

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var (
	pcsUID = uuid.NewUUID()
)

func TestComputeExpectedHPAs(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected []hpaInfo
	}{
		{
			name: "PodClique HPA",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).
				WithReplicas(1).
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("test-clique").
						WithReplicas(3).
						WithMinAvailable(1).
						WithScaleConfig(ptr.To(int32(2)), 5).
						Build(),
				).
				Build(),
			expected: []hpaInfo{
				{
					targetScaleResourceKind: constants.KindPodClique,
					targetScaleResourceName: "test-pcs-0-test-clique",
					scaleConfig: grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: ptr.To(int32(2)),
						MaxReplicas: 5,
					},
				},
			},
		},
		{
			name: "PodCliqueScalingGroup HPA",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).
				WithReplicas(1).
				WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{
					Name:         "test-sg",
					Replicas:     ptr.To(int32(4)),
					MinAvailable: ptr.To(int32(3)),
					CliqueNames:  []string{"test-clique"},
					ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: ptr.To(int32(2)),
						MaxReplicas: 6,
					},
				}).
				Build(),
			expected: []hpaInfo{
				{
					targetScaleResourceKind: constants.KindPodCliqueScalingGroup,
					targetScaleResourceName: "test-pcs-0-test-sg",
					scaleConfig: grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: ptr.To(int32(2)),
						MaxReplicas: 6,
					},
				},
			},
		},
		{
			name: "Both PodClique and PodCliqueScalingGroup HPAs",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).
				WithReplicas(1).
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("individual-clique").
						WithReplicas(2).
						WithMinAvailable(1).
						WithScaleConfig(ptr.To(int32(2)), 4).
						Build(),
				).
				WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{
					Name:         "scaling-group",
					Replicas:     ptr.To(int32(3)),
					MinAvailable: ptr.To(int32(2)),
					CliqueNames:  []string{"sg-clique"},
					ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: ptr.To(int32(1)),
						MaxReplicas: 5,
					},
				}).
				Build(),
			expected: []hpaInfo{
				{
					targetScaleResourceKind: constants.KindPodClique,
					targetScaleResourceName: "test-pcs-0-individual-clique",
					scaleConfig: grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: ptr.To(int32(2)),
						MaxReplicas: 4,
					},
				},
				{
					targetScaleResourceKind: constants.KindPodCliqueScalingGroup,
					targetScaleResourceName: "test-pcs-0-scaling-group",
					scaleConfig: grovecorev1alpha1.AutoScalingConfig{
						MinReplicas: ptr.To(int32(1)),
						MaxReplicas: 5,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &_resource{}
			result := r.computeExpectedHPAs(tt.pcs, []int{0})

			assert.Equal(t, len(tt.expected), len(result), "Number of HPAs should match")

			for i, expected := range tt.expected {
				assert.Equal(t, expected.targetScaleResourceKind, result[i].targetScaleResourceKind)
				assert.Equal(t, expected.targetScaleResourceName, result[i].targetScaleResourceName)
				assert.Equal(t, expected.scaleConfig, result[i].scaleConfig,
					"scaleConfig should be correctly set for %s", expected.targetScaleResourceName)
			}
		})
	}
}

func TestComputeExpectedHPAsUsesSparseReplicaIndices(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).
		WithReplicas(3).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("worker").
				WithScaleConfig(ptr.To(int32(1)), 3).
				Build(),
		).
		Build()
	replicaIndices := []int{0, 2, 4}

	got := (&_resource{}).computeExpectedHPAs(pcs, replicaIndices)

	assert.Equal(t, []hpaInfo{
		{replicaIndex: 0, objectKey: client.ObjectKey{Namespace: "default", Name: "test-pcs-0-worker"}, targetScaleResourceKind: constants.KindPodClique, targetScaleResourceName: "test-pcs-0-worker", scaleConfig: *pcs.Spec.Template.Cliques[0].Spec.ScaleConfig},
		{replicaIndex: 2, objectKey: client.ObjectKey{Namespace: "default", Name: "test-pcs-2-worker"}, targetScaleResourceKind: constants.KindPodClique, targetScaleResourceName: "test-pcs-2-worker", scaleConfig: *pcs.Spec.Template.Cliques[0].Spec.ScaleConfig},
		{replicaIndex: 4, objectKey: client.ObjectKey{Namespace: "default", Name: "test-pcs-4-worker"}, targetScaleResourceKind: constants.KindPodClique, targetScaleResourceName: "test-pcs-4-worker", scaleConfig: *pcs.Spec.Template.Cliques[0].Spec.ScaleConfig},
	}, got)
	assert.Equal(t, []int{0, 2, 4}, replicaIndices)
}

func TestGetScaleInPCSReplicaIndicesAwaitingHPACleanup(t *testing.T) {
	existingHPAs := []autoscalingv2.HorizontalPodAutoscaler{
		{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "1"}}},
		{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "4"}}},
	}

	got, err := (&_resource{}).getScaleInPCSReplicaIndicesAwaitingHPACleanup(context.Background(), existingHPAs, []int{1, 2, 4})

	require.NoError(t, err)
	assert.Equal(t, []int{1, 4}, got)
}

func TestPCSReplicaIndexForHPAUsesLabel(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "3"}}}

	got, found, err := (&_resource{}).pcsReplicaIndexForHPA(context.Background(), hpa)

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 3, got)
}

func TestPCSReplicaIndexFromScaleTarget(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	pclq := testutils.NewPodCliqueBuilder("test-pcs", pcsUID, "worker", "default", 5).Build()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pclq).Build()
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: constants.KindPodClique, Name: pclq.Name},
		},
	}

	got, found, err := (&_resource{client: cl}).pcsReplicaIndexFromScaleTarget(context.Background(), hpa)

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 5, got)
}

func TestBuildResource(t *testing.T) {
	tests := []struct {
		name                string
		hpaInfo             hpaInfo
		expectedMinReplicas int32
		expectedMaxReplicas int32
	}{
		{
			name: "Sets HPA spec from scaleConfig",
			hpaInfo: hpaInfo{
				targetScaleResourceKind: constants.KindPodClique,
				targetScaleResourceName: "test-resource",
				scaleConfig: grovecorev1alpha1.AutoScalingConfig{
					MinReplicas: ptr.To(int32(2)),
					MaxReplicas: 5,
				},
			},
			expectedMinReplicas: 2,
			expectedMaxReplicas: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &_resource{}
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).Build()
			hpa := &autoscalingv2.HorizontalPodAutoscaler{}

			// This would normally be set by the scheme, but for testing we can skip it
			err := r.buildResource(pcs, hpa, tt.hpaInfo)

			// We expect an error due to missing scheme, but we can still check the spec
			assert.NotNil(t, err) // Expected due to SetControllerReference failing
			assert.Equal(t, tt.expectedMinReplicas, *hpa.Spec.MinReplicas,
				"MinReplicas should be correctly set")
			assert.Equal(t, tt.expectedMaxReplicas, hpa.Spec.MaxReplicas,
				"MaxReplicas should be correctly set")
			assert.Equal(t, tt.hpaInfo.targetScaleResourceKind, hpa.Spec.ScaleTargetRef.Kind,
				"ScaleTargetRef Kind should be correctly set")
			assert.Equal(t, tt.hpaInfo.targetScaleResourceName, hpa.Spec.ScaleTargetRef.Name,
				"ScaleTargetRef Name should be correctly set")
		})
	}
}
