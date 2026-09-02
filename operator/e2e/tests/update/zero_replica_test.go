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

package update

import (
	"context"
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
)

func setupIdleUpdateTest(t *testing.T) (*testctx.TestContext, func()) {
	t.Helper()
	const workloadName = "workload-idle-wake"

	ctx := context.Background()
	tc, cleanup := testctx.PrepareTest(ctx, t, 1,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         workloadName,
			YAMLPath:     "../../yaml/workload-idle-wake.yaml",
			Namespace:    "default",
			ExpectedPods: 0,
		}),
	)
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		cleanup()
		t.Fatalf("Failed to deploy idle workload: %v", err)
	}

	workloadManager := workload.NewWorkloadManager(tc.Client, tests.Logger)
	if _, err := workloadManager.WaitForPodClique(
		ctx, tc.Namespace, workloadName+"-0-worker", tc.Timeout, tc.Interval,
	); err != nil {
		cleanup()
		t.Fatalf("Failed to wait for idle PodClique: %v", err)
	}

	return tc, cleanup
}
