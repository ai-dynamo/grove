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
	"maps"
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
)

// ValidatePodCliqueSetReplicas validates every name whose shape depends on a
// PodCliqueSet's configured replica counts. Scalar bounds are schema validated.
func ValidatePodCliqueSetReplicas(pcs *grovecorev1alpha1.PodCliqueSet, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) field.ErrorList {
	templatePath := field.NewPath("spec").Child("template")
	allErrs := validateResourceClaimReferenceNames(
		pcs.Name,
		pcs.Spec.Replicas,
		resourceclaim.ResourceSharersFromPCS(pcs.Spec.Template.ResourceSharing),
		templatePath.Child("resourceSharing"),
	)
	allErrs = append(allErrs, apivalidation.ValidateNonnegativeField(int64(pcs.Spec.Replicas), field.NewPath("spec").Child("replicas"))...)
	if pcs.Spec.Replicas < 0 {
		return allErrs
	}

	pcsNameReplica := apicommon.ResourceNameReplica{
		Name:    pcs.Name,
		Replica: max(0, int(pcs.Spec.Replicas-1)),
	}
	templatesByName := make(map[string]*grovecorev1alpha1.PodCliqueTemplateSpec, len(pcs.Spec.Template.Cliques))
	templateIndexes := make(map[string]int, len(pcs.Spec.Template.Cliques))
	for i, template := range pcs.Spec.Template.Cliques {
		if template == nil {
			continue
		}
		templatesByName[template.Name] = template
		templateIndexes[template.Name] = i
	}

	groupedCliques := sets.New[string]()
	groupedPCSGs := make(map[int]map[string]grovecorev1alpha1.PodCliqueScalingGroup)

	for _, pcsg := range pcsgs {
		labelValue, exists := pcsg.Labels[apicommon.LabelPodCliqueSetReplicaIndex]
		if !exists {
			continue
		}
		replica, err := strconv.Atoi(labelValue)
		if err != nil {
			continue
		}
		if replica >= int(pcs.Spec.Replicas) {
			continue
		}
		if _, ok := groupedPCSGs[replica]; !ok {
			groupedPCSGs[replica] = make(map[string]grovecorev1alpha1.PodCliqueScalingGroup)
		}
		groupedPCSGs[replica][pcsg.Name] = pcsg
	}
	pcsgReplicaIndexes := slices.Sorted(maps.Keys(groupedPCSGs))

	for i, config := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		groupedCliques.Insert(config.CliqueNames...)

		// The last requested PCS index is the worst case for configured names.
		allErrs = append(allErrs, validateConfiguredScalingGroup(
			pcsNameReplica,
			config,
			nil,
			i,
			templatesByName,
			templateIndexes,
			templatePath,
		)...)

		for _, replica := range pcsgReplicaIndexes {
			pcsNameReplica := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replica}
			pcsg, ok := groupedPCSGs[replica][apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, config.Name)]
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

// ValidatePodCliqueReplicas validates the relationships and generated names
// affected by the concrete replica count of a PodClique.
func ValidatePodCliqueReplicas(pclq *grovecorev1alpha1.PodClique, template *grovecorev1alpha1.PodCliqueTemplateSpec) field.ErrorList {
	var allErrs field.ErrorList

	minAvailable := ptr.Deref(pclq.Spec.MinAvailable, pclq.Spec.Replicas)
	if minAvailable > pclq.Spec.Replicas {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("replicas"), pclq.Spec.Replicas, "replicas must not be less than minAvailable"))
	}

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

// ValidatePodCliqueScalingGroupReplicas validates the relationships and
// generated descendant names affected by the concrete replica count of a PCSG.
func ValidatePodCliqueScalingGroupReplicas(
	pcsg *grovecorev1alpha1.PodCliqueScalingGroup,
	pcs *grovecorev1alpha1.PodCliqueSet,
	config *grovecorev1alpha1.PodCliqueScalingGroupConfig,
) field.ErrorList {
	var allErrs field.ErrorList

	allErrs = append(allErrs, validateResourceClaimReferenceNames(
		pcsg.Name,
		pcsg.Spec.Replicas,
		resourceclaim.ResourceSharersFromPCSG(config.ResourceSharing),
		field.NewPath("resourceSharing"),
	)...)

	minAvailable := ptr.Deref(pcsg.Spec.MinAvailable, 1)
	if minAvailable > pcsg.Spec.Replicas {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("replicas"), pcsg.Spec.Replicas, "replicas must not be less than minAvailable"))
	}

	if pcsg.Spec.Replicas <= 0 {
		return allErrs
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
	config grovecorev1alpha1.PodCliqueScalingGroupConfig,
	pcsg *grovecorev1alpha1.PodCliqueScalingGroup,
	configIndex int,
	templatesByName map[string]*grovecorev1alpha1.PodCliqueTemplateSpec,
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
	template *grovecorev1alpha1.PodCliqueTemplateSpec,
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
	var allErrs field.ErrorList
	for i := range refs {
		ref := refs[i].GetBase()
		if err := validateResourceClaimReferenceName(ownerName, replicas, ref); err != nil {
			allErrs = append(allErrs, field.Invalid(refsPath.Index(i).Child("name"), ref.Name, err.Error()))
		}
	}
	return allErrs
}
