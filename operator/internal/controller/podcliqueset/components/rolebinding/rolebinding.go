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

package rolebinding

import (
	"context"
	"fmt"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errGetRoleBinding    grovecorev1alpha1.ErrorCode = "ERR_GET_ROLEBINDING"
	errSyncRoleBinding   grovecorev1alpha1.ErrorCode = "ERR_SYNC_ROLEBINDING"
	errDeleteRoleBinding grovecorev1alpha1.ErrorCode = "ERR_DELETE_ROLEBINDING"
)

type _resource struct {
	client client.Client
	scheme *runtime.Scheme
}

// New creates an instance of RoleBinding components operator.
func New(client client.Client, scheme *runtime.Scheme) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client: client,
		scheme: scheme,
	}
}

// GetExistingResourceNames returns the names of all the existing resources that the RoleBinding Operator manages.
func (r _resource) GetExistingResourceNames(ctx context.Context, _ logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	roleBindingNames := make([]string, 0, 1)
	objectKey := getObjectKey(pcsObjMeta)
	objMeta := &metav1.PartialObjectMetadata{}
	objMeta.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("RoleBinding"))
	if err := r.client.Get(ctx, objectKey, objMeta); err != nil {
		if errors.IsNotFound(err) {
			return roleBindingNames, nil
		}
		return roleBindingNames, groveerr.WrapError(err,
			errGetRoleBinding,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error getting RoleBinding: %v for PodCliqueSet: %v", objectKey, k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	if metav1.IsControlledBy(objMeta, &pcsObjMeta) {
		roleBindingNames = append(roleBindingNames, objMeta.Name)
	}
	return roleBindingNames, nil
}

// Sync synchronizes all resources that the RoleBinding Operator manages.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	existingRoleBindingNames, err := r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return groveerr.WrapError(err,
			errSyncRoleBinding,
			component.OperationSync,
			fmt.Sprintf("Error getting existing RoleBinding names for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	if len(existingRoleBindingNames) > 0 {
		logger.Info("RoleBinding already exists, skipping creation", "existingRoleBinding", existingRoleBindingNames[0])
		return nil
	}
	objectKey := getObjectKey(pcs.ObjectMeta)
	roleBinding := emptyRoleBinding(objectKey)
	logger.Info("Running CreateOrUpdate RoleBinding", "objectKey", objectKey)
	if err := r.buildResource(pcs, roleBinding); err != nil {
		return groveerr.WrapError(err,
			errSyncRoleBinding,
			component.OperationSync,
			fmt.Sprintf("Error building RoleBinding: %v for PodCliqueSet: %v", objectKey, client.ObjectKeyFromObject(pcs)),
		)
	}
	if err := client.IgnoreAlreadyExists(r.client.Create(ctx, roleBinding)); err != nil {
		return groveerr.WrapError(err,
			errSyncRoleBinding,
			component.OperationSync,
			fmt.Sprintf("Error syncing RoleBinding: %v for PodCliqueSet: %v", objectKey, client.ObjectKeyFromObject(pcs)),
		)
	}
	logger.Info("Created RoleBinding", "objectKey", objectKey)
	return nil
}

// Delete removes the RoleBinding resource for the PodCliqueSet.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	objectKey := getObjectKey(pcsObjMeta)
	logger.Info("Triggering delete of RoleBinding", "objectKey", objectKey)
	if err := r.client.Delete(ctx, emptyRoleBinding(objectKey)); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RoleBinding not found, deletion is a no-op", "objectKey", objectKey)
			return nil
		}
		return groveerr.WrapError(err,
			errDeleteRoleBinding,
			component.OperationDelete,
			fmt.Sprintf("Error deleting RoleBinding: %v for PodCliqueSet: %v", objectKey, k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	logger.Info("deleted RoleBinding", "objectKey", objectKey)
	return nil
}

// buildResource configures the RoleBinding linking ServiceAccount to Role.
func (r _resource) buildResource(pcs *grovecorev1alpha1.PodCliqueSet, roleBinding *rbacv1.RoleBinding) error {
	roleBinding.Labels = getLabels(pcs.ObjectMeta)
	if err := controllerutil.SetControllerReference(pcs, roleBinding, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errSyncRoleBinding,
			component.OperationSync,
			fmt.Sprintf("Error setting controller reference for RoleBinding: %v", client.ObjectKeyFromObject(roleBinding)),
		)
	}
	roleBinding.RoleRef = rbacv1.RoleRef{
		APIGroup: rbacv1.SchemeGroupVersion.Group,
		Kind:     "Role",
		Name:     apicommon.GeneratePodRoleName(pcs.Name),
	}
	roleBinding.Subjects = []rbacv1.Subject{
		{
			APIGroup:  corev1.SchemeGroupVersion.Group,
			Kind:      "ServiceAccount",
			Name:      apicommon.GeneratePodServiceAccountName(pcs.Name),
			Namespace: pcs.Namespace,
		},
	}
	return nil
}

// getLabels constructs labels for a RoleBinding resource.
func getLabels(pcsObjMeta metav1.ObjectMeta) map[string]string {
	roleLabels := map[string]string{
		apicommon.LabelComponentKey: apicommon.LabelComponentNamePodRoleBinding,
		apicommon.LabelAppNameKey:   strings.ReplaceAll(apicommon.GeneratePodRoleBindingName(pcsObjMeta.Name), ":", "-"),
	}
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjMeta.Name),
		roleLabels,
	)
}

// getObjectKey constructs the object key for the RoleBinding resource.
func getObjectKey(pcsObjMeta metav1.ObjectMeta) client.ObjectKey {
	return client.ObjectKey{
		Name:      apicommon.GeneratePodRoleBindingName(pcsObjMeta.Name),
		Namespace: pcsObjMeta.Namespace,
	}
}

// emptyRoleBinding creates an empty RoleBinding with only metadata set.
func emptyRoleBinding(objKey client.ObjectKey) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}
