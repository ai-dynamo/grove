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

package components

import (
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

// SchedulerType represents the type of scheduler being used
type SchedulerType string

const (
	// SchedulerTypeDefault represents the default Kubernetes scheduler (kube-scheduler)
	SchedulerTypeDefault SchedulerType = "default"
	// SchedulerTypeGrove represents the Grove custom scheduler
	SchedulerTypeGrove SchedulerType = "grove"
)

// DetectSchedulerType determines which scheduler is being used for a PodCliqueSet.
// It checks the schedulerName field in PodSpec of all cliques:
// - If schedulerName is empty or "default-scheduler", it uses default kube-scheduler
// - Otherwise, it uses grove scheduler
func DetectSchedulerType(pcs *grovecorev1alpha1.PodCliqueSet) SchedulerType {
	// Check all cliques for scheduler name
	for _, clique := range pcs.Spec.Template.Cliques {
		schedulerName := clique.Spec.PodSpec.SchedulerName

		// If any clique explicitly specifies a non-default scheduler, use grove scheduler
		if schedulerName != "" && schedulerName != "default-scheduler" {
			return SchedulerTypeGrove
		}
	}

	// Default to using default-scheduler (kube-scheduler 1.35+)
	// This means we'll use the Workload API for gang scheduling
	return SchedulerTypeDefault
}

// ShouldUseWorkloadAPI returns true if we should use Kubernetes Workload API instead of PodGang API
func ShouldUseWorkloadAPI(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	return DetectSchedulerType(pcs) == SchedulerTypeDefault
}
