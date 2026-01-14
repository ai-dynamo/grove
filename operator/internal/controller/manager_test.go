// /*
// Copyright 2024 The Grove Authors.
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

package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
)

// TestCreateManagerOptions tests the creation of controller-runtime manager options
// from an operator configuration, ensuring all configuration fields are properly
// mapped to their corresponding manager options.
func TestCreateManagerOptions(t *testing.T) {
	// Test with minimal configuration
	t.Run("minimal configuration", func(t *testing.T) {
		cfg := &configv1alpha1.OperatorConfiguration{
			Server: configv1alpha1.ServerConfiguration{
				Metrics: &configv1alpha1.Server{
					BindAddress: "0.0.0.0",
					Port:        8080,
				},
				HealthProbes: &configv1alpha1.Server{
					BindAddress: "0.0.0.0",
					Port:        8081,
				},
				Webhooks: configv1alpha1.WebhookServer{
					Server: configv1alpha1.Server{
						BindAddress: "0.0.0.0",
						Port:        9443,
					},
					ServerCertDir: "/tmp/certs",
				},
			},
			LeaderElection: configv1alpha1.LeaderElectionConfiguration{
				Enabled:           false,
				ResourceName:      "operator-lock",
				ResourceLock:      "leases",
				LeaseDuration:     metav1.Duration{Duration: 15 * time.Second},
				RenewDeadline:     metav1.Duration{Duration: 10 * time.Second},
				RetryPeriod:       metav1.Duration{Duration: 2 * time.Second},
				ResourceNamespace: "default",
			},
		}

		opts := createManagerOptions(cfg)

		assert.Equal(t, groveclientscheme.Scheme, opts.Scheme)
		assert.Equal(t, "0.0.0.0:8080", opts.Metrics.BindAddress)
		assert.Equal(t, "0.0.0.0:8081", opts.HealthProbeBindAddress)
		assert.False(t, opts.LeaderElection)
		assert.Equal(t, "operator-lock", opts.LeaderElectionID)
		assert.Equal(t, "leases", opts.LeaderElectionResourceLock)
		assert.True(t, opts.LeaderElectionReleaseOnCancel)
		assert.NotNil(t, opts.GracefulShutdownTimeout)
		assert.Equal(t, 5*time.Second, *opts.GracefulShutdownTimeout)
		assert.NotNil(t, opts.Controller.RecoverPanic)
		assert.True(t, *opts.Controller.RecoverPanic)
		assert.NotNil(t, opts.WebhookServer)
	})

	// Test with leader election enabled
	t.Run("leader election enabled", func(t *testing.T) {
		cfg := &configv1alpha1.OperatorConfiguration{
			Server: configv1alpha1.ServerConfiguration{
				Metrics: &configv1alpha1.Server{
					BindAddress: "127.0.0.1",
					Port:        9090,
				},
				HealthProbes: &configv1alpha1.Server{
					BindAddress: "127.0.0.1",
					Port:        9091,
				},
				Webhooks: configv1alpha1.WebhookServer{
					Server: configv1alpha1.Server{
						BindAddress: "0.0.0.0",
						Port:        9443,
					},
					ServerCertDir: "/tmp/certs",
				},
			},
			LeaderElection: configv1alpha1.LeaderElectionConfiguration{
				Enabled:           true,
				ResourceName:      "grove-operator-leader",
				ResourceLock:      "leases",
				LeaseDuration:     metav1.Duration{Duration: 30 * time.Second},
				RenewDeadline:     metav1.Duration{Duration: 20 * time.Second},
				RetryPeriod:       metav1.Duration{Duration: 5 * time.Second},
				ResourceNamespace: "grove-system",
			},
		}

		opts := createManagerOptions(cfg)

		assert.True(t, opts.LeaderElection)
		assert.Equal(t, "grove-operator-leader", opts.LeaderElectionID)
		assert.NotNil(t, opts.LeaseDuration)
		assert.Equal(t, 30*time.Second, *opts.LeaseDuration)
		assert.NotNil(t, opts.RenewDeadline)
		assert.Equal(t, 20*time.Second, *opts.RenewDeadline)
		assert.NotNil(t, opts.RetryPeriod)
		assert.Equal(t, 5*time.Second, *opts.RetryPeriod)
	})

	// Test with profiling enabled
	t.Run("profiling enabled", func(t *testing.T) {
		cfg := &configv1alpha1.OperatorConfiguration{
			Server: configv1alpha1.ServerConfiguration{
				Metrics: &configv1alpha1.Server{
					BindAddress: "0.0.0.0",
					Port:        8080,
				},
				HealthProbes: &configv1alpha1.Server{
					BindAddress: "0.0.0.0",
					Port:        8081,
				},
				Webhooks: configv1alpha1.WebhookServer{
					Server: configv1alpha1.Server{
						BindAddress: "0.0.0.0",
						Port:        9443,
					},
					ServerCertDir: "/tmp/certs",
				},
			},
			LeaderElection: configv1alpha1.LeaderElectionConfiguration{
				Enabled:      false,
				ResourceName: "operator-lock",
				ResourceLock: "leases",
			},
			Debugging: &configv1alpha1.DebuggingConfiguration{
				EnableProfiling: ptr.To(true),
			},
		}

		opts := createManagerOptions(cfg)

		assert.Equal(t, pprofBindAddress, opts.PprofBindAddress)
	})

	// Test with profiling explicitly disabled
	t.Run("profiling disabled", func(t *testing.T) {
		cfg := &configv1alpha1.OperatorConfiguration{
			Server: configv1alpha1.ServerConfiguration{
				Metrics: &configv1alpha1.Server{
					BindAddress: "0.0.0.0",
					Port:        8080,
				},
				HealthProbes: &configv1alpha1.Server{
					BindAddress: "0.0.0.0",
					Port:        8081,
				},
				Webhooks: configv1alpha1.WebhookServer{
					Server: configv1alpha1.Server{
						BindAddress: "0.0.0.0",
						Port:        9443,
					},
					ServerCertDir: "/tmp/certs",
				},
			},
			LeaderElection: configv1alpha1.LeaderElectionConfiguration{
				Enabled:      false,
				ResourceName: "operator-lock",
				ResourceLock: "leases",
			},
			Debugging: &configv1alpha1.DebuggingConfiguration{
				EnableProfiling: ptr.To(false),
			},
		}

		opts := createManagerOptions(cfg)

		assert.Empty(t, opts.PprofBindAddress)
	})

	// Test with no debugging configuration
	t.Run("no debugging configuration", func(t *testing.T) {
		cfg := &configv1alpha1.OperatorConfiguration{
			Server: configv1alpha1.ServerConfiguration{
				Metrics: &configv1alpha1.Server{
					BindAddress: "0.0.0.0",
					Port:        8080,
				},
				HealthProbes: &configv1alpha1.Server{
					BindAddress: "0.0.0.0",
					Port:        8081,
				},
				Webhooks: configv1alpha1.WebhookServer{
					Server: configv1alpha1.Server{
						BindAddress: "0.0.0.0",
						Port:        9443,
					},
					ServerCertDir: "/tmp/certs",
				},
			},
			LeaderElection: configv1alpha1.LeaderElectionConfiguration{
				Enabled:      false,
				ResourceName: "operator-lock",
				ResourceLock: "leases",
			},
		}

		opts := createManagerOptions(cfg)

		assert.Empty(t, opts.PprofBindAddress)
	})
}

