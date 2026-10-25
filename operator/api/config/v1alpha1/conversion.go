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

package v1alpha1

import (
	"github.com/ai-dynamo/grove/operator/api/config/v1beta1"

	"k8s.io/apimachinery/pkg/conversion"
)

// Convert_v1alpha1_LeaderElectionConfiguration_To_v1beta1_LeaderElectionConfiguration drops ResourceNamespace,
// which the operator never used: the leader election lock is always created in the operator's namespace.
func Convert_v1alpha1_LeaderElectionConfiguration_To_v1beta1_LeaderElectionConfiguration(in *LeaderElectionConfiguration, out *v1beta1.LeaderElectionConfiguration, s conversion.Scope) error {
	return autoConvert_v1alpha1_LeaderElectionConfiguration_To_v1beta1_LeaderElectionConfiguration(in, out, s)
}
