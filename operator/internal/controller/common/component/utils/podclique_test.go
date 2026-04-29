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

package utils

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestGetPCLQsByOwner tests the GetPCLQsByOwner function
func TestGetPCLQsByOwner(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// ownerKind is the kind of the owner
		ownerKind string
		// ownerObjectKey is the owner's object key
		ownerObjectKey client.ObjectKey
		// selectorLabels are the labels to match
		selectorLabels map[string]string
		// existingPCLQs are the existing PodCliques
		existingPCLQs []grovecorev1alpha1.PodClique
		// expectedPCLQs are the expected PodCliques
		expectedPCLQs []string
		// expectError indicates if an error is expected
		expectError bool
	}{
		{
			// Tests finding PodCliques owned by a PodCliqueSet
			name:      "finds_owned_podcliques",
			ownerKind: "PodCliqueSet",
			ownerObjectKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			selectorLabels: map[string]string{
				apicommon.LabelPartOfKey: "test-pcs",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-1",
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
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-2",
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
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-pclq",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "other-pcs",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "other-pcs",
							},
						},
					},
				},
			},
			expectedPCLQs: []string{"test-pclq-1", "test-pclq-2"},
			expectError:   false,
		},
		{
			// Tests when no PodCliques match the owner
			name:      "no_matching_owner",
			ownerKind: "PodCliqueSet",
			ownerObjectKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			selectorLabels: map[string]string{
				apicommon.LabelPartOfKey: "test-pcs",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "PodCliqueSet",
								Name: "other-pcs",
							},
						},
					},
				},
			},
			expectedPCLQs: []string{},
			expectError:   false,
		},
		{
			// Tests when PodCliques have no owner references
			name:      "no_owner_references",
			ownerKind: "PodCliqueSet",
			ownerObjectKey: client.ObjectKey{
				Name:      "test-pcs",
				Namespace: "default",
			},
			selectorLabels: map[string]string{
				apicommon.LabelPartOfKey: "test-pcs",
			},
			existingPCLQs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pclq-1",
						Namespace: "default",
						Labels: map[string]string{
							apicommon.LabelPartOfKey: "test-pcs",
						},
					},
				},
			},
			expectedPCLQs: []string{},
			expectError:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup scheme
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			// Build runtime objects
			runtimeObjs := []runtime.Object{}
			for i := range tc.existingPCLQs {
				runtimeObjs = append(runtimeObjs, &tc.existingPCLQs[i])
			}

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(runtimeObjs...).
				Build()

			// Call function
			ctx := context.Background()
			pclqs, err := GetPCLQsByOwner(ctx, fakeClient, tc.ownerKind, tc.ownerObjectKey, tc.selectorLabels)

			// Verify results
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(tc.expectedPCLQs), len(pclqs))
				for i, pclq := range pclqs {
					assert.Equal(t, tc.expectedPCLQs[i], pclq.Name)
				}
			}
		})
	}
}

