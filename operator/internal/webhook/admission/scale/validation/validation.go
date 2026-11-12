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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/samber/lo"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
)

// ValidatePodCliqueSetReplicas validates every name whose shape depends on a
// PodCliqueSet's configured replica counts. Scalar bounds are schema validated.
// maxPCSReplicaIndex is the highest observed PCS replica index, or -1 if none exists.
func ValidatePodCliqueSetReplicas(pcs *grovecorev1beta1.PodCliqueSet, pcsgs map[int][]grovecorev1beta1.PodCliqueScalingGroup, maxPCSReplicaIndex int) field.ErrorList {
	// Scaling to zero retires every live identity. Keep validating configured
	// names at index zero, without making retirement depend on the old indexes.
	if pcs.Spec.Replicas == 0 {
		pcsgs = nil
	}
	maximumIndex := -1
	if pcs.Spec.Replicas > 0 {
		maximumIndex = maximumReplicaIndex(pcs, maxPCSReplicaIndex)
	}
	templatePath := field.NewPath("spec").Child("template")
	allErrs := validateResourceClaimReferenceNamesAtIndex(
		pcs.Name,
		maximumIndex,
		resourceclaim.ResourceSharersFromPCS(pcs.Spec.Template.ResourceSharing),
		templatePath.Child("resourceSharing"),
	)
	allErrs = append(allErrs, apivalidation.ValidateNonnegativeField(int64(pcs.Spec.Replicas), field.NewPath("spec").Child("replicas"))...)
	if pcs.Spec.Replicas < 0 {
		return allErrs
	}

	pcsNameReplica := apicommon.ResourceNameReplica{
		Name:    pcs.Name,
		Replica: max(0, maximumIndex),
	}
	if err := validateGeneratedPodGangName(apicommon.GenerateAnchorPodGangName(pcsNameReplica, podGangValidationEpoch)); err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("replicas"), pcs.Spec.Replicas, err.Error()))
	}
	templatesByName := make(map[string]*grovecorev1beta1.PodCliqueTemplateSpec, len(pcs.Spec.Template.Cliques))
	templateIndexes := make(map[string]int, len(pcs.Spec.Template.Cliques))
	for i, template := range pcs.Spec.Template.Cliques {
		if template == nil {
			continue
		}
		templatesByName[template.Name] = template
		templateIndexes[template.Name] = i
	}

	groupedCliques := sets.New[string]()

	for i, config := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		groupedCliques.Insert(config.CliqueNames...)

		// Check configured names at the highest live, reserved, or newly allocated index.
		allErrs = append(allErrs, validateConfiguredScalingGroup(
			pcsNameReplica,
			config,
			nil,
			i,
			templatesByName,
			templateIndexes,
			templatePath,
		)...)

		for replica, replicaPCSGs := range pcsgs {
			if replica < 0 {
				continue
			}
			pcsNameReplica := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replica}

			pcsg, ok := lo.Find(
				replicaPCSGs,
				func(pcsg grovecorev1beta1.PodCliqueScalingGroup) bool {
					return pcsg.Name == apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, config.Name)
				},
			)
			if !ok {
				continue
			}

			allErrs = append(allErrs, validateConfiguredScalingGroup(
				pcsNameReplica,
				config,
				&pcsg,
				i,
				templatesByName,
				templateIndexes,
				templatePath,
			)...)
		}
	}

	for i, template := range pcs.Spec.Template.Cliques {
		if template == nil || groupedCliques.Has(template.Name) {
			continue
		}
		pclqName := apicommon.GeneratePodCliqueName(pcsNameReplica, template.Name)
		allErrs = append(allErrs, validateConfiguredPodClique(
			pclqName,
			template,
			templatePath.Child("cliques").Index(i),
		)...)
	}
	return allErrs
}

