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

package registry

import (
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/record"
)

// TestNewRegistry tests New with different scheduler profiles.
func TestNewRegistry(t *testing.T) {
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
			errContains:   "not supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := testutils.CreateDefaultFakeClient(nil)
			recorder := record.NewFakeRecorder(10)

			cfg := configv1alpha1.SchedulerConfiguration{
				Profiles: []configv1alpha1.SchedulerProfile{
					{Name: tt.schedulerName},
				},
				DefaultProfileName: string(tt.schedulerName),
			}
			reg, err := New(cl, cl.Scheme(), recorder, cfg)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, reg)
			} else {
				require.NoError(t, err)
				require.NotNil(t, reg.GetDefault())
				name := reg.GetDefault().Name()
				assert.Equal(t, tt.expectedName, name)
				assert.Equal(t, reg.GetDefault(), reg.Get(name))
				assert.Equal(t, reg.GetDefault(), reg.Get(""), "Get with empty string should return the default backend")
			}
		})
	}

	t.Run("multiple profiles with default set to kai", func(t *testing.T) {
		cl := testutils.CreateDefaultFakeClient(nil)
		recorder := record.NewFakeRecorder(10)
		cfg := configv1alpha1.SchedulerConfiguration{
			Profiles: []configv1alpha1.SchedulerProfile{
				{Name: configv1alpha1.SchedulerNameKube},
				{Name: configv1alpha1.SchedulerNameKai},
			},
			DefaultProfileName: string(configv1alpha1.SchedulerNameKai),
		}
		reg, err := New(cl, cl.Scheme(), recorder, cfg)
		require.NoError(t, err)
		require.NotNil(t, reg.Get(string(configv1alpha1.SchedulerNameKai)))
		require.NotNil(t, reg.Get(string(configv1alpha1.SchedulerNameKube)))
		assert.Equal(t, reg.GetDefault(), reg.Get(string(configv1alpha1.SchedulerNameKai)))
	})
}

// TestGet tests the Get method of registry in isolation using stub backends.
func TestGet(t *testing.T) {
	kubeBackend := testutils.NewFakeSchedulerBackend("kube")
	kaiBackend := testutils.NewFakeSchedulerBackend("kai")
	reg := &registry{
		backends: map[string]scheduler.Backend{
			"kube": kubeBackend,
			"kai":  kaiBackend,
		},
		defaultBackend: kubeBackend,
	}

	tests := []struct {
		name     string
		input    string
		expected scheduler.Backend
	}{
		{
			name:     "known name returns matching backend",
			input:    "kube",
			expected: kubeBackend,
		},
		{
			name:     "another known name returns matching backend",
			input:    "kai",
			expected: kaiBackend,
		},
		{
			name:     "unknown name returns nil",
			input:    "volcano",
			expected: nil,
		},
		{
			name:     "empty string returns default backend",
			input:    "",
			expected: kubeBackend,
		},
		{
			name:     "whitespace-only string returns default backend",
			input:    "   ",
			expected: kubeBackend,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, reg.Get(tt.input))
		})
	}
}

// TestGet_NoDefault tests Get when no default backend is configured.
func TestGet_NoDefault(t *testing.T) {
	kubeBackend := testutils.NewFakeSchedulerBackend("kube")
	reg := &registry{
		backends: map[string]scheduler.Backend{
			"kube": kubeBackend,
		},
	}

	assert.Equal(t, kubeBackend, reg.Get("kube"), "known name still returns backend when no default is set")
	assert.Nil(t, reg.Get(""), "empty name returns nil when no default is configured")
	assert.Nil(t, reg.Get("  "), "whitespace name returns nil when no default is configured")
	assert.Nil(t, reg.Get("unknown"), "unknown name returns nil when no default is configured")
}

// TestGetDefault tests the GetDefault method of registry in isolation using stub backends.
func TestGetDefault(t *testing.T) {
	kubeBackend := testutils.NewFakeSchedulerBackend("kube")

	t.Run("returns configured default backend", func(t *testing.T) {
		reg := &registry{
			backends:       map[string]scheduler.Backend{"kube": kubeBackend},
			defaultBackend: kubeBackend,
		}
		require.NotNil(t, reg.GetDefault())
		assert.Equal(t, kubeBackend, reg.GetDefault())
	})

	t.Run("returns nil when no default is configured", func(t *testing.T) {
		reg := &registry{
			backends: map[string]scheduler.Backend{"kube": kubeBackend},
		}
		assert.Nil(t, reg.GetDefault())
	})

	t.Run("returns nil when registry has no backends", func(t *testing.T) {
		reg := &registry{backends: make(map[string]scheduler.Backend)}
		assert.Nil(t, reg.GetDefault())
	})
}

// TestAll tests the All method of registry in isolation using stub backends.
func TestAll(t *testing.T) {
	kubeBackend := testutils.NewFakeSchedulerBackend("kube")
	kaiBackend := testutils.NewFakeSchedulerBackend("kai")
	reg := &registry{
		backends: map[string]scheduler.Backend{
			"kube": kubeBackend,
			"kai":  kaiBackend,
		},
	}
	require.NotNil(t, reg.All())
	assert.Equal(t, reg.All(), map[string]scheduler.Backend{
		"kube": kubeBackend,
		"kai":  kaiBackend,
	})
}
