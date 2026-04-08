// /*
// Copyright 2024 The Grove Authors.
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

package validation

import (
	"fmt"
	"slices"
	"strings"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
)

// ValidateOperatorConfiguration validates the operator configuration.
func ValidateOperatorConfiguration(config *configv1alpha1.OperatorConfiguration) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateLogConfiguration(config)...)
	allErrs = append(allErrs, validateSchedulerConfiguration(&config.Scheduler, field.NewPath("scheduler"))...)
	allErrs = append(allErrs, validateLeaderElectionConfiguration(config.LeaderElection, field.NewPath("leaderElection"))...)
	allErrs = append(allErrs, validateClientConnectionConfiguration(config.ClientConnection, field.NewPath("clientConnection"))...)
	allErrs = append(allErrs, validateControllerConfiguration(config.Controllers, field.NewPath("controllers"))...)
	return allErrs
}

func validateLogConfiguration(config *configv1alpha1.OperatorConfiguration) field.ErrorList {
	allErrs := field.ErrorList{}
	if len(strings.TrimSpace(string(config.LogLevel))) > 0 && !sets.New(configv1alpha1.AllLogLevels...).Has(config.LogLevel) {
		allErrs = append(allErrs, field.NotSupported(field.NewPath("logLevel"), config.LogLevel, configv1alpha1.AllLogLevels))
	}
	if len(strings.TrimSpace(string(config.LogFormat))) > 0 && !sets.New(configv1alpha1.AllLogFormats...).Has(config.LogFormat) {
		allErrs = append(allErrs, field.NotSupported(field.NewPath("logFormat"), config.LogFormat, configv1alpha1.AllLogFormats))
	}
	return allErrs
}

func validateSchedulerConfiguration(scheduler *configv1alpha1.SchedulerConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	profilesPath := fldPath.Child("profiles")
	defaultProfileNamePath := fldPath.Child("defaultProfileName")
	if len(scheduler.Profiles) == 0 {
		allErrs = append(allErrs, field.Required(profilesPath, "at least one scheduler profile is required"))
	}
	seenNames := sets.New[configv1alpha1.SchedulerName]()
	for i, p := range scheduler.Profiles {
		idxPath := profilesPath.Index(i)
		if len(strings.TrimSpace(string(p.Name))) == 0 {
			allErrs = append(allErrs, field.Required(idxPath.Child("name"), "scheduler profile name is required"))
		} else if !slices.Contains(configv1alpha1.SupportedSchedulerNames, p.Name) {
			allErrs = append(allErrs, field.NotSupported(idxPath.Child("name"), p.Name, configv1alpha1.SupportedSchedulerNames))
		} else {
			if seenNames.Has(p.Name) {
				allErrs = append(allErrs, field.Duplicate(idxPath.Child("name"), p.Name))
			}
			seenNames.Insert(p.Name)
		}
	}
	if !seenNames.Has(configv1alpha1.SchedulerNameKube) {
		allErrs = append(allErrs, field.Required(profilesPath, fmt.Sprintf("the %q scheduler profile is required", configv1alpha1.SchedulerNameKube)))
	}
	if strings.TrimSpace(scheduler.DefaultProfileName) == "" {
		allErrs = append(allErrs, field.Required(defaultProfileNamePath, "default scheduler profile name is required"))
	} else if !seenNames.Has(configv1alpha1.SchedulerName(scheduler.DefaultProfileName)) {
		allErrs = append(allErrs, field.Invalid(defaultProfileNamePath, scheduler.DefaultProfileName, "default profile must be one of the configured profiles"))
	}
	return allErrs
}

func validateLeaderElectionConfiguration(cfg configv1alpha1.LeaderElectionConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if !cfg.Enabled {
		return allErrs
	}
	allErrs = append(allErrs, mustBeGreaterThanZeroDuration(cfg.LeaseDuration, fldPath.Child("leaseDuration"))...)
	allErrs = append(allErrs, mustBeGreaterThanZeroDuration(cfg.RenewDeadline, fldPath.Child("renewDeadline"))...)
	allErrs = append(allErrs, mustBeGreaterThanZeroDuration(cfg.RetryPeriod, fldPath.Child("retryPeriod"))...)

	if cfg.LeaseDuration.Duration <= cfg.RenewDeadline.Duration {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("leaseDuration"), cfg.RenewDeadline, "LeaseDuration must be greater than RenewDeadline"))
	}
	if len(cfg.ResourceLock) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("resourceLock"), "resourceLock is required"))
	}
	if len(cfg.ResourceName) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("resourceName"), "resourceName is required"))
	}
	return allErrs
}

func validateClientConnectionConfiguration(cfg configv1alpha1.ClientConnectionConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if cfg.Burst < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("burst"), cfg.Burst, "must be non-negative"))
	}
	return allErrs
}

func validateControllerConfiguration(controllerCfg configv1alpha1.ControllerConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validatePodCliqueSetControllerConfiguration(controllerCfg.PodCliqueSet, fldPath.Child("podCliqueSet"))...)
	allErrs = append(allErrs, validatePodCliqueScalingGroupConfiguration(controllerCfg.PodCliqueScalingGroup, fldPath.Child("podCliqueScalingGroup"))...)
	allErrs = append(allErrs, validatePodCliqueControllerConfiguration(controllerCfg.PodClique, fldPath.Child("podClique"))...)
	allErrs = append(allErrs, validatePodGangControllerConfiguration(controllerCfg.PodGang, fldPath.Child("podGang"))...)
	return allErrs
}

func validatePodCliqueSetControllerConfiguration(pcsControllerCfg configv1alpha1.PodCliqueSetControllerConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateConcurrentSyncs(pcsControllerCfg.ConcurrentSyncs, fldPath)...)
	return allErrs
}

func validatePodCliqueScalingGroupConfiguration(pcsgControllerCfg configv1alpha1.PodCliqueScalingGroupControllerConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateConcurrentSyncs(pcsgControllerCfg.ConcurrentSyncs, fldPath)...)
	return allErrs
}

func validatePodCliqueControllerConfiguration(pclqControllerConfig configv1alpha1.PodCliqueControllerConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateConcurrentSyncs(pclqControllerConfig.ConcurrentSyncs, fldPath)...)
	return allErrs
}

func validatePodGangControllerConfiguration(pgControllerCfg configv1alpha1.PodGangControllerConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateConcurrentSyncs(pgControllerCfg.ConcurrentSyncs, fldPath)...)
	return allErrs
}

func validateConcurrentSyncs(concurrentSyncs *int, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if ptr.Deref(concurrentSyncs, 0) <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("concurrentSyncs"), concurrentSyncs, "must be greater than 0"))
	}
	return allErrs
}

func mustBeGreaterThanZeroDuration(duration metav1.Duration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if duration.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, duration, "must be greater than 0"))
	}
	return allErrs
}
