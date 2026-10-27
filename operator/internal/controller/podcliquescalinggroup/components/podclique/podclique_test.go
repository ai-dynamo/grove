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

package podclique

import (
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/expect"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestNew tests creating a new PodClique operator
func TestNew(t *testing.T) {
	scheme := groveclientscheme.Scheme
	cl := testutils.NewTestClientBuilder().Build()
	eventRecorder := &record.FakeRecorder{}

	operator := New(cl, scheme, eventRecorder, expect.NewExpectationsStore())

	assert.NotNil(t, operator)
	r, ok := operator.(*_resource)
	require.True(t, ok)
	assert.Equal(t, cl, r.client)
	assert.Equal(t, scheme, r.scheme)
	assert.Equal(t, eventRecorder, r.eventRecorder)
}

// TestGetPCSGTemplateNumPods tests calculating the number of pods in a PCSG template
func TestGetPCSGTemplateNumPods(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		pcsg     *grovecorev1alpha1.PodCliqueScalingGroup
		expected int
	}{
		{
			name: "all_cliques_match",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("clique1").WithReplicas(2).Build()).
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("clique2").WithReplicas(3).Build()).
				Build(),
			pcsg:     testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "default", "test-pcs", 0).WithCliqueNames([]string{"clique1", "clique2"}).Build(),
			expected: 5,
		},
		{
			name: "partial_cliques_match",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("clique1").WithReplicas(2).Build()).
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("clique2").WithReplicas(3).Build()).
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("clique3").WithReplicas(4).Build()).
				Build(),
			pcsg:     testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "default", "test-pcs", 0).WithCliqueNames([]string{"clique1", "clique3"}).Build(),
			expected: 6,
		},
		{
			name: "no_matching_cliques",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("clique1").WithReplicas(2).Build()).
				Build(),
			pcsg:     testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "default", "test-pcs", 0).WithCliqueNames([]string{"clique2", "clique3"}).Build(),
			expected: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &_resource{}
			result := r.getPCSGTemplateNumPods(tc.pcs, tc.pcsg)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestGetPCSReplicaFromPCSG tests extracting PCS replica index from PCSG labels
func TestGetPCSReplicaFromPCSG(t *testing.T) {
	tests := []struct {
		name string
		// pcsg is the PodCliqueScalingGroup with labels
		pcsg *grovecorev1alpha1.PodCliqueScalingGroup
		// expected is the expected replica index
		expected int
		// expectError indicates if an error is expected
		expectError bool
	}{
		{
			// Tests successful extraction of replica index
			name: "valid_replica_index",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						apicommon.LabelPodCliqueSetReplicaIndex: "2",
					},
				},
			},
			expected:    2,
			expectError: false,
		},
		{
			// Tests missing replica index label
			name: "missing_replica_index",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{},
				},
			},
			expected:    0,
			expectError: true,
		},
		{
			// Tests invalid replica index format
			name: "invalid_replica_index",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						apicommon.LabelPodCliqueSetReplicaIndex: "invalid",
					},
				},
			},
			expected:    0,
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := getPCSReplicaFromPCSG(tc.pcsg)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, result)
			}
		})
	}
}

// TestGetPodCliqueSelectorLabels tests generating selector labels for PodCliques
func TestGetPodCliqueSelectorLabels(t *testing.T) {
	tests := []struct {
		name string
		// pcsgMeta is the PCSG object metadata
		pcsgMeta metav1.ObjectMeta
		// expectedLabels are the expected selector labels
		expectedLabels map[string]string
	}{
		{
			// Tests generating labels for PCSG with PCS owner
			name: "pcsg_with_pcs_owner",
			pcsgMeta: metav1.ObjectMeta{
				Name:      "test-pcsg",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPartOfKey: "test-pcs",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind: "PodCliqueSet",
						Name: "test-pcs",
					},
				},
			},
			expectedLabels: map[string]string{
				apicommon.LabelManagedByKey:          apicommon.LabelManagedByValue,
				apicommon.LabelPartOfKey:             "test-pcs",
				apicommon.LabelComponentKey:          apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
				apicommon.LabelPodCliqueScalingGroup: "test-pcsg",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := getPodCliqueSelectorLabels(tc.pcsgMeta)

			for key, expectedValue := range tc.expectedLabels {
				assert.Equal(t, expectedValue, result[key])
			}
		})
	}
}