// TestGroupPCLQsByPodGangName tests the GroupPCLQsByPodGangName function
func TestGroupPCLQsByPodGangName(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pclqs is the list of PodCliques to group
		pclqs []grovecorev1alpha1.PodClique
		// expected is the expected grouping
		expected map[string][]grovecorev1alpha1.PodClique
	}{
		{
			// Tests grouping PodCliques by PodGang name
			name: "groups_by_podgang_name",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-1",
						Labels: map[string]string{
							apicommon.LabelPodGang: "podgang-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-2",
						Labels: map[string]string{
							apicommon.LabelPodGang: "podgang-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pclq-3",
						Labels: map[string]string{
							apicommon.LabelPodGang: "podgang-2",
						},
					},
				},
			},
			expected: map[string][]grovecorev1alpha1.PodClique{
				"podgang-1": {
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "pclq-1",
							Labels: map[string]string{
								apicommon.LabelPodGang: "podgang-1",
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "pclq-2",
							Labels: map[string]string{
								apicommon.LabelPodGang: "podgang-1",
							},
						},
					},
				},
				"podgang-2": {
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "pclq-3",
							Labels: map[string]string{
								apicommon.LabelPodGang: "podgang-2",
							},
						},
					},
				},
			},
		},
		{
			// Tests with empty list
			name:     "empty_list",
			pclqs:    []grovecorev1alpha1.PodClique{},
			expected: map[string][]grovecorev1alpha1.PodClique{},
		},
		{
			// Tests with PodCliques missing PodGang label
			name: "missing_podgang_label",
			pclqs: []grovecorev1alpha1.PodClique{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "pclq-1",
						Labels: map[string]string{},
					},
				},
			},
			expected: map[string][]grovecorev1alpha1.PodClique{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GroupPCLQsByPodGangName(tc.pclqs)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestIsPCLQAutoUpdateInProgress tests the IsPCLQAutoUpdateInProgress function
func TestIsPCLQAutoUpdateInProgress(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pclq is the PodClique to check
		pclq *grovecorev1alpha1.PodClique
		// expected is the expected result
		expected bool
	}{
		{
			// Tests when no rolling update progress exists
			name: "no_rolling_update_progress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			// Tests when rolling update is in progress
			name: "update_in_progress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt: metav1.Now(),
					},
				},
			},
			expected: true,
		},
		{
			// Tests when rolling update is completed
			name: "update_completed",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   &metav1.Time{Time: metav1.Now().Time},
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsPCLQAutoUpdateInProgress(tc.pclq)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestIsLastPCLQUpdateCompleted tests the IsLastPCLQUpdateCompleted function
func TestIsLastPCLQUpdateCompleted(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pclq is the PodClique to check
		pclq *grovecorev1alpha1.PodClique
		// expected is the expected result
		expected bool
	}{
		{
			// Tests when no rolling update progress exists
			name: "no_rolling_update_progress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			// Tests when rolling update is in progress
			name: "update_in_progress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt: metav1.Now(),
					},
				},
			},
			expected: false,
		},
		{
			// Tests when rolling update is completed
			name: "update_completed",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   &metav1.Time{Time: metav1.Now().Time},
					},
				},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsLastPCLQUpdateCompleted(tc.pclq)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestComputePCLQPodTemplateHash_PodSpecSliceOrderInvariants pins the
