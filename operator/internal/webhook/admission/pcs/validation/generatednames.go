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
	"k8s.io/apimachinery/pkg/util/validation/field"
)

type generatedResourceClaimNameCheck struct {
	fieldPath            *field.Path
	referenceName        string
	maxGeneratedName     string
	ownerDescription     string
	nameValidationErrors []string
}

type generatedResourceClaimNameValidator struct {
	pcs *grovecorev1alpha1.PodCliqueSet
}

func newGeneratedResourceClaimNameValidator(
	pcs *grovecorev1alpha1.PodCliqueSet,
) *generatedResourceClaimNameValidator {
	return &generatedResourceClaimNameValidator{pcs: pcs}
}

func (v *generatedResourceClaimNameValidator) validate(fldPath *field.Path) field.ErrorList {
	return generatedResourceClaimNameErrors(buildGeneratedResourceClaimNameChecks(v.pcs, fldPath))
}

func (v *generatedResourceClaimNameValidator) updateWarnings(fldPath *field.Path) []string {
	var warnings []string
	for _, check := range buildGeneratedResourceClaimNameChecks(v.pcs, fldPath) {
		if len(check.nameValidationErrors) == 0 {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(
			"%s generates an invalid ResourceClaim name for Pods; new Pods may fail admission",
			check.fieldPath,
		))
	}
	return warnings
}

func buildGeneratedResourceClaimNameChecks(
	pcs *grovecorev1alpha1.PodCliqueSet,
	fldPath *field.Path,
) []generatedResourceClaimNameCheck {
	var checks []generatedResourceClaimNameCheck
	pcsReplicas := pcs.Spec.Replicas
	maxPCSReplicaIndex := maxReplicaIndex(pcsReplicas)

	checks = appendGeneratedResourceClaimNameChecks(
		checks,
		resourceclaim.ResourceSharersFromPCS(pcs.Spec.Template.ResourceSharing),
		fldPath.Child("resourceSharing"),
		pcs.Name,
		"PodCliqueSet",
		pcsReplicas,
	)

	pcsgConfigIndexesByCliqueName := make(map[string][]int)
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		cfg := &pcs.Spec.Template.PodCliqueScalingGroupConfigs[i]
		pcsgName := apicommon.GeneratePodCliqueScalingGroupName(
			apicommon.ResourceNameReplica{Name: pcs.Name, Replica: maxPCSReplicaIndex},
			cfg.Name,
		)
		maxPCSGReplicas := maxPCSGConfiguredReplicas(cfg)
		checks = appendGeneratedResourceClaimNameChecks(
			checks,
			resourceclaim.ResourceSharersFromPCSG(cfg.ResourceSharing),
			fldPath.Child("podCliqueScalingGroups").Index(i).Child("resourceSharing"),
			pcsgName,
			fmt.Sprintf("PodCliqueScalingGroup %q", cfg.Name),
			maxPCSGReplicas,
		)
		for _, cliqueName := range cfg.CliqueNames {
			pcsgConfigIndexesByCliqueName[cliqueName] = append(pcsgConfigIndexesByCliqueName[cliqueName], i)
		}
	}

	for i, clique := range pcs.Spec.Template.Cliques {
		if clique == nil {
			continue
		}
		resourceSharingPath := fldPath.Child("cliques").Index(i).Child("resourceSharing")
		maxPCLQReplicas := maxConfiguredReplicas(&clique.Spec.Replicas, clique.Spec.ScaleConfig)
		pcsgConfigIndexes := pcsgConfigIndexesByCliqueName[clique.Name]
		if len(pcsgConfigIndexes) == 0 {
			pclqName := apicommon.GeneratePodCliqueName(
				apicommon.ResourceNameReplica{Name: pcs.Name, Replica: maxPCSReplicaIndex},
				clique.Name,
			)
			checks = appendGeneratedResourceClaimNameChecks(
				checks,
				resourceclaim.ResourceSharersFromPCLQ(clique.ResourceSharing),
				resourceSharingPath,
				pclqName,
				fmt.Sprintf("standalone PodClique %q", clique.Name),
				maxPCLQReplicas,
			)
			continue
		}

		for _, pcsgConfigIndex := range pcsgConfigIndexes {
			cfg := &pcs.Spec.Template.PodCliqueScalingGroupConfigs[pcsgConfigIndex]
			maxPCSGReplicas := maxPCSGConfiguredReplicas(cfg)
			pcsgName := apicommon.GeneratePodCliqueScalingGroupName(
				apicommon.ResourceNameReplica{Name: pcs.Name, Replica: maxPCSReplicaIndex},
				cfg.Name,
			)
			pclqName := apicommon.GeneratePodCliqueName(
				apicommon.ResourceNameReplica{
					Name:    pcsgName,
					Replica: maxReplicaIndex(maxPCSGReplicas),
				},
				clique.Name,
			)
			checks = appendGeneratedResourceClaimNameChecks(
				checks,
				resourceclaim.ResourceSharersFromPCLQ(clique.ResourceSharing),
				resourceSharingPath,
				pclqName,
				fmt.Sprintf("PodClique %q in PodCliqueScalingGroup %q", clique.Name, cfg.Name),
				maxPCLQReplicas,
			)
		}
	}

	return checks
}

