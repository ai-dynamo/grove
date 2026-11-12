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
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const testNamespace = "test-namespace"

func TestHandlePodCliqueScale(t *testing.T) {
	boundaryPCS, boundaryPCLQ := standaloneFixture("p", strings.Repeat("c", 55))

	t.Run("accepts generated-hostname boundary", func(t *testing.T) {
		h := newTestHandler(t, boundaryPCS, boundaryPCLQ)
		resp := h.Handle(t.Context(), scaleRequest(t, "podcliques", boundaryPCLQ.Name, 1000))

		assert.True(t, resp.Allowed, responseMessage(resp))
		assert.Equal(t, int32(http.StatusOK), resp.Result.Code)
	})

	t.Run("rejects generated hostname beyond boundary", func(t *testing.T) {
		h := newTestHandler(t, boundaryPCS, boundaryPCLQ)
		resp := h.Handle(t.Context(), scaleRequest(t, "podcliques", boundaryPCLQ.Name, 1001))

		assert.False(t, resp.Allowed)
		assert.Contains(t, responseMessage(resp), "generated pod hostname")
	})

	t.Run("external scale is not limited by absent ScaleConfig", func(t *testing.T) {
		pcs, pclq := standaloneFixture("workload", "worker")
		require.Nil(t, pcs.Spec.Template.Cliques[0].Spec.ScaleConfig)
		h := newTestHandler(t, pcs, pclq)

		resp := h.Handle(t.Context(), scaleRequest(t, "podcliques", pclq.Name, 25))

		assert.True(t, resp.Allowed, responseMessage(resp))
	})

	t.Run("PCSG-owned PodClique honors generated-hostname boundary", func(t *testing.T) {
		pcs, pcsg := scalingGroupFixture("p", strings.Repeat("g", 31), strings.Repeat("c", 21))
		pclq := &grovecorev1beta1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name:      apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: 0}, pcsg.Spec.CliqueNames[0]),
				Namespace: testNamespace,
				Labels: map[string]string{
					apicommon.LabelPartOfKey:                         pcs.Name,
					apicommon.LabelPodCliqueSetReplicaIndex:          "0",
					apicommon.LabelPodCliqueScalingGroup:             pcsg.Name,
					apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
				},
			},
			Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)},
		}
		h := newTestHandler(t, pcs, pcsg, pclq)

		resp := h.Handle(t.Context(), scaleRequest(t, "podcliques", pclq.Name, 1000))
		assert.True(t, resp.Allowed, responseMessage(resp))

		resp = h.Handle(t.Context(), scaleRequest(t, "podcliques", pclq.Name, 1001))
		assert.False(t, resp.Allowed)
		assert.Contains(t, responseMessage(resp), "generated pod hostname")
	})
}

func TestHandlePodCliqueScalingGroupScale(t *testing.T) {
	boundaryPCS, boundaryPCSG := scalingGroupFixture("p", strings.Repeat("g", 31), strings.Repeat("c", 21))

	t.Run("accepts generated child hostname boundary", func(t *testing.T) {
		h := newTestHandler(t, boundaryPCS, boundaryPCSG)
		resp := h.Handle(t.Context(), scaleRequest(t, "podcliquescalinggroups", boundaryPCSG.Name, 1000))

		assert.True(t, resp.Allowed, responseMessage(resp))
	})

	t.Run("rejects generated child hostname beyond boundary", func(t *testing.T) {
		h := newTestHandler(t, boundaryPCS, boundaryPCSG)
		resp := h.Handle(t.Context(), scaleRequest(t, "podcliquescalinggroups", boundaryPCSG.Name, 1001))

		assert.False(t, resp.Allowed)
		assert.Contains(t, responseMessage(resp), "generated pod hostname")
	})

	t.Run("external scale is not limited by absent ScaleConfig", func(t *testing.T) {
		pcs, pcsg := scalingGroupFixture("workload", "workers", "worker")
		require.Nil(t, pcs.Spec.Template.PodCliqueScalingGroupConfigs[0].ScaleConfig)
		h := newTestHandler(t, pcs, pcsg)

		resp := h.Handle(t.Context(), scaleRequest(t, "podcliquescalinggroups", pcsg.Name, 25))

		assert.True(t, resp.Allowed, responseMessage(resp))
	})
}