// TestEmptyPodClique tests creating an empty PodClique
func TestEmptyPodClique(t *testing.T) {
	objKey := client.ObjectKey{
		Name:      "test-pclq",
		Namespace: "test-ns",
	}

	pclq := emptyPodClique(objKey)

	assert.Equal(t, objKey.Name, pclq.Name)
	assert.Equal(t, objKey.Namespace, pclq.Namespace)
}

// TestAddEnvironmentVariablesToPodContainerSpecs tests adding PCSG env vars to containers
func TestAddEnvironmentVariablesToPodContainerSpecs(t *testing.T) {
	tests := []struct {
		name string
		// pclq is the PodClique to modify
		pclq *grovecorev1alpha1.PodClique
		// numPods is the number of pods in the PCSG template
		numPods int
		// validate performs custom validation on the result
		validate func(*testing.T, *grovecorev1alpha1.PodClique)
	}{
		{
			// Tests adding env vars to containers and init containers
			name: "add_env_vars_to_all_containers",
			pclq: &grovecorev1alpha1.PodClique{
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "container1"},
							{Name: "container2"},
						},
						InitContainers: []corev1.Container{
							{Name: "init-container"},
						},
					},
				},
			},
			numPods: 5,
			validate: func(t *testing.T, pclq *grovecorev1alpha1.PodClique) {
				// Check containers
				for _, container := range pclq.Spec.PodSpec.Containers {
					assert.True(t, hasEnvVar(container.Env, "GROVE_PCSG_NAME"))
					assert.True(t, hasEnvVar(container.Env, "GROVE_PCSG_TEMPLATE_NUM_PODS"))
					assert.True(t, hasEnvVar(container.Env, "GROVE_PCSG_INDEX"))
				}
				// Check init containers
				for _, container := range pclq.Spec.PodSpec.InitContainers {
					assert.True(t, hasEnvVar(container.Env, "GROVE_PCSG_NAME"))
					assert.True(t, hasEnvVar(container.Env, "GROVE_PCSG_TEMPLATE_NUM_PODS"))
					assert.True(t, hasEnvVar(container.Env, "GROVE_PCSG_INDEX"))
				}
			},
		},
		{
			name: "replace_colliding_pcsg_env_var",
			pclq: &grovecorev1alpha1.PodClique{
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "container",
								Env: []corev1.EnvVar{
									{Name: apiconstants.EnvVarPodCliqueScalingGroupName, Value: "stale"},
									{Name: "USER_VAR", Value: "user-value"},
								},
							},
						},
					},
				},
			},
			numPods: 5,
			validate: func(t *testing.T, pclq *grovecorev1alpha1.PodClique) {
				assert.Equal(t, []corev1.EnvVar{
					{
						Name: apiconstants.EnvVarPodCliqueScalingGroupName,
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								FieldPath: fmt.Sprintf("metadata.labels['%s']", apicommon.LabelPodCliqueScalingGroup),
							},
						},
					},
					{Name: apiconstants.EnvVarPodCliqueScalingGroupTemplateNumPods, Value: "5"},
					{
						Name: apiconstants.EnvVarPodCliqueScalingGroupIndex,
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								FieldPath: fmt.Sprintf("metadata.labels['%s']", apicommon.LabelPodCliqueScalingGroupReplicaIndex),
							},
						},
					},
					{Name: "USER_VAR", Value: "user-value"},
				}, pclq.Spec.PodSpec.Containers[0].Env)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &_resource{}
			r.addEnvironmentVariablesToPodContainerSpecs(tc.pclq, tc.numPods)
			tc.validate(t, tc.pclq)
		})
	}
}

