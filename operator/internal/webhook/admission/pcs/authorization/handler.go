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
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	groveconfigv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Handler is the PodCliqueSet authorization admission webhook handler.
type Handler struct {
	config                           groveconfigv1alpha1.AuthorizerConfig
	client                           client.Client
	mgr                              manager.Manager
	decoder                          *requestDecoder
	baseLogger                       logr.Logger
	reconcilerServiceAccountUserName string
}

// NewHandler creates a new handler for the auhtorizer webhook.
func NewHandler(mgr manager.Manager, config groveconfigv1alpha1.AuthorizerConfig, reconcilerServiceAccountUserName string) *Handler {
	return &Handler{
		config:                           config,
		mgr:                              mgr,
		client:                           mgr.GetClient(),
		decoder:                          newRequestDecoder(mgr),
		baseLogger:                       mgr.GetLogger().WithName("authorizer-webhook").WithName(Name),
		reconcilerServiceAccountUserName: reconcilerServiceAccountUserName,
	}
}

// Handle handles requests and admits them if they are authorized.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := h.baseLogger.WithValues("user", req.UserInfo.Username, "operation", req.Operation, "resource", req.Resource, "subresource", req.SubResource, "name", req.Name, "namespace", req.Namespace)
	logger.V(5).Info("Authorizer webhook invoked")

	// always allow `CONNECT` operations, irrespective of the user.
	if req.Operation == admissionv1.Connect {
		logger.Info("Connect operation requested, which is always allowed for users with sufficient RBAC. Admitting.")
		return admission.Allowed(fmt.Sprintf("operation %s is allowed", req.Operation))
	}

	// Decode and convert the request object to `metav1.PartialObjectMeta`.
	resourcePartialObjMeta, err := h.decoder.decode(logger, req)
	if err != nil {
		return admission.Errored(toAdmissionError(err), err)
	}
	resourceObjectKey := client.ObjectKeyFromObject(resourcePartialObjMeta)

	// Get the parent PodCliqueSet
	pcsPartialObjectMetadata, warnings, err := h.getParentPCSPartialObjectMetadata(ctx, resourcePartialObjMeta.ObjectMeta)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err).WithWarnings(warnings...)
	}
	// PCS can be nil if the resource has no reference of a PodCliqueSet or the referenced PodCliqueSet is not found.
	if pcsPartialObjectMetadata == nil {
		return admission.Allowed(fmt.Sprintf("admission allowed, no PodCliqueSet could be determined for resource: %v", resourceObjectKey)).WithWarnings(warnings...)
	}

	// Check if protection of PodCliqueSet managed resources has been disabled by setting the annotation to "true".
	if value, ok := pcsPartialObjectMetadata.Annotations[apiconstants.AnnotationDisableManagedResourceProtection]; ok && value == "true" {
		logger.Info("Resource has the \"grove.io/disable-managed-resource-protection\" annotation set to \"true\", authorized webhook bypassed. Admitting request.", "objectKey", resourceObjectKey)
		return admission.Allowed(fmt.Sprintf("admission allowed, resource protection is disabled for PodCliqueSet: %v", client.ObjectKeyFromObject(pcsPartialObjectMetadata)))
	}

	switch req.Operation {
	case admissionv1.Create, admissionv1.Update:
		return h.handleCreateOrUpdate(req, logger, resourceObjectKey)
	case admissionv1.Delete:
		return h.handleDelete(req, logger, resourceObjectKey)
	default:
		return admission.Denied("Unhandled operation. Admission denied.")
	}
}

// handleCreateOrUpdate allows creation/updation of managed resources only if the request is from the reconciler service account, or one of the exempted service accounts.
func (h *Handler) handleCreateOrUpdate(req admission.Request, logger logr.Logger, resourceObjectKey client.ObjectKey) admission.Response {
	if req.UserInfo.Username == h.reconcilerServiceAccountUserName {
		return admission.Allowed(fmt.Sprintf("admission allowed, creation/updation of resource: %v is initiated by the grove reconciler service account", resourceObjectKey))
	}

	if slices.Contains(h.config.ExemptServiceAccountUserNames, req.UserInfo.Username) {
		return admission.Allowed(fmt.Sprintf("admission allowed, creation/updation of resource: %v is initiated by exempt serviceaccount: %v", resourceObjectKey, req.UserInfo.Username))
	}

	logger.Info("admission denied for create/update operation", "objectKey", resourceObjectKey)
	return admission.Denied(fmt.Sprintf("admission denied, creation/updation of resource: %v is not allowed", resourceObjectKey))
}

// handleDelete allows deletion of managed resources only if the request is either from the service account used by the reconcilers
// or the request is coming from one of the exempted service accounts.
// There is an exception to this, where no restriction are placed on Pods.
func (h *Handler) handleDelete(req admission.Request, logger logr.Logger, resourceObjectKey client.ObjectKey) admission.Response {
	if groupKindFromRequest(req) == podGVK.GroupKind() {
		return admission.Allowed("admission allowed, deletion of resource: Pod is allowed for all users having sufficient RBAC")
	}

	if req.UserInfo.Username == h.reconcilerServiceAccountUserName {
		return admission.Allowed(fmt.Sprintf("admission allowed, deletion of resource: %v is initiated by the grove reconciler service account", resourceObjectKey))
	}

	if slices.Contains(h.config.ExemptServiceAccountUserNames, req.UserInfo.Username) {
		return admission.Allowed(fmt.Sprintf("admission allowed, deletion of resource: %v is initiated by exempt serviceaccount: %v", resourceObjectKey, req.UserInfo.Username))
	}

	logger.Info("admission denied for delete operation", "objectKey", resourceObjectKey)
	return admission.Denied(fmt.Sprintf("admission denied, deletion of resource: %v is not allowed", resourceObjectKey))
}

func (h *Handler) getParentPCSPartialObjectMetadata(ctx context.Context, resourceObjMeta metav1.ObjectMeta) (*metav1.PartialObjectMetadata, admission.Warnings, error) {
	resourceObjKey := k8sutils.GetObjectKeyFromObjectMeta(resourceObjMeta)
	pcsName, ok := resourceObjMeta.Labels[apicommon.LabelPartOfKey]
	if !ok {
		return nil, admission.Warnings{fmt.Sprintf("missing required label %s on resource %v, could not determine parent PodCliqueSet", apicommon.LabelPartOfKey, resourceObjKey)}, nil
	}

	pcsPartialObjectMetadata, err := k8sutils.GetExistingPartialObjectMetadata(ctx, h.client, pcsGVK, client.ObjectKey{Name: pcsName, Namespace: resourceObjMeta.Namespace})
	if apierrors.IsNotFound(err) {
		return nil, admission.Warnings{fmt.Sprintf("parent PodCliqueSet %s not found for resource: %v", pcsName, resourceObjKey)}, nil
	}
	return pcsPartialObjectMetadata, nil, err
}

func toAdmissionError(err error) int32 {
	if errors.Is(err, errDecodeRequestObject) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func groupKindFromRequest(req admission.Request) schema.GroupKind {
	return schema.GroupKind{
		Group: req.Kind.Group,
		Kind:  req.Kind.Kind,
	}
}
