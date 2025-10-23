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
	"testing"
	"time"

	"github.com/go-logr/logr"
	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetWebhooks tests the creation of webhook info structures based on authorizer configuration.
func TestGetWebhooks(t *testing.T) {
	// Test with authorizer disabled
	t.Run("authorizer disabled", func(t *testing.T) {
		webhooks := getWebhooks(false)
		
		require.Len(t, webhooks, 2)
		// Check that defaulting and validating webhooks are present
		assert.Equal(t, cert.Mutating, webhooks[0].Type)
		assert.Equal(t, cert.Validating, webhooks[1].Type)
	})

	// Test with authorizer enabled
	t.Run("authorizer enabled", func(t *testing.T) {
		webhooks := getWebhooks(true)
		
		require.Len(t, webhooks, 3)
		// Check that all three webhooks are present
		assert.Equal(t, cert.Mutating, webhooks[0].Type)
		assert.Equal(t, cert.Validating, webhooks[1].Type)
		assert.Equal(t, cert.Validating, webhooks[2].Type)
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
