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
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

// MaxUnavailablePods runs runUpdate (which triggers the rolling update and waits for it to complete)
// while sampling the workload's pods, and returns the maximum number of pods matching matchFn
// observed in any single sample. matchFn defines what counts as unavailable. The sample interval is
// well below the readiness-delay window so that window is observable across samples.
func MaxUnavailablePods(tc *testctx.TestContext, matchFn func(*corev1.Pod) bool, runUpdate func() error) (int, error) {
	// Sample well below the readiness delay so the not-ready window is caught.
	const sampleInterval = 500 * time.Millisecond
	sampler := pods.NewPodCountSampler()
	sampler.Start(tc.Ctx, sampleInterval, func(context.Context) ([]corev1.Pod, error) {
		podList, err := tc.ListPods()
		if err != nil {
			return nil, err
		}
		return podList.Items, nil
	}, matchFn)
	err := runUpdate()
	return sampler.Stop(), err
}

// NotReadyPodForClique matches live (non-terminating) not-ready Pods that belong to the given
// standalone PodClique template name.
func NotReadyPodForClique(cliqueName string) func(*corev1.Pod) bool {
	return func(pod *corev1.Pod) bool {
		if pod.DeletionTimestamp != nil {
			return false
		}
		pclq, ok := pod.Labels[apicommon.LabelPodClique]
		return ok && strings.HasSuffix(pclq, "-"+cliqueName) && !podReady(pod)
	}
}

// podReady reports whether the Pod has a Ready condition set to True.
func podReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// NotReadyPodForPCSG matches live (non-terminating) not-ready Pods that belong to the given
// PodCliqueScalingGroup.
func NotReadyPodForPCSG(tc *testctx.TestContext, pcsgConfigName string) func(*corev1.Pod) bool {
	name := PCSGFQN(tc, pcsgConfigName)
	return func(pod *corev1.Pod) bool {
		if pod.DeletionTimestamp != nil {
			return false
		}
		return pod.Labels[apicommon.LabelPodCliqueScalingGroup] == name && !podReady(pod)
	}
}

// RestartOperator simulates a Grove operator crash by deleting its pod in the operator namespace and
// waiting for the Deployment to bring up a fresh Ready replica. It lets a test assert the operator resumes
// in-flight work from persisted state after a restart.
func RestartOperator(tc *testctx.TestContext) error {
	pm := pods.NewPodManager(tc.Client, tests.Logger)
	before, err := pm.List(tc.Ctx, setup.OperatorNamespace, "")
	if err != nil {
		return fmt.Errorf("failed to list operator pods in %s: %w", setup.OperatorNamespace, err)
	}
	oldPodUIDs := make(map[types.UID]struct{}, len(before.Items))
	for i := range before.Items {
		oldPodUIDs[before.Items[i].UID] = struct{}{}
		if err := tc.Client.Delete(tc.Ctx, &before.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete operator pod %s: %w", before.Items[i].Name, err)
		}
	}
	// Wait until exactly one operator pod, none of the deleted ones, is Ready.
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		current, err := pm.List(ctx, setup.OperatorNamespace, "")
		if err != nil {
			return false, nil //nolint:nilerr // Retry transient API read failures until the polling deadline.
		}
		if len(current.Items) != 1 {
			return false, nil
		}
		pod := &current.Items[0]
		if _, isOld := oldPodUIDs[pod.UID]; isOld {
			return false, nil
		}
		return kubeutils.IsPodReady(pod), nil
	})
}

// VerifyPodHasUpdatedSpec verifies that a pod has the UPDATE_TRIGGER environment variable,
// indicating it was created with the updated spec.
func VerifyPodHasUpdatedSpec(tc *testctx.TestContext, podName string) error {
	tc.T.Helper()

	var pod corev1.Pod
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: podName}, &pod); err != nil {
		return fmt.Errorf("failed to get pod %s: %w", podName, err)
	}

	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "UPDATE_TRIGGER" {
				return nil
			}
		}
	}

	return fmt.Errorf("pod %s does not have the UPDATE_TRIGGER environment variable", podName)
}

// DeletePodAndWaitForTermination deletes a pod and waits until it is fully terminated.
func DeletePodAndWaitForTermination(tc *testctx.TestContext, podName string) error {
	tc.T.Helper()

	if err := tc.Client.Delete(tc.Ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: tc.Namespace}}); err != nil {
		return fmt.Errorf("failed to delete pod %s: %w", podName, err)
	}

	pm := pods.NewPodManager(tc.Client, testctx.Logger)
	w := waiter.New[*corev1.PodList]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval)
	_, err := w.WaitFor(tc.Ctx, pm.FetchFunc(tc.Ctx, tc.Namespace, tc.GetLabelSelector()), pods.ExcludesPod(podName))
	if err != nil {
		return fmt.Errorf("failed to wait for the pod %s to be terminated: %w", podName, err)
	}

	tests.Logger.Debugf("Deleted pod %s, waiting for replacement...", podName)
	return nil
}

// AssertAffectedPodsAtRevision fails unless every Pod in the named clique carries updateValue.
func AssertAffectedPodsAtRevision(tc *testctx.TestContext, cliqueName, updateValue string) {
	tc.T.Helper()
	pods, err := tc.ListPods()
	require.NoError(tc.T, err)

	matched := 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		pclqName := pod.Labels[apicommon.LabelPodClique]
		if !strings.HasSuffix(pclqName, "-"+cliqueName) {
			continue
		}
		matched++
		require.NotEmpty(tc.T, pod.Spec.Containers, "Pod %s has no containers", pod.Name)
		found := false
		for _, env := range pod.Spec.Containers[0].Env {
			if env.Name == "UPDATE_TRIGGER" && env.Value == updateValue {
				found = true
				break
			}
		}
		assert.True(tc.T, found, "Pod %s does not carry UPDATE_TRIGGER=%q", pod.Name, updateValue)
	}
	require.Positive(tc.T, matched, "no Pods found for clique %s", cliqueName)
}

// DeleteNotReadyPodsOnReplica deletes every not-ready pod of the given PCS replica index, leaving healthy
// pods in place. This covers a held Pending pod and a crash-looping Running-but-NotReady pod. Removing a
// KWOK shaping stage does not release a pod already shaped by it, so deleting the not-ready pods lets fresh
// ones come up Ready under the default stage while the replica's healthy pods are untouched.
func DeleteNotReadyPodsOnReplica(t *testing.T, tc *testctx.TestContext, pcsReplicaIndex int) {
	t.Helper()
	podList, err := tc.ListPods()
	if err != nil {
		t.Fatalf("failed to list Pods: %v", err)
	}
	wantReplica := strconv.Itoa(pcsReplicaIndex)
	for i := range podList.Items {
		pod := podList.Items[i]
		if pod.Labels[apicommon.LabelPodCliqueSetReplicaIndex] != wantReplica || pod.DeletionTimestamp != nil || kubeutils.IsPodReady(&pod) {
			continue
		}
		if err := tc.Client.Delete(tc.Ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
			t.Fatalf("failed to delete not-ready pod %s during replica %d recovery: %v", pod.Name, pcsReplicaIndex, err)
		}
	}
}
