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
	"fmt"
	"net/http"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"
	groveutils "github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Handler validates replica-count changes made through the scale subresources
// of PodCliqueSets, PodCliques, and PodCliqueScalingGroups.
type Handler struct {
	logger  logr.Logger
	client  client.Client
	decoder admission.Decoder
}

// NewHandler creates a replica validation webhook handler.
func NewHandler(mgr manager.Manager) *Handler {
	return &Handler{
		logger:  mgr.GetLogger().WithName("webhook").WithName(Name),
		client:  mgr.GetClient(),
		decoder: admission.NewDecoder(mgr.GetScheme()),
	}
}

// Handle validates supported scale-subresource requests.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	h.logger.V(1).Info("Scale validation webhook invoked",
		"name", req.Name,
		"namespace", req.Namespace,
		"operation", req.Operation,
		"resource", req.Resource.Resource,
		"subresource", req.SubResource,
		"user", req.UserInfo.Username,
	)

	if req.Resource.Group != grovecorev1alpha1.SchemeGroupVersion.Group ||
		req.Resource.Version != grovecorev1alpha1.SchemeGroupVersion.Version {
		return badRequest(req, "unsupported API group or version")
	}

	if req.SubResource != "" && req.SubResource != "scale" {
		return badRequest(req, fmt.Sprintf("unsupported subresource %q", req.SubResource))
	}

	if req.Operation != admissionv1.Update {
		return badRequest(req, fmt.Sprintf("unsupported operation %q", req.Operation))
	}

	var (
		objectKey = client.ObjectKey{Name: req.Name, Namespace: req.Namespace}
		scale     *autoscalingv1.Scale
	)

	if req.SubResource == "scale" {
		scale = &autoscalingv1.Scale{}
		if err := h.decoder.Decode(req, scale); err != nil {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("decode Scale: %w", err))
		}
	}

	switch req.Resource.Resource {
	case "podcliquesets":
		pcs, err := getObject[grovecorev1alpha1.PodCliqueSet](ctx, req, h.client, h.decoder)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, fmt.Errorf("get PodCliqueSet %v: %w", objectKey, err))
		}
		if pcs.DeletionTimestamp != nil {
			return admission.Allowed("PodCliqueSet is being deleted")
		}
		if scale != nil {
			pcs.Spec.Replicas = scale.Spec.Replicas
		}

		allErrs, err := h.validatePodCliqueSet(ctx, pcs)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}

		return responseForValidation("PodCliqueSet replica change is valid", allErrs)
	case "podcliques":
		pclq, err := getObject[grovecorev1alpha1.PodClique](ctx, req, h.client, h.decoder)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, fmt.Errorf("get PodClique %v: %w", objectKey, err))
		}
		if pclq.DeletionTimestamp != nil {
			return admission.Allowed("PodClique is being deleted")
		}
		if scale != nil {
			pclq.Spec.Replicas = scale.Spec.Replicas
		}

		allErrs, err := h.validatePodClique(ctx, pclq)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}

		return responseForValidation("PodClique replica change is valid", allErrs)
	case "podcliquescalinggroups":
		pcsg, err := getObject[grovecorev1alpha1.PodCliqueScalingGroup](ctx, req, h.client, h.decoder)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, fmt.Errorf("get PodCliqueScalingGroup %v: %w", objectKey, err))
		}
		if pcsg.DeletionTimestamp != nil {
			return admission.Allowed("PodCliqueScalingGroup is being deleted")
		}
		if scale != nil {
			pcsg.Spec.Replicas = scale.Spec.Replicas
		}

		allErrs, err := h.validatePodCliqueScalingGroup(ctx, pcsg)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}

		return responseForValidation("PodCliqueScalingGroup replica change is valid", allErrs)
	default:
		return badRequest(req, "unsupported resource")
	}
}

func responseForValidation(message string, allErrs field.ErrorList) admission.Response {
	if len(allErrs) == 0 {
		return admission.Allowed(message)
	}
	return admission.Denied(allErrs.ToAggregate().Error())
}

func badRequest(req admission.Request, message string) admission.Response {
	return admission.Errored(http.StatusBadRequest, fmt.Errorf(
		"%s: %s %s/%s",
		message,
		req.Operation,
		req.Resource.Resource,
		req.SubResource,
	))
}

func (h *Handler) validatePodCliqueSet(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (field.ErrorList, error) {
	pcsgs, err := componentutils.GetPCSGsForPCS(ctx, h.client, client.ObjectKeyFromObject(pcs))
	if err != nil {
		return nil, fmt.Errorf("get PodCliqueScalingGroups for %s/%s: %w", pcs.Namespace, pcs.Name, err)
	}
	return ValidatePodCliqueSetReplicas(pcs, pcsgs), nil
}

func (h *Handler) validatePodClique(ctx context.Context, pclq *grovecorev1alpha1.PodClique) (field.ErrorList, error) {
	pcs, allErrs, err := h.getParentPodCliqueSet(ctx, pclq.ObjectMeta)
	if err != nil || pcs == nil {
		return allErrs, err
	}

	cliqueName, err := groveutils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("metadata"), pclq.ObjectMeta, err.Error()))
		return allErrs, nil
	}
	template := componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName)
	if template == nil {
		return allErrs, nil
	}

	return append(allErrs, ValidatePodCliqueReplicas(pclq, template)...), nil
}

func (h *Handler) validatePodCliqueScalingGroup(ctx context.Context, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) (field.ErrorList, error) {
	pcs, allErrs, err := h.getParentPodCliqueSet(ctx, pcsg.ObjectMeta)
	if err != nil || pcs == nil {
		return allErrs, err
	}

	pcsReplicaIndex, err := k8sutils.GetPodCliqueSetReplicaIndex(pcsg.ObjectMeta)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("metadata").Child("labels"), pcsg.Labels, err.Error()))
		return allErrs, nil
	}
	config := resourceclaim.FindPCSGConfig(pcs, pcsg, pcsReplicaIndex)
	if config == nil {
		return allErrs, nil
	}

	return append(allErrs, ValidatePodCliqueScalingGroupReplicas(pcsg, pcs, config)...), nil
}

func (h *Handler) getParentPodCliqueSet(ctx context.Context, objectMeta metav1.ObjectMeta) (*grovecorev1alpha1.PodCliqueSet, field.ErrorList, error) {
	labels := objectMeta.GetLabels()
	pcs, err := componentutils.GetPodCliqueSet(ctx, h.client, objectMeta)
	if apierrors.IsNotFound(err) {
		return nil, field.ErrorList{field.Invalid(
			field.NewPath("metadata").Child("labels").Key(apicommon.LabelPartOfKey),
			labels[apicommon.LabelPartOfKey],
			"referenced parent PodCliqueSet does not exist",
		)}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get parent PodCliqueSet %s/%s: %w", objectMeta.Namespace, labels[apicommon.LabelPartOfKey], err)
	}
	return pcs, nil, nil
}

type handlerObject[T any] interface {
	*T
	client.Object
}

// getObject fetches the object from the cluster for /scale requests or decodes the incoming request body.
func getObject[T any, PT handlerObject[T]](ctx context.Context, req admission.Request, cl client.Client, decoder admission.Decoder) (PT, error) {
	var (
		obj = PT(new(T))
		err error
	)

	if req.SubResource == "scale" {
		err = cl.Get(ctx, client.ObjectKey{Name: req.Name, Namespace: req.Namespace}, obj)
	} else {
		err = decoder.Decode(req, obj)
	}

	return obj, err
}
