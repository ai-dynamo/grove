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
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
)

// Test_SR1_DisjointPodCliqueSchedulers verifies that each PodClique in a single
// PodCliqueSet can select a different enabled scheduler backend. Besides
// admission, this covers per-clique Pod preparation and successful scheduling.
func Test_SR1_DisjointPodCliqueSchedulers(t *testing.T) {
	const (
		workloadName = "disjoint-schedulers"
		expectedPods = 2
	)

	ctx := context.Background()
	tc, cleanup := testctx.PrepareTest(ctx, t, expectedPods,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         workloadName,
			YAMLPath:     "../yaml/disjoint-schedulers.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()

	pods, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("PodCliqueSet with disjoint scheduler names was not admitted and reconciled: %v", err)
	}

	wantSchedulerByClique := map[string]string{
		workloadName + "-0-kube-worker": string(configv1alpha1.SchedulerNameKube),
		workloadName + "-0-kai-worker":  string(configv1alpha1.SchedulerNameKai),
	}
	for _, pod := range pods.Items {
		cliqueName := pod.Labels[apicommon.LabelPodClique]
		wantScheduler, ok := wantSchedulerByClique[cliqueName]
		if !ok {
			t.Errorf("pod %s has unexpected PodClique label %q", pod.Name, cliqueName)
			continue
		}
		if pod.Spec.SchedulerName != wantScheduler {
			t.Errorf("pod %s from PodClique %s uses scheduler %q, want %q", pod.Name, cliqueName, pod.Spec.SchedulerName, wantScheduler)
		}
		delete(wantSchedulerByClique, cliqueName)
	}
	for cliqueName := range wantSchedulerByClique {
		t.Errorf("no pod was created for PodClique %s", cliqueName)
	}
	if t.Failed() {
		return
	}

	if err := tc.WaitForPods(expectedPods); err != nil {
		t.Fatalf("pods using disjoint scheduler names did not become ready: %v", err)
	}
}