// ValidatePodCliqueReplicas validates generated names
// affected by the concrete replica count of a PodClique.
func ValidatePodCliqueReplicas(pclq *grovecorev1beta1.PodClique, template *grovecorev1beta1.PodCliqueTemplateSpec) field.ErrorList {
	var allErrs field.ErrorList

	if err := validateGeneratedPodNames(pclq.Name, pclq.Spec.Replicas); err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("replicas"), pclq.Spec.Replicas, err.Error()))
	}
	if template != nil {
		allErrs = append(allErrs, validateResourceClaimReferenceNames(
			pclq.Name,
			pclq.Spec.Replicas,
			resourceclaim.ResourceSharersFromPCLQ(template.ResourceSharing),
			field.NewPath("resourceSharing"),
		)...)
	}
	return allErrs
}

// ValidatePodCliqueScalingGroupReplicas validates generated descendant names
// affected by the concrete replica count of a PCSG.
func ValidatePodCliqueScalingGroupReplicas(
	pcsg *grovecorev1beta1.PodCliqueScalingGroup,
	pcs *grovecorev1beta1.PodCliqueSet,
	config *grovecorev1beta1.PodCliqueScalingGroupConfig,
	pcsReplicaIndex int,
	oldReplicas int32,
) field.ErrorList {
	var allErrs field.ErrorList

	allErrs = append(allErrs, validateResourceClaimReferenceNames(
		pcsg.Name,
		pcsg.Spec.Replicas,
		resourceclaim.ResourceSharersFromPCSG(config.ResourceSharing),
		field.NewPath("resourceSharing"),
	)...)

	if pcsg.Spec.Replicas <= 0 {
		return allErrs
	}

	pcsNameReplica := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	// Scale-out can place new indexes in a non-anchor, even when restoring minAvailable after zero.
	if pcsg.Spec.Replicas > ptr.Deref(config.MinAvailable, 1) || pcsg.Spec.Replicas > oldReplicas {
		podGangName := apicommon.GenerateNonAnchorPodGangName(pcsNameReplica, podGangValidationEpoch, config.Name, pcsg.Spec.Replicas-1)
		if err := validateGeneratedPodGangName(podGangName); err != nil {
			allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("replicas"), pcsg.Spec.Replicas, err.Error()))
		}
	}

	pcsgNameReplica := apicommon.ResourceNameReplica{
		Name:    pcsg.Name,
		Replica: int(pcsg.Spec.Replicas - 1),
	}
	for i, cliqueName := range pcsg.Spec.CliqueNames {
		template := componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName)
		if template == nil {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec").Child("cliqueNames").Index(i),
				cliqueName,
				fmt.Sprintf("no PodClique template %q exists in parent PodCliqueSet %q", cliqueName, pcs.Name),
			))
			continue
		}

		pclqName := apicommon.GeneratePodCliqueName(pcsgNameReplica, cliqueName)
		if err := validateGeneratedPodNames(pclqName, template.Spec.Replicas); err != nil {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec").Child("replicas"),
				template.Spec.Replicas,
				fmt.Sprintf("generated PodClique %q for member %q is invalid: %v", pclqName, cliqueName, err),
			))
		}
		allErrs = append(allErrs, validateResourceClaimReferenceNames(
			pclqName,
			template.Spec.Replicas,
			resourceclaim.ResourceSharersFromPCLQ(template.ResourceSharing),
			field.NewPath("resourceSharing"),
		)...)
	}
	return allErrs
}

