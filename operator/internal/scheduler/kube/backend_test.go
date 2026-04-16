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

package kube

import (
	"context"
	"encoding/json"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	schedulingv1alpha1 "k8s.io/api/scheduling/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// newGangSchedulingProfile returns a SchedulerProfile with GangScheduling enabled.
func newGangSchedulingProfile(t *testing.T) configv1alpha1.SchedulerProfile {
	t.Helper()
	cfgBytes, err := json.Marshal(configv1alpha1.KubeSchedulerConfig{GangScheduling: true})
	require.NoError(t, err)
	return configv1alpha1.SchedulerProfile{
		Name:   configv1alpha1.SchedulerNameKube,
		Config: &runtime.RawExtension{Raw: cfgBytes},
	}
}

func TestBackend_PreparePod_GangSchedulingDisabled(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKube}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").Build()
	b.PreparePod(pod)

	assert.Equal(t, string(configv1alpha1.SchedulerNameKube), pod.Spec.SchedulerName)
	assert.Nil(t, pod.Spec.WorkloadRef, "WorkloadRef should not be set when GangScheduling is disabled")
}

func TestBackend_PreparePod_GangSchedulingEnabled(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	b := New(cl, cl.Scheme(), recorder, newGangSchedulingProfile(t))

	pod := testutils.NewPodBuilder("test-pod", "default").
		WithLabels(map[string]string{
			apicommon.LabelPodGang:   "my-podgang",
			apicommon.LabelPodClique: "my-podclique",
		}).
		Build()
	b.PreparePod(pod)

	assert.Equal(t, string(configv1alpha1.SchedulerNameKube), pod.Spec.SchedulerName)
	require.NotNil(t, pod.Spec.WorkloadRef, "WorkloadRef should be set when GangScheduling is enabled")
	assert.Equal(t, "my-podgang", pod.Spec.WorkloadRef.Name)
	assert.Equal(t, "my-podclique", pod.Spec.WorkloadRef.PodGroup)
}

func TestBackend_PreparePod_GangSchedulingEnabled_MissingLabels(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	b := New(cl, cl.Scheme(), recorder, newGangSchedulingProfile(t))

	// Pod with no podgang/podclique labels — WorkloadRef must not be set.
	pod := testutils.NewPodBuilder("test-pod", "default").Build()
	b.PreparePod(pod)

	assert.Equal(t, string(configv1alpha1.SchedulerNameKube), pod.Spec.SchedulerName)
	assert.Nil(t, pod.Spec.WorkloadRef, "WorkloadRef should not be set when pod labels are missing")
}

func TestBackend_SyncPodGang_GangSchedulingDisabled(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKube}
	b := New(cl, cl.Scheme(), recorder, profile)

	podGang := testutils.NewPodGangBuilder("my-podgang", "default").
		WithPodGroup("clique-a", 2).
		Build()

	err := b.SyncPodGang(context.Background(), podGang)
	require.NoError(t, err)

	// No Workload should have been created.
	workloadList := &schedulingv1alpha1.WorkloadList{}
	require.NoError(t, cl.List(context.Background(), workloadList, client.InNamespace("default")))
	assert.Empty(t, workloadList.Items, "no Workload should be created when GangScheduling is disabled")
}

func TestBackend_SyncPodGang_GangSchedulingEnabled_Create(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	b := New(cl, cl.Scheme(), recorder, newGangSchedulingProfile(t))

	podGang := testutils.NewPodGangBuilder("my-podgang", "default").
		WithPodGroup("clique-a", 2).
		WithPodGroup("clique-b", 3).
		Build()

	err := b.SyncPodGang(context.Background(), podGang)
	require.NoError(t, err)

	// Workload should exist with the same name and namespace as the PodGang.
	workload := &schedulingv1alpha1.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "my-podgang"}, workload))

	require.Len(t, workload.Spec.PodGroups, 2)

	assert.Equal(t, "clique-a", workload.Spec.PodGroups[0].Name)
	require.NotNil(t, workload.Spec.PodGroups[0].Policy.Gang)
	assert.Equal(t, int32(2), workload.Spec.PodGroups[0].Policy.Gang.MinCount)

	assert.Equal(t, "clique-b", workload.Spec.PodGroups[1].Name)
	require.NotNil(t, workload.Spec.PodGroups[1].Policy.Gang)
	assert.Equal(t, int32(3), workload.Spec.PodGroups[1].Policy.Gang.MinCount)
}

func TestBackend_SyncPodGang_GangSchedulingEnabled_RecreateOnChange(t *testing.T) {
	// Pre-create a Workload with MinCount=2 for clique-a.
	podGang := testutils.NewPodGangBuilder("my-podgang", "default").
		WithPodGroup("clique-a", 2).
		Build()

	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	b := New(cl, cl.Scheme(), recorder, newGangSchedulingProfile(t))

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	// Now sync again with MinCount=5 — Workload.Spec.PodGroups is immutable so the backend
	// must delete and recreate the Workload.
	podGang.Spec.PodGroups[0].MinReplicas = 5
	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	workload := &schedulingv1alpha1.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "my-podgang"}, workload))
	require.NotNil(t, workload.Spec.PodGroups[0].Policy.Gang)
	assert.Equal(t, int32(5), workload.Spec.PodGroups[0].Policy.Gang.MinCount)
}

func TestBackend_SyncPodGang_GangSchedulingEnabled_NoOpWhenUnchanged(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	b := New(cl, cl.Scheme(), recorder, newGangSchedulingProfile(t))

	podGang := testutils.NewPodGangBuilder("my-podgang", "default").
		WithPodGroup("clique-a", 2).
		Build()

	// First sync creates the Workload.
	require.NoError(t, b.SyncPodGang(context.Background(), podGang))
	// Second sync with identical spec must not fail.
	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	workload := &schedulingv1alpha1.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "my-podgang"}, workload))
	assert.Equal(t, int32(2), workload.Spec.PodGroups[0].Policy.Gang.MinCount)
}