// TestGetRestConfig tests the creation of a Kubernetes REST configuration
// from an operator configuration, verifying client connection settings.
func TestGetRestConfig(t *testing.T) {
	// Save original function and restore after test
	originalGetConfigOrDie := ctrl.GetConfigOrDie
	defer func() { ctrl.GetConfigOrDie = originalGetConfigOrDie }()

	// Test with valid configuration
	t.Run("valid configuration", func(t *testing.T) {
		// Mock ctrl.GetConfigOrDie to return a test config
		ctrl.GetConfigOrDie = func() *rest.Config {
			return &rest.Config{
				Host: "https://test-cluster:6443",
			}
		}

		cfg := &configv1alpha1.OperatorConfiguration{
			ClientConnection: configv1alpha1.ClientConnectionConfiguration{
				QPS:                100.0,
				Burst:              200,
				ContentType:        "application/json",
				AcceptContentTypes: "application/json",
			},
		}

		restCfg := getRestConfig(cfg)

		assert.NotNil(t, restCfg)
		assert.Equal(t, float32(100.0), restCfg.QPS)
		assert.Equal(t, 200, restCfg.Burst)
		assert.Equal(t, "application/json", restCfg.ContentType)
		assert.Equal(t, "application/json", restCfg.AcceptContentTypes)
		assert.Equal(t, "https://test-cluster:6443", restCfg.Host)
	})

	// Test with nil configuration
	t.Run("nil configuration", func(t *testing.T) {
		ctrl.GetConfigOrDie = func() *rest.Config {
			return &rest.Config{
				Host:  "https://test-cluster:6443",
				QPS:   50.0,
				Burst: 100,
			}
		}

		restCfg := getRestConfig(nil)

		assert.NotNil(t, restCfg)
		// Should use values from ctrl.GetConfigOrDie
		assert.Equal(t, "https://test-cluster:6443", restCfg.Host)
		assert.Equal(t, float32(50.0), restCfg.QPS)
		assert.Equal(t, 100, restCfg.Burst)
	})
}