// per-PCLQ pod-template hash behavior with respect to slice order in the
// embedded PodSpec.
//
// The fix at internal/utils/kubernetes/pod.go canonicalizes name-keyed
// (+listType=map) slices before hashing so that two PodSpecs that represent
// the same desired state always produce the same hash, regardless of how
// the upstream controller happened to serialize the slices.
//
// Slices that are +listType=atomic (Env, VolumeMounts, ...) and whose order
// has runtime semantic meaning (Env participates in $(VAR) substitution)
// remain order-sensitive. Reordering them is a real spec change and must
// continue to flip the hash.
func TestComputePCLQPodTemplateHash_PodSpecSliceOrderInvariants(t *testing.T) {
	envByName := map[string]corev1.EnvVar{
		"FOO":   {Name: "FOO", Value: "foo-value"},
		"BAR":   {Name: "BAR", Value: "bar-value"},
		"BAZ":   {Name: "BAZ", Value: "baz-value"},
		"QUX":   {Name: "QUX", Value: "qux-value"},
		"QUUX":  {Name: "QUUX", Value: "quux-value"},
		"CORGE": {Name: "CORGE", Value: "corge-value"},
	}
	makeContainerWithEnv := func(name string, envOrder []string) corev1.Container {
		envs := make([]corev1.EnvVar, 0, len(envOrder))
		for _, n := range envOrder {
			envs = append(envs, envByName[n])
		}
		return corev1.Container{
			Name:  name,
			Image: name + ":latest",
			Env:   envs,
		}
	}
	makeTemplate := func(spec corev1.PodSpec) *grovecorev1alpha1.PodCliqueTemplateSpec {
		return &grovecorev1alpha1.PodCliqueTemplateSpec{
			Name: "worker",
			Spec: grovecorev1alpha1.PodCliqueSpec{
				PodSpec: spec,
			},
		}
	}

	t.Run("container_reorder_does_not_change_hash", func(t *testing.T) {
		envs := []string{"BAR", "BAZ", "CORGE", "FOO", "QUUX", "QUX"}
		main := makeContainerWithEnv("main", envs)
		side := corev1.Container{Name: "sidecar", Image: "sidecar:latest"}

		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{Containers: []corev1.Container{main, side}}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{Containers: []corev1.Container{side, main}}), "")

		assert.Equal(t, hashA, hashB,
			"Containers is +listType=map keyed by name — order must not affect the hash")
	})

	t.Run("volume_reorder_does_not_change_hash", func(t *testing.T) {
		volA := corev1.Volume{Name: "model-cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
		volB := corev1.Volume{Name: "shared-memory", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
		volC := corev1.Volume{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}

		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main"}},
			Volumes:    []corev1.Volume{volA, volB, volC},
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main"}},
			Volumes:    []corev1.Volume{volC, volA, volB},
		}), "")

		assert.Equal(t, hashA, hashB,
			"Volumes is +listType=map keyed by name — order must not affect the hash")
	})

	t.Run("image_pull_secret_reorder_does_not_change_hash", func(t *testing.T) {
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main"}},
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "docker-imagepullsecret"},
				{Name: "nvcr-imagepullsecret"},
			},
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main"}},
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "nvcr-imagepullsecret"},
				{Name: "docker-imagepullsecret"},
			},
		}), "")

		assert.Equal(t, hashA, hashB,
			"ImagePullSecrets is +listType=map keyed by name — order must not affect the hash")
	})

	t.Run("container_port_reorder_does_not_change_hash", func(t *testing.T) {
		makeWithPorts := func(ports []corev1.ContainerPort) *grovecorev1alpha1.PodCliqueTemplateSpec {
			return makeTemplate(corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Image: "main:latest", Ports: ports}},
			})
		}
		hashA := ComputePCLQPodTemplateHash(makeWithPorts([]corev1.ContainerPort{
			{Name: "http", ContainerPort: 8000, Protocol: corev1.ProtocolTCP},
			{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeWithPorts([]corev1.ContainerPort{
			{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
			{Name: "http", ContainerPort: 8000, Protocol: corev1.ProtocolTCP},
		}), "")

		assert.Equal(t, hashA, hashB,
			"Container.Ports is +listType=map keyed by (containerPort, protocol) — order must not affect the hash")
	})

	t.Run("env_var_reorder_changes_hash_atomic_listtype", func(t *testing.T) {
		envAlpha := []string{"BAR", "BAZ", "CORGE", "FOO", "QUUX", "QUX"}
		envShuffled := []string{"FOO", "CORGE", "BAR", "QUUX", "BAZ", "QUX"}

		hashAlpha := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{makeContainerWithEnv("main", envAlpha)},
		}), "")
		hashShuffled := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{makeContainerWithEnv("main", envShuffled)},
		}), "")

		assert.NotEqual(t, hashAlpha, hashShuffled,
			"Container.Env is +listType=atomic and order participates in $(VAR) substitution — reorder is a real spec change and must change the hash")
	})

	t.Run("init_container_reorder_changes_hash", func(t *testing.T) {
		initA := corev1.Container{Name: "init-a", Image: "init-a:latest"}
		initB := corev1.Container{Name: "init-b", Image: "init-b:latest"}

		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "main"}},
			InitContainers: []corev1.Container{initA, initB},
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "main"}},
			InitContainers: []corev1.Container{initB, initA},
		}), "")

		assert.NotEqual(t, hashA, hashB,
			"InitContainers run in slice order — reorder is a real spec change and must change the hash")
	})

	t.Run("resize_policy_reorder_changes_hash_atomic_listtype", func(t *testing.T) {
		rpCPU := corev1.ContainerResizePolicy{ResourceName: corev1.ResourceCPU, RestartPolicy: corev1.NotRequired}
		rpMem := corev1.ContainerResizePolicy{ResourceName: corev1.ResourceMemory, RestartPolicy: corev1.RestartContainer}

		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "main:v1", ResizePolicy: []corev1.ContainerResizePolicy{rpCPU, rpMem}}},
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "main:v1", ResizePolicy: []corev1.ContainerResizePolicy{rpMem, rpCPU}}},
		}), "")

		assert.NotEqual(t, hashA, hashB,
			"Container.ResizePolicy is +listType=atomic — reorder is a real spec change and must change the hash")
	})

	t.Run("identical_input_produces_identical_hash", func(t *testing.T) {
		envs := []string{"BAR", "BAZ", "CORGE", "FOO", "QUUX", "QUX"}
		containers := []corev1.Container{makeContainerWithEnv("main", envs), {Name: "sidecar", Image: "sidecar:latest"}}

		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{Containers: containers}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{Containers: containers}), "")

		assert.Equal(t, hashA, hashB, "sanity: identical input must produce identical hash")
	})
}

