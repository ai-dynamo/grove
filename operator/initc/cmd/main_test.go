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

package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetupSignalHandler tests the signal handling context setup.
func TestSetupSignalHandler(t *testing.T) {
	// Basic functionality test - context should be created without cancellation
	ctx := setupSignalHandler()

	require.NotNil(t, ctx, "Context should not be nil")

	// Context should not be cancelled initially
	select {
	case <-ctx.Done():
		require.FailNow(t, "Context should not be cancelled initially")
	default:
		// Expected behavior
	}

	// Context should have a cancel function available (indirectly tested)
	// We can't easily test the signal handling without potentially affecting the test process
	assert.NotNil(t, ctx, "Context should be properly initialized")
}

// TestSetupSignalHandlerCancellation tests that the context is cancelled on signal.
func TestSetupSignalHandlerCancellation(t *testing.T) {
	ctx := setupSignalHandler()

	// Send SIGTERM to ourselves to test cancellation
	process := os.Process{Pid: os.Getpid()}
	err := process.Signal(syscall.SIGTERM)
	require.NoError(t, err, "Should be able to send signal to self")

	// Wait for context to be cancelled
	select {
	case <-ctx.Done():
		// Expected behavior - context should be cancelled
		assert.Equal(t, context.Canceled, ctx.Err(), "Context should be cancelled")
	case <-time.After(100 * time.Millisecond):
		require.FailNow(t, "Context should have been cancelled within timeout")
	}
}
