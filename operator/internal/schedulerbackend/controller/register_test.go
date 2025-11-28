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

package controller

import (
	"testing"

	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kai"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend/kube"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/record"
)

// TestNewReconcilerWithBackendKai tests creating a reconciler with explicit kai backend.
func TestNewReconcilerWithBackendKai(t *testing.T) {
	tests := []struct {
		name         string
		backendType  string
		expectedName string
	}{
		{
			name:         "create reconciler with kai backend",
			backendType:  "kai",
			expectedName: "KAI-Scheduler",
		},
		{
			name:         "create reconciler with kube backend",
			backendType:  "kube",
			expectedName: "Kube-Scheduler",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := testutils.CreateDefaultFakeClient(nil)
			recorder := record.NewFakeRecorder(10)

			var backend schedulerbackend.SchedulerBackend
			if tt.backendType == "kai" {
				backend = kai.New(cl, cl.Scheme(), recorder, "kai-scheduler")
			} else {
				backend = kube.New(cl, cl.Scheme(), recorder, "default-scheduler")
			}

			reconciler := NewReconcilerWithBackend(cl, cl.Scheme(), backend)

			require.NotNil(t, reconciler)
			assert.Equal(t, cl, reconciler.Client)
			assert.Equal(t, cl.Scheme(), reconciler.Scheme)
			assert.Equal(t, backend, reconciler.Backend)
			assert.Equal(t, tt.expectedName, reconciler.Backend.Name())
		})
	}
}

// TestNewReconcilerWithBackendNilBackend tests NewReconcilerWithBackend with nil backend.
func TestNewReconcilerWithBackendNilBackend(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)

	// Should not panic even with nil backend
	assert.NotPanics(t, func() {
		reconciler := NewReconcilerWithBackend(cl, cl.Scheme(), nil)
		assert.NotNil(t, reconciler)
		assert.Nil(t, reconciler.Backend)
	})
}

// TestNewReconcilerWithBackendFields tests that all fields are correctly set.
func TestNewReconcilerWithBackendFields(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	backend := kai.New(cl, cl.Scheme(), recorder, "kai-scheduler")

	reconciler := NewReconcilerWithBackend(cl, cl.Scheme(), backend)

	// Verify all fields are set correctly
	require.NotNil(t, reconciler)
	assert.NotNil(t, reconciler.Client, "Client should not be nil")
	assert.NotNil(t, reconciler.Scheme, "Scheme should not be nil")
	assert.NotNil(t, reconciler.Backend, "Backend should not be nil")
	assert.Equal(t, "KAI-Scheduler", reconciler.Backend.Name())
}
