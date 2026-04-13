//go:build e2e

package scale

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/measurement"
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

	dl := pprof.NewDownloader(
		fmt.Sprintf("http://%s", session.Addr()),
		runID,
		pprof.WithOutputDir(diagDir),
		pprof.WithLogger(logr),
	)

	Logger.Infof("pprof enabled via svc/%s → http://%s", cfg.Service, session.Addr())
	return measurement.WithAfterPhaseHookAsync(dl.DownloadForPhase), session.Close
}
