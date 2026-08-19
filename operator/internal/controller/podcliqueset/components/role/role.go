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

package role

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
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errCodeGetRole grovecorev1alpha1.ErrorCode = "ERR_GET_ROLE"
	errSyncRole    grovecorev1alpha1.ErrorCode = "ERR_SYNC_ROLE"
	errDeleteRole  grovecorev1alpha1.ErrorCode = "ERR_DELETE_ROLE"
)

type _resource struct {
	client client.Client
	scheme *runtime.Scheme
}

// New creates an instance of Role components operator.
func New(client client.Client, scheme *runtime.Scheme) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client: client,
		scheme: scheme,
	}
}

// GetExistingResourceNames returns the names of all the existing resources that the Role Operator manages.
func (r _resource) GetExistingResourceNames(ctx context.Context, _ logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	roleNames := make([]string, 0, 1)
	objectKey := getObjectKey(pcsObjMeta)
	partialObjMeta, err := k8sutils.GetExistingPartialObjectMetadata(ctx, r.client, rbacv1.SchemeGroupVersion.WithKind("Role"), objectKey)
	if err != nil {
		if errors.IsNotFound(err) {
			return roleNames, nil
		}
		return nil, groveerr.WrapError(err,
			errCodeGetRole,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error getting Role: %v for PodCliqueSet: %v", objectKey, k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	if metav1.IsControlledBy(partialObjMeta, &pcsObjMeta) {
		roleNames = append(roleNames, partialObjMeta.Name)
	}
	return roleNames, nil
}

// Sync synchronizes all resources that the Role Operator manages.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	existingRoleNames, err := r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeGetRole,
			component.OperationSync,
			fmt.Sprintf("Error getting existing Role names for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	if len(existingRoleNames) > 0 {
		logger.Info("Role already exists, skipping creation", "existingRole", existingRoleNames[0])
		return nil
	}
	objectKey := getObjectKey(pcs.ObjectMeta)
	role := emptyRole(objectKey)
	logger.Info("Running CreateOrUpdate Role", "objectKey", objectKey)
	if err := r.buildResource(pcs, role); err != nil {
		return groveerr.WrapError(err,
			errSyncRole,
			component.OperationSync,
			fmt.Sprintf("Error building Role: %v for PodCliqueSet: %v", objectKey, client.ObjectKeyFromObject(pcs)),
		)
	}
	if err := client.IgnoreAlreadyExists(r.client.Create(ctx, role)); err != nil {
		return groveerr.WrapError(err,
			errSyncRole,
			component.OperationSync,
			fmt.Sprintf("Error syncing Role: %v for PodCliqueSet: %v", objectKey, client.ObjectKeyFromObject(pcs)),
		)
	}
	logger.Info("Created Role", "objectKey", objectKey)
	return nil
}

// Delete removes the Role resource for the PodCliqueSet.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	objectKey := getObjectKey(pcsObjMeta)
	logger.Info("Triggering delete of Role", "objectKey", objectKey)
	if err := r.client.Delete(ctx, emptyRole(objectKey)); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Role not found, deletion is a no-op", "objectKey", objectKey)
			return nil
		}
		return groveerr.WrapError(err,
			errDeleteRole,
			component.OperationDelete,
			fmt.Sprintf("Error deleting Role: %v for PodCliqueSet: %v", objectKey, k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	logger.Info("deleted Role", "objectKey", objectKey)
	return nil
}

// buildResource configures the Role with pod access permissions.
func (r _resource) buildResource(pcs *grovecorev1alpha1.PodCliqueSet, role *rbacv1.Role) error {
	role.Labels = getLabels(pcs.ObjectMeta)
	if err := controllerutil.SetControllerReference(pcs, role, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errSyncRole,
			component.OperationSync,
			fmt.Sprintf("Error setting controller reference for role: %v", client.ObjectKeyFromObject(role)),
		)
	}
	role.Rules = []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"pods", "pods/status"},
			Verbs:     []string{"get", "list", "watch"},
		},
	}
	return nil
}

// getLabels constructs labels for a Role resource.
func getLabels(pcsObjMeta metav1.ObjectMeta) map[string]string {
	roleLabels := map[string]string{
		apicommon.LabelComponentKey: apicommon.LabelComponentNamePodRole,
		apicommon.LabelAppNameKey:   strings.ReplaceAll(apicommon.GeneratePodRoleName(pcsObjMeta.Name), ":", "-"),
	}
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjMeta.Name),
		roleLabels,
	)
}

// getObjectKey constructs the object key for the Role resource.
func getObjectKey(pcsObjMeta metav1.ObjectMeta) client.ObjectKey {
	return client.ObjectKey{
		Name:      apicommon.GeneratePodRoleName(pcsObjMeta.Name),
		Namespace: pcsObjMeta.Namespace,
	}
}

// emptyRole creates an empty Role with only metadata set.
func emptyRole(objKey client.ObjectKey) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}
