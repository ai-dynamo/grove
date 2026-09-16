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
	"context"
	"encoding/json"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kueue"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	testNamespace = "default"
	testPCLQName  = "demo-0-worker"
)

// newPodClique returns a PodClique whose schedulerName resolves to the given backend.
func newPodClique(schedulerName string, replicas int32) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		TypeMeta: metav1.TypeMeta{
			APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
			Kind:       "PodClique",
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testPCLQName},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas: replicas,
			PodSpec:  corev1.PodSpec{SchedulerName: schedulerName},
		},
	}
}

func newScale(replicas int32) *autoscalingv1.Scale {
	return &autoscalingv1.Scale{
		TypeMeta:   metav1.TypeMeta{APIVersion: "autoscaling/v1", Kind: "Scale"},
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testPCLQName},
		Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
	}
}

func rawOf(t *testing.T, obj any) runtime.RawExtension {
	t.Helper()
	data, err := json.Marshal(obj)
	require.NoError(t, err)
	return runtime.RawExtension{Raw: data}
}

// newHandler builds a Handler backed by a registry where the kueue backend restricts PodClique scaling
// and the default (kube) backend does not implement scheduler.PodCliqueScaleValidator at all.
func newHandler(t *testing.T, existing ...client.Object) *Handler {
	t.Helper()
	cl := testutils.CreateDefaultFakeClient(existing)
	kueueBackend := kueue.New(cl, cl.Scheme(), record.NewFakeRecorder(10),
		configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
	require.NoError(t, kueueBackend.Init(nil))
	return &Handler{
		client:  cl,
		decoder: admission.NewDecoder(cl.Scheme()),
		schedRegistry: &testutils.FakeSchedulerRegistry{
			Backends: map[string]scheduler.Backend{
				string(configv1alpha1.SchedulerNameKube):  testutils.NewFakeSchedulerBackend(string(configv1alpha1.SchedulerNameKube)),
				string(configv1alpha1.SchedulerNameKueue): kueueBackend,
			},
			DefaultBackend: string(configv1alpha1.SchedulerNameKube),
		},
	}
}

func TestHandle_PodCliqueResource(t *testing.T) {
	testCases := []struct {
		description   string
		schedulerName string
		oldReplicas   int32
		newReplicas   int32
		wantAllowed   bool
	}{
		{
			description:   "kueue backend allows an update that does not change replicas",
			schedulerName: string(configv1alpha1.SchedulerNameKueue),
			oldReplicas:   4,
			newReplicas:   4,
			wantAllowed:   true,
		},
		{
			description:   "kueue backend denies a scale out",
			schedulerName: string(configv1alpha1.SchedulerNameKueue),
			oldReplicas:   4,
			newReplicas:   7,
		},
		{
			description:   "kueue backend denies a scale in",
			schedulerName: string(configv1alpha1.SchedulerNameKueue),
			oldReplicas:   4,
			newReplicas:   1,
		},
		{
			description:   "backend that does not restrict scaling allows a scale out",
			schedulerName: string(configv1alpha1.SchedulerNameKube),
			oldReplicas:   4,
			newReplicas:   7,
			wantAllowed:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			h := newHandler(t)
			resp := h.Handle(context.Background(), admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Update,
					Namespace: testNamespace,
					Name:      testPCLQName,
					OldObject: rawOf(t, newPodClique(tc.schedulerName, tc.oldReplicas)),
					Object:    rawOf(t, newPodClique(tc.schedulerName, tc.newReplicas)),
				},
			})
			assert.Equal(t, tc.wantAllowed, resp.Allowed)
			if !tc.wantAllowed {
				assert.Contains(t, resp.Result.Message, "does not support scaling a PodClique")
			}
		})
	}
}

// TestHandle_ScaleSubResource covers the path kubectl scale and the HorizontalPodAutoscaler take. The
// admitted objects are autoscaling/v1 Scale, so the PodClique is read from the cluster to resolve the
// backend while the replica counts come from the request.
func TestHandle_ScaleSubResource(t *testing.T) {
	testCases := []struct {
		description   string
		schedulerName string
		oldReplicas   int32
		newReplicas   int32
		wantAllowed   bool
	}{
		{
			description:   "kueue backend denies a scale through the scale subresource",
			schedulerName: string(configv1alpha1.SchedulerNameKueue),
			oldReplicas:   4,
			newReplicas:   7,
		},
		{
			description:   "kueue backend allows an unchanged replica count",
			schedulerName: string(configv1alpha1.SchedulerNameKueue),
			oldReplicas:   4,
			newReplicas:   4,
			wantAllowed:   true,
		},
		{
			description:   "backend that does not restrict scaling allows a scale",
			schedulerName: string(configv1alpha1.SchedulerNameKube),
			oldReplicas:   4,
			newReplicas:   7,
			wantAllowed:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			h := newHandler(t, newPodClique(tc.schedulerName, tc.oldReplicas))
			resp := h.Handle(context.Background(), admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation:   admissionv1.Update,
					SubResource: scaleSubResource,
					Namespace:   testNamespace,
					Name:        testPCLQName,
					OldObject:   rawOf(t, newScale(tc.oldReplicas)),
					Object:      rawOf(t, newScale(tc.newReplicas)),
				},
			})
			assert.Equal(t, tc.wantAllowed, resp.Allowed)
			if !tc.wantAllowed {
				assert.Contains(t, resp.Result.Message, "does not support scaling a PodClique")
			}
		})
	}
}

func TestHandle_NonUpdateOperationIsAllowed(t *testing.T) {
	h := newHandler(t)
	for _, op := range []admissionv1.Operation{admissionv1.Create, admissionv1.Delete, admissionv1.Connect} {
		resp := h.Handle(context.Background(), admission.Request{
			AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: op,
				Namespace: testNamespace,
				Name:      testPCLQName,
			},
		})
		assert.True(t, resp.Allowed, "operation %s must be allowed", op)
	}
}

// TestHandle_ScaleSubResourceMissingPodClique asserts the request is rejected rather than silently
// admitted when the PodClique needed to resolve the backend cannot be read.
func TestHandle_ScaleSubResourceMissingPodClique(t *testing.T) {
	h := newHandler(t)
	resp := h.Handle(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation:   admissionv1.Update,
			SubResource: scaleSubResource,
			Namespace:   testNamespace,
			Name:        testPCLQName,
			OldObject:   rawOf(t, newScale(4)),
			Object:      rawOf(t, newScale(7)),
		},
	})
	assert.False(t, resp.Allowed)
	assert.Contains(t, resp.Result.Message, "failed to get PodClique")
}