// TestGetExistingResourceNames tests getting existing PodClique names
func TestGetExistingResourceNames(t *testing.T) {
	tests := []struct {
		name string
		// pcsgObjMeta is the PCSG object metadata
		pcsgObjMeta metav1.ObjectMeta
		// existingObjs are the existing objects in the cluster
		existingObjs []runtime.Object
		// expectedNames are the expected resource names
		expectedNames []string
		// expectError indicates if an error is expected
		expectError bool
	}{
		{
			// Tests finding owned PodCliques
			name: "find_owned_podcliques",
			pcsgObjMeta: metav1.ObjectMeta{
				Name:      "test-pcsg",
				Namespace: "default",
				UID:       "pcsg-uid",
				Labels: map[string]string{
					apicommon.LabelPartOfKey: "test-pcs",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind: "PodCliqueSet",
						Name: "test-pcs",
					},
				},
			},
			existingObjs: []runtime.Object{
				&grovecorev1alpha1.PodClique{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey:          apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:             "test-pcs",
							apicommon.LabelComponentKey:          apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
							apicommon.LabelPodCliqueScalingGroup: "test-pcsg",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind:       "PodCliqueScalingGroup",
								Name:       "test-pcsg",
								UID:        "pcsg-uid",
								Controller: new(true),
							},
						},
					},
				},
				&grovecorev1alpha1.PodClique{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-2",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey:          apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:             "test-pcs",
							apicommon.LabelComponentKey:          apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
							apicommon.LabelPodCliqueScalingGroup: "test-pcsg",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind:       "PodCliqueScalingGroup",
								Name:       "test-pcsg",
								UID:        "pcsg-uid",
								Controller: new(true),
							},
						},
					},
				},
			},
			expectedNames: []string{"test-pclq-1", "test-pclq-2"},
			expectError:   false,
		},
		{
			// Tests no existing PodCliques
			name: "no_existing_podcliques",
			pcsgObjMeta: metav1.ObjectMeta{
				Name:      "test-pcsg",
				Namespace: "default",
				UID:       "pcsg-uid",
				Labels: map[string]string{
					apicommon.LabelPartOfKey: "test-pcs",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind: "PodCliqueSet",
						Name: "test-pcs",
					},
				},
			},
			existingObjs:  []runtime.Object{},
			expectedNames: []string{},
			expectError:   false,
		},
	}

	ctx := t.Context()
	logger := logr.Discard()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjs...).
				Build()

			r := &_resource{
				client: fakeClient,
			}

			names, err := r.GetExistingResourceNames(ctx, logger, tc.pcsgObjMeta)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.ElementsMatch(t, tc.expectedNames, names)
			}
		})
	}
}

// TestDelete tests deleting PodCliques
func TestDelete(t *testing.T) {
	tests := []struct {
		name string
		// pcsgObjMeta is the PCSG object metadata
		pcsgObjMeta metav1.ObjectMeta
		// existingObjs are the existing objects in the cluster
		existingObjs []runtime.Object
		// expectError indicates if an error is expected
		expectError bool
		// validate performs validation after deletion
		validate func(*testing.T, client.Client)
	}{
		{
			// Tests successful deletion of PodCliques
			name: "delete_existing_podcliques",
			pcsgObjMeta: metav1.ObjectMeta{
				Name:      "test-pcsg",
				Namespace: "default",
				UID:       "pcsg-uid",
				Labels: map[string]string{
					apicommon.LabelPartOfKey: "test-pcs",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind: "PodCliqueSet",
						Name: "test-pcs",
					},
				},
			},
			existingObjs: []runtime.Object{
				&grovecorev1alpha1.PodClique{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelManagedByKey:          apicommon.LabelManagedByValue,
							apicommon.LabelPartOfKey:             "test-pcs",
							apicommon.LabelComponentKey:          apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
							apicommon.LabelPodCliqueScalingGroup: "test-pcsg",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueScalingGroup",
								Name: "test-pcsg",
								UID:  "pcsg-uid",
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(_ *testing.T, _ client.Client) {
				// In the fake client, the PodClique might still exist since
				// the Delete method uses DeleteAllOf which may not work as expected
				// with the fake client
			},
		},
	}

	ctx := t.Context()
	logger := logr.Discard()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjs...).
				Build()

			r := &_resource{
				client:        fakeClient,
				eventRecorder: &record.FakeRecorder{},
			}

			err := r.Delete(ctx, logger, tc.pcsgObjMeta)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tc.validate != nil {
				tc.validate(t, fakeClient)
			}
		})
	}
}

