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
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	sparseScalingWorkloadName    = "pcs-sparse-scaling"
	pcsScalingCrashloopStageName = "pod-crashloop-pcs-scaling-r4"
	pcsScalingCrashloopStagePath = "../yaml/kwok/pod-crashloop-pcs-scaling-r4.yaml"
)

// Test_PCSS1_AvailabilityAwareScalingReplicaSelection verifies availability-aware scale-in selection
// and lowest-hole reuse:
//  1. Deploy a five-replica PCS and wait for all Pods to become Ready.
//  2. Replace replica 4's Pod with a scheduled but not Ready Pod.
//  3. Cordon the worker nodes and delete replica 2's Pod so its replacement remains unscheduled.
//  4. Scale the PCS from five to four replicas.
//  5. Verify replica 2 is removed before the scheduled but unavailable replica 4, retaining [0, 1, 3, 4].
//  6. Wait for the first scale-in resource cleanup to complete.
//  7. Scale the PCS from four to three replicas.
//  8. Verify replica 4 is removed before available replicas, retaining [0, 1, 3].
//  9. Wait for the second scale-in resource cleanup to complete.
//  10. Scale the PCS from three to four replicas.
//  11. Verify scale-out reuses the lowest completed hole, producing [0, 1, 2, 3].
//  12. Wait for scale-out progress to complete.
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
	pcs := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: sparseScalingWorkloadName}, pcs))
	require.Nil(t, pcs.Status.ScalingProgress, "initial provisioning must not be recorded as scaling progress")

	Logger.Info("2. Replace replica 4's Pod with a scheduled but not Ready Pod")
	require.NoError(t, kwok.ApplyStage(ctx, tc.Client, pcsScalingCrashloopStagePath))
	defer func() {
		if err := kwok.DeleteStage(ctx, tc.Client, pcsScalingCrashloopStageName); err != nil {
			Logger.Warnf("cleanup: delete PCS scaling crash-loop KWOK stage: %v", err)
		}
	}()
	pods, err := tc.ListPods()
	require.NoError(t, err)
	replica4Pod := findReadyPodFromPodClique(pods, sparseScalingWorkloadName+"-4-worker")
	require.NotNil(t, replica4Pod)
	require.NoError(t, tc.Client.Delete(ctx, replica4Pod))
	require.NoError(t, waitForPCLQReplicaCounts(tc, sparseScalingWorkloadName+"-4-worker", 1, 0))

	Logger.Info("3. Cordon the worker nodes and delete replica 2's Pod so its replacement remains unscheduled")
	pods, err = tc.ListPods()
	require.NoError(t, err)
	replica2Pod := findReadyPodFromPodClique(pods, sparseScalingWorkloadName+"-2-worker")
	require.NotNil(t, replica2Pod)
	workerNodes, err := tc.GetWorkerNodes()
	require.NoError(t, err)
	tc.CordonNodes(workerNodes)
	defer tc.UncordonNodes(workerNodes)
	require.NoError(t, tc.Client.Delete(ctx, replica2Pod))
	require.NoError(t, waitForPCLQReplicaCounts(tc, sparseScalingWorkloadName+"-2-worker", 0, 0))
	require.NoError(t, waitForPCLQMinAvailableBreach(tc, sparseScalingWorkloadName+"-2-worker"))

	Logger.Info("4. Scale the PCS from five to four replicas")
	require.NoError(t, tc.ScalePCS(sparseScalingWorkloadName, 4))

	Logger.Info("5. Verify replica 2 is removed before the scheduled but unavailable replica 4, retaining [0, 1, 3, 4]")
	require.NoError(t, waitForPCSReplicaIndices(tc, sparseScalingWorkloadName, sets.New(0, 1, 3, 4)))

	Logger.Info("6. Wait for the first scale-in resource cleanup to complete")
	require.NoError(t, waitForPCSScalingProgressToComplete(tc, sparseScalingWorkloadName))

	Logger.Info("7. Scale the PCS from four to three replicas")
	require.NoError(t, tc.ScalePCS(sparseScalingWorkloadName, 3))

	Logger.Info("8. Verify replica 4 is removed before available replicas, retaining [0, 1, 3]")
	require.NoError(t, waitForPCSReplicaIndices(tc, sparseScalingWorkloadName, sets.New(0, 1, 3)))

	Logger.Info("9. Wait for the second scale-in resource cleanup to complete")
	require.NoError(t, waitForPCSScalingProgressToComplete(tc, sparseScalingWorkloadName))

	Logger.Info("10. Scale the PCS from three to four replicas")
	require.NoError(t, tc.ScalePCS(sparseScalingWorkloadName, 4))

	Logger.Info("11. Verify scale-out reuses index 2, producing replica identities [0, 1, 2, 3]")
	require.NoError(t, waitForPCSReplicaIndices(tc, sparseScalingWorkloadName, sets.New(0, 1, 2, 3)))

	Logger.Info("12. Wait for scale-out progress to complete")
	require.NoError(t, waitForPCSScalingProgressToComplete(tc, sparseScalingWorkloadName))
}

func waitForPCSScalingProgressToComplete(tc *testctx.TestContext, pcsName string) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pcs := &grovecorev1beta1.PodCliqueSet{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pcsName}, pcs); err != nil {
			return false, err
		}
		return pcs.Status.ScalingProgress == nil, nil
	})
}

func waitForPCSReplicaIndices(tc *testctx.TestContext, pcsName string, expected sets.Set[int]) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pclqs := &grovecorev1beta1.PodCliqueList{}
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

func waitForPCLQReplicaCounts(tc *testctx.TestContext, pclqName string, scheduledReplicas, readyReplicas int32) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pclq := &grovecorev1beta1.PodClique{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pclqName}, pclq); err != nil {
			return false, err
		}
		return pclq.Status.ScheduledReplicas == scheduledReplicas && pclq.Status.ReadyReplicas == readyReplicas, nil
	})
}

func waitForPCLQMinAvailableBreach(tc *testctx.TestContext, pclqName string) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pclq := &grovecorev1beta1.PodClique{}
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pclqName}, pclq); err != nil {
			return false, err
		}
		condition := meta.FindStatusCondition(pclq.Status.Conditions, apiconstants.ConditionTypeMinAvailableBreached)
		return condition != nil && condition.Status == metav1.ConditionTrue, nil
	})
}
