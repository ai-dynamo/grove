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

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeOperatorConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []byte
		wantErr  bool
		assertFn func(t *testing.T, cfg *OperatorConfiguration)
	}{
		{
			name: "happy path: explicit values parsed",
			input: []byte(`apiVersion: operator.config.grove.io/v1alpha1
kind: OperatorConfiguration
runtimeClientConnection:
  qps: 200
  burst: 300
controllers:
  podCliqueSet:
    concurrentSyncs: 2
  podCliqueScalingGroup:
    concurrentSyncs: 3
  podClique:
    concurrentSyncs: 4
`),
			assertFn: func(t *testing.T, cfg *OperatorConfiguration) {
				assert.InDelta(t, float32(200), cfg.ClientConnection.QPS, 0.01)
				assert.Equal(t, 300, cfg.ClientConnection.Burst)
				require.NotNil(t, cfg.Controllers.PodCliqueSet.ConcurrentSyncs)
				assert.Equal(t, 2, *cfg.Controllers.PodCliqueSet.ConcurrentSyncs)
				require.NotNil(t, cfg.Controllers.PodCliqueScalingGroup.ConcurrentSyncs)
				assert.Equal(t, 3, *cfg.Controllers.PodCliqueScalingGroup.ConcurrentSyncs)
				require.NotNil(t, cfg.Controllers.PodClique.ConcurrentSyncs)
				assert.Equal(t, 4, *cfg.Controllers.PodClique.ConcurrentSyncs)
			},
		},
		{
			name: "defaults applied for minimal YAML",
			input: []byte(`apiVersion: operator.config.grove.io/v1alpha1
kind: OperatorConfiguration
`),
			assertFn: func(t *testing.T, cfg *OperatorConfiguration) {
				assert.InDelta(t, float32(100), cfg.ClientConnection.QPS, 0.01)
				assert.Equal(t, 120, cfg.ClientConnection.Burst)
				require.NotNil(t, cfg.Controllers.PodCliqueSet.ConcurrentSyncs)
				assert.Equal(t, 10, *cfg.Controllers.PodCliqueSet.ConcurrentSyncs)
				require.NotNil(t, cfg.Controllers.PodCliqueScalingGroup.ConcurrentSyncs)
				assert.Equal(t, 5, *cfg.Controllers.PodCliqueScalingGroup.ConcurrentSyncs)
				require.NotNil(t, cfg.Controllers.PodClique.ConcurrentSyncs)
				assert.Equal(t, 10, *cfg.Controllers.PodClique.ConcurrentSyncs)
			},
		},
		{
			name:    "malformed YAML returns error",
			input:   []byte(`{this is not valid yaml: [`),
			wantErr: true,
		},
		{
			name: "non-default values preserved",
			input: []byte(`apiVersion: operator.config.grove.io/v1alpha1
kind: OperatorConfiguration
runtimeClientConnection:
  qps: 500
  burst: 600
controllers:
  podCliqueSet:
    concurrentSyncs: 4
  podCliqueScalingGroup:
    concurrentSyncs: 4
  podClique:
    concurrentSyncs: 4
`),
			assertFn: func(t *testing.T, cfg *OperatorConfiguration) {
				assert.InDelta(t, float32(500), cfg.ClientConnection.QPS, 0.01)
				assert.Equal(t, 600, cfg.ClientConnection.Burst)
				require.NotNil(t, cfg.Controllers.PodCliqueSet.ConcurrentSyncs)
				assert.Equal(t, 4, *cfg.Controllers.PodCliqueSet.ConcurrentSyncs)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := DecodeOperatorConfig(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg)
			if tc.assertFn != nil {
				tc.assertFn(t, cfg)
			}
		})
	}
}
