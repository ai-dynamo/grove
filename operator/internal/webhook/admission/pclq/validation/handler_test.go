// /*
// Copyright 2025 The Grove Authors.
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
	testPCLQName  = "test-pcs-0-frontend"
)

func TestValidateUpdate_NoReplicasChange(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	h := newHandler(cl)

	old, neu := buildPCLQ(2), buildPCLQ(2)
	neu.Annotations = map[string]string{"foo": "bar"}

	warnings, err := h.ValidateUpdate(context.Background(), old, neu)
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestValidateUpdate_ReplicasChange_NoCoherentUpdate(t *testing.T) {
	pcs := buildPCS(false)
	cl := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
	h := newHandler(cl)

	old, neu := buildPCLQ(2), buildPCLQ(4)

	warnings, err := h.ValidateUpdate(context.Background(), old, neu)
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestValidateUpdate_ReplicasChange_CoherentUpdateInProgress(t *testing.T) {
	pcs := buildPCS(true)
	cl := testutils.NewTestClientBuilder().WithObjects(pcs).Build()
	h := newHandler(cl)

	old, neu := buildPCLQ(2), buildPCLQ(4)

	warnings, err := h.ValidateUpdate(context.Background(), old, neu)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coherent update is in progress")
	assert.Nil(t, warnings)
}

func TestValidateUpdate_ReplicasChange_OwningPCSNotFound(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	h := newHandler(cl)

	old, neu := buildPCLQ(2), buildPCLQ(4)

	warnings, err := h.ValidateUpdate(context.Background(), old, neu)
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestValidateCreate_AlwaysAllowed(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	h := newHandler(cl)

	warnings, err := h.ValidateCreate(context.Background(), buildPCLQ(4))
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestValidateDelete_AlwaysAllowed(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	h := newHandler(cl)

	warnings, err := h.ValidateDelete(context.Background(), buildPCLQ(4))
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func newHandler(cl client.Client) *Handler {
	return &Handler{
		logger: logr.Discard(),
		client: cl,
	}
}

func buildPCLQ(replicas int32) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPCLQName,
			Namespace: testNamespace,
			Labels:    map[string]string{apicommon.LabelPartOfKey: testPCSName},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: replicas},
	}
}

func buildPCS(updateInProgress bool) *grovecorev1alpha1.PodCliqueSet {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
		},
	}
	if updateInProgress {
		pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
			UpdateStartedAt: metav1.NewTime(time.Now()),
			UpdateEndedAt:   nil,
		}
	}
	return pcs
}
