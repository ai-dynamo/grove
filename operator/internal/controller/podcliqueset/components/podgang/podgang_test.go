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

func TestSetInitializedCondition(t *testing.T) {
	pg := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-1", Namespace: "default", Generation: 1},
	}
	setOrUpdateInitializedCondition(pg, metav1.ConditionFalse, "PodsPending", "waiting")
	require.Len(t, pg.Status.Conditions, 1)
	assert.Equal(t, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized), pg.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionFalse, pg.Status.Conditions[0].Status)
	assert.Equal(t, "PodsPending", pg.Status.Conditions[0].Reason)
	assert.Equal(t, "waiting", pg.Status.Conditions[0].Message)

	// Update existing condition to ready
	setOrUpdateInitializedCondition(pg, metav1.ConditionTrue, "Ready", "all ready")
	require.Len(t, pg.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionTrue, pg.Status.Conditions[0].Status)
	assert.Equal(t, "Ready", pg.Status.Conditions[0].Reason)
}

// TestBuildResource verifies that buildResource correctly populates PodGang
// labels and annotations. It covers both the create path (empty PodGang) and
// the update path (PodGang with pre-existing metadata fetched from the API
// server inside controllerutil.CreateOrPatch), since the original bug wiped
// pre-existing user/admission-webhook metadata on update.
func TestBuildResource(t *testing.T) {
	const pcsName = "test-pcs"
	expectedDefaultLabels := getLabels(pcsName)

	tests := []struct {
		name                      string
		tasEnabled                bool
		pcsAnnotations            map[string]string
		pcsTopologyConstraint     *grovecorev1alpha1.TopologyConstraint
		pgiTopologyConstraint     *groveschedulerv1alpha1.TopologyConstraint
		initialPodGangLabels      map[string]string
		initialPodGangAnnotations map[string]string
		expectedLabels            map[string]string
		expectedAnnotations       map[string]string
	}{
		{
			name: "create path: copies PCS annotations onto empty podgang",
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			expectedLabels: expectedDefaultLabels,
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
		},
		{
			name: "create path: copies multiple PCS annotations onto empty podgang",
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue":      "worker-queue",
				"nvidia.com/dynamo-discovery-backend": "kubernetes",
			},
			expectedLabels: expectedDefaultLabels,
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue":      "worker-queue",
				"nvidia.com/dynamo-discovery-backend": "kubernetes",
			},
		},
		{
			name:       "update path: merges PCS annotations and preserves pre-existing pg metadata when tas is disabled",
			tasEnabled: false,
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
				"custom.annotation/from-pcs":     "pcs-value",
			},
			initialPodGangLabels: map[string]string{
				"external.label/keep": "true",
			},
			initialPodGangAnnotations: map[string]string{
				"external.annotation/keep": "true",
			},
			expectedLabels: mergeMaps(
				map[string]string{"external.label/keep": "true"},
				expectedDefaultLabels,
			),
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
				"custom.annotation/from-pcs":     "pcs-value",
				"external.annotation/keep":       "true",
			},
		},
		{
			name:       "update path: strips topology annotation when tas is disabled",
			tasEnabled: false,
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			initialPodGangAnnotations: map[string]string{
				apicommonconstants.AnnotationTopologyName: "stale-topology",
			},
			expectedLabels: expectedDefaultLabels,
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
		},
		{
			name:       "update path: sets resolved topology annotation when tas is enabled and constraints are present",
			tasEnabled: true,
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				TopologyName: "cluster-topology",
				PackDomain:   "rack",
			},
			pgiTopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{},
			initialPodGangAnnotations: map[string]string{
				apicommonconstants.AnnotationTopologyName: "stale-topology",
			},
			expectedLabels: expectedDefaultLabels,
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue":          "worker-queue",
				apicommonconstants.AnnotationTopologyName: "cluster-topology",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:        pcsName,
					Namespace:   "default",
					UID:         "test-uid-123",
					Annotations: test.pcsAnnotations,
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PriorityClassName:  "default-priority",
						TopologyConstraint: test.pcsTopologyConstraint,
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
				fqn:                "test-pcs-0",
				topologyConstraint: test.pgiTopologyConstraint,
				pclqs: []pclqInfo{
					{
						fqn:      "test-clique-0",
						replicas: 2,
					},
				},
			}

			require.NoError(t, r.buildResource(pcs, pgi, pg))

			// Strip the optional scheduler-name label that buildResource adds when a
			// default scheduler backend is registered, since this test does not
			// initialize the scheduler manager.
			delete(pg.Labels, apicommon.LabelSchedulerName)

			assert.Equal(t, test.expectedLabels, pg.Labels)
			assert.Equal(t, test.expectedAnnotations, pg.Annotations)
		})
	}
}

func mergeMaps(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