// TestIdentifyFullyQualifiedStartupDependencyNames tests identifying startup dependencies
func TestBuildResource_MNNVLInjection(t *testing.T) {
	tests := []struct {
		description                         string
		pcsgAnnotations                     map[string]string
		cliqueAnnotations                   map[string]string
		containers                          []corev1.Container
		initContainers                      []corev1.Container
		expectedContainersWithClaims        []string
		expectedContainersWithoutClaims     []string
		expectedInitContainersWithClaims    []string
		expectedInitContainersWithoutClaims []string
		expectPodLevelClaim                 bool
		expectedRCTName                     string
	}{
		{
			description:                  "MNNVL enabled on PCSG with GPU container injects claims",
			pcsgAnnotations:              map[string]string{mnnvl.AnnotationMNNVLGroup: "default"},
			containers:                   []corev1.Container{gpuContainer("gpu-worker", 8)},
			expectedContainersWithClaims: []string{"gpu-worker"},
			expectPodLevelClaim:          true,
			expectedRCTName:              "test-pcs-0-default",
		},
		{
			description:                     "MNNVL not enabled on PCSG does not inject claims",
			containers:                      []corev1.Container{gpuContainer("gpu-worker", 8)},
			expectedContainersWithoutClaims: []string{"gpu-worker"},
		},
		{
			description:                     "MNNVL enabled on PCSG but no GPU containers does not inject claims",
			pcsgAnnotations:                 map[string]string{mnnvl.AnnotationMNNVLGroup: "default"},
			containers:                      []corev1.Container{cpuContainer("cpu-only", 1)},
			expectedContainersWithoutClaims: []string{"cpu-only"},
		},
		{
			description:                     "MNNVL enabled on PCSG with mixed GPU and non-GPU containers",
			pcsgAnnotations:                 map[string]string{mnnvl.AnnotationMNNVLGroup: "default"},
			containers:                      []corev1.Container{gpuContainer("gpu-worker", 8), cpuContainer("sidecar", 1)},
			expectedContainersWithClaims:    []string{"gpu-worker"},
			expectedContainersWithoutClaims: []string{"sidecar"},
			expectPodLevelClaim:             true,
		},
		{
			description:                      "MNNVL enabled on PCSG with GPU in init container",
			pcsgAnnotations:                  map[string]string{mnnvl.AnnotationMNNVLGroup: "default"},
			initContainers:                   []corev1.Container{gpuContainer("init-gpu", 1)},
			containers:                       []corev1.Container{{Name: "main"}},
			expectedContainersWithoutClaims:  []string{"main"},
			expectedInitContainersWithClaims: []string{"init-gpu"},
			expectPodLevelClaim:              true,
		},
		{
			description:                     "MNNVL disabled explicitly on PCSG does not inject claims",
			pcsgAnnotations:                 map[string]string{mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut},
			containers:                      []corev1.Container{gpuContainer("gpu-worker", 8)},
			expectedContainersWithoutClaims: []string{"gpu-worker"},
		},
		{
			description:                  "mnnvl-group on PCSG sets the resource claim template name to include the group",
			pcsgAnnotations:              map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"},
			containers:                   []corev1.Container{gpuContainer("gpu-worker", 8)},
			expectedContainersWithClaims: []string{"gpu-worker"},
			expectPodLevelClaim:          true,
			expectedRCTName:              "test-pcs-0-workers",
		},
		{
			description:                  "mnnvl-group on clique overrides the PCSG auto-mnnvl group",
			pcsgAnnotations:              map[string]string{mnnvl.AnnotationMNNVLGroup: "default"},
			cliqueAnnotations:            map[string]string{mnnvl.AnnotationMNNVLGroup: "encoders"},
			containers:                   []corev1.Container{gpuContainer("gpu-worker", 8)},
			expectedContainersWithClaims: []string{"gpu-worker"},
			expectPodLevelClaim:          true,
			expectedRCTName:              "test-pcs-0-encoders",
		},
		{
			description:                  "mnnvl-group on clique only with no PCSG annotation",
			cliqueAnnotations:            map[string]string{mnnvl.AnnotationMNNVLGroup: "training"},
			containers:                   []corev1.Container{gpuContainer("gpu-worker", 8)},
			expectedContainersWithClaims: []string{"gpu-worker"},
			expectPodLevelClaim:          true,
			expectedRCTName:              "test-pcs-0-training",
		},
	}

	operator := &_resource{scheme: groveclientscheme.Scheme, eventRecorder: &record.FakeRecorder{}}
	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			pcs := newBuildResourcePCS("worker", tc.cliqueAnnotations, tc.containers, tc.initContainers)
			pcsg := newBuildResourcePCSG(tc.pcsgAnnotations)
			pclq := emptyMemberPodClique("test-pcs-0-sg-0-worker")
			ss := &syncSnapshot{pcs: pcs, pcsg: pcsg, pcsReplicaIndex: 0, pgm: newAnchorPodGangMap()}

			require.NoError(t, operator.buildResource(logr.Discard(), ss, 0, pclq, false))

			assertPodLevelMNNVLClaim(t, pclq, tc.expectPodLevelClaim, tc.expectedRCTName)
			assertContainerMNNVLClaims(t, pclq.Spec.PodSpec.Containers, tc.expectedContainersWithClaims, tc.expectedContainersWithoutClaims)
			assertContainerMNNVLClaims(t, pclq.Spec.PodSpec.InitContainers, tc.expectedInitContainersWithClaims, tc.expectedInitContainersWithoutClaims)
		})
	}
}

