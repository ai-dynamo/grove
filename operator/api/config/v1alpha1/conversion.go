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
	v1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"

	conversion "k8s.io/apimachinery/pkg/conversion"
)

// Convert_v1beta1_KueueSchedulerConfiguration_To_v1alpha1_KueueSchedulerConfiguration drops
// EnablePodSetGroupTopology, which exists only in v1beta1.
func Convert_v1beta1_KueueSchedulerConfiguration_To_v1alpha1_KueueSchedulerConfiguration(in *v1beta1.KueueSchedulerConfiguration, out *KueueSchedulerConfiguration, s conversion.Scope) error {
	return autoConvert_v1beta1_KueueSchedulerConfiguration_To_v1alpha1_KueueSchedulerConfiguration(in, out, s)
}
