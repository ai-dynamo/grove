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

package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestCreateObjectKeyForCreateWebhooks(t *testing.T) {
	const (
		testObjName         = "test-obj"
		testObjNamespace    = "test-ns"
		testObjGenerateName = "test-gen-obj"
	)
	testCases := []struct {
		description     string
		obj             client.Object
		admissionReq    admission.Request
		expectObjectKey client.ObjectKey
	}{
		{
			description:     "object with name and without namespace",
			obj:             &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: testObjName}},
			admissionReq:    admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Namespace: testObjNamespace}},
			expectObjectKey: client.ObjectKey{Name: testObjName, Namespace: testObjNamespace},
		},
		{
			description:     "object with generateName and without namespace",
			obj:             &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: testObjGenerateName}},
			admissionReq:    admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Namespace: testObjNamespace}},
			expectObjectKey: client.ObjectKey{Name: testObjGenerateName, Namespace: testObjNamespace},
		},
		{
			description:     "object with name and namespace",
			obj:             &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: testObjName, Namespace: testObjNamespace}},
			admissionReq:    admission.Request{},
			expectObjectKey: client.ObjectKey{Name: testObjName, Namespace: testObjNamespace},
		},
		{
			description:     "object with generateName and namespace",
			obj:             &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: testObjGenerateName, Namespace: testObjNamespace}},
			admissionReq:    admission.Request{},
			expectObjectKey: client.ObjectKey{Name: testObjGenerateName, Namespace: testObjNamespace},
		},
		{
			description:     "non-namespaced object with name",
			obj:             &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testObjName}},
			admissionReq:    admission.Request{},
			expectObjectKey: client.ObjectKey{Namespace: "", Name: testObjName},
		},
		{
			description:     "non-namespaced object with generateName",
			obj:             &corev1.Node{ObjectMeta: metav1.ObjectMeta{GenerateName: testObjGenerateName}},
			admissionReq:    admission.Request{},
			expectObjectKey: client.ObjectKey{Namespace: "", Name: testObjGenerateName},
		},
		{
			description:     "object with empty name and generateName falls back to empty",
			obj:             &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "", GenerateName: ""}},
			admissionReq:    admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Namespace: testObjNamespace}},
			expectObjectKey: client.ObjectKey{Name: "", Namespace: testObjNamespace},
		},
		{
			description:     "object with nil metadata uses admission request namespace",
			obj:             &corev1.Pod{ObjectMeta: metav1.ObjectMeta{}},
			admissionReq:    admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Namespace: testObjNamespace}},
			expectObjectKey: client.ObjectKey{Name: "", Namespace: testObjNamespace},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			actualObjectKey := CreateObjectKeyForCreateWebhooks(tc.obj, tc.admissionReq)
			assert.Equal(t, tc.expectObjectKey, actualObjectKey)
		})
	}
}
