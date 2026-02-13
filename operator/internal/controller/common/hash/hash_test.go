package hash

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCompute_IdenticalSpecsProduceSameHash(t *testing.T) {
	spec := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"foo": "bar"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c1", Image: "nginx"}},
		},
	}
	h1, err1 := Compute(spec)
	h2, err2 := Compute(spec)
	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.Equal(t, h1, h2)
}

func TestCompute_DifferentSpecsProduceDifferentHash(t *testing.T) {
	spec1 := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"foo": "bar"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c1", Image: "nginx"}}},
	}
	spec2 := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"foo": "baz"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c1", Image: "nginx"}}},
	}
	h1, err1 := Compute(spec1)
	h2, err2 := Compute(spec2)
	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.NotEqual(t, h1, h2)
}

func TestCompute_NilInput(t *testing.T) {
	var err error
	_, err = Compute(nil)
	assert.Error(t, err)

	_, err = Compute()
	assert.Error(t, err)

	_, err = Compute(nil, nil)
	assert.Error(t, err)
}

func TestComputePCLQPodTemplateHash_IdenticalInputsSameHash(t *testing.T) {
	tmpl := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name:   "foo",
		Labels: map[string]string{"a": "b"},
		Annotations: map[string]string{"x": "y"},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "c1", Image: "nginx"}},
			},
		},
	}
	h1, err1 := ComputePCLQPodTemplateHash(tmpl, "pclass")
	h2, err2 := ComputePCLQPodTemplateHash(tmpl, "pclass")
	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.Equal(t, h1, h2)
}

func TestComputePCLQPodTemplateHash_DifferentPriorityClass(t *testing.T) {
	tmpl := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name:   "foo",
		Labels: map[string]string{"a": "b"},
		Annotations: map[string]string{"x": "y"},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "c1", Image: "nginx"}},
			},
		},
	}
	h1, err1 := ComputePCLQPodTemplateHash(tmpl, "pclass1")
	h2, err2 := ComputePCLQPodTemplateHash(tmpl, "pclass2")
	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.NotEqual(t, h1, h2)
}

func TestComputePCLQPodTemplateHash_DifferentLabels(t *testing.T) {
	tmpl1 := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name:   "foo",
		Labels: map[string]string{"a": "b1"},
		Annotations: map[string]string{"x": "y"},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "c1", Image: "nginx"}},
			},
		},
	}
	tmpl2 := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name:   "foo",
		Labels: map[string]string{"a": "b2"},
		Annotations: map[string]string{"x": "y"},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "c1", Image: "nginx"}},
			},
		},
	}
	h1, err1 := ComputePCLQPodTemplateHash(tmpl1, "pclass")
	h2, err2 := ComputePCLQPodTemplateHash(tmpl2, "pclass")
	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.NotEqual(t, h1, h2)
}

func TestComputePCLQPodTemplateHash_NilInput(t *testing.T) {
	h, err := ComputePCLQPodTemplateHash(nil, "pclass")
	assert.Error(t, err)
	assert.Empty(t, h)
}

