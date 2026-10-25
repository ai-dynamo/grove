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
	"github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"k8s.io/apimachinery/pkg/conversion"
)

// Convert_v1alpha1_TopologyConstraint_To_v1beta1_TopologyConstraint converts the deprecated packDomain into
// pack.required, which v1beta1 has in its place. A pack.required that is already set takes precedence.
func Convert_v1alpha1_TopologyConstraint_To_v1beta1_TopologyConstraint(in *TopologyConstraint, out *v1beta1.TopologyConstraint, s conversion.Scope) error {
	if err := autoConvert_v1alpha1_TopologyConstraint_To_v1beta1_TopologyConstraint(in, out, s); err != nil {
		return err
	}
	if in.PackDomain == "" {
		return nil
	}
	if out.Pack == nil {
		out.Pack = &v1beta1.TopologyPackConstraint{}
	}
	if out.Pack.RequiredDomain == "" {
		out.Pack.RequiredDomain = v1beta1.TopologyDomain(in.PackDomain)
	}
	return nil
}

// Convert_v1beta1_TopologyConstraint_To_v1alpha1_TopologyConstraint converts a v1beta1 TopologyConstraint. The
// deprecated packDomain is never set, so a v1alpha1 object that used it reads back with pack.required instead.
func Convert_v1beta1_TopologyConstraint_To_v1alpha1_TopologyConstraint(in *v1beta1.TopologyConstraint, out *TopologyConstraint, s conversion.Scope) error {
	return autoConvert_v1beta1_TopologyConstraint_To_v1alpha1_TopologyConstraint(in, out, s)
}

// Convert_Pointer_v1alpha1_PodCliqueTemplateSpec_To_Pointer_v1beta1_PodCliqueTemplateSpec converts an element of
// the template's clique list.
func Convert_Pointer_v1alpha1_PodCliqueTemplateSpec_To_Pointer_v1beta1_PodCliqueTemplateSpec(in **PodCliqueTemplateSpec, out **v1beta1.PodCliqueTemplateSpec, s conversion.Scope) error {
	if *in == nil {
		*out = nil
		return nil
	}
	*out = new(v1beta1.PodCliqueTemplateSpec)
	return Convert_v1alpha1_PodCliqueTemplateSpec_To_v1beta1_PodCliqueTemplateSpec(*in, *out, s)
}

// Convert_Pointer_v1beta1_PodCliqueTemplateSpec_To_Pointer_v1alpha1_PodCliqueTemplateSpec converts an element of
// the template's clique list.
func Convert_Pointer_v1beta1_PodCliqueTemplateSpec_To_Pointer_v1alpha1_PodCliqueTemplateSpec(in **v1beta1.PodCliqueTemplateSpec, out **PodCliqueTemplateSpec, s conversion.Scope) error {
	if *in == nil {
		*out = nil
		return nil
	}
	*out = new(PodCliqueTemplateSpec)
	return Convert_v1beta1_PodCliqueTemplateSpec_To_v1alpha1_PodCliqueTemplateSpec(*in, *out, s)
}
