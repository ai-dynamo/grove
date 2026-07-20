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

package serviceaccount

import (
	"context"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errGetServiceAccount    v1alpha1.ErrorCode = "ERR_GET_SERVICEACCOUNT"
	errSyncServiceAccount   v1alpha1.ErrorCode = "ERR_SYNC_SERVICEACCOUNT"
	errDeleteServiceAccount v1alpha1.ErrorCode = "ERR_DELETE_SERVICEACCOUNT"
)

type _resource struct {
	client client.Client
	scheme *runtime.Scheme
}

// New creates an instance of ServiceAccount components operator.
func New(client client.Client, scheme *runtime.Scheme) component.Operator[v1alpha1.PodCliqueSet] {
	return &_resource{
		client: client,
		scheme: scheme,
	}
}

// GetExistingResourceNames returns the names of all the existing resources that the ServiceAccount Operator manages.
func (r _resource) GetExistingResourceNames(ctx context.Context, _ logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	saNames := make([]string, 0, 1)
	objectKey := getObjectKey(pcsObjMeta)
	objMeta := &metav1.PartialObjectMetadata{}
	objMeta.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ServiceAccount"))
	if err := r.client.Get(ctx, objectKey, objMeta); err != nil {
		if errors.IsNotFound(err) {
			return saNames, nil
		}
		return saNames, groveerr.WrapError(err,
			errGetServiceAccount,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error getting ServiceAccount: %v for PodCliqueSet: %v", objectKey, k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	if metav1.IsControlledBy(objMeta, &pcsObjMeta) {
		saNames = append(saNames, objMeta.Name)
	}
	return saNames, nil
}

// Sync synchronizes all resources that the ServiceAccount Operator manages.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *v1alpha1.PodCliqueSet) error {
	objectKey := getObjectKey(pcs.ObjectMeta)
	sa := emptyServiceAccount(objectKey)

	logger.Info("Running CreateOrUpdate ServiceAccount", "objectKey", objectKey)
	opResult, err := controllerutil.CreateOrPatch(ctx, r.client, sa, func() error {
		return r.buildResource(pcs, sa)
	})
	if err != nil {
		return groveerr.WrapError(err,
			errSyncServiceAccount,
			component.OperationSync,
			fmt.Sprintf("Error syncing ServiceAccount: %v for PodCliqueSet: %v", objectKey, client.ObjectKeyFromObject(pcs)),
		)
	}
	logger.Info("Triggered create or update of ServiceAccount", "objectKey", objectKey, "result", opResult)
	return nil
}

// Delete removes the ServiceAccount resource for the PodCliqueSet.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	objectKey := getObjectKey(pcsObjMeta)
	logger.Info("Triggering delete of ServiceAccount", "objectKey", objectKey)
	if err := r.client.Delete(ctx, emptyServiceAccount(objectKey)); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("ServiceAccount not found, deletion is a no-op", "objectKey", objectKey)
			return nil
		}
		return groveerr.WrapError(err,
			errDeleteServiceAccount,
			component.OperationDelete,
			fmt.Sprintf("Error deleting ServiceAccount: %v for PodCliqueSet: %v", objectKey, k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	logger.Info("Deleted ServiceAccount", "objectKey", objectKey)
	return nil
}

// buildResource configures the ServiceAccount with automount enabled.
func (r _resource) buildResource(pcs *v1alpha1.PodCliqueSet, sa *corev1.ServiceAccount) error {
	sa.Labels = getLabels(pcs.ObjectMeta)
	if err := controllerutil.SetControllerReference(pcs, sa, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errSyncServiceAccount,
			component.OperationSync,
			fmt.Sprintf("Error setting controller reference for ServiceAccount: %v", client.ObjectKeyFromObject(sa)),
		)
	}
	sa.AutomountServiceAccountToken = ptr.To(true)
	return nil
}

// getLabels constructs labels for a ServiceAccount resource.
func getLabels(pcsObjMeta metav1.ObjectMeta) map[string]string {
	roleLabels := map[string]string{
		apicommon.LabelComponentKey: apicommon.LabelComponentNamePodServiceAccount,
		apicommon.LabelAppNameKey:   apicommon.GeneratePodServiceAccountName(pcsObjMeta.Name),
	}
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjMeta.Name),
		roleLabels,
	)
}

// getObjectKey constructs the object key for the ServiceAccount resource.
func getObjectKey(pcsObjMeta metav1.ObjectMeta) client.ObjectKey {
	return client.ObjectKey{
		Name:      apicommon.GeneratePodServiceAccountName(pcsObjMeta.Name),
		Namespace: pcsObjMeta.Namespace,
	}
}

// emptyServiceAccount creates an empty ServiceAccount with only metadata set.
func emptyServiceAccount(objKey client.ObjectKey) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}
