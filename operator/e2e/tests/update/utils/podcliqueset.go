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

//go:build e2e

// Package utils provides shared assertions and resource helpers for update e2e tests.
package utils

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"

	"k8s.io/apimachinery/pkg/types"
)

// PCSGenerationHash returns the PodCliqueSet's current generation hash from its status.
func PCSGenerationHash(t *testing.T, tc *testctx.TestContext) string {
	t.Helper()
	var pcs grovev1beta1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name}, &pcs); err != nil {
		t.Fatalf("Failed to get PodCliqueSet %s: %v", tc.Workload.Name, err)
	}
	if pcs.Status.CurrentGenerationHash == nil {
		t.Fatalf("PodCliqueSet %s has no CurrentGenerationHash", tc.Workload.Name)
	}
	return *pcs.Status.CurrentGenerationHash
}

// GetPCS fetches a PodCliqueSet by name using the K8s client.
func GetPCS(tc *testctx.TestContext, pcsName string) (*grovev1beta1.PodCliqueSet, error) {
	var pcs grovev1beta1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: pcsName, Namespace: tc.Namespace}, &pcs); err != nil {
		return nil, err
	}
	return &pcs, nil
}

// StandaloneCliqueFQN returns the PodClique name for a standalone clique in PCS replica 0.
func StandaloneCliqueFQN(tc *testctx.TestContext, cliqueName string) string {
	return apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: 0}, cliqueName)
}

// PCSGFQN returns the PodCliqueScalingGroup name for a config in PCS replica 0.
func PCSGFQN(tc *testctx.TestContext, pcsgConfigName string) string {
	return PCSGFQNForReplica(tc, pcsgConfigName, 0)
}

// PCSGFQNForReplica returns the fully qualified PodCliqueScalingGroup name for the given PCS replica.
func PCSGFQNForReplica(tc *testctx.TestContext, pcsgConfigName string, pcsReplicaIndex int) string {
	return apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex}, pcsgConfigName)
}
