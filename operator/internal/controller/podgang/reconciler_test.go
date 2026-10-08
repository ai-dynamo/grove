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

package podgang

import (
	"context"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReconcileSyncsTerminatingPodGang(t *testing.T) {
	podGang := testutils.NewPodGangBuilder("test", "default").
		WithManaged(true).
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		WithDeletionTimestamp().
		Build()
	podGang.Finalizers = []string{"test.grove.io/hold"}
	cl := testutils.CreateDefaultFakeClient([]client.Object{podGang})
	backend := &recordingBackend{Backend: testutils.NewFakeSchedulerBackend(string(configv1alpha1.SchedulerNameKai))}
	registry := &testutils.FakeSchedulerRegistry{
		Backends:       map[string]scheduler.Backend{backend.Name(): backend},
		DefaultBackend: backend.Name(),
	}
	reconciler := &Reconciler{Client: cl, schedRegistry: registry}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(podGang)})
	require.NoError(t, err)
	require.NotNil(t, backend.synced)
	assert.False(t, backend.synced.DeletionTimestamp.IsZero())
}

type recordingBackend struct {
	scheduler.Backend
	synced *groveschedulerv1alpha1.PodGang
}

func (b *recordingBackend) SyncPodGang(_ context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	b.synced = podGang.DeepCopy()
	return nil
}