// mockManager is a minimal mock implementation of ctrl.Manager for testing.
type mockManager struct {
	ctrlmanager.Manager
	healthChecks  map[string]healthz.Checker
	readyChecks   map[string]healthz.Checker
	webhookServer *mockWebhookServer
}

func (m *mockManager) AddHealthzCheck(name string, check healthz.Checker) error {
	if m.healthChecks == nil {
		m.healthChecks = make(map[string]healthz.Checker)
	}
	m.healthChecks[name] = check
	return nil
}

func (m *mockManager) AddReadyzCheck(name string, check healthz.Checker) error {
	if m.readyChecks == nil {
		m.readyChecks = make(map[string]healthz.Checker)
	}
	m.readyChecks[name] = check
	return nil
}

func (m *mockManager) GetWebhookServer() ctrlwebhook.Server {
	if m.webhookServer == nil {
		m.webhookServer = &mockWebhookServer{}
	}
	return m.webhookServer
}

// mockWebhookServer is a minimal mock implementation of webhook.Server.
type mockWebhookServer struct {
	started bool
	mux     *http.ServeMux
}

func (w *mockWebhookServer) Register(_ string, _ http.Handler) {
	// No-op for testing
}

func (w *mockWebhookServer) Start(_ context.Context) error {
	w.started = true
	return nil
}

func (w *mockWebhookServer) StartedChecker() healthz.Checker {
	return func(_ *http.Request) error {
		if !w.started {
			return errors.New("webhook server not started")
		}
		return nil
	}
}

func (w *mockWebhookServer) NeedLeaderElection() bool {
	return false
}

func (w *mockWebhookServer) WebhookMux() *http.ServeMux {
	if w.mux == nil {
		w.mux = http.NewServeMux()
	}
	return w.mux
}

