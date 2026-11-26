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

// Convert_v1beta1_OperatorConfiguration_To_v1alpha1_OperatorConfiguration converts a v1beta1 OperatorConfiguration
// to v1alpha1. The scale-in configuration has no v1alpha1 counterpart and is dropped: a v1alpha1 configuration reads
// no scale-in cost.
func Convert_v1beta1_OperatorConfiguration_To_v1alpha1_OperatorConfiguration(in *v1beta1.OperatorConfiguration, out *OperatorConfiguration, s conversion.Scope) error {
	return autoConvert_v1beta1_OperatorConfiguration_To_v1alpha1_OperatorConfiguration(in, out, s)
}

// Convert_v1alpha1_OperatorConfiguration_To_v1beta1_OperatorConfiguration converts a v1alpha1 OperatorConfiguration
// to v1beta1. A v1alpha1 configuration cannot name a scale-in cost source, so the converted configuration reads none,
// the v1beta1 default. Defaulting runs on the decoded version only, so the conversion sets it explicitly.
func Convert_v1alpha1_OperatorConfiguration_To_v1beta1_OperatorConfiguration(in *OperatorConfiguration, out *v1beta1.OperatorConfiguration, s conversion.Scope) error {
	if err := autoConvert_v1alpha1_OperatorConfiguration_To_v1beta1_OperatorConfiguration(in, out, s); err != nil {
		return err
	}
	out.ScaleIn.CostSource = v1beta1.ScaleInCostSourceNone
	return nil
}