func TestHandlePodCliqueSetScaleUsesFocusedValidation(t *testing.T) {
	pcs, _ := standaloneFixture("p", strings.Repeat("c", 55))
	h := newTestHandler(t, pcs)

	t.Run("scale to zero is accepted", func(t *testing.T) {
		resp := h.Handle(t.Context(), scaleRequest(t, "podcliquesets", pcs.Name, 0))
		assert.True(t, resp.Allowed, responseMessage(resp))
	})

	t.Run("generated hostname beyond boundary is rejected", func(t *testing.T) {
		resp := h.Handle(t.Context(), scaleRequest(t, "podcliquesets", pcs.Name, 1001))
		assert.False(t, resp.Allowed)
		assert.Equal(t, int32(http.StatusForbidden), resp.Result.Code)
		assert.Contains(t, responseMessage(resp), "generated pod hostname")
	})
}

func TestHandleScaleValidatesPodGangLabels(t *testing.T) {
	t.Run("PCS scale checks the anchor label", func(t *testing.T) {
		pcs, _ := standaloneFixture(strings.Repeat("p", 41), "worker")
		h := newTestHandler(t, pcs)
		resp := h.Handle(t.Context(), scaleRequest(t, "podcliquesets", pcs.Name, 10))
		require.True(t, resp.Allowed, responseMessage(resp))

		resp = h.Handle(t.Context(), scaleRequest(t, "podcliquesets", pcs.Name, 11))
		require.False(t, resp.Allowed)
		assert.Contains(t, responseMessage(resp), "generated PodGang label")
	})

	t.Run("PCSG scale and update check non-anchor labels", func(t *testing.T) {
		pcs, pcsg := scalingGroupFixture("p", strings.Repeat("g", 37), "worker")
		h := newTestHandler(t, pcs, pcsg)
		resp := h.Handle(t.Context(), scaleRequest(t, "podcliquescalinggroups", pcsg.Name, 10))
		require.True(t, resp.Allowed, responseMessage(resp))

		resp = h.Handle(t.Context(), scaleRequest(t, "podcliquescalinggroups", pcsg.Name, 11))
		require.False(t, resp.Allowed)
		assert.Contains(t, responseMessage(resp), "generated PodGang label")

		pcsg.Spec.Replicas = 11
		resp = h.Handle(t.Context(), mainRequest(t, "podcliquescalinggroups", admissionv1.Update, pcsg))
		require.False(t, resp.Allowed)
		assert.Contains(t, responseMessage(resp), "generated PodGang label")

		resp = h.Handle(t.Context(), scaleRequest(t, "podcliquescalinggroups", pcsg.Name, 0))
		require.True(t, resp.Allowed, responseMessage(resp))
	})
}

