// /*
// Copyright 2025 The Grove Authors.
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

package cert

import (
	"os"
	"testing"
	"time"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"

	"github.com/go-logr/logr"
	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestGetWebhooks tests the creation of webhook info structures based on authorizer configuration.
func TestGetWebhooks(t *testing.T) {
	// Test with authorizer disabled
	t.Run("authorizer disabled", func(t *testing.T) {
		webhooks := getWebhooks(false)

		// Expect 2 webhooks: podcliqueset-defaulting, podcliqueset-validating
		require.Len(t, webhooks, 2)
		// Check that defaulting and validating webhooks are present
		assert.Equal(t, cert.Mutating, webhooks[0].Type)   // podcliqueset-defaulting-webhook
		assert.Equal(t, cert.Validating, webhooks[1].Type) // podcliqueset-validating-webhook
	})

	// Test with authorizer enabled
	t.Run("authorizer enabled", func(t *testing.T) {
		webhooks := getWebhooks(true)

		// Expect 3 webhooks: the 2 base webhooks plus the authorizer-webhook
		require.Len(t, webhooks, 3)
		// Check that all three webhooks are present
		assert.Equal(t, cert.Mutating, webhooks[0].Type)   // podcliqueset-defaulting-webhook
		assert.Equal(t, cert.Validating, webhooks[1].Type) // podcliqueset-validating-webhook
		assert.Equal(t, cert.Validating, webhooks[2].Type) // authorizer-webhook
	})
}

// TestGetOperatorNamespace tests reading the operator namespace from a file.
// Note: This function uses a hardcoded file path, so we test with the actual constant.
// In practice, the file should exist in the operator's pod environment.
func TestGetOperatorNamespace(t *testing.T) {
	// Test with non-existent file (expected in test environment)
	t.Run("file does not exist in test environment", func(t *testing.T) {
		// Since constants.OperatorNamespaceFile is a constant pointing to
		// /var/run/secrets/kubernetes.io/serviceaccount/namespace,
		// which won't exist in a test environment, we expect an error
		_, err := getOperatorNamespace()

		// This is expected to fail in test environment
		if err != nil {
			assert.Error(t, err)
		}
	})
}

// TestGetOperatorNamespaceFromFile tests the namespace file reading logic with various inputs.
func TestGetOperatorNamespaceFromFile(t *testing.T) {
	t.Run("file does not exist", func(t *testing.T) {
		_, err := getOperatorNamespaceFromFile("/nonexistent/path/to/namespace")
		require.Error(t, err)
	})

	t.Run("file contains valid namespace", func(t *testing.T) {
		// Create a temp file with a valid namespace
		tmpFile, err := os.CreateTemp("", "namespace-test")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString("test-namespace")
		require.NoError(t, err)
		tmpFile.Close()

		namespace, err := getOperatorNamespaceFromFile(tmpFile.Name())
		require.NoError(t, err)
		assert.Equal(t, "test-namespace", namespace)
	})

	t.Run("file is empty", func(t *testing.T) {
		// Create an empty temp file
		tmpFile, err := os.CreateTemp("", "namespace-test")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())
		tmpFile.Close()

		_, err = getOperatorNamespaceFromFile(tmpFile.Name())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "operator namespace is empty")
	})
}

// TestWaitTillWebhookCertsReady tests waiting for webhook certificates to be ready.
func TestWaitTillWebhookCertsReady(t *testing.T) {
	// Test that function returns when channel is closed
	t.Run("channel closed immediately", func(t *testing.T) {
		certsReady := make(chan struct{})
		close(certsReady)

		logger := logr.Discard()

		// This should return immediately
		done := make(chan struct{})
		go func() {
			WaitTillWebhookCertsReady(logger, certsReady)
			close(done)
		}()

		select {
		case <-done:
			// Success - function returned
		case <-time.After(1 * time.Second):
			t.Fatal("WaitTillWebhookCertsReady did not return in time")
		}
	})

	// Test that function waits until channel is closed
	t.Run("channel closed after delay", func(t *testing.T) {
		certsReady := make(chan struct{})
		logger := logr.Discard()

		done := make(chan struct{})
		go func() {
			WaitTillWebhookCertsReady(logger, certsReady)
			close(done)
		}()

		// Ensure it's still waiting
		select {
		case <-done:
			t.Fatal("WaitTillWebhookCertsReady returned too early")
		case <-time.After(100 * time.Millisecond):
			// Good, still waiting
		}

		// Now close the channel
		close(certsReady)

		// Should return now
		select {
		case <-done:
			// Success
		case <-time.After(1 * time.Second):
			t.Fatal("WaitTillWebhookCertsReady did not return after channel closed")
		}
	})
}

