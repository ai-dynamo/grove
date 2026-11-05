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
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	scaleInCostWorkloadName       = "pcs-scale-in-cost"
	scaleInCostCrashloopStageName = "pod-crashloop-pcs-scale-in-cost-r2"
	scaleInCostCrashloopStagePath = "../yaml/kwok/pod-crashloop-pcs-scale-in-cost-r2.yaml"
	// scaleInCostMark is the cost a drain controller writes on the pods it wants gone first.
	scaleInCostMark = "-1000000"
	// scaleInCostPodsPerReplica is the worker PodClique's replicas and minAvailable in the workload manifest.
	scaleInCostPodsPerReplica = 2
)

// Test_SIC1_CostSteersPodCliqueSetScaleIn verifies that a per-pod deletion cost steers which PodCliqueSet
// replica a scale-in removes, that the operator reads it only when configured to, and that a cost never
// outranks availability:
//  1. Deploy a three-replica PCS (two pods per replica) and wait for all Pods to become Ready.
//  2. With the default cost source (none), mark one pod of replica 1 and, once every replica reports its pods
//     ready, scale the PCS to two replicas. A scale-in sent before the PodClique statuses catch up with the
//     pods would find a replica still counted as unavailable and remove it ahead of every available one.
//  3. Verify the mark is ignored: replica 2, the highest available index, is removed, retaining [0, 1].
//  4. Scale the PCS back to three replicas and wait for replica 2 to be recreated and Ready.
//  5. Switch the operator's scale-in cost source to pod-deletion-cost.
//  6. Scale the PCS to two replicas.
//  7. Verify the still-marked replica 1 is removed, retaining [0, 2], and the selection event carries its cost.
//  8. Replace one pod of replica 2 with a scheduled but not Ready pod, making replica 2 unavailable.
//  9. Mark both pods of replica 0, then prove the operator has read the marks before they are relied on: scale
//     replica 0's PodClique to three pods and back to two, and verify a marked pod leaves while the unmarked,
//     newest pod stays. Replica 0 keeps one marked pod, so the replica's cost is still the mark.
//  10. Scale the PCS to one replica.
//  11. Verify the unavailable replica 2 is removed ahead of the marked, available replica 0, retaining [0].
func Test_SIC1_CostSteersPodCliqueSetScaleIn(t *testing.T) {
	ctx := context.Background()
	Logger.Info("1. Deploy a three-replica PCS with two pods per replica and wait for all Pods to become Ready")
	tc, cleanup := testctx.PrepareTest(ctx, t, 6,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         scaleInCostWorkloadName,
			YAMLPath:     "../yaml/pcs-scale-in-cost.yaml",
			Namespace:    "default",
			ExpectedPods: 6,
		}),
	)
	defer cleanup()

	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err)
	require.NoError(t, tc.WaitForReadyPods(6))

	Logger.Info("2. With the default cost source, mark one pod of replica 1 and scale the PCS to two replicas")
	require.NoError(t, markPodsOfPCSReplica(tc, 1, 1))
	require.NoError(t, waitForPCSReplicasAvailable(tc, scaleInCostWorkloadName, 0, 1, 2))
	require.NoError(t, tc.ScalePCS(scaleInCostWorkloadName, 2))

	Logger.Info("3. Verify the mark is ignored: replica 2 is removed, retaining [0, 1]")
	require.NoError(t, waitForPCSReplicaIndices(tc, scaleInCostWorkloadName, sets.New(0, 1)))
	require.NoError(t, waitForPCSScalingProgressToComplete(tc, scaleInCostWorkloadName))

	Logger.Info("4. Scale the PCS back to three replicas and wait for replica 2 to be recreated and Ready")
	require.NoError(t, tc.ScalePCS(scaleInCostWorkloadName, 3))
	require.NoError(t, waitForPCSReplicaIndices(tc, scaleInCostWorkloadName, sets.New(0, 1, 2)))
	require.NoError(t, waitForPCSScalingProgressToComplete(tc, scaleInCostWorkloadName))
	require.NoError(t, tc.WaitForReadyPods(6))
	require.NoError(t, waitForPCSReplicasAvailable(tc, scaleInCostWorkloadName, 0, 1, 2))

	Logger.Info("5. Switch the operator's scale-in cost source to pod-deletion-cost")
	setScaleInCostSource(t, tc, configv1beta1.ScaleInCostSourcePodDeletionCost)

	Logger.Info("6. Scale the PCS to two replicas")
	require.NoError(t, tc.ScalePCS(scaleInCostWorkloadName, 2))

	Logger.Info("7. Verify the marked replica 1 is removed, retaining [0, 2], and the selection event carries its cost")
	require.NoError(t, waitForPCSReplicaIndices(tc, scaleInCostWorkloadName, sets.New(0, 2)))
	require.NoError(t, waitForPCSScalingProgressToComplete(tc, scaleInCostWorkloadName))
	require.NoError(t, waitForScaleInSelectionEvent(tc, scaleInCostWorkloadName, "Selected PCS replica indices [1] for scale-in (scale-in cost: 1="+scaleInCostMark+")"))

	Logger.Info("8. Replace one pod of replica 2 with a scheduled but not Ready pod, making replica 2 unavailable")
	require.NoError(t, kwok.ApplyStage(ctx, tc.Client, scaleInCostCrashloopStagePath))
	defer func() {
		if err := kwok.DeleteStage(ctx, tc.Client, scaleInCostCrashloopStageName); err != nil {
			Logger.Warnf("cleanup: delete scale-in cost crash-loop KWOK stage: %v", err)
		}
	}()
	pods, err := tc.ListPods()
	require.NoError(t, err)
	replica2Pod := findReadyPodFromPodClique(pods, scaleInCostWorkloadName+"-2-worker")
	require.NotNil(t, replica2Pod)
	require.NoError(t, tc.Client.Delete(ctx, replica2Pod))
	require.NoError(t, waitForPCLQReplicaCounts(tc, scaleInCostWorkloadName+"-2-worker", 2, 1))

	Logger.Info("9. Mark both pods of replica 0 and prove the operator has read the marks through a PodClique scale-in")
	replica0PCLQ := scaleInCostWorkloadName + "-0-worker"
	replica0Pods, err := podNamesOfPodClique(tc, replica0PCLQ)
	require.NoError(t, err)
	require.Len(t, replica0Pods, 2)
	require.NoError(t, markPodsOfPCSReplica(tc, 0, 2))
	require.NoError(t, tc.ScalePodClique(replica0PCLQ, 3))
	require.NoError(t, waitForPCLQReplicaCounts(tc, replica0PCLQ, 3, 3))
	replica0NewestPod, err := newPodOfPodClique(tc, replica0PCLQ, sets.New(replica0Pods...))
	require.NoError(t, err)
	require.NoError(t, tc.ScalePodClique(replica0PCLQ, 2))
	marked := sets.New(replica0Pods...)
	remaining, err := waitForPodCliquePods(tc, replica0PCLQ, func(names sets.Set[string]) bool {
		// The unmarked newest pod stays and exactly one marked pod leaves: the operator read the marks.
		return names.Len() == 2 && names.Has(replica0NewestPod) && names.Intersection(marked).Len() == 1
	})
	require.NoError(t, err)
	Logger.Infof("Replica 0 keeps the marked pod %v and the newest pod %s", sets.List(remaining.Intersection(marked)), replica0NewestPod)

	Logger.Info("10. Scale the PCS to one replica")
	require.NoError(t, waitForPCSReplicasAvailable(tc, scaleInCostWorkloadName, 0))
	require.NoError(t, tc.ScalePCS(scaleInCostWorkloadName, 1))

	Logger.Info("11. Verify the unavailable replica 2 is removed ahead of the marked replica 0, retaining [0]")
	require.NoError(t, waitForPCSReplicaIndices(tc, scaleInCostWorkloadName, sets.New(0)))
	require.NoError(t, waitForPCSScalingProgressToComplete(tc, scaleInCostWorkloadName))
}