func TestHandleAnchorOnlyScalingGroup(t *testing.T) {
	for _, tt := range []struct {
		name        string
		oldReplicas int32
		replicas    int32
		allowed     bool
	}{
		{name: "unchanged anchor group", oldReplicas: 1, replicas: 1, allowed: true},
		{name: "scale down to the anchor", oldReplicas: 2, replicas: 1, allowed: true},
		{name: "scale to zero", oldReplicas: 1, replicas: 0, allowed: true},
		{name: "scale above the anchor", oldReplicas: 1, replicas: 2},
		{name: "restore from zero can create a non-anchor", oldReplicas: 0, replicas: 1},
	} {
		for _, subresource := range []string{"scale", ""} {
			t.Run(tt.name+"/"+subresource, func(t *testing.T) {
				pcs, pcsg := scalingGroupFixture("p", strings.Repeat("g", 38), "c")
				// The cached count remains one. The admission old object must determine scale-out.
				h := newTestHandler(t, pcs, pcsg)
				var req admission.Request
				if subresource == "scale" {
					req = scaleRequest(t, "podcliquescalinggroups", pcsg.Name, tt.replicas)
					req.OldObject = scaleRequest(t, "podcliquescalinggroups", pcsg.Name, tt.oldReplicas).Object
				} else {
					pcsg.Spec.Replicas = tt.oldReplicas
					oldObject := mainRequest(t, "podcliquescalinggroups", admissionv1.Update, pcsg).Object
					pcsg.Spec.Replicas = tt.replicas
					req = mainRequest(t, "podcliquescalinggroups", admissionv1.Update, pcsg)
					req.OldObject = oldObject
				}
				resp := h.Handle(t.Context(), req)
				require.Equal(t, tt.allowed, resp.Allowed, responseMessage(resp))
				if !tt.allowed {
					assert.Contains(t, responseMessage(resp), "generated PodGang label")
				}
			})
		}
	}
}

func TestHandlePCSScaleWithAnchorOnlyGroup(t *testing.T) {
	pcs, pcsg := scalingGroupFixture("p", strings.Repeat("g", 38), "c")
	pcsg.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(pcs, grovecorev1beta1.SchemeGroupVersion.WithKind("PodCliqueSet"))}
	h := newTestHandler(t, pcs, pcsg)
	resp := h.Handle(t.Context(), scaleRequest(t, "podcliquesets", pcs.Name, 2))
	require.True(t, resp.Allowed, responseMessage(resp))
}

func TestHandleMainResourceUpdatesUseIncomingObject(t *testing.T) {
	t.Run("PodClique", func(t *testing.T) {
		pcs, storedPCLQ := standaloneFixture("p", strings.Repeat("c", 55))
		h := newTestHandler(t, pcs, storedPCLQ)
		incomingPCLQ := storedPCLQ.DeepCopy()

		incomingPCLQ.Spec.Replicas = 1000
		resp := h.Handle(t.Context(), mainRequest(t, "podcliques", admissionv1.Update, incomingPCLQ))
		assert.True(t, resp.Allowed, responseMessage(resp))

		incomingPCLQ.Spec.Replicas = 1001
		resp = h.Handle(t.Context(), mainRequest(t, "podcliques", admissionv1.Update, incomingPCLQ))
		assert.False(t, resp.Allowed)
		assert.Contains(t, responseMessage(resp), "generated pod hostname")
	})

	t.Run("PodCliqueScalingGroup", func(t *testing.T) {
		pcs, storedPCSG := scalingGroupFixture("p", strings.Repeat("g", 31), strings.Repeat("c", 21))
		h := newTestHandler(t, pcs, storedPCSG)
		incomingPCSG := storedPCSG.DeepCopy()

		incomingPCSG.Spec.Replicas = 1000
		resp := h.Handle(t.Context(), mainRequest(t, "podcliquescalinggroups", admissionv1.Update, incomingPCSG))
		assert.True(t, resp.Allowed, responseMessage(resp))

		incomingPCSG.Spec.Replicas = 1001
		resp = h.Handle(t.Context(), mainRequest(t, "podcliquescalinggroups", admissionv1.Update, incomingPCSG))
		assert.False(t, resp.Allowed)
		assert.Contains(t, responseMessage(resp), "generated pod hostname")
	})
}

