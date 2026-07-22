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

package podcliqueset

import (
	"net/http"
	"testing"

	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var _ func(ctrl.Manager) error = Setup

func TestSetup(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	server := &recordingServer{
		Server:   webhook.NewServer(webhook.Options{}),
		handlers: make(map[string]http.Handler),
	}
	mgr := &testutils.FakeManager{
		Client:        cl,
		Scheme:        cl.Scheme(),
		Logger:        logr.Discard(),
		WebhookServer: server,
	}

	require.NoError(t, Setup(mgr))
	require.ElementsMatch(t, []string{
		"/webhooks/default-podcliqueset",
		"/webhooks/validate-podcliqueset",
	}, registeredPaths(server.handlers))
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
