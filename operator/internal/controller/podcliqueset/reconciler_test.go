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

package podcliqueset

import (
	"testing"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestEnsureScalingProgressExpectationSatisfied(t *testing.T) {
	objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: types.UID("uid")}
	expectedProgress := &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1}}
	expectations := &pcsexpectations.ScalingProgress{}
	expectations.Record(objectMeta, expectedProgress)
	reconciler := &Reconciler{scalingProgressExpectations: expectations}
	pcs := &grovecorev1beta1.PodCliqueSet{ObjectMeta: objectMeta}

	result := reconciler.ensureScalingProgressExpectationSatisfied(pcs)
	assert.True(t, result.NeedsRequeue())

	pcs.Status.ScalingProgress = expectedProgress
	result = reconciler.ensureScalingProgressExpectationSatisfied(pcs)
	assert.False(t, result.NeedsRequeue())
	assert.False(t, ctrlcommon.ShortCircuitReconcileFlow(result))

	pcs.Status.ScalingProgress = nil
	result = reconciler.ensureScalingProgressExpectationSatisfied(pcs)
	assert.False(t, result.NeedsRequeue())
	assert.False(t, ctrlcommon.ShortCircuitReconcileFlow(result))
}

func TestReconcileWaitsForGenerationBeforeFinalizer(t *testing.T) {
	t.Parallel()
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").Build()
	cl := testutils.SetupFakeClient(pcs)
	r := &Reconciler{
		client: cl, reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(cl),
		scalingProgressExpectations: &pcsexpectations.ScalingProgress{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
		operatorRegistry: component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result](),
	}
	r.pcsGenerationStatusExpectations.Record(pcs.ObjectMeta, "persisted-generation", new("persisted-scheduling"))

	result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pcs)})

	require.NoError(t, err)
	assert.Equal(t, constants.InformerCacheSyncRetryInterval, result.RequeueAfter)
	stored := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), stored))
	assert.Empty(t, stored.Finalizers, "a stale generation must stop before the first spec write")
	assert.Nil(t, stored.Status.ObservedGeneration)
	assert.False(t, r.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil))
}

func TestReconcileDeletionPrecedesGenerationExpectation(t *testing.T) {
	t.Parallel()
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").Build()
	pcs.Finalizers = []string{apiconstants.FinalizerPodCliqueSet}
	pcs.DeletionTimestamp = new(metav1.Now())
	cl := testutils.SetupFakeClient(pcs)
	r := &Reconciler{
		client: cl, reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(cl),
		scalingProgressExpectations: &pcsexpectations.ScalingProgress{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
		operatorRegistry: component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result](),
	}
	r.pcsGenerationStatusExpectations.Record(pcs.ObjectMeta, "persisted-generation", nil)

	result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pcs)})

	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)
	assert.True(t, apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), &grovecorev1beta1.PodCliqueSet{})))
	assert.True(t, r.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil))
}

func TestGenerationExpectationDoesNotBlockRecreatedPCS(t *testing.T) {
	t.Parallel()
	oldPCS := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "old-uid").Build()
	newPCS := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "new-uid").Build()
	r := &Reconciler{}
	r.pcsGenerationStatusExpectations.Record(oldPCS.ObjectMeta, "old-generation", nil)

	assert.False(t, ctrlcommon.ShortCircuitReconcileFlow(r.ensureGenerationStatusExpectationSatisfied(newPCS)))
	assert.False(t, r.pcsGenerationStatusExpectations.ClearIfSatisfied(oldPCS.ObjectMeta, nil, nil), "another PCS must not consume the old expectation")
}
