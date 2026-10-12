/*
Copyright 2025 The Grove Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pod

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/lpx"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func TestAddServiceAccountTokenSecretVolumeUsesShortSecretName(t *testing.T) {
	// 44 is the longest admitted PodCliqueSet name (the webhook caps combined
	// resource name length at 45). The shortened "-ic-sat" suffix must keep the
	// generated Secret name within the 63-byte label-value limit even at this bound.
	pcsName := strings.Repeat("a", 44)
	pod := &corev1.Pod{}

	addServiceAccountTokenSecretVolume(pcsName, pod)

	assert.Len(t, pod.Spec.Volumes, 1)
	volume := pod.Spec.Volumes[0]
	assert.Equal(t, serviceAccountTokenSecretVolumeName, volume.Name)
	if assert.NotNil(t, volume.Secret) {
		expectedSecretName := common.GenerateInitContainerSATokenSecretName(pcsName)
		assert.Equal(t, expectedSecretName, volume.Secret.SecretName)
		assert.NotEqual(t, common.GenerateLegacyInitContainerSATokenSecretName(pcsName), volume.Secret.SecretName)
		assert.LessOrEqual(t, len(expectedSecretName), 63)
	}
}

func TestBuildResourceWithLPXBackend(t *testing.T) {
	const (
		namespace   = "default"
		pcsName     = "model"
		cliqueName  = "gpu-worker"
		podGangName = "model-0"
		claimName   = "model-gpu-000"
	)

	uid := types.UID("test-uid")
	podSpec := corev1.PodSpec{
		SchedulerName: string(configv1alpha1.SchedulerNameLPX),
		Containers: []corev1.Container{{
			Name:  "worker",
			Image: "worker",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceName("lpu.nvidia.com/lpu"): resource.MustParse("1")},
			},
		}},
		ResourceClaims: []corev1.PodResourceClaim{{
			Name:              "gpu",
			ResourceClaimName: ptr.To(claimName),
		}},
	}
	pcs := testutils.NewPodCliqueSetBuilder(pcsName, namespace, uid).
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeInOrder)).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
				WithPodSpec(podSpec).
				Build(),
		).
		Build()
	pclq := testutils.NewPodCliqueBuilder(pcsName, uid, cliqueName, namespace, 0).Build()
	pclq.Spec.PodSpec = *podSpec.DeepCopy()
	pclq.Annotations = map[string]string{
		"example.com/source": "podclique",
		constants.AnnotationPodCliqueScalingGroupPodIndexOffset: "0",
	}

	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	registry := &testutils.FakeSchedulerRegistry{
		Backends: map[string]scheduler.Backend{
			string(configv1alpha1.SchedulerNameLPX): lpx.New(
				nil,
				configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX},
				testutils.NewFakeSchedulerBackend(string(configv1alpha1.SchedulerNameKai)),
			),
		},
		DefaultBackend: string(configv1alpha1.SchedulerNameLPX),
	}
	resource := &_resource{scheme: scheme, schedRegistry: registry}
	pod := &corev1.Pod{}

	require.NoError(t, resource.buildResource(pcs, pclq, podGangName, pod, 0, nil))

	assert.Equal(t, string(configv1alpha1.SchedulerNameLPX), pod.Spec.SchedulerName)
	assert.Equal(t, pclq.Name+"-", pod.GenerateName)
	assert.Equal(t, podGangName, pod.Labels[common.LabelPodGang])
	require.Len(t, pod.Spec.SchedulingGates, 1)
	assert.Equal(t, podGangSchedulingGate, pod.Spec.SchedulingGates[0].Name)
	require.Len(t, pod.Spec.ResourceClaims, 1)
	require.NotNil(t, pod.Spec.ResourceClaims[0].ResourceClaimName)
	assert.Equal(t, claimName, *pod.Spec.ResourceClaims[0].ResourceClaimName)
	assert.Equal(t, "podclique", pod.Annotations["example.com/source"])
	assert.NotContains(t, pod.Annotations, constants.AnnotationPodCliqueScalingGroupPodIndexOffset)

	pod.Annotations["example.com/source"] = "pod"
	assert.Equal(t, "podclique", pclq.Annotations["example.com/source"])
}

// TestGetSelectorLabelsForPods_PCSGOwnedPodClique tests that getSelectorLabelsForPods
// returns the correct selector labels for PCSG-owned PodCliques.
func TestGetSelectorLabelsForPods_PCSGOwnedPodClique(t *testing.T) {
	const (
		pcsName  = "workload1"
		pcsgName = "workload1-0-sg-x"
		pclqName = "workload1-0-sg-x-0-pc-a"
	)

	tests := []struct {
		name           string
		pclqObjectMeta metav1.ObjectMeta
		expectedLabels map[string]string
		description    string
	}{
		{
			name: "PCS-owned PodClique should use PCS name in selector",
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "workload1-0-pc-a",
				Namespace: "default",
				Labels: map[string]string{
					common.LabelPartOfKey:    pcsName,
					common.LabelManagedByKey: common.LabelManagedByValue,
					common.LabelPodClique:    "workload1-0-pc-a",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "grove.io/v1alpha1",
						Kind:       "PodCliqueSet",
						Name:       pcsName,
					},
				},
			},
			expectedLabels: map[string]string{
				common.LabelManagedByKey: common.LabelManagedByValue,
				common.LabelPartOfKey:    pcsName,
				common.LabelPodClique:    "workload1-0-pc-a",
			},
			description: "PCS-owned PodCliques work correctly (owner name == PCS name)",
		},
		{
			name: "PCSG-owned PodClique should use PCS name in selector (not PCSG name)",
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      pclqName,
				Namespace: "default",
				Labels: map[string]string{
					common.LabelPartOfKey:             pcsName,
					common.LabelManagedByKey:          common.LabelManagedByValue,
					common.LabelPodClique:             pclqName,
					common.LabelPodCliqueScalingGroup: pcsgName,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "grove.io/v1alpha1",
						Kind:       "PodCliqueScalingGroup",
						Name:       pcsgName,
					},
				},
			},
			expectedLabels: map[string]string{
				common.LabelManagedByKey: common.LabelManagedByValue,
				common.LabelPartOfKey:    pcsName,
				common.LabelPodClique:    pclqName,
			},
			description: "PCSG-owned PodCliques should use PCS name from labels, not owner reference",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actualLabels := getSelectorLabelsForPods(tt.pclqObjectMeta)

			// Check each expected label
			for key, expectedValue := range tt.expectedLabels {
				actualValue, exists := actualLabels[key]
				if !exists {
					t.Errorf("Expected label %q not found in selector", key)
					continue
				}
				if actualValue != expectedValue {
					t.Errorf("Label %q: expected %q, got %q. %s",
						key, expectedValue, actualValue, tt.description)
				}
			}

			// Specifically check the LabelPartOfKey
			if actualLabels[common.LabelPartOfKey] != tt.expectedLabels[common.LabelPartOfKey] {
				t.Errorf("LabelPartOfKey: expected %q, got %q",
					tt.expectedLabels[common.LabelPartOfKey],
					actualLabels[common.LabelPartOfKey])
			}
		})
	}
}

func TestAddEnvironmentVariables(t *testing.T) {
	tests := []struct {
		name              string
		pclq              *grovecorev1alpha1.PodClique
		expectedEnvVars   []string
		unexpectedEnvVars []string
	}{
		{
			name: "standalone PodClique",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
				constants.EnvVarPodIndex,
			},
			unexpectedEnvVars: []string{constants.EnvVarPodCliqueScalingGroupPodIndex},
		},
		{
			name: "PCSG member PodClique",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
					Labels: map[string]string{
						common.LabelPodCliqueScalingGroup: "test-pcsg",
					},
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
				constants.EnvVarPodIndex,
				constants.EnvVarPodCliqueScalingGroupPodIndex,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				Spec: tt.pclq.Spec.PodSpec,
			}

			addEnvironmentVariables(pod, tt.pclq, "test-pcs", 0)

			// Check that all containers have the expected environment variables
			for _, container := range pod.Spec.Containers {
				assertExpectedEnvVars(t, container, tt.expectedEnvVars)

				// Check unexpected environment variables are not present
				envVarNames := make(map[string]bool)
				for _, env := range container.Env {
					envVarNames[env.Name] = true
				}
				for _, unexpectedEnv := range tt.unexpectedEnvVars {
					if envVarNames[unexpectedEnv] {
						t.Errorf("unexpected environment variable %s found in container %s", unexpectedEnv, container.Name)
					}
				}

				// Verify Grove environment variables use direct values, except pod indices sourced from labels.
				directValueEnvVars := filterOutEnvVar(tt.expectedEnvVars, constants.EnvVarPodIndex)
				directValueEnvVars = filterOutEnvVar(directValueEnvVars, constants.EnvVarPodCliqueScalingGroupPodIndex)
				assertGroveEnvVarsDirectValues(t, container, directValueEnvVars)
				assertEnvVarUsesFieldRef(t, container, constants.EnvVarPodIndex, fmt.Sprintf("metadata.labels['%s']", common.LabelPodCliquePodIndex))
				if tt.pclq.Labels[common.LabelPodCliqueScalingGroup] != "" {
					assertEnvVarUsesFieldRef(t, container, constants.EnvVarPodCliqueScalingGroupPodIndex, fmt.Sprintf("metadata.labels['%s']", common.LabelPodCliqueScalingGroupPodIndex))
				}
			}
		})
	}
}

func TestGetPCSGPodIndex(t *testing.T) {
	t.Run("PCSG member", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				common.LabelPodCliqueScalingGroup: "test-pcs-0-engine",
			},
			Annotations: map[string]string{constants.AnnotationPodCliqueScalingGroupPodIndexOffset: "2"},
		}}

		index, err := getPCSGPodIndex(pclq, 1)
		require.NoError(t, err)
		require.NotNil(t, index)
		assert.Equal(t, 3, *index)
	})

	t.Run("standalone PodClique", func(t *testing.T) {
		index, err := getPCSGPodIndex(&grovecorev1alpha1.PodClique{}, 0)

		require.NoError(t, err)
		assert.Nil(t, index)
	})

	t.Run("PCSG member without offset", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
			common.LabelPodCliqueScalingGroup: "test-pcs-0-engine",
		}}}

		index, err := getPCSGPodIndex(pclq, 0)

		assert.Error(t, err)
		assert.Nil(t, index)
	})
}

func TestGetLabelsIncludesPCSGPodIndex(t *testing.T) {
	labels := getLabels(metav1.ObjectMeta{}, "test-pcs", "test-podgang", 0, 1, ptr.To(2))

	assert.Equal(t, "2", labels[common.LabelPodCliqueScalingGroupPodIndex])
}

func TestAddGroveEnvironmentVariables_NoDuplicates(t *testing.T) {
	tests := []struct {
		name            string
		pclq            *grovecorev1alpha1.PodClique
		existingEnvVars []corev1.EnvVar
		expectedEnvVars []string
		shouldReplace   map[string]string // env var name -> expected value
		shouldPreserve  []string          // env var names that should be preserved
	}{
		{
			name: "Container with existing Grove env vars - should replace",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{Name: "GROVE_PCS_NAME", Value: "old-pcs-name"},
									{Name: "GROVE_PCS_INDEX", Value: "old-index"},
								},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:  "test-init-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{Name: "GROVE_POD_INDEX", Value: "old-pod-index"},
								},
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
			},
			shouldReplace: map[string]string{
				"GROVE_PCS_NAME":  "test-pcs",
				"GROVE_PCS_INDEX": "0",
			},
		},
		{
			name: "Container with user env vars - should preserve",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{Name: "USER_VAR", Value: "user-value"},
									{Name: "CUSTOM_CONFIG", Value: "custom-value"},
								},
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
			},
			shouldPreserve: []string{"USER_VAR", "CUSTOM_CONFIG"},
		},
		{
			name: "Container with mixed env vars - should replace Grove, preserve user",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{Name: "USER_VAR", Value: "user-value"},
									{Name: "GROVE_PCS_NAME", Value: "old-pcs-name"},
									{Name: "CUSTOM_CONFIG", Value: "custom-value"},
								},
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
			},
			shouldReplace: map[string]string{
				"GROVE_PCS_NAME": "test-pcs",
			},
			shouldPreserve: []string{"USER_VAR", "CUSTOM_CONFIG"},
		},
		{
			name: "PCSG PodClique with existing env vars",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
					Labels: map[string]string{
						common.LabelPodCliqueScalingGroup: "test-pcsg",
					},
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{Name: "GROVE_PCSG_NAME", Value: "old-pcsg-name"},
									{Name: constants.EnvVarPodCliqueScalingGroupPodIndex, Value: "stale"},
									{Name: "USER_VAR", Value: "user-value"},
								},
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
				constants.EnvVarPodCliqueScalingGroupPodIndex,
			},
			shouldReplace:  map[string]string{},
			shouldPreserve: []string{"USER_VAR"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				Spec: tt.pclq.Spec.PodSpec,
			}

			addEnvironmentVariables(pod, tt.pclq, "test-pcs", 0)

			expectedPrefix := []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
				constants.EnvVarPodIndex,
			}
			if tt.pclq.Labels[common.LabelPodCliqueScalingGroup] != "" {
				expectedPrefix = append(expectedPrefix, constants.EnvVarPodCliqueScalingGroupPodIndex)
			}
			assertContainer := func(container corev1.Container) {
				assertExpectedEnvVars(t, container, tt.expectedEnvVars)
				assertReplacedEnvVars(t, container, tt.shouldReplace)
				assertPreservedEnvVars(t, container, tt.shouldPreserve)
				assertNoDuplicateEnvVars(t, container)
				require.GreaterOrEqual(t, len(container.Env), len(expectedPrefix))
				for i, envVarName := range expectedPrefix {
					assert.Equal(t, envVarName, container.Env[i].Name)
				}
				assertEnvVarUsesFieldRef(t, container, constants.EnvVarPodIndex, fmt.Sprintf("metadata.labels['%s']", common.LabelPodCliquePodIndex))
				if tt.pclq.Labels[common.LabelPodCliqueScalingGroup] != "" {
					assertEnvVarUsesFieldRef(t, container, constants.EnvVarPodCliqueScalingGroupPodIndex, fmt.Sprintf("metadata.labels['%s']", common.LabelPodCliqueScalingGroupPodIndex))
				}
			}
			for _, container := range pod.Spec.Containers {
				assertContainer(container)
			}
			for _, container := range pod.Spec.InitContainers {
				assertContainer(container)
			}
		})
	}
}

func TestAddGroveEnvironmentVariables_EmptyContainers(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pclq",
			Namespace: "test-ns",
		},
	}

	// Should not panic with empty containers
	addEnvironmentVariables(pod, pclq, "test-pcs", 0)
	assert.Empty(t, pod.Spec.Containers)
}

func TestAddGroveEnvironmentVariables_MultipleContainers(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "image1",
				},
				{
					Name:  "container2",
					Image: "image2",
					Env: []corev1.EnvVar{
						{Name: "EXISTING_VAR", Value: "existing-value"},
					},
				},
			},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pclq",
			Namespace: "test-ns",
		},
	}

	addEnvironmentVariables(pod, pclq, "test-pcs", 0)

	// Both containers should have Grove environment variables
	expectedEnvVars := []string{
		constants.EnvVarPodCliqueSetName,
		constants.EnvVarPodCliqueSetIndex,
		constants.EnvVarPodCliqueName,
		constants.EnvVarHeadlessService,
	}

	for _, container := range pod.Spec.Containers {
		assertExpectedEnvVars(t, container, expectedEnvVars)
		assertNoDuplicateEnvVars(t, container)
	}

	// Second container should preserve existing environment variable
	envVarNames := make(map[string]bool)
	for _, env := range pod.Spec.Containers[1].Env {
		envVarNames[env.Name] = true
	}
	assert.True(t, envVarNames["EXISTING_VAR"], "existing environment variable should be preserved")
}

// Helper functions

// Test_generateArgsForInitContainer_WaitsOnParentReplicaInOwnPodGang covers the resolution half of #873.
// The health-ordered drain selects the unavailable replica into the first anchor (its input and selection
// are covered by TestBuildAnchorBearingSubStepPicksWorstOffFirst). Given that committed anchor, which pairs
// prefill replica 1 with decode replica 0, decode-0 (which starts after prefill) must wait on prefill
// replica 1, the prefill replica in its own PodGang, and never on prefill replica 0, which the stale
// pre-update anchor still holds and this pod's init container can never observe.
func Test_generateArgsForInitContainer_WaitsOnParentReplicaInOwnPodGang(t *testing.T) {
	const (
		pcsName  = "ml"
		oldEpoch = "100"
		newEpoch = "200"
	)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				StartupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeExplicit),
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "pf", Spec: grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To(int32(1))}},
					{Name: "dc", Spec: grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To(int32(1)), StartsAfter: []string{"pf"}}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "prefill", CliqueNames: []string{"pf"}},
					{Name: "decode", CliqueNames: []string{"dc"}},
				},
			},
		},
	}
	rnr := common.ResourceNameReplica{Name: pcsName, Replica: 0}
	newAnchorGangName := common.GenerateAnchorPodGangName(rnr, newEpoch)
	// Mid coherent update: the old-generation anchor still holds the replicas that have not migrated, and
	// the new-generation anchor is the first the health-ordered drain produced, pairing the unavailable
	// prefill replica 1 with decode replica 0.
	pgm := &grovecorev1alpha1.PodGangMap{
		Spec: grovecorev1alpha1.PodGangMapSpec{
			Entries: []grovecorev1alpha1.PodGangEntry{
				{
					Epoch:                      oldEpoch,
					PodCliqueSetGenerationHash: "old-hash",
					Role:                       grovecorev1alpha1.PodGangEntryRoleAnchor,
					PCSGReplicaIndices:         map[string][]int32{"prefill": {0}, "decode": {1}},
				},
				{
					Epoch:                      newEpoch,
					PodCliqueSetGenerationHash: "new-hash",
					Role:                       grovecorev1alpha1.PodGangEntryRoleAnchor,
					PCSGReplicaIndices:         map[string][]int32{"prefill": {1}, "decode": {0}},
				},
			},
		},
	}
	// decode replica 0 on the new generation; its pod belongs to the new anchor PodGang. Its StartsAfter is
	// intentionally left empty: the operator derives the dependency from the PCS template, not this field.
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-0-decode-0-dc", pcsName),
			Labels: map[string]string{
				common.LabelPartOfKey:                         pcsName,
				common.LabelPodCliqueScalingGroup:             fmt.Sprintf("%s-0-decode", pcsName),
				common.LabelPodCliqueScalingGroupReplicaIndex: "0",
			},
		},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{common.LabelPodGang: newAnchorGangName}}}

	args, err := generateArgsForInitContainer(pcs, pclq, pod, pgm)
	require.NoError(t, err)
	assert.Equal(t, []string{fmt.Sprintf("--podcliques=%s-0-prefill-1-pf:1", pcsName)}, args)
}

// Test_generateArgsForInitContainer_IgnoresStalePreUpgradeStartsAfter verifies the resolver derives startup
// dependencies from the PodCliqueSet template, not from the PodClique's StartsAfter. An existing PCSG child
// created by an older operator still carries a resolved parent FQN in StartsAfter; after an upgrade the
// resolver must ignore that stale value and still emit the parent resolved against the pod's own gang.
func Test_generateArgsForInitContainer_IgnoresStalePreUpgradeStartsAfter(t *testing.T) {
	const (
		pcsName = "ml"
		epoch   = "100"
	)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				StartupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeExplicit),
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "pf", Spec: grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To(int32(1))}},
					{Name: "dc", Spec: grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To(int32(1)), StartsAfter: []string{"pf"}}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "prefill", CliqueNames: []string{"pf"}},
					{Name: "decode", CliqueNames: []string{"dc"}},
				},
			},
		},
	}
	rnr := common.ResourceNameReplica{Name: pcsName, Replica: 0}
	anchorGangName := common.GenerateAnchorPodGangName(rnr, epoch)
	pgm := &grovecorev1alpha1.PodGangMap{
		Spec: grovecorev1alpha1.PodGangMapSpec{
			Entries: []grovecorev1alpha1.PodGangEntry{
				{
					Epoch:                      epoch,
					PodCliqueSetGenerationHash: "hash",
					Role:                       grovecorev1alpha1.PodGangEntryRoleAnchor,
					PCSGReplicaIndices:         map[string][]int32{"prefill": {0}, "decode": {0}},
				},
			},
		},
	}
	// Existing PCSG child from an older operator: StartsAfter still holds a resolved parent FQN that is not a
	// clique name. If the resolver trusted it, StartupDependencyTargetsInEntry would match nothing and emit
	// no wait target, letting decode start before prefill.
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-0-decode-0-dc", pcsName),
			Labels: map[string]string{
				common.LabelPartOfKey:                         pcsName,
				common.LabelPodCliqueScalingGroup:             fmt.Sprintf("%s-0-decode", pcsName),
				common.LabelPodCliqueScalingGroupReplicaIndex: "0",
			},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{StartsAfter: []string{fmt.Sprintf("%s-0-prefill-1-pf", pcsName)}},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{common.LabelPodGang: anchorGangName}}}

	args, err := generateArgsForInitContainer(pcs, pclq, pod, pgm)
	require.NoError(t, err)
	assert.Equal(t, []string{fmt.Sprintf("--podcliques=%s-0-prefill-0-pf:1", pcsName)}, args)
}

func TestCliqueTemplateName(t *testing.T) {
	const pcsName = "ml"
	rnr := common.ResourceNameReplica{Name: pcsName, Replica: 0}
	tests := []struct {
		description string
		pclq        *grovecorev1alpha1.PodClique
		want        string
	}{
		{
			description: "standalone PodClique FQN yields the clique template name",
			pclq:        &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-0-frontend", pcsName)}},
			want:        "frontend",
		},
		{
			description: "PodCliqueScalingGroup member FQN yields the clique template name",
			pclq: &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-0-decode-0-dc", pcsName),
				Labels: map[string]string{
					common.LabelPodCliqueScalingGroup:             fmt.Sprintf("%s-0-decode", pcsName),
					common.LabelPodCliqueScalingGroupReplicaIndex: "0",
				},
			}},
			want: "dc",
		},
		{
			description: "PodCliqueScalingGroup member clique name containing hyphens is recovered intact",
			pclq: &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-0-decode-2-worker-a", pcsName),
				Labels: map[string]string{
					common.LabelPodCliqueScalingGroup:             fmt.Sprintf("%s-0-decode", pcsName),
					common.LabelPodCliqueScalingGroupReplicaIndex: "2",
				},
			}},
			want: "worker-a",
		},
	}
	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, cliqueTemplateName(tc.pclq, rnr))
		})
	}
}

// Test_generateArgsForInitContainer_BoundsStandaloneWaitToGangCommittedCount is the scale-in reproducer: a
// worker PCSG member starts after a standalone frontend with MinAvailable 2. A scale-in can leave the gang
// holding only one frontend pod, so the worker's init container must wait on just that one pod, not the full
// MinAvailable, or it would never finish starting since it watches only its own gang.
func Test_generateArgsForInitContainer_BoundsStandaloneWaitToGangCommittedCount(t *testing.T) {
	const (
		pcsName = "ml"
		epoch   = "100"
	)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				StartupType: ptr.To(grovecorev1alpha1.CliqueStartupTypeExplicit),
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To(int32(2))}},
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To(int32(1)), StartsAfter: []string{"frontend"}}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "decode", CliqueNames: []string{"worker"}},
				},
			},
		},
	}
	rnr := common.ResourceNameReplica{Name: pcsName, Replica: 0}
	anchorGangName := common.GenerateAnchorPodGangName(rnr, epoch)
	// A gang left with a single frontend pod after a scale-in, co-committing worker replica 1.
	pgm := &grovecorev1alpha1.PodGangMap{
		Spec: grovecorev1alpha1.PodGangMapSpec{
			Entries: []grovecorev1alpha1.PodGangEntry{
				{
					Epoch:                      epoch,
					PodCliqueSetGenerationHash: "hash",
					Role:                       grovecorev1alpha1.PodGangEntryRoleAnchor,
					PodCliques:                 map[string]int32{"frontend": 1},
					PCSGReplicaIndices:         map[string][]int32{"decode": {1}},
				},
			},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-0-decode-1-worker", pcsName),
			Labels: map[string]string{
				common.LabelPartOfKey:                         pcsName,
				common.LabelPodCliqueScalingGroup:             fmt.Sprintf("%s-0-decode", pcsName),
				common.LabelPodCliqueScalingGroupReplicaIndex: "1",
			},
		},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{common.LabelPodGang: anchorGangName}}}

	args, err := generateArgsForInitContainer(pcs, pclq, pod, pgm)
	require.NoError(t, err)
	assert.Equal(t, []string{fmt.Sprintf("--podcliques=%s-0-frontend:1", pcsName)}, args)
}

func Test_buildResource_AddsStartupInitContainerForInOrderStandalone(t *testing.T) {
	t.Setenv(envVarInitContainerImage, "registry:5001/grove-initc")
	const (
		pcsName   = "ml"
		namespace = "default"
		epoch     = "100"
	)
	uid := types.UID("test-uid")
	podSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "busybox"}}}
	pcs := testutils.NewPodCliqueSetBuilder(pcsName, namespace, uid).
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeInOrder)).
		WithStandaloneClique("leader").
		WithStandaloneClique("worker").
		Build()
	rnr := common.ResourceNameReplica{Name: pcsName, Replica: 0}
	anchorGangName := common.GenerateAnchorPodGangName(rnr, epoch)
	pgm := &grovecorev1alpha1.PodGangMap{
		Spec: grovecorev1alpha1.PodGangMapSpec{
			Entries: []grovecorev1alpha1.PodGangEntry{{
				Epoch:                      epoch,
				PodCliqueSetGenerationHash: "hash",
				Role:                       grovecorev1alpha1.PodGangEntryRoleAnchor,
				PodCliques:                 map[string]int32{"leader": 1, "worker": 1},
			}},
		},
	}
	pclq := testutils.NewPodCliqueBuilder(pcsName, uid, "worker", namespace, 0).Build()
	pclq.Spec.PodSpec = *podSpec.DeepCopy()

	resource := newInitContainerTestResource(t)
	pod := &corev1.Pod{}
	require.NoError(t, resource.buildResource(pcs, pclq, anchorGangName, pod, 0, pgm))

	require.Len(t, pod.Spec.InitContainers, 1)
	assert.Equal(t, initContainerName, pod.Spec.InitContainers[0].Name)
	assert.Equal(t, []string{fmt.Sprintf("--podcliques=%s-0-leader:1", pcsName)}, pod.Spec.InitContainers[0].Args)
}

func Test_buildResource_AddsStartupInitContainerForInOrderPCSGMember(t *testing.T) {
	t.Setenv(envVarInitContainerImage, "registry:5001/grove-initc")
	const (
		pcsName   = "ml"
		namespace = "default"
		epoch     = "100"
	)
	uid := types.UID("test-uid")
	podSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "busybox"}}}
	pcs := testutils.NewPodCliqueSetBuilder(pcsName, namespace, uid).
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeInOrder)).
		WithStandaloneClique("leader").
		WithScalingGroupConfig("decode", []string{"worker"}, 1, 1).
		Build()
	rnr := common.ResourceNameReplica{Name: pcsName, Replica: 0}
	anchorGangName := common.GenerateAnchorPodGangName(rnr, epoch)
	pgm := &grovecorev1alpha1.PodGangMap{
		Spec: grovecorev1alpha1.PodGangMapSpec{
			Entries: []grovecorev1alpha1.PodGangEntry{{
				Epoch:                      epoch,
				PodCliqueSetGenerationHash: "hash",
				Role:                       grovecorev1alpha1.PodGangEntryRoleAnchor,
				PodCliques:                 map[string]int32{"leader": 1},
				PCSGReplicaIndices:         map[string][]int32{"decode": {0}},
			}},
		},
	}
	pclq := testutils.NewPCSGPodCliqueBuilder(fmt.Sprintf("%s-0-decode-0-worker", pcsName), namespace, pcsName, fmt.Sprintf("%s-0-decode", pcsName), 0, 0).Build()
	pclq.Spec.PodSpec = *podSpec.DeepCopy()
	pclq.Annotations = map[string]string{constants.AnnotationPodCliqueScalingGroupPodIndexOffset: "0"}

	resource := newInitContainerTestResource(t)
	pod := &corev1.Pod{}
	require.NoError(t, resource.buildResource(pcs, pclq, anchorGangName, pod, 0, pgm))

	require.Len(t, pod.Spec.InitContainers, 1)
	assert.Equal(t, initContainerName, pod.Spec.InitContainers[0].Name)
	assert.Equal(t, []string{fmt.Sprintf("--podcliques=%s-0-leader:1", pcsName)}, pod.Spec.InitContainers[0].Args)
}

// -------------------------------------------------------------------------------------------

// assertExpectedEnvVars asserts that the expected environment variables are present.
func assertExpectedEnvVars(t *testing.T, container corev1.Container, expectedEnvVars []string) {
	envVarNames := make(map[string]bool)
	for _, env := range container.Env {
		envVarNames[env.Name] = true
	}
	for _, expectedEnv := range expectedEnvVars {
		assert.True(t, envVarNames[expectedEnv], "expected environment variable %s not found in container %s", expectedEnv, container.Name)
	}
}

// assertGroveEnvVarsDirectValues asserts Grove environment variables have direct values.
func assertGroveEnvVarsDirectValues(t *testing.T, container corev1.Container, groveEnvVars []string) {
	// Create a set of Grove env vars for quick lookup
	groveEnvVarSet := make(map[string]bool)
	for _, envVar := range groveEnvVars {
		groveEnvVarSet[envVar] = true
	}

	for _, env := range container.Env {
		// Only validate Grove environment variables
		if groveEnvVarSet[env.Name] {
			assert.NotEmpty(t, env.Value, "Grove environment variable %s should have a direct value", env.Name)
			assert.Nil(t, env.ValueFrom, "Grove environment variable %s should not use ValueFrom (Downward API)", env.Name)
		}
	}
}

// Helper function to assert replaced environment variables use correct Downward API
func assertReplacedEnvVars(t *testing.T, container corev1.Container, shouldReplace map[string]string) {
	if shouldReplace == nil {
		return
	}

	envVarNames := make(map[string]corev1.EnvVar)
	for _, env := range container.Env {
		envVarNames[env.Name] = env
	}

	for envName, expectedValue := range shouldReplace {
		envVar, found := envVarNames[envName]
		assert.True(t, found, "environment variable %s should exist", envName)
		if found {
			assert.Equal(t, expectedValue, envVar.Value,
				"environment variable %s has wrong value", envName)
		}
	}
}

// Helper function to assert preserved environment variables maintain their values
func assertPreservedEnvVars(t *testing.T, container corev1.Container, shouldPreserve []string) {
	if shouldPreserve == nil {
		return
	}

	envVarNames := make(map[string]corev1.EnvVar)
	for _, env := range container.Env {
		envVarNames[env.Name] = env
	}

	for _, preserveEnv := range shouldPreserve {
		envVar, found := envVarNames[preserveEnv]
		assert.True(t, found, "expected preserved environment variable %s not found in container %s", preserveEnv, container.Name)
		if found {
			assert.NotEmpty(t, envVar.Value, "preserved environment variable %s should have its original value", preserveEnv)
		}
	}
}

// Helper function to assert no duplicate environment variables
func assertNoDuplicateEnvVars(t *testing.T, container corev1.Container) {
	envVarCounts := make(map[string]int)
	for _, env := range container.Env {
		envVarCounts[env.Name]++
	}
	for envName, count := range envVarCounts {
		assert.Equal(t, 1, count, "environment variable %s appears %d times (should be 1)", envName, count)
	}
}

func assertEnvVarUsesFieldRef(t *testing.T, container corev1.Container, envVarName, expectedFieldPath string) {
	for _, env := range container.Env {
		if env.Name == envVarName {
			assert.Empty(t, env.Value, "environment variable %s should not have a direct value", envVarName)
			if assert.NotNil(t, env.ValueFrom, "environment variable %s should use ValueFrom", envVarName) {
				if assert.NotNil(t, env.ValueFrom.FieldRef, "environment variable %s should use FieldRef", envVarName) {
					assert.Equal(t, expectedFieldPath, env.ValueFrom.FieldRef.FieldPath,
						"environment variable %s has wrong FieldPath", envVarName)
				}
			}
			return
		}
	}
	t.Errorf("environment variable %s not found in container %s", envVarName, container.Name)
}

func filterOutEnvVar(envVars []string, exclude string) []string {
	var result []string
	for _, v := range envVars {
		if v != exclude {
			result = append(result, v)
		}
	}
	return result
}

// newInitContainerTestResource builds a _resource with a scheme and a fake scheduler registry for exercising
// buildResource in init-container tests.
func newInitContainerTestResource(t *testing.T) *_resource {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	registry := &testutils.FakeSchedulerRegistry{
		Backends: map[string]scheduler.Backend{
			string(configv1alpha1.SchedulerNameLPX): lpx.New(
				nil,
				configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX},
				testutils.NewFakeSchedulerBackend(string(configv1alpha1.SchedulerNameKai)),
			),
		},
		DefaultBackend: string(configv1alpha1.SchedulerNameLPX),
	}
	return &_resource{scheme: scheme, schedRegistry: registry}
}
