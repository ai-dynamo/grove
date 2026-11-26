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

// Package crds returns the grove.io CRDs.
//
// Deprecated: use github.com/ai-dynamo/grove/operator/api/core/crds, whose CRDs serve every grove.io version.
// This package returns the same CRDs.
package crds

import "github.com/ai-dynamo/grove/operator/api/core/crds"

// PodCliqueCRD returns the PodClique CRD
//
// Deprecated: use crds.PodCliqueCRD of github.com/ai-dynamo/grove/operator/api/core/crds.
func PodCliqueCRD() string {
	return crds.PodCliqueCRD()
}

// PodCliqueSetCRD returns the PodCliqueSet CRD
//
// Deprecated: use crds.PodCliqueSetCRD of github.com/ai-dynamo/grove/operator/api/core/crds.
func PodCliqueSetCRD() string {
	return crds.PodCliqueSetCRD()
}

// ClusterTopologyCRD returns the ClusterTopologyBinding CRD
//
// Deprecated: use crds.ClusterTopologyCRD of github.com/ai-dynamo/grove/operator/api/core/crds.
func ClusterTopologyCRD() string {
	return crds.ClusterTopologyCRD()
}

// PodCliqueScalingGroupCRD returns the PodCliqueScalingGroup CRD
//
// Deprecated: use crds.PodCliqueScalingGroupCRD of github.com/ai-dynamo/grove/operator/api/core/crds.
func PodCliqueScalingGroupCRD() string {
	return crds.PodCliqueScalingGroupCRD()
}

// PodGangMapCRD returns the PodGangMap CRD
//
// Deprecated: use crds.PodGangMapCRD of github.com/ai-dynamo/grove/operator/api/core/crds.
func PodGangMapCRD() string {
	return crds.PodGangMapCRD()
}
