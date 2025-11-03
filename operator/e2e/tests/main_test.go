//go:build e2e

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

// Package tests contains end-to-end tests for the Grove operator.
//
// These tests are disabled by default due to the 'e2e' build tag above.
// To run these tests, use:
//
//	go test -tags=e2e ./e2e_testing/tests/...
//
// Without the -tags=e2e flag, these tests will be skipped entirely.
package tests

import (
	"context"
	"os"
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/setup"
)

// TestMain manages the lifecycle of the shared cluster for all tests
func TestMain(m *testing.M) {
	ctx := context.Background()

	// Setup shared cluster once for all tests
	sharedCluster := setup.SharedCluster(logger)
	if err := sharedCluster.Setup(ctx, testImages); err != nil {
		logger.Errorf("failed to setup shared cluster: %s", err)
		os.Exit(1)
	}

	// Run tests
	code := m.Run()

	// Teardown shared cluster
	sharedCluster.Teardown()

	os.Exit(code)
}
