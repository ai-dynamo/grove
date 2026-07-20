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

package podgang

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/lpx"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildResourceWithLPXBackend(t *testing.T) {
	const (
		namespace   = "default"
		pcsName     = "model"
		cliqueName  = "gpu-worker"
		podGangName = "model-0"
		podName     = "model-0-gpu-worker-abcde"
	)

	pcs := testutils.NewPodCliqueSetBuilder(pcsName, namespace, "test-uid").
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
				WithPodSpec(corev1.PodSpec{
					SchedulerName: string(configv1alpha1.SchedulerNameLPX),
					Containers: []corev1.Container{{
						Name:  "worker",
						Image: "worker",
					}},
				}).
				Build(),
		).
		Build()
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))
	registry := &testutils.FakeSchedulerRegistry{
		Backends: map[string]scheduler.Backend{
			string(configv1alpha1.SchedulerNameLPX): lpx.New(
				configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX},
			),
		},
		DefaultBackend: string(configv1alpha1.SchedulerNameLPX),
	}
	resource := &_resource{
		scheme:        scheme,
		schedRegistry: registry,
	}
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: podGangName, Namespace: namespace},
	}
	info := &podGangInfo{
		fqn: podGangName,
		pclqs: []pclqInfo{{
			fqn:                "model-0-gpu-worker",
			replicas:           1,
			minAvailable:       1,
			associatedPodNames: []string{podName},
		}},
	}

	require.NoError(t, resource.buildResource(pcs, info, podGang))

	assert.Equal(t, string(configv1alpha1.SchedulerNameLPX), podGang.Labels[apicommon.LabelSchedulerName])
	require.Len(t, podGang.Spec.PodGroups, 1)
	require.Len(t, podGang.Spec.PodGroups[0].PodReferences, 1)
	assert.Equal(t, namespace, podGang.Spec.PodGroups[0].PodReferences[0].Namespace)
	assert.Equal(t, podName, podGang.Spec.PodGroups[0].PodReferences[0].Name)
}

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
// labels and annotations. PCS owns the non-grove.io key namespace exclusively
// (additions and removals on the PCS propagate); grove.io/-prefixed keys are
// operator-managed and persist independent of PCS state.
func TestBuildResource(t *testing.T) {
	const (
		pcsName              = "test-pcs"
		defaultSchedulerName = "default-scheduler"
	)
	// expectedDefaultLabels reflects what every PodGang carries after buildResource:
	// the operator-managed label set from getLabels plus the scheduler name resolved
	// from the fake registry (testutils.NewDefaultFakeRegistry returns "default-scheduler").
	expectedDefaultLabels := lo.Assign(
		getLabels(pcsName),
		map[string]string{apicommon.LabelSchedulerName: defaultSchedulerName},
	)

	tests := []struct {
		name                      string
		tasEnabled                bool
		pcsLabels                 map[string]string
		pcsAnnotations            map[string]string
		pcsTopologyConstraint     *grovecorev1alpha1.TopologyConstraint
		pgiTopologyConstraint     *groveschedulerv1alpha1.TopologyConstraint
		initialPodGangLabels      map[string]string
		initialPodGangAnnotations map[string]string
		expectedLabels            map[string]string
		expectedAnnotations       map[string]string
	}{
		{
			name: "create path: mirrors PCS annotations onto empty podgang",
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			expectedLabels: expectedDefaultLabels,
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
		},
		{
			name: "create path: mirrors multiple PCS annotations onto empty podgang",
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
			name: "create path: mirrors PCS labels onto empty podgang alongside operator labels",
			pcsLabels: map[string]string{
				"team":                   "platform",
				"app.kubernetes.io/name": "demo",
			},
			expectedLabels: lo.Assign(
				map[string]string{
					"team":                   "platform",
					"app.kubernetes.io/name": "demo",
				},
				expectedDefaultLabels,
			),
			expectedAnnotations: map[string]string{},
		},
		{
			name: "mirror path: drops stale non-grove.io annotation that PCS no longer carries",
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			initialPodGangAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
				"nvidia.com/stale-key":           "stale-value",
			},
			expectedLabels: expectedDefaultLabels,
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
		},
		{
			name:                 "mirror path: drops stale non-grove.io label that PCS no longer carries",
			pcsLabels:            map[string]string{"team": "platform"},
			initialPodGangLabels: map[string]string{"team": "platform", "stale.label/key": "stale-value"},
			expectedLabels: lo.Assign(
				map[string]string{"team": "platform"},
				expectedDefaultLabels,
			),
			expectedAnnotations: map[string]string{},
		},
		{
			name: "mirror path: preserves grove.io annotations on the podgang regardless of PCS",
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			initialPodGangAnnotations: map[string]string{
				"grove.io/operator-managed": "controller-set",
			},
			expectedLabels: expectedDefaultLabels,
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
				"grove.io/operator-managed":      "controller-set",
			},
		},
		{
			name: "mirror path: ignores grove.io entries set on the PCS (operator owns that namespace)",
			pcsLabels: map[string]string{
				"grove.io/should-not-mirror": "from-pcs",
			},
			pcsAnnotations: map[string]string{
				"grove.io/should-not-mirror":     "from-pcs",
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			expectedLabels: expectedDefaultLabels,
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
		},
		{
			name:       "tas disabled: strips controller-managed topology annotation even if pre-existing",
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
			name: "stale grove.io/scheduler-name label is overwritten by the resolved scheduler",
			initialPodGangLabels: map[string]string{
				apicommon.LabelSchedulerName: "stale-scheduler",
			},
			expectedLabels:      expectedDefaultLabels,
			expectedAnnotations: map[string]string{},
		},
		{
			name:       "tas enabled with translated constraints: sets resolved topology annotation",
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
					Labels:      test.pcsLabels,
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
				schedRegistry: testutils.NewDefaultFakeRegistry(),
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

			assert.Equal(t, test.expectedLabels, pg.Labels)
			assert.Equal(t, test.expectedAnnotations, pg.Annotations)
		})
	}
}

// TestMirrorPCSMetadataNeverReturnsNil pins the contract that mirrorPCSMetadata
// always returns a non-nil map, which buildResource relies on when it directly
// indexes pg.Labels / pg.Annotations after the mirror.
func TestMirrorPCSMetadataNeverReturnsNil(t *testing.T) {
	assert.NotNil(t, mirrorPCSMetadata(nil, nil, nil))
}