func TestHandleAllowsUpdatesDuringDeletion(t *testing.T) {
	deletionTimestamp := metav1.Now()
	tests := []struct {
		name     string
		resource string
		object   runtime.Object
	}{
		{
			name:     "PodCliqueSet",
			resource: "podcliquesets",
			object: &grovecorev1beta1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "deleting", Namespace: testNamespace, DeletionTimestamp: &deletionTimestamp},
				Spec:       grovecorev1beta1.PodCliqueSetSpec{Replicas: -1},
			},
		},
		{
			name:     "PodClique",
			resource: "podcliques",
			object: &grovecorev1beta1.PodClique{
				ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: testNamespace, DeletionTimestamp: &deletionTimestamp},
				Spec:       grovecorev1beta1.PodCliqueSpec{Replicas: -1},
			},
		},
		{
			name:     "PodCliqueScalingGroup",
			resource: "podcliquescalinggroups",
			object: &grovecorev1beta1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: testNamespace, DeletionTimestamp: &deletionTimestamp},
				Spec:       grovecorev1beta1.PodCliqueScalingGroupSpec{Replicas: -1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := newTestHandler(t).Handle(t.Context(), mainRequest(t, tt.resource, admissionv1.Update, tt.object))

			assert.True(t, resp.Allowed, responseMessage(resp))
			assert.Equal(t, int32(http.StatusOK), resp.Result.Code)
		})
	}
}

func TestHandleRejectsMalformedAndUnsupportedRequests(t *testing.T) {
	pcs, pclq := standaloneFixture("workload", "worker")
	h := newTestHandler(t, pcs, pclq)

	tests := []struct {
		name string
		req  admission.Request
	}{
		{
			name: "unsupported group",
			req: admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				Resource:  metav1.GroupVersionResource{Group: "other.io", Version: "v1", Resource: "podcliques"},
			}},
		},
		{name: "PodCliqueSet resource", req: mainRequest(t, "podcliquesets", admissionv1.Create, pcs)},
		{
			name: "unsupported status subresource",
			req: func() admission.Request {
				r := mainRequest(t, "podcliques", admissionv1.Update, pclq)
				r.SubResource = "status"
				return r
			}(),
		},
		{
			name: "CREATE scale",
			req: func() admission.Request {
				r := scaleRequest(t, "podcliques", pclq.Name, 2)
				r.Operation = admissionv1.Create
				return r
			}(),
		},
		{
			name: "malformed object",
			req: admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Operation:   admissionv1.Update,
				Resource:    groveResource("podcliques"),
				SubResource: "scale",
				Object:      runtime.RawExtension{Raw: []byte("not JSON")},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := h.Handle(t.Context(), tt.req)
			assert.False(t, resp.Allowed)
			assert.Equal(t, int32(http.StatusBadRequest), resp.Result.Code, responseMessage(resp))
		})
	}
}

func TestHandleFailsClosedWhenRelatedObjectsCannotBeLoaded(t *testing.T) {
	t.Run("missing parent label is invalid input", func(t *testing.T) {
		pclq := &grovecorev1beta1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: testNamespace},
			Spec:       grovecorev1beta1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)},
		}
		resp := newTestHandler(t, pclq).Handle(t.Context(), scaleRequest(t, "podcliques", pclq.Name, 2))

		assert.False(t, resp.Allowed)
		assert.Equal(t, int32(http.StatusForbidden), resp.Result.Code)
		assert.Contains(t, responseMessage(resp), apicommon.LabelPartOfKey)
	})

	t.Run("unexpected parent lookup error is internal", func(t *testing.T) {
		pcs, pclq := standaloneFixture("workload", "worker")
		cl := testutils.NewTestClientBuilder().
			WithObjects(pcs, pclq).
			RecordErrorForObjects(
				testutils.ClientMethodGet,
				apierrors.NewInternalError(errors.New("test lookup error")),
				client.ObjectKeyFromObject(pcs),
			).
			Build()
		mgr := &testutils.FakeManager{Client: cl, Scheme: cl.Scheme(), Logger: logr.Discard()}
		resp := NewHandler(mgr).Handle(t.Context(), scaleRequest(t, "podcliques", pclq.Name, 2))

		assert.False(t, resp.Allowed)
		assert.Equal(t, int32(http.StatusInternalServerError), resp.Result.Code)
		assert.Contains(t, responseMessage(resp), "test lookup error")
	})

	t.Run("missing scale target is internal", func(t *testing.T) {
		resp := newTestHandler(t).Handle(t.Context(), scaleRequest(t, "podcliques", "missing", 2))

		assert.False(t, resp.Allowed)
		assert.Equal(t, int32(http.StatusInternalServerError), resp.Result.Code)
		assert.Contains(t, responseMessage(resp), "get PodClique")
	})
}

