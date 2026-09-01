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

package validation

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestHandlePodCliqueUpdate(t *testing.T) {
	owned := testutils.NewPCSGPodCliqueBuilder("owned", "default", "pcs", "group", 0, 0).WithReplicas(1).Build()
	setPCSGControllerOwner(owned, "group")
	standalone := testutils.NewPodCliqueBuilder("pcs", types.UID("uid"), "worker", "default", 0).WithReplicas(1).Build()

	tests := []struct {
		name    string
		oldPCLQ *grovecorev1alpha1.PodClique
		newPCLQ *grovecorev1alpha1.PodClique
		allowed bool
		message string
	}{
		{
			name:    "owned replica change is denied",
			oldPCLQ: owned,
			newPCLQ: owned.DeepCopy(),
			allowed: false,
			message: denialMessage,
		},
		{
			name:    "owned unchanged replicas are allowed",
			oldPCLQ: owned,
			newPCLQ: owned.DeepCopy(),
			allowed: true,
			message: "PodClique update is valid",
		},
		{
			name:    "standalone replica change is allowed",
			oldPCLQ: standalone,
			newPCLQ: standalone.DeepCopy(),
			allowed: true,
			message: "PodClique update is valid",
		},
		{
			name:    "removing ownership while changing replicas is denied",
			oldPCLQ: owned,
			newPCLQ: owned.DeepCopy(),
			allowed: false,
			message: denialMessage,
		},
		{
			name:    "removing owner label does not bypass controller ownership",
			oldPCLQ: owned,
			newPCLQ: owned.DeepCopy(),
			allowed: false,
			message: denialMessage,
		},
		{
			name:    "adding ownership while changing replicas is denied",
			oldPCLQ: standalone,
			newPCLQ: standalone.DeepCopy(),
			allowed: false,
			message: denialMessage,
		},
	}
	tests[0].newPCLQ.Spec.Replicas = 0
	tests[2].newPCLQ.Spec.Replicas = 0
	delete(tests[3].newPCLQ.Labels, apicommon.LabelPodCliqueScalingGroup)
	tests[3].newPCLQ.Spec.Replicas = 0
	delete(tests[4].newPCLQ.Labels, apicommon.LabelPodCliqueScalingGroup)
	tests[4].newPCLQ.Spec.Replicas = 0
	tests[5].newPCLQ.Labels[apicommon.LabelPodCliqueScalingGroup] = "group"
	tests[5].newPCLQ.Spec.Replicas = 0

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldRaw, err := json.Marshal(tt.oldPCLQ)
			require.NoError(t, err)
			newRaw, err := json.Marshal(tt.newPCLQ)
			require.NoError(t, err)

			resp := (&Handler{}).Handle(t.Context(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				OldObject: runtime.RawExtension{Raw: oldRaw},
				Object:    runtime.RawExtension{Raw: newRaw},
			}})
			assert.Equal(t, tt.allowed, resp.Allowed)
			require.NotNil(t, resp.Result)
			assert.Equal(t, tt.message, resp.Result.Message)
		})
	}
}

func TestHandlePodCliqueUpdateDecodeErrors(t *testing.T) {
	tests := []struct {
		name    string
		oldRaw  []byte
		newRaw  []byte
		wantErr string
	}{
		{name: "invalid old object", oldRaw: []byte("{"), newRaw: []byte("{}"), wantErr: "decoding old PodClique"},
		{name: "invalid new object", oldRaw: []byte("{}"), newRaw: []byte("{"), wantErr: "decoding new PodClique"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := (&Handler{}).Handle(t.Context(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				OldObject: runtime.RawExtension{Raw: tt.oldRaw},
				Object:    runtime.RawExtension{Raw: tt.newRaw},
			}})
			assert.False(t, resp.Allowed)
			require.NotNil(t, resp.Result)
			assert.EqualValues(t, http.StatusBadRequest, resp.Result.Code)
			assert.Contains(t, resp.Result.Message, tt.wantErr)
		})
	}
}

