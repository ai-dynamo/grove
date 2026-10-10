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

//go:build e2e

package tests

import (
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/testctx"

	"github.com/stretchr/testify/assert"
)

// Test_SG1_ScaleBelowMinAvailableRejectedZeroAllowed verifies the child scale guard: a scale that lands
// between 1 and minAvailable is rejected, while a scale all the way to 0 is allowed, for both a standalone
// PodClique and a PodCliqueScalingGroup. WL1 has pc-a (standalone, minAvailable 2) and sg-x
// (PodCliqueScalingGroup, minAvailable 2).
func Test_SG1_ScaleBelowMinAvailableRejectedZeroAllowed(t *testing.T) {
	ctx := t.Context()

	Logger.Info("1. Initialize a Grove cluster and deploy workload WL1, verify 10 pods")
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("2. A standalone PodClique scale to a partial count below minAvailable is rejected")
	if err := tc.ScalePodClique("workload1-0-pc-a", 1); assert.Error(t, err, "pc-a 2 to 1 must be rejected") {
		assert.Contains(t, err.Error(), "must be either 0 or at least")
	}

	Logger.Info("3. A PodCliqueScalingGroup scale to a partial count below minAvailable is rejected")
	if err := tc.ScalePCSG("workload1-0-sg-x", 1); assert.Error(t, err, "sg-x 2 to 1 must be rejected") {
		assert.Contains(t, err.Error(), "must be either 0 or at least")
	}

	Logger.Info("4. A scale all the way to 0 is allowed for both")
	if err := tc.ScalePodClique("workload1-0-pc-a", 0); err != nil {
		t.Fatalf("scaling a standalone PodClique to 0 must be allowed: %v", err)
	}
	if err := tc.ScalePCSG("workload1-0-sg-x", 0); err != nil {
		t.Fatalf("scaling a PodCliqueScalingGroup to 0 must be allowed: %v", err)
	}

	Logger.Info("🎉 SG-1 completed successfully!")
}