// Test_SIC2_CostSteersPodCliquePodScaleIn verifies that a per-pod deletion cost steers which pod a standalone
// PodClique's scale-in removes, and that without the cost source the newest pod is removed as before:
//  1. Deploy the PCS and wait for all Pods to become Ready.
//  2. With the default cost source (none), mark one of replica 0's two original pods, then scale its
//     PodClique from two to three pods and wait for the newer third pod to become Ready.
//  3. Scale the PodClique back to two pods and verify the newest pod is removed, the marked one staying.
//  4. Switch the operator's scale-in cost source to pod-deletion-cost.
//  5. Scale the PodClique to three pods again and wait for the new pod to become Ready.
//  6. Scale the PodClique back to two pods and verify the marked pod is removed, the newest one staying.
func Test_SIC2_CostSteersPodCliquePodScaleIn(t *testing.T) {
	ctx := context.Background()
	Logger.Info("1. Deploy the PCS and wait for all Pods to become Ready")
	tc, cleanup := testctx.PrepareTest(ctx, t, 6,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         scaleInCostWorkloadName,
			YAMLPath:     "../yaml/pcs-scale-in-cost.yaml",
			Namespace:    "default",
			ExpectedPods: 6,
		}),
	)
	defer cleanup()

	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err)
	require.NoError(t, tc.WaitForReadyPods(6))
	pclqName := scaleInCostWorkloadName + "-0-worker"

	Logger.Info("2. With the default cost source, mark one original pod of replica 0 and scale its PodClique to three pods")
	originalPods, err := podNamesOfPodClique(tc, pclqName)
	require.NoError(t, err)
	require.Len(t, originalPods, 2)
	markedPod := originalPods[0]
	require.NoError(t, markPod(tc, markedPod))
	require.NoError(t, tc.ScalePodClique(pclqName, 3))
	require.NoError(t, tc.WaitForReadyPods(7))
	newestPod, err := newPodOfPodClique(tc, pclqName, sets.New(originalPods...))
	require.NoError(t, err)

	Logger.Info("3. Scale the PodClique back to two pods and verify the newest pod is removed, the marked one staying")
	require.NoError(t, tc.ScalePodClique(pclqName, 2))
	require.NoError(t, waitForPodClique(tc, pclqName, sets.New(originalPods...)))
	Logger.Infof("Removed the newest pod %s, the marked pod %s stayed", newestPod, markedPod)

	Logger.Info("4. Switch the operator's scale-in cost source to pod-deletion-cost")
	setScaleInCostSource(t, tc, configv1beta1.ScaleInCostSourcePodDeletionCost)

	Logger.Info("5. Scale the PodClique to three pods again and wait for the new pod to become Ready")
	require.NoError(t, tc.ScalePodClique(pclqName, 3))
	require.NoError(t, tc.WaitForReadyPods(7))
	newestPod, err = newPodOfPodClique(tc, pclqName, sets.New(originalPods...))
	require.NoError(t, err)

	Logger.Info("6. Scale the PodClique back to two pods and verify the marked pod is removed, the newest one staying")
	require.NoError(t, tc.ScalePodClique(pclqName, 2))
	require.NoError(t, waitForPodClique(tc, pclqName, sets.New(originalPods[1], newestPod)))
	Logger.Infof("Removed the marked pod %s, the newest pod %s stayed", markedPod, newestPod)
}

