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

package validation

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// warningRecorder collects the admission warnings the API server returns to the client.
type warningRecorder struct {
	mu       sync.Mutex
	warnings []string
}

func (w *warningRecorder) HandleWarningHeaderWithContext(_ context.Context, _ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warnings = append(w.warnings, text)
}

func (w *warningRecorder) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	warnings := w.warnings
	w.warnings = nil
	return warnings
}

// TestAdmission_UnenabledSchedulerBackend runs the webhook behind a real API server and covers
// the request that blocked enabling a new backend: Argo CD's server-side apply dry run of an
// existing ClusterTopologyBinding that gains a reference to a backend the running operator lacks.
func TestAdmission_UnenabledSchedulerBackend(t *testing.T) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "..", "..", "api", "core", "v1alpha1", "crds")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			ValidatingWebhooks: []*admissionregistrationv1.ValidatingWebhookConfiguration{newTestValidatingWebhookConfiguration()},
		},
	}
	cfg, err := testEnv.Start()
	require.NoError(t, err, "envtest is required; run via make test-unit")
	t.Cleanup(func() { require.NoError(t, testEnv.Stop()) })

	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

	webhookOpts := testEnv.WebhookInstallOptions
	mgr, err := manager.New(cfg, manager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookOpts.LocalServingHost,
			Port:    webhookOpts.LocalServingPort,
			CertDir: webhookOpts.LocalServingCertDir,
		}),
	})
	require.NoError(t, err)
	require.NoError(t, newTestHandler().RegisterWithManager(mgr))

	ctx, cancel := context.WithCancel(context.Background())
	mgrDone := make(chan error, 1)
	go func() { mgrDone <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-mgrDone)
	})
	waitForWebhookServer(t, webhookOpts)

	recorder := &warningRecorder{}
	clientCfg := rest.CopyConfig(cfg)
	clientCfg.WarningHandlerWithContext = recorder
	cl, err := client.New(clientCfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	levels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
	}
	kaiRef := grovecorev1alpha1.SchedulerTopologyBinding{SchedulerName: string(configv1alpha1.SchedulerNameKai), TopologyReference: "kai-topology"}
	unenabledRef := grovecorev1alpha1.SchedulerTopologyBinding{SchedulerName: string(configv1alpha1.SchedulerNameKueue), TopologyReference: "kueue-topology"}

	t.Run("server-side apply dry run adding an unenabled backend is admitted with a warning", func(t *testing.T) {
		existing := newTestClusterTopology(levels, []grovecorev1alpha1.SchedulerTopologyBinding{kaiRef})
		existing.Name = "host-scale"
		require.NoError(t, cl.Create(ctx, existing))
		assert.Empty(t, recorder.take())

		desired := newTestClusterTopology(levels, []grovecorev1alpha1.SchedulerTopologyBinding{kaiRef, unenabledRef})
		desired.Name = existing.Name
		desired.TypeMeta = metav1.TypeMeta{APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(), Kind: "ClusterTopologyBinding"}
		//nolint:staticcheck // Argo CD's server-side diff issues exactly this typed apply patch.
		require.NoError(t, cl.Patch(ctx, desired, client.Apply, client.DryRunAll, client.ForceOwnership, client.FieldOwner("argocd-controller")))

		warnings := recorder.take()
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], "spec.schedulerTopologyReferences[1].schedulerName")
		assert.Contains(t, warnings[0], `scheduler backend "kueue" is not enabled in Grove`)

		persisted := &grovecorev1alpha1.ClusterTopologyBinding{}
		require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(existing), persisted))
		assert.Equal(t, []grovecorev1alpha1.SchedulerTopologyBinding{kaiRef}, persisted.Spec.SchedulerTopologyBindings)
	})

	t.Run("create referencing an unenabled backend is admitted with a warning", func(t *testing.T) {
		ct := newTestClusterTopology(levels, []grovecorev1alpha1.SchedulerTopologyBinding{unenabledRef})
		ct.Name = "rack-scale"
		require.NoError(t, cl.Create(ctx, ct))

		warnings := recorder.take()
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], `scheduler backend "kueue" is not enabled in Grove`)
	})

	t.Run("reference to an enabled backend without topology support is still denied", func(t *testing.T) {
		ct := newTestClusterTopology(levels, []grovecorev1alpha1.SchedulerTopologyBinding{
			{SchedulerName: string(configv1alpha1.SchedulerNameKube), TopologyReference: "kube-topology"},
		})
		ct.Name = "kube-scale"
		err := cl.Create(ctx, ct)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `admission webhook "clustertopology.validating.webhooks.grove.io" denied the request`)
		assert.Contains(t, err.Error(), "scheduler backend does not implement topology-aware scheduling")
	})
}

func newTestValidatingWebhookConfiguration() *admissionregistrationv1.ValidatingWebhookConfiguration {
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: Name},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name: "clustertopology.validating.webhooks.grove.io",
			// envtest joins the serving address and this path with a slash.
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{Path: ptr.To(strings.TrimPrefix(webhookPath, "/"))},
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{grovecorev1alpha1.SchemeGroupVersion.Group},
					APIVersions: []string{grovecorev1alpha1.SchemeGroupVersion.Version},
					Resources:   []string{"clustertopologybindings"},
				},
			}},
			FailurePolicy:           ptr.To(admissionregistrationv1.Fail),
			SideEffects:             ptr.To(admissionregistrationv1.SideEffectClassNone),
			AdmissionReviewVersions: []string{"v1"},
		}},
	}
}

func waitForWebhookServer(t *testing.T, opts envtest.WebhookInstallOptions) {
	t.Helper()
	addr := net.JoinHostPort(opts.LocalServingHost, fmt.Sprint(opts.LocalServingPort))
	dialer := &net.Dialer{Timeout: time.Second}
	require.Eventually(t, func() bool {
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test-only readiness probe
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 30*time.Second, 100*time.Millisecond)
}
