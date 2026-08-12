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
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
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

// TestBuildResource verifies that buildResource correctly populates PodGang labels and annotations.
// PCS owns the non-grove.io key namespace exclusively (additions and removals on the PCS propagate);
// grove.io/-prefixed keys are operator-managed and persist independent of PCS state. The operator
// also stamps a fixed set of managed labels (managed-by, part-of, component, replica-index,
// scheduler-name, generation-hash) plus any entry-derived extra labels (epoch, role).
func TestBuildResource(t *testing.T) {
	const (
		pcsName              = "test-pcs"
		namespace            = "default"
		defaultSchedulerName = "default-scheduler"
		generationHash       = "gen-hash-1"
	)
	// operatorLabels is the literal set of operator-managed labels buildResource stamps on every
	// PodGang for a replica-0 PodGang scheduled by the default scheduler. It is written out by hand
	// (not derived from buildLabels) so the test independently pins the expected label set.
	operatorLabels := func(schedulerName string, replicaIndex string) map[string]string {
		return map[string]string{
			apicommon.LabelManagedByKey:               apicommon.LabelManagedByValue,
			apicommon.LabelPartOfKey:                  pcsName,
			apicommon.LabelComponentKey:               apicommon.LabelComponentNamePodGang,
			apicommon.LabelPodCliqueSetReplicaIndex:   replicaIndex,
			apicommon.LabelSchedulerName:              schedulerName,
			apicommon.LabelPodCliqueSetGenerationHash: generationHash,
		}
	}
	expectedDefaultLabels := operatorLabels(defaultSchedulerName, "0")

	tests := []struct {
		name                       string
		tasEnabled                 bool
		schedulerName              string
		pcsLabels                  map[string]string
		pcsAnnotations             map[string]string
		pcsTopologyConstraint      *grovecorev1alpha1.TopologyConstraint
		pgiTopologyConstraint      *groveschedulerv1alpha1.TopologyConstraint
		pgiReplicaIndex            int
		pgiExtraLabels             map[string]string
		initialPodGangLabels       map[string]string
		initialPodGangAnnotations  map[string]string
		initialPodGangConstraint   *groveschedulerv1alpha1.TopologyConstraint
		expectedLabels             map[string]string
		expectedAnnotations        map[string]string
		expectedTopologyConstraint *groveschedulerv1alpha1.TopologyConstraint
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
			name:                "scheduler name label reflects the resolved scheduler backend",
			schedulerName:       "custom-scheduler",
			expectedLabels:      operatorLabels("custom-scheduler", "0"),
			expectedAnnotations: map[string]string{},
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
			name:            "replica index and entry-derived extra labels are stamped",
			pgiReplicaIndex: 2,
			pgiExtraLabels: map[string]string{
				apicommon.LabelEpoch:       "1000",
				apicommon.LabelPodGangRole: "Anchor",
			},
			expectedLabels: lo.Assign(
				operatorLabels(defaultSchedulerName, "2"),
				map[string]string{
					apicommon.LabelEpoch:       "1000",
					apicommon.LabelPodGangRole: "Anchor",
				},
			),
			expectedAnnotations: map[string]string{},
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
			expectedTopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{},
		},
		{
			name:       "tas enabled without translated constraints: clears stale topology annotation and constraint",
			tasEnabled: true,
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
				TopologyName: "cluster-topology",
				PackDomain:   "rack",
			},
			pgiTopologyConstraint: nil,
			initialPodGangAnnotations: map[string]string{
				apicommonconstants.AnnotationTopologyName: "stale-topology",
			},
			initialPodGangConstraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To("topology.kubernetes.io/rack")},
			},
			expectedLabels: expectedDefaultLabels,
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			expectedTopologyConstraint: nil,
		},
		{
			name:       "tas enabled with constraints but no resolvable topology name: clears topology annotation",
			tasEnabled: true,
			pcsAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			pcsTopologyConstraint: nil,
			pgiTopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{},
			initialPodGangAnnotations: map[string]string{
				apicommonconstants.AnnotationTopologyName: "stale-topology",
			},
			expectedLabels: expectedDefaultLabels,
			expectedAnnotations: map[string]string{
				"nvidia.com/kai-scheduler-queue": "worker-queue",
			},
			expectedTopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:        pcsName,
					Namespace:   namespace,
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
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: ptr.To(generationHash),
				},
			}

			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
			require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(pcs).
				Build()

			schedulerName := defaultSchedulerName
			if test.schedulerName != "" {
				schedulerName = test.schedulerName
			}
			registry := &testutils.FakeSchedulerRegistry{
				Backends:       map[string]scheduler.Backend{schedulerName: testutils.NewFakeSchedulerBackend(schedulerName)},
				DefaultBackend: schedulerName,
			}

			r := &_resource{
				client:        fakeClient,
				scheme:        scheme,
				eventRecorder: record.NewFakeRecorder(10),
				tasConfig: configv1alpha1.TopologyAwareSchedulingConfiguration{
					Enabled: test.tasEnabled,
				},
				schedRegistry: registry,
			}

			pg := &groveschedulerv1alpha1.PodGang{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   namespace,
					Name:        "test-pcs-0",
					Labels:      test.initialPodGangLabels,
					Annotations: test.initialPodGangAnnotations,
				},
				Spec: groveschedulerv1alpha1.PodGangSpec{
					TopologyConstraint: test.initialPodGangConstraint,
				},
			}

			pgi := &podGangInfo{
				fqn:                "test-pcs-0",
				pcsReplicaIndex:    test.pgiReplicaIndex,
				extraLabels:        test.pgiExtraLabels,
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
			assert.Equal(t, test.expectedTopologyConstraint, pg.Spec.TopologyConstraint)
		})
	}
}

// TestMirrorPCSMetadataNeverReturnsNil pins the contract that mirrorPCSMetadata
// always returns a non-nil map, which buildResource relies on when it directly
// indexes pg.Labels / pg.Annotations after the mirror.
func TestMirrorPCSMetadataNeverReturnsNil(t *testing.T) {
	assert.NotNil(t, mirrorPCSMetadata(nil, nil, nil))
}
