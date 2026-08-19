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
	"errors"
	"fmt"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/scale/scheme/autoscalingv1"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	pcsGVK            = grovecorev1alpha1.SchemeGroupVersion.WithKind(constants.KindPodCliqueSet)
	pcsgGVK           = grovecorev1alpha1.SchemeGroupVersion.WithKind(constants.KindPodCliqueScalingGroup)
	pclqGVK           = grovecorev1alpha1.SchemeGroupVersion.WithKind(constants.KindPodClique)
	podGVK            = corev1.SchemeGroupVersion.WithKind("Pod")
	secretGVK         = corev1.SchemeGroupVersion.WithKind("Secret")
	roleGVK           = rbacv1.SchemeGroupVersion.WithKind("Role")
	roleBindingGVK    = rbacv1.SchemeGroupVersion.WithKind("RoleBinding")
	serviceGVK        = corev1.SchemeGroupVersion.WithKind("Service")
	serviceAccountGVK = corev1.SchemeGroupVersion.WithKind("ServiceAccount")
	hpaV2GVK          = autoscalingv2.SchemeGroupVersion.WithKind("HorizontalPodAutoscaler")
	hpaV1GVK          = autoscalingv1.SchemeGroupVersion.WithKind("HorizontalPodAutoscaler")
	podgangGVK        = groveschedulerv1alpha1.SchemeGroupVersion.WithKind("PodGang")

	errDecodeRequestObject  = errors.New("failed to decode request")
	errUnsupportedOperation = errors.New("unsupported operation")
)

// requestDecoder decodes the admission requests.
// It optimizes only gets PartialObjectMetadata for the resource
// as there is no need to get the full resource.
type requestDecoder struct {
	decoder admission.Decoder
}

func newRequestDecoder(mgr manager.Manager) *requestDecoder {
	return &requestDecoder{
		decoder: admission.NewDecoder(mgr.GetScheme()),
	}
}

func (d *requestDecoder) decode(logger logr.Logger, req admission.Request) (*metav1.PartialObjectMetadata, error) {
	reqGVK := schema.GroupVersionKind{
		Group:   req.Kind.Group,
		Version: req.Kind.Version,
		Kind:    req.Kind.Kind,
	}
	switch reqGVK {
	case pcsgGVK, pclqGVK, podGVK, serviceAccountGVK, serviceGVK, secretGVK, roleGVK, roleBindingGVK,
		hpaV2GVK, hpaV1GVK,
		podgangGVK:
		return d.decodeAsPartialObjectMetadata(req)
	default:
		logger.Info("Skipping decoding, unknown GVK", "GVK", reqGVK)
		return nil, nil
	}
}

func (d *requestDecoder) decodeAsPartialObjectMetadata(req admission.Request) (partialObjMeta *metav1.PartialObjectMetadata, err error) {
	var obj *unstructured.Unstructured
	switch req.Operation {
	case admissionv1.Connect:
		return
	case admissionv1.Create:
		obj, err = d.asUnstructured(req.Object)
		if err != nil {
			return
		}
	case admissionv1.Update:
		// OldObject is used since labels are used to check if a resource is managed by grove.
		// If these labels are changed in an update, it might cause the authorizer webhook to not work as intended.
		obj, err = d.asUnstructured(req.OldObject)
		if err != nil {
			return
		}
	case admissionv1.Delete:
		// OldObject contains the object being deleted
		//https://github.com/kubernetes/kubernetes/pull/76346
		obj, err = d.asUnstructured(req.OldObject)
		if err != nil {
			return
		}
	default:
		err = fmt.Errorf("%w: operation %s", errUnsupportedOperation, req.Operation)
		return
	}
	return meta.AsPartialObjectMetadata(obj), nil
}

func (d *requestDecoder) asUnstructured(rawObj runtime.RawExtension) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	if err := d.decoder.DecodeRaw(rawObj, obj); err != nil {
		return nil, fmt.Errorf("%w: %w", errDecodeRequestObject, err)
	}
	return obj, nil
}
