//go:build e2e

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

package tests

import (
	"context"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test_OI1_OperatorIgnoresUnmanagedResources verifies that the operator's filtered cache
// causes it to ignore resources without the grove managed-by label.
// Scenario OI-1:
//  1. Initialize a 10-node Grove cluster
//  2. Deploy workload WL1 and verify 10 managed pods are running
//  3. Create unmanaged resources (no grove labels) in the same namespace
//  4. Wait and verify the operator does not modify any unmanaged resource
func Test_OI1_OperatorIgnoresUnmanagedResources(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 10-node Grove cluster")
	expectedManagedPods := 10
	tc, cleanup := testctx.PrepareTest(ctx, t, 10,
		testctx.WithTimeout(5*time.Minute),
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: expectedManagedPods,
		}),
	)
	defer cleanup()

	Logger.Info("2. Deploy workload WL1, and verify 10 managed pods are running")
	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err, "Failed to deploy workload")

	err = tc.WaitForReadyPods(expectedManagedPods)
	require.NoError(t, err, "Failed to wait for managed pods to be ready")

	Logger.Info("3. Create unmanaged resources (no grove labels) in the same namespace")
	clientset := tc.Clients.Clientset
	ns := "default"
	unmanagedLabels := map[string]string{"app": "not-grove"}

	// Pod
	_, err = clientset.CoreV1().Pods(ns).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "unmanaged-pod", Namespace: ns, Labels: unmanagedLabels},
		Spec: corev1.PodSpec{
			Tolerations: []corev1.Toleration{{
				Key: "node_role.e2e.grove.nvidia.com", Operator: corev1.TolerationOpEqual,
				Value: "agent", Effect: corev1.TaintEffectNoSchedule,
			}},
			Containers: []corev1.Container{{Name: "busybox", Image: "registry:5001/busybox:latest", Command: []string{"sleep", "30"}}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create unmanaged pod")
	defer func() { _ = clientset.CoreV1().Pods(ns).Delete(ctx, "unmanaged-pod", metav1.DeleteOptions{}) }()

	// ServiceAccount
	_, err = clientset.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "unmanaged-sa", Namespace: ns, Labels: unmanagedLabels},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create unmanaged service account")
	defer func() { _ = clientset.CoreV1().ServiceAccounts(ns).Delete(ctx, "unmanaged-sa", metav1.DeleteOptions{}) }()

	// Service
	_, err = clientset.CoreV1().Services(ns).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "unmanaged-svc", Namespace: ns, Labels: unmanagedLabels},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create unmanaged service")
	defer func() { _ = clientset.CoreV1().Services(ns).Delete(ctx, "unmanaged-svc", metav1.DeleteOptions{}) }()

	// Role
	_, err = clientset.RbacV1().Roles(ns).Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "unmanaged-role", Namespace: ns, Labels: unmanagedLabels},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create unmanaged role")
	defer func() { _ = clientset.RbacV1().Roles(ns).Delete(ctx, "unmanaged-role", metav1.DeleteOptions{}) }()

	// RoleBinding
	_, err = clientset.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "unmanaged-rb", Namespace: ns, Labels: unmanagedLabels},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "unmanaged-role"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "unmanaged-sa", Namespace: ns}},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create unmanaged role binding")
	defer func() { _ = clientset.RbacV1().RoleBindings(ns).Delete(ctx, "unmanaged-rb", metav1.DeleteOptions{}) }()

	// HorizontalPodAutoscaler
	_, err = clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).Create(ctx, &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "unmanaged-hpa", Namespace: ns, Labels: unmanagedLabels},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "dummy", APIVersion: "apps/v1"},
			MaxReplicas:    1,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create unmanaged HPA")
	defer func() {
		_ = clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).Delete(ctx, "unmanaged-hpa", metav1.DeleteOptions{})
	}()

	Logger.Info("4. Verify the operator does not modify any unmanaged resource")
	// Wait briefly to give the operator a chance to act (it shouldn't)
	time.Sleep(10 * time.Second)

	// Helper to assert a resource was not touched by the operator
	assertUntouched := func(t *testing.T, kind string, ownerRefs []metav1.OwnerReference, resourceLabels map[string]string, finalizers []string) {
		t.Helper()
		assert.Empty(t, ownerRefs, "%s should have no owner references", kind)
		_, hasManagedBy := resourceLabels["app.kubernetes.io/managed-by"]
		assert.False(t, hasManagedBy, "%s should not have managed-by label", kind)
		_, hasPartOf := resourceLabels["app.kubernetes.io/part-of"]
		assert.False(t, hasPartOf, "%s should not have part-of label", kind)
		assert.Empty(t, finalizers, "%s should have no finalizers", kind)
	}

	pod, err := clientset.CoreV1().Pods(ns).Get(ctx, "unmanaged-pod", metav1.GetOptions{})
	require.NoError(t, err, "Failed to get unmanaged pod")
	assertUntouched(t, "Pod", pod.OwnerReferences, pod.Labels, pod.Finalizers)

	sa, err := clientset.CoreV1().ServiceAccounts(ns).Get(ctx, "unmanaged-sa", metav1.GetOptions{})
	require.NoError(t, err, "Failed to get unmanaged service account")
	assertUntouched(t, "ServiceAccount", sa.OwnerReferences, sa.Labels, sa.Finalizers)

	svc, err := clientset.CoreV1().Services(ns).Get(ctx, "unmanaged-svc", metav1.GetOptions{})
	require.NoError(t, err, "Failed to get unmanaged service")
	assertUntouched(t, "Service", svc.OwnerReferences, svc.Labels, svc.Finalizers)

	role, err := clientset.RbacV1().Roles(ns).Get(ctx, "unmanaged-role", metav1.GetOptions{})
	require.NoError(t, err, "Failed to get unmanaged role")
	assertUntouched(t, "Role", role.OwnerReferences, role.Labels, role.Finalizers)

	rb, err := clientset.RbacV1().RoleBindings(ns).Get(ctx, "unmanaged-rb", metav1.GetOptions{})
	require.NoError(t, err, "Failed to get unmanaged role binding")
	assertUntouched(t, "RoleBinding", rb.OwnerReferences, rb.Labels, rb.Finalizers)

	hpa, err := clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, "unmanaged-hpa", metav1.GetOptions{})
	require.NoError(t, err, "Failed to get unmanaged HPA")
	assertUntouched(t, "HorizontalPodAutoscaler", hpa.OwnerReferences, hpa.Labels, hpa.Finalizers)

	// Verify managed pods are still healthy (operator is still working)
	managedPods, err := tc.ListPods()
	require.NoError(t, err, "Failed to list managed pods")
	assert.Len(t, managedPods.Items, expectedManagedPods, "managed pod count should be unchanged")
	for _, p := range managedPods.Items {
		assert.NotEqual(t, "unmanaged-pod", p.Name, "unmanaged pod should not appear in managed pod list")
	}

	Logger.Info("Operator infra test completed successfully!")
}