// setScaleInCostSource switches the operator's scale-in cost source through a Helm upgrade of the operator
// chart, waits for the operator to come back, and restores the default when the test ends.
func setScaleInCostSource(t *testing.T, tc *testctx.TestContext, source configv1beta1.ScaleInCostSourceName) {
	t.Helper()
	chartDir, err := setup.GetGroveChartDir()
	require.NoError(t, err)
	groveConfig := func(source configv1beta1.ScaleInCostSourceName) *setup.GroveConfig {
		return &setup.GroveConfig{
			Webhooks: setup.WebhooksConfig{
				CertProvisionMode: configv1beta1.CertProvisionModeAuto,
				SecretName:        configv1beta1.DefaultWebhookSecretName,
			},
			ScaleIn: configv1beta1.ScaleInConfiguration{CostSource: source},
		}
	}
	// The restore is registered first, so a failed switch still leaves the operator on the default source.
	t.Cleanup(func() {
		if err := setup.UpdateGroveConfiguration(context.WithoutCancel(tc.Ctx), tc.Client.RestConfig, chartDir, groveConfig(configv1beta1.ScaleInCostSourceNone), Logger); err != nil {
			t.Errorf("cleanup: restore the default scale-in cost source: %v", err)
		}
	})
	require.NoError(t, setup.UpdateGroveConfiguration(tc.Ctx, tc.Client.RestConfig, chartDir, groveConfig(source), Logger))
}

