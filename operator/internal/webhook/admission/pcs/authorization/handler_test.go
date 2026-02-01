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

package authorization

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	groveconfigv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const reconcilerServiceAccountUserName = "system:serviceaccount:default:grove-operator"

var exemptServiceAccountUserNames = []string{"exempt-account", "kuberentes-admin"}

func TestHandle(t *testing.T) {
	tests := []struct {
		name           string
		pcsNeeded      bool
		pcsName        string
		pcsNamespace   string
		pcsAnnotations map[string]string
		resourceGVK    schema.GroupVersionKind
		username       string
		operation      admissionv1.Operation
		clientErr      *apierrors.StatusError
		allowed        bool
	}{
		{
			name:      "all users can CONNECT",
			operation: admissionv1.Connect,
			allowed:   true,
		},
		{
			name:         "no parent PodCliqueSet determinable, allow",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  pclqGVK,
			operation:    admissionv1.Create,
			allowed:      true,
		},
		{
			name:         "error while fetching the parent PodCliqueSet must be denied",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  pclqGVK,
			clientErr:    apierrors.NewInternalError(errors.New("internal error")),
			operation:    admissionv1.Create,
			allowed:      false,
		},
		{
			name:         "disable-managed-resource-protection annotation's presence allows requests from all users to CREATE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  pclqGVK,
			pcsAnnotations: map[string]string{
				constants.AnnotationDisableManagedResourceProtection: "true",
			},
			username:  "random-user",
			operation: admissionv1.Create,
			allowed:   true,
		},
		{
			name:         "disable-managed-resource-protection annotation's presence allows requests from all users to UPDATE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  pcsgGVK,
			pcsAnnotations: map[string]string{
				constants.AnnotationDisableManagedResourceProtection: "true",
			},
			username:  "random-user",
			operation: admissionv1.Update,
			allowed:   true,
		},
		{
			name:         "disable-managed-resource-protection annotation's presence allows requests from all users to DELETE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  roleBindingGVK,
			pcsAnnotations: map[string]string{
				constants.AnnotationDisableManagedResourceProtection: "true",
			},
			username:  "random-user",
			operation: admissionv1.Delete,
			allowed:   true,
		},
		{
			name:         "reconciler serviceaccount can CREATE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  roleGVK,
			username:     reconcilerServiceAccountUserName,
			operation:    admissionv1.Create,
			allowed:      true,
		},
		{
			name:         "exempt serviceaccounts can CREATE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  serviceAccountGVK,
			username:     exemptServiceAccountUserNames[0],
			operation:    admissionv1.Create,
			allowed:      true,
		},
		{
			name:         "other users cannot CREATE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  pclqGVK,
			username:     "platinum",
			operation:    admissionv1.Create,
			allowed:      false,
		},
		{
			name:         "reconciler serviceaccount can UPDATE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  secretGVK,
			username:     reconcilerServiceAccountUserName,
			operation:    admissionv1.Update,
			allowed:      true,
		},
		{
			name:         "exempt serviceaccounts can UPDATE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  serviceGVK,
			username:     exemptServiceAccountUserNames[0],
			operation:    admissionv1.Update,
			allowed:      true,
		},
		{
			name:         "other users cannot UPDATE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  pclqGVK,
			username:     "platinum",
			operation:    admissionv1.Update,
			allowed:      false,
		},
		{
			name:         "reconciler serviceaccount can DELETE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  serviceAccountGVK,
			username:     reconcilerServiceAccountUserName,
			operation:    admissionv1.Delete,
			allowed:      true,
		},
		{
			name:         "exempt serviceaccounts can DELETE",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  pclqGVK,
			username:     exemptServiceAccountUserNames[0],
			operation:    admissionv1.Delete,
			allowed:      true,
		},
		{
			name:         "other users cannot DELETE non-pod resources",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  pcsgGVK,
			username:     "platinum",
			operation:    admissionv1.Delete,
			allowed:      false,
		},
		{
			name:         "all users can DELETE pod resources",
			pcsNeeded:    true,
			pcsName:      "test-pcs-name",
			pcsNamespace: "test-pcs-namespace",
			resourceGVK:  podGVK,
			username:     "platinum",
			operation:    admissionv1.Delete,
			allowed:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcsObjectMetadata := &metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{
					Kind:       pcsGVK.Kind,
					APIVersion: pcsGVK.GroupVersion().String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:        tt.pcsName,
					Namespace:   tt.pcsNamespace,
					Annotations: tt.pcsAnnotations,
				},
			}

			// setup fake client, inject errors if any
			clBuilder := testutils.NewTestClientBuilder()
			if tt.pcsNeeded {
				clBuilder.WithObjects(pcsObjectMetadata)
			}
			if tt.clientErr != nil {
				clBuilder.RecordErrorForObjects(testutils.ClientMethodGet, tt.clientErr, client.ObjectKeyFromObject(pcsObjectMetadata))
			}
			cl := clBuilder.Build()
			mgr := createFakeManager(cl)

			handler := NewHandler(mgr, groveconfigv1alpha1.AuthorizerConfig{
				Enabled:                       true,
				ExemptServiceAccountUserNames: exemptServiceAccountUserNames,
			}, reconcilerServiceAccountUserName)

			// construct the request for pclq
			u := createUnstructuredObject(tt.resourceGVK, tt.pcsNamespace, "test-resource-name")
			u.SetLabels(map[string]string{
				apicommon.LabelPartOfKey: tt.pcsName,
			})
			bytes, err := json.Marshal(u)
			assert.NoError(t, err)
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					UserInfo: authenticationv1.UserInfo{
						Username: tt.username,
					},
					Operation: tt.operation,
					Kind:      metav1.GroupVersionKind(tt.resourceGVK),
				},
			}
			switch tt.operation {
			case admissionv1.Create:
				req.Object.Raw = bytes
			case admissionv1.Update, admissionv1.Delete:
				req.OldObject.Raw = bytes
			}

			resp := handler.Handle(t.Context(), req)
			assert.Equal(t, tt.allowed, resp.Allowed)
		})
	}
}

