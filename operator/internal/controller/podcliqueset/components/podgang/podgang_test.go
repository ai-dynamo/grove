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

package podgang

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildResource(t *testing.T) {
	tests := []struct {
		name                      string
		tasEnabled                bool
		pcsAnnotations            map[string]string
		initialPodGangLabels      map[string]string
		initialPodGangAnnotations map[string]string
		expectedLabels            map[string]string
		expectedAnnotations       map[string]string
	}{
		{
			name: "copies PCS annotations onto empty podgang",
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			expectedLabels: map[string]string{
				apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
			},
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
		},
		{
			name: "copies multiple PCS annotations onto empty podgang",
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue":      "worker-queue",
				"nvidia.com/dynamo-discovery-backend": "kubernetes",
			},
			expectedLabels: map[string]string{
				apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
			},
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue":      "worker-queue",
				"nvidia.com/dynamo-discovery-backend": "kubernetes",
			},
		},
		{
			name:       "preserves existing annotations on update path when tas is disabled",
			tasEnabled: false,
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
				"custom.annotation/from-pcs":     "pcs-value",
			},
			initialPodGangLabels: map[string]string{
				"external.label/keep": "true",
			},
			initialPodGangAnnotations: map[string]string{
				"external.annotation/keep":                "true",
				apicommonconstants.AnnotationTopologyName: "existing-topology",
			},
			expectedLabels: map[string]string{
				"external.label/keep":       "true",
				apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
			},
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue":          "worker-queue",
				"custom.annotation/from-pcs":              "pcs-value",
				"external.annotation/keep":                "true",
				apicommonconstants.AnnotationTopologyName: "existing-topology",
			},
		},
		{
			name:       "preserves existing metadata and overrides topology annotation when tas is enabled",
			tasEnabled: true,
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
				"custom.annotation/from-pcs":     "pcs-value",
			},
			initialPodGangLabels: map[string]string{
				"external.label/keep": "true",
			},
			initialPodGangAnnotations: map[string]string{
				"external.annotation/keep":                "true",
				apicommonconstants.AnnotationTopologyName: "old-topology",
			},
			expectedLabels: map[string]string{
				"external.label/keep":       "true",
				apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
			},
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue":          "worker-queue",
				"custom.annotation/from-pcs":              "pcs-value",
				"external.annotation/keep":                "true",
				apicommonconstants.AnnotationTopologyName: grovecorev1alpha1.DefaultClusterTopologyName,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-pcs",
					Namespace:   "default",
					UID:         "test-uid-123",
					Annotations: test.pcsAnnotations,
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PriorityClassName: "default-priority",
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "test-clique",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas: 2,
								},
							},
						},
					},
				},
			}

			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
			require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(pcs).
				Build()

			r := &_resource{
				client:        fakeClient,
				scheme:        scheme,
				eventRecorder: record.NewFakeRecorder(10),
				tasConfig: configv1alpha1.TopologyAwareSchedulingConfiguration{
					Enabled: test.tasEnabled,
				},
			}

			pg := &groveschedulerv1alpha1.PodGang{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   "default",
					Name:        "test-pcs-0",
					Labels:      test.initialPodGangLabels,
					Annotations: test.initialPodGangAnnotations,
				},
			}

			pgi := &podGangInfo{
				fqn: "test-pcs-0",
				pclqs: []pclqInfo{
					{
						fqn:      "test-clique-0",
						replicas: 2,
					},
				},
			}

			err := r.buildResource(pcs, pgi, pg)
			require.NoError(t, err)

			for expectedKey, expectedValue := range test.expectedLabels {
				actualValue, exists := pg.Labels[expectedKey]
				assert.True(t, exists, "PodGang should have label %s", expectedKey)
				assert.Equal(t, expectedValue, actualValue,
					"PodGang label %s should match expected value", expectedKey)
			}

			for expectedKey, expectedValue := range test.expectedAnnotations {
				actualValue, exists := pg.Annotations[expectedKey]
				assert.True(t, exists, "PodGang should have annotation %s", expectedKey)
				assert.Equal(t, expectedValue, actualValue,
					"PodGang annotation %s should match expected value", expectedKey)
			}
		})
	}
}