func appendGeneratedResourceClaimNameChecks(
	checks []generatedResourceClaimNameCheck,
	sharers []resourceclaim.ResourceSharer,
	fldPath *field.Path,
	ownerName string,
	ownerDescription string,
	replicaLimit int32,
) []generatedResourceClaimNameCheck {
	for i, sharer := range sharers {
		ref := sharer.GetBase()
		if ref.Name == "" {
			continue
		}

		var generatedName string
		switch ref.Scope {
		case grovecorev1alpha1.ResourceSharingScopeAllReplicas:
			generatedName = resourceclaim.AllReplicasRCName(ownerName, ref.Name)
		case grovecorev1alpha1.ResourceSharingScopePerReplica:
			generatedName = resourceclaim.PerReplicaRCName(ownerName, maxReplicaIndex(replicaLimit), ref.Name)
		default:
			continue
		}

		checks = append(checks, generatedResourceClaimNameCheck{
			fieldPath:            fldPath.Index(i).Child("name"),
			referenceName:        ref.Name,
			maxGeneratedName:     generatedName,
			ownerDescription:     ownerDescription,
			nameValidationErrors: k8svalidation.IsDNS1123Label(generatedName),
		})
	}
	return checks
}

func generatedResourceClaimNameErrors(checks []generatedResourceClaimNameCheck) field.ErrorList {
	var allErrs field.ErrorList
	for _, check := range checks {
		if len(check.nameValidationErrors) > 0 {
			allErrs = append(allErrs, generatedResourceClaimNameError(check))
		}
	}
	return allErrs
}

func generatedResourceClaimNameError(check generatedResourceClaimNameCheck) *field.Error {
	return field.Invalid(
		check.fieldPath,
		check.referenceName,
		fmt.Sprintf(
			"generated ResourceClaim name %q for %s must be a valid DNS label because it is used as pod.spec.resourceClaims[].name: %s",
			check.maxGeneratedName,
			check.ownerDescription,
			strings.Join(check.nameValidationErrors, "; "),
		),
	)
}

func maxPCSGConfiguredReplicas(cfg *grovecorev1alpha1.PodCliqueScalingGroupConfig) int32 {
	replicas := int32(1)
	if cfg.Replicas != nil {
		replicas = *cfg.Replicas
	}
	return maxConfiguredReplicas(&replicas, cfg.ScaleConfig)
}

// maxConfiguredReplicas returns the largest replica count visible from the PCS template.
// It does not include direct child scale subresource updates, including those made by external autoscalers.
func maxConfiguredReplicas(replicas *int32, scaleConfig *grovecorev1alpha1.AutoScalingConfig) int32 {
	var maxReplicas int32
	if replicas != nil {
		maxReplicas = *replicas
	}
	if scaleConfig != nil && scaleConfig.MaxReplicas > maxReplicas {
		maxReplicas = scaleConfig.MaxReplicas
	}
	return maxReplicas
}

func maxReplicaIndex(replicas int32) int {
	if replicas <= 1 {
		return 0
	}
	return int(replicas - 1)
}
