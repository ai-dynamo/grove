//go:build e2e

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

package tests

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Test_N01_PodName verifies that Pod names retain the stable Pod index in the hostname
// while using a random suffix in metadata.name. The fixture places each hostname
// exactly at the DNS label limit and its metadata name above that limit.
func Test_N01_PodName(t *testing.T) {
	ctx := context.Background()

	pcsName := strings.Repeat("p", 30)
	cliqueName := strings.Repeat("c", 28)

	tc, cleanup := testctx.PrepareTest(
		ctx,
		t,
		0,
		testctx.WithWorkload(&testctx.WorkloadConfig{Name: pcsName}),
		testctx.WithInterval(250*time.Millisecond),
	)
	defer cleanup()

	pcs := testutils.NewPodCliqueSetBuilder(pcsName, tc.Namespace, uuid.NewUUID()).
		WithReplicas(1).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
				WithReplicas(2).
				WithPodSpec(corev1.PodSpec{TerminationGracePeriodSeconds: ptr.To[int64](0)}).
				WithRoleName("worker").
				WithMinAvailable(1).
				WithLabels(map[string]string{"kai.scheduler/queue": "test"}).
				Build(),
		).
		Build()

	err := tc.Client.Create(ctx, pcs)
	require.NoError(t, err)

	podList, err := tc.WaitForPodCount(2)
	require.NoError(t, err)

	podHostnames := lo.Map(podList.Items, func(pod corev1.Pod, _ int) string { return pod.Spec.Hostname })
	assert.ElementsMatch(t, []string{fmt.Sprintf("%s-0-%s-0", pcsName, cliqueName), fmt.Sprintf("%s-0-%s-1", pcsName, cliqueName)}, podHostnames)

	podNames := lo.Map(podList.Items, func(pod corev1.Pod, _ int) string { return pod.Name })
	podNameRegex := regexp.MustCompile(fmt.Sprintf("^%s-0-%s-[0-1]-[a-z0-9]{5}$", pcsName, cliqueName))
	for _, podName := range podNames {
		assert.Regexp(t, podNameRegex, podName)
	}

	indexes, err := podIndexes(podList.Items)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{0, 1}, indexes)
}

// Test_N02_NameValidation verifies name validation performed while admitting a PodCliqueSet.
func Test_N02_NameValidation(t *testing.T) {
	ctx := context.Background()

	tc, cleanup := testctx.PrepareTest(ctx, t, 0, testctx.WithInterval(250*time.Millisecond))
	defer cleanup()

	Logger.Info("01. Validating PodClique with scaling config under DNS name length limit")
	pcsName := strings.Repeat("a", 30)
	cliqueName := strings.Repeat("b", 28)
	pcs := testutils.NewPodCliqueSetBuilder(pcsName, tc.Namespace, uuid.NewUUID()).
		WithReplicas(1).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
				WithReplicas(1).
				WithPodSpec(corev1.PodSpec{TerminationGracePeriodSeconds: ptr.To[int64](0)}).
				WithRoleName("worker").
				WithMinAvailable(1).
				WithScaleConfig(ptr.To(int32(1)), 10).
				WithLabels(map[string]string{"kai.scheduler/queue": "test"}).
				Build(),
		).
		Build()

	verifyWorkloadOnCreate(t, tc, pcs)

	Logger.Info("02. Validating PodClique with scaling config over DNS name length limit")
	pcs.Spec.Template.Cliques[0].Spec.ScaleConfig = &grovecorev1alpha1.AutoScalingConfig{
		MinReplicas: ptr.To(int32(1)),
		MaxReplicas: 11,
	}

	verifyWorkloadOnCreate(t, tc, pcs,
		"spec.template.cliques[0].replicas",
		"generated pod hostname",
	)

	Logger.Info("03. Validating PodClique with name over DNS name length limit")
	pcsName = strings.Repeat("a", 30)
	cliqueName = strings.Repeat("b", 29)
	pcs = testutils.NewPodCliqueSetBuilder(pcsName, tc.Namespace, uuid.NewUUID()).
		WithReplicas(1).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
				WithReplicas(1).
				WithPodSpec(corev1.PodSpec{TerminationGracePeriodSeconds: ptr.To[int64](0)}).
				WithRoleName("worker").
				WithMinAvailable(1).
				WithLabels(map[string]string{"kai.scheduler/queue": "test"}).
				Build(),
		).
		Build()

	verifyWorkloadOnCreate(t, tc, pcs,
		"spec.template.cliques[0].replicas",
		"generated pod hostname",
	)

	Logger.Info("04. Validating PodClique with resource claim over DNS name length limit")
	pcsName = strings.Repeat("i", 40)
	cliqueName = "workr"
	claimName := "shared-gpus"

	pcs = testutils.NewPodCliqueSetBuilder(pcsName, tc.Namespace, uuid.NewUUID()).
		WithReplicas(1).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
				WithReplicas(1).
				WithPodSpec(corev1.PodSpec{TerminationGracePeriodSeconds: ptr.To[int64](0)}).
				WithRoleName("worker").
				WithMinAvailable(1).
				WithLabels(map[string]string{"kai.scheduler/queue": "test"}).
				WithResourceSharing([]grovecorev1alpha1.ResourceSharingSpec{
					{
						Name:  claimName,
						Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
					},
				}).
				Build(),
		).
		WithResourceClaimTemplates(grovecorev1alpha1.ResourceClaimTemplateConfig{
			Name: claimName,
			TemplateSpec: resourcev1.ResourceClaimTemplateSpec{
				Spec: resourcev1.ResourceClaimSpec{
					Devices: resourcev1.DeviceClaim{
						Requests: []resourcev1.DeviceRequest{{
							Name: "device",
							Exactly: &resourcev1.ExactDeviceRequest{
								DeviceClassName: "example-gpu",
							},
						}},
					},
				},
			},
		}).
		Build()

	verifyWorkloadOnCreate(t, tc, pcs,
		"spec.template.cliques[0].resourceSharing[0].name",
		"generated pod resource claim reference name",
	)
}