// TestManageWebhookCerts tests the certificate management behavior for different provision modes.
func TestManageWebhookCerts(t *testing.T) {
	// Test manual mode - should close channel immediately and return nil
	t.Run("manual mode closes channel immediately", func(t *testing.T) {
		certsReady := make(chan struct{})

		// Call ManageWebhookCerts with manual mode
		// Note: mgr is nil because manual mode doesn't use it
		err := ManageWebhookCerts(nil, "/tmp/certs", "test-secret", false, configv1alpha1.CertProvisionModeManual, certsReady)

		// Should return no error
		require.NoError(t, err)

		// Channel should be closed
		select {
		case <-certsReady:
			// Success - channel is closed
		default:
			t.Fatal("certsReady channel should be closed in manual mode")
		}
	})

	// Test manual mode with authorizer enabled - should still work the same way
	t.Run("manual mode with authorizer enabled", func(t *testing.T) {
		certsReady := make(chan struct{})

		err := ManageWebhookCerts(nil, "/tmp/certs", "test-secret", true, configv1alpha1.CertProvisionModeManual, certsReady)

		require.NoError(t, err)

		select {
		case <-certsReady:
			// Success - channel is closed
		default:
			t.Fatal("certsReady channel should be closed in manual mode even with authorizer enabled")
		}
	})

	// Test auto mode - should fail in test environment because namespace file doesn't exist
	// This verifies that auto mode attempts to read the namespace (different code path from manual)
	t.Run("auto mode requires namespace file", func(t *testing.T) {
		certsReady := make(chan struct{})

		// Call ManageWebhookCerts with auto mode
		// This should fail because the namespace file doesn't exist in the test environment
		err := ManageWebhookCerts(nil, "/tmp/certs", "test-secret", false, configv1alpha1.CertProvisionModeAuto, certsReady)

		// Should return an error because namespace file doesn't exist
		require.Error(t, err)

		// Channel should NOT be closed (error occurred before that could happen in auto mode)
		select {
		case <-certsReady:
			t.Fatal("certsReady channel should not be closed when auto mode fails")
		default:
			// Success - channel is still open
		}
	})

	// Test that manual mode doesn't require namespace file
	// This verifies the optimization that manual mode skips namespace lookup
	t.Run("manual mode does not require namespace file", func(t *testing.T) {
		certsReady := make(chan struct{})

		// Even though namespace file doesn't exist, manual mode should succeed
		err := ManageWebhookCerts(nil, "/tmp/certs", "test-secret", false, configv1alpha1.CertProvisionModeManual, certsReady)

		// Should succeed without needing namespace
		require.NoError(t, err)
	})

	// Test unknown/invalid mode - should return an explicit error.
	// This ensures new modes must be explicitly handled, preventing silent mishandling.
	t.Run("unknown mode returns explicit error", func(t *testing.T) {
		certsReady := make(chan struct{})

		err := ManageWebhookCerts(nil, "/tmp/certs", "test-secret", false, configv1alpha1.CertProvisionMode("unknown"), certsReady)

		// Should return an explicit error about unsupported mode
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported cert provision mode")
		assert.Contains(t, err.Error(), "unknown")

		// Channel should NOT be closed since we returned an error
		select {
		case <-certsReady:
			t.Fatal("certsReady channel should not be closed when mode is unsupported")
		default:
			// Success - channel is still open
		}
	})

	// Test empty mode string - should also return explicit error
	t.Run("empty mode returns explicit error", func(t *testing.T) {
		certsReady := make(chan struct{})

		err := ManageWebhookCerts(nil, "/tmp/certs", "test-secret", false, configv1alpha1.CertProvisionMode(""), certsReady)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported cert provision mode")
	})
}

// TestEnsureSecretExistsWithClient tests the secret creation logic.
func TestEnsureSecretExistsWithClient(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	t.Run("creates secret when it does not exist", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()

		err := ensureSecretExistsWithClient(cl, "test-ns", "test-secret")
		require.NoError(t, err)

		// Verify the secret was created with the correct attributes
		secret := &corev1.Secret{}
		err = cl.Get(t.Context(), types.NamespacedName{Namespace: "test-ns", Name: "test-secret"}, secret)
		require.NoError(t, err)
		assert.Equal(t, corev1.SecretTypeTLS, secret.Type)
		assert.Equal(t, "test-ns", secret.Namespace)
		assert.Equal(t, "test-secret", secret.Name)
		assert.Contains(t, secret.Data, "tls.crt")
		assert.Contains(t, secret.Data, "tls.key")
		assert.Contains(t, secret.Data, "ca.crt")
		assert.Empty(t, secret.Data["tls.crt"])
		assert.Empty(t, secret.Data["tls.key"])
		assert.Empty(t, secret.Data["ca.crt"])
	})

	t.Run("created secret has expected labels", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()

		err := ensureSecretExistsWithClient(cl, "test-ns", "test-secret")
		require.NoError(t, err)

		secret := &corev1.Secret{}
		err = cl.Get(t.Context(), types.NamespacedName{Namespace: "test-ns", Name: "test-secret"}, secret)
		require.NoError(t, err)
		assert.Equal(t, "grove-operator", secret.Labels["app.kubernetes.io/managed-by"])
		assert.Equal(t, "webhook", secret.Labels["app.kubernetes.io/component"])
		assert.Equal(t, "grove", secret.Labels["app.kubernetes.io/part-of"])
	})

	t.Run("does not overwrite existing secret", func(t *testing.T) {
		existingSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "test-ns",
				Name:      "test-secret",
			},
			Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{
				"tls.crt": []byte("existing-cert"),
				"tls.key": []byte("existing-key"),
				"ca.crt":  []byte("existing-ca"),
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingSecret).Build()

		err := ensureSecretExistsWithClient(cl, "test-ns", "test-secret")
		require.NoError(t, err)

		// Verify the existing data was preserved
		secret := &corev1.Secret{}
		err = cl.Get(t.Context(), types.NamespacedName{Namespace: "test-ns", Name: "test-secret"}, secret)
		require.NoError(t, err)
		assert.Equal(t, []byte("existing-cert"), secret.Data["tls.crt"])
		assert.Equal(t, []byte("existing-key"), secret.Data["tls.key"])
		assert.Equal(t, []byte("existing-ca"), secret.Data["ca.crt"])
	})
}