func TestHandleScaleUpdate(t *testing.T) {
	owned := testutils.NewPCSGPodCliqueBuilder("owned", "default", "pcs", "group", 0, 0).WithReplicas(1).Build()
	setPCSGControllerOwner(owned, "group")
	delete(owned.Labels, apicommon.LabelPodCliqueScalingGroup)
	legacyOwned := testutils.NewPCSGPodCliqueBuilder("legacy-owned", "default", "pcs", "group", 0, 0).WithReplicas(1).Build()
	standalone := testutils.NewPodCliqueBuilder("pcs", types.UID("uid"), "worker", "default", 0).WithReplicas(1).Build()
	cl := testutils.NewTestClientBuilder().WithObjects(owned, legacyOwned, standalone).Build()
	h := &Handler{reader: cl, logger: logr.Discard()}

	tests := []struct {
		name     string
		pclqName string
		replicas int32
		allowed  bool
		message  string
	}{
		{name: "owned replica change is denied", pclqName: owned.Name, replicas: 0, allowed: false, message: denialMessage},
		{name: "owned unchanged replicas are allowed", pclqName: owned.Name, replicas: 1, allowed: true, message: "PodClique scale update does not change replicas"},
		{name: "legacy label ownership is denied", pclqName: legacyOwned.Name, replicas: 0, allowed: false, message: denialMessage},
		{name: "standalone replica change is allowed", pclqName: standalone.Name, replicas: 0, allowed: true, message: "standalone PodClique scale update is valid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.Handle(t.Context(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Name:        tt.pclqName,
				Namespace:   "default",
				Operation:   admissionv1.Update,
				SubResource: "scale",
				Object:      runtime.RawExtension{Raw: marshalScale(t, tt.pclqName, tt.replicas)},
			}})
			assert.Equal(t, tt.allowed, resp.Allowed)
			require.NotNil(t, resp.Result)
			assert.Equal(t, tt.message, resp.Result.Message)
		})
	}
}

func TestHandleScaleUpdateErrors(t *testing.T) {
	owned := testutils.NewPCSGPodCliqueBuilder("owned", "default", "pcs", "group", 0, 0).Build()
	getErr := apierrors.NewInternalError(errors.New("injected get failure"))
	cl := testutils.NewTestClientBuilder().
		WithObjects(owned).
		RecordErrorForObjects(testutils.ClientMethodGet, getErr, types.NamespacedName{Namespace: owned.Namespace, Name: owned.Name}).
		Build()
	h := &Handler{reader: cl, logger: logr.Discard()}

	tests := []struct {
		name     string
		raw      []byte
		pclqName string
		wantCode int
		wantErr  string
	}{
		{name: "invalid scale object", raw: []byte("{"), pclqName: owned.Name, wantCode: http.StatusBadRequest, wantErr: "decoding Scale"},
		{name: "PodClique read failure", raw: marshalScale(t, owned.Name, 0), pclqName: owned.Name, wantCode: http.StatusInternalServerError, wantErr: "reading PodClique"},
		{name: "PodClique not found", raw: marshalScale(t, "missing", 0), pclqName: "missing", wantCode: http.StatusInternalServerError, wantErr: "reading PodClique"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.Handle(t.Context(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Name:        tt.pclqName,
				Namespace:   "default",
				Operation:   admissionv1.Update,
				SubResource: "scale",
				Object:      runtime.RawExtension{Raw: tt.raw},
			}})
			assert.False(t, resp.Allowed)
			require.NotNil(t, resp.Result)
			assert.EqualValues(t, tt.wantCode, resp.Result.Code)
			assert.Contains(t, resp.Result.Message, tt.wantErr)
		})
	}
}

func TestHandleAllowsNonUpdateOperation(t *testing.T) {
	resp := (&Handler{}).Handle(t.Context(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create,
	}})
	assert.True(t, resp.Allowed)
	require.NotNil(t, resp.Result)
	assert.Equal(t, "operation does not update a PodClique", resp.Result.Message)
}

func TestIsScalingGroupOwned(t *testing.T) {
	controller := true
	tests := []struct {
		name  string
		pclq  *grovecorev1alpha1.PodClique
		owned bool
	}{
		{
			name: "controller owner",
			pclq: &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{
				APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
				Kind:       "PodCliqueScalingGroup",
				Controller: &controller,
			}}}},
			owned: true,
		},
		{
			name:  "legacy label",
			pclq:  &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{apicommon.LabelPodCliqueScalingGroup: "group"}}},
			owned: true,
		},
		{
			name: "non-controller owner",
			pclq: &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{
				APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
				Kind:       "PodCliqueScalingGroup",
			}}}},
		},
		{
			name: "different API version",
			pclq: &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "grove.io/v2",
				Kind:       "PodCliqueScalingGroup",
				Controller: &controller,
			}}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.owned, isScalingGroupOwned(tt.pclq))
		})
	}
}

func setPCSGControllerOwner(pclq *grovecorev1alpha1.PodClique, name string) {
	controller := true
	pclq.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
		Kind:       "PodCliqueScalingGroup",
		Name:       name,
		Controller: &controller,
	}}
}

func marshalScale(t *testing.T, name string, replicas int32) []byte {
	t.Helper()
	raw, err := json.Marshal(&autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
	})
	require.NoError(t, err)
	return raw
}
