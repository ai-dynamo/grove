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

var _ func(ctrl.Manager, *configv1alpha1.OperatorConfiguration) error = Setup

func TestSetupRejectsNilConfiguration(t *testing.T) {
	require.ErrorContains(t, Setup(nil, nil), "operator configuration must not be nil")
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

	operatorCfg := &configv1alpha1.OperatorConfiguration{
		Scheduler: configv1alpha1.SchedulerConfiguration{
			Profiles: []configv1alpha1.SchedulerProfile{
				{Name: configv1alpha1.SchedulerNameKai},
				{Name: configv1alpha1.SchedulerNameLPX},
			},
			DefaultProfileName: string(configv1alpha1.SchedulerNameKai),
		},
	}
	configv1alpha1.SetObjectDefaults_OperatorConfiguration(operatorCfg)

	require.NoError(t, Setup(mgr, operatorCfg))
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
