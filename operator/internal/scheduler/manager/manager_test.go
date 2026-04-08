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

package manager

import (
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/record"
)

// TestNewRegistry tests NewRegistry with different scheduler profiles.
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
			reg, err := NewRegistry(cl, cl.Scheme(), recorder, cfg)

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
				assert.Nil(t, reg.Get(""), "Get with empty string should return nil, not default")
			}
		})
	}

	t.Run("multiple profiles default is kai", func(t *testing.T) {
		cl := testutils.CreateDefaultFakeClient(nil)
		recorder := record.NewFakeRecorder(10)
		cfg := configv1alpha1.SchedulerConfiguration{
			Profiles: []configv1alpha1.SchedulerProfile{
				{Name: configv1alpha1.SchedulerNameKube},
				{Name: configv1alpha1.SchedulerNameKai},
			},
			DefaultProfileName: string(configv1alpha1.SchedulerNameKai),
		}
		reg, err := NewRegistry(cl, cl.Scheme(), recorder, cfg)
		require.NoError(t, err)
		require.NotNil(t, reg.Get(string(configv1alpha1.SchedulerNameKai)))
		require.NotNil(t, reg.Get(string(configv1alpha1.SchedulerNameKube)))
		assert.Equal(t, reg.GetDefault(), reg.Get(string(configv1alpha1.SchedulerNameKai)))
	})
}
