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
	"errors"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestSetBackendLastError(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs", Namespace: "default"},
		Status: grovecorev1alpha1.PodCliqueSetStatus{
			LastErrors: []grovecorev1alpha1.LastError{
				{
					Code:        "ERR_EXISTING",
					Description: "existing component error",
					ObservedAt:  metav1.Now(),
				},
				{
					Code:        errCodeSyncSchedulerBackend,
					Description: "podgang default/other-pg: other backend error",
					ObservedAt:  metav1.Now(),
				},
			},
		},
	}
	podGang := &groveschedulerv1alpha1.PodGang{ObjectMeta: metav1.ObjectMeta{Name: "test-pg", Namespace: "default"}}
	require.NoError(t, controllerutil.SetControllerReference(pcs, podGang, scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&grovecorev1alpha1.PodCliqueSet{}).
		WithObjects(pcs, podGang).
		Build()
	reconciler := &Reconciler{Client: cl, scheme: scheme}

	require.NoError(t, reconciler.setBackendLastError(ctx, podGang, errors.New("KAI migration failed")))
	got := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(pcs), got))
	require.Len(t, got.Status.LastErrors, 3)
	assert.Equal(t, grovecorev1alpha1.ErrorCode("ERR_EXISTING"), got.Status.LastErrors[0].Code)
	assert.Equal(t, "podgang default/other-pg: other backend error", got.Status.LastErrors[1].Description)
	assert.Equal(t, errCodeSyncSchedulerBackend, got.Status.LastErrors[2].Code)
	assert.Equal(t, "podgang default/test-pg: KAI migration failed", got.Status.LastErrors[2].Description)

	require.NoError(t, reconciler.setBackendLastError(ctx, podGang, nil))
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(pcs), got))
	require.Len(t, got.Status.LastErrors, 2)
	assert.Equal(t, grovecorev1alpha1.ErrorCode("ERR_EXISTING"), got.Status.LastErrors[0].Code)
	assert.Equal(t, "podgang default/other-pg: other backend error", got.Status.LastErrors[1].Description)
}