func standaloneFixture(pcsName, cliqueName string) (*grovecorev1beta1.PodCliqueSet, *grovecorev1beta1.PodClique) {
	template := &grovecorev1beta1.PodCliqueTemplateSpec{
		Name: cliqueName,
		Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)},
	}
	pcs := &grovecorev1beta1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: testNamespace, UID: types.UID("pcs-uid")},
		Spec: grovecorev1beta1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1beta1.PodCliqueSetTemplateSpec{Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{template}},
		},
	}
	pclqName := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}, cliqueName)
	pclq := &grovecorev1beta1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pclqName,
			Namespace: testNamespace,
			Labels: map[string]string{
				apicommon.LabelPartOfKey:                pcsName,
				apicommon.LabelPodCliqueSetReplicaIndex: "0",
			},
		},
		Spec: *template.Spec.DeepCopy(),
	}
	return pcs, pclq
}

func scalingGroupFixture(pcsName, configName, cliqueName string) (*grovecorev1beta1.PodCliqueSet, *grovecorev1beta1.PodCliqueScalingGroup) {
	template := &grovecorev1beta1.PodCliqueTemplateSpec{
		Name: cliqueName,
		Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)},
	}
	config := grovecorev1beta1.PodCliqueScalingGroupConfig{
		Name:         configName,
		CliqueNames:  []string{cliqueName},
		Replicas:     ptr.To[int32](1),
		MinAvailable: ptr.To[int32](1),
	}
	pcs := &grovecorev1beta1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: testNamespace, UID: types.UID("pcs-uid")},
		Spec: grovecorev1beta1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
				Cliques:                      []*grovecorev1beta1.PodCliqueTemplateSpec{template},
				PodCliqueScalingGroupConfigs: []grovecorev1beta1.PodCliqueScalingGroupConfig{config},
			},
		},
	}
	pcsgName := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}, configName)
	pcsg := &grovecorev1beta1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pcsgName,
			Namespace: testNamespace,
			Labels: map[string]string{
				apicommon.LabelPartOfKey:                pcsName,
				apicommon.LabelPodCliqueSetReplicaIndex: "0",
			},
		},
		Spec: grovecorev1beta1.PodCliqueScalingGroupSpec{
			Replicas:     int32(1),
			MinAvailable: ptr.To[int32](1),
			CliqueNames:  []string{cliqueName},
		},
	}
	return pcs, pcsg
}

func newTestHandler(t *testing.T, objects ...client.Object) *Handler {
	t.Helper()
	cl := testutils.NewTestClientBuilder().WithObjects(objects...).Build()
	mgr := &testutils.FakeManager{Client: cl, Scheme: cl.Scheme(), Logger: logr.Discard()}
	return NewHandler(mgr)
}

func mainRequest(t *testing.T, resourceName string, operation admissionv1.Operation, obj runtime.Object) admission.Request {
	t.Helper()
	raw, err := json.Marshal(obj)
	require.NoError(t, err)
	metaObj, ok := obj.(metav1.Object)
	require.True(t, ok)
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: operation,
		Resource:  groveResource(resourceName),
		Name:      metaObj.GetName(),
		Namespace: metaObj.GetNamespace(),
		Object:    runtime.RawExtension{Raw: raw},
		OldObject: runtime.RawExtension{Raw: raw},
	}}
}

