package hash

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/dump"
	"k8s.io/apimachinery/pkg/util/rand"
)

func BenchmarkComputeHashWithFNCAndJSONMarshal(b *testing.B) {
	spec := createComplexPodTemplateSpec()
	priorityClassName := "high-priority"

	for b.Loop() {
		hasher := fnv.New64a()
		specBytes, _ := json.Marshal(spec)
		_, err := hasher.Write(specBytes)
		require.NoError(b, err)
		_, err = hasher.Write([]byte(priorityClassName))
		require.NoError(b, err)
		_ = rand.SafeEncodeString(fmt.Sprint(hasher.Sum64()))
		require.NoError(b, err)
	}
}

func BenchmarkComputeHashWithFNVAndDumpForHash(b *testing.B) {
	spec := createComplexPodTemplateSpec()
	priorityClassName := "high-priority"
	for b.Loop() {
		hasher := fnv.New64a()
		// Current implementation
		_, err := fmt.Fprintf(hasher, "%v", dump.ForHash(spec))
		require.NoError(b, err)
		_, err = fmt.Fprintf(hasher, "%v", priorityClassName)
		require.NoError(b, err)
		_ = fmt.Sprintf("%x", hasher.Sum(nil))
	}
}

// createSimplePodCliqueTemplateSpec creates a minimal spec for benchmarking
func createSimplePodCliqueTemplateSpec() *grovecorev1alpha1.PodCliqueTemplateSpec {
	return &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name:   "simple-clique",
		Labels: map[string]string{"app": "test"},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			RoleName: "worker",
			Replicas: 1,
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "main", Image: "test:latest"},
				},
			},
		},
	}
}

func createComplexPodTemplateSpec() *grovecorev1alpha1.PodCliqueTemplateSpec {
	return &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: "complex-clique",
		Labels: map[string]string{
			"app":         "test-app",
			"version":     "v1.2.3",
			"environment": "production",
			"team":        "ml-training",
		},
		Annotations: map[string]string{
			"description":      "Complex pod template for benchmarking",
			"owner":            "ml-team",
			"last-updated":     "2026-02-08",
			"monitoring-level": "high",
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			RoleName: "worker",
			Replicas: 8,
			MinAvailable: func() *int32 {
				v := int32(6)
				return &v
			}(),
			PodSpec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyAlways,
				Containers: []corev1.Container{
					{
						Name:  "main-container",
						Image: "registry.example.com/ml-training:v2.5.1",
						Command: []string{
							"/usr/bin/python",
							"-m",
							"torch.distributed.launch",
						},
						Args: []string{
							"--nproc_per_node=8",
							"--nnodes=8",
							"--node_rank=$(POD_INDEX)",
							"train.py",
							"--batch-size=256",
							"--epochs=100",
						},
						Env: []corev1.EnvVar{
							{Name: "MASTER_ADDR", Value: "master-0"},
							{Name: "MASTER_PORT", Value: "29500"},
							{Name: "WORLD_SIZE", Value: "64"},
							{Name: "RANK", ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.name",
								},
							}},
							{Name: "NCCL_DEBUG", Value: "INFO"},
							{Name: "NCCL_IB_DISABLE", Value: "0"},
							{Name: "NCCL_SOCKET_IFNAME", Value: "eth0"},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("32"),
								corev1.ResourceMemory: resource.MustParse("128Gi"),
								"nvidia.com/gpu":      resource.MustParse("8"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("64"),
								corev1.ResourceMemory: resource.MustParse("256Gi"),
								"nvidia.com/gpu":      resource.MustParse("8"),
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "data", MountPath: "/data", ReadOnly: true},
							{Name: "models", MountPath: "/models"},
							{Name: "checkpoints", MountPath: "/checkpoints"},
							{Name: "logs", MountPath: "/logs"},
							{Name: "shared-memory", MountPath: "/dev/shm"},
						},
						ImagePullPolicy: corev1.PullIfNotPresent,
					},
					{
						Name:  "sidecar-logger",
						Image: "fluent/fluent-bit:2.0",
						Args:  []string{"-c", "/fluent-bit/etc/fluent-bit.conf"},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "logs", MountPath: "/logs", ReadOnly: true},
							{Name: "fluent-config", MountPath: "/fluent-bit/etc"},
						},
					},
				},
				InitContainers: []corev1.Container{
					{
						Name:  "init-data",
						Image: "busybox:1.36",
						Command: []string{
							"sh",
							"-c",
							"echo 'Initializing data...' && sleep 5",
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "data", MountPath: "/data"},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "training-data-pvc",
							},
						},
					},
					{
						Name: "models",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "models-pvc",
							},
						},
					},
					{
						Name: "checkpoints",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{
								SizeLimit: func() *resource.Quantity {
									q := resource.MustParse("100Gi")
									return &q
								}(),
							},
						},
					},
					{
						Name: "logs",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
					{
						Name: "shared-memory",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{
								Medium: corev1.StorageMediumMemory,
								SizeLimit: func() *resource.Quantity {
									q := resource.MustParse("64Gi")
									return &q
								}(),
							},
						},
					},
					{
						Name: "fluent-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "fluent-bit-config",
								},
							},
						},
					},
				},
				NodeSelector: map[string]string{
					"node.kubernetes.io/instance-type": "gpu-8x-a100",
					"topology.kubernetes.io/zone":      "us-west-2a",
				},
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      "nvidia.com/gpu.present",
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"true"},
										},
									},
								},
							},
						},
					},
				},
				Tolerations: []corev1.Toleration{
					{
						Key:      "nvidia.com/gpu",
						Operator: corev1.TolerationOpEqual,
						Value:    "true",
						Effect:   corev1.TaintEffectNoSchedule,
					},
				},
			},
		},
	}
}