// Test_N03_PCS_Scale verifies PodCliqueSet scale requests account for the width of the
// requested PodCliqueSet replica index.
func Test_N03_PCS_Scale(t *testing.T) {
	ctx := context.Background()

	pcsName := "n"
	cliqueName := strings.Repeat("c", 57)

	tc, cleanup := testctx.PrepareTest(
		ctx,
		t,
		0,
		testctx.WithWorkload(&testctx.WorkloadConfig{Name: pcsName}),
		testctx.WithInterval(250*time.Millisecond),
	)
	defer cleanup()

	pcs := testutils.NewPodCliqueSetBuilder(pcsName, tc.Namespace, uuid.NewUUID()).
		WithReplicas(1).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
				WithReplicas(1).
				WithPodSpec(corev1.PodSpec{TerminationGracePeriodSeconds: ptr.To[int64](0)}).
				WithRoleName("worker").
				WithMinAvailable(1).
				WithLabels(map[string]string{"kai.scheduler/queue": "test"}).
				Build(),
		).
		Build()

	err := tc.Client.Create(ctx, pcs)
	require.NoError(t, err)

	_, err = tc.WaitForPodCount(1)
	require.NoError(t, err)

	Logger.Info("01. Scaling PCS to 2 replicas")
	err = workload.Scale(ctx, tc.Client, pcs, 2)
	require.NoError(t, err)
	_, err = tc.WaitForPodCount(2)
	require.NoError(t, err)

	Logger.Info("02. Scaling PCS to 0 replicas")
	err = workload.Scale(ctx, tc.Client, pcs, 0, client.DryRunAll)
	require.NoError(t, err)

	Logger.Info("03. Scaling PCS to 10 replicas")
	err = workload.Scale(ctx, tc.Client, pcs, 10, client.DryRunAll)
	require.NoError(t, err)

	Logger.Info("04. Scaling PCS to 11 replicas")
	err = workload.Scale(ctx, tc.Client, pcs, 11, client.DryRunAll)
	require.ErrorContains(t, err, "generated pod hostname")
}