func scaleRequest(t *testing.T, resourceName, name string, replicas int32) admission.Request {
	t.Helper()
	scale := &autoscalingv1.Scale{
		TypeMeta:   metav1.TypeMeta{APIVersion: autoscalingv1.SchemeGroupVersion.String(), Kind: "Scale"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
	}
	raw, err := json.Marshal(scale)
	require.NoError(t, err)
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation:   admissionv1.Update,
		Resource:    groveResource(resourceName),
		SubResource: "scale",
		Name:        name,
		Namespace:   testNamespace,
		Object:      runtime.RawExtension{Raw: raw},
		OldObject:   runtime.RawExtension{Raw: raw},
	}}
}

func groveResource(resourceName string) metav1.GroupVersionResource {
	return metav1.GroupVersionResource{
		Group:    grovecorev1beta1.SchemeGroupVersion.Group,
		Version:  grovecorev1beta1.SchemeGroupVersion.Version,
		Resource: resourceName,
	}
}

func responseMessage(resp admission.Response) string {
	if resp.Result == nil {
		return ""
	}
	return resp.Result.Message
}

func TestHandlePCSScaleKeepsSparseReplicaNames(t *testing.T) {
	for _, source := range []string{"standalone", "grouped", "service", "scale-out reservation", "scale-in reservation"} {
		t.Run(source, func(t *testing.T) {
			pcs, pclq := standaloneFixture("p", strings.Repeat("c", 55))
			pcs.Status.ObservedGeneration = ptr.To[int64](1)
			pcs.Spec.Template.Cliques[0].Spec.ScaleConfig = &grovecorev1beta1.AutoScalingConfig{MaxReplicas: 1000}
			labels := apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)
			labels[apicommon.LabelPodCliqueSetReplicaIndex] = "10"
			owner := *metav1.NewControllerRef(pcs, grovecorev1beta1.SchemeGroupVersion.WithKind("PodCliqueSet"))
			objects := []client.Object{pcs}
			switch source {
			case "standalone":
				pclq.Name = apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 10}, pcs.Spec.Template.Cliques[0].Name)
				labels[apicommon.LabelComponentKey] = apicommon.LabelComponentNamePodCliqueSetPodClique
				pclq.Labels = labels
				pclq.OwnerReferences = []metav1.OwnerReference{owner}
				objects = append(objects, pclq)
			case "grouped":
				groupPCS, pcsg := scalingGroupFixture("p", "g", strings.Repeat("c", 51))
				pcs.Spec = groupPCS.Spec
				pcs.Spec.Template.PodCliqueScalingGroupConfigs[0].ScaleConfig = &grovecorev1beta1.AutoScalingConfig{MaxReplicas: 1000}
				pcsg.Name = "p-10-g"
				pcsg.Labels = labels
				pcsg.OwnerReferences = []metav1.OwnerReference{owner}
				objects = append(objects, pcsg)
			case "service":
				labels[apicommon.LabelComponentKey] = apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService
				objects = append(objects, &corev1.Service{ObjectMeta: metav1.ObjectMeta{
					Name: "p-10", Namespace: pcs.Namespace, Labels: labels, OwnerReferences: []metav1.OwnerReference{owner},
				}, Spec: corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone}})
			case "scale-out reservation":
				pcs.Status.ScalingProgress = &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{10}}
			case "scale-in reservation":
				pcs.Status.ScalingProgress = &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}}
			}
			h := newTestHandler(t, objects...)
			resp := h.Handle(t.Context(), scaleRequest(t, "podcliquesets", pcs.Name, 1))
			require.False(t, resp.Allowed, "replicas=1 can retain index 10")
			assert.Contains(t, responseMessage(resp), "generated pod hostname")

			resp = h.Handle(t.Context(), scaleRequest(t, "podcliquesets", pcs.Name, 0))
			require.True(t, resp.Allowed, "scale to zero must still retire all identities: %s", responseMessage(resp))
		})
	}
}
