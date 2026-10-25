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

package config

import (
	"testing"
	"time"

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeOperatorConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        []byte
		wantErr      bool
		wantWarnings int
		assertFn     func(t *testing.T, cfg *configv1beta1.OperatorConfiguration)
	}{
		{
			name: "happy path: explicit values parsed",
			input: []byte(`apiVersion: operator.config.grove.io/v1beta1
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
			assertFn: func(t *testing.T, cfg *configv1beta1.OperatorConfiguration) {
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
			input: []byte(`apiVersion: operator.config.grove.io/v1beta1
kind: OperatorConfiguration
`),
			assertFn: func(t *testing.T, cfg *configv1beta1.OperatorConfiguration) {
				assert.InDelta(t, float32(100), cfg.ClientConnection.QPS, 0.01)
				assert.Equal(t, 120, cfg.ClientConnection.Burst)
				require.NotNil(t, cfg.Controllers.PodCliqueSet.ConcurrentSyncs)
				assert.Equal(t, 10, *cfg.Controllers.PodCliqueSet.ConcurrentSyncs)
				require.NotNil(t, cfg.Controllers.PodCliqueScalingGroup.ConcurrentSyncs)
				assert.Equal(t, 5, *cfg.Controllers.PodCliqueScalingGroup.ConcurrentSyncs)
				require.NotNil(t, cfg.Controllers.PodClique.ConcurrentSyncs)
				assert.Equal(t, 10, *cfg.Controllers.PodClique.ConcurrentSyncs)
				assert.Equal(t, []configv1beta1.SchedulerProfile{{Name: configv1beta1.SchedulerNameKube}}, cfg.Scheduler.Profiles)
			},
		},
		{
			name:    "malformed YAML returns error",
			input:   []byte(`{this is not valid yaml: [`),
			wantErr: true,
		},
		{
			name: "unknown version returns error",
			input: []byte(`apiVersion: operator.config.grove.io/v9
kind: OperatorConfiguration
`),
			wantErr: true,
		},
		{
			name: "deprecated v1alpha1 is converted and defaulted, with a warning",
			input: []byte(`apiVersion: operator.config.grove.io/v1alpha1
kind: OperatorConfiguration
runtimeClientConnection:
  qps: 500
  burst: 600
leaderElection:
  enabled: true
  leaseDuration: 30s
  resourceName: my-lock
  resourceNamespace: grove-system
server:
  webhooks:
    port: 9443
scheduler:
  profiles:
  - name: kai-scheduler
  defaultProfileName: kai-scheduler
topologyAwareScheduling:
  enabled: true
`),
			wantWarnings: 1,
			assertFn: func(t *testing.T, cfg *configv1beta1.OperatorConfiguration) {
				assert.Equal(t, configv1beta1.SchemeGroupVersion.String(), cfg.APIVersion)
				assert.InDelta(t, float32(500), cfg.ClientConnection.QPS, 0.01)
				assert.Equal(t, 600, cfg.ClientConnection.Burst)
				assert.True(t, cfg.LeaderElection.Enabled)
				assert.Equal(t, 30*time.Second, cfg.LeaderElection.LeaseDuration.Duration)
				assert.Equal(t, 10*time.Second, cfg.LeaderElection.RenewDeadline.Duration)
				assert.Equal(t, "my-lock", cfg.LeaderElection.ResourceName)
				assert.Equal(t, "grove-system", cfg.LeaderElection.ResourceNamespace)
				assert.Equal(t, 9443, cfg.Server.Webhooks.Port)
				assert.Equal(t, 2751, cfg.Server.HealthProbes.Port)
				assert.Equal(t, string(configv1beta1.SchedulerNameKai), cfg.Scheduler.DefaultProfileName)
				assert.Equal(t, []configv1beta1.SchedulerProfile{{Name: configv1beta1.SchedulerNameKai}, {Name: configv1beta1.SchedulerNameKube}}, cfg.Scheduler.Profiles)
				assert.True(t, cfg.TopologyAwareScheduling.Enabled)
				require.NotNil(t, cfg.Controllers.PodClique.ConcurrentSyncs)
				assert.Equal(t, 10, *cfg.Controllers.PodClique.ConcurrentSyncs)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, warnings, err := DecodeOperatorConfig(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Len(t, warnings, tc.wantWarnings)
			if tc.assertFn != nil {
				tc.assertFn(t, cfg)
			}
		})
	}
}
