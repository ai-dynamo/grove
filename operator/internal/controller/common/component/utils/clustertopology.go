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

package utils

import (
	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

// GetTopologyName extracts the topology name from a PodCliqueSet's labels.
// Returns empty string if the label doesn't exist or is empty.
func GetTopologyName(pcs *grovecorev1alpha1.PodCliqueSet) string {
	if pcs == nil {
		return ""
	}
	topology, exists := pcs.Labels[apicommon.LabelClusterTopologyName]
	if !exists || topology == "" {
		return ""
	}
	return topology
}
