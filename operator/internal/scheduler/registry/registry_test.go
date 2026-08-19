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

package registry

import (
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	schedulertest "github.com/ai-dynamo/grove/operator/test/utils/scheduler"

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
			name:          "lpx scheduler initialization",
			schedulerName: configv1alpha1.SchedulerNameLPX,
			wantErr:       false,
			expectedName:  "lpx-scheduler",
		},
		{
			name:          "unsupported scheduler",
			schedulerName: "unknown-scheduler",
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
			reg, err := New(cl, cl, cl.Scheme(), recorder, cfg)

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
				assert.Nil(t, reg.Get(""), "Get with empty string returns nil; use GetOrDefault for fallback")
				assert.Equal(t, reg.GetDefault(), reg.GetOrDefault(""), "GetOrDefault with empty string returns the default backend")
			}
		})
	}

	t.Run("multiple profiles with default set to kai", func(t *testing.T) {
		cl := schedulertest.NewVolcanoClient(t, testutils.NewVolcanoPodGroupCRD(true))
		recorder := record.NewFakeRecorder(10)
		cfg := configv1alpha1.SchedulerConfiguration{
			Profiles: []configv1alpha1.SchedulerProfile{
				{Name: configv1alpha1.SchedulerNameKube},
				{Name: configv1alpha1.SchedulerNameKai},
				{Name: configv1alpha1.SchedulerNameVolcano},
				{Name: configv1alpha1.SchedulerNameLPX},
			},
			DefaultProfileName: string(configv1alpha1.SchedulerNameKai),
		}
		reg, err := New(cl, cl, cl.Scheme(), recorder, cfg)
		require.NoError(t, err)
		require.NotNil(t, reg.Get(string(configv1alpha1.SchedulerNameKai)))
		require.NotNil(t, reg.Get(string(configv1alpha1.SchedulerNameKube)))
		require.NotNil(t, reg.Get(string(configv1alpha1.SchedulerNameVolcano)))
		require.NotNil(t, reg.Get(string(configv1alpha1.SchedulerNameLPX)))
		assert.Equal(t, reg.GetDefault(), reg.Get(string(configv1alpha1.SchedulerNameKai)))
		assert.NotContains(t, reg.AllTopologyAware(), string(configv1alpha1.SchedulerNameLPX))
	})

	t.Run("volcano scheduler initialization", func(t *testing.T) {
		cl := schedulertest.NewVolcanoClient(t, testutils.NewVolcanoPodGroupCRD(true))

		recorder := record.NewFakeRecorder(10)
		cfg := configv1alpha1.SchedulerConfiguration{
			Profiles: []configv1alpha1.SchedulerProfile{
				{Name: configv1alpha1.SchedulerNameVolcano},
			},
			DefaultProfileName: string(configv1alpha1.SchedulerNameVolcano),
		}
		reg, err := New(cl, cl, cl.Scheme(), recorder, cfg)
		require.NoError(t, err)
		require.NotNil(t, reg.GetDefault())
		assert.Equal(t, string(configv1alpha1.SchedulerNameVolcano), reg.GetDefault().Name())
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
			name:     "empty string returns nil",
			input:    "",
			expected: nil,
		},
		{
			name:     "whitespace-only string returns nil",
			input:    "   ",
			expected: nil,
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

// TestAll tests All method of registry in isolation using stub backends.
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

// TestGetOrDefault tests that empty name falls back to default and non-empty does a direct lookup.
func TestGetOrDefault(t *testing.T) {
	kubeBackend := testutils.NewFakeSchedulerBackend("kube")
	kaiBackend := testutils.NewFakeSchedulerBackend("kai")
	reg := &registry{
		backends: map[string]scheduler.Backend{
			"kube": kubeBackend,
			"kai":  kaiBackend,
		},
		defaultBackend: kubeBackend,
	}

	assert.Equal(t, kubeBackend, reg.GetOrDefault(""), "empty string returns default backend")
	assert.Equal(t, kubeBackend, reg.GetOrDefault("kube"), "known name returns that backend")
	assert.Equal(t, kaiBackend, reg.GetOrDefault("kai"), "known name returns that backend")
	assert.Nil(t, reg.GetOrDefault("volcano"), "unknown name returns nil")
}

// TestAllTopologyAware tests that only backends implementing TopologyAwareBackend are returned.
func TestAllTopologyAware(t *testing.T) {
	t.Run("mixed backends returns only topology-aware ones", func(t *testing.T) {
		tasBackend := testutils.NewFakeTopologyAwareBackend("kai")
		plainBackend := testutils.NewFakeSchedulerBackend("kube")
		reg := &registry{
			backends: map[string]scheduler.Backend{
				"kai":  tasBackend,
				"kube": plainBackend,
			},
		}
		result := reg.AllTopologyAware()
		assert.Len(t, result, 1)
		assert.Equal(t, tasBackend, result["kai"])
		assert.NotContains(t, result, "kube")
	})

	t.Run("no topology-aware backends returns empty map", func(t *testing.T) {
		reg := &registry{
			backends: map[string]scheduler.Backend{
				"kube": testutils.NewFakeSchedulerBackend("kube"),
			},
		}
		result := reg.AllTopologyAware()
		assert.NotNil(t, result)
		assert.Empty(t, result)
	})

	t.Run("all backends topology-aware returns all", func(t *testing.T) {
		tas1 := testutils.NewFakeTopologyAwareBackend("kai")
		tas2 := testutils.NewFakeTopologyAwareBackend("volcano")
		reg := &registry{
			backends: map[string]scheduler.Backend{
				"kai":     tas1,
				"volcano": tas2,
			},
		}
		result := reg.AllTopologyAware()
		assert.Len(t, result, 2)
		assert.Equal(t, tas1, result["kai"])
		assert.Equal(t, tas2, result["volcano"])
	})

	t.Run("empty registry returns empty map", func(t *testing.T) {
		reg := &registry{backends: make(map[string]scheduler.Backend)}
		result := reg.AllTopologyAware()
		assert.NotNil(t, result)
		assert.Empty(t, result)
	})
}