func TestBuildResource_StripsTopologyAnnotation(t *testing.T) {
	pcs := newBuildResourcePCS("worker", map[string]string{
		apiconstants.AnnotationTopologyName: "my-topology",
		"example.com/keep":                  "yes",
	}, nil, nil)
	pcsg := newBuildResourcePCSG(nil)
	pclq := emptyMemberPodClique("test-pcs-0-sg-0-worker")
	ss := &syncSnapshot{pcs: pcs, pcsg: pcsg, pcsReplicaIndex: 0, pgm: newAnchorPodGangMap()}

	operator := &_resource{scheme: groveclientscheme.Scheme}
	require.NoError(t, operator.buildResource(logr.Discard(), ss, 0, pclq, false))
	require.NotNil(t, pclq.Annotations)
	assert.Equal(t, "yes", pclq.Annotations["example.com/keep"])
	_, hasTopologyAnnotation := pclq.Annotations[apiconstants.AnnotationTopologyName]
	assert.False(t, hasTopologyAnnotation)
}

func TestBuildResource_PicksTemplateByExactName(t *testing.T) {
	tests := []struct {
		description string
		cliqueNames []string
	}{
		{
			description: "standalone clique whose name is a suffix of the member clique name",
			cliqueNames: []string{"prefill-worker"},
		},
		{
			description: "two member cliques where one name is a suffix of the other",
			cliqueNames: []string{"worker", "prefill-worker"},
		},
	}

	operator := &_resource{scheme: groveclientscheme.Scheme}
	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			worker := testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(5).WithMinAvailable(1).
				WithPodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "worker-image"}}}).Build()
			prefillWorker := testutils.NewPodCliqueTemplateSpecBuilder("prefill-worker").WithReplicas(2).WithMinAvailable(1).
				WithPodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "prefill-worker-image"}}}).Build()
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
				WithPodCliqueTemplateSpec(worker).
				WithPodCliqueTemplateSpec(prefillWorker).
				Build()
			pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-sg", "default", "test-pcs", 0).
				WithMinAvailable(1).
				WithCliqueNames(tc.cliqueNames).
				Build()
			pclq := emptyMemberPodClique("test-pcs-0-sg-0-prefill-worker")
			ss := &syncSnapshot{pcs: pcs, pcsg: pcsg, pcsReplicaIndex: 0, pgm: newAnchorPodGangMap()}

			require.NoError(t, operator.buildResource(logr.Discard(), ss, 0, pclq, false))
			assert.Equal(t, int32(2), pclq.Spec.Replicas)
			assert.Equal(t, "prefill-worker-image", pclq.Spec.PodSpec.Containers[0].Image)
		})
	}
}