// TestCreateManager tests the creation of a controller-runtime manager
// with operator configuration.
func TestCreateManager(t *testing.T) {
	// Save original function and restore after test
	originalGetConfigOrDie := ctrl.GetConfigOrDie
	defer func() { ctrl.GetConfigOrDie = originalGetConfigOrDie }()

	// Test with valid configuration
	t.Run("valid configuration", func(t *testing.T) {
		ctrl.GetConfigOrDie = func() *rest.Config {
			return &rest.Config{
				Host: "https://test-cluster:6443",
			}
		}

		cfg := &configv1alpha1.OperatorConfiguration{
			Server: configv1alpha1.ServerConfiguration{
				Metrics: &configv1alpha1.Server{
					BindAddress: "0.0.0.0",
					Port:        8080,
				},
				HealthProbes: &configv1alpha1.Server{
					BindAddress: "0.0.0.0",
					Port:        8081,
				},
				Webhooks: configv1alpha1.WebhookServer{
					Server: configv1alpha1.Server{
						BindAddress: "0.0.0.0",
						Port:        9443,
					},
					ServerCertDir: "/tmp/certs",
				},
			},
			LeaderElection: configv1alpha1.LeaderElectionConfiguration{
				Enabled:      false,
				ResourceName: "operator-lock",
				ResourceLock: "leases",
			},
			ClientConnection: configv1alpha1.ClientConnectionConfiguration{
				QPS:   100.0,
				Burst: 200,
			},
		}

		mgr, err := CreateManager(cfg)
		require.NoError(t, err)
		assert.NotNil(t, mgr)
	})
}

// TestSetupHealthAndReadinessEndpoints tests the setup of health and readiness
// endpoints on the manager, verifying proper registration and behavior.
func TestSetupHealthAndReadinessEndpoints(t *testing.T) {
	// Test successful setup with certificates ready
	t.Run("certificates ready", func(t *testing.T) {
		mgr := &mockManager{}
		certsReadyCh := make(chan struct{})
		close(certsReadyCh) // Certificates are ready

		err := SetupHealthAndReadinessEndpoints(mgr, certsReadyCh)
		require.NoError(t, err)

		// Verify healthz check was added
		assert.Contains(t, mgr.healthChecks, "healthz")
		healthCheck := mgr.healthChecks["healthz"]
		assert.NotNil(t, healthCheck)

		// Test healthz endpoint
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		err = healthCheck(req)
		assert.NoError(t, err)

		// Verify readyz check was added
		assert.Contains(t, mgr.readyChecks, "readyz")
		readyCheck := mgr.readyChecks["readyz"]
		assert.NotNil(t, readyCheck)

		// Test readyz endpoint when webhook is started
		webhookServer := mgr.GetWebhookServer().(*mockWebhookServer)
		webhookServer.started = true
		req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
		err = readyCheck(req)
		assert.NoError(t, err)
	})

	// Test readiness check with certificates not ready
	t.Run("certificates not ready", func(t *testing.T) {
		mgr := &mockManager{}
		certsReadyCh := make(chan struct{}) // Not closed, certificates not ready

		err := SetupHealthAndReadinessEndpoints(mgr, certsReadyCh)
		require.NoError(t, err)

		readyCheck := mgr.readyChecks["readyz"]
		require.NotNil(t, readyCheck)

		// Test readyz endpoint when certificates are not ready
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		err = readyCheck(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "certificates are not ready yet")
	})

	// Test readiness check with webhook not started
	t.Run("webhook not started", func(t *testing.T) {
		mgr := &mockManager{}
		certsReadyCh := make(chan struct{})
		close(certsReadyCh) // Certificates are ready

		err := SetupHealthAndReadinessEndpoints(mgr, certsReadyCh)
		require.NoError(t, err)

		readyCheck := mgr.readyChecks["readyz"]
		require.NotNil(t, readyCheck)

		// Test readyz endpoint when webhook is not started
		webhookServer := mgr.GetWebhookServer().(*mockWebhookServer)
		webhookServer.started = false
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		err = readyCheck(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "webhook server not started")
	})
}

