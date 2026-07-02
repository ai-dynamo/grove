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
	errCodeListPods               grovecorev1alpha1.ErrorCode = "ERR_LIST_PODS"
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
	secretNames := make([]string, 0, 2)
	for _, objKey := range getObjectKeys(pcsObjMeta) {
		partialObjMeta, err := k8sutils.GetExistingPartialObjectMetadata(ctx, r.client, corev1.SchemeGroupVersion.WithKind("Secret"), objKey)
		if err != nil {
			if errors.IsNotFound(err) {
				continue
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
	}
	return secretNames, nil
}

// Sync creates the service account token secret if it doesn't exist.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	pcsObjKey := client.ObjectKeyFromObject(pcs)
	objKey := getObjectKey(pcs.ObjectMeta)
	existingSecret, err := r.getSecret(ctx, objKey)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeGetSecret,
			component.OperationSync,
			fmt.Sprintf("Error getting satokensecret: %v for PodCliqueSet: %v", objKey, pcsObjKey),
		)
	}
	if existingSecret != nil {
		if !metav1.IsControlledBy(existingSecret, pcs) {
			return groveerr.New(
				errCodeGetSecret,
				component.OperationSync,
				fmt.Sprintf("Secret %v already exists but is not controlled by PodCliqueSet %v", objKey, pcsObjKey),
			)
		}
		if !hasServiceAccountToken(existingSecret) {
			return newWaitingForTokenError(objKey)
		}
		if err = r.cleanupLegacySecret(ctx, logger, pcs); err != nil {
			return err
		}
		logger.Info("Secret already exists, skipping creation", "existingSecret", client.ObjectKeyFromObject(existingSecret))
		return nil
	}

	secret := emptySecret(objKey)
	if err = r.buildResource(pcs, secret); err != nil {
		return err
	}
	if err = r.client.Create(ctx, secret); err != nil {
		return groveerr.WrapError(err,
			errCodeCreateSecret,
			component.OperationSync,
			fmt.Sprintf("Error creating satokensecret: %v for PodCliqueSet: %v", objKey, pcsObjKey),
		)
	}
	logger.Info("Created Secret", "objectKey", objKey)
	return newWaitingForTokenError(objKey)
}

// Delete removes the service account token secret.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	for _, objectKey := range getObjectKeys(pcsObjMeta) {
		logger.Info("Triggering delete of Secret", "objectKey", objectKey)
		if err := r.client.Delete(ctx, emptySecret(objectKey)); err != nil {
			if errors.IsNotFound(err) {
				logger.Info("Secret not found", "objectKey", objectKey)
				continue
			}
			return groveerr.WrapError(err,
				errCodeDeleteSecret,
				component.OperationDelete,
				fmt.Sprintf("Error deleting satokensecret: %v for PodCliqueSet: %v", objectKey, k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
			)
		}
		logger.Info("Deleted Secret", "objectKey", objectKey)
	}
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

func hasServiceAccountToken(secret *corev1.Secret) bool {
	return len(secret.Data[corev1.ServiceAccountTokenKey]) > 0
}

func newWaitingForTokenError(objKey client.ObjectKey) error {
	return groveerr.New(
		groveerr.ErrCodeRequeueAfter,
		component.OperationSync,
		fmt.Sprintf("Secret %v is waiting for service account token", objKey),
	)
}

func (r _resource) getSecret(ctx context.Context, objKey client.ObjectKey) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	if err := r.client.Get(ctx, objKey, secret); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secret, nil
}

func (r _resource) cleanupLegacySecret(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	pcsObjKey := client.ObjectKeyFromObject(pcs)
	legacyObjKey := getLegacyObjectKey(pcs.ObjectMeta)
	legacySecret, err := r.getSecret(ctx, legacyObjKey)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeGetSecret,
			component.OperationSync,
			fmt.Sprintf("Error getting legacy satokensecret for PodCliqueSet: %v", pcsObjKey),
		)
	}
	if legacySecret == nil || !metav1.IsControlledBy(legacySecret, pcs) {
		return nil
	}
	referencingPods, err := r.getPodsReferencingSecret(ctx, pcs, legacyObjKey.Name)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeListPods,
			component.OperationSync,
			fmt.Sprintf("Error listing Pods referencing legacy satokensecret: %v for PodCliqueSet: %v", legacyObjKey, pcsObjKey),
		)
	}
	if len(referencingPods) > 0 {
		logger.Info("Skipping legacy Secret cleanup because Pods still reference it", "objectKey", legacyObjKey, "pods", referencingPods)
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("Secret %v is still referenced by Pods: %v", legacyObjKey, referencingPods),
		)
	}
	if err = client.IgnoreNotFound(r.client.Delete(ctx, legacySecret)); err != nil {
		return groveerr.WrapError(err,
			errCodeDeleteSecret,
			component.OperationSync,
			fmt.Sprintf("Error deleting legacy satokensecret: %v for PodCliqueSet: %v", legacyObjKey, pcsObjKey),
		)
	}
	logger.Info("Deleted legacy Secret after replacement Secret acquired token", "objectKey", legacyObjKey)
	return nil
}

func (r _resource) getPodsReferencingSecret(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, secretName string) ([]string, error) {
	podList := &corev1.PodList{}
	if err := r.client.List(ctx,
		podList,
		client.InNamespace(pcs.Namespace),
		client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)),
	); err != nil {
		return nil, err
	}

	podNames := make([]string, 0, len(podList.Items))
	for _, pod := range podList.Items {
		if podReferencesSecret(pod, secretName) {
			podNames = append(podNames, client.ObjectKeyFromObject(&pod).String())
		}
	}
	return podNames, nil
}

func podReferencesSecret(pod corev1.Pod, secretName string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.Secret != nil && volume.Secret.SecretName == secretName {
			return true
		}
	}
	return false
}

// getObjectKey constructs the object key for the ServiceAccount token Secret.
func getObjectKey(pcsObjMeta metav1.ObjectMeta) client.ObjectKey {
	return client.ObjectKey{
		Name:      apicommon.GenerateInitContainerSATokenSecretName(pcsObjMeta.Name),
		Namespace: pcsObjMeta.Namespace,
	}
}

func getLegacyObjectKey(pcsObjMeta metav1.ObjectMeta) client.ObjectKey {
	return client.ObjectKey{
		Name:      apicommon.GenerateLegacyInitContainerSATokenSecretName(pcsObjMeta.Name),
		Namespace: pcsObjMeta.Namespace,
	}
}

func getObjectKeys(pcsObjMeta metav1.ObjectMeta) []client.ObjectKey {
	return []client.ObjectKey{
		getObjectKey(pcsObjMeta),
		getLegacyObjectKey(pcsObjMeta),
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
