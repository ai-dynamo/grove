//go:build e2e

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

package scale

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/measurement"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/utils/portforward"
	"github.com/ai-dynamo/grove/operator/e2e/utils/pprof"
)

const (
	pyroscopeDisabledEnvVar   = "GROVE_E2E_PYROSCOPE_DISABLED"
	pyroscopeNamespaceEnvVar  = "GROVE_E2E_PYROSCOPE_NAMESPACE"
	pyroscopeServiceEnvVar    = "GROVE_E2E_PYROSCOPE_SERVICE"
	pyroscopePortEnvVar       = "GROVE_E2E_PYROSCOPE_PORT"
	defaultPyroscopeNamespace = "pyroscope"
	defaultPyroscopeService   = "pyroscope"
	defaultPyroscopePort      = 4040
)

// pyroscopeConfig holds resolved Pyroscope connection settings.
type pyroscopeConfig struct {
	Disabled  bool
	Namespace string
	Service   string
	Port      int
}

// TestMain manages the lifecycle of the shared cluster for all scale tests.
func TestMain(m *testing.M) {
	ctx := context.Background()
	testctx.Logger = Logger

	sharedCluster := setup.SharedCluster(Logger)
	if err := sharedCluster.Setup(ctx, nil); err != nil {
		Logger.Errorf("failed to setup shared cluster: %s", err)
		os.Exit(1)
	}

	code := m.Run()

	sharedCluster.Teardown()

	os.Exit(code)
}

// loadPyroscopeConfig reads env vars and applies defaults.
func loadPyroscopeConfig() pyroscopeConfig {
	if os.Getenv(pyroscopeDisabledEnvVar) == "true" {
		return pyroscopeConfig{Disabled: true}
	}

	port := defaultPyroscopePort
	if v := os.Getenv(pyroscopePortEnvVar); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			Logger.Infof("WARN: invalid %s=%q, using default %d", pyroscopePortEnvVar, v, defaultPyroscopePort)
		} else {
			port = parsed
		}
	}

	return pyroscopeConfig{
		Namespace: envOrDefault(pyroscopeNamespaceEnvVar, defaultPyroscopeNamespace),
		Service:   envOrDefault(pyroscopeServiceEnvVar, defaultPyroscopeService),
		Port:      port,
	}
}

// envOrDefault returns the env var value or the default if unset/empty.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// setupPprofHook configures async pprof profile downloads after each tracker phase.
// Returns a TimelineOption (nil when disabled) and a cleanup function.
// Best-effort: never aborts the test — logs warnings on failure and returns noop.
func setupPprofHook(ctx context.Context, cl *clients.Clients, runID, diagDir string, cfg pyroscopeConfig) (measurement.TimelineOption, func()) {
	noop := func() {}

	if cfg.Disabled {
		Logger.Info("pprof collection disabled via env var")
		return nil, noop
	}

	logr := Logger.GetLogr()
	session, err := portforward.ForwardService(ctx, cl.RestConfig, cl.Clientset,
		cfg.Namespace, cfg.Service, cfg.Port, portforward.WithLogger(logr))
	if err != nil {
		Logger.Infof("WARN: pprof disabled — port-forward to svc/%s failed: %v", cfg.Service, err)
		return nil, noop
	}

	addr := session.Addr()
	dl := pprof.NewDownloader(
		fmt.Sprintf("http://%s", addr),
		runID,
		pprof.WithOutputDir(diagDir),
		pprof.WithLogger(logr),
	)

	Logger.Infof("pprof enabled via svc/%s → http://%s", cfg.Service, addr)
	return measurement.WithAfterPhaseHookAsync(dl.DownloadForPhase), session.Close
}
