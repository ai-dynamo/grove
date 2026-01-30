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

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/record"
)

// TestInitialize tests backend initialization with different schedulers.
func TestInitialize(t *testing.T) {
	tests := []struct {
		name          string
		schedulerName configv1alpha1.SchedulerName
		wantErr       bool
		errContains   string
		expectedName  string
	}{
		{
			name:          "kai scheduler initialization",
			schedulerName: configv1alpha1.SchedulerNameKai,
			wantErr:       false,
			expectedName:  "kai-scheduler",
		},
		{
			name:          "default scheduler initialization",
			schedulerName: configv1alpha1.SchedulerNameKube,
			wantErr:       false,
			expectedName:  "default-scheduler",
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
	err := Initialize(cl, cl.Scheme(), recorder, configv1alpha1.SchedulerNameKai)
	require.NoError(t, err)
	assert.Equal(t, string(configv1alpha1.SchedulerNameKai), GetSchedulerName())
	firstBackend := Get()
	require.NotNil(t, firstBackend)

	// Second initialization should be ignored (due to sync.Once)
	err = Initialize(cl, cl.Scheme(), recorder, configv1alpha1.SchedulerNameKube)
	require.NoError(t, err)
	// Backend should still be kai-scheduler, not default-scheduler
	assert.Equal(t, string(configv1alpha1.SchedulerNameKai), GetSchedulerName())
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

	err := Initialize(cl, cl.Scheme(), recorder, configv1alpha1.SchedulerNameKai)
	require.NoError(t, err)

	backend := Get()
	require.NotNil(t, backend)
	assert.Equal(t, string(configv1alpha1.SchedulerNameKai), backend.Name())
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

	err := Initialize(cl, cl.Scheme(), recorder, configv1alpha1.SchedulerNameKai)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		backend := MustGet()
		assert.NotNil(t, backend)
		assert.Equal(t, "kai-scheduler", backend.Name())
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

	// After initialization with kai scheduler
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)

	err := Initialize(cl, cl.Scheme(), recorder, configv1alpha1.SchedulerNameKai)
	require.NoError(t, err)
	assert.Equal(t, string(configv1alpha1.SchedulerNameKai), GetSchedulerName())
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

	err := Initialize(cl, cl.Scheme(), recorder, configv1alpha1.SchedulerNameKai)
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
