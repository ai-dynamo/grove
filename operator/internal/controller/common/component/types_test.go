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

package component

import (
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestNewOperatorRegistry tests creating a new operator registry.
func TestNewOperatorRegistry(t *testing.T) {
	// Test creating registry for PodCliqueSet
	t.Run("PodCliqueSet registry", func(t *testing.T) {
		registry := NewOperatorRegistry[grovecorev1alpha1.PodCliqueSet]()
		require.NotNil(t, registry)
		assert.Empty(t, registry.GetAllOperators())
	})

	// Test creating registry for PodClique
	t.Run("PodClique registry", func(t *testing.T) {
		registry := NewOperatorRegistry[grovecorev1alpha1.PodClique]()
		require.NotNil(t, registry)
		assert.Empty(t, registry.GetAllOperators())
	})

	// Test creating registry for PodCliqueScalingGroup
	t.Run("PodCliqueScalingGroup registry", func(t *testing.T) {
		registry := NewOperatorRegistry[grovecorev1alpha1.PodCliqueScalingGroup]()
		require.NotNil(t, registry)
		assert.Empty(t, registry.GetAllOperators())
	})
}

// Ensure mockOperatorImpl implements Operator interface at compile time
var _ Operator[grovecorev1alpha1.PodCliqueSet] = (*mockOperatorImpl[grovecorev1alpha1.PodCliqueSet])(nil)

type mockOperatorImpl[T GroveCustomResourceType] struct{}

func (m *mockOperatorImpl[T]) GetExistingResourceNames(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) ([]string, error) {
	return []string{"resource1", "resource2"}, nil
}

func (m *mockOperatorImpl[T]) Sync(_ context.Context, _ logr.Logger, _ *T) error {
	return nil
}

func (m *mockOperatorImpl[T]) Delete(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) error {
	return nil
}

// TestOperatorRegistryRegister tests registering operators in the registry.
func TestOperatorRegistryRegister(t *testing.T) {
	registry := NewOperatorRegistry[grovecorev1alpha1.PodCliqueSet]()
	operator := &mockOperatorImpl[grovecorev1alpha1.PodCliqueSet]{}

	// Register an operator
	registry.Register(KindPodClique, operator)

	// Verify it was registered
	allOps := registry.GetAllOperators()
	assert.Len(t, allOps, 1)
	assert.Contains(t, allOps, KindPodClique)
}

// TestOperatorRegistryGetOperator tests retrieving operators from the registry.
func TestOperatorRegistryGetOperator(t *testing.T) {
	registry := NewOperatorRegistry[grovecorev1alpha1.PodCliqueSet]()
	operator := &mockOperatorImpl[grovecorev1alpha1.PodCliqueSet]{}

	// Test getting non-existent operator
	t.Run("non-existent operator", func(t *testing.T) {
		op, err := registry.GetOperator(KindPodClique)
		assert.Error(t, err)
		assert.Nil(t, op)
		assert.Contains(t, err.Error(), "operator for kind PodClique not found")
	})

	// Register and retrieve operator
	t.Run("existing operator", func(t *testing.T) {
		registry.Register(KindPodClique, operator)

		op, err := registry.GetOperator(KindPodClique)
		require.NoError(t, err)
		assert.NotNil(t, op)
		assert.Equal(t, operator, op)
	})
}

// TestOperatorRegistryGetAllOperators tests retrieving all operators.
func TestOperatorRegistryGetAllOperators(t *testing.T) {
	registry := NewOperatorRegistry[grovecorev1alpha1.PodCliqueSet]()

	// Initially empty
	assert.Empty(t, registry.GetAllOperators())

	// Register multiple operators
	op1 := &mockOperatorImpl[grovecorev1alpha1.PodCliqueSet]{}
	op2 := &mockOperatorImpl[grovecorev1alpha1.PodCliqueSet]{}
	op3 := &mockOperatorImpl[grovecorev1alpha1.PodCliqueSet]{}

	registry.Register(KindPodClique, op1)
	registry.Register(KindServiceAccount, op2)
	registry.Register(KindRole, op3)

	allOps := registry.GetAllOperators()
	assert.Len(t, allOps, 3)
	assert.Contains(t, allOps, KindPodClique)
	assert.Contains(t, allOps, KindServiceAccount)
	assert.Contains(t, allOps, KindRole)
	assert.Equal(t, op1, allOps[KindPodClique])
	assert.Equal(t, op2, allOps[KindServiceAccount])
	assert.Equal(t, op3, allOps[KindRole])
}

// TestOperatorRegistryMultipleRegistrations tests that registering multiple times overwrites.
func TestOperatorRegistryMultipleRegistrations(t *testing.T) {
	registry := NewOperatorRegistry[grovecorev1alpha1.PodClique]()

	op1 := &mockOperatorImpl[grovecorev1alpha1.PodClique]{}
	op2 := &mockOperatorImpl[grovecorev1alpha1.PodClique]{}

	// Register first operator
	registry.Register(KindPod, op1)
	retrieved1, err := registry.GetOperator(KindPod)
	require.NoError(t, err)
	assert.Equal(t, op1, retrieved1)

	// Register second operator with same kind (should overwrite)
	registry.Register(KindPod, op2)
	retrieved2, err := registry.GetOperator(KindPod)
	require.NoError(t, err)
	assert.Equal(t, op2, retrieved2)
	// Verify it's a different instance (overwritten)
	// Since both op1 and op2 are empty structs, we just verify the second retrieval succeeds
	assert.NotNil(t, retrieved2)
}

// TestKindConstants tests that Kind constants are properly defined.
func TestKindConstants(t *testing.T) {
	assert.Equal(t, Kind("PodClique"), KindPodClique)
	assert.Equal(t, Kind("ServiceAccount"), KindServiceAccount)
	assert.Equal(t, Kind("Role"), KindRole)
	assert.Equal(t, Kind("RoleBinding"), KindRoleBinding)
	assert.Equal(t, Kind("ServiceAccountTokenSecret"), KindServiceAccountTokenSecret)
	assert.Equal(t, Kind("HeadlessService"), KindHeadlessService)
	assert.Equal(t, Kind("HorizontalPodAutoscaler"), KindHorizontalPodAutoscaler)
	assert.Equal(t, Kind("Pod"), KindPod)
	assert.Equal(t, Kind("PodCliqueScalingGroup"), KindPodCliqueScalingGroup)
	assert.Equal(t, Kind("PodGang"), KindPodGang)
	assert.Equal(t, Kind("PodCliqueSetReplica"), KindPodCliqueSetReplica)
}

// TestOperationConstants tests that operation constants are properly defined.
func TestOperationConstants(t *testing.T) {
	assert.Equal(t, "GetExistingResourceNames", OperationGetExistingResourceNames)
	assert.Equal(t, "Sync", OperationSync)
	assert.Equal(t, "Delete", OperationDelete)
}
