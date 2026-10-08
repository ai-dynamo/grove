//go:build e2e && e2eupgrade

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

package upgrade

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func revisionWorkload() *testctx.WorkloadConfig {
	return &testctx.WorkloadConfig{
		Name: "upgrade-survivor", YAMLPath: "../../yaml/upgrade.yaml",
		Namespace: "default", ExpectedPods: 2,
	}
}

func waitForPCSConvergence(t *testing.T, tc *testctx.TestContext) *grovev1alpha1.PodCliqueSet {
	t.Helper()
	fetch := waiter.FetchByName(tc.Workload.Name, k8sclient.Getter[*grovev1alpha1.PodCliqueSet](tc.Client, tc.Namespace))
	pcs, err := waiter.New[*grovev1alpha1.PodCliqueSet]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitFor(t.Context(), fetch, func(pcs *grovev1alpha1.PodCliqueSet) bool {
			return pcs != nil &&
				ptr.Deref(pcs.Status.CurrentGenerationHash, "") != "" &&
				ptr.Deref(pcs.Status.ObservedGeneration, 0) == pcs.Generation &&
				pcs.Status.UpdatedReplicas == pcs.Spec.Replicas &&
				pcs.Status.AvailableReplicas == pcs.Spec.Replicas
		})
	require.NoError(t, err, "waiting for workload status to converge")
	return pcs
}

func waitForRevisionAdoption(t *testing.T, tc *testctx.TestContext) (*grovev1alpha1.PodCliqueSet, *commonrevision.Revision) {
	t.Helper()
	pcs := &grovev1alpha1.PodCliqueSet{}
	stored := &appsv1.ControllerRevision{}
	err := wait.PollUntilContextTimeout(t.Context(), tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name}, pcs); err != nil {
			return false, err
		}
		if ptr.Deref(pcs.Status.CurrentRevision, "") == "" || ptr.Deref(pcs.Status.ObservedGeneration, 0) != pcs.Generation ||
			pcs.Status.UpdatedReplicas != pcs.Spec.Replicas || pcs.Status.AvailableReplicas != pcs.Spec.Replicas {
			return false, nil
		}
		err := tc.Client.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: *pcs.Status.CurrentRevision}, stored)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	})
	require.NoError(t, err, "waiting for a usable selected ControllerRevision")
	require.True(t, metav1.IsControlledBy(stored, pcs), "revision must belong to the current PCS UID")
	revision, err := commonrevision.DecodeRevision(stored)
	require.NoError(t, err)
	require.Equal(t, ptr.Deref(pcs.Status.CurrentGenerationHash, ""), revision.GenerationHash())
	return pcs, revision
}

func verifyAdoptedPodHashes(t *testing.T, tc *testctx.TestContext, revision *commonrevision.Revision, before *corev1.PodList) {
	t.Helper()
	current, err := tc.ListPods()
	require.NoError(t, err)
	byUID := make(map[types.UID]corev1.Pod, len(current.Items))
	for _, pod := range current.Items {
		byUID[pod.UID] = pod
		clique := "bootstrap"
		if strings.HasSuffix(pod.Labels[apicommon.LabelPodClique], "-worker") {
			clique = "worker"
		}
		hash, err := revision.CliqueHash(clique)
		require.NoError(t, err)
		require.Equal(t, hash, pod.Labels[apicommon.LabelPodTemplateHash],
			"both surviving and newly scaled pods must use the adopted identity")
	}
	for _, pod := range before.Items {
		require.NotEmpty(t, pod.Labels[apicommon.LabelPodTemplateHash])
		require.Contains(t, byUID, pod.UID)
		require.Equal(t, pod.Labels[apicommon.LabelPodTemplateHash], byUID[pod.UID].Labels[apicommon.LabelPodTemplateHash])
	}
}