// TestComputePCLQPodTemplateHash_RealisticDynamoLikePodSpec is the
// regression test that maps directly back to the latency-mode bug. It
// builds a PodSpec mirroring what the Dynamo operator emits for a vLLM
// prefill worker (multiple containers, many env vars, multiple volumes,
// multiple imagePullSecrets, multiple ports), then permutes every
// +listType=map slice across two builds. The hash must be stable.
//
// Without the canonicalization fix in
// internal/utils/kubernetes/pod.go this test would fail and the gang would
// roll on every Dynamo operator-driven PCS update.
func TestComputePCLQPodTemplateHash_RealisticDynamoLikePodSpec(t *testing.T) {
	mainContainer := func(envs []corev1.EnvVar, ports []corev1.ContainerPort, volumeMounts []corev1.VolumeMount) corev1.Container {
		return corev1.Container{
			Name:         "main",
			Image:        "nvcr.io/nvstaging/ai-dynamo/vllm-runtime:1.1.0-rc5",
			Env:          envs,
			Ports:        ports,
			VolumeMounts: volumeMounts,
		}
	}
	volumeMountsAlpha := []corev1.VolumeMount{
		{Name: "model-cache", MountPath: "/opt/model-cache"},
		{Name: "shared-memory", MountPath: "/dev/shm"},
	}
	volumeMountsShuffled := []corev1.VolumeMount{volumeMountsAlpha[1], volumeMountsAlpha[0]}

	envsAlpha := []corev1.EnvVar{
		{Name: "DYN_FORWARDPASS_METRIC_PORT", Value: "8081"},
		{Name: "HF_HOME", Value: "/opt/model-cache"},
		{Name: "NATS_SERVER", Value: "nats://dynamo-platform-nats.dynamo-system.svc.cluster.local:4222"},
	}
	portsAlpha := []corev1.ContainerPort{
		{Name: "http", ContainerPort: 8000, Protocol: corev1.ProtocolTCP},
		{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
	}

	templateAsEmittedFirst := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: "vllmprefillworker",
		Spec: grovecorev1alpha1.PodCliqueSpec{
			PodSpec: corev1.PodSpec{
				ImagePullSecrets: []corev1.LocalObjectReference{
					{Name: "docker-imagepullsecret"},
					{Name: "nvcr-imagepullsecret"},
				},
				Volumes: []corev1.Volume{
					{Name: "model-cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					{Name: "shared-memory", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				},
				Containers: []corev1.Container{
					mainContainer(envsAlpha, portsAlpha, volumeMountsAlpha),
					{Name: "sidecar", Image: "sidecar:latest"},
				},
			},
		},
	}

	// templateAsEmittedSecond is the SAME desired state but with every
	// +listType=map slice in a different order. This mimics the
	// Dynamo-operator scenario where Go map iteration produces a different
	// sequence on each PCS write.
	portsShuffled := []corev1.ContainerPort{portsAlpha[1], portsAlpha[0]}
	templateAsEmittedSecond := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: "vllmprefillworker",
		Spec: grovecorev1alpha1.PodCliqueSpec{
			PodSpec: corev1.PodSpec{
				ImagePullSecrets: []corev1.LocalObjectReference{
					{Name: "nvcr-imagepullsecret"},
					{Name: "docker-imagepullsecret"},
				},
				Volumes: []corev1.Volume{
					{Name: "shared-memory", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					{Name: "model-cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				},
				Containers: []corev1.Container{
					{Name: "sidecar", Image: "sidecar:latest"},
					mainContainer(envsAlpha, portsShuffled, volumeMountsShuffled),
				},
			},
		},
	}

	hashFirst := ComputePCLQPodTemplateHash(templateAsEmittedFirst, "")
	hashSecond := ComputePCLQPodTemplateHash(templateAsEmittedSecond, "")
	assert.Equal(t, hashFirst, hashSecond,
		"two PodSpecs that represent the same desired state but differ only in +listType=map slice order must hash identically; otherwise the gang rolls on every operator reconcile")
}

// TestComputePCLQPodTemplateHash_AdditionalListTypeMapSlices covers the
// +listType=map slices that aren't exercised by the older invariants test:
// VolumeMounts, VolumeDevices, HostAliases, TopologySpreadConstraints,
// ResourceClaims, SchedulingGates, Container.Resources.Claims, and
// EphemeralContainers. These were called out in the bug-report analysis as
// additional places where non-deterministic upstream serialization could flip
// the per-PCLQ hash.
func TestComputePCLQPodTemplateHash_AdditionalListTypeMapSlices(t *testing.T) {
	makeTemplate := func(spec corev1.PodSpec) *grovecorev1alpha1.PodCliqueTemplateSpec {
		return &grovecorev1alpha1.PodCliqueTemplateSpec{
			Name: "worker",
			Spec: grovecorev1alpha1.PodCliqueSpec{PodSpec: spec},
		}
	}

	t.Run("volume_mount_reorder_does_not_change_hash", func(t *testing.T) {
		mounts := []corev1.VolumeMount{
			{Name: "model-cache", MountPath: "/opt/model-cache"},
			{Name: "shared-memory", MountPath: "/dev/shm"},
		}
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "main:v1", VolumeMounts: mounts}},
		}), "")
		shuffled := []corev1.VolumeMount{mounts[1], mounts[0]}
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "main:v1", VolumeMounts: shuffled}},
		}), "")
		assert.Equal(t, hashA, hashB,
			"Container.VolumeMounts order must not affect the per-PCLQ hash — this is the explicit case from the bug report")
	})

	t.Run("volume_device_reorder_does_not_change_hash", func(t *testing.T) {
		devs := []corev1.VolumeDevice{
			{Name: "raw-a", DevicePath: "/dev/xvda"},
			{Name: "raw-b", DevicePath: "/dev/xvdb"},
		}
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "main:v1", VolumeDevices: devs}},
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "main:v1", VolumeDevices: []corev1.VolumeDevice{devs[1], devs[0]}}},
		}), "")
		assert.Equal(t, hashA, hashB, "Container.VolumeDevices order must not affect the per-PCLQ hash")
	})

	t.Run("host_alias_reorder_does_not_change_hash", func(t *testing.T) {
		aliases := []corev1.HostAlias{
			{IP: "10.0.0.1", Hostnames: []string{"a"}},
			{IP: "10.0.0.2", Hostnames: []string{"b"}},
		}
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:  []corev1.Container{{Name: "main"}},
			HostAliases: aliases,
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:  []corev1.Container{{Name: "main"}},
			HostAliases: []corev1.HostAlias{aliases[1], aliases[0]},
		}), "")
		assert.Equal(t, hashA, hashB, "PodSpec.HostAliases order must not affect the per-PCLQ hash")
	})

	t.Run("topology_spread_constraint_reorder_does_not_change_hash", func(t *testing.T) {
		tsc := []corev1.TopologySpreadConstraint{
			{MaxSkew: 1, TopologyKey: "topology.kubernetes.io/zone", WhenUnsatisfiable: corev1.DoNotSchedule},
			{MaxSkew: 1, TopologyKey: "kubernetes.io/hostname", WhenUnsatisfiable: corev1.ScheduleAnyway},
		}
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:                []corev1.Container{{Name: "main"}},
			TopologySpreadConstraints: tsc,
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:                []corev1.Container{{Name: "main"}},
			TopologySpreadConstraints: []corev1.TopologySpreadConstraint{tsc[1], tsc[0]},
		}), "")
		assert.Equal(t, hashA, hashB, "PodSpec.TopologySpreadConstraints order must not affect the per-PCLQ hash")
	})

	t.Run("resource_claim_reorder_does_not_change_hash", func(t *testing.T) {
		claims := []corev1.PodResourceClaim{{Name: "claim-a"}, {Name: "claim-b"}}
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "main"}},
			ResourceClaims: claims,
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "main"}},
			ResourceClaims: []corev1.PodResourceClaim{claims[1], claims[0]},
		}), "")
		assert.Equal(t, hashA, hashB, "PodSpec.ResourceClaims order must not affect the per-PCLQ hash")
	})

	t.Run("scheduling_gate_reorder_does_not_change_hash", func(t *testing.T) {
		gates := []corev1.PodSchedulingGate{{Name: "gate-a"}, {Name: "gate-b"}}
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:      []corev1.Container{{Name: "main"}},
			SchedulingGates: gates,
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:      []corev1.Container{{Name: "main"}},
			SchedulingGates: []corev1.PodSchedulingGate{gates[1], gates[0]},
		}), "")
		assert.Equal(t, hashA, hashB, "PodSpec.SchedulingGates order must not affect the per-PCLQ hash")
	})

	t.Run("container_resource_claim_reorder_does_not_change_hash", func(t *testing.T) {
		claims := []corev1.ResourceClaim{{Name: "claim-a"}, {Name: "claim-b"}}
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "main:v1", Resources: corev1.ResourceRequirements{Claims: claims}}},
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "main:v1", Resources: corev1.ResourceRequirements{Claims: []corev1.ResourceClaim{claims[1], claims[0]}}}},
		}), "")
		assert.Equal(t, hashA, hashB, "Container.Resources.Claims order must not affect the per-PCLQ hash")
	})

	t.Run("ephemeral_container_reorder_does_not_change_hash", func(t *testing.T) {
		ec := []corev1.EphemeralContainer{
			{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug-a", Image: "busybox"}},
			{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug-b", Image: "busybox"}},
		}
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:          []corev1.Container{{Name: "main"}},
			EphemeralContainers: ec,
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:          []corev1.Container{{Name: "main"}},
			EphemeralContainers: []corev1.EphemeralContainer{ec[1], ec[0]},
		}), "")
		assert.Equal(t, hashA, hashB, "PodSpec.EphemeralContainers order must not affect the per-PCLQ hash")
	})

	t.Run("init_container_inner_volume_mounts_canonicalized", func(t *testing.T) {
		// InitContainers themselves stay ordered (sequential execution),
		// but VolumeMounts inside an InitContainer are order-independent.
		mounts := []corev1.VolumeMount{
			{Name: "a", MountPath: "/a"},
			{Name: "b", MountPath: "/b"},
		}
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "main"}},
			InitContainers: []corev1.Container{{Name: "init", Image: "init:v1", VolumeMounts: mounts}},
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "main"}},
			InitContainers: []corev1.Container{{Name: "init", Image: "init:v1", VolumeMounts: []corev1.VolumeMount{mounts[1], mounts[0]}}},
		}), "")
		assert.Equal(t, hashA, hashB,
			"InitContainer.VolumeMounts order must not affect the per-PCLQ hash even though InitContainers slice order does")
	})

	t.Run("init_container_inner_resource_claims_canonicalized", func(t *testing.T) {
		claims := []corev1.ResourceClaim{{Name: "claim-a"}, {Name: "claim-b"}}
		hashA := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "main"}},
			InitContainers: []corev1.Container{{Name: "init", Image: "init:v1", Resources: corev1.ResourceRequirements{Claims: claims}}},
		}), "")
		hashB := ComputePCLQPodTemplateHash(makeTemplate(corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "main"}},
			InitContainers: []corev1.Container{{Name: "init", Image: "init:v1", Resources: corev1.ResourceRequirements{Claims: []corev1.ResourceClaim{claims[1], claims[0]}}}},
		}), "")
		assert.Equal(t, hashA, hashB,
			"InitContainer.Resources.Claims order must not affect the per-PCLQ hash even though InitContainers slice order does")
	})
}

