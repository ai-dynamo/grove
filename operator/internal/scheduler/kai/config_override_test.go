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

package kai

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// runaiProfile is the kai-scheduler profile a distribution that bundles KAI under different names sets.
func runaiProfile(raw string) configv1beta1.SchedulerProfile {
	return configv1beta1.SchedulerProfile{
		Name:   configv1beta1.SchedulerNameKai,
		Config: &runtime.RawExtension{Raw: []byte(raw)},
	}
}

const runaiConfig = `{"schedulerName":"runai-scheduler","queueLabelKey":"runai/queue","nodePoolLabelKey":"runai/node-pool"}`

func TestResolveConfiguration_Defaults(t *testing.T) {
	for name, profile := range map[string]configv1beta1.SchedulerProfile{
		"no config":    {Name: configv1beta1.SchedulerNameKai},
		"empty config": runaiProfile(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := resolveConfiguration(profile)
			require.NoError(t, err)
			assert.Equal(t, "kai-scheduler", cfg.SchedulerName)
			assert.Equal(t, "kai.scheduler/queue", cfg.QueueLabelKey)
			assert.Equal(t, "kai.scheduler/node-pool", cfg.NodePoolLabelKey)
		})
	}
}

func TestResolveConfiguration_PartialOverrideKeepsOtherDefaults(t *testing.T) {
	cfg, err := resolveConfiguration(runaiProfile(`{"schedulerName":"my-kai"}`))
	require.NoError(t, err)
	assert.Equal(t, "my-kai", cfg.SchedulerName)
	assert.Equal(t, "kai.scheduler/queue", cfg.QueueLabelKey)
	assert.Equal(t, "kai.scheduler/node-pool", cfg.NodePoolLabelKey)
}

func TestResolveConfiguration_Validation(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string // substring of the expected error; empty means valid
	}{
		{name: "run:ai names", config: runaiConfig},
		{name: "unknown field is rejected", config: `{"schedulerNme":"x"}`, wantErr: `unknown field "schedulerNme"`},
		{name: "malformed json", config: `{nope`, wantErr: "invalid kai-scheduler config"},
		{name: "scheduler name must be a DNS subdomain", config: `{"schedulerName":"Bad_Name"}`, wantErr: "config.schedulerName"},
		{name: "queue label key must be a qualified name", config: `{"queueLabelKey":"has space"}`, wantErr: "config.queueLabelKey"},
		{name: "node pool label key must be a qualified name", config: `{"nodePoolLabelKey":"a/b/c"}`, wantErr: "config.nodePoolLabelKey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveConfiguration(runaiProfile(tt.config))
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestBackend_Init_RejectsInvalidConfig(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), runaiProfile(`{"queueLabelKey":"has space"}`))
	require.ErrorContains(t, b.Init(cl), "config.queueLabelKey")
}

func TestBackend_NameAndPreparePod_UseConfiguredSchedulerName(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), runaiProfile(runaiConfig))
	// Name keys the scheduler registry, so it is the pod-facing name, not the profile name.
	assert.Equal(t, "runai-scheduler", b.Name())

	pod := testutils.NewPodBuilder("test-pod", "default").Build()
	pod.Labels = map[string]string{
		apicommon.LabelPodGang:   "test-podgang",
		apicommon.LabelPodClique: "test-clique",
	}
	require.NoError(t, b.PreparePod(pod))
	assert.Equal(t, "runai-scheduler", pod.Spec.SchedulerName)
	// The contract with KAI's podgrouper is independent of the configured names.
	assert.Equal(t, "true", pod.Annotations["kai.scheduler/skip-podgrouper"])
	assert.Equal(t, "test-podgang", pod.Annotations["pod-group-name"])
}

func TestBackend_ValidatePodCliqueSet_ReadsConfiguredQueueKey(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), runaiProfile(runaiConfig))

	withRunai := newPodCliqueSet("with-runai", "")
	withRunai.Labels = map[string]string{"runai/queue": "team-a"}
	require.NoError(t, b.ValidatePodCliqueSet(context.Background(), withRunai))

	// A queue under the upstream key is invisible once the key is overridden.
	upstreamOnly := newPodCliqueSet("upstream-only", "team-a")
	require.ErrorContains(t, b.ValidatePodCliqueSet(context.Background(), upstreamOnly), "no KAI queue is configured")
}

func TestBackend_SyncPodGang_UsesConfiguredQueueAndPreservesNodePool(t *testing.T) {
	pcs := newPodCliqueSet("pcs", "")
	pcs.Labels = map[string]string{"runai/queue": "cmp-l40s"}
	podGang := testutils.NewPodGangBuilder("test-podgang", "default").
		WithSchedulerName("runai-scheduler").
		WithPodGroup("worker", 1).
		Build()
	setPodCliqueSetControllerOwner(podGang, pcs)

	cl := testutils.NewTestClientBuilder().WithObjects(podGang, pcs).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), runaiProfile(runaiConfig))
	require.NoError(t, b.Init(cl))
	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	created := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(podGang), created))
	assert.Equal(t, "cmp-l40s", created.Spec.Queue)

	// The scheduler's runtime stamps the node pool on the PodGroup under ITS key; Grove must not drop it.
	created.Labels = map[string]string{"runai/node-pool": "default"}
	require.NoError(t, cl.Update(context.Background(), created))
	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	updated := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(podGang), updated))
	assert.Equal(t, "default", updated.Labels["runai/node-pool"])
}
