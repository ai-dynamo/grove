//go:build e2e

// /*
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
// */

package tests

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

var podCliqueSetsGVR = schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}

// OnDeleteTestConfig holds configuration for OnDelete update strategy test setup.
type OnDeleteTestConfig struct {
	WorkerNodes  int
	ExpectedPods int

	InitialPCSReplicas int32
	PostScalePods      int

	InitialPCSGReplicas int32
	PCSGName            string
	PostPCSGScalePods   int

	WorkloadName string
	WorkloadYAML string
	Namespace    string
}

func applyOnDeleteDefaults(cfg *OnDeleteTestConfig) {
	if cfg.WorkloadName == "" {
		cfg.WorkloadName = "workload-ondelete"
	}
	if cfg.WorkloadYAML == "" {
		cfg.WorkloadYAML = "../yaml/workload-ondelete.yaml"
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
}

func newOnDeleteTestContext(t *testing.T, ctx context.Context, cfg OnDeleteTestConfig) TestContext {
	return TestContext{
		T:         t,
		Ctx:       ctx,
		Namespace: cfg.Namespace,
		Timeout:   defaultPollTimeout,
		Interval:  defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         cfg.WorkloadName,
			YAMLPath:     cfg.WorkloadYAML,
			Namespace:    cfg.Namespace,
			ExpectedPods: cfg.ExpectedPods,
		},
	}
}

func setupOnDeleteTest(t *testing.T, cfg OnDeleteTestConfig) (TestContext, func(), *rollingUpdateTracker) {
	t.Helper()
	ctx := context.Background()
	applyOnDeleteDefaults(&cfg)

	clientset, restConfig, dynamicClient, clusterCleanup := prepareTestCluster(ctx, t, cfg.WorkerNodes)
	tc := newOnDeleteTestContext(t, ctx, cfg)
	tc.Clientset = clientset
	tc.RestConfig = restConfig
	tc.DynamicClient = dynamicClient

	if err := deployAndVerifyOnDeleteWorkload(tc, cfg); err != nil {
		clusterCleanup()
		t.Fatalf("Failed to set up OnDelete workload: %v", err)
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		clusterCleanup()
		t.Fatalf("Failed to start tracker: %v", err)
	}

	cleanup := func() {
		tracker.Stop()
		clusterCleanup()
	}

	return tc, cleanup, tracker
}

func deployAndVerifyOnDeleteWorkload(tc TestContext, cfg OnDeleteTestConfig) error {
	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		return err
	}

	if err := waitForPods(tc, cfg.ExpectedPods); err != nil {
		return fmt.Errorf("failed to wait for pods to be ready: %w", err)
	}

	if len(pods.Items) != cfg.ExpectedPods {
		return fmt.Errorf("expected %d pods, but found %d", cfg.ExpectedPods, len(pods.Items))
	}

	if err := applyOnDeleteInitialScaling(tc, cfg); err != nil {
		return err
	}

	return nil
}

func applyOnDeleteInitialScaling(tc TestContext, cfg OnDeleteTestConfig) error {
	if cfg.InitialPCSReplicas > 0 {
		scalePCSAndWait(tc, cfg.WorkloadName, cfg.InitialPCSReplicas, cfg.PostScalePods, 0)
		if err := waitForPods(tc, cfg.PostScalePods); err != nil {
			return fmt.Errorf("failed to wait for pods to be ready after PCS scaling: %w", err)
		}
	}

	if cfg.InitialPCSGReplicas > 0 && cfg.PCSGName != "" {
		pcsReplicas := int32(1)
		if cfg.InitialPCSReplicas > 0 {
			pcsReplicas = cfg.InitialPCSReplicas
		}
		scalePCSGAcrossAllReplicasAndWait(tc, cfg.WorkloadName, cfg.PCSGName, pcsReplicas, cfg.InitialPCSGReplicas, cfg.PostPCSGScalePods, 0)
	}

	return nil
}

