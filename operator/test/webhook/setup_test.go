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

	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var _ func(ctrl.Manager, *configv1beta1.OperatorConfiguration) error = Setup

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

	operatorCfg := &configv1beta1.OperatorConfiguration{
		Scheduler: configv1beta1.SchedulerConfiguration{
			Profiles: []configv1beta1.SchedulerProfile{
				{Name: configv1beta1.SchedulerNameKai},
				{Name: configv1beta1.SchedulerNameLPX},
			},
			DefaultProfileName: string(configv1beta1.SchedulerNameKai),
		},
	}
	configv1beta1.SetObjectDefaults_OperatorConfiguration(operatorCfg)

	require.NoError(t, Setup(mgr, operatorCfg))
	require.ElementsMatch(t, []string{
		"/convert",
		"/webhooks/v1beta1/default-podcliqueset",
		"/webhooks/v1beta1/validate-clustertopology",
		"/webhooks/v1beta1/validate-podcliqueset",
		"/webhooks/v1beta1/validate-podclique",
		"/webhooks/v1beta1/validate-podcliquescalinggroup",
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
