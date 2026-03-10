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

package resourceclaim

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// GenerateResourceClaimName produces a deterministic ResourceClaim name scoped to an instance.
// The instanceName is typically the PCLQ or PCSG fully-qualified name.
func GenerateResourceClaimName(rctName, instanceName string) string {
	return fmt.Sprintf("%s-%s", instanceName, rctName)
}

// CreateOrGetResourceClaim ensures a ResourceClaim exists for the given ResourceClaimTemplate and instance.
// It fetches the ResourceClaimTemplate, creates a ResourceClaim from its spec with an owner reference
// to the provided owner object, and returns the claim name. The operation is idempotent.
func CreateOrGetResourceClaim(
	ctx context.Context,
	logger logr.Logger,
	c client.Client,
	scheme *runtime.Scheme,
	rctName string,
	claimName string,
	namespace string,
	owner client.Object,
) (string, error) {
	// Check if the ResourceClaim already exists.
	existingClaim := &resourcev1.ResourceClaim{}
	if err := c.Get(ctx, types.NamespacedName{Name: claimName, Namespace: namespace}, existingClaim); err == nil {
		logger.V(1).Info("ResourceClaim already exists", "claimName", claimName)
		return claimName, nil
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("failed to get ResourceClaim %s: %w", claimName, err)
	}

	// Fetch the ResourceClaimTemplate.
	rct := &resourcev1.ResourceClaimTemplate{}
	if err := c.Get(ctx, types.NamespacedName{Name: rctName, Namespace: namespace}, rct); err != nil {
		return "", fmt.Errorf("failed to get ResourceClaimTemplate %s: %w", rctName, err)
	}

	// Build the ResourceClaim from the template spec.
	claim := &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        claimName,
			Namespace:   namespace,
			Labels:      rct.Spec.Labels,
			Annotations: rct.Spec.Annotations,
		},
		Spec: *rct.Spec.Spec.DeepCopy(),
	}

	if err := controllerutil.SetControllerReference(owner, claim, scheme); err != nil {
		return "", fmt.Errorf("failed to set owner reference on ResourceClaim %s: %w", claimName, err)
	}

	if err := c.Create(ctx, claim); err != nil {
		if apierrors.IsAlreadyExists(err) {
			logger.V(1).Info("ResourceClaim created concurrently", "claimName", claimName)
			return claimName, nil
		}
		return "", fmt.Errorf("failed to create ResourceClaim %s: %w", claimName, err)
	}

	logger.Info("Created ResourceClaim from template", "claimName", claimName, "templateName", rctName)
	return claimName, nil
}

// InjectResourceClaimsIntoPodSpec adds PodResourceClaim entries referencing the pre-created ResourceClaims
// and adds claim references to every container in the PodSpec. The operation is idempotent.
func InjectResourceClaimsIntoPodSpec(podSpec *corev1.PodSpec, claimNames []string) {
	if podSpec == nil || len(claimNames) == 0 {
		return
	}

	for _, claimName := range claimNames {
		alreadyExists := false
		for _, existing := range podSpec.ResourceClaims {
			if existing.Name == claimName {
				alreadyExists = true
				break
			}
		}
		if alreadyExists {
			continue
		}

		rcName := claimName
		podSpec.ResourceClaims = append(podSpec.ResourceClaims, corev1.PodResourceClaim{
			Name:              claimName,
			ResourceClaimName: &rcName,
		})

		ref := corev1.ResourceClaim{Name: claimName}
		for i := range podSpec.Containers {
			addClaimRefToContainer(&podSpec.Containers[i], ref)
		}
		for i := range podSpec.InitContainers {
			addClaimRefToContainer(&podSpec.InitContainers[i], ref)
		}
	}
}

// addClaimRefToContainer adds a ResourceClaim reference to a container if not already present.
func addClaimRefToContainer(container *corev1.Container, ref corev1.ResourceClaim) {
	for _, existing := range container.Resources.Claims {
		if existing.Name == ref.Name {
			return
		}
	}
	container.Resources.Claims = append(container.Resources.Claims, ref)
}