func getPodCliqueSet(tc TestContext) (*grovev1alpha1.PodCliqueSet, error) {
	unstructuredPCS, err := tc.DynamicClient.Resource(podCliqueSetsGVR).Namespace(tc.Namespace).Get(tc.Ctx, tc.Workload.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get PodCliqueSet: %w", err)
	}

	var pcs grovev1alpha1.PodCliqueSet
	if err := utils.ConvertUnstructuredToTyped(unstructuredPCS.Object, &pcs); err != nil {
		return nil, fmt.Errorf("failed to convert to PodCliqueSet: %w", err)
	}

	return &pcs, nil
}

func isOnDeleteUpdateComplete(pcs *grovev1alpha1.PodCliqueSet) bool {
	return pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt != nil
}

func waitForOnDeleteUpdateComplete(tc TestContext) error {
	pollCount := 0
	return pollForCondition(tc, func() (bool, error) {
		pollCount++
		pcs, err := getPodCliqueSet(tc)
		if err != nil {
			return false, err
		}

		if pollCount%3 == 1 {
			logger.Debugf("[waitForOnDeleteUpdateComplete] Poll #%d: UpdatedReplicas=%d, UpdateProgress=%v",
				pollCount, pcs.Status.UpdatedReplicas, pcs.Status.UpdateProgress != nil)
			if pcs.Status.UpdateProgress != nil {
				logger.Debugf("  UpdateStartedAt=%v, UpdateEndedAt=%v",
					pcs.Status.UpdateProgress.UpdateStartedAt,
					pcs.Status.UpdateProgress.UpdateEndedAt)
			}
		}

		if isOnDeleteUpdateComplete(pcs) {
			logger.Debugf("[waitForOnDeleteUpdateComplete] OnDelete update marked complete after %d polls (UpdatedReplicas=%d)",
				pollCount, pcs.Status.UpdatedReplicas)
			return true, nil
		}

		return false, nil
	})
}

func getPCSUpdateProgress(tc TestContext) (*grovev1alpha1.PodCliqueSetUpdateProgress, error) {
	tc.T.Helper()

	pcs, err := getPodCliqueSet(tc)
	if err != nil {
		return nil, err
	}

	return pcs.Status.UpdateProgress, nil
}

func verifyUpdateProgressFields(tc TestContext) {
	tc.T.Helper()

	updateProgress, err := getPCSUpdateProgress(tc)
	if err != nil {
		tc.T.Fatalf("Failed to get UpdateProgress: %v", err)
	}

	if updateProgress == nil {
		tc.T.Fatalf("UpdateProgress should not be nil after OnDelete update")
	}

	if updateProgress.UpdateStartedAt.IsZero() {
		tc.T.Fatalf("UpdateProgress.UpdateStartedAt should be set")
	}

	if updateProgress.UpdateEndedAt == nil {
		tc.T.Fatalf("UpdateProgress.UpdateEndedAt should be set for OnDelete strategy")
	}

	if updateProgress.UpdatedPodCliques != nil {
		tc.T.Fatalf("UpdateProgress.UpdatedPodCliques should be nil for OnDelete strategy, got %v", updateProgress.UpdatedPodCliques)
	}

	if updateProgress.UpdatedPodCliqueScalingGroups != nil {
		tc.T.Fatalf("UpdateProgress.UpdatedPodCliqueScalingGroups should be nil for OnDelete strategy, got %v", updateProgress.UpdatedPodCliqueScalingGroups)
	}

	if updateProgress.CurrentlyUpdating != nil {
		tc.T.Fatalf("UpdateProgress.CurrentlyUpdating should be nil for OnDelete strategy, got %v", updateProgress.CurrentlyUpdating)
	}
}

func verifyNoPodsDeleted(tc TestContext, events []podEvent, existingPodNames map[string]bool) {
	tc.T.Helper()

	deletedPods := []string{}
	for _, event := range events {
		if event.Type == watch.Deleted && existingPodNames[event.Pod.Name] {
			deletedPods = append(deletedPods, event.Pod.Name)
		}
	}

	if len(deletedPods) > 0 {
		tc.T.Fatalf("OnDelete strategy violated: pods were automatically deleted when spec changed: %v", deletedPods)
	}
}

