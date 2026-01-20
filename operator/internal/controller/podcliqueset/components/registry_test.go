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

package components

import (
	"testing"

	groveconfigv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// TestCreateOperatorRegistry tests creating the operator registry for PodCliqueSet reconciler.
func TestCreateOperatorRegistry(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = groveschedulerv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	_ = autoscalingv2.AddToScheme(scheme)

	// Test successful registry creation with all operators
	t.Run("creates registry with all operators", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		mgr := &mockManager{client: cl, scheme: scheme}
		eventRecorder := record.NewFakeRecorder(10)

		registry := CreateOperatorRegistry(mgr, eventRecorder, groveconfigv1alpha1.TopologyAwareSchedulingConfiguration{})

		require.NotNil(t, registry)

		// Verify all expected operators are registered
		expectedKinds := []component.Kind{
			component.KindPodClique,
			component.KindHeadlessService,
			component.KindRole,
			component.KindRoleBinding,
			component.KindServiceAccount,
			component.KindServiceAccountTokenSecret,
			component.KindPodCliqueScalingGroup,
			component.KindHorizontalPodAutoscaler,
			component.KindPodGang,
			component.KindPodCliqueSetReplica,
			component.KindComputeDomain,
		}

		allOps := registry.GetAllOperators()
		assert.Len(t, allOps, len(expectedKinds))

		for _, kind := range expectedKinds {
			op, err := registry.GetOperator(kind)
			require.NoError(t, err, "operator for kind %s should be registered", kind)
			assert.NotNil(t, op, "operator for kind %s should not be nil", kind)
		}
	})

	// Test verifying specific operator registrations
	t.Run("verifies key operator registrations", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		mgr := &mockManager{client: cl, scheme: scheme}
		eventRecorder := record.NewFakeRecorder(10)

		registry := CreateOperatorRegistry(mgr, eventRecorder, groveconfigv1alpha1.TopologyAwareSchedulingConfiguration{})

		// Verify PodClique operator
		pclqOp, err := registry.GetOperator(component.KindPodClique)
		require.NoError(t, err)
		assert.NotNil(t, pclqOp)

		// Verify Service operator
		svcOp, err := registry.GetOperator(component.KindHeadlessService)
		require.NoError(t, err)
		assert.NotNil(t, svcOp)

		// Verify RBAC operators
		roleOp, err := registry.GetOperator(component.KindRole)
		require.NoError(t, err)
		assert.NotNil(t, roleOp)

		rbOp, err := registry.GetOperator(component.KindRoleBinding)
		require.NoError(t, err)
		assert.NotNil(t, rbOp)

		saOp, err := registry.GetOperator(component.KindServiceAccount)
		require.NoError(t, err)
		assert.NotNil(t, saOp)
	})
}

// mockManager is a minimal mock implementation for testing
type mockManager struct {
	manager.Manager
	client client.Client
	scheme *runtime.Scheme
}

func (m *mockManager) GetClient() client.Client {
	return m.client
}

func (m *mockManager) GetScheme() *runtime.Scheme {
	return m.scheme
}
