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

package automnnvl

import (
	"context"
	"os"
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/setup"
)

// TestMain manages the lifecycle of the cluster for all MNNVL tests.
// MNNVL tests always expect an existing cluster set up by the setup scripts
// in hack/e2e-autoMNNVL/.
func TestMain(m *testing.M) {
	ctx := context.Background()

	sharedCluster := setup.SharedCluster(logger)
	if err := sharedCluster.Setup(ctx, nil); err != nil {
		logger.Errorf("failed to setup shared cluster: %s", err)
		os.Exit(1)
	}

	// Run tests
	code := m.Run()

	// No teardown - cluster is managed externally
	os.Exit(code)
}