// Test_N04_PCLQ_Scale verifies PodClique scale requests account for the width of the requested Pod index.
func Test_N04_PCLQ_Scale(t *testing.T) {
	ctx := context.Background()

	pcsName := "n"
	cliqueName := strings.Repeat("c", 57)

	tc, cleanup := testctx.PrepareTest(
		ctx,
		t,
		0,
		testctx.WithWorkload(&testctx.WorkloadConfig{Name: pcsName}),
		testctx.WithInterval(250*time.Millisecond),
	)
	defer cleanup()

	pcs := testutils.NewPodCliqueSetBuilder(pcsName, tc.Namespace, uuid.NewUUID()).
		WithReplicas(1).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
				WithReplicas(1).
				WithPodSpec(corev1.PodSpec{TerminationGracePeriodSeconds: ptr.To[int64](0)}).
				WithRoleName("worker").
				WithMinAvailable(1).
				WithLabels(map[string]string{"kai.scheduler/queue": "test"}).
				Build(),
		).
		Build()

	err := tc.Client.Create(ctx, pcs)
	require.NoError(t, err)

	_, err = tc.WaitForPodCount(1)
	require.NoError(t, err)

	pclqs, err := componentutils.GetPodCliquesWithParentPCS(ctx, tc.Client, client.ObjectKeyFromObject(pcs))
	require.NoError(t, err)
	require.Len(t, pclqs, 1)

	pclq := &pclqs[0]

	Logger.Info("01. Scaling PCLQ to 2 replicas")
	err = workload.Scale(ctx, tc.Client, pclq, 2)
	require.NoError(t, err)
	_, err = tc.WaitForPodCount(2)
	require.NoError(t, err)

	Logger.Info("02. Scaling PCLQ to 0 replicas")
	err = workload.Scale(ctx, tc.Client, pclq, 0, client.DryRunAll)
	require.Error(t, err)

	Logger.Info("03. Scaling PCLQ to 10 replicas")
	err = workload.Scale(ctx, tc.Client, pclq, 10, client.DryRunAll)
	require.NoError(t, err)

	Logger.Info("04. Scaling PCLQ to 11 replicas")
	err = workload.Scale(ctx, tc.Client, pclq, 11, client.DryRunAll)
	require.ErrorContains(t, err, "generated pod hostname")
}

// Test_N05_PCSG_Scale verifies PodCliqueScalingGroup scale requests account for the width
// of the requested scaling-group replica index.
func Test_N05_PCSG_Scale(t *testing.T) {
	ctx := context.Background()

	pcsName := "n"
	cliqueName := "c"
	groupName := strings.Repeat("g", 53)

	tc, cleanup := testctx.PrepareTest(
		ctx,
		t,
		0,
		testctx.WithWorkload(&testctx.WorkloadConfig{Name: pcsName}),
		testctx.WithInterval(250*time.Millisecond),
	)
	defer cleanup()

	pcs := testutils.NewPodCliqueSetBuilder(pcsName, tc.Namespace, uuid.NewUUID()).
		WithReplicas(1).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("c").
				WithReplicas(1).
				WithPodSpec(corev1.PodSpec{TerminationGracePeriodSeconds: ptr.To[int64](0)}).
				WithRoleName("worker").
				WithMinAvailable(1).
				WithLabels(map[string]string{"kai.scheduler/queue": "test"}).
				Build(),
		).
		WithPodCliqueScalingGroupConfig(
			grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:         groupName,
				CliqueNames:  []string{cliqueName},
				Replicas:     ptr.To(int32(1)),
				MinAvailable: ptr.To(int32(1)),
			},
		).
		Build()

	err := tc.Client.Create(ctx, pcs)
	require.NoError(t, err)

	_, err = tc.WaitForPodCount(1)
	require.NoError(t, err)

	pcsgs, err := componentutils.GetPCSGsForPCS(ctx, tc.Client, client.ObjectKeyFromObject(pcs))
	require.NoError(t, err)
	require.Len(t, pcsgs, 1)

	pcsg := &pcsgs[0]

	Logger.Info("01. Scaling PCSG to 2 replicas")
	err = workload.Scale(ctx, tc.Client, pcsg, 2)
	require.NoError(t, err)
	_, err = tc.WaitForPodCount(2)
	require.NoError(t, err)

	Logger.Info("02. Scaling PCSG to 0 replicas")
	err = workload.Scale(ctx, tc.Client, pcsg, 0, client.DryRunAll)
	require.Error(t, err)

	Logger.Info("03. Scaling PCSG to 10 replicas")
	err = workload.Scale(ctx, tc.Client, pcsg, 10, client.DryRunAll)
	require.NoError(t, err)

	Logger.Info("04. Scaling PCSG to 11 replicas")
	err = workload.Scale(ctx, tc.Client, pcsg, 11, client.DryRunAll)
	require.ErrorContains(t, err, "generated pod hostname")
}

