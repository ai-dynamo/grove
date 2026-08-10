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
	identity             string
	fieldPath            *field.Path
	referenceName        string
	maxGeneratedName     string
	ownerDescription     string
	replicaLimits        []int32
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

func (v *generatedResourceClaimNameValidator) validateUpdate(
	oldPCS *grovecorev1alpha1.PodCliqueSet,
	fldPath *field.Path,
) field.ErrorList {
	oldChecks := indexGeneratedResourceClaimNameChecksByIdentity(
		buildGeneratedResourceClaimNameChecks(oldPCS, fldPath),
	)
	newChecks := buildGeneratedResourceClaimNameChecks(v.pcs, fldPath)

	var allErrs field.ErrorList
	for _, check := range newChecks {
		if len(check.nameValidationErrors) == 0 {
			continue
		}
		oldCheck, exists := oldChecks[check.identity]
		if exists && len(oldCheck.nameValidationErrors) > 0 &&
			!replicaLimitsIncreased(oldCheck.replicaLimits, check.replicaLimits) {
			continue
		}
		allErrs = append(allErrs, generatedResourceClaimNameError(check))
	}
	return allErrs
}

func buildGeneratedResourceClaimNameChecks(
	pcs *grovecorev1alpha1.PodCliqueSet,
	fldPath *field.Path,
) []generatedResourceClaimNameCheck {
	var checks []generatedResourceClaimNameCheck
	pcsReplicas := pcs.Spec.Replicas
	maxPCSReplicaIndex := maxReplicaIndex(pcsReplicas)

	for i := range pcs.Spec.Template.ResourceSharing {
		ref := &pcs.Spec.Template.ResourceSharing[i].ResourceSharingSpec
		checks = appendGeneratedResourceClaimNameCheck(
			checks,
			fmt.Sprintf("pcs/%s/%s", ref.Name, ref.Scope),
			fldPath.Child("resourceSharing").Index(i).Child("name"),
			pcs.Name,
			ref,
			"PodCliqueSet",
			nil,
			pcsReplicas,
		)
	}

	pcsgConfigIndexesByCliqueName := make(map[string][]int)
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		cfg := &pcs.Spec.Template.PodCliqueScalingGroupConfigs[i]
		pcsgName := apicommon.GeneratePodCliqueScalingGroupName(
			apicommon.ResourceNameReplica{Name: pcs.Name, Replica: maxPCSReplicaIndex},
			cfg.Name,
		)
		maxPCSGReplicas := maxPCSGConfiguredReplicas(cfg)
		for j := range cfg.ResourceSharing {
			ref := &cfg.ResourceSharing[j].ResourceSharingSpec
			checks = appendGeneratedResourceClaimNameCheck(
				checks,
				fmt.Sprintf("pcsg/%s/%s/%s", cfg.Name, ref.Name, ref.Scope),
				fldPath.Child("podCliqueScalingGroups").Index(i).Child("resourceSharing").Index(j).Child("name"),
				pcsgName,
				ref,
				fmt.Sprintf("PodCliqueScalingGroup %q", cfg.Name),
				[]int32{pcsReplicas},
				maxPCSGReplicas,
			)
		}
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
			for j := range clique.ResourceSharing {
				ref := &clique.ResourceSharing[j]
				checks = appendGeneratedResourceClaimNameCheck(
					checks,
					fmt.Sprintf("pclq/%s/standalone/%s/%s", clique.Name, ref.Name, ref.Scope),
					resourceSharingPath.Index(j).Child("name"),
					pclqName,
					ref,
					fmt.Sprintf("standalone PodClique %q", clique.Name),
					[]int32{pcsReplicas},
					maxPCLQReplicas,
				)
			}
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
			for j := range clique.ResourceSharing {
				ref := &clique.ResourceSharing[j]
				checks = appendGeneratedResourceClaimNameCheck(
					checks,
					fmt.Sprintf("pclq/%s/pcsg/%s/%s/%s", clique.Name, cfg.Name, ref.Name, ref.Scope),
					resourceSharingPath.Index(j).Child("name"),
					pclqName,
					ref,
					fmt.Sprintf("PodClique %q in PodCliqueScalingGroup %q", clique.Name, cfg.Name),
					[]int32{pcsReplicas, maxPCSGReplicas},
					maxPCLQReplicas,
				)
			}
		}
	}

	return checks
}

func appendGeneratedResourceClaimNameCheck(
	checks []generatedResourceClaimNameCheck,
	identity string,
	fldPath *field.Path,
	ownerName string,
	ref *grovecorev1alpha1.ResourceSharingSpec,
	ownerDescription string,
	parentReplicaLimits []int32,
	replicaLimit int32,
) []generatedResourceClaimNameCheck {
	if ref.Name == "" {
		return checks
	}

	var generatedName string
	replicaLimits := append([]int32(nil), parentReplicaLimits...)
	switch ref.Scope {
	case grovecorev1alpha1.ResourceSharingScopeAllReplicas:
		generatedName = resourceclaim.AllReplicasRCName(ownerName, ref.Name)
	case grovecorev1alpha1.ResourceSharingScopePerReplica:
		generatedName = resourceclaim.PerReplicaRCName(ownerName, maxReplicaIndex(replicaLimit), ref.Name)
		replicaLimits = append(replicaLimits, replicaLimit)
	default:
		return checks
	}

	return append(checks, generatedResourceClaimNameCheck{
		identity:             identity,
		fieldPath:            fldPath,
		referenceName:        ref.Name,
		maxGeneratedName:     generatedName,
		ownerDescription:     ownerDescription,
		replicaLimits:        replicaLimits,
		nameValidationErrors: k8svalidation.IsDNS1123Label(generatedName),
	})
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

func indexGeneratedResourceClaimNameChecksByIdentity(
	checks []generatedResourceClaimNameCheck,
) map[string]generatedResourceClaimNameCheck {
	result := make(map[string]generatedResourceClaimNameCheck, len(checks))
	for _, check := range checks {
		result[check.identity] = check
	}
	return result
}

func replicaLimitsIncreased(oldLimits, newLimits []int32) bool {
	if len(oldLimits) != len(newLimits) {
		return true
	}
	for i := range newLimits {
		if newLimits[i] > oldLimits[i] {
			return true
		}
	}
	return false
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