// registerControllersAndWebhooksCase documents how each field drives expectations in TestRegisterControllersAndWebhooks.
// name identifies the scenario and is read during failure output for quick diagnosis.
// waitFn supplies the behavior of certificate readiness waiting to avoid blocking on a real channel.
// controllerErr controls the simulated outcome of controller registration.
// webhookErr controls the simulated outcome of webhook registration.
// expectWebhooksCalled indicates whether webhook registration should be invoked in the scenario.
// expectError indicates whether the function under test should return an error.
// expectedErrFrag, when set, should be contained in the returned error to ensure the correct failure path is exercised.
type registerControllersAndWebhooksCase struct {
	name                 string
	waitFn               func(logr.Logger, chan struct{})
	controllerErr        error
	webhookErr           error
	expectWebhooksCalled bool
	expectError          bool
	expectedErrFrag      string
}

// TestRegisterControllersAndWebhooks verifies that certificate readiness is honored and that controller and webhook
// registration errors are surfaced appropriately.
func TestRegisterControllersAndWebhooks(t *testing.T) {
	testCases := []registerControllersAndWebhooksCase{
		// name: ensures the happy path waits for readiness and registers controllers and webhooks
		{
			name: "registers controllers and webhooks after readiness",
			waitFn: func(_ logr.Logger, ready chan struct{}) {
				close(ready)
			},
			expectWebhooksCalled: true,
		},
		// name: ensures controller registration failures are propagated and webhook registration is skipped
		{
			name: "controller registration failure short-circuits",
			waitFn: func(_ logr.Logger, ready chan struct{}) {
				close(ready)
			},
			controllerErr:        errors.New("controller failure"),
			expectError:          true,
			expectedErrFrag:      "controller failure",
			expectWebhooksCalled: false,
		},
		// name: ensures webhook registration failures bubble up after controllers succeed
		{
			name: "webhook registration failure bubbles up",
			waitFn: func(_ logr.Logger, ready chan struct{}) {
				close(ready)
			},
			webhookErr:           errors.New("webhook failure"),
			expectError:          true,
			expectedErrFrag:      "webhook failure",
			expectWebhooksCalled: true,
		},
	}

	for _, tc := range testCases {
		originalWait := waitTillWebhookCertsReady
		originalRegisterControllers := registerControllersWithMgr
		originalRegisterWebhooks := registerWebhooksWithMgr

		readyCh := make(chan struct{})
		waitCalled := false
		controllersCalled := false
		webhooksCalled := false

		waitTillWebhookCertsReady = func(logger logr.Logger, ch chan struct{}) {
			waitCalled = true
			if tc.waitFn != nil {
				tc.waitFn(logger, ch)
			}
		}
		registerControllersWithMgr = func(_ ctrl.Manager, _ configv1alpha1.ControllerConfiguration, _ configv1alpha1.TopologyAwareSchedulingConfiguration) error {
			controllersCalled = true
			return tc.controllerErr
		}
		registerWebhooksWithMgr = func(_ ctrl.Manager, _ configv1alpha1.AuthorizerConfig, _ configv1alpha1.TopologyAwareSchedulingConfiguration) error {
			webhooksCalled = true
			return tc.webhookErr
		}

		err := RegisterControllersAndWebhooks(&mockManager{}, logr.Discard(), &configv1alpha1.OperatorConfiguration{}, readyCh)
		if tc.expectError {
			require.Error(t, err, tc.name)
			if tc.expectedErrFrag != "" {
				assert.ErrorContains(t, err, tc.expectedErrFrag, tc.name)
			}
		} else {
			require.NoError(t, err, tc.name)
		}

		assert.True(t, waitCalled, tc.name)
		assert.True(t, controllersCalled, tc.name)
		assert.Equal(t, tc.expectWebhooksCalled, webhooksCalled, tc.name)

		waitTillWebhookCertsReady = originalWait
		registerControllersWithMgr = originalRegisterControllers
		registerWebhooksWithMgr = originalRegisterWebhooks
	}
}
