//go:build e2e

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

package setup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/log"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	restfake "k8s.io/client-go/rest/fake"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestGroveConfigToHelmValuesEnablesHealthProbes(t *testing.T) {
	values, err := (&GroveConfig{}).toHelmValues()
	require.NoError(t, err)

	enabled, found, err := unstructured.NestedBool(values, "config", "server", "healthProbes", "enable")
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, enabled)

	port, found, err := unstructured.NestedFieldNoCopy(values, "config", "server", "healthProbes", "port")
	require.NoError(t, err)
	require.True(t, found)
	assert.EqualValues(t, DefaultHealthProbePort, port)
}

func TestGroveConfigToHelmValuesScheduling(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		values, err := (&GroveConfig{
			Scheduler: &configv1alpha1.SchedulerConfiguration{
				DefaultProfileName: string(configv1alpha1.SchedulerNameKube),
				Profiles: []configv1alpha1.SchedulerProfile{{
					Name:   configv1alpha1.SchedulerNameKube,
					Config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":false}`)},
				}},
			},
			TopologyAwareScheduling: &configv1alpha1.TopologyAwareSchedulingConfiguration{Enabled: enabled},
		}).toHelmValues()
		require.NoError(t, err)
		config, found, err := unstructured.NestedMap(values, "config")
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, map[string]interface{}{
			"defaultProfileName": "default-scheduler",
			"profiles": []interface{}{map[string]interface{}{
				"name":   "default-scheduler",
				"config": map[string]interface{}{"gangScheduling": false},
			}},
		}, config["scheduler"])
		assert.Equal(t, map[string]interface{}{"enabled": enabled}, config["topologyAwareScheduling"])
	}

	values, err := (&GroveConfig{}).toHelmValues()
	require.NoError(t, err)
	assert.NotContains(t, values["config"], "scheduler")
	assert.NotContains(t, values["config"], "topologyAwareScheduling")
}

func TestWaitForWebhookReadyRequiresConsecutiveSuccesses(t *testing.T) {
	const (
		testTimeout  = time.Second
		testInterval = time.Millisecond
	)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/grove-system/services/https:grove-operator:webhooks/proxy/webhooks/default-podcliqueset" {
			http.NotFound(w, r)
			return
		}
		switch calls.Add(1) {
		case 1, 2, 4:
			http.Error(w, "webhook unavailable", http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	err := waitForWebhookReady(t.Context(), &rest.Config{Host: server.URL}, testTimeout, testInterval, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(7), calls.Load())
}

func TestUpdateGroveConfigurationWaitsForNewDeployment(t *testing.T) {
	const (
		testTimeout       = 20 * time.Second
		pollInterval      = 20 * time.Millisecond
		observationWindow = time.Second
	)
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run the Helm rollout regression")
	}
	testEnv := &envtest.Environment{ControlPlaneStartTimeout: testTimeout, ControlPlaneStopTimeout: testTimeout}
	config, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testEnv.Stop()) })

	// Helm reads a kubeconfig before applying the supplied REST credentials.
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"test": {Server: config.Host}},
		Contexts:       map[string]*clientcmdapi.Context{"test": {Cluster: "test"}},
		CurrentContext: "test",
	}, kubeconfig))
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Setenv("HELM_DRIVER", "secret")

	var probes atomic.Int32
	config.Wrap(func(transport http.RoundTripper) http.RoundTripper {
		return restfake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/proxy/webhooks/default-podcliqueset") {
				probes.Add(1)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return transport.RoundTrip(req)
		}).Transport
	})
	cl, err := k8sclient.New(config)
	require.NoError(t, err)
	require.NoError(t, cl.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: OperatorNamespace}}))
	logger := log.NewTestLoggerWithOutput(log.ErrorLevel, io.Discard)
	chartDir, err := GetGroveChartDir()
	require.NoError(t, err)
	chartVersion, err := GetGroveChartVersion(chartDir)
	require.NoError(t, err)
	// The configuration update enables probes, changing the real chart's Pod template.
	_, err = InstallHelmChart(&HelmInstallConfig{
		RestConfig: config, ReleaseName: OperatorDeploymentName, Namespace: OperatorNamespace,
		ChartRef: chartDir, ChartVersion: chartVersion, Logger: logger,
		Values: map[string]interface{}{"config": map[string]interface{}{"server": map[string]interface{}{
			"healthProbes": map[string]interface{}{"enable": false},
		}}},
	})
	require.NoError(t, err)
	deployment := &appsv1.Deployment{}
	key := client.ObjectKey{Namespace: OperatorNamespace, Name: OperatorDeploymentName}
	require.NoError(t, cl.Get(t.Context(), key, deployment))
	require.Nil(t, deployment.Spec.Template.Spec.Containers[0].ReadinessProbe)
	oldPod := completeTestRollout(t.Context(), t, cl, deployment, "old")
	oldGeneration := deployment.Generation

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- UpdateGroveConfiguration(ctx, config, chartDir, &GroveConfig{}, logger)
	}()
	// Release Helm's wait even when the blocking assertion fails.
	t.Cleanup(func() {
		require.NoError(t, cl.Get(ctx, key, deployment))
		require.NoError(t, cl.Delete(ctx, oldPod, client.GracePeriodSeconds(0)))
		completeTestRollout(ctx, t, cl, deployment, "new")
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(testTimeout):
			t.Error("configuration update did not finish after the new Deployment became ready")
		}
		assert.Positive(t, probes.Load(), "successful rollout must still probe the webhook")
	})

	require.Eventually(t, func() bool {
		return cl.Get(t.Context(), key, deployment) == nil && deployment.Generation > oldGeneration
	}, testTimeout, pollInterval, "Helm must apply the changed Pod template")
	require.NotNil(t, deployment.Spec.Template.Spec.Containers[0].ReadinessProbe)
	require.Less(t, deployment.Status.ObservedGeneration, deployment.Generation)
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(oldPod), oldPod))
	require.Equal(t, corev1.ConditionTrue, oldPod.Status.Conditions[0].Status)
	require.Never(t, func() bool { return probes.Load() > 0 }, observationWindow, pollInterval,
		"an old Ready Pod must not allow the configuration update to reach the webhook probe")
}

// Envtest has no Deployment controller or kubelet. Publish the state that a
// completed rollout would produce, without running a container.
func completeTestRollout(ctx context.Context, t *testing.T, cl client.Client, deployment *appsv1.Deployment, suffix string) *corev1.Pod {
	t.Helper()
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: deployment.Name + "-" + suffix, Namespace: deployment.Namespace,
			Labels:          deployment.Spec.Selector.MatchLabels,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(deployment, appsv1.SchemeGroupVersion.WithKind("Deployment"))},
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: deployment.Spec.Replicas, Selector: deployment.Spec.Selector.DeepCopy(), Template: *deployment.Spec.Template.DeepCopy(),
		},
	}
	require.NoError(t, cl.Create(ctx, rs))
	rs.Status = appsv1.ReplicaSetStatus{ObservedGeneration: rs.Generation, Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	require.NoError(t, cl.Status().Update(ctx, rs))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: rs.Name, Namespace: deployment.Namespace, Labels: deployment.Spec.Template.Labels},
		Spec:       *deployment.Spec.Template.Spec.DeepCopy(),
	}
	require.NoError(t, cl.Create(ctx, pod))
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	require.NoError(t, cl.Status().Update(ctx, pod))
	deployment.Status = appsv1.DeploymentStatus{
		ObservedGeneration: deployment.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
	}
	require.NoError(t, cl.Status().Update(ctx, deployment))
	return pod
}