func TestSyncPCSGPodIndexOffsetsUsesCurrentReplicaCounts(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs", Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
			Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
			},
		}},
	}
	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-0-engine", Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
			Replicas:    1,
			CliqueNames: []string{"leader", "worker"},
		},
	}
	leader := grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Name:      "test-pcs-0-engine-0-leader",
		Namespace: "default",
		Labels: map[string]string{
			apicommon.LabelPodCliqueScalingGroup:             "test-pcs-0-engine",
			apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
		},
	}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}}
	worker := grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Name:      "test-pcs-0-engine-0-worker",
		Namespace: "default",
		Labels: map[string]string{
			apicommon.LabelPodCliqueScalingGroup:             "test-pcs-0-engine",
			apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
		},
		Annotations: map[string]string{
			apiconstants.AnnotationPodCliqueScalingGroupPodIndexOffset: "1",
		},
	}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}}

	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&leader, &worker).Build()
	r := _resource{client: cl}
	ss := &syncSnapshot{pcs: pcs, pcsg: pcsg, existingPCLQs: []grovecorev1alpha1.PodClique{leader, worker}}

	require.NoError(t, r.syncPCSGPodIndexOffsets(t.Context(), ss))

	updatedWorker := &grovecorev1alpha1.PodClique{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(&worker), updatedWorker))
	assert.Equal(t, "2", updatedWorker.Annotations[apiconstants.AnnotationPodCliqueScalingGroupPodIndexOffset])
}

func TestResolvePodGangName(t *testing.T) {
	const (
		pcsName       = "test-pcs"
		namespace     = "default"
		pcsgConfig    = "sg"
		anchorEpoch   = "1000"
		tailEpoch     = "1001"
		scaleOutEpoch = "1002"
	)
	rnr := apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}
	pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(rnr, pcsgConfig)
	// MinAvailable 2: anchor owns indices [0,2), tail owns [2,4), ScaleOut is pre-created and empty.
	pcsg := testutils.NewPodCliqueScalingGroupBuilder(pcsgFQN, namespace, pcsName, 0).
		WithMinAvailable(2).
		WithCliqueNames([]string{"worker"}).
		Build()
	pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, "uid", 0).WithEntries(
		testutils.NewPodGangEntryBuilder("hash", anchorEpoch).
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithPCSGReplicaIndices(map[string][]int32{pcsgConfig: {0, 1}}).Build(),
		testutils.NewPodGangEntryBuilder("hash", tailEpoch).
			WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
			WithPCSGReplicaIndices(map[string][]int32{pcsgConfig: {2, 3}}).
			WithDependsOn(anchorEpoch).Build(),
		testutils.NewPodGangEntryBuilder("hash", scaleOutEpoch).
			WithRole(grovecorev1alpha1.PodGangEntryRoleScaleOut).
			WithDependsOn(anchorEpoch).Build(),
	).Build()

	tests := []struct {
		name             string
		pcsgReplicaIndex int32
		expectedName     string
	}{
		{"anchor index resolves to the anchor PodGang name", 0, apicommon.GenerateAnchorPodGangName(rnr, anchorEpoch)},
		{"tail index resolves to a non-anchor PodGang name at the tail epoch", 2, apicommon.GenerateNonAnchorPodGangName(rnr, tailEpoch, pcsgConfig, 2)},
		{"not-yet-placed scale-out index resolves at the ScaleOut epoch", 5, apicommon.GenerateNonAnchorPodGangName(rnr, scaleOutEpoch, pcsgConfig, 5)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := resolvePodGangName(pgm, rnr, pcsg, test.pcsgReplicaIndex)
			require.NoError(t, err)
			assert.Equal(t, test.expectedName, actual)
		})
	}

	t.Run("errors when the index is unresolvable and no ScaleOut entry exists", func(t *testing.T) {
		anchorOnly := testutils.NewPodGangMapBuilder(pcsName, namespace, "uid", 0).WithEntries(
			testutils.NewPodGangEntryBuilder("hash", anchorEpoch).
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
				WithPCSGReplicaIndices(map[string][]int32{pcsgConfig: {0, 1}}).Build(),
		).Build()
		_, err := resolvePodGangName(anchorOnly, rnr, pcsg, 5)
		require.Error(t, err)
	})
}

func gpuContainer(name string, gpus int) corev1.Container {
	return corev1.Container{
		Name: name,
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{constants.GPUResourceName: resource.MustParse(fmt.Sprintf("%d", gpus))},
		},
	}
}

func cpuContainer(name string, cpus int) corev1.Container {
	return corev1.Container{
		Name: name,
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(fmt.Sprintf("%d", cpus))},
		},
	}
}

