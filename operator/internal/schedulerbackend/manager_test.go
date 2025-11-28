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

package schedulerbackend

import (
	"sync"
	"testing"

	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/record"
)

// TestInitialize tests backend initialization with different schedulers.
func TestInitialize(t *testing.T) {
	tests := []struct {
		name          string
		schedulerName string
		wantErr       bool
		errContains   string
		expectedName  string
	}{
		{
			name:          "kai-scheduler initialization",
			schedulerName: "kai-scheduler",
			wantErr:       false,
			expectedName:  "KAI-Scheduler",
		},
		{
			name:          "default-scheduler initialization",
			schedulerName: "default-scheduler",
			wantErr:       false,
			expectedName:  "Kube-Scheduler",
		},
		{
			name:          "empty scheduler name defaults to default-scheduler",
			schedulerName: "",
			wantErr:       false,
			expectedName:  "Kube-Scheduler",
		},
		{
			name:          "unsupported scheduler",
			schedulerName: "volcano",
			wantErr:       true,
			errContains:   "unsupported scheduler",
		},
		{
			name:          "invalid scheduler name",
			schedulerName: "invalid-scheduler",
			wantErr:       true,
			errContains:   "unsupported scheduler",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset global state before each test
			globalBackend = nil
			globalSchedulerName = ""
			initOnce = sync.Once{}

			cl := testutils.CreateDefaultFakeClient(nil)
			recorder := record.NewFakeRecorder(10)

			err := Initialize(cl, cl.Scheme(), recorder, tt.schedulerName)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, Get())
				assert.False(t, IsInitialized())
			} else {
				require.NoError(t, err)
				require.NotNil(t, Get())
				assert.True(t, IsInitialized())
				assert.Equal(t, tt.expectedName, Get().Name())
			}
		})
	}
}

// TestInitializeOnce tests that Initialize can only be called once.
func TestInitializeOnce(t *testing.T) {
	// Reset global state
	globalBackend = nil
	globalSchedulerName = ""
	initOnce = sync.Once{}

	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	// First initialization should succeed
	err := Initialize(cl, cl.Scheme(), recorder, "kai-scheduler")
	require.NoError(t, err)
	assert.Equal(t, "kai-scheduler", GetSchedulerName())
	firstBackend := Get()
	require.NotNil(t, firstBackend)

	// Second initialization should be ignored (due to sync.Once)
	err = Initialize(cl, cl.Scheme(), recorder, "default-scheduler")
	require.NoError(t, err)
	// Backend should still be kai-scheduler, not default-scheduler
	assert.Equal(t, "kai-scheduler", GetSchedulerName())
	assert.Equal(t, firstBackend, Get())
}

// TestGet tests the Get function.
func TestGet(t *testing.T) {
	// Reset global state
	globalBackend = nil
	globalSchedulerName = ""
	initOnce = sync.Once{}

	// Before initialization, Get should return nil
	assert.Nil(t, Get())

	// After initialization, Get should return the backend
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	err := Initialize(cl, cl.Scheme(), recorder, "kai-scheduler")
	require.NoError(t, err)

	backend := Get()
	require.NotNil(t, backend)
	assert.Equal(t, "KAI-Scheduler", backend.Name())
}

// TestMustGet tests the MustGet function.
func TestMustGet(t *testing.T) {
	// Reset global state
	globalBackend = nil
	globalSchedulerName = ""
	initOnce = sync.Once{}

	// Before initialization, MustGet should panic
	assert.Panics(t, func() {
		MustGet()
	})

	// After initialization, MustGet should return the backend
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	err := Initialize(cl, cl.Scheme(), recorder, "kai-scheduler")
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		backend := MustGet()
		assert.NotNil(t, backend)
		assert.Equal(t, "KAI-Scheduler", backend.Name())
	})
}

// TestGetSchedulerName tests the GetSchedulerName function.
func TestGetSchedulerName(t *testing.T) {
	// Reset global state
	globalBackend = nil
	globalSchedulerName = ""
	initOnce = sync.Once{}

	// Before initialization, GetSchedulerName should return empty string
	assert.Equal(t, "", GetSchedulerName())

	// After initialization with kai-scheduler
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	err := Initialize(cl, cl.Scheme(), recorder, "kai-scheduler")
	require.NoError(t, err)
	assert.Equal(t, "kai-scheduler", GetSchedulerName())
}

// TestIsInitialized tests the IsInitialized function.
func TestIsInitialized(t *testing.T) {
	// Reset global state
	globalBackend = nil
	globalSchedulerName = ""
	initOnce = sync.Once{}

	// Before initialization
	assert.False(t, IsInitialized())

	// After successful initialization
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	err := Initialize(cl, cl.Scheme(), recorder, "kai-scheduler")
	require.NoError(t, err)
	assert.True(t, IsInitialized())
}

// TestInitializeFailedInit tests that failed initialization leaves state as not initialized.
func TestInitializeFailedInit(t *testing.T) {
	// Reset global state
	globalBackend = nil
	globalSchedulerName = ""
	initOnce = sync.Once{}

	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	// Try to initialize with unsupported scheduler
	err := Initialize(cl, cl.Scheme(), recorder, "unsupported-scheduler")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported scheduler")

	// Verify state is not initialized
	assert.False(t, IsInitialized())
	assert.Nil(t, Get())
	assert.Equal(t, "", GetSchedulerName())
}