func TestHandleCreateOrUpdate(t *testing.T) {
	tests := []struct {
		name     string
		username string
		gvks     []schema.GroupVersionKind
		req      admission.Request
		allowed  bool
	}{
		{
			name: "resource creation/updation by reconciler serviceaccount is allowed",
			gvks: []schema.GroupVersionKind{
				pcsgGVK, pclqGVK, podGVK, serviceAccountGVK, serviceGVK, secretGVK, roleGVK, roleBindingGVK, hpaV2GVK, hpaV1GVK, podgangGVK,
			},
			username: reconcilerServiceAccountUserName,
			allowed:  true,
		},
		{
			name: "resource creation/updation by exempt serviceaccount is allowed",
			gvks: []schema.GroupVersionKind{
				pcsgGVK, pclqGVK, podGVK, serviceAccountGVK, serviceGVK, secretGVK, roleGVK, roleBindingGVK, hpaV2GVK, hpaV1GVK, podgangGVK,
			},
			username: exemptServiceAccountUserNames[0],
			allowed:  true,
		},
		{
			name: "resource creation/updation by other serviceaccounts is denied",
			gvks: []schema.GroupVersionKind{
				pcsgGVK, pclqGVK, podGVK, serviceAccountGVK, serviceGVK, secretGVK, roleGVK, roleBindingGVK, hpaV2GVK, hpaV1GVK, podgangGVK,
			},
			username: "boingo",
			allowed:  false,
		},
	}
	logger := logr.Discard()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := testutils.NewTestClientBuilder().Build()
			mgr := createFakeManager(cl)
			handler := NewHandler(mgr, groveconfigv1alpha1.AuthorizerConfig{
				Enabled:                       true,
				ExemptServiceAccountUserNames: exemptServiceAccountUserNames,
			}, reconcilerServiceAccountUserName)

			for _, gvk := range tt.gvks {
				req := admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						UserInfo: authenticationv1.UserInfo{
							Username: tt.username,
						},
						Kind: metav1.GroupVersionKind(gvk),
					},
				}
				resp := handler.handleCreateOrUpdate(req, logger, client.ObjectKey{
					Namespace: "test-namespace",
					Name:      "test-resource",
				})
				assert.Equal(t, tt.allowed, resp.Allowed)
			}
		})
	}
}