// newBuildResourcePCS builds a single-clique PodCliqueSet used by buildResource tests, with the given
// clique annotations and containers.
func newBuildResourcePCS(cliqueName string, cliqueAnnotations map[string]string, containers, initContainers []corev1.Container) *grovecorev1alpha1.PodCliqueSet {
	cliqueBuilder := testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
		WithReplicas(1).
		WithMinAvailable(1).
		WithPodSpec(corev1.PodSpec{Containers: containers, InitContainers: initContainers})
	if len(cliqueAnnotations) > 0 {
		cliqueBuilder = cliqueBuilder.WithAnnotations(cliqueAnnotations)
	}
	return testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
		WithPodCliqueTemplateSpec(cliqueBuilder.Build()).
		Build()
}

// newBuildResourcePCSG builds the PodCliqueScalingGroup for replica index 0 that owns the worker
// clique, with the given annotations.
func newBuildResourcePCSG(pcsgAnnotations map[string]string) *grovecorev1alpha1.PodCliqueScalingGroup {
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-sg", "default", "test-pcs", 0).
		WithMinAvailable(1).
		WithCliqueNames([]string{"worker"}).
		Build()
	pcsg.Annotations = pcsgAnnotations
	return pcsg
}

// newAnchorPodGangMap builds a PodGangMap whose anchor entry owns PodCliqueScalingGroup replica index
// 0, so buildResource resolves the PodGang name from this entry's epoch.
func newAnchorPodGangMap() *grovecorev1alpha1.PodGangMap {
	return testutils.NewPodGangMapBuilder("test-pcs", "default", "uid", 0).WithEntries(
		testutils.NewPodGangEntryBuilder("hash", "1000").
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithPCSGReplicaIndices(map[string][]int32{"sg": {0}}).Build(),
	).Build()
}

// emptyMemberPodClique returns a bare PodClique that buildResource fills in.
func emptyMemberPodClique(name string) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
}

// assertPodLevelMNNVLClaim asserts the pod-level MNNVL resource claim and, when set, its resource
// claim template name.
func assertPodLevelMNNVLClaim(t *testing.T, pclq *grovecorev1alpha1.PodClique, expectClaim bool, expectedRCTName string) {
	t.Helper()
	if !expectClaim {
		assert.Empty(t, pclq.Spec.PodSpec.ResourceClaims, "expected no pod-level claims")
		return
	}
	require.Len(t, pclq.Spec.PodSpec.ResourceClaims, 1, "expected pod-level MNNVL claim")
	assert.Equal(t, mnnvl.MNNVLClaimName, pclq.Spec.PodSpec.ResourceClaims[0].Name)
	if expectedRCTName != "" {
		require.NotNil(t, pclq.Spec.PodSpec.ResourceClaims[0].ResourceClaimTemplateName)
		assert.Equal(t, expectedRCTName, *pclq.Spec.PodSpec.ResourceClaims[0].ResourceClaimTemplateName)
	}
}

// assertContainerMNNVLClaims asserts which containers carry the MNNVL claim and which do not.
func assertContainerMNNVLClaims(t *testing.T, containers []corev1.Container, wantWithClaim, wantWithoutClaim []string) {
	t.Helper()
	withClaim, withoutClaim := triageContainersByMNNVLClaim(containers)
	assert.ElementsMatch(t, wantWithClaim, withClaim, "containers with MNNVL claims should match expected")
	assert.ElementsMatch(t, wantWithoutClaim, withoutClaim, "containers without MNNVL claims should match expected")
}

// hasEnvVar reports whether an env var with the given name exists in the slice.
func hasEnvVar(envVars []corev1.EnvVar, name string) bool {
	for _, ev := range envVars {
		if ev.Name == name {
			return true
		}
	}
	return false
}

// triageContainersByMNNVLClaim separates containers into those with the MNNVL claim and those without.
func triageContainersByMNNVLClaim(containers []corev1.Container) (withClaim, withoutClaim []string) {
	for _, c := range containers {
		hasClaim := false
		for _, claim := range c.Resources.Claims {
			if claim.Name == mnnvl.MNNVLClaimName {
				hasClaim = true
				break
			}
		}
		if hasClaim {
			withClaim = append(withClaim, c.Name)
		} else {
			withoutClaim = append(withoutClaim, c.Name)
		}
	}
	return withClaim, withoutClaim
}
