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

package podgang

import (
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend"
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
			expectedName: "kai-scheduler",
		},
		{
			name:         "create reconciler with kube backend",
			backendType:  "kube",
			expectedName: "default-scheduler",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := testutils.CreateDefaultFakeClient(nil)
			recorder := record.NewFakeRecorder(10)
			profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKube, Default: true}
			if tt.backendType == "kai" {
				profile = configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai, Default: true}
			}
			_ = schedulerbackend.Initialize(cl, cl.Scheme(), recorder, configv1alpha1.SchedulerConfiguration{
				Profiles: []configv1alpha1.SchedulerProfile{profile},
			})
			mgr := &testutils.FakeManager{Client: cl, Scheme: cl.Scheme()}
			reconciler, err := NewReconciler(mgr)
			require.NoError(t, err)
			require.NotNil(t, reconciler)
			assert.Equal(t, cl, reconciler.Client)
			assert.Equal(t, cl.Scheme(), reconciler.Scheme)
			def := schedulerbackend.GetDefault()
			require.NotNil(t, def)
			assert.Contains(t, []string{"kai-scheduler", "kube-scheduler"}, def.Name())
		})
	}
}

// TestReconcilerFields tests that Reconciler fields are set correctly when constructed via NewReconciler.
func TestReconcilerFields(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	_ = schedulerbackend.Initialize(cl, cl.Scheme(), recorder, configv1alpha1.SchedulerConfiguration{
		Profiles: []configv1alpha1.SchedulerProfile{{Name: configv1alpha1.SchedulerNameKai, Default: true}},
	})
	mgr := &testutils.FakeManager{Client: cl, Scheme: cl.Scheme()}
	reconciler, err := NewReconciler(mgr)
	require.NoError(t, err)
	require.NotNil(t, reconciler)
	assert.NotNil(t, reconciler.Client, "Client should not be nil")
	assert.NotNil(t, reconciler.Scheme, "Scheme should not be nil")
}
