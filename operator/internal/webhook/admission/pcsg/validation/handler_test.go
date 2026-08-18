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

package validation

import (
	"context"
	"strconv"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testNamespace = "default"
	testPCSName   = "test-pcs"
	testPCSGName  = "test-pcs-0-sg"
)

func TestValidateUpdate_NoReplicasChange(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	h := newHandler(cl)

	old, neu := buildPCSG(0, 2), buildPCSG(0, 2)
	neu.Annotations = map[string]string{"foo": "bar"}

	warnings, err := h.ValidateUpdate(context.Background(), old, neu)
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestValidateUpdate_ReplicasChange_NoCoherentUpdate(t *testing.T) {
	pcs := buildPCS(nil)
	cl := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
	h := newHandler(cl)

	old, neu := buildPCSG(0, 2), buildPCSG(0, 4)

	warnings, err := h.ValidateUpdate(context.Background(), old, neu)
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestValidateUpdate_ReplicasChange_ReplicaUnderCoherentUpdate(t *testing.T) {
	// Coherent update in progress on replica 0, and this PCSG belongs to replica 0 => blocked.
	pcs := buildPCS([]int32{0})
	cl := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
	h := newHandler(cl)

	old, neu := buildPCSG(0, 2), buildPCSG(0, 4)

	warnings, err := h.ValidateUpdate(context.Background(), old, neu)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coherent update is in progress")
	assert.Nil(t, warnings)
}

func TestValidateUpdate_ReplicasChange_DifferentReplicaUnderCoherentUpdate(t *testing.T) {
	// Coherent update in progress on replica 0, but this PCSG belongs to replica 1 => allowed.
	pcs := buildPCS([]int32{0})
	cl := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
	h := newHandler(cl)

	old, neu := buildPCSG(1, 2), buildPCSG(1, 4)

	warnings, err := h.ValidateUpdate(context.Background(), old, neu)
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestValidateUpdate_ReplicasChange_OwningPCSNotFound(t *testing.T) {
	// Orphaned PCSG (owning PCS missing); webhook fails open and allows the change
	// rather than blocking recovery actions.
	cl := testutils.NewTestClientBuilder().Build()
	h := newHandler(cl)

	old, neu := buildPCSG(0, 2), buildPCSG(0, 4)

	warnings, err := h.ValidateUpdate(context.Background(), old, neu)
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestValidateUpdate_ReplicasChange_MissingReplicaIndexLabel(t *testing.T) {
	// A PCSG missing the replica-index label is a contract violation; the webhook fails closed.
	pcs := buildPCS([]int32{0})
	cl := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
	h := newHandler(cl)

	old, neu := buildPCSG(0, 2), buildPCSG(0, 4)
	delete(neu.Labels, apicommon.LabelPodCliqueSetReplicaIndex)

	warnings, err := h.ValidateUpdate(context.Background(), old, neu)
	require.Error(t, err)
	assert.Nil(t, warnings)
}

func TestValidateCreate_AlwaysAllowed(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	h := newHandler(cl)

	warnings, err := h.ValidateCreate(context.Background(), buildPCSG(0, 4))
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestValidateDelete_AlwaysAllowed(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	h := newHandler(cl)

	warnings, err := h.ValidateDelete(context.Background(), buildPCSG(0, 4))
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func newHandler(cl client.Client) *Handler {
	return &Handler{
		logger: logr.Discard(),
		client: cl,
	}
}

func buildPCSG(pcsReplicaIndex, replicas int32) *grovecorev1alpha1.PodCliqueScalingGroup {
	return &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPCSGName,
			Namespace: testNamespace,
			Labels: map[string]string{
				apicommon.LabelPartOfKey:                testPCSName,
				apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(int(pcsReplicaIndex)),
			},
		},
		Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: replicas},
	}
}

// buildPCS builds a Coherent-strategy PodCliqueSet. When updatingReplicaIndices is non-nil an update
// is in flight with those replica indices in CurrentlyUpdating (UpdateEndedAt unset). A nil argument
// means no update is in progress.
func buildPCS(updatingReplicaIndices []int32) *grovecorev1alpha1.PodCliqueSet {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
		},
	}
	if updatingReplicaIndices != nil {
		currentlyUpdating := make([]grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress, 0, len(updatingReplicaIndices))
		for _, idx := range updatingReplicaIndices {
			currentlyUpdating = append(currentlyUpdating, grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{ReplicaIndex: idx})
		}
		pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
			UpdateStartedAt:   metav1.NewTime(time.Now()),
			CurrentlyUpdating: currentlyUpdating,
		}
	}
	return pcs
}
