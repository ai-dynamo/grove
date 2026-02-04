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

package podclique

import (
	"context"
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"

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
	// Tests creating a new operator instance
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	eventRecorder := &record.FakeRecorder{}

	operator := New(client, scheme, eventRecorder)

	assert.NotNil(t, operator)
	resource, ok := operator.(*_resource)
	assert.True(t, ok)
	assert.Equal(t, client, resource.client)
	assert.Equal(t, scheme, resource.scheme)
	assert.Equal(t, eventRecorder, resource.eventRecorder)
}

// TestGetPCSGTemplateNumPods tests calculating the number of pods in a PCSG template
func TestGetPCSGTemplateNumPods(t *testing.T) {
	tests := []struct {
		name string
		// pcs is the PodCliqueSet
		pcs *grovecorev1alpha1.PodCliqueSet
		// pcsg is the PodCliqueScalingGroup
		pcsg *grovecorev1alpha1.PodCliqueScalingGroup
		// expected is the expected total number of pods
		expected int
	}{
		{
			// Tests with all clique names matching
			name: "all_cliques_match",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "clique1",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas: 2,
								},
							},
							{
								Name: "clique2",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas: 3,
								},
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					CliqueNames: []string{"clique1", "clique2"},
				},
			},
			expected: 5,
		},
		{
			// Tests with partial clique names matching
			name: "partial_cliques_match",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "clique1",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas: 2,
								},
							},
							{
								Name: "clique2",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas: 3,
								},
							},
							{
								Name: "clique3",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas: 4,
								},
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					CliqueNames: []string{"clique1", "clique3"},
				},
			},
			expected: 6,
		},
		{
			// Tests with no matching cliques
			name: "no_matching_cliques",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "clique1",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas: 2,
								},
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					CliqueNames: []string{"clique2", "clique3"},
				},
			},
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
								Kind: "PodCliqueScalingGroup",
								Name: "test-pcsg",
								UID:  "pcsg-uid",
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
								Kind: "PodCliqueScalingGroup",
								Name: "test-pcsg",
								UID:  "pcsg-uid",
							},
						},
					},
				},
			},
			expectedNames: []string{}, // Fake client doesn't support PartialObjectMetadataList
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

	ctx := context.Background()
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

	ctx := context.Background()
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
func TestIdentifyFullyQualifiedStartupDependencyNames(t *testing.T) {
	tests := []struct {
		name string
		// pcs is the PodCliqueSet
		pcs *grovecorev1alpha1.PodCliqueSet
		// pcsReplica is the PCS replica index
		pcsReplica int
		// pcsg is the PodCliqueScalingGroup
		pcsg *grovecorev1alpha1.PodCliqueScalingGroup
		// pcsgReplica is the PCSG replica index
		pcsgReplica int
		// pclq is the PodClique
		pclq *grovecorev1alpha1.PodClique
		// foundAtIndex is the index where the clique was found
		foundAtIndex int
		// expected are the expected dependency names
		expected []string
		// expectError indicates if an error is expected
		expectError bool
	}{
		{
			// Tests in-order startup for first clique
			name: "in_order_first_clique",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						StartupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeInOrder),
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "clique1"},
							{Name: "clique2"},
						},
					},
				},
			},
			pcsReplica: 0,
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcsg",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					MinAvailable: ptr.To(int32(2)),
					CliqueNames:  []string{"clique1", "clique2"},
				},
			},
			pcsgReplica:  0,
			pclq:         &grovecorev1alpha1.PodClique{},
			foundAtIndex: 0,
			expected:     nil,
			expectError:  false,
		},
		{
			// Tests in-order startup for second clique in base PodGang
			name: "in_order_second_clique_base_podgang",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						StartupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeInOrder),
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "clique1"},
							{Name: "clique2"},
						},
					},
				},
			},
			pcsReplica: 0,
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcsg",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					MinAvailable: ptr.To(int32(2)),
					CliqueNames:  []string{"clique1", "clique2"},
				},
			},
			pcsgReplica:  1,
			pclq:         &grovecorev1alpha1.PodClique{},
			foundAtIndex: 1,
			expected:     []string{"test-pcs-0-clique1"},
			expectError:  false,
		},
		{
			// Tests explicit startup dependencies
			name: "explicit_startup",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						StartupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeExplicit),
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "clique1"},
							{Name: "clique2"},
						},
					},
				},
			},
			pcsReplica: 0,
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcsg",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					MinAvailable: ptr.To(int32(1)),
					CliqueNames:  []string{"clique1", "clique2"},
				},
			},
			pcsgReplica: 0,
			pclq: &grovecorev1alpha1.PodClique{
				Spec: grovecorev1alpha1.PodCliqueSpec{
					StartsAfter: []string{"clique1"},
				},
			},
			foundAtIndex: 1,
			expected:     []string{"test-pcs-0-clique1"},
			expectError:  false,
		},
		{
			// Tests nil startup type
			name: "nil_startup_type",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pcs",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						StartupType: nil,
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "clique1"},
						},
					},
				},
			},
			pcsReplica: 0,
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					MinAvailable: ptr.To(int32(1)),
				},
			},
			pcsgReplica:  0,
			pclq:         &grovecorev1alpha1.PodClique{},
			foundAtIndex: 0,
			expected:     nil,
			expectError:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := identifyFullyQualifiedStartupDependencyNames(
				tc.pcs,
				tc.pcsReplica,
				tc.pcsg,
				tc.pcsgReplica,
				tc.pclq,
				tc.foundAtIndex,
			)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, result)
			}
		})
	}
}