// TestComputePCLQPodTemplateHash_RealSpecChangesStillFlipHash is the
// regression test in the other direction: the per-PCLQ hash must continue
// to change when the desired state actually changes. If a future
// over-canonicalization (e.g. accidentally sorting Env, or zeroing out a
// field) silently masked real changes, rolling updates would simply stop
// working and pass this test suite. Pin it.
func TestComputePCLQPodTemplateHash_RealSpecChangesStillFlipHash(t *testing.T) {
	base := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: "worker",
		Spec: grovecorev1alpha1.PodCliqueSpec{PodSpec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "main", Image: "main:v1", Env: []corev1.EnvVar{{Name: "FOO", Value: "1"}}, VolumeMounts: []corev1.VolumeMount{{Name: "cache", MountPath: "/cache"}}},
			},
			Volumes: []corev1.Volume{{Name: "cache"}},
		}},
	}
	baseHash := ComputePCLQPodTemplateHash(base, "")

	t.Run("image_change_flips_hash", func(t *testing.T) {
		mut := base.DeepCopy()
		mut.Spec.PodSpec.Containers[0].Image = "main:v2"
		assert.NotEqual(t, baseHash, ComputePCLQPodTemplateHash(mut, ""), "image change must flip the per-PCLQ hash")
	})
	t.Run("env_value_change_flips_hash", func(t *testing.T) {
		mut := base.DeepCopy()
		mut.Spec.PodSpec.Containers[0].Env[0].Value = "2"
		assert.NotEqual(t, baseHash, ComputePCLQPodTemplateHash(mut, ""), "env var value change must flip the per-PCLQ hash")
	})
	t.Run("volume_mount_path_change_flips_hash", func(t *testing.T) {
		mut := base.DeepCopy()
		mut.Spec.PodSpec.Containers[0].VolumeMounts[0].MountPath = "/cache-new"
		assert.NotEqual(t, baseHash, ComputePCLQPodTemplateHash(mut, ""), "changing a volumeMount's mountPath must flip the per-PCLQ hash")
	})
	t.Run("new_volume_flips_hash", func(t *testing.T) {
		mut := base.DeepCopy()
		mut.Spec.PodSpec.Volumes = append(mut.Spec.PodSpec.Volumes, corev1.Volume{Name: "scratch"})
		assert.NotEqual(t, baseHash, ComputePCLQPodTemplateHash(mut, ""), "adding a volume must flip the per-PCLQ hash")
	})
	t.Run("pclq_template_label_change_flips_hash", func(t *testing.T) {
		mut := base.DeepCopy()
		mut.Labels = map[string]string{"role": "worker-v2"}
		assert.NotEqual(t, baseHash, ComputePCLQPodTemplateHash(mut, ""), "PodCliqueTemplateSpec.Labels change must flip the per-PCLQ hash")
	})
}
