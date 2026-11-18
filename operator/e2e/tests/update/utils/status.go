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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strconv"
)

// WaitForStandaloneReplicaUnavailable waits until the named standalone PodClique on the given PCS replica index
// reports fewer ready replicas than its MinAvailable, so the operator classifies that replica as unavailable.
func WaitForStandaloneReplicaUnavailable(tc *testctx.TestContext, cliqueName string, pcsReplicaIndex int) error {
	fqn := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex}, cliqueName)
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var pclq grovev1beta1.PodClique
		if err := tc.Client.Get(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: fqn}, &pclq); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		minAvailable := int32(1)
		if pclq.Spec.MinAvailable != nil {
			minAvailable = *pclq.Spec.MinAvailable
		}
		return pclq.Status.ReadyReplicas < minAvailable, nil
	})
}

// AssertNoActiveChildUpdates fails if any PCLQ or PCSG owned by the workload still has unfinished
// update progress after the PCS reports completion.
func AssertNoActiveChildUpdates(tc *testctx.TestContext) {
	tc.T.Helper()
	matchingPCS := client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}
	inNamespace := client.InNamespace(tc.Namespace)

	var pclqs grovev1beta1.PodCliqueList
	require.NoError(tc.T, tc.Client.List(tc.Ctx, &pclqs, inNamespace, matchingPCS))
	for i := range pclqs.Items {
		progress := pclqs.Items[i].Status.UpdateProgress
		assert.False(tc.T, progress != nil && progress.UpdateEndedAt == nil,
			"PodClique %s still has an active update", pclqs.Items[i].Name)
	}

	var pcsgs grovev1beta1.PodCliqueScalingGroupList
	require.NoError(tc.T, tc.Client.List(tc.Ctx, &pcsgs, inNamespace, matchingPCS))
	for i := range pcsgs.Items {
		progress := pcsgs.Items[i].Status.UpdateProgress
		assert.False(tc.T, progress != nil && progress.UpdateEndedAt == nil,
			"PodCliqueScalingGroup %s still has an active update", pcsgs.Items[i].Name)
	}
}

// WaitForReplicaReadyPods waits for the expected number of live Ready pods on one PCS replica.
func WaitForReplicaReadyPods(tc *testctx.TestContext, pcsReplicaIndex, expectedPods int) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var list corev1.PodList
		if err := tc.Client.List(ctx, &list, client.InNamespace(tc.Namespace), client.MatchingLabels{
			apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
		}); err != nil {
			return false, err
		}
		live, ready := 0, 0
		for i := range list.Items {
			pod := &list.Items[i]
			if pod.DeletionTimestamp != nil {
				continue
			}
			live++
			if kubeutils.IsPodReady(pod) {
				ready++
			}
		}
		return live == expectedPods && ready == expectedPods, nil
	})
}

// AssertGenerationHashConverged fails the test unless every PodClique and PodCliqueScalingGroup of the
// workload reports CurrentPodCliqueSetGenerationHash equal to the PodCliqueSet's CurrentGenerationHash.
func AssertGenerationHashConverged(tc *testctx.TestContext) {
	tc.T.Helper()
	pcs, err := GetPCS(tc, tc.Workload.Name)
	if err != nil {
		tc.T.Fatalf("failed to get PodCliqueSet: %v", err)
	}
	if pcs.Status.CurrentGenerationHash == nil {
		tc.T.Fatalf("PodCliqueSet %s has no CurrentGenerationHash", pcs.Name)
	}
	want := *pcs.Status.CurrentGenerationHash
	inNamespace := client.InNamespace(tc.Namespace)
	matchingPCS := client.MatchingLabels{apicommon.LabelPartOfKey: pcs.Name}

	var pclqList grovev1beta1.PodCliqueList
	if err := tc.Client.List(tc.Ctx, &pclqList, inNamespace, matchingPCS); err != nil {
		tc.T.Fatalf("failed to list PodCliques: %v", err)
	}
	for i := range pclqList.Items {
		pclq := &pclqList.Items[i]
		if pclq.Status.CurrentPodCliqueSetGenerationHash == nil || *pclq.Status.CurrentPodCliqueSetGenerationHash != want {
			tc.T.Fatalf("PodClique %s did not converge to generation hash %s, got %v", pclq.Name, want, pclq.Status.CurrentPodCliqueSetGenerationHash)
		}
	}

	var pcsgList grovev1beta1.PodCliqueScalingGroupList
	if err := tc.Client.List(tc.Ctx, &pcsgList, inNamespace, matchingPCS); err != nil {
		tc.T.Fatalf("failed to list PodCliqueScalingGroups: %v", err)
	}
	for i := range pcsgList.Items {
		pcsg := &pcsgList.Items[i]
		if pcsg.Status.CurrentPodCliqueSetGenerationHash == nil || *pcsg.Status.CurrentPodCliqueSetGenerationHash != want {
			tc.T.Fatalf("PodCliqueScalingGroup %s did not converge to generation hash %s, got %v", pcsg.Name, want, pcsg.Status.CurrentPodCliqueSetGenerationHash)
		}
	}
}
