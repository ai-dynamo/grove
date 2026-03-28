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

// TestPodGangAnnotationsFromPodCliqueSet verifies that scheduler queue annotations
// from PodCliqueSet are propagated to PodGang resources.
// This test reproduces the bug where kai-scheduler-queue annotation is lost when
// creating instances via dynamo.
func TestPodGangAnnotationsFromPodCliqueSet(t *testing.T) {
	tests := []struct {
		name                    string
		pcsAnnotations          map[string]string
		expectedPodGangContains map[string]string
		description             string
	}{
		{
			name: "PCS with kai-scheduler-queue annotation",
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			expectedPodGangContains: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			description: "Queue annotation from PCS should be present in PodGang",
		},
		{
			name: "PCS with multiple scheduler annotations",
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue":      "worker-queue",
				"nvidia.com/dynamo-discovery-backend": "kubernetes",
			},
			expectedPodGangContains: map[string]string{
				"nvidia.com/kai-scheduler-queue":      "worker-queue",
				"nvidia.com/dynamo-discovery-backend": "kubernetes",
			},
			description: "Multiple scheduler annotations should be propagated to PodGang",
		},
		{
			name: "PCS with queue annotation and other annotations",
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "special-queue",
				"custom.annotation/key":          "value",
			},
			expectedPodGangContains: map[string]string{
				"nvidia.com/kai-scheduler-queue": "special-queue",
				"custom.annotation/key":          "value",
			},
			description: "All PCS annotations including queue should be in PodGang",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create PodCliqueSet with scheduler queue annotation
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

			// Create a fake client and scheme
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
			require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(pcs).
				Build()

			// Create the podgang resource operator
			eventRecorder := record.NewFakeRecorder(10)
			tasConfig := configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: false,
			}

			r := &_resource{
				client:        fakeClient,
				scheme:        scheme,
				eventRecorder: eventRecorder,
				tasConfig:     tasConfig,
			}

			// Create a PodGang object to be populated
			pg := &groveschedulerv1alpha1.PodGang{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test-pcs-0",
				},
			}

			// Create podGangInfo with mock data
			pgi := &podGangInfo{
				fqn: "test-pcs-0",
				pclqs: []pclqInfo{
					{
						fqn:      "test-clique-0",
						replicas: 2,
					},
				},
			}

			// Call buildResource which should populate annotations
			err := r.buildResource(pcs, pgi, pg)
			require.NoError(t, err, "buildResource should not error")

			// Verify that all expected annotations are present in PodGang
			for expectedKey, expectedValue := range test.expectedPodGangContains {
				actualValue, exists := pg.Annotations[expectedKey]
				assert.True(t, exists, "PodGang should have annotation %s", expectedKey)
				assert.Equal(t, expectedValue, actualValue,
					"PodGang annotation %s should match PCS annotation", expectedKey)
			}
		})
	}
}
