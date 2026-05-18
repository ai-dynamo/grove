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
	"sort"
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	e2egvk "github.com/ai-dynamo/grove/operator/e2e/grove/gvk"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type cascadeResource struct {
	name string
	gvk  schema.GroupVersionKind
}

var cascadeDeletedResources = []cascadeResource{
	{name: "PodCliqueScalingGroups", gvk: e2egvk.PodCliqueScalingGroup},
	{name: "PodCliques", gvk: e2egvk.PodClique},
	{name: "PodGangs", gvk: e2egvk.PodGang},
	{name: "Pods", gvk: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}},
	{name: "Services", gvk: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}},
	{name: "ServiceAccounts", gvk: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ServiceAccount"}},
	{name: "Secrets", gvk: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"}},
	{name: "Roles", gvk: schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"}},
	{name: "RoleBindings", gvk: schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"}},
	{name: "HorizontalPodAutoscalers", gvk: schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"}},
}

// Test_CascadeDeletePodCliqueSet verifies that deleting a small PodCliqueSet
// cascades through the owned Grove resource tree and managed child resources.
func Test_CascadeDeletePodCliqueSet(t *testing.T) {
	ctx := context.Background()

	const expectedPods = 10
	workloadConfig := &testctx.WorkloadConfig{
		Name:         "workload1",
		YAMLPath:     "../yaml/workload1.yaml",
		Namespace:    "default",
		ExpectedPods: expectedPods,
	}

	tc, cleanup := testctx.PrepareTest(ctx, t, expectedPods,
		testctx.WithWorkload(workloadConfig),
	)
	defer cleanup()

	Logger.Info("1. Deploy workload and verify child resources exist")
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	counts, err := countCascadeResources(tc.Ctx, tc, workloadConfig.Name)
	if err != nil {
		t.Fatalf("Failed to count cascade resources before delete: %v", err)
	}
	if counts["PodCliqueScalingGroups"] == 0 || counts["PodCliques"] == 0 || counts["PodGangs"] == 0 || counts["Pods"] != expectedPods {
		t.Fatalf("Expected workload child resources before delete, got: %s", formatCascadeResourceCounts(counts))
	}

	Logger.Info("2. Delete the PodCliqueSet")
	wm := workload.NewWorkloadManager(tc.Client, Logger)
	if err := wm.DeletePCS(tc.Ctx, tc.Namespace, workloadConfig.Name); err != nil {
		t.Fatalf("Failed to delete PodCliqueSet: %v", err)
	}

	Logger.Info("3. Verify the cascade deleted owned resources")
	if err := waitForCascadeResourcesDeleted(tc, workloadConfig.Name); err != nil {
		t.Fatalf("Failed waiting for cascade delete: %v", err)
	}
}

func countCascadeResources(ctx context.Context, tc *testctx.TestContext, workloadName string) (map[string]int, error) {
	counts := map[string]int{}

	var pcs grovecorev1alpha1.PodCliqueSet
	err := tc.Client.Get(ctx, types.NamespacedName{Name: workloadName, Namespace: tc.Namespace}, &pcs)
	switch {
	case apierrors.IsNotFound(err):
		counts["PodCliqueSets"] = 0
	case err != nil:
		return nil, fmt.Errorf("get PodCliqueSet: %w", err)
	default:
		counts["PodCliqueSets"] = 1
	}

	for _, resource := range cascadeDeletedResources {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(resource.gvk.GroupVersion().WithKind(resource.gvk.Kind + "List"))
		if err := tc.Client.List(ctx, list,
			client.InNamespace(tc.Namespace),
			client.MatchingLabels{apicommon.LabelPartOfKey: workloadName},
		); err != nil {
			return nil, fmt.Errorf("list %s: %w", resource.name, err)
		}
		counts[resource.name] = len(list.Items)
	}

	return counts, nil
}

func waitForCascadeResourcesDeleted(tc *testctx.TestContext, workloadName string) error {
	w := waiter.New[map[string]int]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval).
		WithLogger(Logger).
		WithRetryOnError()

	fetchCounts := waiter.FetchFunc[map[string]int](func(ctx context.Context) (map[string]int, error) {
		return countCascadeResources(ctx, tc, workloadName)
	})
	allDeleted := waiter.Predicate[map[string]int](func(counts map[string]int) bool {
		for _, count := range counts {
			if count != 0 {
				return false
			}
		}
		return true
	})

	if err := w.WaitUntil(tc.Ctx, fetchCounts, allDeleted); err != nil {
		counts, countErr := countCascadeResources(tc.Ctx, tc, workloadName)
		if countErr != nil {
			return fmt.Errorf("%w; failed to count remaining resources: %v", err, countErr)
		}
		return fmt.Errorf("%w; remaining resources: %s", err, formatCascadeResourceCounts(counts))
	}
	return nil
}

func formatCascadeResourceCounts(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		if counts[name] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", name, counts[name]))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