func validateConfiguredScalingGroup(
	pcsNameReplica apicommon.ResourceNameReplica,
	config grovecorev1beta1.PodCliqueScalingGroupConfig,
	pcsg *grovecorev1beta1.PodCliqueScalingGroup,
	configIndex int,
	templatesByName map[string]*grovecorev1beta1.PodCliqueTemplateSpec,
	templateIndexes map[string]int,
	templatePath *field.Path,
) field.ErrorList {
	configPath := templatePath.Child("podCliqueScalingGroups").Index(configIndex)
	pcsgName := apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, config.Name)
	liveReplicas := ptr.Deref(config.Replicas, 1)
	if pcsg != nil {
		liveReplicas = max(liveReplicas, pcsg.Spec.Replicas)
	}
	pcsgReplicas := configuredReplicas(liveReplicas, config.ScaleConfig)
	if pcsgReplicas <= 0 {
		return nil
	}

	allErrs := validateResourceClaimReferenceNames(
		pcsgName,
		pcsgReplicas,
		resourceclaim.ResourceSharersFromPCSG(config.ResourceSharing),
		configPath.Child("resourceSharing"),
	)
	// New groups place minAvailable replicas in the anchor. An HPA can also scale an existing
	// group out from zero, which can use a non-anchor even when its maximum is minAvailable.
	needsNonAnchor := pcsgReplicas > ptr.Deref(config.MinAvailable, 1)
	if pcsg != nil && config.ScaleConfig != nil && config.ScaleConfig.MaxReplicas > pcsg.Spec.Replicas {
		needsNonAnchor = true
	}
	if needsNonAnchor {
		podGangName := apicommon.GenerateNonAnchorPodGangName(pcsNameReplica, podGangValidationEpoch, config.Name, pcsgReplicas-1)
		if err := validateGeneratedPodGangName(podGangName); err != nil {
			allErrs = append(allErrs, field.Invalid(configPath.Child("replicas"), pcsgReplicas, err.Error()))
		}
	}

	pcsgNameReplica := apicommon.ResourceNameReplica{
		Name:    pcsgName,
		Replica: int(pcsgReplicas - 1),
	}
	for i, cliqueName := range config.CliqueNames {
		template := templatesByName[cliqueName]
		if template == nil {
			continue
		}
		pclqName := apicommon.GeneratePodCliqueName(pcsgNameReplica, cliqueName)
		cliquePath := templatePath.Child("cliques").Index(templateIndexes[cliqueName])
		cliqueReplicas := configuredReplicas(template.Spec.Replicas, template.Spec.ScaleConfig)
		if err := validateGeneratedPodNames(pclqName, cliqueReplicas); err != nil {
			allErrs = append(allErrs, field.Invalid(
				configPath.Child("cliqueNames").Index(i),
				cliqueName,
				fmt.Sprintf("generated PodClique %q is invalid: %v", pclqName, err),
			))
		}
		allErrs = append(allErrs, validateResourceClaimReferenceNames(
			pclqName,
			cliqueReplicas,
			resourceclaim.ResourceSharersFromPCLQ(template.ResourceSharing),
			cliquePath.Child("resourceSharing"),
		)...)
	}
	return allErrs
}

func validateConfiguredPodClique(
	pclqName string,
	template *grovecorev1beta1.PodCliqueTemplateSpec,
	cliquePath *field.Path,
) field.ErrorList {
	replicas := configuredReplicas(template.Spec.Replicas, template.Spec.ScaleConfig)
	var allErrs field.ErrorList
	if err := validateGeneratedPodNames(pclqName, replicas); err != nil {
		allErrs = append(allErrs, field.Invalid(
			cliquePath.Child("replicas"),
			replicas,
			fmt.Sprintf("generated PodClique %q is invalid: %v", pclqName, err),
		))
	}
	allErrs = append(allErrs, validateResourceClaimReferenceNames(
		pclqName,
		replicas,
		resourceclaim.ResourceSharersFromPCLQ(template.ResourceSharing),
		cliquePath.Child("resourceSharing"),
	)...)
	return allErrs
}

func validateResourceClaimReferenceNames(
	ownerName string,
	replicas int32,
	refs []resourceclaim.ResourceSharer,
	refsPath *field.Path,
) field.ErrorList {
	return validateResourceClaimReferenceNamesAtIndex(ownerName, int(replicas)-1, refs, refsPath)
}

func validateResourceClaimReferenceNamesAtIndex(
	ownerName string,
	replicaIndex int,
	refs []resourceclaim.ResourceSharer,
	refsPath *field.Path,
) field.ErrorList {
	var allErrs field.ErrorList
	for i := range refs {
		ref := refs[i].GetBase()
		if err := validateResourceClaimReferenceNameAtIndex(ownerName, replicaIndex, ref); err != nil {
			allErrs = append(allErrs, field.Invalid(refsPath.Index(i).Child("name"), ref.Name, err.Error()))
		}
	}
	return allErrs
}
