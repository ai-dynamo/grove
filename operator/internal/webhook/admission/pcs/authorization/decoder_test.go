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

package authorization

import (
	"encoding/json"
	"testing"

	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestDecode(t *testing.T) {
	tests := []struct {
		name            string
		gvks            []schema.GroupVersionKind
		operation       admissionv1.Operation
		managedResource bool
	}{
		{
			name: "managed resources must decode and return non nil PartialObjectMetadata",
			gvks: []schema.GroupVersionKind{
				pcsgGVK, pclqGVK, podGVK, serviceAccountGVK, serviceGVK, secretGVK, roleGVK, roleBindingGVK, hpaV2GVK, hpaV1GVK, podgangGVK,
			},
			operation:       admissionv1.Create,
			managedResource: true,
		},
		{
			name: "managed resources must decode and return non nil PartialObjectMetadata",
			gvks: []schema.GroupVersionKind{
				pcsgGVK, pclqGVK, podGVK, secretGVK, roleGVK, roleBindingGVK, serviceGVK, serviceAccountGVK, hpaV1GVK, hpaV2GVK, podgangGVK,
			},
			operation:       admissionv1.Update,
			managedResource: true,
		},
		{
			name: "managed resources must decode and return non nil PartialObjectMetadata",
			gvks: []schema.GroupVersionKind{
				pcsgGVK, pclqGVK, podGVK, secretGVK, roleGVK, roleBindingGVK, serviceGVK, serviceAccountGVK, hpaV1GVK, hpaV2GVK, podgangGVK,
			},
			operation:       admissionv1.Delete,
			managedResource: true,
		},
		{
			name:            "unmanaged resources must return nil",
			gvks:            []schema.GroupVersionKind{corev1.SchemeGroupVersion.WithKind("Deployment")},
			operation:       admissionv1.Create,
			managedResource: false,
		},
		{
			name:            "unmanaged resources must return nil",
			gvks:            []schema.GroupVersionKind{corev1.SchemeGroupVersion.WithKind("Deployment")},
			operation:       admissionv1.Update,
			managedResource: false,
		},
		{
			name:            "unmanaged resources must return nil",
			gvks:            []schema.GroupVersionKind{corev1.SchemeGroupVersion.WithKind("Deployment")},
			operation:       admissionv1.Delete,
			managedResource: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := testutils.NewTestClientBuilder().Build()
			mgr := createFakeManager(cl)
			d := newRequestDecoder(mgr)
			for _, gvk := range tt.gvks {
				u := createUnstructuredObject(gvk, "test-object-namespace", "test-object-name")
				bytes, err := json.Marshal(u)
				assert.NoError(t, err)
				expectedPartialObjectMetadata := &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      u.GetName(),
						Namespace: u.GetNamespace(),
					},
				}
				req := admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						Operation: tt.operation,
						Kind:      metav1.GroupVersionKind(gvk),
					},
				}
				if tt.managedResource {
					switch tt.operation {
					case admissionv1.Create:
						req.Object.Raw = bytes
					case admissionv1.Update, admissionv1.Delete:
						req.OldObject.Raw = bytes
					case admissionv1.Connect:
						expectedPartialObjectMetadata = nil
					}
				} else {
					expectedPartialObjectMetadata = nil
				}

				resp, err := d.decode(logr.Discard(), req)
				assert.NoError(t, err)
				assert.NoError(t, err)
				assert.NoError(t, err)
				assert.Equal(t, expectedPartialObjectMetadata, resp)
			}
		})
	}
}

func TestDecodeAsPartialObjectMetadata(t *testing.T) {
	tests := []struct {
		name      string
		reqKind   metav1.GroupVersionKind
		operation admissionv1.Operation
		object    client.Object
		oldObject client.Object
	}{
		{
			name:      "create must use Object",
			reqKind:   metav1.GroupVersionKind(roleBindingGVK),
			operation: admissionv1.Create,
			oldObject: nil,
			object:    createUnstructuredObject(roleBindingGVK, "test-role-namespace", "test-role-name"),
		},
		{
			name:      "update must use OldObject",
			reqKind:   metav1.GroupVersionKind(pclqGVK),
			operation: admissionv1.Update,
			oldObject: createUnstructuredObject(pclqGVK, "test-pclq-namespace", "test-pclq-name"),
			object:    createUnstructuredObject(pclqGVK, "test-pclq-namespace", "test-pclq-name"),
		},
		{
			name:      "delete must use OldObject",
			reqKind:   metav1.GroupVersionKind(pcsgGVK),
			operation: admissionv1.Delete,
			oldObject: createUnstructuredObject(pcsgGVK, "test-pcsg-namespace", "test-pcsg-name"),
			object:    nil,
		},
		{
			name:      "connect must return nil",
			reqKind:   metav1.GroupVersionKind(podGVK),
			operation: admissionv1.Connect,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := testutils.NewTestClientBuilder().Build()
			mgr := createFakeManager(cl)
			decoder := newRequestDecoder(mgr)

			bytesOldObject, err := json.Marshal(tt.oldObject)
			assert.NoError(t, err)
			bytesObject, err := json.Marshal(tt.object)
			assert.NoError(t, err)
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Kind:      tt.reqKind,
					Operation: tt.operation,
					OldObject: runtime.RawExtension{
						Raw: bytesOldObject,
					},
					Object: runtime.RawExtension{
						Raw: bytesObject,
					},
				},
			}

			var expectedPartialObjectMetadata *metav1.PartialObjectMetadata
			switch tt.operation {
			case admissionv1.Create:
				expectedPartialObjectMetadata = &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tt.object.GetName(),
						Namespace: tt.object.GetNamespace(),
					},
				}
			case admissionv1.Update, admissionv1.Delete:
				expectedPartialObjectMetadata = &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tt.oldObject.GetName(),
						Namespace: tt.oldObject.GetNamespace(),
					},
				}
			case admissionv1.Connect:
				expectedPartialObjectMetadata = nil
			}

			respPartialObjectMetadata, err := decoder.decodeAsPartialObjectMetadata(req)

			assert.NoError(t, err)
			assert.Equal(t, expectedPartialObjectMetadata, respPartialObjectMetadata)
		})
	}
}

func createUnstructuredObject(gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}
