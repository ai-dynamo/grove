package pclq

import (
	"fmt"
	"hash/fnv"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/rand"
)

// ComputePodTemplateHash computes a hash for a Kubernetes PodTemplateSpec.
// This uses FNV-64a hash with JSON marshaling for optimal performance.
// This is faster than using dump.ForHash() which uses deep reflection.
func ComputePodTemplateHash(podTemplateSpec *corev1.PodTemplateSpec) string {
	hasher := fnv.New64a()

	// JSON marshaling is 2-3x faster than dump.ForHash and ensures
	// all fields are included (critical for rolling updates)
	specBytes, _ := json.Marshal(podTemplateSpec)
	hasher.Write(specBytes)

	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum64()))
}

// ComputePCLQPodTemplateHash computes the pod template hash for a PodClique template spec.
// This wraps the PodCliqueTemplateSpec into a standard PodTemplateSpec and computes its hash.
// The hash includes:
// - Labels and annotations from the template
// - The entire PodSpec
// - The priority class name
//
// Any change to these fields will result in a different hash, triggering a rolling update.
func ComputePCLQPodTemplateHash(
	pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec,
	priorityClassName string,
) string {
	podTemplateSpec := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      pclqTemplateSpec.Labels,
			Annotations: pclqTemplateSpec.Annotations,
		},
		Spec: pclqTemplateSpec.Spec.PodSpec,
	}
	podTemplateSpec.Spec.PriorityClassName = priorityClassName

	return ComputePodTemplateHash(&podTemplateSpec)
}
