// /*
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
// */

package utils

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
	"strings"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
)

const maxLabelValueLength = 63

// ResolveSchedulerName resolves an empty scheduler name to the configured
// default and otherwise returns the registered backend's canonical name.
func ResolveSchedulerName(schedRegistry scheduler.Registry, schedulerName string) string {
	if schedRegistry == nil {
		return schedulerName
	}
	if backend := schedRegistry.GetOrDefault(schedulerName); backend != nil {
		return backend.Name()
	}
	return schedulerName
}

// SchedulerNamesForPodCliqueSet returns the distinct resolved scheduler names
// selected by the PodClique templates in stable order.
func SchedulerNamesForPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet, schedRegistry scheduler.Registry) []string {
	names := make(map[string]struct{}, len(pcs.Spec.Template.Cliques))
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique == nil {
			continue
		}
		name := ResolveSchedulerName(schedRegistry, clique.Spec.PodSpec.SchedulerName)
		names[name] = struct{}{}
	}
	return slices.Sorted(maps.Keys(names))
}

// GenerateSchedulerScopedPodGangName returns the legacy PodGang name for a
// single-scheduler PCS and a scheduler-qualified name for a disjoint PCS.
func GenerateSchedulerScopedPodGangName(baseName string, pcs *grovecorev1alpha1.PodCliqueSet, schedulerName string, schedRegistry scheduler.Registry) string {
	if len(SchedulerNamesForPodCliqueSet(pcs, schedRegistry)) <= 1 {
		return baseName
	}

	resolvedSchedulerName := ResolveSchedulerName(schedRegistry, schedulerName)
	candidate := fmt.Sprintf("%s-%s", baseName, resolvedSchedulerName)
	if len(candidate) <= maxLabelValueLength {
		return candidate
	}

	nameHash := sha256.Sum256([]byte(candidate))
	hashSuffix := fmt.Sprintf("-%x", nameHash[:4])
	maxBaseNameLength := maxLabelValueLength - len(hashSuffix)
	trimmedBaseName := strings.TrimRight(baseName[:min(len(baseName), maxBaseNameLength)], "-.")
	return trimmedBaseName + hashSuffix
}
