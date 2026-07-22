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

package webhook

import (
	"net/http"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var _ func(ctrl.Manager, Options) error = Setup

func TestNewOperatorConfiguration(t *testing.T) {
	tests := []struct {
		name            string
		options         Options
		wantProfiles    []configv1alpha1.SchedulerName
		wantDefault     configv1alpha1.SchedulerName
		wantErrContains string
	}{
		{
			name:         "defaults to Kubernetes",
			wantProfiles: []configv1alpha1.SchedulerName{configv1alpha1.SchedulerNameKube},
			wantDefault:  configv1alpha1.SchedulerNameKube,
		},
		{
			name: "enables optional backends",
			options: Options{
				KAI:     true,
				Volcano: true,
				LPX:     true,
			},
			wantProfiles: []configv1alpha1.SchedulerName{
				configv1alpha1.SchedulerNameKube,
				configv1alpha1.SchedulerNameKai,
				configv1alpha1.SchedulerNameVolcano,
				configv1alpha1.SchedulerNameLPX,
			},
			wantDefault: configv1alpha1.SchedulerNameKube,
		},
		{
			name: "selects an enabled default backend",
			options: Options{
				KAI:     true,
				Default: configv1alpha1.SchedulerNameKai,
			},
			wantProfiles: []configv1alpha1.SchedulerName{
				configv1alpha1.SchedulerNameKube,
				configv1alpha1.SchedulerNameKai,
			},
			wantDefault: configv1alpha1.SchedulerNameKai,
		},
		{
			name: "rejects a disabled default backend",
			options: Options{
				Default: configv1alpha1.SchedulerNameVolcano,
			},
			wantErrContains: "default scheduler \"volcano\" is not enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			operatorCfg, err := newOperatorConfiguration(tt.options)
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantDefault, configv1alpha1.SchedulerName(operatorCfg.Scheduler.DefaultProfileName))

			profiles := make([]configv1alpha1.SchedulerName, 0, len(operatorCfg.Scheduler.Profiles))
			for _, profile := range operatorCfg.Scheduler.Profiles {
				profiles = append(profiles, profile.Name)
			}
			require.ElementsMatch(t, tt.wantProfiles, profiles)
		})
	}
}

func TestSetup(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	server := &recordingServer{
		Server:   webhook.NewServer(webhook.Options{}),
		handlers: make(map[string]http.Handler),
	}
	mgr := &testManager{
		FakeManager: &testutils.FakeManager{
			Client:        cl,
			Scheme:        cl.Scheme(),
			Logger:        logr.Discard(),
			WebhookServer: server,
		},
		config: &rest.Config{Host: "https://127.0.0.1"},
	}

	require.NoError(t, Setup(mgr, Options{
		KAI:     true,
		LPX:     true,
		Default: configv1alpha1.SchedulerNameKai,
	}))
	require.ElementsMatch(t, []string{
		"/webhooks/default-podcliqueset",
		"/webhooks/validate-clustertopology",
		"/webhooks/validate-podcliqueset",
	}, registeredPaths(server.handlers))
}

type testManager struct {
	*testutils.FakeManager
	config *rest.Config
}

func (m *testManager) GetConfig() *rest.Config {
	return m.config
}

func (m *testManager) GetEventRecorderFor(string) record.EventRecorder {
	return record.NewFakeRecorder(10)
}

type recordingServer struct {
	webhook.Server
	handlers map[string]http.Handler
}

func (s *recordingServer) Register(path string, handler http.Handler) {
	s.handlers[path] = handler
}

func registeredPaths(handlers map[string]http.Handler) []string {
	paths := make([]string, 0, len(handlers))
	for path := range handlers {
		paths = append(paths, path)
	}
	return paths
}