func verifyPodHasUpdatedSpec(tc TestContext, podName string) error {
	tc.T.Helper()

	pod, err := tc.Clientset.CoreV1().Pods(tc.Namespace).Get(tc.Ctx, podName, metav1.GetOptions{})
	if err != nil {
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

func deletePodAndWaitForTermination(tc TestContext, podName string) error {
	tc.T.Helper()

	if err := tc.Clientset.CoreV1().Pods(tc.Namespace).Delete(tc.Ctx, podName, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("failed to delete pod %s: %w", podName, err)
	}

	err := pollForCondition(tc, func() (bool, error) {
		pods, err := listPods(tc)
		if err != nil {
			return false, err
		}
		for _, pod := range pods.Items {
			if pod.Name == podName {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to wait for the pod %s to be terminated: %w", podName, err)
	}

	logger.Debugf("Deleted pod %s, waiting for replacement...", podName)
	return nil
}

func getPodsForClique(tc TestContext, cliqueName string) ([]string, error) {
	tc.T.Helper()

	pods, err := listPods(tc)
	if err != nil {
		return nil, err
	}

	var cliquePods []string
	for _, pod := range pods.Items {
		if pod.Labels == nil {
			continue
		}
		if pclq, ok := pod.Labels["grove.io/podclique"]; ok && strings.HasSuffix(pclq, "-"+cliqueName) {
			cliquePods = append(cliquePods, pod.Name)
		}
	}

	return cliquePods, nil
}

func captureExistingPodNames(tc TestContext) (map[string]bool, error) {
	pods, err := listPods(tc)
	if err != nil {
		return nil, err
	}

	existingPodNames := make(map[string]bool, len(pods.Items))
	for _, pod := range pods.Items {
		existingPodNames[pod.Name] = true
	}

	logger.Debugf("Captured %d existing pods before spec change", len(existingPodNames))
	return existingPodNames, nil
}

func waitForOnDeleteUpdateCompleteWithTimeout(tc TestContext, timeout time.Duration) error {
	tcWithTimeout := tc
	tcWithTimeout.Timeout = timeout
	return waitForOnDeleteUpdateComplete(tcWithTimeout)
}

func waitForOnDeleteUpdateCompleteDefault(tc TestContext) error {
	return waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute)
}

func getFirstPodForClique(tc TestContext, cliqueName string) (string, error) {
	cliquePods, err := getPodsForClique(tc, cliqueName)
	if err != nil {
		return "", err
	}
	if len(cliquePods) == 0 {
		return "", fmt.Errorf("no pods found for %s", cliqueName)
	}
	return cliquePods[0], nil
}

func findFirstNewPodName(tc TestContext, existingPodNames map[string]bool) (string, error) {
	pods, err := listPods(tc)
	if err != nil {
		return "", err
	}

	for _, pod := range pods.Items {
		if !existingPodNames[pod.Name] {
			return pod.Name, nil
		}
	}

	return "", fmt.Errorf("could not find newly created replacement pod")
}

func verifyNoAutomaticDeletionAfterUpdate(
	tc TestContext,
	tracker *rollingUpdateTracker,
	existingPodNames map[string]bool,
	expectedPods int,
	verifyProgressFields bool,
) {
	tc.T.Helper()

	time.Sleep(10 * time.Second)

	if err := waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		tc.T.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	if verifyProgressFields {
		verifyUpdateProgressFields(tc)
	}

	tracker.Stop()
	events := tracker.getEvents()
	verifyNoPodsDeleted(tc, events, existingPodNames)

	pods, err := listPods(tc)
	if err != nil {
		tc.T.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != expectedPods {
		tc.T.Fatalf("Expected %d pods to still be running, but got %d", expectedPods, len(pods.Items))
	}
}

func verifyAllPodsFromOriginalSet(tc TestContext, existingPodNames map[string]bool) {
	tc.T.Helper()

	pods, err := listPods(tc)
	if err != nil {
		tc.T.Fatalf("Failed to list pods: %v", err)
	}

	for _, pod := range pods.Items {
		if !existingPodNames[pod.Name] {
			tc.T.Fatalf("Unexpected new pod %s found - pods should not have been recreated", pod.Name)
		}
	}
}
