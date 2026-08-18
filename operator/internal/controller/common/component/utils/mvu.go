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
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

// MVUTemplate describes the composition of one Minimum Viable Unit (MVU) PodGang.
// It specifies the minimum number of pods per standalone PodClique and the minimum
// number of replicas per PodCliqueScalingGroup that must be gang-scheduled together.
type MVUTemplate struct {
	// StandalonePCLQs maps standalone PodClique name to the minAvailable pod count.
	StandalonePCLQs map[string]int32
	// PCSGs maps PodCliqueScalingGroup name to the minAvailable replica count.
	PCSGs map[string]int32
}

// ComputeMVUTemplateFromPCSTemplateSpec computes the MVU template for a PCS from its spec.
func ComputeMVUTemplateFromPCSTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet) MVUTemplate {
	return MVUTemplate{
		StandalonePCLQs: GetStandalonePCLQMinAvailableFromPCSTemplateSpec(pcs),
		PCSGs:           GetPCSGMinAvailableFromPCSTemplateSpec(pcs),
	}
}