func TestHandleDelete(t *testing.T) {
	tests := []struct {
		name     string
		username string
		gvks     []schema.GroupVersionKind
		req      admission.Request
		allowed  bool
	}{
		{
			name:     "deletion of pods  by reconciler serviceaccount is allowed",
			username: reconcilerServiceAccountUserName,
			gvks:     []schema.GroupVersionKind{podGVK},
			allowed:  true,
		},
		{
			name:     "deletion of pods by exempt serviceaccounts is allowed",
			username: exemptServiceAccountUserNames[1],
			gvks:     []schema.GroupVersionKind{podGVK},
			allowed:  true,
		},
		{
			name:     "deletion of pods other serviceaccounts is allowed",
			username: "zeppeli",
			gvks:     []schema.GroupVersionKind{podGVK},
			allowed:  true,
		},
		{
			name:     "deletion of non-pod resources by reconciler serviceaccount is allowed",
			username: reconcilerServiceAccountUserName,
			gvks: []schema.GroupVersionKind{
				pcsgGVK, pclqGVK, serviceAccountGVK, serviceGVK, secretGVK, roleGVK, roleBindingGVK, hpaV2GVK, hpaV1GVK, podgangGVK,
			},
			allowed: true,
		},
		{
			name:     "deletion of non-pod resources by an exempt serviceaccount is allowed",
			username: exemptServiceAccountUserNames[1],
			gvks: []schema.GroupVersionKind{
				pcsgGVK, pclqGVK, serviceAccountGVK, serviceGVK, secretGVK, roleGVK, roleBindingGVK, hpaV2GVK, hpaV1GVK, podgangGVK,
			},
			allowed: true,
		},
		{
			name:     "deletion of non-pod resources by any other serviceaccounts is denied",
			username: "dio",
			gvks: []schema.GroupVersionKind{
				pcsgGVK, pclqGVK, serviceAccountGVK, serviceGVK, secretGVK, roleGVK, roleBindingGVK, hpaV2GVK, hpaV1GVK, podgangGVK,
			},
			allowed: false,
		},
	}

	logger := logr.Discard()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := testutils.NewTestClientBuilder().Build()
			mgr := createFakeManager(cl)
			handler := NewHandler(mgr, groveconfigv1alpha1.AuthorizerConfig{
				Enabled:                       true,
				ExemptServiceAccountUserNames: exemptServiceAccountUserNames,
			}, reconcilerServiceAccountUserName)

			for _, gvk := range tt.gvks {
				req := admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						UserInfo: authenticationv1.UserInfo{
							Username: tt.username,
						},
						Kind: metav1.GroupVersionKind(gvk),
					},
				}
				resp := handler.handleDelete(req, logger, client.ObjectKey{
					Namespace: "test-namespace",
					Name:      "test-resource",
				})
				assert.Equal(t, tt.allowed, resp.Allowed)
			}
		})
	}
}

func TestGetParentPCSPartialObjectMetadata(t *testing.T) {
	tests := []struct {
		name               string
		resourceObjectMeta metav1.ObjectMeta
		pcsObjectMetadata  *metav1.PartialObjectMetadata
		shouldWarn         bool
		err                *apierrors.StatusError
	}{
		{
			name: "no labels on resource, should just warn and not error",
			resourceObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "test-namespace",
				Labels:    map[string]string{},
			},
			pcsObjectMetadata: nil,
			shouldWarn:        true,
			err:               nil,
		},
		{
			name: "label present on resource, but a missing PCS should just warn and not error",
			resourceObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "test-namespace",
				Labels: map[string]string{
					"app.kubernetes.io/part-of": "test-pcs",
				},
			},
			pcsObjectMetadata: nil,
			shouldWarn:        true,
			err:               nil,
		},
		{
			name: "label present on resource, and a PCS present must return the PCS",
			resourceObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "test-namespace",
				Labels: map[string]string{
					"app.kubernetes.io/part-of": "test-pcs",
				},
			},
			pcsObjectMetadata: &metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{
					Kind:       "PodCliqueSet",
					APIVersion: "grove.io/v1alpha1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "test-namespace",
				},
			},
			shouldWarn: false,
			err:        nil,
		},
		{
			name: "label present on resource, client get call errors must return error",
			resourceObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "test-namespace",
				Labels: map[string]string{
					"app.kubernetes.io/part-of": "test-pcs",
				},
			},
			pcsObjectMetadata: &metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{
					Kind:       "PodCliqueSet",
					APIVersion: "grove.io/v1alpha1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "test-namespace",
				},
			},
			shouldWarn: false,
			err:        apierrors.NewInternalError(errors.New("internal error")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clBuilder := testutils.NewTestClientBuilder()
			if tt.pcsObjectMetadata != nil {
				clBuilder.WithObjects(tt.pcsObjectMetadata)
			}
			if tt.err != nil {
				clBuilder.RecordErrorForObjects(testutils.ClientMethodGet, tt.err, client.ObjectKeyFromObject(tt.pcsObjectMetadata))
			}
			cl := clBuilder.Build()

			mgr := createFakeManager(cl)
			handler := NewHandler(mgr, groveconfigv1alpha1.AuthorizerConfig{
				Enabled: true,
			}, reconcilerServiceAccountUserName)

			fetchedPCS, warn, err := handler.getParentPCSPartialObjectMetadata(t.Context(), tt.resourceObjectMeta)

			if tt.err != nil {
				assert.Error(t, err)
			} else {
				assert.Equal(t, tt.pcsObjectMetadata, fetchedPCS)
			}

			if tt.shouldWarn {
				assert.NotNil(t, warn)
			}
		})
	}
}

func TestToAdmissionError(t *testing.T) {
	testError := fmt.Errorf("test error")
	wrappedDecodeError := errors.Join(testError, errDecodeRequestObject)

	assert.Equal(t, int32(http.StatusInternalServerError), toAdmissionError(testError), "all other errors must respond with internal server error")
	assert.Equal(t, int32(http.StatusBadRequest), toAdmissionError(wrappedDecodeError), "decode error must respond with bad request")
}

func createFakeManager(cl client.Client) manager.Manager {
	return &testutils.FakeManager{
		Client: cl,
		Scheme: cl.Scheme(),
		Logger: logr.Discard(),
	}
}