// Test_N06_ResourceClaim_Scale verifies replica-dependent ResourceClaim reference validation on the
// PodCliqueSet scale subresource.
func Test_N06_ResourceClaim_Scale(t *testing.T) {
	ctx := context.Background()

	pcsName := strings.Repeat("n", 50)
	cliqueName := "c"

	tc, cleanup := testctx.PrepareTest(
		ctx,
		t,
		0,
		testctx.WithWorkload(&testctx.WorkloadConfig{Name: pcsName}),
		testctx.WithInterval(250*time.Millisecond),
	)
	defer cleanup()

	rctName := strings.Repeat("x", 10)

	rct := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: rctName, Namespace: tc.Namespace},
		Spec: resourcev1.ResourceClaimTemplateSpec{
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{
						{
							Name: "dev",
							Exactly: &resourcev1.ExactDeviceRequest{
								DeviceClassName: "class",
							},
						},
					},
				},
			},
		},
	}

	err := tc.Client.Create(tc.Ctx, rct)
	require.NoError(t, err)

	pcs := testutils.NewPodCliqueSetBuilder(pcsName, tc.Namespace, uuid.NewUUID()).
		WithReplicas(1).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(cliqueName).
				WithReplicas(1).
				WithPodSpec(corev1.PodSpec{TerminationGracePeriodSeconds: ptr.To[int64](0)}).
				WithRoleName("worker").
				WithMinAvailable(1).
				WithLabels(map[string]string{"kai.scheduler/queue": "test"}).
				Build(),
		).
		WithResourceSharing(grovecorev1alpha1.PCSResourceSharingSpec{
			ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{
				Name:      rctName,
				Namespace: tc.Namespace,
				Scope:     grovecorev1alpha1.ResourceSharingScopePerReplica,
			},
		}).
		Build()

	err = tc.Client.Create(ctx, pcs)
	require.NoError(t, err)

	_, err = tc.WaitForPodCount(1)
	require.NoError(t, err)

	Logger.Info("01. Scaling PCS to 10 replicas")
	err = workload.Scale(ctx, tc.Client, pcs, 10, client.DryRunAll)
	require.NoError(t, err)

	Logger.Info("02. Scaling PCS to 11 replicas")
	err = workload.Scale(ctx, tc.Client, pcs, 11, client.DryRunAll)
	require.ErrorContains(t, err, "generated pod resource claim reference name")
}

// verifyWorkloadOnCreate checks a workload against by executing a dry-run create call.
// If errorContains are specified, it will check that the error contains those values.
// If errorContains is empty, the request must succeed.
func verifyWorkloadOnCreate(t *testing.T, tc *testctx.TestContext, pcs *grovecorev1alpha1.PodCliqueSet, errorContains ...string) {
	t.Helper()
	err := tc.Client.Create(tc.Ctx, pcs, client.DryRunAll)
	if len(errorContains) == 0 {
		require.NoError(t, err, "expected workload to succeed validation")
	}
	for _, want := range errorContains {
		assert.ErrorContains(t, err, want)
	}
}

// podIndexes extracts the pod index from all clique pods.
// If there are any duplicates or invalid values, it will return an error.
func podIndexes(pods []corev1.Pod) ([]int, error) {
	indexes := sets.New[int]()
	for _, pod := range pods {
		indexValue, ok := pod.Labels[apicommon.LabelPodCliquePodIndex]
		if !ok {
			return nil, fmt.Errorf("pod %q is missing %q", pod.Name, apicommon.LabelPodCliquePodIndex)
		}
		podIndex, err := strconv.Atoi(indexValue)
		if err != nil {
			return nil, fmt.Errorf("pod %q has invalid index label %q: %v", pod.Name, indexValue, err)
		}
		if indexes.Has(podIndex) {
			return nil, fmt.Errorf("pod index %d appears more than once", podIndex)
		}
		indexes.Insert(podIndex)
	}
	return indexes.UnsortedList(), nil
}