// waitForPCSReplicasAvailable waits until the worker PodClique of each given PCS replica reports every pod
// scheduled and ready, so the PCS controller counts the replica as available. Pod readiness alone is not enough:
// the PodClique status that the selection reads trails the pods by a reconcile.
func waitForPCSReplicasAvailable(tc *testctx.TestContext, pcsName string, pcsReplicaIndices ...int) error {
	for _, pcsReplicaIndex := range pcsReplicaIndices {
		pclqName := fmt.Sprintf("%s-%d-worker", pcsName, pcsReplicaIndex)
		if err := waitForPCLQReplicaCounts(tc, pclqName, scaleInCostPodsPerReplica, scaleInCostPodsPerReplica); err != nil {
			return fmt.Errorf("PCS replica %d not available: %w", pcsReplicaIndex, err)
		}
	}
	return nil
}

// markPod writes the pod-deletion-cost mark on the named pod.
func markPod(tc *testctx.TestContext, podName string) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: tc.Namespace, Name: podName}}
	patch := client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, corev1.PodDeletionCost, scaleInCostMark)))
	return tc.Client.Patch(tc.Ctx, pod, patch)
}

// markPodsOfPCSReplica writes the mark on count pods of the given PCS replica.
func markPodsOfPCSReplica(tc *testctx.TestContext, pcsReplicaIndex, count int) error {
	pods, err := tc.ListPods()
	if err != nil {
		return err
	}
	marked := 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil || pod.Labels[apicommon.LabelPodCliqueSetReplicaIndex] != fmt.Sprint(pcsReplicaIndex) {
			continue
		}
		if err := markPod(tc, pod.Name); err != nil {
			return err
		}
		if marked++; marked == count {
			return nil
		}
	}
	return fmt.Errorf("marked %d of %d pods of PCS replica %d", marked, count, pcsReplicaIndex)
}

// podNamesOfPodClique returns the names of the non-terminating pods of the PodClique.
func podNamesOfPodClique(tc *testctx.TestContext, pclqName string) ([]string, error) {
	pods, err := tc.ListPods()
	if err != nil {
		return nil, err
	}
	var names []string
	for i := range pods.Items {
		if pods.Items[i].DeletionTimestamp == nil && pods.Items[i].Labels[apicommon.LabelPodClique] == pclqName {
			names = append(names, pods.Items[i].Name)
		}
	}
	return names, nil
}

// newPodOfPodClique returns the one pod of the PodClique that is not among the known pods.
func newPodOfPodClique(tc *testctx.TestContext, pclqName string, knownPods sets.Set[string]) (string, error) {
	names, err := podNamesOfPodClique(tc, pclqName)
	if err != nil {
		return "", err
	}
	newPods := sets.New(names...).Difference(knownPods)
	if newPods.Len() != 1 {
		return "", fmt.Errorf("expected exactly one new pod in PodClique %s, found %v", pclqName, sets.List(newPods))
	}
	return sets.List(newPods)[0], nil
}

// waitForPodClique waits until the PodClique's non-terminating pods are exactly the expected ones.
func waitForPodClique(tc *testctx.TestContext, pclqName string, expected sets.Set[string]) error {
	_, err := waitForPodCliquePods(tc, pclqName, expected.Equal)
	return err
}

// waitForPodCliquePods waits until the names of the PodClique's non-terminating pods satisfy the predicate, and
// returns them.
func waitForPodCliquePods(tc *testctx.TestContext, pclqName string, predicate func(names sets.Set[string]) bool) (sets.Set[string], error) {
	var names sets.Set[string]
	err := wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(context.Context) (bool, error) {
		podNames, err := podNamesOfPodClique(tc, pclqName)
		if err != nil {
			return false, err
		}
		names = sets.New(podNames...)
		return predicate(names), nil
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for the pods of PodClique %s, last seen %v: %w", pclqName, sets.List(names), err)
	}
	return names, nil
}

// waitForScaleInSelectionEvent waits until the PCS carries a scale-in selection event with the given message.
func waitForScaleInSelectionEvent(tc *testctx.TestContext, pcsName, message string) error {
	var seen []string
	err := wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		events := &corev1.EventList{}
		if err := tc.Client.List(ctx, events, client.InNamespace(tc.Namespace), &client.ListOptions{
			Raw: &metav1.ListOptions{FieldSelector: "involvedObject.name=" + pcsName + ",reason=PodCliqueSetReplicaIndicesSelectedForScaleIn"},
		}); err != nil {
			return false, err
		}
		seen = seen[:0]
		for _, event := range events.Items {
			if event.Message == message {
				return true, nil
			}
			seen = append(seen, event.Message)
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for event %q on PodCliqueSet %s, saw: %s: %w", message, pcsName, strings.Join(seen, " | "), err)
	}
	return nil
}
