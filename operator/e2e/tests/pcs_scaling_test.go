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

package tests

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const sparseScalingWorkloadName = "pcs-sparse-scaling"

// Test_PCSS1_AvailabilityAwareScalingReplicaSelection verifies availability-aware scale-in selection
// and lowest-hole reuse:
//  1. Deploy a five-replica PCS and wait for all Pods to become Ready.
//  2. Cordon the worker nodes and delete the Pod for PCS replica 2 so it becomes unavailable.
//  3. Scale the PCS from five to three replicas.
//  4. Verify the unavailable replica 2 and the highest-index available replica 4 are removed, retaining [0, 1, 3].
//  5. Wait for scale-in resource cleanup to complete.
//  6. Scale the PCS from three to four replicas.
//  7. Verify scale-out reuses the lowest completed hole, producing [0, 1, 2, 3].
//  8. Wait for scale-out progress to complete.
func Test_PCSS1_AvailabilityAwareScalingReplicaSelection(t *testing.T) {
	ctx := context.Background()
	Logger.Info("1. Deploy a five-replica PCS and wait for all Pods to become Ready")
	tc, cleanup := testctx.PrepareTest(ctx, t, 5,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         sparseScalingWorkloadName,
			YAMLPath:     "../yaml/pcs-sparse-scaling.yaml",
			Namespace:    "default",
			ExpectedPods: 5,
		}),
	)
	defer cleanup()

	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err)
	require.NoError(t, tc.WaitForReadyPods(5))
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: sparseScalingWorkloadName}, pcs))
	require.Nil(t, pcs.Status.ScalingProgress, "initial provisioning must not be recorded as scaling progress")

	Logger.Info("2. Cordon the worker nodes and delete the Pod for PCS replica 2 so it becomes unavailable")
	pods, err := tc.ListPods()
	require.NoError(t, err)
	target := findReadyPodFromPodClique(pods, sparseScalingWorkloadName+"-2-worker")
	require.NotNil(t, target)
	workerNodes, err := tc.GetWorkerNodes()
	require.NoError(t, err)
	tc.CordonNodes(workerNodes)
	defer tc.UncordonNodes(workerNodes)
	require.NoError(t, tc.Client.Delete(ctx, target))
	require.NoError(t, waitForPCLQMinAvailableBreach(tc, sparseScalingWorkloadName+"-2-worker"))

	Logger.Info("3. Scale the PCS from five to three replicas")
	require.NoError(t, tc.ScalePCS(sparseScalingWorkloadName, 3))

	Logger.Info("4. Verify replicas 2 and 4 are removed, retaining replica identities [0, 1, 3]")
	require.NoError(t, waitForPCSReplicaIndices(tc, sparseScalingWorkloadName, sets.New(0, 1, 3)))

	Logger.Info("5. Wait for scale-in resource cleanup to complete")
	require.NoError(t, waitForPCSScalingProgressToComplete(tc, sparseScalingWorkloadName))

	Logger.Info("6. Scale the PCS from three to four replicas")
	require.NoError(t, tc.ScalePCS(sparseScalingWorkloadName, 4))

	Logger.Info("7. Verify scale-out reuses index 2, producing replica identities [0, 1, 2, 3]")
	require.NoError(t, waitForPCSReplicaIndices(tc, sparseScalingWorkloadName, sets.New(0, 1, 2, 3)))

	Logger.Info("8. Wait for scale-out progress to complete")
	require.NoError(t, waitForPCSScalingProgressToComplete(tc, sparseScalingWorkloadName))
}

func waitForPCSScalingProgressToComplete(tc *testctx.TestContext, pcsName string) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pcs := &grovecorev1alpha1.PodCliqueSet{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pcsName}, pcs); err != nil {
			return false, err
		}
		return pcs.Status.ScalingProgress == nil, nil
	})
}

func waitForPCSReplicaIndices(tc *testctx.TestContext, pcsName string, expected sets.Set[int]) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pclqs := &grovecorev1alpha1.PodCliqueList{}
		if err := tc.Client.List(ctx, pclqs,
			client.InNamespace(tc.Namespace),
			client.MatchingLabels{apicommon.LabelPartOfKey: pcsName},
		); err != nil {
			return false, err
		}
		observed := sets.New[int]()
		for i := range pclqs.Items {
			if !pclqs.Items[i].DeletionTimestamp.IsZero() {
				continue
			}
			raw, ok := pclqs.Items[i].Labels[apicommon.LabelPodCliqueSetReplicaIndex]
			if !ok {
				return false, fmt.Errorf("PodClique %s is missing PCS replica index label", pclqs.Items[i].Name)
			}
			pcsReplicaIndex, err := strconv.Atoi(raw)
			if err != nil {
				return false, fmt.Errorf("PodClique %s has invalid PCS replica index %q: %w", pclqs.Items[i].Name, raw, err)
			}
			observed.Insert(pcsReplicaIndex)
		}
		return observed.Equal(expected), nil
	})
}

func waitForPCLQMinAvailableBreach(tc *testctx.TestContext, pclqName string) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pclq := &grovecorev1alpha1.PodClique{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pclqName}, pclq); err != nil {
			return false, err
		}
		condition := meta.FindStatusCondition(pclq.Status.Conditions, apiconstants.ConditionTypeMinAvailableBreached)
		return condition != nil && condition.Status == metav1.ConditionTrue, nil
	})
}
