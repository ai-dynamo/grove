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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// TestCreateOperatorRegistry tests creating the operator registry for PodCliqueScalingGroup reconciler.
func TestCreateOperatorRegistry(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	
	// Test successful registry creation
	t.Run("creates registry with podclique operator", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		mgr := &mockManager{client: cl, scheme: scheme}
		eventRecorder := record.NewFakeRecorder(10)
		
		registry := CreateOperatorRegistry(mgr, eventRecorder)
		
		require.NotNil(t, registry)
		
		// Verify PodClique operator is registered
		pclqOp, err := registry.GetOperator(component.KindPodClique)
		require.NoError(t, err)
		assert.NotNil(t, pclqOp)
		
		// Verify only one operator is registered
		allOps := registry.GetAllOperators()
		assert.Len(t, allOps, 1)
		assert.Contains(t, allOps, component.KindPodClique)
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
