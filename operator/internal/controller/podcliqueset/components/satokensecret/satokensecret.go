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

package satokensecret

import (
	"context"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errCodeGetSecret              grovecorev1alpha1.ErrorCode = "ERR_GET_SECRET"
	errCodeSetControllerReference grovecorev1alpha1.ErrorCode = "ERR_SET_CONTROLLER_REFERENCE"
	errCodeCreateSecret           grovecorev1alpha1.ErrorCode = "ERR_CREATE_SECRET"
	errCodeDeleteSecret           grovecorev1alpha1.ErrorCode = "ERR_DELETE_SECRET"
)

type _resource struct {
	client client.Client
	scheme *runtime.Scheme
}

// New creates an instance of Secret components operator.
func New(client client.Client, scheme *runtime.Scheme) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client: client,
		scheme: scheme,
	}
}

// GetExistingResourceNames returns the names of existing service account token secrets.
func (r _resource) GetExistingResourceNames(ctx context.Context, _ logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	secretNames := make([]string, 0, 1)
	objKey := getObjectKey(pcsObjMeta)
	partialObjMeta, err := k8sutils.GetExistingPartialObjectMetadata(ctx, r.client, corev1.SchemeGroupVersion.WithKind("Secret"), objKey)
	if err != nil {
		if errors.IsNotFound(err) {
			return secretNames, nil
		}
		return nil, groveerr.WrapError(err,
			errCodeGetSecret,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error getting Secret: %v for PodCliqueSet: %v", objKey, k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	if metav1.IsControlledBy(partialObjMeta, &pcsObjMeta) {
		secretNames = append(secretNames, partialObjMeta.Name)
	}
	return secretNames, nil
}

// Sync creates the service account token secret if it doesn't exist.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	pcsObjKey := client.ObjectKeyFromObject(pcs)
	existingSecretNames, err := r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeGetSecret,
			component.OperationSync,
			fmt.Sprintf("Error getting existing satokensecret names for PodCliqueSet: %v", pcsObjKey),
		)
	}
	if len(existingSecretNames) > 0 {
		logger.Info("Secret already exists, skipping creation", "existingSecret", existingSecretNames[0])
		return nil
	}
	objKey := getObjectKey(pcs.ObjectMeta)
	secret := emptySecret(objKey)
	if err = r.buildResource(pcs, secret); err != nil {
		return err
	}
	if err = client.IgnoreAlreadyExists(r.client.Create(ctx, secret)); err != nil {
		return groveerr.WrapError(err,
			errCodeCreateSecret,
			component.OperationSync,
			fmt.Sprintf("Error creating satokensecret: %v for PodCliqueSet: %v", objKey, pcsObjKey),
		)
	}
	logger.Info("Created Secret", "objectKey", objKey)
	return nil
}

// Delete removes the service account token secret.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	objectKey := getObjectKey(pcsObjMeta)
	logger.Info("Triggering delete of Secret", "objectKey", objectKey)
	if err := r.client.Delete(ctx, emptySecret(objectKey)); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Secret not found", "objectKey", objectKey)
			return nil
		}
		return groveerr.WrapError(err,
			errCodeDeleteSecret,
			component.OperationDelete,
			fmt.Sprintf("Error deleting satokensecret: %v for PodCliqueSet: %v", objectKey, k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	logger.Info("Deleted Secret", "objectKey", objectKey)
	return nil
}

// buildResource configures the Secret as a ServiceAccountToken type.
func (r _resource) buildResource(pcs *grovecorev1alpha1.PodCliqueSet, secret *corev1.Secret) error {
	secret.Labels = getLabels(pcs.Name, secret.Name)
	if err := controllerutil.SetControllerReference(pcs, secret, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errCodeSetControllerReference,
			component.OperationSync,
			fmt.Sprintf("Error setting controller reference for satokensecret: %v", client.ObjectKeyFromObject(secret)),
		)
	}
	secret.Type = corev1.SecretTypeServiceAccountToken
	secret.Annotations = map[string]string{
		corev1.ServiceAccountNameKey: apicommon.GeneratePodServiceAccountName(pcs.Name),
	}
	return nil
}

// getLabels constructs labels for a ServiceAccount token Secret resource.
func getLabels(pcsName, secretName string) map[string]string {
	secretLabels := map[string]string{
		apicommon.LabelComponentKey: apicommon.LabelComponentNameServiceAccountTokenSecret,
		apicommon.LabelAppNameKey:   secretName,
	}
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		secretLabels,
	)
}

// getObjectKey constructs the object key for the ServiceAccount token Secret.
func getObjectKey(pcsObjMeta metav1.ObjectMeta) client.ObjectKey {
	return client.ObjectKey{
		Name:      apicommon.GenerateInitContainerSATokenSecretName(pcsObjMeta.Name),
		Namespace: pcsObjMeta.Namespace,
	}
}

// emptySecret creates an empty Secret with only metadata set.
func emptySecret(objKey client.ObjectKey) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}