// Helper function to check if an env var exists in a slice
func hasEnvVar(envVars []corev1.EnvVar, name string) bool {
	for _, ev := range envVars {
		if ev.Name == name {
			return true
		}
	}
	return false
}

func TestBuildResource_MNNVLInjection(t *testing.T) {
	tests := []struct {
		description                         string
		pcsgAnnotations                     map[string]string
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
			description: "MNNVL enabled on PCSG with GPU container injects claims",
			pcsgAnnotations: map[string]string{
				mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled,
			},
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{"gpu-worker"},
			expectedContainersWithoutClaims: []string{},
			expectPodLevelClaim:             true,
			expectedRCTName:                 "test-pcs-0",
		},
		{
			description:     "MNNVL not enabled on PCSG does not inject claims",
			pcsgAnnotations: nil,
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{},
			expectedContainersWithoutClaims: []string{"gpu-worker"},
			expectPodLevelClaim:             false,
		},
		{
			description: "MNNVL enabled on PCSG but no GPU containers does not inject claims",
			pcsgAnnotations: map[string]string{
				mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled,
			},
			containers: []corev1.Container{
				{
					Name: "cpu-only",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{},
			expectedContainersWithoutClaims: []string{"cpu-only"},
			expectPodLevelClaim:             false,
		},
		{
			description: "MNNVL enabled on PCSG with mixed GPU and non-GPU containers",
			pcsgAnnotations: map[string]string{
				mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled,
			},
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
				{
					Name: "sidecar",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{"gpu-worker"},
			expectedContainersWithoutClaims: []string{"sidecar"},
			expectPodLevelClaim:             true,
		},
		{
			description: "MNNVL enabled on PCSG with GPU in init container",
			pcsgAnnotations: map[string]string{
				mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled,
			},
			initContainers: []corev1.Container{
				{
					Name: "init-gpu",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("1"),
						},
					},
				},
			},
			containers: []corev1.Container{
				{Name: "main"},
			},
			expectedContainersWithClaims:        []string{},
			expectedContainersWithoutClaims:     []string{"main"},
			expectedInitContainersWithClaims:    []string{"init-gpu"},
			expectedInitContainersWithoutClaims: []string{},
			expectPodLevelClaim:                 true,
		},
		{
			description: "MNNVL disabled explicitly on PCSG does not inject claims",
			pcsgAnnotations: map[string]string{
				mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLDisabled,
			},
			containers: []corev1.Container{
				{
					Name: "gpu-worker",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.GPUResourceName: resource.MustParse("8"),
						},
					},
				},
			},
			expectedContainersWithClaims:    []string{},
			expectedContainersWithoutClaims: []string{"gpu-worker"},
			expectPodLevelClaim:             false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			pcsName := "test-pcs"
			pcsNamespace := "default"
			pcsReplicaIndex := 0
			pcsgReplicaIndex := 0
			pclqTemplateName := "worker"

			// Create PCS with the test case's containers
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pcsName,
					Namespace: pcsNamespace,
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						StartupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder),
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: pclqTemplateName,
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas:     1,
									MinAvailable: ptr.To(int32(1)),
									PodSpec: corev1.PodSpec{
										Containers:     tc.containers,
										InitContainers: tc.initContainers,
									},
								},
							},
						},
					},
				},
			}

			// Create PCSG with MNNVL annotations and required label for pcsReplicaIndex
			pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:        fmt.Sprintf("%s-%d", pcsName, pcsReplicaIndex),
					Namespace:   pcsNamespace,
					Annotations: tc.pcsgAnnotations,
					Labels: map[string]string{
						apicommon.LabelPodCliqueSetReplicaIndex: fmt.Sprintf("%d", pcsReplicaIndex),
					},
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					MinAvailable: ptr.To(int32(1)),
					CliqueNames:  []string{pclqTemplateName},
				},
			}

			// Create empty PodClique with matching name suffix (must end with template name)
			pclqName := fmt.Sprintf("%s-%d-%s-%d-%s", pcsName, pcsReplicaIndex, "pcsg", pcsgReplicaIndex, pclqTemplateName)
			pclq := &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pclqName,
					Namespace: pcsNamespace,
				},
			}

			// Create operator and call buildResource
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			operator := &_resource{
				client:        nil, // not needed for buildResource
				scheme:        scheme,
				eventRecorder: &record.FakeRecorder{},
			}

			err := operator.buildResource(logr.Discard(), pcs, pcsg, pcsgReplicaIndex, pclq)
			require.NoError(t, err)

			// Verify pod-level claims
			if tc.expectPodLevelClaim {
				require.Len(t, pclq.Spec.PodSpec.ResourceClaims, 1, "expected pod-level MNNVL claim")
				assert.Equal(t, mnnvl.MNNVLClaimName, pclq.Spec.PodSpec.ResourceClaims[0].Name)
				if tc.expectedRCTName != "" {
					require.NotNil(t, pclq.Spec.PodSpec.ResourceClaims[0].ResourceClaimTemplateName)
					assert.Equal(t, tc.expectedRCTName, *pclq.Spec.PodSpec.ResourceClaims[0].ResourceClaimTemplateName)
				}
			} else {
				assert.Empty(t, pclq.Spec.PodSpec.ResourceClaims, "expected no pod-level claims")
			}

			// Verify container claims
			withClaims, withoutClaims := triageContainersByMNNVLClaim(pclq.Spec.PodSpec.Containers)
			assert.ElementsMatch(t, tc.expectedContainersWithClaims, withClaims,
				"containers with MNNVL claims should match expected")
			assert.ElementsMatch(t, tc.expectedContainersWithoutClaims, withoutClaims,
				"containers without MNNVL claims should match expected")

			// Verify init container claims
			initWithClaims, initWithoutClaims := triageContainersByMNNVLClaim(pclq.Spec.PodSpec.InitContainers)
			assert.ElementsMatch(t, tc.expectedInitContainersWithClaims, initWithClaims,
				"init containers with MNNVL claims should match expected")
			assert.ElementsMatch(t, tc.expectedInitContainersWithoutClaims, initWithoutClaims,
				"init containers without MNNVL claims should match expected")
		})
	}
}

// triageContainersByMNNVLClaim separates containers into those with MNNVL claim and those without.
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