func updateWorkerAndWait(t *testing.T, tc *testctx.TestContext, marker string) {
	t.Helper()
	before, err := tc.ListPods()
	require.NoError(t, err)
	require.NotEmpty(t, before.Items)
	originalUIDs := make(map[types.UID]bool, len(before.Items))
	for _, pod := range before.Items {
		originalUIDs[pod.UID] = true
	}
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pcs := &grovev1alpha1.PodCliqueSet{}
		if err := tc.Client.Get(t.Context(), client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name}, pcs); err != nil {
			return err
		}
		original := pcs.DeepCopy()
		for _, clique := range pcs.Spec.Template.Cliques {
			if clique.Name == "worker" {
				clique.Spec.PodSpec.Containers[0].Env = []corev1.EnvVar{{Name: "UPDATE_TRIGGER", Value: marker}}
			}
		}
		return tc.Client.Patch(t.Context(), pcs, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}))
	}))
	manager := pods.NewPodManager(tc.Client, testctx.Logger)
	_, err = waiter.New[*corev1.PodList]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitFor(t.Context(), manager.FetchFunc(t.Context(), tc.Namespace, tc.GetLabelSelector()), func(current *corev1.PodList) bool {
			if !pods.AllReady(len(before.Items))(current) {
				return false
			}
			workers := 0
			for _, pod := range current.Items {
				if !pod.DeletionTimestamp.IsZero() {
					return false
				}
				if !strings.HasSuffix(pod.Labels[apicommon.LabelPodClique], "-worker") {
					if !originalUIDs[pod.UID] {
						return false
					}
					continue
				}
				workers++
				if originalUIDs[pod.UID] {
					return false
				}
				found := false
				for _, env := range pod.Spec.Containers[0].Env {
					if env.Name == "UPDATE_TRIGGER" && env.Value == marker {
						found = true
					}
				}
				if !found {
					return false
				}
			}
			return workers == len(before.Items)/2
		})
	require.NoError(t, err, "worker pods must be replaced and Ready while bootstrap pods survive")
}

// verifyStableWorkload permits replacements during recovery, but not continued churn after Ready.
func verifyStableWorkload(t *testing.T, tc *testctx.TestContext, count int) {
	t.Helper()
	const (
		stabilityWindow = 30 * time.Second
		pollInterval    = time.Second
	)
	type containerIdentity struct {
		ID       string
		Restarts int32
	}
	selector, err := labels.Parse(tc.GetLabelSelector())
	require.NoError(t, err)
	var baseline map[string]containerIdentity
	var start time.Time
	err = wait.PollUntilContextTimeout(t.Context(), pollInterval, stabilityWindow+defaultPollTimeout, true,
		func(ctx context.Context) (bool, error) {
			current := &corev1.PodList{}
			if err := tc.Client.List(ctx, current, &client.ListOptions{Namespace: tc.Namespace, LabelSelector: selector}); err != nil {
				return false, err
			}
			if !pods.AllReady(count)(current) {
				return false, fmt.Errorf("expected %d Ready pods throughout the stability window", count)
			}
			pcs := &grovev1alpha1.PodCliqueSet{}
			if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: tc.Workload.Name}, pcs); err != nil {
				return false, err
			}
			if ptr.Deref(pcs.Status.ObservedGeneration, 0) != pcs.Generation ||
				pcs.Status.AvailableReplicas != pcs.Spec.Replicas || pcs.Status.UpdatedReplicas != pcs.Spec.Replicas ||
				(pcs.Status.UpdateProgress != nil && len(pcs.Status.UpdateProgress.CurrentlyUpdating) != 0) {
				return false, fmt.Errorf("PCS %s is not converged during the stability window", pcs.Name)
			}
			identities := make(map[string]containerIdentity)
			for _, pod := range current.Items {
				if pod.UID == "" || !pod.DeletionTimestamp.IsZero() {
					return false, fmt.Errorf("pod %s has no UID or is terminating", pod.Name)
				}
				if len(pod.Status.ContainerStatuses) != len(pod.Spec.Containers) ||
					len(pod.Status.InitContainerStatuses) != len(pod.Spec.InitContainers) {
					return false, fmt.Errorf("pod %s has incomplete container status", pod.Name)
				}
				for kind, statuses := range map[string][]corev1.ContainerStatus{
					"app": pod.Status.ContainerStatuses, "init": pod.Status.InitContainerStatuses,
				} {
					for _, status := range statuses {
						if status.ContainerID == "" {
							return false, fmt.Errorf("pod %s container %s has no ID", pod.Name, status.Name)
						}
						key := fmt.Sprintf("%s/%s/%s/%s", pod.Name, pod.UID, kind, status.Name)
						identities[key] = containerIdentity{ID: status.ContainerID, Restarts: status.RestartCount}
					}
				}
			}
			if baseline == nil {
				baseline = identities
				start = time.Now()
				t.Logf("recovered workload identities: %+v", baseline)
			} else if !maps.Equal(baseline, identities) {
				return false, fmt.Errorf("pods were replaced or containers restarted after recovery: before=%+v after=%+v", baseline, identities)
			}
			if time.Since(start) >= stabilityWindow {
				t.Logf("workload remained Ready without replacements or restarts for %s", stabilityWindow)
				return true, nil
			}
			return false, nil
		})
	require.NoError(t, err, "checking workload stability after downgrade and scaling")
}
