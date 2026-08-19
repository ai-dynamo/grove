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
	"fmt"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
)

// configuredReplicas returns the largest replica count declared by the initial
// spec and its optional HPA configuration. External scale requests are
// validated against their requested count instead.
func configuredReplicas(initial int32, cfg *grovecorev1alpha1.AutoScalingConfig) int32 {
	if cfg == nil || cfg.MaxReplicas <= initial {
		return initial
	}
	return cfg.MaxReplicas
}

// validateGeneratedPodNames validates the Pod hostname and metadata name that
// Grove generates for the last requested PodClique replica. A zero replica
// count creates no Pods and therefore has no generated names to validate.
func validateGeneratedPodNames(pclqName string, replicas int32) error {
	if replicas <= 0 {
		return nil
	}

	podIndex := int(replicas - 1)
	podHostname := apicommon.GeneratePodHostname(pclqName, podIndex)
	if validationErrs := k8svalidation.IsDNS1123Label(podHostname); len(validationErrs) > 0 {
		return fmt.Errorf("generated pod hostname %q is invalid: %s", podHostname, strings.Join(validationErrs, "; "))
	}

	podName := apicommon.GeneratePodNameWithSuffix(
		pclqName,
		podIndex,
		strings.Repeat("x", apicommon.PodNameRandomSuffixLength),
	)
	if validationErrs := k8svalidation.IsDNS1123Subdomain(podName); len(validationErrs) > 0 {
		return fmt.Errorf("generated pod name %q is invalid: %s", podName, strings.Join(validationErrs, "; "))
	}

	return nil
}

// validateResourceClaimReferenceName validates the ResourceClaim reference name
// generated from one resource-sharing entry. ResourceSharingScope's enum is
// schema validated, so an unknown scope is ignored here.
func validateResourceClaimReferenceName(ownerName string, replicas int32, ref *grovecorev1alpha1.ResourceSharingSpec) error {
	var rcName string
	switch ref.Scope {
	case grovecorev1alpha1.ResourceSharingScopeAllReplicas:
		rcName = resourceclaim.RCName(ownerName, ref, nil)
	case grovecorev1alpha1.ResourceSharingScopePerReplica:
		if replicas <= 0 {
			return nil
		}
		rcName = resourceclaim.RCName(ownerName, ref, ptr.To(int(replicas-1)))
	default:
		return nil
	}

	if validationErrs := k8svalidation.IsDNS1123Label(rcName); len(validationErrs) > 0 {
		return fmt.Errorf(
			"generated pod resource claim reference name %q is invalid: %s. Pod resource claim names must fit DNS_LABEL because they are written to pod.spec.resourceClaims[].name and container resource claim references; shorten the resourceSharing name or generated owner name, or reduce replicas",
			rcName,
			strings.Join(validationErrs, "; "),
		)
	}
	return nil
}
