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

package portforward

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

const portForwardReadyTimeout = 10 * time.Second

// Session is an active port-forward tunnel.
// The zero value is invalid; always construct via ForwardService.
type Session struct {
	// LocalPort is the kernel-assigned local port for the tunnel.
	LocalPort int
	stop      chan struct{}
	once      sync.Once
}

// Close tears down the port-forward. Safe to call multiple times.
func (s *Session) Close() {
	s.once.Do(func() { close(s.stop) })
}

// Addr returns "localhost:<LocalPort>" for use as an HTTP base URL.
func (s *Session) Addr() string {
	return fmt.Sprintf("localhost:%d", s.LocalPort)
}

// sessionCfg holds functional option state for ForwardService.
type sessionCfg struct {
	logger logr.Logger
}

// Option is a functional option for ForwardService.
type Option func(*sessionCfg)

// WithLogger sets the logger for the session.
func WithLogger(l logr.Logger) Option {
	return func(c *sessionCfg) { c.logger = l }
}

// ForwardService creates a port-forward to the first ready pod backing serviceName.
// Uses port 0 so the kernel assigns the local port; reads the actual port via
// pf.GetPorts() after readyChan fires. No reconnect — tunnel is single-use for
// the test lifetime.
func ForwardService(
	ctx context.Context,
	restCfg *rest.Config,
	cl client.Reader,
	namespace string,
	serviceName string,
	remotePort int,
	opts ...Option,
) (*Session, error) {
	cfg := &sessionCfg{logger: logr.Discard()}
	for _, opt := range opts {
		opt(cfg)
	}

	podName, err := resolvePodViaEndpoints(ctx, cl, namespace, serviceName)
	if err != nil {
		return nil, fmt.Errorf("resolve pod for service %q: %w", serviceName, err)
	}

	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, podName)

	transport, upgrader, err := spdy.RoundTripperFor(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create SPDY round tripper: %w", err)
	}

	targetURL, err := parsePortForwardURL(restCfg.Host, path)
	if err != nil {
		return nil, fmt.Errorf("parse port-forward URL: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, targetURL)

	stopChan := make(chan struct{})
	readyChan := make(chan struct{})

	ports := []string{fmt.Sprintf("0:%d", remotePort)}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}

	pf, err := portforward.New(dialer, ports, stopChan, readyChan, out, errOut)
	if err != nil {
		close(stopChan)
		return nil, fmt.Errorf("create port forwarder: %w", err)
	}

	errChan := make(chan error, 1)
	go func() {
		if err := pf.ForwardPorts(); err != nil {
			errChan <- err
		}
	}()

	select {
	case <-readyChan:
		cfg.logger.V(1).Info("port-forward established", "pod", podName, "remotePort", remotePort)
	case err := <-errChan:
		close(stopChan)
		return nil, fmt.Errorf("port-forward to %s/%s failed: %w", namespace, podName, err)
	case <-time.After(portForwardReadyTimeout):
		close(stopChan)
		select {
		case pfErr := <-errChan:
			return nil, fmt.Errorf("port-forward to %s/%s failed (after timeout): %w", namespace, podName, pfErr)
		default:
			return nil, fmt.Errorf("timeout waiting for port-forward to %s/%s", namespace, podName)
		}
	case <-ctx.Done():
		close(stopChan)
		return nil, ctx.Err()
	}

	if errOut.Len() > 0 {
		cfg.logger.Info("port-forward stderr", "output", errOut.String())
	}

	assignedPorts, err := pf.GetPorts()
	if err != nil {
		close(stopChan)
		return nil, fmt.Errorf("get assigned ports: %w", err)
	}
	if len(assignedPorts) == 0 {
		close(stopChan)
		return nil, fmt.Errorf("no ports assigned by port forwarder")
	}

	session := &Session{
		LocalPort: int(assignedPorts[0].Local),
		stop:      stopChan,
	}

	// Bridge ctx cancellation to session.Close() so the tunnel is torn down
	// when the test context is cancelled.
	go func() {
		select {
		case <-ctx.Done():
			session.Close()
		case <-stopChan:
		}
	}()

	return session, nil
}

// resolvePodViaEndpoints returns the name of the first ready pod backing the
// given service, using the Endpoints API (returns only Ready pods from
// Subsets[*].Addresses, not NotReadyAddresses).
func resolvePodViaEndpoints(
	ctx context.Context,
	cl client.Reader,
	namespace, serviceName string,
) (string, error) {
	var endpoints corev1.Endpoints
	if err := cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: serviceName}, &endpoints); err != nil {
		return "", fmt.Errorf("get endpoints for %q: %w", serviceName, err)
	}
	if len(endpoints.Subsets) == 0 {
		return "", fmt.Errorf("no subsets in endpoints for service %q", serviceName)
	}
	for _, subset := range endpoints.Subsets {
		for _, addr := range subset.Addresses {
			if addr.TargetRef == nil {
				continue
			}
			return addr.TargetRef.Name, nil
		}
	}
	return "", fmt.Errorf("no ready pods found in endpoints for service %q", serviceName)
}

// parsePortForwardURL builds the full port-forward URL from the REST API host and pod path.
func parsePortForwardURL(host, path string) (*url.URL, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	u.Path = path
	return u, nil
}