// BenchmarkComputePCLQPodTemplateHash_Simple benchmarks the full hash computation with a simple spec
func BenchmarkComputePCLQPodTemplateHash_Simple(b *testing.B) {
	spec := createSimplePodCliqueTemplateSpec()
	priorityClassName := "high-priority"

	for b.Loop() {
		_, _ = ComputePCLQPodTemplateHash(spec, priorityClassName)
	}
}

// BenchmarkComputePCLQPodTemplateHash_Complex benchmarks the full hash computation with a complex spec
func BenchmarkComputePCLQPodTemplateHash_Complex(b *testing.B) {
	spec := createComplexPodTemplateSpec()
	priorityClassName := "high-priority"

	for b.Loop() {
		_, _ = ComputePCLQPodTemplateHash(spec, priorityClassName)
	}
}

// BenchmarkGetOrCompute_CacheHit benchmarks GetOrCompute with cache hits
func BenchmarkGetOrCompute_CacheHit(b *testing.B) {
	cache := NewDefaultPodTemplateSpecHashCache(b.Context())
	spec := createComplexPodTemplateSpec()
	priorityClassName := "high-priority"

	// Prime the cache
	_, _ = cache.GetOrCompute("test-pcs", 1, spec, priorityClassName)

	for b.Loop() {
		_, _ = cache.GetOrCompute("test-pcs", 1, spec, priorityClassName)
	}
}

// BenchmarkGetOrCompute_CacheMiss benchmarks GetOrCompute with cache misses
func BenchmarkGetOrCompute_CacheMiss(b *testing.B) {
	cache := NewDefaultPodTemplateSpecHashCache(b.Context())
	spec := createComplexPodTemplateSpec()
	priorityClassName := "high-priority"

	i := 0
	for b.Loop() {
		// Use different generation each time to force cache miss
		_, _ = cache.GetOrCompute("test-pcs", int64(i), spec, priorityClassName)
		i++
	}
}

// BenchmarkGetOrCompute_SpecContentChange benchmarks GetOrCompute when spec content changes
func BenchmarkGetOrCompute_SpecContentChange(b *testing.B) {
	cache := NewDefaultPodTemplateSpecHashCache(b.Context())
	spec := createComplexPodTemplateSpec()
	priorityClassName := "high-priority"

	i := 0
	for b.Loop() {
		// Change spec content each iteration
		spec.Spec.PodSpec.Containers[0].Image = fmt.Sprintf("test:v%d", i)
		_, _ = cache.GetOrCompute("test-pcs", 1, spec, priorityClassName)
		i++
	}
}