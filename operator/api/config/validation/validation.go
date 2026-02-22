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
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

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
	allErrs = append(allErrs, validateTopologyAwareSchedulingConfig(config.TopologyAwareScheduling, field.NewPath("topologyAwareScheduling"))...)
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
	defaultCount := 0
	seenNames := make(map[configv1alpha1.SchedulerName]struct{})
	for i, p := range scheduler.Profiles {
		idxPath := profilesPath.Index(i)
		if len(strings.TrimSpace(string(p.Name))) == 0 {
			allErrs = append(allErrs, field.Required(idxPath.Child("name"), "scheduler profile name is required"))
		} else if !slices.Contains(configv1alpha1.SupportedSchedulerNames, p.Name) {
			allErrs = append(allErrs, field.NotSupported(idxPath.Child("name"), p.Name, configv1alpha1.SupportedSchedulerNames))
		} else {
			if _, exists := seenNames[p.Name]; exists {
				allErrs = append(allErrs, field.Duplicate(idxPath.Child("name"), p.Name))
			}
			seenNames[p.Name] = struct{}{}
		}
		if p.Default {
			defaultCount++
		}
	}
	if defaultCount > 1 {
		allErrs = append(allErrs, field.Invalid(profilesPath, defaultCount, "at most one scheduler profile may have default: true"))
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
	allErrs = append(allErrs, validatePodCliqueScalingGroupConfiguration(controllerCfg.PodCliqueScalingGroup, fldPath.Child("podCliqueSet"))...)
	return allErrs
}

func validatePodCliqueSetControllerConfiguration(pcsCfg configv1alpha1.PodCliqueSetControllerConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateConcurrentSyncs(pcsCfg.ConcurrentSyncs, fldPath)...)
	return allErrs
}

func validatePodCliqueScalingGroupConfiguration(pcsgCfg configv1alpha1.PodCliqueScalingGroupControllerConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateConcurrentSyncs(pcsgCfg.ConcurrentSyncs, fldPath)...)
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

// validateTopologyAwareSchedulingConfig validates the cluster topology configuration.
// When cluster topology is enabled, it ensures the topology name and levels are provided,
// and validates domain and key uniqueness.
func validateTopologyAwareSchedulingConfig(clusterTopologyCfg configv1alpha1.TopologyAwareSchedulingConfiguration, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if !clusterTopologyCfg.Enabled {
		return allErrs
	}
	allErrs = validateClusterTopologyLevels(clusterTopologyCfg.Levels, fldPath.Child("levels"))
	return allErrs
}

func validateClusterTopologyLevels(levels []corev1alpha1.TopologyLevel, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if len(levels) == 0 {
		allErrs = append(allErrs, field.Required(fldPath, "levels are required when topology is enabled"))
	}
	allErrs = append(allErrs, mustHaveSupportedTopologyDomains(levels, fldPath)...)
	allErrs = append(allErrs, mustHaveUniqueTopologyLevels(levels, fldPath)...)
	return allErrs
}

func mustHaveSupportedTopologyDomains(levels []corev1alpha1.TopologyLevel, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	supportedDomains := corev1alpha1.SupportedTopologyDomains()
	for i, level := range levels {
		if !slices.Contains(supportedDomains, level.Domain) {
			allErrs = append(allErrs, field.Invalid(fldPath.Index(i).Child("domain"), level.Domain, fmt.Sprintf("must be one of %v", supportedDomains)))
		}
	}
	return allErrs
}

func mustHaveUniqueTopologyLevels(levels []corev1alpha1.TopologyLevel, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	seenDomains := make(map[corev1alpha1.TopologyDomain]struct{})
	seenKeys := make(map[string]struct{})
	for i, level := range levels {
		if _, exists := seenDomains[level.Domain]; exists {
			allErrs = append(allErrs, field.Duplicate(fldPath.Index(i).Child("domain"), level.Domain))
		}
		if _, exists := seenKeys[level.Key]; exists {
			allErrs = append(allErrs, field.Duplicate(fldPath.Index(i).Child("key"), level.Key))
		}
		seenDomains[level.Domain] = struct{}{}
		seenKeys[level.Key] = struct{}{}
	}
	return allErrs
}
