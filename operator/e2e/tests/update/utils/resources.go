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

package utils

import (
	"context"
	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"slices"
)

// ============================================================================
// Common update utilities
// ============================================================================

// GetPCS fetches a PodCliqueSet by name using the K8s client.
func GetPCS(tc *testctx.TestContext, pcsName string) (*grovev1beta1.PodCliqueSet, error) {
	var pcs grovev1beta1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: pcsName, Namespace: tc.Namespace}, &pcs); err != nil {
		return nil, err
	}
	return &pcs, nil
}

// GetClaimedPCSReplicaIndices returns the sorted PCS replica indices that currently have a PodGangMap, which
// identifies the replicas whose resources exist.
func GetClaimedPCSReplicaIndices(tc *testctx.TestContext) ([]int, error) {
	pgmList := &grovev1beta1.PodGangMapList{}
	if err := tc.Client.List(tc.Ctx, pgmList, client.InNamespace(tc.Namespace), client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}); err != nil {
		return nil, err
	}
	indices := make([]int, 0, len(pgmList.Items))
	for i := range pgmList.Items {
		replicaIndex, err := kubeutils.GetPodCliqueSetReplicaIndex(pgmList.Items[i].ObjectMeta)
		if err != nil {
			return nil, err
		}
		indices = append(indices, replicaIndex)
	}
	slices.Sort(indices)
	return indices, nil
}

// WaitForClaimedPCSReplicaIndices waits until the claimed PCS replica indices equal the expected set.
func WaitForClaimedPCSReplicaIndices(tc *testctx.TestContext, expectedReplicaIndices []int) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(context.Context) (bool, error) {
		claimed, err := GetClaimedPCSReplicaIndices(tc)
		if err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return slices.Equal(claimed, expectedReplicaIndices), nil
	})
}

// GetReplicaPodGangMap fetches the PodGangMap object of the given PCS replica.
func GetReplicaPodGangMap(tc *testctx.TestContext, pcsReplicaIndex int) (*grovev1beta1.PodGangMap, error) {
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex})
	var pgm grovev1beta1.PodGangMap
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pgmName}, &pgm); err != nil {
		return nil, err
	}
	return &pgm, nil
}

// PcsgFQNForReplica returns the fully qualified PodCliqueScalingGroup name for the given PCS replica.
func PcsgFQNForReplica(tc *testctx.TestContext, pcsgConfigName string, pcsReplicaIndex int) string {
	return apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex}, pcsgConfigName)
}
